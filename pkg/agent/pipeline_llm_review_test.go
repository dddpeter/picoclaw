package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// ─── Review fixes for the 2026-10-05 context-management series ───

// TestDoSplitTurnCompact_SummarizerUsesActiveModel pins review P1-1b: the
// split-turn summarizer must call with exec.activeModel (the model the turn
// is about to use), never the stale exec.llmModel — routeMediaTurn rotates
// the active pair to a vision candidate one assignment before llmModel
// catches up, and the mismatched (vision provider + stale model id) call
// was silently failing open on multimodal turns.
func TestDoSplitTurnCompact_SummarizerUsesActiveModel(t *testing.T) {
	stub := &summarizeStubProvider{}
	_, ts, exec := splitTurnTestExec(t, stub, activeTail())
	// Simulate the pre-fix hazard: llmModel stale/empty, activeModel is the
	// vision candidate routeMediaTurn just selected.
	exec.llmModel = ""
	exec.activeModel = "vision-model"

	pipeline := &Pipeline{}
	if !pipeline.doSplitTurnCompact(context.Background(), ts, exec, false) {
		t.Fatal("expected split-turn to fire")
	}
	stub.mu.Lock()
	models := append([]string(nil), stub.lastModels...)
	stub.mu.Unlock()
	if len(models) != 1 || models[0] != "vision-model" {
		t.Fatalf("summarizer model = %v, want [vision-model]", models)
	}
}

// optsRecordingProvider records the max_tokens of every Chat call.
type optsRecordingProvider struct {
	mu         sync.Mutex
	maxTokens  []int
	failFirstN int
	calls      int
}

func (p *optsRecordingProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, opts map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls++
	callNo := p.calls
	mt, _ := common.AsInt(opts["max_tokens"])
	p.maxTokens = append(p.maxTokens, mt)
	p.mu.Unlock()
	if callNo <= p.failFirstN {
		return nil, &common.EmptyCompletionError{}
	}
	return &providers.LLMResponse{Content: "ok", FinishReason: "stop"}, nil
}

func (p *optsRecordingProvider) GetDefaultModel() string { return "opts-model" }

// TestPipeline_CallLLM_ReclampOnRetry pins review P2-1/P2-2: the output
// budget must be re-clamped on every retry attempt. First call fails with
// EmptyCompletionError while the context is stuffed (clamped to a tiny
// budget); the retry path compacts the history away, and the second call
// must then see the FULL configured max_tokens again instead of the frozen
// floor.
func TestPipeline_CallLLM_ReclampOnRetry(t *testing.T) {
	provider := &optsRecordingProvider{failFirstN: 1}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	pipeline := NewPipeline(al)
	ts := newCompactTurnState(t, al, "reclamp", makeTestProcessOpts("reclamp"))
	ts.agent.SplitTurnEnabled = false // isolate the clamp from split-turn

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn: %v", err)
	}

	// Raise the configured budget above the clamp floor (4096) so the first
	// attempt is genuinely clamped, not floored.
	ts.agent.MaxTokens = 32_768
	configured := ts.agent.MaxTokens
	// ~160k-token prompt (chars×2/5) under a 170k window: the clamp bites
	// on the first attempt; after the retry compaction empties the history
	// the rebuilt context is tiny and the clamp must release.
	exec.callMessages = []providers.Message{{Role: "user", Content: strings.Repeat("x", 400_000)}}
	exec.messages = exec.callMessages
	exec.currentTurnStart = 0
	ts.agent.ContextWindow = 170_000

	_, _ = pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)

	provider.mu.Lock()
	calls := append([]int(nil), provider.maxTokens...)
	provider.mu.Unlock()
	if len(calls) < 2 {
		t.Fatalf("expected ≥2 provider calls, got %d (%v)", len(calls), calls)
	}
	if calls[0] >= configured {
		t.Fatalf("first attempt must be clamped below configured: %d vs %d", calls[0], configured)
	}
	if calls[len(calls)-1] < configured {
		t.Fatalf("retry must re-clamp to full budget after compaction: last=%d, configured=%d (all: %v)",
			calls[len(calls)-1], configured, calls)
	}
}

// summarizeAwareProvider routes summarizer calls (identified by the
// compression prompt) to a canned summary, and applies failFirstN only to
// real turn calls.
type summarizeAwareProvider struct {
	mu            sync.Mutex
	turnCalls     int
	failFirstN    int
	turnResponse  string
	summaryCalled int
}

func (p *summarizeAwareProvider) Chat(_ context.Context, msgs []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	if len(msgs) > 0 && strings.Contains(msgs[0].Content, "对话压缩助手") {
		p.mu.Lock()
		p.summaryCalled++
		p.mu.Unlock()
		return &providers.LLMResponse{Content: "摘要：已扫描，发现 3 个问题", FinishReason: "stop"}, nil
	}
	p.mu.Lock()
	p.turnCalls++
	n := p.turnCalls
	p.mu.Unlock()
	if n <= p.failFirstN {
		return nil, &common.EmptyCompletionError{}
	}
	return &providers.LLMResponse{Content: p.turnResponse, FinishReason: "stop"}, nil
}

func (p *summarizeAwareProvider) GetDefaultModel() string { return "aware-model" }

// TestPipeline_CallLLM_SplitTurnForcedAfterRetryRebuild pins review P2-3:
// after a split-turn rewrite, a context failure must NOT dead-end — the
// retry rebuild reconstructs the raw tail (undoing the split), and the !fit
// escape hatch fires a FORCED second split on the rebuilt view so the turn
// recovers instead of failing with "refusing to drop active turn
// messages". Split-turn is DISABLED at the boundary (throttle armed) to
// prove the force path works even when the boundary pass already ran.
func TestPipeline_CallLLM_SplitTurnForcedAfterRetryRebuild(t *testing.T) {
	provider := &summarizeAwareProvider{
		failFirstN:   1,
		turnResponse: "recovered after forced split",
	}
	al, _, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	pipeline := NewPipeline(al)
	ts := newCompactTurnState(t, al, "forced-split", makeTestProcessOpts("forced-split"))
	ts.agent.SplitTurnEnabled = true
	ts.agent.SplitTurnKeepTokens = 1000

	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn: %v", err)
	}

	// A tail big enough to overflow a tiny window on its own, all persisted.
	tail := activeTail()
	stable := []providers.Message{{Role: "system", Content: "sys"}}
	exec.messages = append(append([]providers.Message(nil), stable...), tail...)
	exec.callMessages = exec.messages
	exec.currentTurnStart = len(stable)
	exec.providerToolDefs = nil
	for i := range tail {
		ts.recordPersistedMessage(tail[i])
	}
	// Window so small the compacted (empty) stable history plus the tail
	// still overflows — the tail alone drives the budget, which is exactly
	// the split-turn scenario.
	ts.agent.ContextWindow = 8000

	ctrl, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1)
	if err != nil {
		t.Fatalf("expected forced split to recover the turn, got error: %v", err)
	}
	if ctrl != ControlBreak {
		t.Fatalf("ctrl = %v, want ControlBreak", ctrl)
	}
	if exec.finalContent != "recovered after forced split" {
		t.Fatalf("finalContent = %q", exec.finalContent)
	}
	if !ts.splitTurnDone {
		t.Fatal("split-turn throttle flag not armed")
	}
	provider.mu.Lock()
	summaries := provider.summaryCalled
	provider.mu.Unlock()
	if summaries == 0 {
		t.Fatal("no summarizer call happened — the split path never fired")
	}
}

var _ = providers.Message{} // keep providers import if fixtures shrink
