// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// newPerModelWindowConfig builds the 2026-10-08 remote-incident shape: a
// trusted 1M agent-level window sized for a flagship entry, plus a speed
// entry that caps itself at 180k via per-model context_window.
func newPerModelWindowConfig(workspace, defaultModel string, trust bool) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                    workspace,
				Provider:                     "openai",
				ModelName:                    defaultModel,
				MaxTokens:                    16_384,
				ContextWindow:                1_000_000,
				TrustConfiguredContextWindow: trust,
			},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "flagship",
				Model:     "minimax/m3",
				APIBase:   "https://flagship.example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
			},
			{
				ModelName:     "speed",
				Model:         "minimax/m2-highspeed",
				APIBase:       "https://speed.example.invalid/v1",
				APIKeys:       config.SimpleSecureStrings("test-key"),
				ContextWindow: 180_000,
			},
		},
	}
}

// TestNewAgentInstance_PerModelContextWindow pins the min() resolution of
// the per-model context_window cap (fork feature): an entry can only lower
// the agent-level budget, never raise it, and both the gating window and the
// UI-declared window follow the active model.
func TestNewAgentInstance_PerModelContextWindow(t *testing.T) {
	cfg := newPerModelWindowConfig(t.TempDir(), "flagship", true)

	// Uncapped entry keeps the trusted global window verbatim.
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 1_000_000 || agent.DeclaredContextWindow != 1_000_000 {
		t.Fatalf("flagship windows = %d/%d, want 1000000/1000000",
			agent.ContextWindow, agent.DeclaredContextWindow)
	}

	// Capped entry: gating and declared both take min(1M, 180k).
	speedDefaults := cfg.Agents.Defaults
	speedDefaults.ModelName = "speed"
	agent = NewAgentInstance(nil, &speedDefaults, cfg, &mockProvider{})
	if agent.ContextWindow != 180_000 {
		t.Fatalf("speed ContextWindow = %d, want 180000", agent.ContextWindow)
	}
	if agent.DeclaredContextWindow != 180_000 {
		t.Fatalf("speed DeclaredContextWindow = %d, want 180000", agent.DeclaredContextWindow)
	}
	// CompactionBudget derives from the capped window, not the global one.
	if got := agent.CompactionBudget(); got != 180_000-16_384 {
		t.Fatalf("CompactionBudget() = %d, want %d", got, 180_000-16_384)
	}

	// A per-model value above the agent ceiling cannot raise it.
	bigDefaults := cfg.Agents.Defaults
	bigDefaults.ContextWindow = 131_072
	bigDefaults.TrustConfiguredContextWindow = true
	bigCfg := newPerModelWindowConfig(t.TempDir(), "speed", true)
	bigCfg.Agents.Defaults = bigDefaults
	agent = NewAgentInstance(nil, &bigDefaults, bigCfg, &mockProvider{})
	if agent.ContextWindow != 131_072 {
		t.Fatalf("uncapped-above-global ContextWindow = %d, want 131072 (min semantics)",
			agent.ContextWindow)
	}
}

// TestNewAgentInstance_PerModelContextWindowClampInterplay pins the
// ordering: the 256k sanity clamp applies to the agent-level window first,
// then the per-model cap folds in with min() — so a per-model value above
// the clamp cannot bypass it, and one below wins.
func TestNewAgentInstance_PerModelContextWindowClampInterplay(t *testing.T) {
	cfg := newPerModelWindowConfig(t.TempDir(), "speed", false)

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 180_000 {
		t.Fatalf("below-clamp per-model ContextWindow = %d, want 180000", agent.ContextWindow)
	}
	if agent.DeclaredContextWindow != 180_000 {
		t.Fatalf("below-clamp per-model DeclaredContextWindow = %d, want 180000", agent.DeclaredContextWindow)
	}

	// Per-model 300k against a clamped 256k gating window: gating stays at
	// the clamp, declared reports min(1M, 300k).
	over := newPerModelWindowConfig(t.TempDir(), "speed", false)
	over.ModelList[1].ContextWindow = 300_000
	agent = NewAgentInstance(nil, &over.Agents.Defaults, over, &mockProvider{})
	if agent.ContextWindow != 256_000 {
		t.Fatalf("above-clamp per-model ContextWindow = %d, want 256000 (clamp binds)", agent.ContextWindow)
	}
	if agent.DeclaredContextWindow != 300_000 {
		t.Fatalf("above-clamp per-model DeclaredContextWindow = %d, want 300000", agent.DeclaredContextWindow)
	}
}

// TestNewAgentInstance_PerModelContextWindowHeuristic pins that the cap
// also folds into the derived heuristic window (context_window unset →
// max(4x max_tokens, 256k)).
func TestNewAgentInstance_PerModelContextWindowHeuristic(t *testing.T) {
	cfg := newPerModelWindowConfig(t.TempDir(), "speed", true)
	cfg.Agents.Defaults.ContextWindow = 0
	cfg.Agents.Defaults.TrustConfiguredContextWindow = false

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 180_000 || agent.DeclaredContextWindow != 180_000 {
		t.Fatalf("heuristic+cap windows = %d/%d, want 180000/180000",
			agent.ContextWindow, agent.DeclaredContextWindow)
	}
}

// TestSwapAgentModelReCapsContextWindow pins that /switch (and the /new and
// restart-recovery paths that share swapAgentModelLocked) re-derives the
// effective window from the incoming model entry: the 2026-10-08 incident
// was exactly a switch from a 1M flagship to a 204k speed model that kept
// planning compaction against the old ceiling. CompactionBudget must follow
// without rebuilding the agent.
func TestSwapAgentModelReCapsContextWindow(t *testing.T) {
	cfg := newPerModelWindowConfig(t.TempDir(), "flagship", true)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	al := &AgentLoop{}

	if _, err := al.swapAgentModelLocked(cfg, agent, "speed"); err != nil {
		t.Fatalf("switch to speed: %v", err)
	}
	if agent.Model != "speed" {
		t.Fatalf("agent.Model = %q, want speed", agent.Model)
	}
	if agent.ContextWindow != 180_000 || agent.DeclaredContextWindow != 180_000 {
		t.Fatalf("post-switch windows = %d/%d, want 180000/180000",
			agent.ContextWindow, agent.DeclaredContextWindow)
	}
	if got := agent.CompactionBudget(); got != 180_000-16_384 {
		t.Fatalf("post-switch CompactionBudget() = %d, want %d", got, 180_000-16_384)
	}

	// Switching back restores the agent-level window.
	if _, err := al.swapAgentModelLocked(cfg, agent, "flagship"); err != nil {
		t.Fatalf("switch back to flagship: %v", err)
	}
	if agent.ContextWindow != 1_000_000 || agent.DeclaredContextWindow != 1_000_000 {
		t.Fatalf("restored windows = %d/%d, want 1000000/1000000",
			agent.ContextWindow, agent.DeclaredContextWindow)
	}
	if got := agent.CompactionBudget(); got != 1_000_000-16_384 {
		t.Fatalf("restored CompactionBudget() = %d, want %d", got, 1_000_000-16_384)
	}
}
