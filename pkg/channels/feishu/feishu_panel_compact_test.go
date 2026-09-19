package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// TestFeishuReasoningRoundsRenderFlat locks in the hermes-aligned flat
// rendering of finalized reasoning rounds: title + indented thinking text
// directly in the panel body — expanding the outer panel is enough to read
// every round, no nested per-round collapsible panels.
func TestFeishuReasoningRoundsRenderFlat(t *testing.T) {
	state := &feishuStreamState{
		Rounds: []feishuReasoningRound{
			{Text: "round one thinking", Duration: 2 * time.Second},
			{Text: "round two thinking", Duration: 3 * time.Second},
		},
	}
	panel := buildFeishuPanel(state, true)
	children := panel["elements"].([]any)

	if len(children) != 4 {
		t.Fatalf("two rounds should render title+text each (4 children), got %d", len(children))
	}
	for i, want := range []string{"第 1 轮推理", "第 2 轮推理"} {
		title, ok := children[i*2].(map[string]any)
		if !ok || title["tag"] != "div" {
			t.Fatalf("round %d should start with a title div, got %#v", i+1, children[i*2])
		}
		content, _ := title["text"].(map[string]any)["content"].(string)
		if !strings.Contains(content, want) || !strings.Contains(content, "✓") {
			t.Errorf("round %d title %q should contain %q and the finalized ✓", i+1, content, want)
		}
		body, ok := children[i*2+1].(map[string]any)
		if !ok || body["tag"] != "div" {
			t.Fatalf("round %d thinking text should render as an indented div, got %#v", i+1, children[i*2+1])
		}
		md := body["text"].(map[string]any)
		if c, _ := md["content"].(string); !strings.Contains(c, fmt.Sprintf("round %s", map[int]string{1: "one", 2: "two"}[i+1])) {
			t.Errorf("round %d body should keep the thinking text, got %q", i+1, c)
		}
	}
}

func TestFeishuReasoningRoundEmptyTextTitleOnly(t *testing.T) {
	state := &feishuStreamState{Rounds: []feishuReasoningRound{{}}}
	children := buildFeishuPanel(state, true)["elements"].([]any)
	if len(children) != 1 {
		t.Fatalf("empty round should render title only, got %d children", len(children))
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

func TestTruncateFeishuCodeResultLongSingleLine(t *testing.T) {
	// A single long line (no newlines) longer than 600 bytes used to hit a
	// slice-bounds panic: the guard checked >600 but sliced to [:1200].
	long := strings.Repeat("a", 700)
	got := truncateFeishuCodeResult(long)
	if len(got) > 700 || !strings.HasSuffix(got, "\n…") {
		t.Fatalf("unexpected truncation result: %q", got)
	}

	// Chinese content must stay valid UTF-8 after the byte cap.
	chinese := strings.Repeat("字", 300) // 900 bytes
	got = truncateFeishuCodeResult(chinese)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated result is not valid UTF-8: %q", got)
	}
	if len(got) > 601+len("\n…") {
		t.Fatalf("result not capped near 600 bytes: %d", len(got))
	}
}

func TestFeishuCardSummaryRuneSafe(t *testing.T) {
	// First line crossing the 120-byte boundary mid-rune used to produce
	// invalid UTF-8 (rendered as U+FFFD on the card).
	summary := feishuCardSummary(strings.Repeat("字", 50))
	content, _ := summary["content"].(string)
	if !utf8.ValidString(content) {
		t.Fatalf("summary content is not valid UTF-8: %q", content)
	}
}

// TestFeishuPanelElementBudgetUnderCaps locks in the max-caps behavior:
// 20 rounds + 20 tools cost ~5 tag objects each (~207 with scaffolding),
// which exceeds the 195 threshold, so the card-level element-limit safety
// net trims the oldest rounds and inserts a fold hint. The sealed card must
// land under the threshold with the trim honestly reflected in the panel.
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
	panel := card["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)
	children := panel["elements"].([]any)
	hint, _ := children[0].(map[string]any)["content"].(string)
	if !strings.Contains(hint, "已折叠") {
		t.Errorf("maxed panel should open with a fold hint, got %q", hint)
	}
	visibleRounds := 0
	for _, c := range children {
		if m, ok := c.(map[string]any); ok {
			if txt, ok := m["text"].(map[string]any); ok {
				if s, _ := txt["content"].(string); strings.Contains(s, "轮推理") {
					visibleRounds++
				}
			}
		}
	}
	if visibleRounds == feishuMaxReasoningRounds {
		t.Errorf("safety net should trim some rounds at max caps, but all %d are visible", visibleRounds)
	}
}
