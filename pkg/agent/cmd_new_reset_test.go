package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
