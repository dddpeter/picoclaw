package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// degradedStormProvider simulates a 429 storm on the primary model: every
// primary-model call fails with a retriable rate-limit error while the
// fallback model keeps the loop alive with tool calls. Both candidates share
// one provider instance, so Chat dispatches on the model name.
type degradedStormProvider struct {
	mu            sync.Mutex
	fallbackCalls int
	// fallbackToolCalls is how many tool-call responses the fallback model
	// emits before answering with plain text.
	fallbackToolCalls int
}

func (p *degradedStormProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if model != "fallback-model" {
		return nil, errors.New("rate limit exceeded")
	}
	p.mu.Lock()
	p.fallbackCalls++
	n := p.fallbackCalls
	p.mu.Unlock()
	if n <= p.fallbackToolCalls {
		return &providers.LLMResponse{
			Content: "working via fallback",
			ToolCalls: []providers.ToolCall{{
				ID:        fmt.Sprintf("call_%d", n),
				Type:      "function",
				Name:      "mock_custom",
				Arguments: map[string]any{},
			}},
		}, nil
	}
	return &providers.LLMResponse{Content: "done via fallback", FinishReason: "stop"}, nil
}

func (p *degradedStormProvider) GetDefaultModel() string { return "primary-model" }

// streakCountingTool is a mock_custom-compatible tool with a call counter
// so the streak test can assert the breaker fires before the Nth tool round
// executes.
type streakCountingTool struct {
	mu    sync.Mutex
	calls int
}

func (c *streakCountingTool) Name() string        { return "mock_custom" }
func (c *streakCountingTool) Description() string { return "counting mock tool" }
func (c *streakCountingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (c *streakCountingTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return tools.SilentResult("ok")
}

func newDegradedStormLoop(t *testing.T, provider providers.LLMProvider, tool tools.Tool) *AgentLoop {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "primary-model",
				ModelFallbacks:    []string{"openai/fallback-model"},
				MaxTokens:         4096,
				MaxToolIterations: 30,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "primary-model",
			Model:     "openai/primary-model",
		}},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	if tool != nil {
		al.RegisterTool(tool)
	}
	return al
}

func runDegradedStormTurn(al *AgentLoop) (string, error) {
	return al.runAgentLoop(
		context.Background(),
		al.GetRegistry().GetDefaultAgent(),
		processOptions{
			SessionKey:      "agent:main:pico:chat-degraded",
			Channel:         "pico",
			ChatID:          "chat-degraded",
			UserMessage:     "keep working",
			SendResponse:    false,
			DefaultResponse: defaultResponse,
			NoHistory:       true,
		},
	)
}

// TestDegradedStreak_AbortsTurnAtLimit verifies the heartbeat circuit
// breaker: a turn whose iterations are all served by fallback candidates
// (primary 429-limited) aborts after degradedStreakAbortLimit consecutive
// degraded iterations, before executing that iteration's tool calls.
func TestDegradedStreak_AbortsTurnAtLimit(t *testing.T) {
	provider := &degradedStormProvider{fallbackToolCalls: 20} // never runs out
	tool := &streakCountingTool{}
	al := newDegradedStormLoop(t, provider, tool)

	_, err := runDegradedStormTurn(al)
	if err == nil {
		t.Fatal("expected turn to abort with a degraded-streak error")
	}
	if !strings.Contains(err.Error(), "fallback degraded streak") {
		t.Fatalf("error = %q, want it to mention the degraded streak", err)
	}

	provider.mu.Lock()
	fallbackCalls := provider.fallbackCalls
	provider.mu.Unlock()
	if fallbackCalls != degradedStreakAbortLimit {
		t.Fatalf("fallback served %d iterations, want exactly %d (breaker fires on the Nth)",
			fallbackCalls, degradedStreakAbortLimit)
	}

	tool.mu.Lock()
	toolCalls := tool.calls
	tool.mu.Unlock()
	if toolCalls != degradedStreakAbortLimit-1 {
		t.Fatalf("tool executed %d times, want %d (the breaking iteration's tools must not run)",
			toolCalls, degradedStreakAbortLimit-1)
	}
}

// TestDegradedStreak_UnderLimitCompletesTurn verifies a turn that only
// degrades a few iterations before finishing normally: the breaker must not
// fire below the threshold and the fallback-served answer is delivered.
func TestDegradedStreak_UnderLimitCompletesTurn(t *testing.T) {
	provider := &degradedStormProvider{fallbackToolCalls: 2} // 2 degraded iterations, then answer
	al := newDegradedStormLoop(t, provider, &streakCountingTool{})

	response, err := runDegradedStormTurn(al)
	if err != nil {
		t.Fatalf("turn under the streak limit must complete, got error: %v", err)
	}
	if response != "done via fallback" {
		t.Fatalf("response = %q, want the fallback-served answer", response)
	}
}
