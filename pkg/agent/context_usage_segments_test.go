// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// segTestTool is a minimal registry tool whose name drives segment
// classification.
type segTestTool struct{ name, desc string }

func (m *segTestTool) Name() string        { return m.name }
func (m *segTestTool) Description() string { return m.desc }
func (m *segTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (m *segTestTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return tools.SilentResult("ok")
}

// TestComputeContextUsage_Segments pins the segment breakdown (octop
// borrowing §五): used tokens split into system_prompt / skills /
// tool_definitions / mcp / subagents / conversation, with conversation equal
// to HistoryTokens and the segments summing to UsedTokens. MCP bloat and
// subagent definitions become visible at a glance.
func TestComputeContextUsage_Segments(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:     t.TempDir(),
				ModelName:     "test-model",
				ContextWindow: 100_000,
			},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})

	agent.Tools.Register(&segTestTool{name: "read_file", desc: "reads a file from the workspace"})
	agent.Tools.Register(&segTestTool{name: "mcp_github_create_issue", desc: "creates a GitHub issue"})
	agent.Tools.Register(&segTestTool{name: "spawn", desc: "spawns a subagent"})

	for i := range 4 {
		agent.Sessions.AddFullMessage("s-seg", providers.Message{
			Role:    "user",
			Content: strings.Repeat("上下文内容", 40) + string(rune('a'+i)),
		})
	}
	if err := agent.Sessions.Save("s-seg"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	usage := computeContextUsage(agent, "s-seg")
	if usage == nil {
		t.Fatal("computeContextUsage() = nil")
	}

	segs := map[string]int{}
	for _, s := range usage.Segments {
		segs[s.Key] = s.Tokens
	}
	for _, key := range []string{"system_prompt", "skills", "tool_definitions", "mcp", "subagents", "conversation"} {
		if _, ok := segs[key]; !ok {
			t.Fatalf("segment %q missing from %+v", key, usage.Segments)
		}
	}
	if segs["conversation"] != usage.HistoryTokens {
		t.Fatalf("conversation segment = %d, want HistoryTokens %d", segs["conversation"], usage.HistoryTokens)
	}
	if segs["system_prompt"] <= 0 {
		t.Fatalf("system_prompt segment should be positive, got %d", segs["system_prompt"])
	}
	if segs["tool_definitions"] <= 0 {
		t.Fatalf("tool_definitions segment should be positive, got %d", segs["tool_definitions"])
	}
	if segs["mcp"] <= 0 {
		t.Fatalf("mcp_ tool must land in the mcp segment, got %d", segs["mcp"])
	}
	if segs["subagents"] <= 0 {
		t.Fatalf("spawn tool must land in the subagents segment, got %d", segs["subagents"])
	}

	sum := 0
	for _, v := range segs {
		sum += v
	}
	if sum != usage.UsedTokens {
		t.Fatalf("segments sum %d != UsedTokens %d (breakdown must partition the total)", sum, usage.UsedTokens)
	}

	// The builtin read_file lands only in tool_definitions: mcp + subagents
	// segments stay smaller than the combined tool span when more builtin
	// tools exist (here: read_file plus whatever NewAgentInstance seeds).
	if segs["tool_definitions"] <= segs["mcp"] {
		t.Fatalf("tool_definitions (%d) should dominate the mcp segment (%d) here",
			segs["tool_definitions"], segs["mcp"])
	}
}
