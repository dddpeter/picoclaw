package feishu

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func greyLine(text string) string {
	return "<font color='grey'>" + text + "</font>"
}

// answerElementContent extracts the answer element's markdown from a built
// card. Cards are inspected as maps, not marshaled JSON: encoding/json escapes
// the <font> tags the trail is made of.
func answerElementContent(t *testing.T, card map[string]any) string {
	t.Helper()
	elements := card["body"].(map[string]any)["elements"].([]any)
	for _, el := range elements {
		if m, ok := el.(map[string]any); ok && m["element_id"] == feishuAnswerElementID {
			content, _ := m["content"].(string)
			return content
		}
	}
	t.Fatalf("card has no answer element: %v", card)
	return ""
}

// TestNarrationLinesAppendAboveLiveAnswer: when the pipeline archives an
// iteration's prose (KindText step, tool calls follow), the text becomes a
// grey line above the answer slot and the next iteration typewrites below it
// instead of overwriting it. The sealed card keeps trail + final answer, while
// the card summary previews only the final answer.
func TestNarrationLinesAppendAboveLiveAnswer(t *testing.T) {
	fake := &degradeAPIFake{}
	s := newDegradeTestStreamer(t, fake)
	ctx := t.Context()

	if err := s.Update(ctx, "第一轮：我先查一下仓库结构"); err != nil {
		t.Fatalf("iteration 1 update: %v", err)
	}
	if err := s.AppendToolStep(ctx, bus.ToolStep{Kind: bus.ToolStepKindText, Result: "第一轮：我先查一下仓库结构"}); err != nil {
		t.Fatalf("archive iteration 1: %v", err)
	}
	// The panel refresh triggered by the archive already shows the pinned
	// line with an empty live slot.
	if got := answerElementContent(t, fake.lastCard); got != greyLine("第一轮：我先查一下仓库结构") {
		t.Errorf("refresh after archive: answer element = %q, want pinned grey line only", got)
	}

	// Iteration 2 must write through immediately (the pin resets the
	// throttle) and land below the pinned line.
	writesBefore := fake.streamContentCalls
	if err := s.Update(ctx, "第二轮：找到了三个文件"); err != nil {
		t.Fatalf("iteration 2 update: %v", err)
	}
	if fake.streamContentCalls != writesBefore+1 {
		t.Fatalf("first update after a pin must not be throttled, writes %d -> %d", writesBefore, fake.streamContentCalls)
	}
	want := greyLine("第一轮：我先查一下仓库结构") + "\n\n第二轮：找到了三个文件"
	if fake.lastContent != want {
		t.Fatalf("element content = %q, want %q", fake.lastContent, want)
	}

	if err := s.FinalizeWithContext(ctx, "最终答案", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got := answerElementContent(t, fake.lastCard); got != greyLine("第一轮：我先查一下仓库结构")+"\n\n最终答案" {
		t.Errorf("sealed answer element = %q, want grey trail above the final answer (iteration-2 draft replaced)", got)
	}
	summary := fake.lastCard["config"].(map[string]any)["summary"].(map[string]any)["content"].(string)
	if summary != "最终答案" {
		t.Errorf("card summary should preview the final answer only, got %q", summary)
	}
	// The panel archive of the mid-turn text is unchanged by the trail.
	s.mu.Lock()
	tools := len(s.state.Tools)
	s.mu.Unlock()
	if tools != 1 {
		t.Errorf("KindText step should still join the panel timeline, got %d tools", tools)
	}
}

// TestNarrationLineCaps: multi-paragraph prose collapses to one line, long
// text is truncated, and only the newest feishuNarrationMaxLines lines stay.
func TestNarrationLineCaps(t *testing.T) {
	if got := feishuNarrationLine("第一段\n\n第二段\t结尾  "); got != "第一段 第二段 结尾" {
		t.Errorf("whitespace should collapse to single spaces, got %q", got)
	}
	long := strings.Repeat("字", feishuNarrationLineRunes+10)
	if got := []rune(feishuNarrationLine(long)); len(got) != feishuNarrationLineRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("over-long line should be cut to %d runes plus ellipsis, got %d runes", feishuNarrationLineRunes, len(got))
	}

	s := newDegradeTestStreamer(t, &degradeAPIFake{})
	total := feishuNarrationMaxLines + 2
	for i := 1; i <= total; i++ {
		s.mu.Lock()
		s.pinNarrationLocked(fmt.Sprintf("第 %d 轮说明", i))
		s.mu.Unlock()
	}
	s.mu.Lock()
	composed := composeFeishuAnswer(s.state.Narration, "")
	s.mu.Unlock()
	for i := 1; i <= 2; i++ {
		if strings.Contains(composed, fmt.Sprintf("第 %d 轮说明", i)) {
			t.Errorf("oldest line %d should be dropped beyond the cap", i)
		}
	}
	for i := 3; i <= total; i++ {
		if !strings.Contains(composed, greyLine(fmt.Sprintf("第 %d 轮说明", i))) {
			t.Errorf("line %d should survive the cap", i)
		}
	}
}

// TestNarrationTrailSurvivesCancel: an interrupted turn keeps the trail plus
// whatever the live slot held.
func TestNarrationTrailSurvivesCancel(t *testing.T) {
	fake := &degradeAPIFake{}
	s := newDegradeTestStreamer(t, fake)
	ctx := t.Context()

	if err := s.AppendToolStep(ctx, bus.ToolStep{Kind: bus.ToolStepKindText, Result: "先看一下"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := s.Update(ctx, "写到一半"); err != nil {
		t.Fatalf("update: %v", err)
	}
	s.CancelWithReason(ctx, "stop_command")
	if got := answerElementContent(t, fake.lastCard); got != greyLine("先看一下")+"\n\n写到一半" {
		t.Errorf("cancelled answer element = %q, want trail + partial answer", got)
	}
}
