package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestBuildFeishuPanelSkillAndMCPSteps(t *testing.T) {
	state := &feishuStreamState{
		Rounds: []feishuReasoningRound{
			{Text: "thinking about it", Duration: time.Second},
		},
		Tools: []bus.ToolStep{
			{Tool: "pdf-reader, web-lookup", Kind: bus.ToolStepKindSkill},
			{Tool: "web_search", Args: `{"q":"picoclaw"}`, Result: "3 results", Duration: time.Second},
			{Tool: "mcp_fetch_search", Args: `{"url":"https://example.com"}`, Result: "page text", Duration: 2 * time.Second, Kind: bus.ToolStepKindMCP},
		},
	}

	panel := buildFeishuPanel(state, true)
	data, err := json.Marshal(panel)
	if err != nil {
		t.Fatalf("marshal panel: %v", err)
	}
	rendered := string(data)

	if !strings.Contains(rendered, "已加载技能：pdf-reader, web-lookup") {
		t.Errorf("panel should render skill activation entry, got:\n%s", rendered)
	}
	// Underscores are markdown-escaped in titles (web\_search), so match the
	// prefix that survives escaping.
	if !strings.Contains(rendered, "MCP fetch") {
		t.Errorf("panel should render MCP-tagged tool step, got:\n%s", rendered)
	}

	// Header counts only executions: skill entry must not inflate it.
	title := panel["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "2 次工具") {
		t.Errorf("header %q should count 2 executions (excluding the skill entry)", title)
	}

	// Skill entry precedes reasoning rounds; executions follow them.
	skillIdx := strings.Index(rendered, "已加载技能")
	reasoningIdx := strings.Index(rendered, "第 1 轮推理")
	mcpIdx := strings.Index(rendered, "MCP fetch")
	if skillIdx == -1 || reasoningIdx == -1 || mcpIdx == -1 {
		t.Fatalf("missing expected sections in panel:\n%s", rendered)
	}
	if !(skillIdx < reasoningIdx && reasoningIdx < mcpIdx) {
		t.Errorf("expected order skill < reasoning < executions, got skill=%d reasoning=%d mcp=%d",
			skillIdx, reasoningIdx, mcpIdx)
	}
}

func TestBuildFeishuPanelPlainToolUnchanged(t *testing.T) {
	state := &feishuStreamState{
		Tools: []bus.ToolStep{
			{Tool: "web_search", Args: `{"q":"x"}`, Result: "ok", Duration: time.Second},
		},
	}
	panel := buildFeishuPanel(state, true)
	data, _ := json.Marshal(panel)
	rendered := string(data)

	if strings.Contains(rendered, "MCP") || strings.Contains(rendered, "已加载技能") {
		t.Errorf("plain tool steps should render without MCP/skill labels:\n%s", rendered)
	}
	title := panel["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "1 次工具") {
		t.Errorf("plain tool step missing from panel (header %q):\n%s", title, rendered)
	}
}
