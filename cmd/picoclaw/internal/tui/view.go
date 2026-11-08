package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// 主屏滚动模式渲染编排（P4，docs/design/2026-11-07-tui-p4-pi-layout-design.zh.md）。
// View 只含活区：未提交 timeline 尾部 + progress + editor + footer(2 行)；
// 已提交内容经 tea.Println 沉淀进 scrollback，与 pi 的 TuiMainScreen 行为一致。

// liveBudget returns the max lines the live region may occupy (terminal
// height minus editor + footer + breathing room).
func (m *appModel) liveBudget() int {
	if m.height <= 0 {
		return 0
	}
	progress := 0
	if m.state.Generating && m.state.Progress != "" {
		progress = 1
	}
	b := m.height - progress - m.composerLines - editorFrameLines - footerLines - 2
	if b < 0 {
		b = 0
	}
	return b
}

// itemHeights returns rendered line heights for the uncommitted tail
// (spacer included, matching renderTimeline's rhythm).
func (m *appModel) itemHeights() (live []int, total int) {
	items := m.state.Items[m.state.CommitIndex:]
	out := make([]int, len(items))
	for i := range items {
		if items[i].Kind == ItemUser && i > 0 && items[i-1].Kind != ItemUser {
			out[i] = 1 // Spacer(1) before a user block
		}
		out[i] += lipgloss.Height(renderItemCached(m.state, &items[i], m.width, m.md))
		total += out[i]
	}
	return out, total
}

// flushCommits commits stable items into the terminal scrollback via
// tea.Println (P4 §1.2)：turn 结束提交全部终态项；活区溢出预算时从顶部
// 提交到预算内。进行中（Streaming）内容从不提交，也阻挡其后的提交。
func (m *appModel) flushCommits() tea.Cmd {
	if m.fullscreen || !m.ready {
		return nil
	}
	live, total := m.itemHeights()
	if len(live) == 0 {
		return nil
	}
	budget := m.liveBudget()
	force := !m.state.Generating

	var b strings.Builder
	committed := 0
	base := m.state.CommitIndex
	for i := 0; i < len(live); i++ {
		if !ItemTerminal(&m.state.Items[base+i]) {
			break
		}
		if !force && total <= budget {
			break
		}
		// spacer 行随用户块一起提交（与 renderTimeline 节奏一致）。
		if m.state.Items[base+i].Kind == ItemUser && base+i > 0 &&
			m.state.Items[base+i-1].Kind != ItemUser {
			b.WriteString("\n")
		}
		b.WriteString(renderItemCached(m.state, &m.state.Items[base+i], m.width, m.md))
		b.WriteString("\n")
		total -= live[i]
		committed++
		if !force && total <= budget {
			break
		}
	}
	if committed == 0 {
		return nil
	}
	m.state.CommitIndex = base + committed
	return tea.Println(strings.TrimSuffix(b.String(), "\n"))
}

// commitAll force-commits every terminal item（切会话重建后整段历史入
// scrollback，配合 tea.ClearScreen，P4 §1.2）。
func (m *appModel) commitAll() tea.Cmd {
	if m.fullscreen || !m.ready {
		return nil
	}
	var b strings.Builder
	committed := 0
	base := m.state.CommitIndex
	for i := base; i < len(m.state.Items); i++ {
		if !ItemTerminal(&m.state.Items[i]) {
			break // 与 flushCommits 同语义：不跳过进行中内容
		}
		if m.state.Items[i].Kind == ItemUser && i > base && m.state.Items[i-1].Kind != ItemUser {
			b.WriteString("\n")
		}
		b.WriteString(renderItemCached(m.state, &m.state.Items[i], m.width, m.md))
		b.WriteString("\n")
		committed++
	}
	if committed == 0 {
		return nil
	}
	m.state.CommitIndex = len(m.state.Items)
	return tea.Println(strings.TrimSuffix(b.String(), "\n"))
}

// liveLines renders the uncommitted timeline tail for the live region.
func (m *appModel) liveLines() []string {
	items := m.state.Items[m.state.CommitIndex:]
	if len(items) == 0 {
		return nil
	}
	var b strings.Builder
	prev := ItemKind(-1)
	for i := range items {
		it := &items[i]
		if it.Kind == ItemUser && i > 0 && prev != ItemUser {
			b.WriteString("\n")
		}
		b.WriteString(renderItemCached(m.state, it, m.width, m.md))
		b.WriteString("\n")
		prev = it.Kind
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

// renderStartupHeader is printed once into scrollback on launch (pi 式启动头，
// P4 §2）。
func renderStartupHeader(version, gatewayURL string) string {
	var b strings.Builder
	brand := "鲎 Limulus tui"
	if v := displayVersion(version); v != "" {
		brand += " " + v
	}
	b.WriteString(stBrand.Render(brand))
	if gatewayURL != "" {
		b.WriteString("  " + stHint.Render(gatewayURL))
	}
	b.WriteString("\n")
	b.WriteString(stDim.Render("Esc") + stMuted.Render(" 打断 · ") +
		stDim.Render("Ctrl+C") + stMuted.Render(" 清空 · ") +
		stDim.Render("Ctrl+D") + stMuted.Render(" 退出 · ") +
		stDim.Render("/") + stMuted.Render(" 命令 · ") +
		stDim.Render("?") + stMuted.Render(" 帮助 · ") +
		stDim.Render("Ctrl+S") + stMuted.Render(" 会话 · ") +
		stDim.Render("Ctrl+G") + stMuted.Render(" 编辑器"))
	return b.String()
}

// renderFooterPi renders the pi-style two-line footer (P4 §2):
// L1 = gateway · title · connection（dim）；L2 = stats 左 + model 右。
func renderFooterPi(m *appModel) string {
	dot, dotSt := connIndicator(m.connState)

	// L1：网关 · 会话标题 · 连接态（断线时附 dial error）。
	var l1 strings.Builder
	l1.WriteString(stDim.Render(m.gatewayHostLabel()))
	if t := strings.TrimSpace(m.state.Title); t != "" {
		l1.WriteString(stDim.Render(" · " + truncateRunes(t, 24)))
	}
	l1.WriteString(dotSt.Render(" · " + dot + " " + m.connState.String()))
	if m.connState != picoclient.StateConnected {
		if dialErr := tail(m.client.LastDialError(), 48); dialErr != "" {
			l1.WriteString(stError.Render(" · " + dialErr))
		}
	}
	if m.state.ReadOnly {
		l1.WriteString(stWarning.Render(" · 只读(" + m.state.ReadOnlyChannel + ")"))
	}

	// L2：↑in ↓out + ctx%（左）+ model / spinner（右）。
	var stats strings.Builder
	if u := m.state.Usage; u != nil && u.InputTokens+u.OutputTokens > 0 {
		stats.WriteString("↑" + formatTokens(u.InputTokens) + " ↓" + formatTokens(u.OutputTokens) + " ")
	}
	if u := m.state.CtxUsage; u != nil && u.TotalTokens > 0 {
		pct := u.UsedPercent
		pctSt := stDim
		switch {
		case pct >= 90:
			pctSt = stError
		case pct >= 70:
			pctSt = stWarning
		}
		stats.WriteString(pctSt.Render(
			trimTrailingDotZero(fmt.Sprintf("%.1f", pct)) + "%/" + formatTokens(u.TotalTokens)))
	}
	right := m.state.ModelName
	if m.state.Generating {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		right = string(frame) + " " + elapsed(m.state.GeneratingSince)
	}
	l2 := joinLeftRight(stDim.Render(stats.String()), stDim.Render(right), m.width)

	return clipLine(l1.String(), m.width) + "\n" + clipLine(l2, m.width)
}

// trimTrailingDotZero: 34.0 → "34"，34.5 → "34.5"。
func trimTrailingDotZero(s string) string {
	if strings.HasSuffix(s, ".0") {
		return s[:len(s)-2]
	}
	return s
}

// gatewayHostLabel extracts host:port from the gateway URL for the footer.
func (m *appModel) gatewayHostLabel() string {
	u := strings.TrimPrefix(strings.TrimPrefix(m.gatewayURL, "wss://"), "ws://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		u = "gateway"
	}
	return u
}
