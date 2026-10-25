package providers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFallbackResult_DegradedFromPrimary(t *testing.T) {
	candidates := []FallbackCandidate{
		makeCandidate("openai", "primary-model"),
		makeCandidate("openai", "fallback-model"),
	}

	t.Run("primary served", func(t *testing.T) {
		ct := NewCooldownTracker()
		fc := NewFallbackChain(ct, nil)
		result, err := fc.Execute(context.Background(), candidates, successRun("hello"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.DegradedFromPrimary(candidates) {
			t.Fatal("primary-served result must not be degraded")
		}
	})

	t.Run("fallback served after primary failure", func(t *testing.T) {
		ct := NewCooldownTracker()
		fc := NewFallbackChain(ct, nil)
		attempt := 0
		run := func(ctx context.Context, provider, model string) (*LLMResponse, error) {
			attempt++
			if attempt == 1 {
				return nil, errors.New("429 too many requests")
			}
			return &LLMResponse{Content: "from fallback", FinishReason: "stop"}, nil
		}
		result, err := fc.Execute(context.Background(), candidates, run)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.DegradedFromPrimary(candidates) {
			t.Fatal("result served by the second candidate must be degraded")
		}
	})

	t.Run("fallback served while primary in cooldown", func(t *testing.T) {
		ct := NewCooldownTracker()
		fc := NewFallbackChain(ct, nil)
		// Put the primary into a rate-limit cooldown, then run the chain:
		// the primary is skipped and the fallback succeeds.
		ct.MarkFailureWithHint(candidates[0].StableKey(), FailoverRateLimit, time.Minute)
		result, err := fc.Execute(context.Background(), candidates, successRun("from fallback"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.DegradedFromPrimary(candidates) {
			t.Fatal("result served by the fallback while primary cooled down must be degraded")
		}
	})

	t.Run("nil receiver and empty candidates", func(t *testing.T) {
		var result *FallbackResult
		if result.DegradedFromPrimary(candidates) {
			t.Fatal("nil receiver must report false")
		}
		ok := &FallbackResult{IdentityKey: candidates[0].StableKey()}
		if ok.DegradedFromPrimary(nil) {
			t.Fatal("empty candidates must report false")
		}
		emptyIdentity := &FallbackResult{}
		if emptyIdentity.DegradedFromPrimary(candidates) {
			t.Fatal("empty identity key (no success) must report false")
		}
	})
}
