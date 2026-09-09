package providers

import (
	"context"
	"errors"
	"testing"
)

// TestFallbackChainAvailable pins the availability probe used by callers that
// bypass ExecuteCandidate (the agent's streaming first hop): a candidate in
// cooldown must report unavailable, a success must clear it.
func TestFallbackChainAvailable(t *testing.T) {
	cooldown := NewCooldownTracker()
	fc := NewFallbackChain(cooldown, nil)

	const key = "openai/test-model"
	if !fc.Available(key) {
		t.Fatal("fresh candidate must be available")
	}

	cooldown.MarkFailure(key, FailoverRateLimit)
	if fc.Available(key) {
		t.Fatal("candidate in cooldown must be unavailable")
	}

	cooldown.MarkSuccess(key)
	if !fc.Available(key) {
		t.Fatal("candidate must be available again after success")
	}

	var nilChain *FallbackChain
	if !nilChain.Available(key) {
		t.Fatal("nil chain must report available (fail open)")
	}
}

// TestFallbackChainAvailableMatchesExecuteCandidate ensures the probe and the
// real walk agree: after ExecuteCandidate skips a cooled-down candidate, the
// probe also reports it unavailable.
func TestFallbackChainAvailableMatchesExecuteCandidate(t *testing.T) {
	cooldown := NewCooldownTracker()
	fc := NewFallbackChain(cooldown, nil)

	candidates := []FallbackCandidate{
		{Provider: "openai", Model: "primary"},
		{Provider: "openai", Model: "backup"},
	}
	primary := candidates[0].StableKey()

	// Exhaust the primary with a failure, let the walk succeed on backup.
	_, err := fc.ExecuteCandidate(context.Background(), candidates, func(_ context.Context, c FallbackCandidate) (*LLMResponse, error) {
		if c.Model == "primary" {
			return nil, errors.New("Status: 429")
		}
		return &LLMResponse{Content: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("ExecuteCandidate() error = %v", err)
	}
	if fc.Available(primary) {
		t.Fatal("probe must agree with the walk: failed primary is in cooldown")
	}
}
