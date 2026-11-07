package tui

import (
	"fmt"
	"strconv"
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

// renderTimeline renders every state item; width is the wrap width. Results
// are memoized per item (design §5.2) — steady-state frames only re-render
// the footer/statusbar and the tail item.
func renderTimeline(s *State, width int, md *markdownRenderer) string {
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
		b.WriteString(renderItemCached(s, it, width, md))
		b.WriteString("\n")
		prev = it.Kind
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// itemRenderKey is the cache signature: any input that changes an item's
// rendered bytes must appear here (design §5.2).
func itemRenderKey(s *State, it *Item, width int) string {
	var sb strings.Builder
	sb.WriteString(strconv.Itoa(int(it.Kind)))
	sb.WriteByte('|')
	sb.WriteString(strconv.Itoa(width))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatBool(it.Streaming))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatBool(it.Expanded))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatBool(s.RawMarkdown))
	sb.WriteByte('|')
	sb.WriteString(strconv.Itoa(len(it.Content)))
	sb.WriteByte('|')
	sb.WriteString(fnvHash(it.Content))
	if it.Approval != nil {
		sb.WriteString("|ap:")
		sb.WriteString(it.Approval.Tool)
	}
	if it.Ask != nil {
		sb.WriteString("|ask:")
		sb.WriteString(strconv.Itoa(len(it.Ask.Questions)))
	}
	sb.WriteString("|seal:")
	sb.WriteString(it.Sealed)
	if it.Kind == ItemToolCalls {
		// ToolArgs 可能被 message.update 原地改写：同长度不同内容也必须
		// 失效缓存（评审修复：仅长度会渲染陈旧参数）。
		sb.WriteString("|" + it.ToolName + "|" + fnvHash(it.ToolArgs))
	}
	// media.create 会原地更新 Attachments：数量入签名。
	sb.WriteString("|att:")
	sb.WriteString(strconv.Itoa(len(it.Attachments)))
	return sb.String()
}

func fnvHash(s string) string {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h, 36)
}

// renderItemCached returns the memoized render for one item.
func renderItemCached(s *State, it *Item, width int, md *markdownRenderer) string {
	key := itemRenderKey(s, it, width)
	if it.renderCacheKey == key && it.renderCacheVal != "" {
		return it.renderCacheVal
	}
	var b strings.Builder
	renderItem(&b, s, it, width, md)
	out := b.String()
	it.renderCacheKey = key
	it.renderCacheVal = out
	return out
}

// invalidateRenderCache clears every item's memo (width change / :raw / clear).
func (s *State) invalidateRenderCache() {
	for i := range s.Items {
		s.Items[i].renderCacheKey = ""
		s.Items[i].renderCacheVal = ""
	}
}

func renderItem(b *strings.Builder, s *State, it *Item, width int, md *markdownRenderer) {
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
		switch {
		case it.Approval != nil:
			b.WriteString(renderApprovalCard(it, width))
		case it.Ask != nil:
			b.WriteString(renderAskCard(it, width))
		default:
			b.WriteString(renderToolFeedbackLine(it, width))
		}

	case ItemAnswer:
		st := stText
		if it.Streaming {
			st = stMuted
		}
		if it.Content == "" {
			b.WriteString(st.Render("…"))
			return
		}
		if !it.Streaming && !s.RawMarkdown && md != nil {
			rendered := md.render(it.Content, max(20, width-2))
			if containsANSI(rendered) {
				b.WriteString(rendered)
				return
			}
			b.WriteString(rendered)
			return
		}
		b.WriteString(st.Width(width).Render(it.Content))

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

// renderToolFeedbackLine renders the animated tool feedback line: collapsed
// shows the bold tool name + first-line preview; expanded (Ctrl+T) shows the
// full content with diff coloring (+绿/-红，设计 §1)。
func renderToolFeedbackLine(it *Item, width int) string {
	if it.Expanded {
		var b strings.Builder
		b.WriteString(renderToolFeedback(it.Content))
		b.WriteString("\n")
		b.WriteString(renderDiffColored(it.Content, width))
		b.WriteString("\n")
		b.WriteString(stHint.Render("Ctrl+T 收起"))
		return b.String()
	}
	line := renderToolFeedback(it.Content)
	return stMuted.MaxWidth(width).Render(line)
}

// renderDiffColored wraps content lines and colors diff-looking line starts.
func renderDiffColored(content string, width int) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	// Skip the "🔧 name" header line(s): the caller already rendered them.
	start := 0
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "🔧") {
		start = 1
	}
	out := make([]string, 0, len(lines)-start)
	for _, ln := range lines[start:] {
		trimmed := strings.TrimLeft(ln, " \t")
		switch {
		case strings.HasPrefix(trimmed, "+"):
			out = append(out, diffLineSt(colDiffAddBg).MaxWidth(width).Render(ln))
		case strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---"):
			out = append(out, diffLineSt(colDiffDelBg).MaxWidth(width).Render(ln))
		default:
			out = append(out, stMuted.MaxWidth(width).Render(ln))
		}
	}
	return strings.Join(out, "\n")
}

var diffStCache = map[string]lipgloss.Style{}

func diffLineSt(bg string) lipgloss.Style {
	if st, ok := diffStCache[bg]; ok {
		return st
	}
	st := lipgloss.NewStyle().Foreground(cText).Background(lipgloss.Color(bg))
	diffStCache[bg] = st
	return st
}

// renderApprovalCard renders the tool-approval ask card (design §2.3):
// warning-tone block with tool/preview/reply-hint, or the sealed one-liner.
func renderApprovalCard(it *Item, width int) string {
	if it.Sealed != "" {
		return stDim.Render("⚠ 审批 · " + it.Approval.Tool + " — " + it.Sealed)
	}
	ap := it.Approval
	var b strings.Builder
	b.WriteString(stWarning.Bold(true).Render("⚠ 审批请求 · " + ap.Tool))
	b.WriteString("\n")
	if pv := firstLine(ap.Preview); pv != "" {
		b.WriteString(stMuted.Width(width).Render(truncateRunes(pv, max(10, width-4))))
		b.WriteString("\n")
	}
	hint := "回复 /approve 或 /deny <原因>（可加 always / all）"
	if ap.TimeoutMS > 0 {
		hint += " · " + (time.Duration(ap.TimeoutMS) * time.Millisecond).Round(time.Second).String() + " 未答复按拒绝"
	}
	b.WriteString(stHint.Width(width).Render(hint))
	return b.String()
}

// renderAskCard renders the structured ask card (design §2.3).
func renderAskCard(it *Item, width int) string {
	if it.Sealed != "" {
		label := "问询"
		if len(it.Ask.Questions) > 0 {
			label = firstLine(it.Ask.Questions[0].Question)
		}
		return stDim.Render("❓ " + truncateRunes(label, 40) + " — " + it.Sealed)
	}
	ask := it.Ask
	var b strings.Builder
	for qi, q := range ask.Questions {
		head := q.Header
		if head == "" {
			head = "问询 " + strconv.Itoa(qi+1)
		}
		b.WriteString(stAccent.Render("❓ " + head))
		b.WriteString("\n")
		b.WriteString(stText.Width(width).Render(q.Question))
		for _, opt := range q.Options {
			line := "  • " + opt.Label
			if opt.Description != "" {
				line += " — " + opt.Description
			}
			b.WriteString("\n")
			b.WriteString(stMuted.MaxWidth(width).Render(line))
		}
		if qi < len(ask.Questions)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	hint := "回复选项文本或自由输入（多选用 ; 分隔，多题每题一行）"
	if ask.TimeoutMS > 0 {
		hint += " · " + (time.Duration(ask.TimeoutMS) * time.Millisecond).Round(time.Second).String() + " 超时"
	}
	b.WriteString(stHint.Width(width).Render(hint))
	return b.String()
}

// renderCompletionPopup renders the command-completion popup above the
// editor (design §3.4): accent-selected row + dim hint footer.
func renderCompletionPopup(cs *completionState, width int) []string {
	if !cs.active() || width < 12 {
		return nil
	}
	rows := cs.visibleRows()
	if len(rows) > maxCompletionRows {
		rows = rows[:maxCompletionRows]
	}
	out := make([]string, 0, len(rows)+1)
	for i, row := range rows {
		label := row.Label
		hint := row.Hint
		if hint != "" {
			if w := lipgloss.Width(label) + lipgloss.Width(hint) + 2; w < width {
				label += strings.Repeat(" ", min(4, width-w))
			}
		}
		line := stBold.Render(label) + " " + stDim.Render(hint)
		if i == cs.selected {
			line = padBlockLines(line, width, stAccentRow)
		} else {
			line = clipLine(line, width)
		}
		out = append(out, line)
	}
	out = append(out, stHint.Render("↑↓ 选择 · Tab 补全 · Enter 选定 · Esc 关闭"))
	return out
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
	b.WriteString(stBrand.Render("鲎 Limulus tui"))
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
	if m.state.ReadOnly {
		b.WriteString(stWarning.Render(" · 只读(" + m.state.ReadOnlyChannel + ")"))
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
		"Enter 发送 · Esc 停止 · Ctrl+S 会话 · Ctrl+X 复制 · Ctrl+O 思考 · Ctrl+T 工具 · ? 帮助")
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

	brand := "鲎 Limulus tui"
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
// 无四角，对齐 pi editor）；生成中边框染 accent 色，spinner 嵌入上边框；
// 只读会话（非 pico）边框降 dim 并提示切回。
func renderEditor(m *appModel) string {
	width := m.width
	if width < 4 {
		width = 4
	}
	frameSt := stBorderLn
	if m.state.Generating {
		frameSt = stAccent
	}
	if m.state.ReadOnly {
		frameSt = stHint
	}

	var top string
	switch {
	case m.state.Generating:
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		label := stAccent.Render(string(frame) + " 生成中 · Esc 停止")
		top = horizontalLine(width, frameSt, label)
	case m.state.ReadOnly:
		label := stHint.Render("只读 · " + m.state.ReadOnlyChannel + " 渠道 · Ctrl+S 切换会话")
		top = horizontalLine(width, frameSt, label)
	default:
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
