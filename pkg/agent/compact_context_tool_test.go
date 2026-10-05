package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// TestCompactContextTool_HonestNoOp pins the honesty principle
// (agentscope-go borrowing §二): below the tool threshold — or when usage
// cannot be estimated — the tool must say plainly that NOTHING was
// compressed, and must not raise the force flag. Claiming compression when
// nothing happened would teach the model that details it can no longer see
// are still in context.
func TestCompactContextTool_HonestNoOp(t *testing.T) {
	tool := NewCompactContextTool()

	// Usage unavailable (no Sessions): conservative no-op, flag untouched.
	ts := &turnState{
		agent:      &AgentInstance{ID: "a", ContextWindow: 100_000, MaxTokens: 8192, CompactToolTriggerRatio: 0.375},
		sessionKey: "s",
	}
	res := tool.Execute(withTurnState(context.Background(), ts), map[string]any{})
	if !strings.Contains(res.ContentForLLM(), "No compaction was performed") {
		t.Fatalf("expected honest no-op wording, got %q", res.ContentForLLM())
	}
	if ts.compactContextRequested.Load() {
		t.Fatal("no-op must not raise the force flag")
	}

	// Below threshold: verify the ratio/threshold math directly (pure helper).
	low := &bus.ContextUsage{HistoryTokens: 10_000}
	agent := &AgentInstance{ID: "a", ContextWindow: 100_000, MaxTokens: 8192, CompactToolTriggerRatio: 0.375}
	ratio, threshold := compactContextUsageRatio(agent, low)
	if ratio != 10000.0/91808.0 {
		t.Fatalf("unexpected ratio %v", ratio)
	}
	if threshold != 0.375 {
		t.Fatalf("threshold must come from the agent field, got %v", threshold)
	}
	if ratio >= threshold {
		t.Fatal("fixture must sit below the threshold")
	}

	// Unset agent threshold falls back to the half-gate default (0.375).
	_, fallback := compactContextUsageRatio(&AgentInstance{ContextWindow: 100_000, MaxTokens: 8192}, low)
	if fallback != 0.375 {
		t.Fatalf("default tool threshold must be 0.375, got %v", fallback)
	}
}

// TestCompactContextTool_PerTurnThrottle caps confused loops at 2 calls.
func TestCompactContextTool_PerTurnThrottle(t *testing.T) {
	tool := NewCompactContextTool()
	ts := &turnState{
		agent:      &AgentInstance{ID: "a", ContextWindow: 100_000, MaxTokens: 8192},
		sessionKey: "s",
	}
	ctx := withTurnState(context.Background(), ts)
	for i := 0; i < compactContextToolMaxUses; i++ {
		res := tool.Execute(ctx, map[string]any{})
		if strings.Contains(res.ContentForLLM(), "already been used") {
			t.Fatalf("call %d must not be throttled yet", i+1)
		}
	}
	res := tool.Execute(ctx, map[string]any{})
	if !strings.Contains(res.ContentForLLM(), "already been used") {
		t.Fatalf("third call must be declined, got %q", res.ContentForLLM())
	}
	if ts.compactContextRequested.Load() {
		t.Fatal("nil-usage no-op path must never raise the flag")
	}
}

// TestCompactContextTool_ForcesNextBoundaryCheck pins the wiring: the force
// flag makes compactBeforeLLMCall run its Compact→Assemble even when the
// estimate is comfortably under budget, tagged model_tool_request; the flag
// is consumed exactly once.
func TestCompactContextTool_ForcesNextBoundaryCheck(t *testing.T) {
	stub := &summarizeStubProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, stub)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	ts := newCompactTurnState(t, al, "cc-force", makeTestProcessOpts("cc-force"))
	agent.ContextWindow = 1_000_000 // comfortably above any estimate below

	exec := &turnExecution{activeProvider: stub, activeModel: "stub-model", llmModel: "stub-model"}
	exec.messages = []providers.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}
	exec.callMessages = exec.messages
	exec.currentTurnStart = 1
	exec.providerToolDefs = nil

	pipeline := NewPipeline(al)
	pipeline.ContextManager = fcm

	// Without the flag: under budget → no compact.
	if pipeline.compactBeforeLLMCall(context.Background(), ts, exec) {
		t.Fatal("under-budget estimate must not compact without the force flag")
	}
	if fcm.callCount() != 0 {
		t.Fatalf("unexpected compact calls: %d", fcm.callCount())
	}

	// With the flag: compact runs despite being under budget, with tool reason.
	ts.compactContextRequested.Store(true)
	if !pipeline.compactBeforeLLMCall(context.Background(), ts, exec) {
		t.Fatal("force flag must trigger the boundary compact")
	}
	if fcm.callCount() != 1 {
		t.Fatalf("expected exactly one compact call, got %d", fcm.callCount())
	}
	if fcm.calls[0].Reason != ContextCompressReasonTool {
		t.Fatalf("expected %q reason, got %q", ContextCompressReasonTool, fcm.calls[0].Reason)
	}

	// The flag is consumed exactly once: a second under-budget call no-ops.
	if pipeline.compactBeforeLLMCall(context.Background(), ts, exec) {
		t.Fatal("flag must be consumed by the first boundary check")
	}
	if fcm.callCount() != 1 {
		t.Fatalf("flag consumption broken: %d calls", fcm.callCount())
	}
}

// TestCompactContextTool_DoesNotConflictWithSplitTurn: the forced compact
// under budget must NOT cascade into the split-turn second stage (that stage
// is for still-over-window payloads), and splitTurnDone stays untouched.
func TestCompactContextTool_DoesNotConflictWithSplitTurn(t *testing.T) {
	stub := &summarizeStubProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, stub)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	ts := newCompactTurnState(t, al, "cc-split", makeTestProcessOpts("cc-split"))
	agent.SplitTurnEnabled = true
	agent.SplitTurnKeepTokens = 1000
	agent.ContextWindow = 1_000_000 // under budget → no split-turn stage

	tail := activeTail()
	stable := []providers.Message{{Role: "system", Content: "sys"}}
	exec := &turnExecution{activeProvider: stub, activeModel: "stub-model", llmModel: "stub-model"}
	exec.messages = append(append([]providers.Message(nil), stable...), tail...)
	exec.callMessages = exec.messages
	exec.currentTurnStart = len(stable)
	exec.providerToolDefs = nil
	ts.recordPersistedMessage(tail[0])
	for i := 1; i < len(tail); i++ {
		ts.recordPersistedMessage(tail[i])
	}

	ts.compactContextRequested.Store(true)
	pipeline := NewPipeline(al)
	pipeline.ContextManager = fcm
	pipeline.compactBeforeLLMCall(context.Background(), ts, exec)

	if ts.splitTurnDone {
		t.Fatal("forced under-budget compact must not fire split-turn")
	}
	if stub.calls != 0 {
		t.Fatalf("split-turn summarizer must not run, got %d calls", stub.calls)
	}
}
