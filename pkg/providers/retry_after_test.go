package providers

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// TestParseRetryAfterHeader covers the RFC 7231 forms: delay-seconds and
// HTTP-date, plus invalid/past/oversized values.
func TestParseRetryAfterHeader(t *testing.T) {
	now := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"empty", "", 0},
		{"whitespace", "  ", 0},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", 0},
		{"valid seconds", "30", 30 * time.Second},
		{"large clamped", "99999", 10 * time.Minute},
		{"invalid text", "soon", 0},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"future date", now.Add(2 * time.Minute).Format(http.TimeFormat), 2 * time.Minute},
		{"future date clamped", now.Add(time.Hour).Format(http.TimeFormat), 10 * time.Minute},
	}

	for _, tc := range cases {
		got := common.ParseRetryAfterHeader(tc.value, now)
		if got != tc.want {
			t.Errorf("%s: ParseRetryAfterHeader(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}}

// TestHandleErrorResponseCapturesRetryAfter: the Retry-After header of a 429
// must survive into the HTTPError so the fallback cooldown can honor it.
func TestHandleErrorResponseCapturesRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"too many requests"}`)),
	}
	err := common.HandleErrorResponse(resp, "https://api.test")

	var httpErr *common.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *common.HTTPError, got %T", err)
	}
	if httpErr.RetryAfter != 45*time.Second {
		t.Fatalf("RetryAfter = %v, want 45s", httpErr.RetryAfter)
	}
	if httpErr.StatusCode != 429 {
		t.Fatalf("StatusCode = %d, want 429", httpErr.StatusCode)
	}
}

// TestClassifyErrorPreservesRetryAfter: ClassifyError must carry the hint
// from the HTTPError into the FailoverError.
func TestClassifyErrorPreservesRetryAfter(t *testing.T) {
	httpErr := &common.HTTPError{StatusCode: 429, RetryAfter: 45 * time.Second}
	result := ClassifyError(httpErr, "openai", "gpt-4")
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if result.RetryAfter != 45*time.Second {
		t.Fatalf("RetryAfter = %v, want 45s", result.RetryAfter)
	}
}

// TestCooldownHintRaisesFloor: a server hint longer than the standard
// exponential cooldown extends it; a shorter hint is ignored (standard wins).
func TestCooldownHintRaisesFloor(t *testing.T) {
	ct := NewCooldownTracker()

	// First failure standard cooldown is 1 minute; hint 5 minutes must win.
	ct.MarkFailureWithHint("a", FailoverRateLimit, 5*time.Minute)
	if remaining := ct.CooldownRemaining("a"); remaining < 4*time.Minute {
		t.Fatalf("long hint: CooldownRemaining = %v, want ~5m", remaining)
	}

	// Fresh tracker: hint 10s is below the 1m standard, standard must win.
	ct2 := NewCooldownTracker()
	ct2.MarkFailureWithHint("b", FailoverRateLimit, 10*time.Second)
	if remaining := ct2.CooldownRemaining("b"); remaining < 55*time.Second {
		t.Fatalf("short hint: CooldownRemaining = %v, want ~1m (standard floor)", remaining)
	}
}

// TestCooldownHintDoesNotAffectBilling: billing failures keep their own
// multi-hour disable schedule regardless of the hint.
func TestCooldownHintDoesNotAffectBilling(t *testing.T) {
	ct := NewCooldownTracker()
	ct.MarkFailureWithHint("a", FailoverBilling, 5*time.Minute)

	// Billing disables for 5h on first failure; a 5m hint must not shorten
	// it, and the standard 1m cooldown must not be what gates availability.
	if remaining := ct.CooldownRemaining("a"); remaining < 4*time.Hour {
		t.Fatalf("billing: CooldownRemaining = %v, want ~5h", remaining)
	}
}
