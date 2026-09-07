package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// feishuLoadingTextAnswer mirrors feishuLoadingText(feishuPhaseAnswer) for
// assertions without exporting the tuple.
const feishuLoadingTextAnswer = "✍ 正在生成回答…"

func TestFeishuLoadingText(t *testing.T) {
	cases := map[string]struct{ zh, en string }{
		feishuPhaseLoading:  {"正在加载上下文…", "Loading context…"},
		feishuPhaseThinking: {"🧠 正在思考…", "Thinking…"},
		feishuPhaseAnswer:   {"✍ 正在生成回答…", "Generating answer…"},
	}
	for phase, want := range cases {
		zh, en, _ := feishuLoadingText(phase)
		if zh != want.zh || en != want.en {
			t.Errorf("phase %q = (%q, %q), want (%q, %q)", phase, zh, en, want.zh, want.en)
		}
	}
}

func TestBuildFeishuLoadingElementPhases(t *testing.T) {
	for _, phase := range []string{feishuPhaseLoading, feishuPhaseThinking, feishuPhaseAnswer} {
		el := buildFeishuLoadingElement(phase, "")
		if el["element_id"] != feishuLoadingElementID {
			t.Errorf("phase %q: wrong element id %v", phase, el["element_id"])
		}
		zh, _, wantColor := feishuLoadingText(phase)
		text := el["text"].(map[string]any)
		if text["content"] != zh {
			t.Errorf("phase %q: content %q != %q", phase, text["content"], zh)
		}
		if text["text_size"] != "notation" {
			t.Errorf("phase %q: status line should use the small notation size, got %v", phase, text["text_size"])
		}
		if text["text_color"] != wantColor {
			t.Errorf("phase %q: text_color %v != %q", phase, text["text_color"], wantColor)
		}
	}
	// In-flight phases must carry the amber tone; the pre-activity phase
	// stays grey.
	for _, phase := range []string{feishuPhaseThinking, feishuPhaseAnswer} {
		if _, _, color := feishuLoadingText(phase); color != feishuAmberColor {
			t.Errorf("phase %q should use the amber status color, got %q", phase, color)
		}
	}
	if _, _, color := feishuLoadingText(feishuPhaseLoading); color != "grey" {
		t.Errorf("loading phase should stay grey, got %q", color)
	}
}

func TestFeishuPanelExpanded(t *testing.T) {
	if !feishuPanelExpanded("") {
		t.Error("panel should stay expanded before the answer starts")
	}
	if !feishuPanelExpanded("   ") {
		t.Error("whitespace-only answer should not collapse the panel")
	}
	if feishuPanelExpanded("partial answer") {
		t.Error("panel should collapse once answer text exists")
	}
}

// TestFeishuRefreshCardKeepsStreamingConfig locks in the fix for the
// "content replaced instead of fully displayed" regression: a mid-stream
// full-card refresh must carry streaming_mode (dropping it kills the answer
// element's typewriter), collapse the panel once the answer exists, and
// render the phase status line.
func TestFeishuRefreshCardKeepsStreamingConfig(t *testing.T) {
	state := &feishuStreamState{
		Rounds: []feishuReasoningRound{{Text: "thinking"}},
		Tools:  []bus.ToolStep{{Tool: "shell", Result: "ok"}},
	}
	card := buildFeishuRefreshCard(state, "partial answer", feishuPhaseAnswer, feishuPanelTextBudget, "")

	cfg, ok := card["config"].(map[string]any)
	if !ok || cfg["streaming_mode"] != true {
		t.Fatalf("refresh card must keep streaming_mode=true, got %#v", card["config"])
	}
	data, _ := json.Marshal(card)
	rendered := string(data)
	if !strings.Contains(rendered, "partial answer") {
		t.Errorf("refresh card should carry the current answer snapshot:\n%s", rendered)
	}
	if !strings.Contains(rendered, feishuLoadingTextAnswer) {
		t.Errorf("refresh card should render the answer-phase status line:\n%s", rendered)
	}
	// Panel collapsed because the answer started.
	elements := card["body"].(map[string]any)["elements"].([]any)
	panel := elements[0].(map[string]any)
	if panel["expanded"] != false {
		t.Errorf("panel should be collapsed once answer exists, got %v", panel["expanded"])
	}
}

func TestFeishuToolStepTitleIconColorByKind(t *testing.T) {
	cases := map[string]struct {
		kind  string
		color string
	}{
		"plain tool": {bus.ToolStepKindTool, "grey"},
		"mcp":        {bus.ToolStepKindMCP, "blue"},
		"skill":      {bus.ToolStepKindSkill, "violet"},
	}
	for name, tc := range cases {
		step := bus.ToolStep{Tool: "some_tool", Kind: tc.kind}
		title := feishuToolStepTitle(step)
		icon := title["icon"].(map[string]any)
		if icon["color"] != tc.color {
			t.Errorf("%s: icon color = %v, want %v", name, icon["color"], tc.color)
		}
	}
}

func TestBuildFeishuLoadingElementSpinnerFallback(t *testing.T) {
	withKey := buildFeishuLoadingElement(feishuPhaseThinking, "img_v2_test_spinner")
	icon := withKey["icon"].(map[string]any)
	if icon["tag"] != "custom_icon" || icon["img_key"] != "img_v2_test_spinner" {
		t.Fatalf("non-empty spinner key should render a custom_icon, got %v", icon)
	}

	withoutKey := buildFeishuLoadingElement(feishuPhaseThinking, "")
	icon = withoutKey["icon"].(map[string]any)
	if icon["tag"] != "standard_icon" {
		t.Fatalf("empty spinner key must fall back to the standard icon, got %v", icon)
	}

	// The initial streaming card must never carry the custom icon: a bad
	// img_key there would break card creation and the whole streaming reply.
	initial := buildFeishuStreamingCard()
	els := initial["body"].(map[string]any)["elements"].([]any)
	for _, el := range els {
		div, ok := el.(map[string]any)
		if !ok || div["element_id"] != feishuLoadingElementID {
			continue
		}
		icon := div["icon"].(map[string]any)
		if icon["tag"] != "standard_icon" {
			t.Fatalf("initial card must use the standard icon, got %v", icon)
		}
	}
}
