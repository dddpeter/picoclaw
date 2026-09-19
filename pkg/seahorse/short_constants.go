package seahorse

import "sync/atomic"

// Short-term memory configuration constants — all are experience-based defaults.

const (
	// OrdinalStep is the gap between ordinals in context_items.
	// Insert at midpoint; resequence only when precision exhausted.
	OrdinalStep = 100

	// ContextThreshold is the compaction trigger for the context window.
	ContextThreshold float64 = 0.75 // Compact at 75% of context window

	// LeafMinFanout is the fanout parameter.
	LeafMinFanout          int = 8 // Min messages per leaf summary
	CondensedMinFanout     int = 4 // Min summaries per condensed
	CondensedMinFanoutHard int = 2 // Min for forced compaction

	// LeafChunkTokens is the token target. Kept modest (8000): each leaf
	// chunk becomes one summarize LLM call, so chunk size directly sets the
	// per-call cost of end-of-turn compaction.
	LeafChunkTokens       int = 8000 // Max tokens per leaf chunk
	LeafTargetTokens      int = 1200 // Target tokens for leaf summaries
	CondensedTargetTokens int = 2000 // Target tokens for condensed summaries
	MaxExpandTokens       int = 4000 // Token cap for expansion queries

	// MaxCompactIterations caps CompactUntilUnder to prevent infinite loops.
	// Each iteration reduces ~4x tokens via leaf (8:1) or condensed (4:1) compaction.
	// With a 200k token context window and 75% threshold, ~20 iterations is enough
	// for any realistic scenario. If exceeded, the issue is logged as a warning.
	MaxCompactIterations int = 20
)

// freshTailCount is how many recent messages are never summarized away.
// Raised from the original 32 to 128 (2026-09-19): for coding agents a
// 32-message raw tail covers only a handful of tool round-trips, so the
// model kept losing exact file contents it had just read and re-read them
// (a dominant cause of slow coding turns). pi keeps full raw history
// until the window is nearly full; 128 messages of tail plus the usage
// gate on post-turn compaction gets most of that benefit without
// unbounded growth. Configurable: agents.defaults.fresh_tail_messages.
var freshTailCount atomic.Int32

// FreshTailCountValue reports the protected raw-tail message count.
func FreshTailCountValue() int { return int(freshTailCount.Load()) }

// SetFreshTailCount overrides the protected raw-tail message count (called
// once at startup/reload from config, before any compaction runs).
func SetFreshTailCount(n int) {
	if n > 0 {
		freshTailCount.Store(int32(n))
	}
}

func init() { freshTailCount.Store(128) }
