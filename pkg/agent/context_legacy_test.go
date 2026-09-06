package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type summarizingRecordingProvider struct {
	mu       sync.Mutex
	prompts  []string
	models   []string
	calls    int
	response string
	fail     bool
	errText  string
}

func (p *summarizingRecordingProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.models = append(p.models, model)
	for _, msg := range messages {
		p.prompts = append(p.prompts, msg.Content)
	}
	if p.errText != "" {
		return nil, fmt.Errorf("%s", p.errText)
	}
	if p.fail {
		return nil, fmt.Errorf("provider unavailable")
	}
	return &providers.LLMResponse{Content: p.response}, nil
}

func (p *summarizingRecordingProvider) GetDefaultModel() string {
	return "recording-model"
}

func (p *summarizingRecordingProvider) allPrompts() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.prompts, "\n---\n")
}

func (p *summarizingRecordingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *summarizingRecordingProvider) lastModel() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.models) == 0 {
		return ""
	}
	return p.models[len(p.models)-1]
}

func newLegacyTestAgentLoop(t *testing.T, provider providers.LLMProvider) *AgentLoop {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "agent-legacy-ctx-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                 tmpDir,
				ModelName:                 "test-model",
				MaxTokens:                 4096,
				MaxToolIterations:         10,
				ContextWindow:             8000,
				SummarizeMessageThreshold: 2,
				SummarizeTokenPercent:     75,
			},
		},
	}
	return NewAgentLoop(cfg, bus.NewMessageBus(), provider)
}

func wireToolCall(id, name, argsJSON string) providers.ToolCall {
	return providers.ToolCall{
		ID:       id,
		Type:     "function",
		Function: &providers.FunctionCall{Name: name, Arguments: argsJSON},
	}
}

func TestSummarizeBatch_PromptIncludesGuardAndToolActivity(t *testing.T) {
	provider := &summarizingRecordingProvider{response: "structured summary"}
	al := newLegacyTestAgentLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	batch := []providers.Message{
		{Role: "user", Content: "Read main.go and tell me what it does"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			wireToolCall("t1", "read_file", `{"path":"main.go"}`),
		}},
		{Role: "tool", ToolCallID: "t1", Content: "package main..."},
		{Role: "assistant", Content: "It is the entry point."},
	}

	lcm := &legacyContextManager{al: al}
	summary, err := lcm.summarizeBatch(context.Background(), agent, batch, "")
	if err != nil {
		t.Fatalf("summarizeBatch failed: %v", err)
	}
	if summary != "structured summary" {
		t.Fatalf("expected provider response as summary, got %q", summary)
	}

	prompt := provider.allPrompts()
	for _, want := range []string{
		"Do NOT continue the conversation",
		"Do NOT respond to any questions",
		"TOOL ACTIVITY",
		"read_file(path=main.go)",
		"tool calls: read_file",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, prompt:\n%s", want, prompt)
		}
	}
}

func TestSummarizeBatch_FallbackIncludesToolActivity(t *testing.T) {
	provider := &summarizingRecordingProvider{fail: true}
	al := newLegacyTestAgentLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	batch := []providers.Message{
		{Role: "user", Content: "Run the tests"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			wireToolCall("t1", "bash", `{"cmd":"go test ./..."}`),
		}},
		{Role: "assistant", Content: "All tests pass."},
	}

	lcm := &legacyContextManager{al: al}
	summary, err := lcm.summarizeBatch(context.Background(), agent, batch, "")
	if err != nil {
		t.Fatalf("summarizeBatch failed: %v", err)
	}
	if !strings.Contains(summary, "bash(cmd=go test ./...)") {
		t.Fatalf("expected fallback summary to contain tool digest, got %q", summary)
	}
}

func TestSummarizeSession_ToolActivityReachesPrompt(t *testing.T) {
	provider := &summarizingRecordingProvider{response: "session summary"}
	al := newLegacyTestAgentLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	agent.Sessions.SetHistory("session-tools", []providers.Message{
		{Role: "user", Content: "Question one"},
		{Role: "assistant", Content: "Let me check.", ToolCalls: []providers.ToolCall{
			wireToolCall("t1", "read_file", `{"path":"pkg/agent/agent.go"}`),
			wireToolCall("t2", "bash", `{"cmd":"go build ./..."}`),
		}},
		{Role: "tool", ToolCallID: "t1", Content: "package agent..."},
		{Role: "tool", ToolCallID: "t2", Content: "build ok"},
		{Role: "assistant", Content: "Answer one"},
		{Role: "user", Content: "Question two"},
		{Role: "assistant", Content: "Answer two"},
		{Role: "user", Content: "Question three"},
		{Role: "assistant", Content: "Answer three"},
	})

	lcm := &legacyContextManager{al: al}
	lcm.summarizeSession(agent, "session-tools")

	prompt := provider.allPrompts()
	if !strings.Contains(prompt, "read_file(path=pkg/agent/agent.go)") {
		t.Fatalf("expected prompt to contain read_file digest, prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "bash(cmd=go build ./...)") {
		t.Fatalf("expected prompt to contain bash digest, prompt:\n%s", prompt)
	}
}

func TestForceCompression_PreservesToolActivity(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	history := make([]providers.Message, 0, 16)
	for turn := 0; turn < 4; turn++ {
		history = append(history,
			providers.Message{Role: "user", Content: fmt.Sprintf("Question %d", turn)},
			providers.Message{Role: "assistant", Content: "Working on it.", ToolCalls: []providers.ToolCall{
				wireToolCall(fmt.Sprintf("t%d", turn), "edit_file", fmt.Sprintf(`{"path":"file%d.go"}`, turn)),
			}},
			providers.Message{Role: "tool", ToolCallID: fmt.Sprintf("t%d", turn), Content: "ok"},
			providers.Message{Role: "assistant", Content: fmt.Sprintf("Answer %d", turn)},
		)
	}
	agent.Sessions.SetHistory("session-compress", history)

	lcm := &legacyContextManager{al: al}
	result, ok := lcm.forceCompression("session-compress")
	if !ok {
		t.Fatal("expected forceCompression to run")
	}
	if result.DroppedMessages == 0 || result.RemainingMessages == 0 {
		t.Fatalf("unexpected compression result: %+v", result)
	}

	summary := agent.Sessions.GetSummary("session-compress")
	if !strings.Contains(summary, "Tool activity before compression") {
		t.Fatalf("expected summary to contain tool activity note, got %q", summary)
	}
	if !strings.Contains(summary, "edit_file(path=file0.go)") {
		t.Fatalf("expected summary to preserve dropped tool calls, got %q", summary)
	}
}

func TestBuildToolActivityDigest(t *testing.T) {
	messages := []providers.Message{
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			wireToolCall("t1", "read_file", `{"path":"a.go"}`),
			{ID: "t2", Name: "bash", Arguments: map[string]any{"cmd": "ls -la"}},
		}},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			wireToolCall("t3", "read_file", `{"path":"a.go"}`),
		}},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			wireToolCall("t4", "no_args", `{}`),
		}},
		{Role: "assistant", ToolCalls: []providers.ToolCall{
			{ID: "t5"},
		}},
	}

	lines := buildToolActivityDigest(messages)
	if len(lines) != 3 {
		t.Fatalf("expected 3 digest lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "read_file(path=a.go) x2" {
		t.Fatalf("expected deduped read_file with count, got %q", lines[0])
	}
	if lines[1] != "bash(cmd=ls -la)" {
		t.Fatalf("expected runtime-form bash signature, got %q", lines[1])
	}
	if lines[2] != "no_args" {
		t.Fatalf("expected bare name for empty args, got %q", lines[2])
	}
}

func TestFormatToolArgs_TruncatesAndPrioritizes(t *testing.T) {
	long := strings.Repeat("x", 500)
	args := map[string]any{
		"zzz_other": long,
		"path":      "some/long/path.go",
	}
	out := formatToolArgs(args)
	if !strings.Contains(out, "path=some/long/path.go") {
		t.Fatalf("expected priority key first, got %q", out)
	}
	if len([]rune(out)) > maxToolDigestArgRunes+20 {
		t.Fatalf("expected args truncated near %d runes, got %d", maxToolDigestArgRunes, len([]rune(out)))
	}
}

func TestRetryLLMCall_UsesLightProviderWhenConfigured(t *testing.T) {
	mainProvider := &summarizingRecordingProvider{response: "main summary"}
	al := newLegacyTestAgentLoop(t, mainProvider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	lightProvider := &summarizingRecordingProvider{response: "light summary"}
	agent.LightProvider = lightProvider
	agent.LightCandidates = []providers.FallbackCandidate{
		{Provider: "test", Model: "light-model", DisplayName: "light-model"},
	}

	lcm := &legacyContextManager{al: al}
	resp, err := lcm.retryLLMCall(context.Background(), agent, "summarize this", 3)
	if err != nil {
		t.Fatalf("retryLLMCall failed: %v", err)
	}
	if resp.Content != "light summary" {
		t.Fatalf("expected light provider response, got %q", resp.Content)
	}
	if lightProvider.lastModel() != "light-model" {
		t.Fatalf("expected light model name, got %q", lightProvider.lastModel())
	}
	if mainProvider.callCount() != 0 {
		t.Fatalf("expected main provider to be idle, got %d calls", mainProvider.callCount())
	}
}

func TestRetryLLMCall_FallsBackToMainProviderWithoutRouting(t *testing.T) {
	mainProvider := &summarizingRecordingProvider{response: "main summary"}
	al := newLegacyTestAgentLoop(t, mainProvider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	lcm := &legacyContextManager{al: al}
	resp, err := lcm.retryLLMCall(context.Background(), agent, "summarize this", 3)
	if err != nil {
		t.Fatalf("retryLLMCall failed: %v", err)
	}
	if resp.Content != "main summary" {
		t.Fatalf("expected main provider response, got %q", resp.Content)
	}
}

func TestRetryLLMCall_FailsFastOnBillingError(t *testing.T) {
	provider := &summarizingRecordingProvider{errText: "402 payment required: insufficient credits"}
	al := newLegacyTestAgentLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	lcm := &legacyContextManager{al: al}
	_, err := lcm.retryLLMCall(context.Background(), agent, "summarize this", 3)
	if err == nil {
		t.Fatal("expected error from billing failure")
	}
	if calls := provider.callCount(); calls != 1 {
		t.Fatalf("expected exactly 1 call (fail fast), got %d", calls)
	}
}

func TestRetryLLMCall_RetriesTransientError(t *testing.T) {
	provider := &summarizingRecordingProvider{errText: "connection reset by peer"}
	al := newLegacyTestAgentLoop(t, provider)
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	lcm := &legacyContextManager{al: al}
	_, err := lcm.retryLLMCall(context.Background(), agent, "summarize this", 3)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if calls := provider.callCount(); calls != 3 {
		t.Fatalf("expected 3 calls for transient error, got %d", calls)
	}
}
