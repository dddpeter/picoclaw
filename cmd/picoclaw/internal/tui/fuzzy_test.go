package tui

import (
	"strings"
	"testing"
)

func TestFuzzyScore_SubsequenceRequired(t *testing.T) {
	// All query runes must appear in order.
	if m := fuzzyScore("abc", "", []rune("")); m.Matches {
		t.Fatalf("empty text should not match 'abc'")
	}
	if m := fuzzyScore("sas", "", []rune(strings.ToLower("/status"))); !m.Matches {
		t.Fatalf("'sas' should subsequence-match '/status'")
	}
	if m := fuzzyScore("ms", "", []rune(strings.ToLower("/status"))); m.Matches {
		t.Fatalf("'ms' (out of order) must not match '/status'")
	}
}

func TestFuzzyScore_ConsecutiveBeatsScattered(t *testing.T) {
	// "sw" consecutive in "/switch" must score better than scattered "sw".
	consec := fuzzyScore("sw", "", []rune(strings.ToLower("/switch")))
	scattered := fuzzyScore("sw", "", []rune(strings.ToLower("/show weather")))
	if !consec.Matches || !scattered.Matches {
		t.Fatalf("both should match: %+v %+v", consec, scattered)
	}
	if consec.Score >= scattered.Score {
		t.Fatalf("consecutive match should score better: %v vs %v", consec.Score, scattered.Score)
	}
}

func TestFuzzyScore_WordBoundaryBeatsMidWord(t *testing.T) {
	// Single-rune query isolates the boundary bonus: 'm' after a space
	// (word boundary) must score better than 'm' mid-word.
	boundary := fuzzyScore("m", "", []rune("a m"))
	mid := fuzzyScore("m", "", []rune("am"))
	if !boundary.Matches || !mid.Matches {
		t.Fatalf("both should match: %+v %+v", boundary, mid)
	}
	if boundary.Score >= mid.Score {
		t.Fatalf("word-boundary should beat mid-word: %v vs %v", boundary.Score, mid.Score)
	}
}

func TestFuzzyFilter_OrdersAndLimits(t *testing.T) {
	texts := []string{"status", "stop", "switch model", "sessions"}
	got := fuzzyFilter("s", texts, 10)
	if len(got) != 4 {
		t.Fatalf("all should match 's': %v", got)
	}
	if len(fuzzyFilter("s", texts, 2)) != 2 {
		t.Fatalf("limit should cap results")
	}
	// No match yields empty.
	if got := fuzzyFilter("zzz", texts, 10); len(got) != 0 {
		t.Fatalf("no matches expected: %v", got)
	}
	// Empty query matches everything in original order.
	got = fuzzyFilter("", texts, 10)
	for i, m := range got {
		if m.Index != i {
			t.Fatalf("empty query should preserve order: %v", got)
		}
	}
}

func TestFuzzyFilter_CJK(t *testing.T) {
	texts := []string{"会话切换", "复制答案", ":copy"}
	got := fuzzyFilter("切换", texts, 10)
	if len(got) != 1 || got[0].Index != 0 {
		t.Fatalf("CJK match failed: %v", got)
	}
}
