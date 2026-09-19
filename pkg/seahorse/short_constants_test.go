package seahorse

import "testing"

// Option C pins the leaf chunk size reduction (20000 → 8000): smaller chunks
// mean each summarize LLM call is proportionally cheaper, directly cutting
// the worst-case end-of-turn latency when compaction is still synchronous
// (pre-option-A builds) and shrinking per-call cost after option A.
func TestLeafChunkTokensReduced(t *testing.T) {
	if LeafChunkTokens != 8000 {
		t.Fatalf("LeafChunkTokens = %d, want 8000 (option C: smaller leaf chunks)", LeafChunkTokens)
	}
	// Invariant: leaf summaries still target more tokens than the condensed
	// minimum, so condensation thresholds stay coherent after the change.
	if LeafChunkTokens <= CondensedTargetTokens {
		t.Fatalf("LeafChunkTokens (%d) must stay above CondensedTargetTokens (%d)",
			LeafChunkTokens, CondensedTargetTokens)
	}
}
