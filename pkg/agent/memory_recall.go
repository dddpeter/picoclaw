// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Defaults for memory recall. The timeout is deliberately short: recall runs
// synchronously during prompt build (turn start), so a slow memory backend
// must not delay every turn by more than a few seconds.
const (
	defaultMemoryRecallTool      = "search"
	defaultMemoryRecallMaxChars  = 2400
	defaultMemoryRecallTimeoutMs = 3000
)

func normalizeMemoryRecallConfig(cfg config.MemoryRecallConfig) config.MemoryRecallConfig {
	if strings.TrimSpace(cfg.Tool) == "" {
		cfg.Tool = defaultMemoryRecallTool
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = defaultMemoryRecallMaxChars
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaultMemoryRecallTimeoutMs
	}
	return cfg
}

// memoryRecallContributor injects shared-memory recall (e.g. OpenViking's
// semantic `search`) into the system prompt's memory slot at turn start,
// using the user's message as the query. Recall is strictly best effort:
// any failure (server down, timeout, empty result) skips injection so a
// memory backend outage can never break a turn.
type memoryRecallContributor struct {
	cfg    config.MemoryRecallConfig
	recall func(ctx context.Context, query string) (string, error)
}

func (c *memoryRecallContributor) PromptSource() PromptSourceDescriptor {
	return PromptSourceDescriptor{
		ID:          PromptSourceMemoryRecall,
		Owner:       "memory",
		Description: "Shared-memory recall (e.g. OpenViking)",
		Allowed:     []PromptPlacement{{Layer: PromptLayerContext, Slot: PromptSlotMemory}},
	}
}

func (c *memoryRecallContributor) ContributePrompt(ctx context.Context, req PromptBuildRequest) ([]PromptPart, error) {
	query := strings.TrimSpace(req.CurrentMessage)
	if query == "" || c.recall == nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.TimeoutMs)*time.Millisecond)
	defer cancel()

	text, err := c.recall(ctx, query)
	if err != nil {
		logger.DebugCF("agent", "Memory recall skipped", map[string]any{
			"server": c.cfg.Server,
			"error":  err.Error(),
		})
		return nil, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if runes := []rune(text); len(runes) > c.cfg.MaxChars {
		text = string(runes[:c.cfg.MaxChars]) + "\n…（recall truncated）"
	}

	return []PromptPart{
		{
			ID:     "context.memory_recall",
			Layer:  PromptLayerContext,
			Slot:   PromptSlotMemory,
			Source: PromptSource{ID: PromptSourceMemoryRecall, Name: "memory:recall:" + c.cfg.Server},
			Title:  "recalled memories",
			Content: "# Recalled memories (shared memory, approximate)\n\n" + text +
				"\n\n(Recalled via `" + c.cfg.Server + "`. Approximate references only — " +
				"always defer to explicit user instructions.)",
			Stable: false,
			Cache:  PromptCacheEphemeral,
		},
	}, nil
}

// registerMemoryRecallContributors wires the recall contributor onto every
// agent's ContextBuilder when memory.recall is enabled. The recall closure
// resolves the MCP manager lazily so deferred and late-connecting servers
// both work.
func registerMemoryRecallContributors(al *AgentLoop, cfg *config.Config, registry *AgentRegistry) {
	if cfg == nil || !cfg.Memory.Recall.Enabled {
		return
	}
	recallCfg := normalizeMemoryRecallConfig(cfg.Memory.Recall)
	if strings.TrimSpace(recallCfg.Server) == "" {
		logger.WarnCF("agent", "Memory recall enabled but no MCP server configured", nil)
		return
	}
	if _, ok := cfg.Tools.MCP.Servers[recallCfg.Server]; !ok {
		logger.WarnCF("agent", "Memory recall server not found in tools.mcp.servers", map[string]any{
			"server": recallCfg.Server,
		})
		return
	}

	recall := func(ctx context.Context, query string) (string, error) {
		if err := al.ensureMCPInitialized(ctx); err != nil {
			return "", fmt.Errorf("mcp init: %w", err)
		}
		manager := al.mcp.getManager()
		if manager == nil {
			return "", fmt.Errorf("mcp manager unavailable")
		}
		result, err := manager.CallTool(ctx, recallCfg.Server, recallCfg.Tool, map[string]any{"query": query})
		if err != nil {
			return "", err
		}
		if result == nil {
			return "", nil
		}
		if result.IsError {
			return "", fmt.Errorf("recall tool %q returned an error result", recallCfg.Tool)
		}
		parts := make([]string, 0, len(result.Content))
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok && strings.TrimSpace(text.Text) != "" {
				parts = append(parts, text.Text)
			}
		}
		return strings.Join(parts, "\n"), nil
	}

	registered := 0
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent.ContextBuilder == nil {
			continue
		}
		if err := agent.ContextBuilder.RegisterPromptContributor(&memoryRecallContributor{
			cfg:    recallCfg,
			recall: recall,
		}); err != nil {
			logger.WarnCF("agent", "Failed to register memory recall contributor", map[string]any{
				"agent_id": agentID,
				"error":    err.Error(),
			})
			continue
		}
		registered++
	}
	logger.InfoCF("agent", "Shared-memory recall enabled", map[string]any{
		"server": recallCfg.Server,
		"tool":   recallCfg.Tool,
		"agents": registered,
	})
}
