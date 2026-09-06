package feishu

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestFeishuLoadingText(t *testing.T) {
	cases := map[string]struct{ zh, en string }{
		feishuPhaseLoading:  {"正在加载上下文…", "Loading context…"},
		feishuPhaseThinking: {"🧠 正在思考…", "Thinking…"},
		feishuPhaseAnswer:   {"✍ 正在生成回答…", "Generating answer…"},
	}
	for phase, want := range cases {
		zh, en := feishuLoadingText(phase)
		if zh != want.zh || en != want.en {
			t.Errorf("phase %q = (%q, %q), want (%q, %q)", phase, zh, en, want.zh, want.en)
		}
	}
}

func TestBuildFeishuLoadingElementPhases(t *testing.T) {
	for _, phase := range []string{feishuPhaseLoading, feishuPhaseThinking, feishuPhaseAnswer} {
		el := buildFeishuLoadingElement(phase)
		if el["element_id"] != feishuLoadingElementID {
			t.Errorf("phase %q: wrong element id %v", phase, el["element_id"])
		}
		zh, _ := feishuLoadingText(phase)
		text := el["text"].(map[string]any)
		if text["content"] != zh {
			t.Errorf("phase %q: content %q != %q", phase, text["content"], zh)
		}
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
