package tui

// 视觉语言对齐 pi（earendil-works/pi）dark 主题的设计纪律：
//   - 色板取自 pi 的 okhsl 感知均匀色值，在此用 ANSI256 近似；
//   - 文本三级层级 text / muted / dim（dim 自带 faint，SGR 2）；
//   - bold 只给工具名与品牌行，italic 只给 thinking；
//   - 状态用低饱和背景色块表达（用户消息蓝灰底、工具三态底），少用图标。
//
// 参考：pi/packages/coding-agent/src/modes/interactive/theme/dark.json。
import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	colText      = "254" // text      okhsl(234 3% 89%)
	colMuted     = "248" // muted     okhsl(229 6% 67%)
	colDim       = "244" // dim       okhsl(229 8% 56%)
	colAccent    = "140" // accent    violet okhsl(295 50% 67%)
	colGreen     = "115" // success   okhsl(159 59% 67%)
	colRed       = "174" // error     okhsl(20 72% 67%)
	colHeading   = "185" // heading   okhsl(83 88% 67%)
	colWarning   = "179" // warning   amber（重试/进度/截断）
	colBorder    = "104" // border    okhsl(231 57% 65%)
	colBorderMut = "60"  // borderMuted okhsl(229 8% 53%)

	colUserBg     = "24"  // userMessageBg  okhsl(233 41% 24%) 深蓝灰
	colToolPendBg = "237" // toolPendingBg  okhsl(229 5% 24%)
	colToolOkBg   = "23"  // toolSuccessBg  okhsl(158 46% 25%) 深青绿
	colToolErrBg  = "52"  // toolErrorBg    okhsl(19 54% 25%)  深红
)

var (
	cText   = lipgloss.Color(colText)
	cMuted  = lipgloss.Color(colMuted)
	cDim    = lipgloss.Color(colDim)
	cAccent = lipgloss.Color(colAccent)
	cGreen  = lipgloss.Color(colGreen)
	cRed    = lipgloss.Color(colRed)
	cHead   = lipgloss.Color(colHeading)
	cWarn   = lipgloss.Color(colWarning)
	cBorder = lipgloss.Color(colBorder)
	cBMut   = lipgloss.Color(colBorderMut)
)

var (
	// 正文与三级层级
	stText  = lipgloss.NewStyle().Foreground(cText)
	stMuted = lipgloss.NewStyle().Foreground(cMuted)
	stDim   = lipgloss.NewStyle().Foreground(cDim).Faint(true)
	// 强调：bold 只给工具名/品牌，italic 只给 thinking
	stBold  = lipgloss.NewStyle().Foreground(cText).Bold(true)
	stThink = lipgloss.NewStyle().Foreground(cMuted).Italic(true)

	stBrand    = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	stHead     = lipgloss.NewStyle().Foreground(cHead)
	stAccent   = lipgloss.NewStyle().Foreground(cAccent)
	stSuccess  = lipgloss.NewStyle().Foreground(cGreen)
	stError    = lipgloss.NewStyle().Foreground(cRed)
	stWarning  = lipgloss.NewStyle().Foreground(cWarn)
	stHint     = lipgloss.NewStyle().Foreground(cDim).Faint(true)
	stBorderLn = lipgloss.NewStyle().Foreground(cBMut)
)

// spinnerFrames 与 pi loader 一致（braille，80ms 节奏）。
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// bgBlock renders content wrapped to width and padded to the full width with
// the given background color — pi 的全宽低饱和消息色块。
func bgBlock(content string, width int, bg color.Color) string {
	if width < 4 {
		width = 4
	}
	lines := strings.Split(lipgloss.NewStyle().Width(width).Render(content), "\n")
	st := lipgloss.NewStyle().Background(bg)
	for i, ln := range lines {
		if pad := width - lipgloss.Width(ln); pad > 0 {
			ln += strings.Repeat(" ", pad)
		}
		lines[i] = st.Render(ln)
	}
	return strings.Join(lines, "\n")
}

// formatTokens: 999 → "999" → "1.2k" → "15k" → "1.2M"（对齐 pi footer）。
func formatTokens(n int) string {
	switch {
	case n < 0:
		return "?"
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// horizontalLine renders a full-width "─" line in the given style, optionally
// embedding labels ("── ⠙ 生成中 ───"，pi 的 editor 边框模式)。label 与填充线
// 之间以一个空格相接，总宽恒等于 width。
func horizontalLine(width int, st lipgloss.Style, parts ...string) string {
	used := 0
	for _, p := range parts {
		used += lipgloss.Width(p) + 1
	}
	fill := width - used
	segments := make([]string, 0, len(parts)+2)
	if fill > 0 {
		segments = append(segments, st.Render(strings.Repeat("─", fill)))
	}
	segments = append(segments, parts...)
	line := strings.Join(segments, " ")
	if pad := width - lipgloss.Width(line); pad > 0 {
		line += st.Render(strings.Repeat("─", pad))
	}
	return line
}
