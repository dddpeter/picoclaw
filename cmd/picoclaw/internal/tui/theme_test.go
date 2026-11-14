package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sipeed/picoclaw/pkg/picoclient"
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

func TestSemanticPaletteSeparatesFooterRoles(t *testing.T) {
	m := newTestApp(t)
	m.state.Title = "SESSIONTITLE"
	m.state.ModelName = "MODELNAME"
	m.state.Usage = &picoclient.TurnUsage{InputTokens: 123, OutputTokens: 456}
	m.state.CtxUsage = &picoclient.ContextUsage{UsedPercent: 25, TotalTokens: 10000}
	out := renderFooterPi(m)
	for _, role := range []struct{ text, color string }{
		{"SESSIONTITLE", "180"}, {"MODELNAME", "117"},
		{"↑123", "110"}, {"↓456", "151"}, {"25%/10k", "150"},
	} {
		want := lipgloss.NewStyle().Foreground(lipgloss.Color(role.color)).Render(role.text)
		if !strings.Contains(out, want) {
			t.Errorf("%s should have its own semantic color %s", role.text, role.color)
		}
	}
	// Meaning remains legible without colors and the richer footer still fits.
	if !strings.Contains(ansi.Strip(out), "↑123 ↓456") {
		t.Fatal("token labels changed while adding colors")
	}
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatal("semantic colors changed footer width")
		}
	}
}

func TestContextPaletteKeepsWarningAndErrorThresholds(t *testing.T) {
	m := newTestApp(t)
	for _, tc := range []struct {
		percent float64
		label   string
		style   lipgloss.Style
	}{
		{75, "75%/10k", stWarning}, {95, "95%/10k", stError},
	} {
		m.state.CtxUsage = &picoclient.ContextUsage{UsedPercent: tc.percent, TotalTokens: 10000}
		if !strings.Contains(renderFooterPi(m), tc.style.Render(tc.label)) {
			t.Errorf("context at %.0f%% must retain warning/error color", tc.percent)
		}
	}
}

func TestSemanticPaletteColorsUserAndToolArguments(t *testing.T) {
	s := NewState()
	for _, tc := range []struct {
		item        Item
		text, color string
	}{
		{Item{Kind: ItemUser, Content: "USERCONTENT"}, "USERCONTENT", "153"},
		{Item{Kind: ItemToolCalls, ToolName: "exec", ToolArgs: "ARGUMENTS"}, "ARGUMENTS", "146"},
	} {
		out := renderItemCached(s, &tc.item, 80, nil)
		// A user background block can combine foreground/background SGRs;
		// checking the style's prefix avoids depending on the trailing reset.
		prefix := strings.TrimSuffix(lipgloss.NewStyle().Foreground(lipgloss.Color(tc.color)).Render(tc.text), "\x1b[m")
		if !strings.Contains(out, prefix) {
			t.Errorf("%s needs a distinct role color %s: %q", tc.text, tc.color, out)
		}
	}
}
