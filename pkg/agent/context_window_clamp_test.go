// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestNewAgentInstance_ContextWindowClamp pins the context-window sanity
// clamp (fork, 2026-10-04): a configured window far above the model's real
// window kept every compaction gate silent until the upstream started
// returning empty completions. Unless trust_configured_context_window is
// set, a configured window above the 256k floor is clamped — the unset path
// (floor + 4x rule) and the explicit-below-floor path are unchanged.
func TestNewAgentInstance_ContextWindowClamp(t *testing.T) {
	tmpDir := t.TempDir()

	// Inflated configured window: clamped to the floor.
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:     tmpDir,
				ModelName:     "test-model",
				ContextWindow: 1_000_000,
			},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 256_000 {
		t.Fatalf("ContextWindow = %d, want 256000 (clamp)", agent.ContextWindow)
	}

	// Opt-out flag restores the configured value verbatim.
	cfg.Agents.Defaults.TrustConfiguredContextWindow = true
	agent = NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 1_000_000 {
		t.Fatalf("ContextWindow = %d, want 1000000 (trust opt-out)", agent.ContextWindow)
	}

	// Below the floor: explicit configuration still wins, no clamp.
	cfg.Agents.Defaults.TrustConfiguredContextWindow = false
	cfg.Agents.Defaults.ContextWindow = 131_072
	agent = NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if agent.ContextWindow != 131_072 {
		t.Fatalf("ContextWindow = %d, want explicit 131072", agent.ContextWindow)
	}
}

// TestAgentInstance_CompactionBudget pins the compaction budget derivation:
// context window minus the output reserve, matching what the seahorse
// engine's history-only token count is compared against. Degenerate configs
// (max_tokens >= window) fall back to half the window rather than a
// non-positive budget.
func TestAgentInstance_CompactionBudget(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:     tmpDir,
				ModelName:     "test-model",
				ContextWindow: 256_000,
				MaxTokens:     32_768,
			},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if got := agent.CompactionBudget(); got != 256_000-32_768 {
		t.Fatalf("CompactionBudget() = %d, want %d", got, 256_000-32_768)
	}

	// Degenerate: max_tokens >= window must not produce a zero budget.
	cfg.Agents.Defaults.MaxTokens = 300_000
	agent = NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if got := agent.CompactionBudget(); got <= 0 {
		t.Fatalf("CompactionBudget() = %d, want > 0 for degenerate config", got)
	}
}
