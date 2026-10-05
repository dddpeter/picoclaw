// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestComputeContextUsage_TotalTokensDeclared pins the display/report split
// (fork, 2026-10-04): bus.ContextUsage.TotalTokens — the figure every UI
// renders as the model's context size (Feishu streaming card, pico/web
// usage, /context, /status) — must carry the DECLARED window (configured
// value before the sanity clamp), while the gate-side fields
// (CompressAtTokens/UsedPercent) stay bound to the clamped ContextWindow
// the compaction gates actually run on. Hand-built agents without the
// declared field fall back to ContextWindow.
func TestComputeContextUsage_TotalTokensDeclared(t *testing.T) {
	// NewAgentInstance path: configured 1M, clamped to 256k.
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:     t.TempDir(),
				ModelName:     "test-model",
				ContextWindow: 1_000_000,
			},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})

	usage := computeContextUsage(agent, "session-display")
	if usage == nil {
		t.Fatal("computeContextUsage() = nil")
	}
	if usage.TotalTokens != 1_000_000 {
		t.Fatalf("TotalTokens = %d, want 1000000 (declared, not clamped %d)",
			usage.TotalTokens, agent.ContextWindow)
	}
	if agent.ContextWindow != 256_000 {
		t.Fatalf("ContextWindow = %d, want 256000 (gates stay clamped)", agent.ContextWindow)
	}
	// CompressAt stays gate-relative: clamped window minus output reserve.
	if usage.CompressAtTokens != 256_000-agent.MaxTokens {
		t.Fatalf("CompressAtTokens = %d, want %d (gate-relative)",
			usage.CompressAtTokens, 256_000-agent.MaxTokens)
	}

	// Hand-built agent (no declared field): falls back to ContextWindow.
	handBuilt := &AgentInstance{
		ContextWindow: 100_000,
		MaxTokens:     8_192,
		Sessions:      agent.Sessions,
	}
	usage = computeContextUsage(handBuilt, "session-display")
	if usage == nil {
		t.Fatal("computeContextUsage() = nil for hand-built agent")
	}
	if usage.TotalTokens != 100_000 {
		t.Fatalf("TotalTokens = %d, want 100000 (ContextWindow fallback)", usage.TotalTokens)
	}
}
