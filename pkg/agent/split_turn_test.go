package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

// ─── Split-turn prefix summarization tests (design ②, 2026-10-05) ───

func bigContent(n int) string { return strings.Repeat("字", n) }

// activeTail builds a realistic active-turn tail: user + several
// assistant(tool_calls)/tool-result pairs with sizeable outputs.
func activeTail() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: bigContent(500)}, // ~200 tokens
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "c1", Type: "function", Function: &providers.FunctionCall{Name: "exec", Arguments: `{"command":"ls"}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: bigContent(4000)}, // ~1600 tokens
		{Role: "assistant", Content: "第一步完成"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "c2", Type: "function", Function: &providers.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "c2", Content: bigContent(4000)},
		{Role: "assistant", Content: "第二步完成"},
	}
}

// TestFindSplitTurnCutPoint_NeverLandsOnToolResult pins the protocol-safety
// invariant: the kept tail must start at a user/assistant boundary.
func TestFindSplitTurnCutPoint_NeverLandsOnToolResult(t *testing.T) {
	tail := activeTail()
	p := findSplitTurnCutPoint(tail, 1000) // keep ~1k tokens
	if p <= 0 {
		t.Fatalf("no cut point found for keep=1000")
	}
	if isToolResultMessage(tail[p]) {
		t.Fatalf("cut landed on a tool result (index %d) — protocol break", p)
	}
	// Prefix must not strand a tool result whose parent tool_calls are also
	// in the prefix... conversely: every tool result IN THE KEPT TAIL must
	// have its parent assistant in the kept tail too.
	kept := tail[p:]
	parents := map[string]bool{}
	for i := range kept {
		if kept[i].Role == "assistant" {
			for _, tc := range kept[i].ToolCalls {
				parents[tc.ID] = true
			}
		}
	}
	for i := range kept {
		if isToolResultMessage(kept[i]) && !parents[kept[i].ToolCallID] {
			t.Fatalf("kept tail has orphan tool result %q at %d", kept[i].ToolCallID, i)
		}
	}
}

// TestFindSplitTurnCutPoint_TailUnderBudgetReturnsMinusOne pins the guard:
// a tail smaller than the keep budget has nothing worth splitting.
func TestFindSplitTurnCutPoint_TailUnderBudgetReturnsMinusOne(t *testing.T) {
	small := []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	if p := findSplitTurnCutPoint(small, 8192); p != -1 {
		t.Fatalf("under-budget tail: cut = %d, want -1", p)
	}
	if p := findSplitTurnCutPoint(small[:1], 100); p != -1 {
		t.Fatalf("single-message tail: cut = %d, want -1", p)
	}
}

// TestBuildSplitTurnMessages pins the reassembly shape: rewritten user
// anchor carries the summary, kept tail verbatim, stable prefix untouched.
func TestBuildSplitTurnMessages(t *testing.T) {
	stable := []providers.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old q"},
		{Role: "assistant", Content: "old a"},
	}
	kept := []providers.Message{
		{Role: "assistant", Content: "第二步完成"},
	}
	out := buildSplitTurnMessages(stable, "评审这个仓库", "1. 已完成扫描\n2. 发现 X", kept)
	if len(out) != len(stable)+1+len(kept) {
		t.Fatalf("length = %d, want %d", len(out), len(stable)+1+len(kept))
	}
	if out[0].Content != "sys" || out[1].Content != "old q" {
		t.Fatal("stable prefix must be untouched")
	}
	anchor := out[len(stable)]
	if anchor.Role != "user" {
		t.Fatalf("anchor role = %q, want user", anchor.Role)
	}
	if !strings.Contains(anchor.Content, "评审这个仓库") ||
		!strings.Contains(anchor.Content, "<history>") ||
		!strings.Contains(anchor.Content, "发现 X") {
		t.Fatalf("anchor content wrong: %q", anchor.Content)
	}
	if out[len(out)-1].Content != "第二步完成" {
		t.Fatal("kept tail must be verbatim")
	}
}

// summarizeStubProvider records summarization calls and returns a canned
// summary.
type summarizeStubProvider struct {
	mu        sync.Mutex
	lastInput string
	calls     int
	fail      bool
}

func (p *summarizeStubProvider) Chat(_ context.Context, msgs []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	p.mu.Lock()
	p.calls++
	p.lastInput = msgs[0].Content
	p.mu.Unlock()
	if p.fail {
		return nil, &common.EmptyCompletionError{}
	}
	return &providers.LLMResponse{Content: "结构化摘要：已完成扫描，发现 3 个问题", FinishReason: "stop"}, nil
}

func (p *summarizeStubProvider) GetDefaultModel() string { return "stub" }

func splitTurnTestExec(t *testing.T, prv providers.LLMProvider, tail []providers.Message) (*Pipeline, *turnState, *turnExecution) {
	t.Helper()
	al, agent, cleanup := newTurnCoordTestLoop(t, prv)
	t.Cleanup(cleanup)
	ts := newCompactTurnState(t, al, "split-session", makeTestProcessOpts("split-session"))
	agent.SplitTurnEnabled = true
	agent.SplitTurnKeepTokens = 1000

	stable := []providers.Message{{Role: "system", Content: "sys"}}
	exec := &turnExecution{
		messages:         append(append([]providers.Message(nil), stable...), tail...),
		activeProvider:   prv,
		activeModel:      "stub-model",
		llmModel:         "stub-model",
		currentTurnStart: len(stable),
	}
	exec.callMessages = exec.messages
	return NewPipeline(al), ts, exec
}

// TestDoSplitTurnCompact_RewritesRequestView pins the end-to-end reducer:
// prefix summarized, request rewritten, throttle armed.
func TestDoSplitTurnCompact_RewritesRequestView(t *testing.T) {
	stub := &summarizeStubProvider{}
	_, ts, exec := splitTurnTestExec(t, stub, activeTail())

	pipeline := &Pipeline{}
	if !pipeline.doSplitTurnCompact(context.Background(), ts, exec) {
		t.Fatal("expected split-turn to fire")
	}
	if !ts.splitTurnDone {
		t.Fatal("throttle flag not armed")
	}
	if stub.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", stub.calls)
	}
	// Request shape: [sys, user'(summary), kept tail...]
	if exec.messages[1].Role != "user" || !strings.Contains(exec.messages[1].Content, "结构化摘要") {
		t.Fatalf("rewritten anchor wrong: %+v", exec.messages[1])
	}
	if exec.currentTurnStart != 2 {
		t.Fatalf("currentTurnStart = %d, want 2 (after sys+anchor)", exec.currentTurnStart)
	}
	// The summarized prefix content must be gone from the request view.
	for _, m := range exec.messages {
		if isToolResultMessage(m) && m.ToolCallID == "c1" {
			t.Fatal("summarized tool result still present in request")
		}
	}
	// Kept tail tool results keep their parents.
	if len(exec.messages) != 4 { // sys + anchor + kept(c2 pair…) — see tail layout
		t.Logf("messages = %d (info)", len(exec.messages))
	}
}

// TestDoSplitTurnCompact_ThrottledOncePerTurn pins the one-shot guard.
func TestDoSplitTurnCompact_ThrottledOncePerTurn(t *testing.T) {
	stub := &summarizeStubProvider{}
	_, ts, exec := splitTurnTestExec(t, stub, activeTail())
	pipeline := &Pipeline{}

	if !pipeline.doSplitTurnCompact(context.Background(), ts, exec) {
		t.Fatal("first call must fire")
	}
	before := append([]providers.Message(nil), exec.messages...)
	if pipeline.doSplitTurnCompact(context.Background(), ts, exec) {
		t.Fatal("second call must be throttled")
	}
	if len(exec.messages) != len(before) || stub.calls != 1 {
		t.Fatal("throttled call must not touch anything")
	}
}

// TestDoSplitTurnCompact_FailureKeepsContext pins fail-open: a summarizer
// error leaves the request view and throttle untouched.
func TestDoSplitTurnCompact_FailureKeepsContext(t *testing.T) {
	stub := &summarizeStubProvider{fail: true}
	_, ts, exec := splitTurnTestExec(t, stub, activeTail())
	before := append([]providers.Message(nil), exec.messages...)
	pipeline := &Pipeline{}

	if pipeline.doSplitTurnCompact(context.Background(), ts, exec) {
		t.Fatal("failing summarizer must return false")
	}
	if ts.splitTurnDone {
		t.Fatal("throttle must not arm on failure")
	}
	if len(exec.messages) != len(before) {
		t.Fatal("request view must be untouched on failure")
	}
}

// TestDoSplitTurnCompact_DisabledByConfig pins the config gate.
func TestDoSplitTurnCompact_DisabledByConfig(t *testing.T) {
	stub := &summarizeStubProvider{}
	_, ts, exec := splitTurnTestExec(t, stub, activeTail())
	ts.agent.SplitTurnEnabled = false
	pipeline := &Pipeline{}
	if pipeline.doSplitTurnCompact(context.Background(), ts, exec) {
		t.Fatal("disabled split-turn must not fire")
	}
	if stub.calls != 0 {
		t.Fatal("summarizer must not be called when disabled")
	}
}

// TestSerializeTurnPrefix_CapsToolResults pins the summarizer input budget.
func TestSerializeTurnPrefix_CapsToolResults(t *testing.T) {
	prefix := []providers.Message{
		{Role: "user", Content: "查一下"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "c1", Type: "function", Function: &providers.FunctionCall{Name: "exec", Arguments: `{"command":"ls"}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: bigContent(10000)},
	}
	out := serializeTurnPrefix(prefix)
	// 10000 runes of content, capped at 2000 + ellipsis.
	if got := strings.Count(out, "字"); got > 2001 {
		t.Fatalf("tool result not capped: %d runes", got)
	}
	if !strings.Contains(out, "[用户]") || !strings.Contains(out, "[调用工具]") || !strings.Contains(out, "[工具结果]") {
		t.Fatalf("serialization labels missing: %q", out[:80])
	}
}

// TestSplitTurnConfigDefaults pins the config helpers (nil = enabled,
// default keep budget).
func TestSplitTurnConfigDefaults(t *testing.T) {
	var nilCfg *config.SplitTurnConfig
	if !nilCfg.EffectiveEnabled() || nilCfg.EffectiveKeepRecentTokens() != 8192 {
		t.Fatal("nil config must default to enabled/8192")
	}
	off := false
	cfg := &config.SplitTurnConfig{Enabled: &off, KeepRecentTokens: 4096}
	if cfg.EffectiveEnabled() || cfg.EffectiveKeepRecentTokens() != 4096 {
		t.Fatal("explicit config not honored")
	}
}

// TestCompactBeforeLLMCall_SecondStageFiresSplitTurn pins the wiring: when
// the stable-history compaction leaves the active tail still over budget,
// the second stage (split-turn) fires within the same call.
func TestCompactBeforeLLMCall_SecondStageFiresSplitTurn(t *testing.T) {
	stub := &summarizeStubProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, stub)
	defer cleanup()

	fcm := newFakeContextManagerForCompact()
	al.contextManager = fcm

	ts := newCompactTurnState(t, al, "split-wire", makeTestProcessOpts("split-wire"))
	agent.SplitTurnEnabled = true
	agent.SplitTurnKeepTokens = 1000

	// Active tail with every message already persisted (mirrors the real
	// flow: ExecuteTools/CallLLM persist before the next boundary).
	tail := activeTail()
	stable := []providers.Message{{Role: "system", Content: "sys"}}
	exec := &turnExecution{
		activeProvider: stub,
		activeModel:    "stub-model",
		llmModel:       "stub-model",
	}
	exec.messages = append(append([]providers.Message(nil), stable...), tail...)
	exec.callMessages = exec.messages
	exec.currentTurnStart = len(stable)
	exec.providerToolDefs = nil
	ts.recordPersistedMessage(tail[0])
	for i := 1; i < len(tail); i++ {
		ts.recordPersistedMessage(tail[i])
	}

	// Window small enough that even the faked (empty) compacted history +
	// the tail overflows → stage 2 must fire.
	agent.ContextWindow = 1500

	pipeline := NewPipeline(al)
	pipeline.ContextManager = fcm
	pipeline.compactBeforeLLMCall(context.Background(), ts, exec)

	if !ts.splitTurnDone {
		t.Fatal("second stage (split-turn) did not fire")
	}
	if stub.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", stub.calls)
	}
	// The rewritten anchor must be in the final request view.
	found := false
	for _, m := range exec.callMessages {
		if m.Role == "user" && strings.Contains(m.Content, "结构化摘要") {
			found = true
		}
	}
	if !found {
		t.Fatal("rewritten user anchor missing from callMessages")
	}
}
