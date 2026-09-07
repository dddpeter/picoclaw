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
			{Text: "thinking about it", Duration: time.Second, Seq: 2},
		},
		Tools: []bus.ToolStep{
			{Tool: "pdf-reader, web-lookup", Kind: bus.ToolStepKindSkill},
			{Tool: "web_search", Args: `{"q":"picoclaw"}`, Result: "3 results", Duration: time.Second},
			{Tool: "mcp_fetch_search", Args: `{"url":"https://example.com"}`, Result: "page text", Duration: 2 * time.Second, Kind: bus.ToolStepKindMCP},
		},
		// Real chronology: skills seed first, then reasoning, then tools.
		ToolSeqs: []int{1, 3, 4},
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

func TestMidTurnTextStepsRenderWithoutToolCount(t *testing.T) {
	state := &feishuStreamState{
		Tools: []bus.ToolStep{
			{Kind: bus.ToolStepKindText, Result: "我先检查一下配置文件的默认模型设置。"},
			{Tool: "exec", Args: "cat config.json", Result: "…", Duration: 12},
			{Kind: bus.ToolStepKindText, Result: "配置没有问题，接下来修改代码。"},
			{Tool: "edit_file", Args: "x.go", Result: "ok", Duration: 5},
		},
	}
	panel := buildFeishuPanelBudget(state, true, feishuPanelTextBudget)
	children := panel["elements"].([]any)

	var labels, bodies, execTitles int
	for _, el := range children {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if m["tag"] == "markdown" {
			if c, _ := m["content"].(string); strings.Contains(c, "本轮说明") {
				labels++
			}
		}
		if m["tag"] == "div" {
			if txt, ok := m["text"].(map[string]any); ok {
				c, _ := txt["content"].(string)
				if c == "我先检查一下配置文件的默认模型设置。" || c == "配置没有问题，接下来修改代码。" {
					bodies++
				}
				// Short tool results render as compact div lines carrying
				// the args preview.
				if strings.Contains(c, "cat config.json") || strings.Contains(c, "x.go") {
					execTitles++
				}
			}
		}
	}
	if labels != 2 || bodies != 2 {
		t.Fatalf("mid-turn texts should render as 2 labels + 2 bodies, got %d/%d in %d children", labels, bodies, len(children))
	}
	if execTitles != 2 {
		t.Fatalf("tool steps should still render, got %d", execTitles)
	}

	// The header must not count text archives as tool executions: 2 real
	// tools, not 4 steps.
	header := panel["header"].(map[string]any)
	for _, v := range headerChildrenTexts(header) {
		if strings.Contains(v, "4 次工具") {
			t.Fatalf("text archives leaked into the tool count: %q", v)
		}
		if strings.Contains(v, "2 次工具") {
			return // pass
		}
	}
	t.Fatalf("header should report 2 tool executions, got %+v", header)
}

func headerChildrenTexts(header map[string]any) []string {
	var out []string
	title, ok := header["title"].(map[string]any)
	if !ok {
		return out
	}
	if content, ok := title["content"].(string); ok {
		out = append(out, content)
	}
	elements, ok := header["elements"].([]any)
	if !ok {
		return out
	}
	for _, el := range elements {
		if m, ok := el.(map[string]any); ok {
			if c, ok := m["content"].(string); ok {
				out = append(out, c)
			}
		}
	}
	return out
}
