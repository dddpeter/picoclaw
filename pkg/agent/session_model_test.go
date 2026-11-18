package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// sessionModelTestProvider is a no-op LLM provider for the session-model
// tests (the feature never calls the model itself).
type sessionModelTestProvider struct{}

func (p *sessionModelTestProvider) Chat(
	_ interface{ Done() <-chan struct{} },
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "ok"}, nil
}

func newSessionModelTestLoop(t *testing.T) *AgentLoop {
	t.Helper()
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:      t.TempDir(),
				Provider:       "openai",
				ModelName:      "primary",
				MaxTokens:      4096,
				ContextWindow:  100_000,
				ModelFallbacks: []string{"fallback"},
			},
		},
		ModelList: []*config.ModelConfig{
			{ModelName: "primary", Model: "openai/primary-model", APIKeys: config.SimpleSecureStrings("k1")},
			{ModelName: "small", Model: "openai/small-model", APIKeys: config.SimpleSecureStrings("k2"), ContextWindow: 32_000},
			{ModelName: "fallback", Model: "openai/fallback-model", APIKeys: config.SimpleSecureStrings("k3")},
		},
	}
	return NewAgentLoop(cfg, bus.NewMessageBus(), nil)
}

// A session-scoped model must NOT touch the config file, and must NOT
// mutate the agent's global model — the agent keeps running the configured
// model while the session runs the chosen one.
func TestSetSessionModel_DoesNotMutateAgentModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("no default agent")
	}
	originalModel := agent.Model
	originalWindow := agent.ContextWindow
	originalDeclared := agent.DeclaredContextWindow

	sessionKey := "sk_v1_test"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	if agent.Model != originalModel {
		t.Fatalf("agent model mutated: %q -> %q", originalModel, agent.Model)
	}
	if agent.ContextWindow != originalWindow {
		t.Fatalf("agent context window mutated: %d -> %d", originalWindow, agent.ContextWindow)
	}
	if agent.DeclaredContextWindow != originalDeclared {
		t.Fatalf("agent declared window mutated: %d -> %d", originalDeclared, agent.DeclaredContextWindow)
	}
	if got := al.GetSessionModel(sessionKey); got != "small" {
		t.Fatalf("GetSessionModel = %q, want %q", got, "small")
	}
}

// An unknown model must be rejected — a session-scoped model is a choice
// among CONFIGURED models, never an ad-hoc provider.
func TestSetSessionModel_RejectsUnknownModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	if err := al.SetSessionModel("sk_v1_test", "not-configured"); err == nil {
		t.Fatal("expected an error for an unconfigured model")
	}
	if got := al.GetSessionModel("sk_v1_test"); got != "" {
		t.Fatalf("rejected model was stored: %q", got)
	}
}

// The resolved session model carries the model's own (min-capped) context
// window, and the agent's window stays untouched.
func TestResolveSessionModel_WindowFollowsModel(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()

	sessionKey := "sk_v1_window"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}

	resolved := al.resolveSessionModel(agent, sessionKey)
	if resolved == nil {
		t.Fatal("session model did not resolve")
	}
	if resolved.ModelName != "small" {
		t.Fatalf("resolved model = %q, want %q", resolved.ModelName, "small")
	}
	// The per-model window (32k) min-caps the agent's window (100k).
	if resolved.ContextWindow != 32_000 {
		t.Fatalf("resolved window = %d, want 32000", resolved.ContextWindow)
	}
	if resolved.DeclaredContextWindow != 32_000 {
		t.Fatalf("resolved declared window = %d, want 32000", resolved.DeclaredContextWindow)
	}
	// The agent's own window is unchanged.
	if agent.ContextWindow != 100_000 {
		t.Fatalf("agent window mutated: %d", agent.ContextWindow)
	}
}

// A session WITHOUT an override resolves to nothing — the turn then uses
// the agent's configured model, exactly as before.
func TestResolveSessionModel_NoOverride(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	if resolved := al.resolveSessionModel(agent, "sk_v1_none"); resolved != nil {
		t.Fatalf("expected no session model, got %+v", resolved)
	}
}

// Clearing the override returns the session to the configured model.
func TestSetSessionModel_Clear(t *testing.T) {
	al := newSessionModelTestLoop(t)
	sessionKey := "sk_v1_clear"
	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if err := al.SetSessionModel(sessionKey, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := al.GetSessionModel(sessionKey); got != "" {
		t.Fatalf("override not cleared: %q", got)
	}
}

// The context usage of a session-scoped model reports the model's window
// as the total — the UI's context ring must match the model in use.
func TestSessionContextUsage_UsesSessionWindow(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()
	sessionKey := "sk_v1_usage"

	if err := al.SetSessionModel(sessionKey, "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	// Refresh the cached windows the way a turn would.
	if resolved := al.resolveSessionModel(agent, sessionKey); resolved == nil {
		t.Fatal("session model did not resolve")
	}

	usage := al.SessionContextUsage(agent, sessionKey)
	if usage == nil {
		t.Fatal("no usage computed")
	}
	if usage.TotalTokens != 32_000 {
		t.Fatalf("usage total = %d, want 32000", usage.TotalTokens)
	}
}

// The session model survives independently of the agent's model: setting it
// on one session never affects another session.
func TestSetSessionModel_IsolatedPerSession(t *testing.T) {
	al := newSessionModelTestLoop(t)
	agent := al.GetRegistry().GetDefaultAgent()

	if err := al.SetSessionModel("sk_v1_a", "small"); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if resolved := al.resolveSessionModel(agent, "sk_v1_b"); resolved != nil {
		t.Fatalf("session B inherited session A's model: %+v", resolved)
	}
}
