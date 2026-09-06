package openai_compat

import "testing"

func TestThinkSplitterStream(t *testing.T) {
	ts := newThinkSplitter()
	// simulate M3 stream: <think> in first delta, split mid-tag, answer after
	deltas := []string{"<think>User", " wants one char", "</think>\n\n", "好"}
	var rAll, aAll string
	for _, d := range deltas {
		r, a := ts.Feed(d)
		rAll += r
		aAll += a
	}
	if rAll != "User wants one char" {
		t.Errorf("reasoning = %q, want %q", rAll, "User wants one char")
	}
	if aAll != "\n\n好" {
		t.Errorf("answer = %q, want %q", aAll, "\n\n好")
	}
}

func feedAll(t *testing.T, ts *thinkSplitter, deltas []string) (reasoning, answer string) {
	t.Helper()
	for _, d := range deltas {
		r, a := ts.Feed(d)
		reasoning += r
		answer += a
	}
	return
}

func TestThinkSplitterTagSplitAcrossChunks(t *testing.T) {
	// "<think>" split at every possible boundary must still be recognized.
	for cut := 1; cut < len("<think>"); cut++ {
		ts := newThinkSplitter()
		r, a := feedAll(t, ts, []string{"前言", "<think>"[:cut], "<think>"[cut:], "推理内容"})
		if r != "推理内容" {
			t.Errorf("cut=%d: reasoning = %q, want %q", cut, r, "推理内容")
		}
		if a != "前言" {
			t.Errorf("cut=%d: answer = %q, want %q", cut, a, "前言")
		}
	}
	// "</think>" split across chunks must not leak tag bytes into either side.
	for cut := 1; cut < len("</think>"); cut++ {
		ts := newThinkSplitter()
		r, a := feedAll(t, ts, []string{"<think>推理", "</think>"[:cut], "</think>"[cut:], "答案"})
		if r != "推理" {
			t.Errorf("cut=%d: reasoning = %q, want %q", cut, r, "推理")
		}
		if a != "答案" {
			t.Errorf("cut=%d: answer = %q, want %q", cut, a, "答案")
		}
	}
}

func TestThinkSplitterCloseFlushesHeldBytes(t *testing.T) {
	ts := newThinkSplitter()
	_, a := feedAll(t, ts, []string{"答案尾巴<"})
	if a != "答案尾巴" {
		t.Errorf("answer = %q, want %q", a, "答案尾巴")
	}
	rRemain, aRemain := ts.Close()
	if rRemain != "" || aRemain != "<" {
		t.Errorf("Close = (%q, %q), want (%q, %q)", rRemain, aRemain, "", "<")
	}
}

func TestThinkSplitterUnterminatedThink(t *testing.T) {
	ts := newThinkSplitter()
	r, _ := feedAll(t, ts, []string{"<think>只有推理没闭合"})
	if r != "只有推理没闭合" {
		// Reasoning flows incrementally even without the closing tag.
		t.Errorf("reasoning = %q, want %q", r, "只有推理没闭合")
	}
}
