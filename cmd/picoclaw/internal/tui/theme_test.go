package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		0: "0", 999: "999", 1000: "1.0k", 1230: "1.2k", 9999: "10.0k",
		10_000: "10k", 87000: "87k", 1_000_000: "1.0M", 12_300_000: "12.3M",
		-5: "?",
	}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHorizontalLineWidthInvariant(t *testing.T) {
	label := stAccent.Render("⠙ 生成中 · Esc 停止")
	for _, w := range []int{20, 40, 80, 120} {
		if got := lipgloss.Width(horizontalLine(w, stBorderLn, label)); got != w {
			t.Fatalf("label w=%d: got width %d", w, got)
		}
	}
	for _, w := range []int{10, 80} {
		if got := lipgloss.Width(horizontalLine(w, stBorderLn)); got != w {
			t.Fatalf("plain w=%d: got width %d", w, got)
		}
	}
}

func TestBgBlockPadsToFullWidth(t *testing.T) {
	line := bgBlock("你好 world", 20, lipgloss.Color(colUserBg))
	if w := lipgloss.Width(line); w != 20 {
		t.Fatalf("width = %d, want 20", w)
	}
}
