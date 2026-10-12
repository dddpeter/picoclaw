// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// manualCompactMinMessages guards against "compacted 1 message" no-ops that
// feel broken (octop-harness compaction.py MIN_SUMMARIZE_MESSAGES, octop
// borrowing §五).
const manualCompactMinMessages = 4

// runManualCompact force-compacts a session's context on demand — the manual
// counterpart to the automatic watermarks (octop borrowing §五: users clear
// the window before a big task, or retry after an automatic compaction
// failure). Tiers:
//   - history below the floor        → nothing_to_compact (no pointless summary)
//   - a compaction already in flight → busy (per-session dedup)
//   - a turn is active               → scheduled at turn end (non-blocking,
//     aligned with /new and /switch; runs through the shared scheduler)
//   - idle                           → synchronous, bounded by compactCallTimeout
//
// Unlike /new the session is NOT rotated; the transcript stays on disk and
// compressed details remain re-readable (short_expand for seahorse sessions).
func (al *AgentLoop) runManualCompact(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
) (*commands.CompactOutcome, error) {
	if agent == nil || agent.Sessions == nil {
		return nil, fmt.Errorf("session store not available")
	}
	if al.contextManager == nil {
		return nil, fmt.Errorf("context manager not available")
	}

	history := agent.Sessions.GetHistory(sessionKey)
	before := len(history)
	beforeUsage := agent.snapshotContextUsage(sessionKey)

	base := func(state string) *commands.CompactOutcome {
		out := &commands.CompactOutcome{
			State:          state,
			MessagesBefore: before,
			MessagesAfter:  before,
		}
		if beforeUsage != nil {
			out.UsedBefore = beforeUsage.UsedTokens
			out.UsedAfter = beforeUsage.UsedTokens
			out.TotalTokens = beforeUsage.TotalTokens
			out.CompressAtTokens = beforeUsage.CompressAtTokens
		}
		return out
	}

	if before < manualCompactMinMessages {
		return base(commands.CompactStateNothing), nil
	}

	scheduler := al.compactScheduler
	if scheduler == nil {
		return nil, fmt.Errorf("compact scheduler not available")
	}
	if _, loaded := scheduler.inFlight.LoadOrStore(sessionKey, struct{}{}); loaded {
		return base(commands.CompactStateBusy), nil
	}

	req := &CompactRequest{
		SessionKey: sessionKey,
		Reason:     ContextCompressReasonManual,
		Budget:     agent.CompactionBudget(),
	}

	// A live turn owns the session: schedule at turn end instead of running
	// inline (the turn is mutating history right now).
	if al.getActiveTurnState(sessionKey) != nil {
		scheduler.wg.Add(1)
		go func() {
			defer scheduler.wg.Done()
			defer scheduler.inFlight.Delete(sessionKey)
			// Detached context: must outlive neither the command nor the
			// turn, just its own bound.
			runCtx, cancel := context.WithTimeout(context.Background(), compactCallTimeout)
			defer cancel()
			if err := scheduler.compact(runCtx, req); err != nil {
				logger.WarnCF("agent", "Manual compact (scheduled) failed", map[string]any{
					"session_key": sessionKey,
					"error":       err.Error(),
				})
			}
		}()
		return base(commands.CompactStateScheduled), nil
	}

	// Idle: run synchronously so the reply can carry real after-stats.
	defer scheduler.inFlight.Delete(sessionKey)
	runCtx, cancel := context.WithTimeout(context.Background(), compactCallTimeout)
	defer cancel()
	if err := scheduler.compact(runCtx, req); err != nil {
		return base(commands.CompactStateBusy), fmt.Errorf("compaction failed: %w", err)
	}

	out := base(commands.CompactStateDone)
	out.MessagesAfter = len(agent.Sessions.GetHistory(sessionKey))
	if afterUsage := agent.snapshotContextUsage(sessionKey); afterUsage != nil {
		out.UsedAfter = afterUsage.UsedTokens
	}
	return out, nil
}

// tryHandleCompactCommand routes /compact while a turn is active: instead of
// leaking the protocol word into the steering queue, it schedules a manual
// compaction at turn end and acknowledges (the /stop precedent).
func (al *AgentLoop) tryHandleCompactCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	sessionKey string,
) bool {
	cmdName, ok := commands.CommandName(msg.Content)
	if !ok || cmdName != "compact" {
		return false
	}
	if fields := lenFields(msg.Content); fields != 1 {
		return false // trailing content — not a bare /compact
	}

	agent := al.agentForSession(sessionKey)
	outcome, err := al.runManualCompact(ctx, agent, sessionKey)
	reply := commands.FormatCompactReply(outcome, err)

	if al.channelManager != nil {
		al.channelManager.InvokeTypingStop(msg.Channel, msg.ChatID)
	}
	al.PublishResponseIfNeeded(ctx, msg.Channel, msg.ChatID, sessionKey, reply)
	return true
}

// lenFields counts whitespace-separated tokens without allocating.
func lenFields(s string) int {
	n, inField := 0, false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inField = false
		} else if !inField {
			inField = true
			n++
		}
	}
	return n
}
