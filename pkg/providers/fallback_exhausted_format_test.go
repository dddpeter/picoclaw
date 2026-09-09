package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFallbackExhaustedErrorAllSkippedShowsRecovery pins the user-facing
// aggregate for the all-skip outcome: when every candidate was skipped due to
// cooldown the message must say "unavailable" (not "failed") and carry the
// remaining cooldown, so the user can tell a recoverable rate-limit window
// from a real outage and knows how long to wait.
func TestFallbackExhaustedErrorAllSkippedShowsRecovery(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{
		{
			Provider: "openai", Model: "deepseek-v4-flash",
			Skipped: true, Reason: FailoverRateLimit,
			CooldownRemaining: 42 * time.Second,
			Error:             errors.New("openai/deepseek-v4-flash in cooldown (42s remaining)"),
		},
		{
			Provider: "openai", Model: "glm-5.3-flash",
			Skipped: true, Reason: FailoverRateLimit,
			CooldownRemaining: 90 * time.Second,
			Error:             errors.New("openai/glm-5.3-flash in cooldown (1m30s remaining)"),
		},
	}}

	msg := err.Error()
	if !strings.Contains(msg, "all 2 candidates unavailable") {
		t.Fatalf("all-skip aggregate must say unavailable, got %q", msg)
	}
	if !strings.Contains(msg, "skipped (cooldown, 42s remaining)") {
		t.Fatalf("skip line must carry remaining cooldown, got %q", msg)
	}
	if !strings.Contains(msg, "skipped (cooldown, 1m30s remaining)") {
		t.Fatalf("second skip line must carry remaining cooldown, got %q", msg)
	}
	if strings.Contains(msg, "candidates failed") {
		t.Fatalf("all-skip aggregate must not claim the candidates failed, got %q", msg)
	}
}

// TestFallbackExhaustedErrorMixedKeepsFailedHeader: as soon as one candidate
// produced a real upstream error the header stays "failed" — only the pure
// skip outcome gets the softer "unavailable" wording.
func TestFallbackExhaustedErrorMixedKeepsFailedHeader(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{
		{
			Provider: "openai", Model: "deepseek-v4-flash",
			Reason:   FailoverRateLimit,
			Error:    errors.New("Status: 429"),
			Duration: 300 * time.Millisecond,
		},
		{
			Provider: "openai", Model: "glm-5.3-flash",
			Skipped:  true, Reason: FailoverRateLimit,
			CooldownRemaining: 25 * time.Second,
			Error:             errors.New("openai/glm-5.3-flash in cooldown (25s remaining)"),
		},
	}}

	msg := err.Error()
	if !strings.Contains(msg, "all 2 candidates failed:") {
		t.Fatalf("mixed outcome must keep the failed header, got %q", msg)
	}
	if !strings.Contains(msg, "skipped (cooldown, 25s remaining)") {
		t.Fatalf("skip line must still carry remaining cooldown, got %q", msg)
	}
}

// TestFallbackExhaustedErrorNonCooldownSkipKeepsCause: skips that are not
// cooldown (local rate-limit tokens) keep printing their underlying reason.
func TestFallbackExhaustedErrorNonCooldownSkipKeepsCause(t *testing.T) {
	err := &FallbackExhaustedError{Attempts: []FallbackAttempt{
		{
			Provider: "openai", Model: "primary",
			Skipped:  true, Reason: FailoverRateLimit,
			Error:    errors.New("openai/primary waiting for local rate limit token"),
			Duration: time.Second,
		},
	}}

	msg := err.Error()
	if !strings.Contains(msg, "all 1 candidates unavailable:") {
		t.Fatalf("all-skip header expected, got %q", msg)
	}
	if !strings.Contains(msg, "skipped (openai/primary waiting for local rate limit token)") {
		t.Fatalf("non-cooldown skip must keep its cause, got %q", msg)
	}
}

// TestExecuteCandidateRecordsCooldownRemaining wires the formatting to the
// real walk: after both candidates cool down, the exhausted error carries the
// remaining time for each.
func TestExecuteCandidateRecordsCooldownRemaining(t *testing.T) {
	cooldown := NewCooldownTracker()
	fc := NewFallbackChain(cooldown, nil)

	candidates := []FallbackCandidate{
		{Provider: "openai", Model: "primary"},
		{Provider: "openai", Model: "backup"},
	}
	for _, c := range candidates {
		cooldown.MarkFailure(c.StableKey(), FailoverRateLimit)
	}

	_, err := fc.ExecuteCandidate(context.Background(), candidates,
		func(_ context.Context, _ FallbackCandidate) (*LLMResponse, error) {
			t.Fatal("all candidates are cooling down; the run function must not be called")
			return nil, nil
		})
	if err == nil {
		t.Fatal("expected exhaustion when every candidate is in cooldown")
	}

	var exhausted *FallbackExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *FallbackExhaustedError, got %T", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "candidates unavailable") {
		t.Fatalf("aggregate must mark the all-skip outcome, got %q", msg)
	}
	if !strings.Contains(msg, "cooldown, ") || !strings.Contains(msg, "remaining)") {
		t.Fatalf("aggregate must carry remaining cooldown, got %q", msg)
	}
}
