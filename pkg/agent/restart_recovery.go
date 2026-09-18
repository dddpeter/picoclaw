// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// Restart recovery (fork feature, docs/design/long-task-execution.zh.md §3):
// after a gateway restart, sessions whose turn was interrupted mid-tool-loop
// carry dangling trailing tool calls — the next LLM request would 400, and
// the user has no idea a task was left unfinished. RunRestartRecovery seals
// those sessions (with a restart-semantics note), notifies their users that
// replying "继续" resumes the task, and re-reminds until the user shows up
// (bounded by reminder_max). Best effort: async at startup, failures logged.

const restartRecoveryNotice = "⚠ 检测到上次任务被中断（网关重启）。会话已封口保留，回复「继续」可让模型接着做。"

// recoveryReminder tracks one session's pending re-reminder. Entries are
// kept (not deleted) once exhausted so recoveryReminderCount reports the
// final tally; cancelRecoveryReminder deletes them outright.
type recoveryReminder struct {
	sessionKey string
	channel    string
	chatID     string
	nextAt     time.Time
	interval   time.Duration
	sent       int
	max        int
}

// RunRestartRecovery scans all sessions, seals interrupted ones, sends the
// first notification, then (optionally) loops re-reminding until the user
// sends any message to a reminded session. Blocks until ctx is done when
// reminders are enabled; callers run it in a goroutine.
func (al *AgentLoop) RunRestartRecovery(ctx context.Context) {
	cfg := al.GetConfig().Agents.Defaults.RestartRecovery
	if !cfg.IsEnabled() {
		return
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.Sessions == nil {
		return
	}

	reminderInterval := time.Duration(cfg.ReminderIntervalMinutes) * time.Minute
	reminderMax := cfg.ReminderMax
	for _, key := range agent.Sessions.ListSessions() {
		if ctx.Err() != nil {
			return
		}
		al.recoverOneSession(ctx, agent, key, cfg, reminderInterval, reminderMax)
	}

	if reminderInterval > 0 && reminderMax > 0 && ctx.Err() == nil {
		al.runRecoveryReminderLoop(ctx, time.Minute)
	}
}

func (al *AgentLoop) recoverOneSession(
	ctx context.Context,
	agent *AgentInstance,
	key string,
	cfg config.RestartRecoveryConfig,
	reminderInterval time.Duration,
	reminderMax int,
) {
	history := agent.Sessions.GetHistory(key)
	sealed := sealDanglingWith(history, restartToolResultNote)
	if len(sealed) == len(history) {
		return // clean tail (or already sealed) — nothing to recover
	}
	agent.Sessions.SetHistory(key, sealed)
	logger.InfoCF("agent", "restart recovery: sealed interrupted session",
		map[string]any{"session_key": key, "sealed_calls": len(sealed) - len(history)})

	if cfg.NotifyWindowHours <= 0 || ctx.Err() != nil {
		return // notifications disabled
	}
	if !al.sessionActiveWithin(agent, key, time.Duration(cfg.NotifyWindowHours)*time.Hour) {
		return // stale interruption — sealed silently
	}
	channel, chatID := outboundTargetForSession(agent, key)
	if channel == "" || chatID == "" {
		return // no resolvable target (internal/unknown sessions stay silent)
	}
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel:    channel,
		ChatID:     chatID,
		AgentID:    agent.ID,
		SessionKey: key,
		Content:    restartRecoveryNotice,
	}); err != nil {
		logger.WarnCF("agent", "restart recovery: failed to notify",
			map[string]any{"session_key": key, "error": err.Error()})
		return
	}
	if reminderInterval > 0 && reminderMax > 0 {
		al.recoveryReminders.Store(key, &recoveryReminder{
			sessionKey: key,
			channel:    channel,
			chatID:     chatID,
			nextAt:     time.Now().Add(reminderInterval),
			interval:   reminderInterval,
			max:        reminderMax,
		})
	}
}

// runRecoveryReminderLoop polls pending reminders every tick and re-sends
// the notice for due ones (until reminder_max is reached). Any user message
// to the session cancels it (see cancelRecoveryReminder).
func (al *AgentLoop) runRecoveryReminderLoop(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = time.Minute
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			al.recoveryReminders.Range(func(k, v any) bool {
				reminder, ok := v.(*recoveryReminder)
				if !ok || reminder.sent >= reminder.max || now.Before(reminder.nextAt) {
					return true
				}
				reminder.sent++
				reminder.nextAt = now.Add(reminder.interval)
				logger.InfoCF("agent", "restart recovery: re-reminding interrupted session",
					map[string]any{"session_key": reminder.sessionKey, "sent": reminder.sent, "max": reminder.max})
				if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel:    reminder.channel,
					ChatID:     reminder.chatID,
					SessionKey: reminder.sessionKey,
					Content:    restartRecoveryNotice,
				}); err != nil {
					logger.WarnCF("agent", "restart recovery: re-reminder failed",
						map[string]any{"session_key": reminder.sessionKey, "error": err.Error()})
				}
				return true
			})
		}
	}
}

// cancelRecoveryReminder drops a session's pending reminder — called when
// any user message lands in that session, including the "继续" resume itself.
func (al *AgentLoop) cancelRecoveryReminder(sessionKey string) {
	al.recoveryReminders.Delete(sessionKey)
}

// sessionActiveWithin reports whether the session's on-disk record changed
// within window. Stores that cannot answer (no LastModified support) are
// treated as active so notifications are not silently suppressed.
func (al *AgentLoop) sessionActiveWithin(agent *AgentInstance, key string, window time.Duration) bool {
	lm, ok := agent.Sessions.(interface {
		LastModified(sessionKey string) (time.Time, bool)
	})
	if !ok {
		return true
	}
	mod, exists := lm.LastModified(key)
	if !exists {
		return false
	}
	return time.Since(mod) <= window
}

// outboundTargetForSession resolves (channel, chatID) for proactive outbound
// from a session's stored scope: Values["chat"] is "<chatType>:<chatID>"
// (see pkg/session/allocator.go buildSessionScope).
func outboundTargetForSession(agent *AgentInstance, key string) (string, string) {
	metaStore, ok := agent.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		return "", ""
	}
	scope := metaStore.GetSessionScope(key)
	if scope == nil {
		return "", ""
	}
	channel := strings.ToLower(strings.TrimSpace(scope.Channel))
	v := strings.TrimSpace(scope.Values["chat"])
	if channel == "" || v == "" {
		return "", ""
	}
	if idx := strings.Index(v, ":"); idx >= 0 {
		v = v[idx+1:]
	}
	return channel, v
}

// --- test hooks (production code must not call these) ---

// registerRecoveryReminderForTest injects a reminder with overridden timing.
func (al *AgentLoop) registerRecoveryReminderForTest(sessionKey, channel, chatID string, interval time.Duration, max int) *recoveryReminder {
	r := &recoveryReminder{
		sessionKey: sessionKey,
		channel:    channel,
		chatID:     chatID,
		nextAt:     time.Now().Add(interval),
		interval:   interval,
		max:        max,
	}
	al.recoveryReminders.Store(sessionKey, r)
	return r
}

// recoveryReminderCount reports how many reminders were sent for a session.
func (al *AgentLoop) recoveryReminderCount(sessionKey string) int {
	v, ok := al.recoveryReminders.Load(sessionKey)
	if !ok {
		return 0
	}
	if r, ok := v.(*recoveryReminder); ok {
		return r.sent
	}
	return 0
}

// sealDanglingToolCallsForRestart is a thin alias used by tests to exercise
// restart sealing directly.
func sealDanglingToolCallsForRestart(history []providers.Message) []providers.Message {
	return sealDanglingWith(history, restartToolResultNote)
}
