// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"strings"
	"sync"
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
//
// Interruptions that leave a CLEAN tail (killed mid-LLM-generation, before
// any message of the turn persisted) are caught by the turn-in-flight marker
// instead: runAgentLoop write-ahead records the marker before a turn starts
// and clears it on every exit path, so a surviving marker means the process
// died mid-turn. The marker also carries the model the turn was using, which
// recovery restores — /switch state is in-memory only and would otherwise be
// lost, silently resuming the task on the config default model.

const restartRecoveryNoticeSealed = "⚠ 检测到上次任务被中断（网关重启）。会话已封口保留，回复「继续」可让模型接着做。"

const restartRecoveryNoticeUnsealed = "⚠ 检测到上次任务被中断（网关重启），回答未能完成。回复「继续」可让模型接着做。"

// recoveryReminder tracks one session's pending re-reminder. Entries are
// kept (not deleted) once exhausted so recoveryReminderCount reports the
// final tally; cancelRecoveryReminder deletes them outright. The mu-guarded
// accessors keep the mutable fields race-free: they are written by the
// reminder-loop goroutine and read from other goroutines.
type recoveryReminder struct {
	mu         sync.Mutex
	sessionKey string
	agentID    string
	channel    string
	chatID     string
	content    string // notice text; sealed and unsealed interruptions word it differently
	interval   time.Duration
	max        int
	nextAt     time.Time // guarded by mu
	sent       int       // guarded by mu
}

func (r *recoveryReminder) due(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent < r.max && !now.Before(r.nextAt)
}

// fire records a send and schedules the next one; returns the new count.
func (r *recoveryReminder) fire(now time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent++
	r.nextAt = now.Add(r.interval)
	return r.sent
}

func (r *recoveryReminder) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// exhausted reports whether this reminder will never fire again (used by
// the loop to decide it can exit once nothing is pending).
func (r *recoveryReminder) exhausted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent >= r.max
}

// RunRestartRecovery scans every agent's sessions (agents.list deployments
// keep one store per agent — the default agent alone misses the rest), seals
// interrupted ones, sends the first notification, then (optionally) loops
// re-reminding until the user sends any message to a reminded session.
// Blocks until ctx is done when reminders are enabled; callers run it in a
// goroutine.
func (al *AgentLoop) RunRestartRecovery(ctx context.Context) {
	cfg := al.GetConfig().Agents.Defaults.RestartRecovery
	if !cfg.IsEnabled() {
		return
	}
	recoveryStart := time.Now()

	// Dedupe by store instance: agents sharing a workspace hold distinct
	// store objects over the same directory; the idempotent seal and the
	// one-shot marker consumption make the double scan harmless, but
	// skipping repeats avoids duplicate work.
	reminderInterval := time.Duration(cfg.ReminderIntervalMinutes) * time.Minute
	reminderMax := cfg.ReminderMax
	seenStores := make(map[any]bool)
	for _, agent := range al.recoveryAgents() {
		if agent == nil || agent.Sessions == nil || seenStores[agent.Sessions] {
			continue
		}
		seenStores[agent.Sessions] = true
		markers := al.loadInterruptedTurns(agent, recoveryStart)
		for _, key := range sessionKeysUnion(agent.Sessions.ListSessions(), markers) {
			if ctx.Err() != nil {
				return
			}
			marker, hasMarker := markers[key]
			al.recoverOneSession(ctx, agent, key, marker, hasMarker, cfg, reminderInterval, reminderMax, recoveryStart)
		}
	}

	if reminderInterval > 0 && reminderMax > 0 && ctx.Err() == nil {
		al.runRecoveryReminderLoop(ctx, time.Minute)
	}
}

// loadInterruptedTurns snapshots the surviving turn markers of one store,
// keeping only leftovers from a previous process. Markers written after
// recovery started, or whose session currently has an active turn, belong
// to a live turn of THIS process and are left untouched.
func (al *AgentLoop) loadInterruptedTurns(agent *AgentInstance, recoveryStart time.Time) map[string]session.InflightTurn {
	markerStore, ok := agent.Sessions.(session.InflightTurnStore)
	if !ok {
		return nil
	}
	markers := make(map[string]session.InflightTurn)
	for _, m := range markerStore.ListTurnsInFlight() {
		if m.StartedAt.After(recoveryStart) {
			continue
		}
		if al.getActiveTurnState(m.SessionKey) != nil {
			continue
		}
		markers[m.SessionKey] = m
	}
	return markers
}

// sessionKeysUnion merges the store's session list with marker keys so
// recovery also reaches sessions whose marker survived but whose history
// was never created (or has since been removed).
func sessionKeysUnion(keys []string, markers map[string]session.InflightTurn) []string {
	seen := make(map[string]bool, len(keys)+len(markers))
	merged := make([]string, 0, len(keys)+len(markers))
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			merged = append(merged, k)
		}
	}
	for k := range markers {
		if !seen[k] {
			seen[k] = true
			merged = append(merged, k)
		}
	}
	return merged
}

// recoveryAgents returns every registered agent plus the default one
// (which may be absent from the registry in edge configurations).
func (al *AgentLoop) recoveryAgents() []*AgentInstance {
	registry := al.GetRegistry()
	if registry == nil {
		return nil
	}
	ids := registry.ListAgentIDs()
	agents := make([]*AgentInstance, 0, len(ids)+1)
	for _, id := range ids {
		if agent, ok := registry.GetAgent(id); ok && agent != nil {
			agents = append(agents, agent)
		}
	}
	if def := registry.GetDefaultAgent(); def != nil {
		agents = append(agents, def)
	}
	return agents
}

func (al *AgentLoop) recoverOneSession(
	ctx context.Context,
	agent *AgentInstance,
	key string,
	marker session.InflightTurn,
	hasMarker bool,
	cfg config.RestartRecoveryConfig,
	reminderInterval time.Duration,
	reminderMax int,
	recoveryStart time.Time,
) {
	// A turn is running in this session right now — any marker on disk is
	// its write-ahead record, not an interruption leftover.
	if al.getActiveTurnState(key) != nil {
		return
	}

	history := agent.Sessions.GetHistory(key)
	sealed := sealDanglingWith(history, restartToolResultNote)
	sealedNow := len(sealed) != len(history)

	if !sealedNow && !hasMarker {
		return // clean tail, no marker — nothing to recover
	}

	// The marker is consumed below on every path past this point (deferred),
	// so a second store over the same directory cannot re-notify.
	if hasMarker {
		defer al.clearInflightMarker(agent, key, recoveryStart)
		// Model continuity first: replying "继续" should continue on the
		// model the interrupted task was using, not the config default.
		al.restoreInterruptedModel(agent, marker)
	}

	// A marker with no durable history: the turn died before its first
	// message persisted — there is nothing to resume and nothing to report.
	if len(history) == 0 {
		return
	}

	// Decide the notification-window verdict BEFORE sealing: SetHistory
	// rewrites the jsonl file and refreshes its mtime, so a check made
	// after the write would see every sealed session as "just active"
	// and notify_window_hours would never suppress anything.
	notify := cfg.NotifyWindowHours > 0 && ctx.Err() == nil &&
		al.sessionActiveWithin(agent, key, time.Duration(cfg.NotifyWindowHours)*time.Hour)

	if sealedNow {
		agent.Sessions.SetHistory(key, sealed)
		logger.InfoCF("agent", "restart recovery: sealed interrupted session",
			map[string]any{"session_key": key, "sealed_calls": len(sealed) - len(history)})
	} else if historyTailIsFinalAnswer(history) {
		// Marker present but the tail is a persisted final answer: the turn
		// finished before the process died and the marker is merely a stale
		// write-ahead record — nothing to report.
		return
	}

	if !notify {
		return // notifications disabled or stale interruption — sealed silently
	}
	channel, chatID := outboundTargetForSession(agent, key)
	if channel == "" || chatID == "" {
		return // no resolvable target (internal/unknown sessions stay silent)
	}
	notice := restartRecoveryNoticeSealed
	if !sealedNow {
		notice = restartRecoveryNoticeUnsealed
	}
	// The marker's agent owns the session; fall back to the scanning agent
	// for marker-less (dangling-only) interruptions.
	outboundAgentID := agent.ID
	if hasMarker && marker.AgentID != "" {
		outboundAgentID = marker.AgentID
	}
	if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
		Channel:    channel,
		ChatID:     chatID,
		AgentID:    outboundAgentID,
		SessionKey: key,
		Content:    notice,
	}); err != nil {
		logger.WarnCF("agent", "restart recovery: failed to notify",
			map[string]any{"session_key": key, "error": err.Error()})
		return
	}
	if reminderInterval > 0 && reminderMax > 0 {
		al.recoveryReminders.Store(key, &recoveryReminder{
			sessionKey: key,
			agentID:    outboundAgentID,
			channel:    channel,
			chatID:     chatID,
			content:    notice,
			nextAt:     time.Now().Add(reminderInterval),
			interval:   reminderInterval,
			max:        reminderMax,
		})
	}
}

// restoreInterruptedModel best-effort restores the model the interrupted
// turn was using. /switch state lives in memory only, so without this a
// restart silently resumes interrupted tasks on the config default model.
// Mirrors /switch discipline: TryLock, never block on the model-state write
// lock — recovery must not hang behind an in-flight turn.
func (al *AgentLoop) restoreInterruptedModel(agent *AgentInstance, marker session.InflightTurn) {
	if marker.Model == "" {
		return
	}
	if marker.AgentID != "" && marker.AgentID != agent.ID {
		return
	}
	modelMu := agent.modelStateMutex()
	modelMu.RLock()
	current := agent.Model
	modelMu.RUnlock()
	if current == marker.Model {
		return
	}
	cfg := al.GetConfig()
	modelFound := false
	for _, modelCfg := range cfg.ModelList {
		if modelCfg != nil && modelCfg.ModelName == marker.Model {
			modelFound = true
			break
		}
	}
	if !modelFound {
		logger.WarnCF("agent", "restart recovery: interrupted turn's model no longer configured; keeping current model",
			map[string]any{"model": marker.Model, "session_key": marker.SessionKey})
		return
	}
	if !modelMu.TryLock() {
		logger.InfoCF("agent", "restart recovery: model restore skipped, a task is currently running",
			map[string]any{"model": marker.Model, "session_key": marker.SessionKey})
		return
	}
	defer modelMu.Unlock()
	if agent.Model == marker.Model {
		return
	}
	if _, err := al.swapAgentModelLocked(cfg, agent, marker.Model); err != nil {
		logger.WarnCF("agent", "restart recovery: model restore failed",
			map[string]any{"model": marker.Model, "session_key": marker.SessionKey, "error": err.Error()})
		return
	}
	logger.InfoCF("agent", "restart recovery: restored interrupted turn's model",
		map[string]any{"model": marker.Model, "previous_model": current, "session_key": marker.SessionKey})
}

// clearInflightMarker consumes the session's turn marker (one-shot: a second
// store scanning the same directory must not re-notify). Before deleting it
// re-checks the on-disk marker: a turn that started while recovery was
// processing this session has since rewritten the marker, and that live
// write-ahead record must survive for its own exit path (and a future
// recovery) — only the stale, pre-recovery marker is consumed.
func (al *AgentLoop) clearInflightMarker(agent *AgentInstance, key string, recoveryStart time.Time) {
	markerStore, ok := agent.Sessions.(session.InflightTurnStore)
	if !ok {
		return
	}
	for _, current := range markerStore.ListTurnsInFlight() {
		if current.SessionKey == key {
			if !current.StartedAt.After(recoveryStart) {
				markerStore.ClearTurnInFlight(key)
			}
			return
		}
	}
}

// historyTailIsFinalAnswer reports whether the history tail looks like a
// finished turn: a trailing assistant message without tool calls is the
// persisted final answer. Anything else (a user message awaiting its reply,
// a tool result mid-loop) means the turn was still in flight when the
// process died.
func historyTailIsFinalAnswer(history []providers.Message) bool {
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	return last.Role == "assistant" && len(last.ToolCalls) == 0
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
			pending := 0
			al.recoveryReminders.Range(func(k, v any) bool {
				reminder, ok := v.(*recoveryReminder)
				if !ok {
					return true
				}
				if !reminder.exhausted() {
					pending++
				}
				if !reminder.due(now) {
					return true
				}
				sent := reminder.fire(now)
				logger.InfoCF("agent", "restart recovery: re-reminding interrupted session",
					map[string]any{"session_key": reminder.sessionKey, "sent": sent, "max": reminder.max})
				if err := al.bus.PublishOutbound(ctx, bus.OutboundMessage{
					Channel:    reminder.channel,
					ChatID:     reminder.chatID,
					AgentID:    reminder.agentID,
					SessionKey: reminder.sessionKey,
					Content:    reminder.content,
				}); err != nil {
					logger.WarnCF("agent", "restart recovery: re-reminder failed",
						map[string]any{"session_key": reminder.sessionKey, "error": err.Error()})
				}
				return true
			})
			// Reminders are only registered before the loop starts; once every
			// entry is exhausted (or cancelled out of the map) there is nothing
			// left to wait for — exit instead of ticking forever.
			if pending == 0 {
				return
			}
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
		content:    restartRecoveryNoticeSealed,
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
		return r.sentCount()
	}
	return 0
}

// sealDanglingToolCallsForRestart is a thin alias used by tests to exercise
// restart sealing directly.
func sealDanglingToolCallsForRestart(history []providers.Message) []providers.Message {
	return sealDanglingWith(history, restartToolResultNote)
}
