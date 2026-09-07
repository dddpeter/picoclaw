package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newResetTestConfig(workspace, defaultModel string) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         workspace,
				Provider:          "openai",
				ModelName:         defaultModel,
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "local",
				Model:     "openai/local-model",
				APIBase:   "https://local.example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
			},
			{
				ModelName: "deepseek",
				Model:     "openrouter/deepseek/deepseek-v3.2",
				APIBase:   "https://openrouter.ai/api/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
			},
		},
	}
}

func TestProcessMessage_NewResetsSwitchedModelToDefault(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := newResetTestConfig(tmpDir, "local")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	switchResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/switch model to deepseek",
	})
	if !strings.Contains(switchResp, "Switched model from local to deepseek") {
		t.Fatalf("unexpected /switch reply: %q", switchResp)
	}

	newResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/new",
	})
	if !strings.Contains(newResp, "New conversation started") || !strings.Contains(newResp, "Model: local") {
		t.Fatalf("unexpected /new reply: %q, want reset back to default model", newResp)
	}

	showResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/show model",
	})
	if !strings.Contains(showResp, "Current Model: local") {
		t.Fatalf("model should be back on default after /new: %q", showResp)
	}

	if provider.calls != 0 {
		t.Fatalf("LLM should not be called for /switch, /new and /show, calls=%d", provider.calls)
	}
}

func TestProcessMessage_NewPicksUpDiskConfigDefaultModel(t *testing.T) {
	tmpDir := t.TempDir()

	// In-memory config still points at "local"; the config file on disk has
	// already been edited to default to "deepseek" (plus its model_list entry).
	cfg := newResetTestConfig(tmpDir, "local")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)

	diskCfg := map[string]any{
		"version": 3,
		"agents": map[string]any{
			"defaults": map[string]any{
				"provider":   "openai",
				"model_name": "deepseek",
				"workspace":  tmpDir,
			},
		},
		"model_list": []map[string]any{
			{"model_name": "local", "model": "openai/local-model", "api_base": "https://local.example.invalid/v1", "api_keys": []string{"test-key"}},
			{"model_name": "deepseek", "model": "openrouter/deepseek/deepseek-v3.2", "api_base": "https://openrouter.ai/api/v1", "api_keys": []string{"test-key"}},
		},
	}
	raw, err := json.Marshal(diskCfg)
	if err != nil {
		t.Fatalf("marshal disk config: %v", err)
	}
	configPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatalf("write disk config: %v", err)
	}
	al.SetConfigPath(configPath)

	helper := testHelper{al: al}
	newResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/new",
	})
	if !strings.Contains(newResp, "New conversation started") || !strings.Contains(newResp, "Model: deepseek") {
		t.Fatalf("unexpected /new reply: %q, want disk-config default model", newResp)
	}

	showResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/show model",
	})
	if !strings.Contains(showResp, "Current Model: deepseek") {
		t.Fatalf("model should follow the disk config default after /new: %q", showResp)
	}

	if provider.calls != 0 {
		t.Fatalf("LLM should not be called for /new and /show, calls=%d", provider.calls)
	}
}

// A hung or in-flight turn holds the model state read lock for its whole
// lifetime (runTurn). /new must never block behind that lock: when the model
// already matches the default it is a no-op, and when a switch would be
// needed but the lock is held it must degrade to a skip warning.
func TestProcessMessage_NewNeverBlocksBehindTurnReadLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Default is "deepseek"; switch to "local" so /new would need the write lock.
	cfg := newResetTestConfig(tmpDir, "deepseek")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	switchResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/switch model to local",
	})
	if !strings.Contains(switchResp, "Switched model from deepseek to local") {
		t.Fatalf("unexpected /switch reply: %q", switchResp)
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent not found")
	}
	modelMu := agent.modelStateMutex()
	modelMu.RLock()
	defer modelMu.RUnlock()

	done := make(chan string, 1)
	go func() {
		done <- helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
			Channel:  "telegram",
			SenderID: "user1",
			ChatID:   "chat1",
			Content:  "/new",
		})
	}()

	select {
	case reply := <-done:
		if !strings.Contains(reply, "New conversation started") {
			t.Fatalf("/new reply = %q, want conversation reset to still be reported", reply)
		}
		if !strings.Contains(reply, "model reset skipped") {
			t.Fatalf("/new reply = %q, want skip warning while a turn holds model state", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/new deadlocked behind the turn read lock")
	}
}

// When the current model already equals the configured default, /new stays a
// pure no-op on the model path — no write lock attempt, no warning.
func TestProcessMessage_NewNoopWhileTurnHoldsReadLock(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := newResetTestConfig(tmpDir, "local")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent not found")
	}
	modelMu := agent.modelStateMutex()
	modelMu.RLock()
	defer modelMu.RUnlock()

	done := make(chan string, 1)
	go func() {
		done <- helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
			Channel:  "telegram",
			SenderID: "user1",
			ChatID:   "chat1",
			Content:  "/new",
		})
	}()

	select {
	case reply := <-done:
		if !strings.Contains(reply, "New conversation started") || !strings.Contains(reply, "Model: local") {
			t.Fatalf("/new reply = %q, want clean no-op reset reporting the default model", reply)
		}
		if strings.Contains(reply, "skipped") || strings.Contains(reply, "reset failed") {
			t.Fatalf("/new reply = %q, no-op path should not warn", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("/new deadlocked on the no-op path")
	}
}
