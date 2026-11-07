package tui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

type eventMsg picoclient.Event
type stateMsg picoclient.ConnState
type spinnerTickMsg struct{}
type streamClosedMsg struct{}

type appModel struct {
	client          *picoclient.Client
	state           *State
	connState       picoclient.ConnState
	composer        textarea.Model
	viewport        viewport.Model
	width           int
	height          int
	ready           bool
	spinnerIdx      int
	lastCtrlC       time.Time
	stopRequestedAt time.Time
	sessionFile     string
	gatewayURL      string
	version         string
	historyLoad     func(sessionID string) SessionHistory
	historyDone     bool
	composerLines   int
	initCmd         tea.Cmd

	// P2（docs/design/2026-11-07-tui-p2-design.zh.md）
	completion   completionState   // "/" 与 ":" 补全浮层（§3）
	overlay      overlayState      // 会话/模型/帮助/用量浮层（§4/§6）
	md           *markdownRenderer // 终帧 markdown 渲染（§5）
	histIdx      int               // 输入回溯游标；-1 = 未在回溯（§1）
	histDraft    string            // 回溯前的草稿
	listSessions func() []SessionEntry
	historyByKey func(key string) SessionHistory
	models       []string
}

func newAppModel(client *picoclient.Client, sessionFile, gatewayURL, version string, historyLoad func(sessionID string) SessionHistory) *appModel {
	ta := textarea.New()
	ta.Placeholder = "给 picoclaw 发消息…（:help 查看指令）"
	ta.Prompt = "❯ "
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	focusCmd := ta.Focus()

	return &appModel{
		client:        client,
		state:         NewState(),
		composer:      ta,
		viewport:      viewport.New(),
		sessionFile:   sessionFile,
		gatewayURL:    gatewayURL,
		version:       version,
		historyLoad:   historyLoad,
		composerLines: 1,
		histIdx:       -1,
		md:            newMarkdownRenderer(),
		initCmd:       tea.Batch(focusCmd, waitForEvent(client), waitForState(client), spin(), loadHistory(client, historyLoad)),
	}
}

// setListSources wires the disk-backed session catalog and the config model
// list (both optional; nil/empty just disables the pickers' data).
func (m *appModel) setListSessions(fn func() []SessionEntry)       { m.listSessions = fn }
func (m *appModel) setHistoryByKey(fn func(string) SessionHistory) { m.historyByKey = fn }
func (m *appModel) setModels(models []string)                      { m.models = models }

// syncComposerHeight 让输入框随内容行数增长（1..maxComposerLines），空态
// 只占一行，不再出现成排的空 ❯ 提示行。
func (m *appModel) syncComposerHeight() {
	lines := m.composer.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > maxComposerLines {
		lines = maxComposerLines
	}
	if lines != m.composerLines {
		m.composerLines = lines
		m.composer.SetHeight(lines)
		m.relayout()
	}
}

// loadHistory reads the persisted session from disk (best-effort, off the
// UI goroutine) so a resumed session shows its past conversation.
func loadHistory(client *picoclient.Client, load func(sessionID string) SessionHistory) tea.Cmd {
	if load == nil {
		return nil
	}
	sessionID := client.SessionID()
	return func() tea.Msg {
		return historyMsg{sessionID: sessionID, history: load(sessionID)}
	}
}

type historyMsg struct {
	sessionID string
	history   SessionHistory
}

func waitForEvent(c *picoclient.Client) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-c.Events()
		if !ok {
			return streamClosedMsg{}
		}
		return eventMsg(ev)
	}
}

func waitForState(c *picoclient.Client) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-c.States()
		if !ok {
			return streamClosedMsg{}
		}
		return stateMsg(s)
	}
}

func spin() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

func (m *appModel) Init() tea.Cmd { return m.initCmd }

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.state.invalidateRenderCache()
		m.relayout()
		m.syncViewport()
		return m, nil

	case eventMsg:
		ev := picoclient.Event(msg)
		if m.state.ReadOnly {
			// 非 pico 只读视图：当前 WS 仍属于原 pico 会话，事件不混入视图
			//（设计 §4.2）；切回时会重连补发（last_seq）重建。
			return m, waitForEvent(m.client)
		}
		if ev.Type == "typing.start" {
			m.stopRequestedAt = time.Time{} // fresh turn: Esc/Ctrl+C stop again
		}
		if ev.Type == "typing.stop" || ev.Type == "error" {
			m.stopRequestedAt = time.Time{}
		}
		if m.state.Apply(ev) {
			m.syncViewport()
		}
		// catchup 丢帧且空闲：重读磁盘历史重建时间线（§2.1）。
		if ev.Type == "stream.catchup" && m.state.HistoryStale && !m.state.Generating && m.historyLoad != nil {
			return m, loadHistory(m.client, m.historyLoad)
		}
		return m, waitForEvent(m.client)

	case historyMsg:
		if msg.sessionID == m.client.SessionID() && !m.state.ReadOnly {
			m.historyDone = true
			if m.state.HistoryStale {
				// Stale rebuild: replace the timeline wholesale with disk truth.
				st := m.state
				m.state = NewState()
				m.state.RawMarkdown = st.RawMarkdown
				m.state.ModelName = st.ModelName
				m.state.CtxUsage = st.CtxUsage
				m.state.Usage = st.Usage
				m.state.InputHistory = st.InputHistory
				m.state.HistoryStale = false
			}
			m.state.ApplyHistory(msg.history.Title, msg.history.Items)
			m.syncViewport()
		}
		return m, nil

	case stateMsg:
		m.connState = picoclient.ConnState(msg)
		m.syncViewport()
		return m, waitForState(m.client)

	case spinnerTickMsg:
		if m.state.Generating {
			m.spinnerIdx++
		}
		return m, spin()

	case streamClosedMsg:
		m.state.AddLocalError("与服务器的连接已关闭，请退出后重开")
		m.syncViewport()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		nm, cmd := m.viewport.Update(msg)
		m.viewport = nm
		return m, cmd

	default:
		return m, nil
	}
}

func (m *appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()

	// Ctrl+C 永远全局生效（退出语义不变，设计 §7）。
	if k.Code == 'c' && k.Mod.Contains(tea.ModCtrl) {
		return m.handleCtrlC()
	}

	// 浮层打开时优先消费按键（§4.2/§6）。
	if m.overlay.active() {
		return m.handleOverlayKey(k)
	}

	// 补全浮层激活时先路由补全（§3.3）。
	if m.completion.active() {
		if handled, model, cmd := m.handleCompletionKey(k); handled {
			return model, cmd
		}
	}

	switch {
	case k.Code == tea.KeyEsc:
		if m.state.Generating {
			m.requestStop()
			m.syncViewport()
		}
		return m, nil

	case k.Code == 'n' && k.Mod.Contains(tea.ModCtrl):
		if m.state.Generating {
			m.state.AddAction("回合进行中，暂不能开新会话")
			m.syncViewport()
			return m, nil
		}
		m.newSession()
		return m, nil

	case k.Code == 'o' && k.Mod.Contains(tea.ModCtrl):
		if m.state.ToggleLastThought() {
			m.syncViewport()
		}
		return m, nil

	case k.Code == 't' && k.Mod.Contains(tea.ModCtrl):
		if m.state.ToggleLastToolFeedback() {
			m.syncViewport()
		}
		return m, nil

	case k.Code == 's' && k.Mod.Contains(tea.ModCtrl):
		m.openSessionsOverlay()
		return m, nil

	case k.Code == 'x' && k.Mod.Contains(tea.ModCtrl):
		return m.copyLastAnswer(1)

	case k.Code == 'l' && k.Mod.Contains(tea.ModCtrl):
		return m, tea.ClearScreen

	case k.Code == tea.KeyPgUp:
		m.viewport.PageUp()
		return m, nil

	case k.Code == tea.KeyPgDown:
		m.viewport.PageDown()
		return m, nil

	case k.Code == tea.KeyHome:
		m.viewport.GotoTop()
		return m, nil

	case k.Code == tea.KeyEnd:
		m.viewport.GotoBottom()
		return m, nil

	case k.Code == tea.KeyUp:
		if m.composer.LineCount() <= 1 {
			m.recallHistory(-1)
			return m, nil
		}

	case k.Code == tea.KeyDown:
		if m.composer.LineCount() <= 1 {
			m.recallHistory(+1)
			return m, nil
		}

	case k.Code == '?' && m.composer.Value() == "":
		m.overlay.open(overlayHelp)
		return m, nil

	case k.Code == tea.KeyEnter:
		if k.Mod.Contains(tea.ModShift) || k.Mod.Contains(tea.ModAlt) {
			m.composer.InsertString("\n")
			m.syncComposerHeight()
			return m, nil
		}
		return m, m.sendCurrent()
	}

	nm, cmd := m.composer.Update(msg)
	m.composer = nm
	m.syncComposerHeight()
	updateCompletion(&m.completion, m.composer.Value())
	return m, cmd
}

func (m *appModel) handleCtrlC() (tea.Model, tea.Cmd) {
	// Deterministic exit: a press within 1s of the previous one quits.
	// Otherwise: generating → stop the turn once (after which a single
	// Ctrl+C quits outright), idle → show the hint once (deduped).
	now := time.Now()
	within := now.Sub(m.lastCtrlC) <= time.Second
	m.lastCtrlC = now
	if within || (m.state.Generating && !m.stopRequestedAt.IsZero()) {
		return m, tea.Quit
	}
	if m.state.Generating {
		m.requestStop()
		m.state.AddAction("已请求停止当前回合；再按一次 Ctrl+C 退出")
	} else {
		m.state.AddActionDeduped("再按一次 Ctrl+C 退出")
	}
	m.syncViewport()
	return m, nil
}

// handleCompletionKey routes popup keys; handled=false falls through to the
// normal composer path (design §3.3).
func (m *appModel) handleCompletionKey(k tea.Key) (bool, tea.Model, tea.Cmd) {
	switch {
	case k.Code == tea.KeyUp:
		m.completion.move(-1)
		return true, m, nil

	case k.Code == tea.KeyDown:
		m.completion.move(+1)
		return true, m, nil

	case k.Code == tea.KeyTab:
		if repl := m.completion.accept(); repl != "" {
			m.composer.SetValue(repl)
			m.composer.CursorEnd()
			m.syncComposerHeight()
			updateCompletion(&m.completion, repl)
		}
		return true, m, nil

	case k.Code == tea.KeyEnter:
		if repl := m.completion.accept(); repl != "" {
			m.composer.SetValue(repl)
			m.composer.CursorEnd()
			m.syncComposerHeight()
		}
		m.completion.dismissed = true // Enter 选定即关（再按 Enter 才发送）
		return true, m, nil

	case k.Code == tea.KeyEsc:
		m.completion.dismissed = true
		return true, m, nil
	}
	return false, m, nil
}

// handleOverlayKey: everything except Ctrl+C feeds the modal (§4.2).
func (m *appModel) handleOverlayKey(k tea.Key) (tea.Model, tea.Cmd) {
	window := overlayMaxHeight(m.viewport.Height()) - 2
	switch {
	case k.Code == tea.KeyEsc:
		m.overlay.open(overlayNone)
		return m, nil

	case k.Code == tea.KeyEnter:
		return m.activateOverlayPick()

	case k.Code == tea.KeyUp:
		m.overlay.move(-1, window)
		return m, nil

	case k.Code == tea.KeyDown:
		m.overlay.move(+1, window)
		return m, nil

	case k.Code == tea.KeyPgUp:
		m.overlay.move(-window, window)
		return m, nil

	case k.Code == tea.KeyPgDown:
		m.overlay.move(+window, window)
		return m, nil
	}

	if m.overlay.typeable() {
		switch {
		case k.Code == tea.KeyBackspace:
			m.overlay.backspace()
			return m, nil
		case k.Code == tea.KeySpace:
			m.overlay.insertQuery(" ")
			return m, nil
		case k.Text != "" && !k.Mod.Contains(tea.ModCtrl):
			m.overlay.insertQuery(k.Text)
			return m, nil
		}
	}
	return m, nil
}

// openSessionsOverlay loads the session catalog and opens the picker.
func (m *appModel) openSessionsOverlay() {
	m.overlay.open(overlaySessions)
	if m.listSessions != nil {
		m.overlay.entries = m.listSessions()
	} else {
		m.overlay.entries = nil
	}
	m.overlay.current = m.client.SessionID()
	m.overlay.refilter()
}

// activateOverlayPick executes Enter on the modal's selection.
func (m *appModel) activateOverlayPick() (tea.Model, tea.Cmd) {
	entry, model, ok := m.overlay.pick()
	if !ok {
		return m, nil
	}
	kind := m.overlay.kind
	m.overlay.open(overlayNone)
	switch kind {
	case overlaySessions:
		if entry.PicoID == "" {
			// 非 pico 会话：只读加载（composer 禁用，§4.2）。
			m.state = NewState()
			m.state.ReadOnly = true
			m.state.ReadOnlyChannel = entry.Channel
			if m.historyByKey != nil {
				if h := m.historyByKey(entry.Key); h.Found {
					m.state.ApplyHistory(h.Title, h.Items)
				}
			}
			m.composer.Blur()
			m.syncViewport()
			return m, nil
		}
		wasReadOnly := m.state.ReadOnly
		if m.state.Generating && !wasReadOnly {
			m.state.AddAction("回合进行中，暂不能切换会话")
			m.syncViewport()
			return m, nil
		}
		if entry.PicoID == m.client.SessionID() && !wasReadOnly {
			return m, nil // current session, nothing to do
		}
		// 同 id 也重拨：只读视图期间漏掉的帧靠 last_seq 补发重建。
		m.client.SetSessionID(entry.PicoID)
		m.state = NewState()
		m.persistSession(entry.PicoID)
		m.composer.Focus()
		m.syncViewport()
		return m, loadHistory(m.client, m.historyLoad)

	case overlayModel:
		m.state.AddAction("⇄ 切换模型 → " + model)
		if err := m.client.Send(context.Background(), "/switch model to "+model, nil); err != nil {
			m.state.AddLocalError("切换请求发送失败：" + err.Error())
		}
		m.syncViewport()
		return m, nil
	}
	return m, nil
}

// recallHistory browses sent inputs with ↑/↓ (single-line composer only).
func (m *appModel) recallHistory(delta int) {
	h := m.state.InputHistory
	if len(h) == 0 {
		return
	}
	if m.histIdx == -1 {
		if delta > 0 {
			return // nothing to move forward to
		}
		m.histDraft = m.composer.Value()
		m.histIdx = len(h) - 1
	} else {
		next := m.histIdx + delta
		if next >= len(h) {
			m.histIdx = -1
			m.composer.SetValue(m.histDraft)
			m.syncComposerHeight()
			return
		}
		if next < 0 {
			next = 0
		}
		m.histIdx = next
	}
	m.composer.SetValue(h[m.histIdx])
	m.composer.CursorEnd()
	m.syncComposerHeight()
}

// copyLastAnswer puts the nth-latest answer on the clipboard via OSC52
// (tea.SetClipboard; ssh/tmux 远程可用，设计 §1/Ctrl+X)。
func (m *appModel) copyLastAnswer(n int) (tea.Model, tea.Cmd) {
	content, ok := m.state.LastAnswer(n)
	if !ok {
		m.state.AddActionDeduped(fmt.Sprintf("没有可复制的答案（倒数第 %d 条不存在）", n))
		m.syncViewport()
		return m, nil
	}
	label := "最近一条答案"
	if n > 1 {
		label = "倒数第 " + strconv.Itoa(n) + " 条答案"
	}
	m.state.AddActionDeduped("📋 已复制 " + label + "（" + strconv.Itoa(len([]rune(content))) + " 字）")
	m.syncViewport()
	return m, tea.SetClipboard(content)
}

// sendCurrent ships the composer content: ":" local commands are handled
// in-process, everything else goes to the gateway as message.send.
func (m *appModel) sendCurrent() tea.Cmd {
	if m.state.ReadOnly {
		m.state.AddActionDeduped("只读会话（" + m.state.ReadOnlyChannel + " 渠道）：发送已禁用，Ctrl+S 切换会话")
		m.syncViewport()
		return nil
	}
	content := strings.TrimSpace(m.composer.Value())
	if content == "" {
		return nil
	}
	m.composer.Reset()
	m.completion.reset()
	m.histIdx = -1
	m.histDraft = ""
	m.syncComposerHeight()

	if strings.HasPrefix(content, ":") {
		return m.localCommand(content)
	}

	m.state.PushInputHistory(content)
	m.state.AddUser(content)
	if err := m.client.Send(context.Background(), content, nil); err != nil {
		m.state.AddLocalError("发送失败：" + err.Error())
	}
	m.syncViewport()
	return nil
}

func (m *appModel) localCommand(cmd string) tea.Cmd {
	name, rest, _ := strings.Cut(strings.TrimPrefix(cmd, ":"), " ")
	name = strings.TrimSpace(name)
	switch name {
	case "help", "h":
		m.overlay.open(overlayHelp)
	case "q", "quit", "exit":
		return tea.Quit
	case "stop":
		m.requestStop()
	case "new":
		if m.state.Generating {
			m.state.AddAction("回合进行中，暂不能开新会话")
		} else {
			m.newSession()
		}
	case "clear":
		m.state = NewState()
		m.syncViewport()
	case "ls", "sessions":
		m.openSessionsOverlay()
	case "model":
		if strings.TrimSpace(rest) == "" {
			m.overlay.open(overlayModel)
			m.overlay.models = m.models
			m.overlay.refilter()
		} else {
			m.state.AddAction("⇄ 切换模型 → " + strings.TrimSpace(rest))
			if err := m.client.Send(context.Background(), "/switch model to "+strings.TrimSpace(rest), nil); err != nil {
				m.state.AddLocalError("切换请求发送失败：" + err.Error())
			}
		}
	case "status":
		return m.synthesize("/status")
	case "context":
		return m.synthesize("/context")
	case "reload":
		return m.synthesize("/reload")
	case "title":
		if strings.TrimSpace(rest) == "" {
			m.state.AddLocalError("用法：:title <文本>")
		} else {
			return m.synthesize("/title " + strings.TrimSpace(rest))
		}
	case "usage":
		m.overlay.open(overlayUsage)
	case "copy":
		n := 1
		if v, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && v > 0 {
			n = v
		}
		_, c := m.copyLastAnswer(n)
		return c
	case "raw":
		m.state.RawMarkdown = !m.state.RawMarkdown
		m.state.invalidateRenderCache()
		mode := "markdown 渲染"
		if m.state.RawMarkdown {
			mode = "原始文本"
		}
		m.state.AddAction(":raw — 答案按 " + mode + " 显示")
		m.syncViewport()
	default:
		m.state.AddLocalError(fmt.Sprintf("未知本地命令 :%s（服务端命令用 / 前缀发送）", name))
	}
	m.syncViewport()
	return nil
}

// synthesize sends a server slash command, showing a dim action line first.
func (m *appModel) synthesize(serverCmd string) tea.Cmd {
	m.state.AddAction("→ " + serverCmd)
	if err := m.client.Send(context.Background(), serverCmd, nil); err != nil {
		m.state.AddLocalError("命令发送失败：" + err.Error())
	}
	m.syncViewport()
	return nil
}

func (m *appModel) requestStop() {
	m.stopRequestedAt = time.Now()
	m.state.AddAction("⏹ 已请求停止当前回合")
	if err := m.client.Send(context.Background(), "/stop", nil); err != nil {
		m.state.AddLocalError("停止请求发送失败：" + err.Error())
	}
}

func (m *appModel) newSession() {
	id := uuid.NewString()
	m.client.SetSessionID(id)
	m.state = NewState()
	m.persistSession(id)
	m.syncViewport()
}

func (m *appModel) persistSession(id string) {
	if m.sessionFile == "" {
		return
	}
	_ = os.WriteFile(m.sessionFile, []byte(id), 0o600)
}

// relayout recomputes viewport bounds from the terminal size.
// 布局高度（行数）：statusbar(1) + viewport + progress(0/1) +
// editor(上边框1 + composer + 下边框1) + footer(2)。composer 高度自适应
// 输入行数（1..maxComposerLines，pi editor 的增长行为）。
const (
	maxComposerLines = 3
	editorFrameLines = 2
	footerLines      = 2
)

func (m *appModel) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.composer.SetWidth(m.width)
	progressLines := 0
	if m.state.Generating && m.state.Progress != "" {
		progressLines = 1
	}
	vh := m.height - 1 - progressLines - m.composerLines - editorFrameLines - footerLines
	if vh < 3 {
		vh = 3
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(vh)
}

// syncViewport re-renders the timeline, keeping the view pinned to the bottom
// only when the user is already reading the latest output. The empty state
// renders the welcome screen instead of a blank void.
func (m *appModel) syncViewport() {
	if !m.ready {
		return
	}
	follow := m.viewport.AtBottom()
	content := renderTimeline(m.state, m.width, m.md)
	if len(m.state.Items) == 0 {
		content = renderWelcome(m, m.width)
	}
	// 内容不足一屏时贴底排布（聊天语义：最新内容紧挨输入框），不再
	// 顶部对齐留一大段空白。
	if h := lipgloss.Height(content); h < m.viewport.Height() {
		content = strings.Repeat("\n", m.viewport.Height()-h) + content
	}
	m.viewport.SetContent(content)
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *appModel) View() tea.View {
	if !m.ready {
		return tea.NewView("正在启动 Limulus TUI…")
	}

	var b strings.Builder
	b.WriteString(renderStatusbar(m))
	b.WriteString("\n")

	if m.state.Generating && m.state.Progress != "" {
		b.WriteString(stWarning.MaxWidth(m.width).Render("· " + m.state.Progress))
		b.WriteString("\n")
	}

	m.relayout() // keep bounds fresh for progress-line toggling

	viewportLines := strings.Split(m.viewport.View(), "\n")
	overlayLines := m.overlayLines(len(viewportLines))
	if len(overlayLines) > 0 {
		// 叠绘在 viewport 区域底部（§3.4/§4）：主布局不变，浮层盖住
		// timeline 的最后 h 行。
		start := max(0, len(viewportLines)-len(overlayLines))
		copy(viewportLines[start:], overlayLines)
	}

	b.WriteString(strings.Join(viewportLines, "\n"))
	b.WriteString("\n")
	b.WriteString(renderEditor(m))
	b.WriteString("\n")
	b.WriteString(renderFooter(m))

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "Limulus tui"
	v.MouseMode = tea.MouseModeCellMotion // 滚轮滚动（§7）；Shift+拖动选文本
	return v
}

// overlayLines renders whichever floating panel is open: the modal overlay
// box or the smaller completion popup (mutually exclusive).
func (m *appModel) overlayLines(viewportLines int) []string {
	if m.overlay.active() {
		maxLines := overlayMaxHeight(m.viewport.Height())
		if body := renderOverlayBox(&m.overlay, overlayRenderCtx{
			usage:     m.state.CtxUsage,
			turnUsage: m.state.Usage,
			modelName: m.state.ModelName,
		}, m.width, maxLines-2); len(body) > 0 {
			if len(body) > maxLines {
				body = body[:maxLines]
			}
			return body
		}
	}
	if rows := renderCompletionPopup(&m.completion, m.width); len(rows) > 0 {
		if viewportLines < len(rows) {
			return rows[:max(1, viewportLines)]
		}
		return rows
	}
	return nil
}
