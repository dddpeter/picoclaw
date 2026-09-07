package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestBuildFeishuStreamingCardStructure(t *testing.T) {
	card := buildFeishuStreamingCard("chat-t")
	if card["schema"] != "2.0" {
		t.Fatalf("schema = %v, want 2.0", card["schema"])
	}
	cfg := card["config"].(map[string]any)
	if cfg["streaming_mode"] != true {
		t.Fatal("streaming_mode should be true")
	}
	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 4 {
		t.Fatalf("initial card should have panel/answer/loading/stop elements, got %d", len(elements))
	}
	ids := map[string]bool{}
	for _, e := range elements {
		if m, ok := e.(map[string]any); ok {
			if id, ok := m["element_id"].(string); ok {
				ids[id] = true
			}
		}
	}
	for _, want := range []string{feishuAnswerElementID, feishuPanelElementID, feishuLoadingElementID} {
		if !ids[want] {
			t.Errorf("initial card missing element %s", want)
		}
	}
}

func TestCountFeishuTagObjects(t *testing.T) {
	card := map[string]any{
		"body": map[string]any{
			"elements": []any{
				map[string]any{
					"tag":  "div",
					"icon": map[string]any{"tag": "standard_icon"},
					"text": map[string]any{"tag": "lark_md", "content": "hi"},
				},
				map[string]any{"tag": "hr"},
			},
		},
	}
	if got := countFeishuTagObjects(card); got != 4 {
		t.Errorf("count = %d, want 4 (div + icon + lark_md + hr)", got)
	}
	if got := countFeishuTagObjects("not an object"); got != 0 {
		t.Errorf("count on scalar = %d, want 0", got)
	}
}

func TestBuildFeishuFinalCardPanelAndFooter(t *testing.T) {
	state := &feishuStreamState{
		Rounds: []feishuReasoningRound{
			{Text: "round one thinking", Duration: 2 * time.Second},
			{Text: "round two thinking", Duration: 3 * time.Second},
		},
		Tools: []bus.ToolStep{
			{Tool: "web_search", Args: `{"q":"picoclaw"}`, Result: "found 3 results", Duration: time.Second},
			{Tool: "shell", Args: `{"cmd":"ls"}`, Result: "boom", IsError: true, Duration: 2 * time.Second},
		},
		ModelName:    "glm-4.7",
		InputTokens:  100,
		OutputTokens: 42,
	}
	card := buildFeishuFinalCard(state, "final answer", false, 12*time.Second, "")

	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) != 4 { // panel + answer + hr + footer markdown
		t.Fatalf("expected panel+answer+hr+footer (4 body items), got %d", len(elements))
	}
	panel := elements[0].(map[string]any)
	if panel["tag"] != "collapsible_panel" || panel["expanded"] != false {
		t.Errorf("final panel should be collapsed collapsible_panel, got %v", panel["tag"])
	}
	if got := countFeishuTagObjects(card); got > feishuElementLimit-feishuElementLimitMargin {
		t.Errorf("final card element count %d exceeds safety threshold", got)
	}

	// Panel title reflects totals.
	title := panel["header"].(map[string]any)["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "2 轮推理") || !strings.Contains(title, "2 次工具") {
		t.Errorf("panel title %q should mention rounds and tools", title)
	}

	// Footer contains status, model and tokens.
	var footer strings.Builder
	for _, e := range elements {
		if m, ok := e.(map[string]any); ok && m["tag"] == "markdown" {
			if c, ok := m["content"].(string); ok && strings.Contains(c, "glm-4.7") {
				footer.WriteString(c)
			}
		}
	}
	if !strings.Contains(footer.String(), "↑ 100 ↓ 42") || !strings.Contains(footer.String(), "已完成") {
		t.Errorf("footer %q missing model/tokens/status", footer.String())
	}

	// Sealed card disables streaming mode.
	if card["config"].(map[string]any)["streaming_mode"] != false {
		t.Error("final card should have streaming_mode=false")
	}
}

func TestBuildFeishuFinalCardEmpty(t *testing.T) {
	state := &feishuStreamState{}
	card := buildFeishuFinalCard(state, "", false, time.Second, "")
	elements := card["body"].(map[string]any)["elements"].([]any)
	if len(elements) == 0 {
		t.Fatal("empty final card should still render something")
	}
	if first := elements[0].(map[string]any)["tag"]; first == "collapsible_panel" {
		t.Error("empty state should not render a panel")
	}
}

func TestBuildFeishuPanelDisplayCapsAndHint(t *testing.T) {
	state := &feishuStreamState{}
	for i := 0; i < feishuMaxReasoningRounds+7; i++ {
		state.Rounds = append(state.Rounds, feishuReasoningRound{Text: fmt.Sprintf("round %d", i)})
	}
	for i := 0; i < feishuMaxToolSteps+3; i++ {
		state.Tools = append(state.Tools, bus.ToolStep{Tool: fmt.Sprintf("tool%d", i), Result: "ok"})
	}
	panel := buildFeishuPanel(state, true)
	children := panel["elements"].([]any)
	if len(children) == 0 || len(children) > 200 {
		t.Fatalf("panel children out of range: %d", len(children))
	}
	hint, _ := children[0].(map[string]any)["content"].(string)
	if !strings.Contains(hint, "7 轮早期推理") || !strings.Contains(hint, "3 步早期操作") {
		t.Errorf("collapse hint %q should mention trimmed rounds and tools", hint)
	}
}

func TestEnforceFeishuElementLimitTrims(t *testing.T) {
	// Build a card whose panel exceeds the cap by padding many heavy children,
	// bypassing the per-kind caps that normally prevent this.
	state := &feishuStreamState{}
	for i := 0; i < 90; i++ {
		state.Tools = append(state.Tools, bus.ToolStep{
			Tool:   fmt.Sprintf("tool_%02d", i),
			Args:   "args",
			Result: "result text",
		})
	}
	card := map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []any{buildFeishuPanelRaw(state)},
		},
	}
	if countFeishuTagObjects(card) <= feishuElementLimit-feishuElementLimitMargin {
		t.Skip("construction did not exceed the threshold")
	}
	enforceFeishuElementLimit(card)
	if got := countFeishuTagObjects(card); got > feishuElementLimit-feishuElementLimitMargin {
		t.Errorf("element count after trim = %d, still over threshold", got)
	}
	panel := card["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)
	children := panel["elements"].([]any)
	if hint, _ := children[0].(map[string]any)["content"].(string); !strings.Contains(hint, "已折叠") {
		t.Errorf("first child should be collapse hint, got %v", children[0])
	}
}

// buildFeishuPanelRaw builds a panel without applying display caps, for
// exercising the card-level safety net directly.
func buildFeishuPanelRaw(state *feishuStreamState) map[string]any {
	children := []any{}
	for _, step := range state.Tools {
		children = append(children, feishuToolStepElements(step)...)
	}
	return map[string]any{
		"tag":        "collapsible_panel",
		"expanded":   false,
		"header":     feishuPanelHeader(0, false, len(state.Tools), 0),
		"elements":   children,
		"element_id": feishuPanelElementID,
	}
}

func TestFeishuInlineCodeEscaping(t *testing.T) {
	// A line containing backticks needs a longer delimiter run.
	got := feishuInlineCodeLine("has `tick` inside")
	if !strings.HasPrefix(got, "``has") || !strings.HasSuffix(got, "inside``") {
		t.Errorf("inline code line = %q, want double-backtick delimiters", got)
	}
	// Lines starting/ending with the delimiter char are padded so the
	// delimiter stays distinct from content.
	got = feishuInlineCodeLine("`lead")
	if !strings.HasPrefix(got, "`` `") || !strings.HasSuffix(got, "d ``") {
		t.Errorf("padded line = %q", got)
	}
	// Multi-line output: every non-empty line wrapped, blanks preserved.
	if block := feishuInlineCodeBlock("a\n\nb"); block != "`a`\n\n`b`" {
		t.Errorf("inline code block = %q", block)
	}
}

func TestToolStepOutputUsesInlineCodeNotFences(t *testing.T) {
	elements := feishuToolStepElements(bus.ToolStep{Tool: "t", Result: "line1\nline2"})
	data, _ := json.Marshal(elements)
	if strings.Contains(string(data), "```") {
		t.Errorf("tool output must not use fenced code blocks: %s", string(data))
	}
	if !strings.Contains(string(data), "`line1`") {
		t.Errorf("tool output should wrap lines in inline code: %s", string(data))
	}
}

func TestTruncateFeishuReasoning(t *testing.T) {
	long := strings.Repeat("字", feishuReasoningDisplayLimit+500)
	got := truncateFeishuReasoning(long)
	if len(got) > feishuReasoningDisplayLimit {
		t.Errorf("truncated length %d exceeds limit", len(got))
	}
	if !strings.Contains(got, "已截断") {
		t.Error("truncated text should carry a truncation suffix")
	}
}

func TestFeishuCardSummary(t *testing.T) {
	s := feishuCardSummary("第一行\n第二行 ```code```")
	if s["content"] != "第一行" {
		t.Errorf("summary = %v, want first line only", s["content"])
	}
	if feishuCardSummary("")["content"] != "已完成" {
		t.Error("empty summary should fall back")
	}
}

func TestFormatFeishuElapsed(t *testing.T) {
	cases := map[time.Duration]string{
		500 * time.Millisecond:  "500ms",
		2500 * time.Millisecond: "2.5s",
		90 * time.Second:        "1m30s",
	}
	for d, want := range cases {
		if got := formatFeishuElapsed(d); got != want {
			t.Errorf("formatFeishuElapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestApplyPanelTextBudgetTrimsOldestFirst(t *testing.T) {
	rounds := make([]feishuReasoningRound, 5)
	for i := range rounds {
		rounds[i].Text = strings.Repeat("推", 5000) // 15KB each in bytes
	}
	tools := []bus.ToolStep{{Tool: "t", Result: strings.Repeat("结", 200)}}
	orig := rounds[0].Text

	newRounds, newTools := applyPanelTextBudget(rounds, tools, feishuPanelTextBudget)
	if len(newRounds) != 5 || newTools[0].Tool != "t" {
		t.Fatal("budget must not drop items, only trim text")
	}
	if newRounds[0].Text == orig || !strings.Contains(newRounds[0].Text, "省略") {
		t.Error("oldest round should be trimmed to a stub first")
	}
	if len(newRounds[4].Text) != len(orig) {
		t.Errorf("newest round should be preserved (len %d, want %d)", len(newRounds[4].Text), len(orig))
	}
	total := 0
	for _, r := range newRounds {
		total += len(r.Text)
	}
	if total > feishuPanelTextBudget+1000 {
		t.Errorf("trimmed panel text %d bytes still over budget", total)
	}
	// Original slices untouched.
	if rounds[0].Text != orig {
		t.Error("applyPanelTextBudget must not mutate caller slices")
	}
}

func TestFinalCardStaysUnderSizeLimit(t *testing.T) {
	state := &feishuStreamState{}
	for i := 0; i < feishuMaxReasoningRounds; i++ {
		state.Rounds = append(state.Rounds, feishuReasoningRound{Text: strings.Repeat("思", feishuReasoningDisplayLimit)})
	}
	for i := 0; i < feishuMaxToolSteps; i++ {
		state.Tools = append(state.Tools, bus.ToolStep{Tool: "tool", Args: strings.Repeat("参", 200), Result: strings.Repeat("果", 400)})
	}
	card := buildFeishuFinalCard(state, strings.Repeat("答", 4000), false, time.Second, "")
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 30000 {
		t.Errorf("worst-case final card is %d bytes, exceeds Feishu 30KB limit", len(data))
	}
}

func TestFinalCardShowsCancelReason(t *testing.T) {
	state := &feishuStreamState{}
	card := buildFeishuFinalCard(state, "", true, time.Second, "stop_command")
	data, _ := json.Marshal(card)
	if !strings.Contains(string(data), "用户停止") {
		t.Errorf("sealed card should show stop reason, got: %.200s", string(data))
	}

	card = buildFeishuFinalCard(state, "", true, time.Second, "custom_code_xyz")
	data, _ = json.Marshal(card)
	if !strings.Contains(string(data), "custom_code_xyz") {
		t.Error("unknown reason codes should render verbatim")
	}
}

func TestPanelShowsSteeringNotice(t *testing.T) {
	state := &feishuStreamState{
		SteeringCount: 2,
		SteeringLast:  "顺便也查一下价格",
	}
	if !state.hasPanelContent() {
		t.Fatal("steering notices alone should count as panel content")
	}
	panel := buildFeishuPanel(state, true)
	data, _ := json.Marshal(panel["elements"])
	if !strings.Contains(string(data), "收到 2 条追加指令") || !strings.Contains(string(data), "顺便也查一下价格") {
		t.Errorf("panel missing steering notice: %.300s", string(data))
	}

	// Long previews are truncated.
	state.SteeringLast = strings.Repeat("长", 100)
	panel = buildFeishuPanel(state, true)
	data, _ = json.Marshal(panel["elements"])
	if !strings.Contains(string(data), "…") {
		t.Error("long steering preview should be truncated")
	}
}
