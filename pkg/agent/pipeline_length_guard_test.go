package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// truncatedToolCallProvider returns a tool call whose message was cut off by
// the output token limit, then recovers with a plain answer.
type truncatedToolCallProvider struct {
	calls int
}

func (m *truncatedToolCallProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "counting_tool", Arguments: map[string]any{"task": "maybe-missing-args"}},
			},
			FinishReason: "length",
		}, nil
	}
	return &providers.LLMResponse{Content: "re-issued without tools"}, nil
}

func (m *truncatedToolCallProvider) GetDefaultModel() string {
	return "truncated-tool-model"
}

type countingTool struct {
	executions int
}

func (c *countingTool) Name() string        { return "counting_tool" }
func (c *countingTool) Description() string { return "Counts executions" }
func (c *countingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": true}
}

func (c *countingTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	c.executions++
	return tools.SilentResult("ok")
}

func TestExecuteTools_RejectsToolCallsWhenTruncatedByTokenLimit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-length-guard-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}

	msgBus := bus.NewMessageBus()
	provider := &truncatedToolCallProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	counter := &countingTool{}
	al.RegisterTool(counter)
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(t, al, 8,
		runtimeevents.KindAgentToolExecSkipped)
	defer closeRuntimeEvents()

	response, err := al.runAgentLoop(context.Background(), defaultAgent, processOptions{
		SessionKey:      "session-length",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		InboundContext: &bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "tester",
		},
		RouteResult: &routing.ResolvedRoute{
			AgentID:   "main",
			Channel:   "cli",
			AccountID: routing.DefaultAccountID,
			SessionPolicy: routing.SessionPolicy{
				Dimensions: []string{"sender"},
			},
			MatchedBy: "default",
		},
		SessionScope: &session.SessionScope{
			Version:    session.ScopeVersionV1,
			AgentID:    "main",
			Channel:    "cli",
			Account:    routing.DefaultAccountID,
			Dimensions: []string{"sender"},
			Values:     map[string]string{"sender": "tester"},
		},
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if response != "re-issued without tools" {
		t.Fatalf("expected recovered response, got %q", response)
	}

	if counter.executions != 0 {
		t.Fatalf("expected tool NOT to execute under length-truncated response, got %d executions", counter.executions)
	}

	events := collectRuntimeEventStream(runtimeCh)
	if len(events) == 0 {
		t.Fatal("expected a tool exec skipped event")
	}
	payload, ok := events[0].Payload.(ToolExecSkippedPayload)
	if !ok {
		t.Fatalf("expected ToolExecSkippedPayload, got %T", events[0].Payload)
	}
	if payload.Tool != "counting_tool" || !strings.Contains(payload.Reason, "output token limit") {
		t.Fatalf("unexpected skipped payload: %+v", payload)
	}

	history := defaultAgent.Sessions.GetHistory("session-length")
	foundDenied := false
	for _, msg := range history {
		if msg.Role == "tool" && strings.Contains(msg.Content, "not executed") {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Fatal("expected denied tool message persisted to history")
	}
}

// scriptedAnswerProvider replays scripted direct answers, recording the
// message list each call received.
type scriptedAnswerProvider struct {
	responses []providers.LLMResponse
	calls     [][]providers.Message
}

func (p *scriptedAnswerProvider) Chat(
	_ context.Context,
	msgs []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls = append(p.calls, append([]providers.Message(nil), msgs...))
	r := p.responses[min(len(p.calls)-1, len(p.responses)-1)]
	return &r, nil
}

func (p *scriptedAnswerProvider) GetDefaultModel() string {
	return "scripted-answer-model"
}

// TestDirectAnswerTruncatedByTokenLimit (fork, 2026-10-05): direct answers cut
// by an output cap are auto-continued and stitched; the truncation note only
// appears when the continuation budget is exhausted. A normal finish stays
// verbatim.
func TestDirectAnswerTruncatedByTokenLimit(t *testing.T) {
	t.Run("continuation stitches complete answer without note", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "第一段", FinishReason: "length"},
			{Content: "第二段", FinishReason: "length"},
			{Content: "，第三段完", FinishReason: "stop"},
		}}
		response, al := runLengthNoteTurn(t, provider, "session-len-recover")
		if want := "第一段第二段，第三段完"; response != want {
			t.Fatalf("response = %q, want %q", response, want)
		}
		if len(provider.calls) != 3 {
			t.Fatalf("LLM calls = %d, want 3", len(provider.calls))
		}
		// The continuation call must see the partial answer plus the
		// continuation directive as the trailing request messages.
		second := provider.calls[1]
		if len(second) < 3 || second[len(second)-2].Role != "assistant" || second[len(second)-2].Content != "第一段" {
			t.Fatalf("continuation request misses partial assistant piece: %+v", second[len(second)-2:])
		}
		if last := second[len(second)-1]; last.Role != "user" || !strings.Contains(last.Content, "续写") {
			t.Fatalf("continuation directive missing: %+v", last)
		}
		// History carries the stitched answer exactly once; pieces and the
		// directive are request-view only.
		history := al.registry.GetDefaultAgent().Sessions.GetHistory("session-len-recover")
		assistants := 0
		directives := 0
		for _, m := range history {
			if m.Role == "assistant" {
				assistants++
				if m.Content != "第一段第二段，第三段完" {
					t.Fatalf("history assistant content = %q", m.Content)
				}
			}
			if m.Role == "user" && strings.Contains(m.Content, "续写") {
				directives++
			}
		}
		if assistants != 1 {
			t.Fatalf("history assistant messages = %d, want 1", assistants)
		}
		if directives != 0 {
			t.Fatalf("continuation directive leaked into history %d times", directives)
		}
	})

	t.Run("exhausted budget keeps pieces and appends note", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "段", FinishReason: "length"},
		}}
		response, _ := runLengthNoteTurn(t, provider, "session-len-exhaust")
		if want := "段段段段" + truncatedAnswerNote; response != want {
			t.Fatalf("response = %q, want %q", response, want)
		}
		// 1 original + 3 default continuation rounds (config knob
		// answer_continuation_limit is unset in this test config).
		if len(provider.calls) != 4 {
			t.Fatalf("LLM calls = %d, want 4", len(provider.calls))
		}
	})

	t.Run("stop stays verbatim", func(t *testing.T) {
		provider := &scriptedAnswerProvider{responses: []providers.LLMResponse{
			{Content: "写到一半的话", FinishReason: "stop"},
		}}
		response, _ := runLengthNoteTurn(t, provider, "session-len-stop")
		if response != "写到一半的话" {
			t.Fatalf("response = %q, want 写到一半的话", response)
		}
		if len(provider.calls) != 1 {
			t.Fatalf("LLM calls = %d, want 1", len(provider.calls))
		}
	})
}

func runLengthNoteTurn(t *testing.T, provider providers.LLMProvider, sessionKey string) (string, *AgentLoop) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "agent-length-note-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	// t.Cleanup, not defer: callers assert against session history after
	// this helper returns, so the workspace must outlive the helper.
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	response, err := al.runAgentLoop(context.Background(), defaultAgent, processOptions{
		SessionKey:      sessionKey,
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "say something",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
		InboundContext: &bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "tester",
		},
		RouteResult: &routing.ResolvedRoute{
			AgentID:   "main",
			Channel:   "cli",
			AccountID: routing.DefaultAccountID,
			SessionPolicy: routing.SessionPolicy{
				Dimensions: []string{"sender"},
			},
			MatchedBy: "default",
		},
		SessionScope: &session.SessionScope{
			Version:    session.ScopeVersionV1,
			AgentID:    "main",
			Channel:    "cli",
			Account:    routing.DefaultAccountID,
			Dimensions: []string{"sender"},
			Values:     map[string]string{"sender": "tester"},
		},
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	return response, al
}

func TestToolStepKind(t *testing.T) {
	if got := toolStepKind("mcp_fetch_search"); got != bus.ToolStepKindMCP {
		t.Fatalf("expected mcp kind, got %q", got)
	}
	if got := toolStepKind("read_file"); got != bus.ToolStepKindTool {
		t.Fatalf("expected tool kind, got %q", got)
	}
}

type toolStepRecordingStreamer struct {
	recordingStreamer
	steps []bus.ToolStep
}

func (s *toolStepRecordingStreamer) AppendToolStep(_ context.Context, step bus.ToolStep) error {
	s.steps = append(s.steps, step)
	return nil
}

func TestSeedSkillPanelStep(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	ts := newTurnState(agent, processOptions{SessionKey: "session-skills"}, al.newTurnEventScope(agent.ID, "session-skills", nil))
	ts.recordSkillContextSnapshot("initial", []string{"pdf-reader", "web-lookup"})

	streamer := &toolStepRecordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: streamer}
	exec := &turnExecution{}

	seedSkillPanelStep(context.Background(), publisher, ts, exec)
	seedSkillPanelStep(context.Background(), publisher, ts, exec)

	if len(streamer.steps) != 1 {
		t.Fatalf("expected exactly 1 seeded skill step, got %d", len(streamer.steps))
	}
	step := streamer.steps[0]
	if step.Kind != bus.ToolStepKindSkill {
		t.Fatalf("expected skill kind, got %q", step.Kind)
	}
	if !strings.Contains(step.Tool, "pdf-reader") || !strings.Contains(step.Tool, "web-lookup") {
		t.Fatalf("expected skill names in step, got %q", step.Tool)
	}
}

func TestSeedSkillPanelStep_NoSkills(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	ts := newTurnState(agent, processOptions{SessionKey: "session-noskills"}, al.newTurnEventScope(agent.ID, "session-noskills", nil))

	streamer := &toolStepRecordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: streamer}
	exec := &turnExecution{}

	seedSkillPanelStep(context.Background(), publisher, ts, exec)

	if len(streamer.steps) != 0 {
		t.Fatalf("expected no skill steps, got %d", len(streamer.steps))
	}
}

// TestAppendToolStepAllowsUnnamedTextArchives: mid-turn text archives carry no
// tool name by design and must survive the publisher gate; every other
// unnamed step is still dropped.
func TestAppendToolStepAllowsUnnamedTextArchives(t *testing.T) {
	streamer := &toolStepRecordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: streamer}

	publisher.AppendToolStep(context.Background(), bus.ToolStep{Kind: bus.ToolStepKindText, Result: "中间说明"})
	publisher.AppendToolStep(context.Background(), bus.ToolStep{Result: "unnamed execution"})

	if len(streamer.steps) != 1 {
		t.Fatalf("expected only the text archive to pass the gate, got %d steps", len(streamer.steps))
	}
	if step := streamer.steps[0]; step.Kind != bus.ToolStepKindText || step.Result != "中间说明" {
		t.Fatalf("unexpected surviving step: %+v", step)
	}
}

// TestAnswerContinuationLimitConfig pins the config knob: nil defaults to 3,
// explicit values pass through, 0 and negatives disable.
func TestAnswerContinuationLimitConfig(t *testing.T) {
	def := &config.AgentDefaults{}
	if got := def.GetAnswerContinuationLimit(); got != 3 {
		t.Fatalf("default limit = %d, want 3", got)
	}
	v := 7
	if got := (&config.AgentDefaults{AnswerContinuationLimit: &v}).GetAnswerContinuationLimit(); got != 7 {
		t.Fatalf("explicit limit = %d, want 7", got)
	}
	zero := 0
	if got := (&config.AgentDefaults{AnswerContinuationLimit: &zero}).GetAnswerContinuationLimit(); got != 0 {
		t.Fatalf("zero limit = %d, want 0 (disabled)", got)
	}
	neg := -2
	if got := (&config.AgentDefaults{AnswerContinuationLimit: &neg}).GetAnswerContinuationLimit(); got != 0 {
		t.Fatalf("negative limit = %d, want 0 (disabled)", got)
	}
}
