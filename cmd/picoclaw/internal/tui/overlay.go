package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// overlayKind identifies which modal is open (design §3.4/§4/§6).
type overlayKind int

const (
	overlayNone overlayKind = iota
	overlaySessions
	overlayModel
	overlayHelp
	overlayUsage
	overlayAsk
)

// overlayState is the terminal-independent state of the active modal. It owns
// its query (typed keys go here while open, not to the composer), the fuzzy
// filtered view and the cursor/scroll. Actions are executed by app.go.
type overlayState struct {
	kind     overlayKind
	query    string
	selected int
	offset   int // 选择型浮层的滚动窗口偏移
	scroll   int // 内容型浮层（help/usage）的行滚动偏移

	// sessions picker
	entries  []SessionEntry
	filtered []int // indices into entries
	current  string

	// model picker
	models []string

	// ask overlay（P3 §1）：步进式问询答题，绑定时间线卡 item ID
	ask      *askFlow
	askItem  string
	askFlash string // 最近一次动作提示行（submit-blocked 等）
}

// open resets the overlay for a new kind.
func (o *overlayState) open(kind overlayKind) {
	*o = overlayState{kind: kind}
}

// active reports whether a modal is open.
func (o *overlayState) active() bool { return o.kind != overlayNone }

// title returns the header line for the overlay.
func (o *overlayState) title() string {
	switch o.kind {
	case overlaySessions:
		return "会话"
	case overlayModel:
		return "切换模型"
	case overlayHelp:
		return "帮助"
	case overlayUsage:
		return "用量"
	case overlayAsk:
		if o.ask != nil && len(o.ask.questions) > 1 {
			return "问询 · " + strconv.Itoa(o.ask.current+1) + "/" + strconv.Itoa(len(o.ask.questions))
		}
		return "问询"
	}
	return ""
}

// typeable reports whether a key press should be folded into the query.
func (o *overlayState) typeable() bool {
	switch o.kind {
	case overlaySessions, overlayModel:
		return true // fuzzy search inputs
	}
	return false
}

// insertQuery appends typed text to the query and refilters.
func (o *overlayState) insertQuery(text string) {
	o.query += text
	o.refilter()
}

// backspace drops the last rune from the query and refilters.
func (o *overlayState) backspace() {
	if o.query == "" {
		return
	}
	r := []rune(o.query)
	o.query = string(r[:len(r)-1])
	o.refilter()
}

// refilter recomputes the fuzzy filtered rows for searchable overlays.
func (o *overlayState) refilter() {
	switch o.kind {
	case overlaySessions:
		texts := make([]string, len(o.entries))
		for i, e := range o.entries {
			texts[i] = e.searchToken()
		}
		matches := fuzzyFilter(o.query, texts, maxSessionEntries)
		o.filtered = make([]int, 0, len(matches))
		for _, m := range matches {
			o.filtered = append(o.filtered, m.Index)
		}
	case overlayModel:
		matches := fuzzyFilter(o.query, o.models, maxSessionEntries)
		o.filtered = make([]int, 0, len(matches))
		for _, m := range matches {
			o.filtered = append(o.filtered, m.Index)
		}
	}
	o.selected = 0
	o.offset = 0
}

// rows returns the number of selectable rows.
func (o *overlayState) rows() int { return len(o.filtered) }

// move shifts the selection by delta (wrapping) keeping it inside the scroll
// window; content overlays (help/usage) scroll their lines instead.
// Returns true when the view changed.
func (o *overlayState) move(delta, window int) bool {
	if !o.typeable() {
		if window <= 0 {
			return false
		}
		next := o.scroll + delta
		if next < 0 {
			next = 0
		}
		if next == o.scroll {
			return false
		}
		o.scroll = next
		return true
	}
	n := o.rows()
	if n == 0 {
		return false
	}
	next := (o.selected + delta + n) % n
	if next == o.selected {
		return false
	}
	o.selected = next
	if window > 0 {
		if o.selected < o.offset {
			o.offset = o.selected
		}
		if o.selected >= o.offset+window {
			o.offset = o.selected - window + 1
		}
	}
	return true
}

// overlayScrollLines returns the scroll offset/window hint for content
// overlays (0 for selectable ones, which use offset/selected).
func (o *overlayState) scrollLines() int { return o.scroll }

// visible returns the filtered indices within the current scroll window.
func (o *overlayState) visible(window int) []int {
	if window <= 0 {
		return o.filtered
	}
	end := min(o.offset+window, len(o.filtered))
	if o.offset >= end {
		return nil
	}
	return o.filtered[o.offset:end]
}

// pick returns the selected entry/label, or ok=false when nothing selected.
func (o *overlayState) pick() (entry *SessionEntry, model string, ok bool) {
	if len(o.filtered) == 0 || o.selected >= len(o.filtered) {
		return nil, "", false
	}
	idx := o.filtered[o.selected]
	switch o.kind {
	case overlaySessions:
		return &o.entries[idx], "", true
	case overlayModel:
		return nil, o.models[idx], true
	}
	return nil, "", false
}

// ---------------------------------------------------------------------------
// Rendering

// scrollLines returns the content-overlay window for the current scroll
// offset, clamping the offset to the content bounds（评审修复：move() 无法
// 知道内容行数，越界偏移在此收敛，防止切片越界 panic）。
func scrollLines(all []string, o *overlayState, maxLines int) []string {
	maxScroll := max(0, len(all)-maxLines)
	if o.scroll > maxScroll {
		o.scroll = maxScroll
	}
	if o.scroll < 0 {
		o.scroll = 0
	}
	end := min(o.scroll+maxLines, len(all))
	if o.scroll >= end {
		return nil
	}
	return all[o.scroll:end]
}

// overlayMaxHeight bounds an overlay to the viewport area minus breathing
// room (design §4.2: min(内容, viewport 高-2)).
func overlayMaxHeight(viewportHeight int) int {
	h := viewportHeight - 2
	if h < 3 {
		h = 3
	}
	return h
}

// overlayRenderCtx carries the app data overlays need at render time;
// keeps overlay.go free of appModel (testable in isolation).
type overlayRenderCtx struct {
	usage     *picoclient.ContextUsage
	turnUsage *picoclient.TurnUsage
	modelName string
}

// renderOverlayBox renders the full overlay: top border with embedded title,
// content lines, bottom border with the query/hint. width is terminal width.
func renderOverlayBox(o *overlayState, rc overlayRenderCtx, width, maxLines int) []string {
	if width < 12 {
		width = 12
	}
	var body []string
	switch o.kind {
	case overlaySessions:
		body = renderSessionsBody(o, width, maxLines)
	case overlayModel:
		body = renderModelBody(o, width, maxLines)
	case overlayHelp:
		body = scrollLines(renderHelpBody(width, 0), o, maxLines)
	case overlayUsage:
		body = scrollLines(renderUsageBody(rc, 0), o, maxLines)
	case overlayAsk:
		body = renderAskBody(o, width, maxLines)
	}

	title := " " + o.title() + " "
	if o.typeable() && o.query != "" {
		title += "· 搜索: " + o.query + " "
	}
	top := horizontalLine(width, stBorderLn, stHead.Render(title))
	bottom := horizontalLine(width, stBorderLn, stHint.Render(overlayFooterHint(o)))
	out := make([]string, 0, len(body)+2)
	out = append(out, top)
	out = append(out, body...)
	out = append(out, bottom)
	return out
}

func overlayFooterHint(o *overlayState) string {
	switch o.kind {
	case overlaySessions:
		return "↑↓ 选择 · Enter 切换 · Esc 关闭 · 非 pico 会话只读"
	case overlayModel:
		return "↑↓ 选择 · Enter 切换（/switch model to）· Esc 关闭"
	case overlayAsk:
		return askFooterHint(o)
	case overlayHelp, overlayUsage:
		return "↑↓ 滚动 · Esc 关闭"
	}
	return "Esc 关闭"
}

func askFooterHint(o *overlayState) string {
	if o.ask == nil {
		return "Esc 关闭"
	}
	switch o.ask.mode {
	case "input":
		return "输入自由文本 · Enter 确认 · Esc 返回"
	case "review":
		return "↑↓ 选题回改 · Enter 提交 · Esc 回末题"
	}
	return "↑↓/字母 选择 · Enter 确认 · Space 多选 · Esc 返回/关闭"
}

// overlayRow renders one selectable row; selected rows get the accent block
// padded to the full terminal width.
func overlayRow(width int, selected bool, parts ...string) string {
	line := strings.Join(nonEmpty(parts...), "  ")
	if selected {
		return padBlockLines(line, width, stAccentRow)
	}
	return stMuted.Render(line)
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func renderSessionsBody(o *overlayState, width, maxLines int) []string {
	if len(o.entries) == 0 {
		return []string{stHint.Render("未找到 workspace 会话目录（远程网关？）——仅当前会话可用")}
	}
	if len(o.filtered) == 0 {
		return []string{stHint.Render("无匹配会话：" + o.query)}
	}
	rows := o.visible(maxLines)
	selectedIdx := -1
	if o.selected < len(o.filtered) {
		selectedIdx = o.filtered[o.selected]
	}
	out := make([]string, 0, len(rows))
	for _, idx := range rows {
		e := o.entries[idx]
		selected := idx == selectedIdx
		marker := "  "
		if e.PicoID == o.current {
			marker = "● "
		}
		title := e.Title
		if title == "" {
			title = "（无标题）"
		}
		meta := relativeTime(e.UpdatedAt)
		if e.PicoID == "" {
			meta = "只读 · " + meta
		}
		out = append(out, overlayRow(width, selected, marker+e.Channel, title, meta))
	}
	return out
}

func renderModelBody(o *overlayState, width, maxLines int) []string {
	if len(o.models) == 0 {
		return []string{stHint.Render("config 未配置可用模型清单（agents.defaults.model）——用 :model <名> 直接切换")}
	}
	if len(o.filtered) == 0 {
		return []string{stHint.Render("无匹配模型：" + o.query)}
	}
	rows := o.visible(maxLines)
	selectedIdx := -1
	if o.selected < len(o.filtered) {
		selectedIdx = o.filtered[o.selected]
	}
	out := make([]string, 0, len(rows))
	for _, idx := range rows {
		name := o.models[idx]
		out = append(out, overlayRow(width, idx == selectedIdx, name))
	}
	return out
}

// renderHelpBody renders the paged help sheet: keys, ":" commands, "/" commands.
func renderHelpBody(width, maxLines int) []string {
	lines := make([]string, 0, 64)
	lines = append(lines, stHead.Render("快捷键"))
	for _, row := range helpKeyRows {
		lines = append(lines, helpRow(row[0], strings.Join(row[1:], "  ")))
	}
	lines = append(lines, "", stHead.Render(": 本地命令"))
	for _, lc := range localCommands {
		lines = append(lines, helpRow(lc.Usage, lc.Desc))
	}
	lines = append(lines, "", stHead.Render("/ 服务端命令（直通网关）"))
	for _, d := range sortedServerCommandHelp() {
		lines = append(lines, helpRow("/"+d.Name, d.Description))
	}
	lines = append(lines, "", stHint.Render("鼠标：滚轮滚动时间线 · 拖选文本按住 Shift"))
	return clipLines(lines, maxLines)
}

var helpKeyRows = [][]string{
	{"Enter", "发送（补全激活时先选定建议）"},
	{"Shift+Enter", "换行"},
	{"Esc", "停止回合（唯一打断键）/ 关闭浮层"},
	{"Ctrl+C", "清空输入框；双击退出"},
	{"Ctrl+D", "输入为空时退出"},
	{"Ctrl+N", "新会话"},
	{"Ctrl+T", "展开/折叠 Thinking"},
	{"Ctrl+O", "展开/折叠最近工具输出"},
	{"Ctrl+S", "会话选择浮层"},
	{"Ctrl+X", "复制最近一条答案"},
	{"Ctrl+G", "外部编辑器编辑输入（存盘回填）"},
	{"Ctrl+V", "粘贴剪贴板图片并发送"},
	{"?", "本帮助（输入框为空时）"},
	{"↑ / ↓", "补全/浮层导航；单行输入时回溯发送历史"},
	{"PgUp/PgDn/Home/End", "滚动（仅 --fullscreen；主屏用终端原生滚动）"},
	{"Ctrl+L", "重绘"},
}

func helpRow(key, desc string) string {
	return stDim.Render(padRight(key, 18)) + stMuted.Render(desc)
}

func padRight(s string, w int) string {
	if vis := lipgloss.Width(s); vis < w {
		return s + strings.Repeat(" ", w-vis)
	}
	return s
}

// renderUsageBody renders context/turn usage details (design :usage).
func renderUsageBody(rc overlayRenderCtx, maxLines int) []string {
	lines := make([]string, 0, 16)
	u := rc.usage
	if u == nil {
		return []string{stHint.Render("尚无用量数据——发送一条消息后回填")}
	}
	lines = append(lines, stHead.Render("上下文"))
	lines = append(lines, usageRow("已用", formatTokens(u.UsedTokens), u.UsedPercent))
	lines = append(lines, usageRow("总计", formatTokens(u.TotalTokens), 0))
	if u.HistoryTokens > 0 {
		lines = append(lines, usageRow("历史", formatTokens(u.HistoryTokens), 0))
	}
	if u.CompressAtTokens > 0 {
		lines = append(lines, usageRow("压缩水位", formatTokens(u.CompressAtTokens), 0))
	}
	if u.SummarizeAtTokens > 0 {
		lines = append(lines, usageRow("摘要水位", formatTokens(u.SummarizeAtTokens), 0))
	}
	if t := rc.turnUsage; t != nil && t.InputTokens+t.OutputTokens > 0 {
		lines = append(lines, "", stHead.Render("最近一轮"))
		lines = append(lines, stMuted.Render(fmt.Sprintf("输入 %s · 输出 %s · 合计 %s",
			formatTokens(t.InputTokens), formatTokens(t.OutputTokens), formatTokens(t.TotalTokens))))
	}
	return clipLines(lines, maxLines)
}

// usageRow renders "label value" plus an optional percent bar.
func usageRow(label, value string, pct float64) string {
	line := stDim.Render(padRight(label, 8)) + stMuted.Render(value)
	if pct > 0 {
		line += "  " + pctBar(pct, 24)
	}
	return line
}

// pctBar renders a coarse textual bar: ▓ filled / · empty.
func pctBar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct / 100 * float64(width))
	st := stMuted
	switch {
	case pct >= 90:
		st = stError
	case pct >= 70:
		st = stWarning
	}
	return st.Render(strings.Repeat("▓", filled) + stDim.Render(strings.Repeat("·", width-filled)))
}

// renderAskBody renders the ask overlay body（P3 §1.3）。
func renderAskBody(o *overlayState, width, maxLines int) []string {
	f := o.ask
	if f == nil || len(f.questions) == 0 {
		return []string{stHint.Render("无效的问询卡")}
	}
	lines := make([]string, 0, 16)
	switch f.mode {
	case "input":
		q := f.questions[f.current]
		if q.Header != "" {
			lines = append(lines, stAccent.Render("❓ "+q.Header))
		}
		lines = append(lines, stText.Width(width).Render(q.Question))
		lines = append(lines, "")
		label := f.inputLabel()
		if label == "" {
			label = stDim.Render("（输入自由回答…）")
		}
		lines = append(lines, stMuted.Render("✎ ")+stText.Render(label)+stAccent.Render("▏"))
	case "review":
		lines = append(lines, stHead.Render("回顾 — 确认后提交"))
		for i, q := range f.questions {
			ans := f.answerText(i)
			if ans == "" {
				ans = stDim.Render("（未答）")
			}
			selected := f.selected == i
			lines = append(lines, overlayRow(width, selected,
				strconv.Itoa(i+1)+".", firstLine(q.Question), ans))
		}
		selected := f.selected == len(f.questions)
		submit := "✓ 提交（发送回答）"
		if _, ok := f.buildSubmitBody(); !ok {
			submit += " — 尚有未答题"
		}
		lines = append(lines, overlayRow(width, selected, submit))
	default: // step
		q := f.questions[f.current]
		if q.Header != "" {
			lines = append(lines, stAccent.Render("❓ "+q.Header))
		}
		lines = append(lines, stText.Width(width).Render(q.Question))
		lines = append(lines, "")
		rows := f.rows()
		ans := &f.answers[f.current]
		for i, row := range rows {
			selected := i == f.selected
			var chip, label, desc string
			switch row.kind {
			case "option":
				chip = string(askOptionKeys[row.option])
				label = row.label
				desc = q.Options[row.option].Description
				if containsPick(ans.picked, row.label) {
					chip = "✓"
				}
			case "other":
				chip = "✎"
				label = "其他…"
				if containsPick(ans.picked, askOther) {
					label = "其他…（已填 " + truncateRunes(ans.other, 16) + "）"
				}
			case "confirm":
				chip = "✓"
				label = row.label
			case "skip":
				chip = "→"
				label = row.label
			}
			lines = append(lines, overlayRow(width, selected, " "+chip, label, stDim.Render(desc)))
		}
	}
	if o.askFlash != "" {
		lines = append(lines, stWarning.Render(o.askFlash))
	}
	return clipLines(lines, maxLines)
}

func containsPick(picked []string, v string) bool {
	for _, p := range picked {
		if p == v {
			return true
		}
	}
	return false
}

// clipLines keeps at most n lines.
func clipLines(lines []string, n int) []string {
	if n > 0 && len(lines) > n {
		return lines[:n]
	}
	return lines
}
