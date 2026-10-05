package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// --- turn-in-flight marker recovery (2026-10-05 gap fixes) ---

// turnMarkerFile mirrors pkg/memory.sanitizeKey + the sessions dir layout so
// tests can assert on the marker file directly.
func turnMarkerFile(agent *AgentInstance, key string) string {
	s := strings.ReplaceAll(key, ":", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return filepath.Join(agent.Workspace, "sessions", s+".turnmarker.json")
}

func writeTestTurnMarker(t *testing.T, agent *AgentInstance, key, agentID, model string) {
	t.Helper()
	markerStore, ok := agent.Sessions.(session.InflightTurnStore)
	if !ok {
		t.Fatalf("session store does not support turn markers")
	}
	markerStore.MarkTurnInFlight(key, agentID, model)
	if _, err := os.Stat(turnMarkerFile(agent, key)); err != nil {
		t.Fatalf("test marker not written: %v", err)
	}
}

func markerFileExists(agent *AgentInstance, key string) bool {
	_, err := os.Stat(turnMarkerFile(agent, key))
	return err == nil
}

// A marker surviving a restart on a session whose tail is clean (killed
// mid-LLM-generation) must still notify — previously this interruption shape
// was silent because sealing never happened.
func TestRunRestartRecovery_NotifiesCleanTailInterruption(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:midgen"
	history := []providers.Message{
		{Role: "user", Content: "big task"},
		{Role: "assistant", Content: "working on it",
			ToolCalls: []providers.ToolCall{{ID: "c1", Type: "function", Name: "exec",
				Arguments: map[string]any{"command": "ls"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "继续"},
		// The turn died while generating the answer: tail is the user
		// message, no dangling tool calls.
	}
	agent.Sessions.SetHistory(key, history)
	seedSessionScope(t, agent, key, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, key, "default", "test-model")

	al.RunRestartRecovery(context.Background())

	got := oc.text()
	if !strings.Contains(got, "回答未能完成") || !strings.Contains(got, "继续") {
		t.Fatalf("expected unsealed interruption notice, got: %q", got)
	}
	if h := agent.Sessions.GetHistory(key); len(h) != len(history) {
		t.Fatalf("clean-tail session must not gain a seal: %d msgs, want %d", len(h), len(history))
	}
	if markerFileExists(agent, key) {
		t.Fatal("marker must be consumed by recovery")
	}
}

// A marker whose tail is a persisted final answer means the turn finished
// before the process died — the stale write-ahead marker is cleared silently.
func TestRunRestartRecovery_MarkerCompletedTailSilent(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:done"
	agent.Sessions.SetHistory(key, []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "final answer"},
	})
	seedSessionScope(t, agent, key, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, key, "default", "test-model")

	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("completed turn must not be reported as interrupted")
	}
	if markerFileExists(agent, key) {
		t.Fatal("stale marker must be cleared")
	}
}

// A marker for a session with no durable history (turn died before its first
// message persisted) has nothing to resume — cleared silently.
func TestRunRestartRecovery_MarkerWithoutHistoryClearedSilently(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:nohistory"
	writeTestTurnMarker(t, agent, key, "default", "test-model")

	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("empty session must not be reported as interrupted")
	}
	if markerFileExists(agent, key) {
		t.Fatal("orphan marker must be cleared")
	}
}

// Dangling tool calls plus a marker: sealing and the marker describe the same
// interruption — exactly one notification, sealed wording, marker consumed.
func TestRunRestartRecovery_MarkerPlusDanglingNotifiesOnce(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:both"
	agent.Sessions.SetHistory(key, danglingHistory())
	seedSessionScope(t, agent, key, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, key, "default", "test-model")

	al.RunRestartRecovery(context.Background())

	if n := strings.Count(oc.text(), "检测到上次任务被中断"); n != 1 {
		t.Fatalf("expected exactly 1 notification, got %d: %q", n, oc.text())
	}
	if !strings.Contains(oc.text(), "已封口保留") {
		t.Fatalf("dangling interruption must use the sealed wording, got: %q", oc.text())
	}
	if h := agent.Sessions.GetHistory(key); len(h) != 3 {
		t.Fatalf("session should be sealed: %d msgs, want 3", len(h))
	}
	if markerFileExists(agent, key) {
		t.Fatal("marker must be consumed by recovery")
	}
}

// A marker whose session has an active turn belongs to a live turn of this
// process — recovery must neither notify nor consume it.
func TestRunRestartRecovery_LeavesLiveTurnMarkerAlone(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:live"
	agent.Sessions.SetHistory(key, []providers.Message{{Role: "user", Content: "go"}})
	seedSessionScope(t, agent, key, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, key, "default", "test-model")

	al.activeTurnStates.Store(key, &turnState{sessionKey: key})
	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("live turn must not be reported as interrupted")
	}
	if !markerFileExists(agent, key) {
		t.Fatal("live turn's marker must be left for the running turn to clear")
	}

	// Once the turn is gone, the same marker is a real interruption.
	al.activeTurnStates.Delete(key)
	al.RunRestartRecovery(context.Background())
	if !strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("marker should notify once no turn is active")
	}
	if markerFileExists(agent, key) {
		t.Fatal("marker must be consumed after the delayed recovery")
	}
}

// The marker carries the model the interrupted turn used; recovery restores
// it so resuming continues on the task's model instead of the config default
// (/switch state is in-memory only). A model no longer in model_list is kept
// silent — recovery never swaps to an unresolvable model.
func TestRunRestartRecovery_RestoresInterruptedModel(t *testing.T) {
	calls := 0
	seenModel := ""
	server := newChatCompletionTestServer(t, "models", "ok", &calls, &seenModel)
	defer server.Close()

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				Provider:          "openai",
				ModelName:         "primary",
				MaxTokens:         64,
				MaxToolIterations: 2,
				RestartRecovery: config.RestartRecoveryConfig{
					NotifyWindowHours:       24,
					ReminderIntervalMinutes: 0,
				},
			},
		},
		ModelList: []*config.ModelConfig{
			{ModelName: "primary", Model: "openai/primary-model", APIBase: server.URL,
				APIKeys: config.SimpleSecureStrings("k1")},
			{ModelName: "alt", Model: "openai/alt-model", APIBase: server.URL,
				APIKeys: config.SimpleSecureStrings("k1")},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	oc := captureOutbound(msgBus)

	if agent.Model != "primary" {
		t.Fatalf("fixture agent model = %q, want primary", agent.Model)
	}

	key := "agent_default:test:model-restore"
	agent.Sessions.SetHistory(key, []providers.Message{{Role: "user", Content: "task"}})
	seedSessionScope(t, agent, key, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, key, agent.ID, "alt")

	ghostKey := "agent_default:test:model-ghost"
	agent.Sessions.SetHistory(ghostKey, []providers.Message{{Role: "user", Content: "task"}})
	seedSessionScope(t, agent, ghostKey, "test", "direct:chat1")
	writeTestTurnMarker(t, agent, ghostKey, agent.ID, "removed-model")

	al.RunRestartRecovery(context.Background())

	if agent.Model != "alt" {
		t.Fatalf("agent.Model after recovery = %q, want alt (interrupted turn's model)", agent.Model)
	}
	if !strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatalf("expected interruption notifications, got: %q", oc.text())
	}
	if markerFileExists(agent, key) || markerFileExists(agent, ghostKey) {
		t.Fatal("both markers must be consumed")
	}
}

// gatedResponseProvider blocks in Chat until released, letting tests observe
// in-flight turn state (the marker must exist by the first LLM call).
type gatedResponseProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *gatedResponseProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	m.once.Do(func() { close(m.entered) })
	<-m.release
	return &providers.LLMResponse{Content: "done", ToolCalls: []providers.ToolCall{}}, nil
}

func (m *gatedResponseProvider) GetDefaultModel() string { return "mock-model" }

func TestRunAgentLoop_TurnMarkerLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 2,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	provider := &gatedResponseProvider{entered: make(chan struct{}), release: make(chan struct{})}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()

	key := "agent_default:test:lifecycle"
	done := make(chan error, 1)
	go func() {
		_, err := al.runAgentLoop(context.Background(), agent, processOptions{
			SessionKey:      key,
			Channel:         "pico",
			ChatID:          "chat-1",
			UserMessage:     "hello",
			SendResponse:    false,
			DefaultResponse: "ok",
		})
		done <- err
	}()

	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider never entered Chat")
	}
	if !markerFileExists(agent, key) {
		t.Fatal("turn marker must exist while the turn is in flight")
	}

	close(provider.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentLoop() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	if markerFileExists(agent, key) {
		t.Fatal("turn marker must be cleared on turn exit")
	}
}

// NoHistory turns persist nothing; a marker for them would be unrecoverable
// noise, so runAgentLoop must not write one.
func TestRunAgentLoop_NoHistoryTurnWritesNoMarker(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 2,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	provider := &gatedResponseProvider{entered: make(chan struct{}), release: make(chan struct{})}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()

	key := "agent_default:test:nohistory-turn"
	done := make(chan error, 1)
	go func() {
		_, err := al.runAgentLoop(context.Background(), agent, processOptions{
			SessionKey:      key,
			Channel:         "pico",
			ChatID:          "chat-1",
			UserMessage:     "hello",
			SendResponse:    false,
			DefaultResponse: "ok",
			NoHistory:       true,
		})
		done <- err
	}()

	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("provider never entered Chat")
	}
	close(provider.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentLoop() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	if markerFileExists(agent, key) {
		t.Fatal("NoHistory turn must not write a turn marker")
	}
}
