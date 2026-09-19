// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// shouldCompactNow reports whether post-turn compaction should run. nil
// usage (unknown) conservatively compacts — the old behavior.
func shouldCompactNow(threshold float64, usage *bus.ContextUsage, agent *AgentInstance) bool {
	if threshold <= 0 || threshold > 0.98 {
		threshold = 0.75
	}
	if usage == nil {
		return true
	}
	window := agent.ContextWindow - agent.MaxTokens
	if window <= 0 {
		window = agent.ContextWindow
	}
	return usage.UsedTokens >= int(float64(window)*threshold)
}

const (
	// compactCallTimeout bounds a single async compaction call. Generous on
	// purpose: leaf compaction is a summarize LLM call (observed ~16s), and
	// multi-chunk sessions chain several of them inside one Compact; the old
	// synchronous path ran on turnCtx whose effective LLM budget was far
	// larger than the compaction drain bound.
	compactCallTimeout = 3 * time.Minute

	// compactDrainTimeout bounds shutdown draining of in-flight compactions.
	// Anything still running past this bound is abandoned at shutdown rather
	// than blocking exit.
	compactDrainTimeout = 30 * time.Second
)

// compactScheduler runs post-turn context compaction off the turn critical
// path. Mirrors memoryCommitter: fire-and-forget goroutine with a detached
// context, WaitGroup-tracked so shutdown can drain in-flight work. A
// per-session in-flight map dedups concurrent scheduling: consecutive turn
// endings (auto-continue segments, quick follow-ups) must not pile up
// compactions for the same session — the next turn ending over threshold
// schedules a fresh one anyway.
type compactScheduler struct {
	wg       sync.WaitGroup
	inFlight sync.Map // sessionKey → struct{}; one compaction per session at a time
	compact  func(ctx context.Context, req *CompactRequest) error
}

// newCompactScheduler builds the scheduler bound to the loop's context
// manager. The closure resolves al.contextManager at call time so wiring
// order does not matter.
func newCompactScheduler(al *AgentLoop) *compactScheduler {
	return &compactScheduler{
		compact: func(ctx context.Context, req *CompactRequest) error {
			if al.contextManager == nil {
				return nil
			}
			return al.contextManager.Compact(ctx, req)
		},
	}
}

// scheduleCompact enqueues an async compaction for a completed turn when the
// turn's options allow it AND the session is under real context pressure
// (usage gate, pi-style: raw history is preserved until the window is
// meaningfully full — rolling summarization every turn made coding agents
// re-read files whose contents had been summarized away).
// The context is detached because turn contexts are canceled by the time
// finalize returns, yet compaction must still run.
func (al *AgentLoop) scheduleCompact(sessionKey string, budget int, opts processOptions) {
	al.scheduleCompactWithUsage(al.registry.GetDefaultAgent(), sessionKey, budget, opts, nil)
}

// scheduleCompactWithUsage is scheduleCompact with the owning agent (whose
// ContextWindow/MaxTokens/threshold drive the gate — NOT the registry
// default, which differs in multi-agent setups) and a precomputed context
// usage snapshot (finalize already has one; nil computes it here).
func (al *AgentLoop) scheduleCompactWithUsage(agent *AgentInstance, sessionKey string, budget int, opts processOptions, usage *bus.ContextUsage) {
	if al.contextManager == nil || opts.NoHistory || !opts.EnableSummary {
		return
	}
	// Usage gate: below the threshold the session keeps raw history and
	// compaction is skipped entirely (overflow is still handled by
	// forceCompression on context-limit errors).
	if agent != nil {
		if usage == nil {
			usage = computeContextUsage(agent, sessionKey)
		}
		if !shouldCompactNow(agent.CompactUsageThreshold, usage, agent) {
			return
		}
	}

	scheduler := al.compactScheduler
	if scheduler == nil {
		logger.WarnCF("agent", "compactScheduler not initialized; skipping post-turn compaction", nil)
		return
	}
	if _, loaded := scheduler.inFlight.LoadOrStore(sessionKey, struct{}{}); loaded {
		logger.DebugCF("agent", "Post-turn compact already in flight; skipping", map[string]any{
			"session_key": sessionKey,
		})
		return
	}
	req := &CompactRequest{
		SessionKey: sessionKey,
		Reason:     ContextCompressReasonSummarize,
		Budget:     budget,
	}
	scheduler.wg.Add(1)
	go func() {
		defer scheduler.wg.Done()
		defer scheduler.inFlight.Delete(sessionKey)
		// Detached context: must outlive the turn that enqueued it.
		ctx, cancel := context.WithTimeout(context.Background(), compactCallTimeout)
		defer cancel()
		if err := scheduler.compact(ctx, req); err != nil {
			logger.WarnCF("agent", "Post-turn compact failed (turn unaffected)", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
		}
	}()
}

// drainCompact waits for in-flight async compactions, bounded by timeout.
// Call during shutdown before the seahorse engine/DB goes away.
func (al *AgentLoop) drainCompact(timeout time.Duration) {
	scheduler := al.compactScheduler
	if scheduler == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		scheduler.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.WarnCF("agent", "Compact drain timed out; abandoning in-flight compaction",
			map[string]any{"timeout": timeout.String()})
	}
}
