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
