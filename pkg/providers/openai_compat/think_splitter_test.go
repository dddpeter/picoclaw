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
