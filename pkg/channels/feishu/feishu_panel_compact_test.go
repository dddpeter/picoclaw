package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestFeishuReasoningRoundsRenderAsCollapsedNestedPanels(t *testing.T) {
	state := &feishuStreamState{
		Rounds: []feishuReasoningRound{
			{Text: "round one thinking", Duration: 2 * time.Second},
			{Text: "round two thinking", Duration: 3 * time.Second},
		},
	}
	panel := buildFeishuPanel(state, true)
	children := panel["elements"].([]any)

	for i, want := range []string{"第 1 轮推理", "第 2 轮推理"} {
		nested, ok := children[i].(map[string]any)
		if !ok || nested["tag"] != "collapsible_panel" {
			t.Fatalf("round %d should be a nested collapsible_panel, got %#v", i+1, children[i])
		}
		if nested["expanded"] != false {
			t.Errorf("round %d panel should be collapsed by default", i+1)
		}
		title := nested["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
		if !strings.Contains(title, want) {
			t.Errorf("round %d header %q should contain %q", i+1, title, want)
		}
		inner := nested["elements"].([]any)
		md, _ := inner[0].(map[string]any)["content"].(string)
		if !strings.Contains(md, fmt.Sprintf("round %s", map[int]string{1: "one", 2: "two"}[i+1])) {
			t.Errorf("round %d body should keep the thinking text, got %q", i+1, md)
		}
	}
}

func TestFeishuReasoningRoundPanelEmptyText(t *testing.T) {
	p := feishuReasoningRoundPanel(3, feishuReasoningRound{})
	inner := p["elements"].([]any)
	if len(inner) != 1 {
		t.Fatalf("empty round should render one placeholder, got %d", len(inner))
	}
}

func TestFeishuToolStepCompactShortResult(t *testing.T) {
	step := bus.ToolStep{Tool: "web_search", Args: `{"q":"picoclaw"}`, Result: "3 results", Duration: time.Second}
	elements := feishuToolStepElements(step)
	if len(elements) != 2 {
		t.Fatalf("expected title + one compact line (2 elements), got %d", len(elements))
	}
	data, _ := json.Marshal(elements)
	rendered := string(data)
	// Args contain double quotes, which json.Marshal escapes; assert the
	// pieces and the join arrow instead of the exact literal.
	if !strings.Contains(rendered, "picoclaw") || !strings.Contains(rendered, "3 results") || !strings.Contains(rendered, "→") {
		t.Errorf("compact line should join args and result, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "结果") {
		t.Errorf("compact form must not carry the 结果 label:\n%s", rendered)
	}
}

func TestFeishuToolStepCompactWithoutArgs(t *testing.T) {
	step := bus.ToolStep{Tool: "clock", Result: "12:00"}
	elements := feishuToolStepElements(step)
	if len(elements) != 2 {
		t.Fatalf("expected title + one compact line, got %d", len(elements))
	}
	data, _ := json.Marshal(elements)
	if !strings.Contains(string(data), "12:00") {
		t.Errorf("compact line should carry the result, got:\n%s", string(data))
	}
}

func TestFeishuToolStepBlockKeptForLongResult(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	step := bus.ToolStep{Tool: "shell", Args: `{"cmd":"ls"}`, Result: strings.Join(lines, "\n")}
	elements := feishuToolStepElements(step)
	if len(elements) != 3 {
		t.Fatalf("expected title + args + result block, got %d", len(elements))
	}
	data, _ := json.Marshal(elements)
	rendered := string(data)
	if !strings.Contains(rendered, "结果") {
		t.Errorf("long result should keep the labeled block:\n%s", rendered)
	}
	if strings.Contains(rendered, "line-9") {
		t.Errorf("10-line result should be trimmed to 6 lines:\n%s", rendered)
	}
	if !strings.Contains(rendered, "line-5") {
		t.Errorf("first 6 lines should survive trimming:\n%s", rendered)
	}
}

func TestFeishuPanelElementBudgetUnderCaps(t *testing.T) {
	state := &feishuStreamState{}
	for i := 0; i < feishuMaxReasoningRounds; i++ {
		state.Rounds = append(state.Rounds, feishuReasoningRound{Text: "thinking"})
	}
	for i := 0; i < feishuMaxToolSteps; i++ {
		state.Tools = append(state.Tools, bus.ToolStep{Tool: fmt.Sprintf("tool%d", i), Result: "ok"})
	}
	card := buildFeishuFinalCard(state, "answer", false, time.Second, "")
	if got := countFeishuTagObjects(card); got > feishuElementLimit-feishuElementLimitMargin {
		t.Fatalf("maxed panel card uses %d tag objects, threshold is %d", got, feishuElementLimit-feishuElementLimitMargin)
	}
}
