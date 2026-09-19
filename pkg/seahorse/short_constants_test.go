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

func TestFreshTailCountConfigurable(t *testing.T) {
	original := FreshTailCountValue()
	if original != 128 {
		t.Fatalf("default FreshTailCount = %d, want 128", original)
	}
	SetFreshTailCount(64)
	if FreshTailCountValue() != 64 {
		t.Fatalf("override = %d, want 64", FreshTailCountValue())
	}
	SetFreshTailCount(0) // non-positive ignored
	if FreshTailCountValue() != 64 {
		t.Fatalf("non-positive override must be ignored, got %d", FreshTailCountValue())
	}
	SetFreshTailCount(128) // restore for other tests
}
