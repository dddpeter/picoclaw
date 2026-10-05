package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// connIndicator returns the connection dot and its color.
func connIndicator(s picoclient.ConnState) (string, lipgloss.Style) {
	switch s {
	case picoclient.StateConnected:
		return "●", stSuccess
	case picoclient.StateConnecting:
		return "◌", stWarning
	default:
		return "○", stError
	}
}

// renderTimeline renders every state item; width is the wrap width.
func renderTimeline(s *State, width int) string {
	if width < 10 {
		width = 10
	}
	var b strings.Builder
	prev := ItemKind(-1)
	for i := range s.Items {
		it := &s.Items[i]
		// 用户色块之间、色块与其他内容之间留一行呼吸（pi Spacer(1)）。
		if it.Kind == ItemUser && i > 0 && prev != ItemUser {
			b.WriteString("\n")
		}
		renderItem(&b, it, width)
		b.WriteString("\n")
		prev = it.Kind
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func renderItem(b *strings.Builder, it *Item, width int) {
	switch it.Kind {
	case ItemUser:
		line := it.Content
		if it.Steering {
			line += "  " + stDim.Render("↩ 并入当前回合")
		}
		// pi 式：全宽低饱和背景块，无前缀符号。
		b.WriteString(bgBlock(line, width, lipgloss.Color(colUserBg)))

	case ItemThought:
		b.WriteString(renderThought(it, width))

	case ItemToolCalls:
		line := stBold.Render(it.ToolName)
		if args := firstLine(it.ToolArgs); args != "" {
			line += " " + stDim.Render(truncateRunes(args, max(8, width-lipgloss.Width(line)-4)))
		}
		b.WriteString(stText.MaxWidth(width).Render(line))

	case ItemToolFeedback:
		b.WriteString(stMuted.MaxWidth(width).Render(renderToolFeedback(it.Content)))

	case ItemAnswer:
		st := stText
		if it.Streaming {
			st = stMuted
		}
		if it.Content == "" {
			b.WriteString(st.Render("…"))
		} else {
			b.WriteString(st.Width(width).Render(it.Content))
		}

	case ItemError:
		line := ""
		if it.Code != "" {
			line = it.Code + ": "
		}
		b.WriteString(stError.Width(width).Render(line + it.ErrMessage))

	case ItemAction:
		b.WriteString(stDim.Width(width).Render("· " + it.Content))

	case ItemMedia:
		names := make([]string, 0, len(it.Attachments))
		for _, att := range it.Attachments {
			name := att.Filename
			if name == "" {
				name = att.URL
			}
			names = append(names, name)
		}
		b.WriteString(stMuted.Width(width).Render("📎 " + strings.Join(names, ", ")))
	}
}

// renderToolFeedback strips the "🔧 `name`" prefix into a bold tool title with
// a muted remainder（对齐 pi 的 bold(title)+muted(preview) 工具行格式）。
func renderToolFeedback(content string) string {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "🔧"))
	if !strings.HasPrefix(s, "`") {
		return s
	}
	end := strings.IndexByte(s[1:], '`')
	if end < 0 {
		return s
	}
	name := s[1 : 1+end]
	rest := strings.TrimSpace(s[2+end:])
	line := stBold.Render(name)
	if rest != "" {
		line += " " + stMuted.Render(firstLine(rest))
	}
	return line
}

// renderThought renders the folding reasoning block: pi 式 italic muted，
// 收起时一行摘要，流式期间显示尾部 3 行，Ctrl+O 切换。
func renderThought(it *Item, width int) string {
	n := utf8.RuneCountInString(it.Content)
	var header string
	switch {
	case it.Streaming:
		header = stThink.Render(fmt.Sprintf("思考中…（%d 字 · Ctrl+O）", n))
	case it.Expanded:
		header = stThink.Render(fmt.Sprintf("已思考 %d 字（Ctrl+O 收起）", n))
	default:
		header = stThink.Render(fmt.Sprintf("已思考 %d 字（Ctrl+O 展开）", n))
	}

	if it.Expanded || it.Streaming {
		lines := strings.Split(strings.TrimRight(it.Content, "\n"), "\n")
		if !it.Expanded && len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		return header + "\n" + stThink.Width(width).Render(strings.Join(lines, "\n"))
	}
	return header
}

// displayVersion strips the redundant "(git: xxx)" suffix that
// config.FormatVersion appends.
func displayVersion(v string) string {
	if i := strings.Index(v, " (git:"); i >= 0 {
		return v[:i]
	}
	return v
}

// renderStatusbar renders the top line: brand, version, connection, session,
// title, and (when disconnected) the last dial error.
func renderStatusbar(m *appModel) string {
	dot, dotSt := connIndicator(m.connState)

	var b strings.Builder
	b.WriteString(stBrand.Render("picoclaw tui"))
	if v := displayVersion(m.version); v != "" {
		b.WriteString(stHint.Render(" · " + v))
	}
	b.WriteString(dotSt.Render(" " + dot + " " + m.connState.String()))
	if session := m.client.SessionID(); len(session) > 8 {
		b.WriteString(stHint.Render(" · " + session[:8]))
	}
	if t := strings.TrimSpace(m.state.Title); t != "" {
		b.WriteString(stText.Render(" · " + truncateRunes(t, 24)))
	}
	if m.connState != picoclient.StateConnected {
		if dialErr := tail(m.client.LastDialError(), 72); dialErr != "" {
			b.WriteString(stError.Render(" · " + dialErr))
		}
	}
	return clipLine(b.String(), m.width)
}

// renderFooter renders the bottom two lines（对齐 pi footer）：
// 第一行 = token 统计 + context 用量（阈值变色）+ 右对齐模型名；
// 第二行 = 快捷键提示。
func renderFooter(m *appModel) string {
	var left strings.Builder
	if u := m.state.Usage; u != nil && u.InputTokens+u.OutputTokens > 0 {
		left.WriteString(stMuted.Render(
			"↑" + formatTokens(u.InputTokens) + " ↓" + formatTokens(u.OutputTokens)))
	}
	if u := m.state.CtxUsage; u != nil && u.TotalTokens > 0 {
		pct := u.UsedPercent
		pctSt := stMuted
		switch {
		case pct >= 90:
			pctSt = stError
		case pct >= 70:
			pctSt = stWarning
		}
		left.WriteString(pctSt.Render(fmt.Sprintf(" · ctx %.0f%%", pct)))
		left.WriteString(stHint.Render(
			" (" + formatTokens(u.UsedTokens) + "/" + formatTokens(u.TotalTokens) + ")"))
	}

	right := ""
	if m.state.ModelName != "" {
		right = stMuted.Render(m.state.ModelName)
	}
	if m.state.Generating {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		right = stAccent.Render(string(frame) + " 生成中 " + elapsed(m.state.GeneratingSince))
	}

	line1 := clipLine(joinLeftRight(left.String(), right, m.width), m.width)
	return line1 + "\n" + renderHint(m.width)
}

func renderHint(width int) string {
	return stHint.MaxWidth(width).Render(
		"Enter 发送 · Esc 停止回合 · Ctrl+N 新会话 · Ctrl+O 展开思考 · Ctrl+C 两次退出 · :help")
}

// renderWelcome is the empty-timeline panel: 两条边框线夹内容（pi 的
// DynamicBorder 面板模式）。
func renderWelcome(m *appModel, width int) string {
	if width < 10 {
		width = 10
	}
	var b strings.Builder
	b.WriteString(stBorderLn.Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	brand := "🦞 picoclaw tui"
	if v := displayVersion(m.version); v != "" {
		brand += " · " + v
	}
	b.WriteString(stBrand.Render(brand))
	b.WriteString("\n")

	dot, dotSt := connIndicator(m.connState)
	line := dotSt.Render(dot + " " + m.connState.String())
	if m.gatewayURL != "" {
		line += " " + stHint.Render(m.gatewayURL)
	}
	b.WriteString(clipLine(line, width))

	if session := m.client.SessionID(); session != "" {
		b.WriteString("\n")
		b.WriteString(stHint.Render("会话 " + session))
	}

	b.WriteString("\n\n")
	b.WriteString(stText.Width(width).Render("直接输入消息开始对话。/ 开头发送服务端命令（/status /help /cron …），: 开头为本地指令。"))
	b.WriteString("\n")
	b.WriteString(renderHint(width))
	b.WriteString("\n")
	b.WriteString(stBorderLn.Render(strings.Repeat("─", width)))
	return b.String()
}

// renderEditor draws the composer framed by two full-width "─" lines（无侧框
// 无四角，对齐 pi editor）；生成中边框染 accent 色，spinner 嵌入上边框。
func renderEditor(m *appModel) string {
	width := m.width
	if width < 4 {
		width = 4
	}
	frameSt := stBorderLn
	if m.state.Generating {
		frameSt = stAccent
	}

	var top string
	if m.state.Generating {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		label := stAccent.Render(string(frame) + " 生成中 · Esc 停止")
		top = horizontalLine(width, frameSt, label)
	} else {
		top = frameSt.Render(strings.Repeat("─", width))
	}

	return top + "\n" + m.composer.View() + "\n" + frameSt.Render(strings.Repeat("─", width))
}

func joinLeftRight(left, right string, width int) string {
	lw, rw := lipgloss.Width(left), lipgloss.Width(right)
	gap := width - lw - rw
	if gap < 1 {
		return left + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func clipLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

func elapsed(since time.Time) string {
	return time.Since(since).Truncate(time.Second).String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func tail(s string, n int) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
