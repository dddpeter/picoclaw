package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// TestClampMaxTokensToContext pins the dynamic output-budget clamp (fork,
// 2026-10-05, pi's clampMaxTokensToContext): request only the output space
// the window can actually hold, so a stuffed context never asks for a
// max_tokens the gateway cannot fulfill (the classic silent-empty-response
// recipe on aggregate gateways).
func TestClampMaxTokensToContext(t *testing.T) {
	const window = 100_000
	cases := []struct {
		name          string
		maxTokens     int
		contextTokens int
	}{
		{"room to spare keeps configured", 8192, 20_000},
		{"tight context clamps into the remainder", 8192, 95_000},
		{"nearly full clamps to a small budget", 32768, 90_000},
		{"stuffed context floors at minimum", 32768, 99_000},
		{"zero max keeps zero", 0, 20_000},
	}
	for _, tc := range cases {
		// Expectations derive from the formula so the test stays honest.
		want := tc.maxTokens
		available := window - tc.contextTokens - iterationCompactSafetyTokens
		if tc.maxTokens > 0 && available < tc.maxTokens {
			want = max(1024, available)
		}
		got := clampMaxTokensToContext(tc.maxTokens, window, tc.contextTokens)
		if got != want {
			t.Errorf("%s: clamp(%d, %d, %d) = %d, want %d", tc.name, tc.maxTokens, window, tc.contextTokens, got, want)
		}
	}
	// Invalid window short-circuits to the configured value.
	if got := clampMaxTokensToContext(8192, 0, 20_000); got != 8192 {
		t.Errorf("zero window: got %d, want 8192", got)
	}
}

// TestClampMaxTokensToContext_MonotoneNonIncreasing verifies the clamp never
// returns more than the configured value and never less than the floor.
func TestClampMaxTokensToContext_MonotoneNonIncreasing(t *testing.T) {
	const window, maxTokens = 50_000, 16_384
	prev := maxTokens
	for ctx := 0; ctx <= window+10_000; ctx += 2_500 {
		got := clampMaxTokensToContext(maxTokens, window, ctx)
		if got > prev {
			t.Fatalf("clamp increased with context at ctx=%d: %d > %d", ctx, got, prev)
		}
		if got < 1024 {
			t.Fatalf("clamp below floor at ctx=%d: %d", ctx, got)
		}
		prev = got
	}
}

// TestPipeline_CallLLM_IterationBoundaryCompacts pins the fork's
// iteration-boundary compaction (2026-10-05): when the assembled context
// exceeds the window at an iteration boundary (between tool results and the
// next LLM call), CallLLM must compact synchronously BEFORE calling the
// provider — the provider never sees the over-limit payload at all.
func TestPipeline_CallLLM_IterationBoundaryCompacts(t *testing.T) {
	provider := &failOnceLLMProvider{
		err:      &common.EmptyCompletionError{},
		response: "ok after iteration compaction",
	}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	pipeline := NewPipeline(al)
	ts := newCompactTurnState(t, al, "test-session", makeTestProcessOpts("test-session"))

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	// Shrink the window so the assembled context (real prompt from
	// SetupTurn) overflows it — that is the trigger condition.
	ts.agent.ContextWindow = 1 // far below any realistic prompt cost
	if !strings.HasPrefix(t.Name(), "Test") {
		t.Fatal("sanity")
	}

	_, _ = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 2)

	// The fake Compact is a no-op, so the over-limit payload still reaches
	// the provider (empty response) and the retry chain compacts AGAIN with
	// reason llm_retry. Assert that the FIRST compact happened at the
	// iteration boundary, before any provider call.
	fcm.mu.Lock()
	calls := append([]CompactRequest(nil), fcm.calls...)
	fcm.mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected an iteration-boundary Compact before the LLM call")
	}
	if calls[0].Reason != ContextCompressReasonIteration {
		t.Fatalf("first Compact reason = %q, want %q", calls[0].Reason, ContextCompressReasonIteration)
	}
}

// TestPipeline_CallLLM_IterationBoundarySkipsNoHistory pins the NoHistory
// guard: no store history to compress, the check must not fire.
func TestPipeline_CallLLM_IterationBoundarySkipsNoHistory(t *testing.T) {
	provider := &failFirstNLLMProvider{response: "fine"}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session")
	opts.NoHistory = true
	ts := newCompactTurnState(t, al, "test-session", opts)

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}
	ts.agent.ContextWindow = 1

	_, _ = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)

	if _, ok := fcm.lastCall(); ok {
		t.Fatal("NoHistory turn must not trigger iteration-boundary compaction")
	}
}

// TestPipeline_CallLLM_IterationBoundaryClampsMaxTokens verifies the clamp
// engages on the request options: with NoHistory (compaction path disabled)
// and a context that nearly fills the window, the provider-visible
// max_tokens must be smaller than the configured value.
func TestPipeline_CallLLM_IterationBoundaryClampsMaxTokens(t *testing.T) {
	provider := &failFirstNLLMProvider{response: "fine"}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	pipeline := NewPipeline(al)
	opts := makeTestProcessOpts("test-session")
	opts.NoHistory = true // isolate the clamp from the compaction path
	ts := newCompactTurnState(t, al, "test-session", opts)

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn failed: %v", err)
	}

	configured := ts.agent.MaxTokens
	// Estimator: chars*2/5 → 400k chars ≈ 160k tokens. Window leaves less
	// room than the configured output budget.
	exec.callMessages = []providers.Message{{Role: "user", Content: strings.Repeat("x", 400_000)}}
	exec.messages = exec.callMessages
	ts.agent.ContextWindow = 160_000 + configured + iterationCompactSafetyTokens/2

	_, _ = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)

	got, _ := common.AsInt(exec.llmOpts["max_tokens"])
	if configured <= 0 {
		t.Fatalf("sanity: configured max_tokens = %d", configured)
	}
	if got >= configured {
		t.Fatalf("max_tokens not clamped: got %d, configured %d", got, configured)
	}
	if got < 1024 {
		t.Fatalf("clamped max_tokens below floor: %d", got)
	}
}
