// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	// manualCompactMinMessages guards against "compacted 1 message" no-ops
	// that feel broken (octop borrowing §五; octop's floor is 2, this is
	// deliberately higher — small histories summarize badly).
	manualCompactMinMessages = 4

	// manualCompactKeepTail is the fresh-tail window a manual compaction
	// protects on the seahorse engine (octop borrowing §五 keep-6: the
	// user sees a clear drop while the newest exchanges stay raw). 0-tail
	// engines ignore it.
	manualCompactKeepTail = 6

	// manualCompactTurnWait bounds how long a scheduled manual compaction
	// waits for the owning turn to end before giving up (review P0-2: the
	// legacy engine compacts via full-replace, so interleaving with a live
	// turn's history appends would drop messages).
	manualCompactTurnWait = 10 * time.Minute

	// manualCompactIdlePoll is the poll interval of the turn-end wait.
	manualCompactIdlePoll = 200 * time.Millisecond
)

// manualCompactMode distinguishes the two /compact entry points (review
// P0-2a: the same registered turnState means different things).
type manualCompactMode uint8

const (
	// manualCompactFromWorker: called on the command-processing worker
	// (handleCommand → Runtime.RunCompact). The Setup placeholder registered
	// in activeTurnStates is the caller's OWN — a command-only message never
	// reaches runTurn — so it counts as idle and the synchronous branch runs.
	manualCompactFromWorker manualCompactMode = iota
	// manualCompactFromPump: called from the Run pump's busy branch
	// (tryHandleCompactCommand). Anything registered (real turn, or another
	// message's placeholder about to become one) means busy — and the pump
	// must never block on a synchronous compaction.
	manualCompactFromPump
)

// runManualCompact force-compacts a session's context on demand — the manual
// counterpart to the automatic watermarks (octop borrowing §五: users clear
// the window before a big task, or retry after an automatic compaction
// failure). Tiers:
//   - history below the floor        → nothing_to_compact (no pointless summary)
//   - a compaction already in flight → busy (per-session dedup)
//   - a live turn owns the session   → scheduled at turn end (non-blocking;
//     the goroutine WAITS for the turn to end — it never compacts while the
//     turn is mutating history)
//   - idle                           → synchronous, bounded by compactCallTimeout
//
// Unlike /new the session is NOT rotated; the transcript stays on disk and
// compressed details remain re-readable (short_expand for seahorse sessions).
func (al *AgentLoop) runManualCompact(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	mode manualCompactMode,
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
		// Model-state read lock, not the bare field: this runs outside any
		// turn, so a concurrent /switch (write lock) must not race the read
		// (instance.go lock-rule comment; the idle scanner does the same).
		Budget: agent.snapshotCompactionBudget(),
	}

	if !al.manualCompactIdle(sessionKey, mode) {
		scheduler.wg.Add(1)
		go func() {
			defer scheduler.wg.Done()
			defer scheduler.inFlight.Delete(sessionKey)
			// Wait for the owning turn to end first (review P0-2b): the
			// compaction itself is what must outlive the turn, not this
			// wait. A turn that outlives the bound abandons the compaction
			// (log only — the user can re-issue /compact).
			waitCtx, waitCancel := context.WithTimeout(context.Background(), manualCompactTurnWait)
			defer waitCancel()
			if !al.waitSessionIdle(waitCtx, sessionKey) {
				logger.WarnCF("agent", "Manual compact (scheduled) gave up waiting for the turn to end", map[string]any{
					"session_key": sessionKey,
				})
				return
			}
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
	stats, err := al.compactWithEngineStats(runCtx, req)
	if err != nil {
		return base(commands.CompactStateBusy), fmt.Errorf("compaction failed: %w", err)
	}

	out := base(commands.CompactStateDone)
	if stats != nil {
		out.TokensSaved = stats.TokensSaved
		if stats.TokensSaved <= 0 && stats.Summaries <= 0 {
			// Engine-side honesty (review P1-5): on engines whose floor
			// (leaf fanout / keep-tail) left nothing to compact, "done"
			// with unmoved numbers reads as a broken feature — report the
			// nothing_to_compact tier instead.
			out.State = commands.CompactStateNothing
			return out, nil
		}
	}
	out.MessagesAfter = len(agent.Sessions.GetHistory(sessionKey))
	if afterUsage := agent.snapshotContextUsage(sessionKey); afterUsage != nil {
		out.UsedAfter = afterUsage.UsedTokens
	}
	return out, nil
}

// manualCompactIdle decides the sync-vs-scheduled tier from the registered
// turn state and the caller's mode (see manualCompactMode).
func (al *AgentLoop) manualCompactIdle(sessionKey string, mode manualCompactMode) bool {
	// The pump NEVER compacts inline: a nil state here means the busy branch
	// raced a turn release between its LoadOrStore and this check — a
	// synchronous compaction (up to compactCallTimeout) would block the
	// single inbound pump for every channel (review P1-1).
	if mode == manualCompactFromPump {
		return false
	}
	ts := al.getActiveTurnState(sessionKey)
	if ts == nil {
		return true
	}
	// The caller's own Setup placeholder (worker mode) is not a live LLM
	// turn — a command-only message never reaches runTurn — so it is idle.
	if strings.HasPrefix(ts.turnID, pendingTurnPrefix) {
		return true
	}
	return false
}

// waitSessionIdle blocks until no turn state is registered for sessionKey,
// polling cheaply. Returns false when ctx ends first. Note the residual
// window (idle → compact start): a turn claiming the session in that gap can
// still interleave — the same window the post-turn automatic compaction has
// always had; the session-claim machinery needed to close it fully is not
// worth the added coupling here.
func (al *AgentLoop) waitSessionIdle(ctx context.Context, sessionKey string) bool {
	ticker := time.NewTicker(manualCompactIdlePoll)
	defer ticker.Stop()
	for al.getActiveTurnState(sessionKey) != nil {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
	return true
}

// compactEngineStats reports what one compaction actually reclaimed from the
// engine that owns the live context window. The JSONL transcript never
// shrinks, so message-count deltas under-report engine compaction (review
// P1-5).
type compactEngineStats struct {
	TokensSaved int
	Summaries   int
}

// compactStatser is the optional capability a ContextManager exposes to
// report per-compaction stats (seahorse does; legacy falls back to nil).
type compactStatser interface {
	CompactWithStats(ctx context.Context, req *CompactRequest) (*compactEngineStats, error)
}

// compactWithEngineStats runs a compaction, returning engine stats when the
// active manager supports them (nil stats = unavailable, not "nothing
// happened").
func (al *AgentLoop) compactWithEngineStats(ctx context.Context, req *CompactRequest) (*compactEngineStats, error) {
	if al.contextManager == nil {
		return nil, fmt.Errorf("context manager not available")
	}
	if cs, ok := al.contextManager.(compactStatser); ok {
		return cs.CompactWithStats(ctx, req)
	}
	return nil, al.contextManager.Compact(ctx, req)
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
	outcome, err := al.runManualCompact(ctx, agent, sessionKey, manualCompactFromPump)
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
