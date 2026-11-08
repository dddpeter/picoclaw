package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
type pasteImageMsg struct {
	img *clipboardImage
	err error
}
type editorDoneMsg struct {
	err error
	dir string
}

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

	// P3：终端焦点跟踪（bell 只在失焦时响）与剪贴板读取器
	focused    *bool
	clipReader *clipboardReader

	// P4：fullscreen=true 保留 P1–P3 的 alt-screen 全屏路径；默认主屏滚动
	//（docs/design/2026-11-07-tui-p4-pi-layout-design.zh.md）。
	fullscreen bool
	pendingCmd tea.Cmd // localCommand 等单返回值路径的延迟命令

	// defaultModel：config 默认模型，footer 模型位的首选回退（P4 修订②）。
	defaultModel string
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

// setFullscreen switches the render mode (P4)：主屏滚动（默认）或 --fullscreen。
func (m *appModel) setFullscreen(v bool) { m.fullscreen = v }

// setDefaultModel seeds the footer model slot（P4 修订②）。
func (m *appModel) setDefaultModel(model string) { m.defaultModel = strings.TrimSpace(model) }

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

func (m *appModel) Init() tea.Cmd {
	if m.fullscreen {
		return m.initCmd
	}
	// 主屏模式（P4）：启动头一次性打印进 scrollback。
	return tea.Batch(m.initCmd, tea.Println(renderStartupHeader(m.version, m.gatewayURL)))
}

func (m *appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.state.invalidateRenderCache()
		m.relayout()
		m.syncViewport()
		return m, m.flushCommits()

	case eventMsg:
		ev := picoclient.Event(msg)
		if m.state.ReadOnly {
			// 非 pico 只读视图：当前 WS 仍属于原 pico 会话，事件不混入视图
			//（P2 设计 §4.2）；切回时会重连补发（last_seq）重建。
			return m, waitForEvent(m.client)
		}
		if m.state.Apply(ev) {
			m.syncViewport()
		}
		// P3 §1.4：ask 卡到达自动弹答题浮层（无其他浮层打开时）；
		// 审批/问询卡必响 bell（打断式，P3 §3）。
		if ev.Ask != nil || ev.Approval != nil {
			if ev.Ask != nil && (!m.overlay.active() || m.overlay.kind == overlayAsk) {
				m.openAskForLatest()
			}
			return m, tea.Batch(waitForEvent(m.client), bell(), m.flushCommits())
		}
		// 回合结束且终端失焦（未知焦点态=响）→ bell（P3 §3）；turn 收尾提交
		// （P4 §1.2）。
		if ev.Type == "typing.stop" || ev.Type == "error" {
			cmds := []tea.Cmd{waitForEvent(m.client), m.flushCommits()}
			if m.focused == nil || !*m.focused {
				cmds = append(cmds, bell())
			}
			return m, tea.Batch(cmds...)
		}
		// catchup 丢帧且空闲：重读磁盘历史重建时间线（P2 §2.1）。
		if ev.Type == "stream.catchup" && m.state.HistoryStale && !m.state.Generating && m.historyLoad != nil {
			return m, loadHistory(m.client, m.historyLoad)
		}
		return m, tea.Batch(waitForEvent(m.client), m.flushCommits())

	case historyMsg:
		if msg.sessionID == m.client.SessionID() && !m.state.ReadOnly {
			m.historyDone = true
			var reset tea.Cmd
			if m.state.HistoryStale {
				// Stale rebuild: replace the timeline wholesale with disk truth.
				// 主屏：清屏重头在历史提交前有序执行（评审修复：原提前
				// return 会跳过 ApplyHistory 丢历史）。
				st := m.state
				m.state = NewState()
				m.state.RawMarkdown = st.RawMarkdown
				m.state.ModelName = st.ModelName
				m.state.CtxUsage = st.CtxUsage
				m.state.Usage = st.Usage
				m.state.InputHistory = st.InputHistory
				m.state.HistoryStale = false
				reset = m.resetMainScreen()
			}
			m.state.ApplyHistory(msg.history.Title, msg.history.Items)
			m.syncViewport()
			if reset != nil {
				return m, tea.Sequence(reset, m.commitAll())
			}
			return m, m.commitAll()
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

	case tea.FocusMsg:
		v := true
		m.focused = &v
		return m, nil

	case tea.BlurMsg:
		v := false
		m.focused = &v
		return m, nil

	case pasteImageMsg:
		return m.handlePasteImage(msg)

	case editorDoneMsg:
		return m.handleEditorDone(msg)

	case streamClosedMsg:
		m.state.AddLocalError("与服务器的连接已关闭，请退出后重开")
		m.syncViewport()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		if m.fullscreen {
			nm, cmd := m.viewport.Update(msg)
			m.viewport = nm
			return m, cmd
		}
		return m, nil // 主屏：滚轮归终端原生滚动（P4 §1.4）

	default:
		return m, nil
	}
}

func (m *appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()

	// Ctrl+C 全局生效（P4 §3：清空输入框，双击退出，照搬 pi）。
	if k.Code == 'c' && k.Mod.Contains(tea.ModCtrl) {
		return m.handleCtrlC()
	}
	// Ctrl+D：输入为空时退出（pi app.exit，P4 §3）。
	if k.Code == 'd' && k.Mod.Contains(tea.ModCtrl) {
		if strings.TrimSpace(m.composer.Value()) == "" {
			return m, tea.Quit
		}
		return m, nil
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
		return m, m.resetMainScreen()

	case k.Code == 'o' && k.Mod.Contains(tea.ModCtrl):
		// P4 §3：对调对齐 pi——Ctrl+O=工具输出、Ctrl+T=thinking。
		if m.state.ToggleLastToolFeedback() {
			m.syncViewport()
			return m, m.flushCommits()
		}
		return m, nil

	case k.Code == 't' && k.Mod.Contains(tea.ModCtrl):
		if m.state.ToggleLastThought() {
			m.syncViewport()
			return m, m.flushCommits()
		}
		return m, nil

	case k.Code == 's' && k.Mod.Contains(tea.ModCtrl):
		m.openSessionsOverlay()
		return m, nil

	case k.Code == 'x' && k.Mod.Contains(tea.ModCtrl):
		return m.copyLastAnswer(1)

	case k.Code == 'g' && k.Mod.Contains(tea.ModCtrl):
		return m.openExternalEditor()

	case k.Code == 'v' && k.Mod.Contains(tea.ModCtrl):
		return m, m.pasteImage()

	case k.Code == 'l' && k.Mod.Contains(tea.ModCtrl):
		return m, tea.ClearScreen

	case k.Code == tea.KeyPgUp:
		if m.fullscreen {
			m.viewport.PageUp()
		}
		return m, nil

	case k.Code == tea.KeyPgDown:
		if m.fullscreen {
			m.viewport.PageDown()
		}
		return m, nil

	case k.Code == tea.KeyHome:
		if m.fullscreen {
			m.viewport.GotoTop()
		}
		return m, nil

	case k.Code == tea.KeyEnd:
		if m.fullscreen {
			m.viewport.GotoBottom()
		}
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
	// P4 §3（照搬 pi）：Ctrl+C 清空输入框；1.5s 内双击退出。生成中的停止
	// 只走 Esc（唯一打断键）。
	now := time.Now()
	within := now.Sub(m.lastCtrlC) <= 1500*time.Millisecond
	m.lastCtrlC = now
	if within {
		return m, tea.Quit
	}
	if content := m.composer.Value(); strings.TrimSpace(content) != "" {
		m.composer.Reset()
		m.completion.reset()
		m.histIdx = -1
		m.histDraft = ""
		m.syncComposerHeight()
		return m, m.flushCommits()
	}
	m.state.AddActionDeduped("再按一次 Ctrl+C 退出")
	m.syncViewport()
	return m, m.flushCommits()
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

// handleOverlayKey: everything except Ctrl+C feeds the modal (§4.2)；ask
// 浮层有自己的三级路由（step/input/review，P3 §1.3）。
func (m *appModel) handleOverlayKey(k tea.Key) (tea.Model, tea.Cmd) {
	if m.overlay.kind == overlayAsk {
		return m.handleAskKey(k)
	}
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

// handleAskKey routes the ask overlay: input/step/review modes (P3 §1.3).
func (m *appModel) handleAskKey(k tea.Key) (tea.Model, tea.Cmd) {
	f := m.overlay.ask
	if f == nil {
		m.overlay.open(overlayNone)
		return m, nil
	}
	m.overlay.askFlash = ""

	// 输入模式：自由文本。
	if f.mode == "input" {
		switch {
		case k.Code == tea.KeyEsc:
			f.back()
			return m, nil
		case k.Code == tea.KeyEnter:
			if action := f.confirmInput(); action != "" {
				return m.handleAskAction(action)
			}
			return m, nil
		case k.Code == tea.KeyBackspace:
			label := f.inputLabel()
			if r := []rune(label); len(r) > 0 {
				f.setInput(string(r[:len(r)-1]))
			}
			return m, nil
		case k.Text != "" && !k.Mod.Contains(tea.ModCtrl):
			f.setInput(f.inputLabel() + k.Text)
			return m, nil
		}
		return m, nil
	}

	// 回顾模式。
	if f.mode == "review" {
		switch {
		case k.Code == tea.KeyEsc:
			f.back()
			return m, nil
		case k.Code == tea.KeyUp:
			f.reviewMove(-1)
			return m, nil
		case k.Code == tea.KeyDown:
			f.reviewMove(+1)
			return m, nil
		case k.Code == tea.KeyEnter:
			return m.handleAskAction(f.reviewActivate())
		}
		return m, nil
	}

	// 步进模式。
	switch {
	case k.Code == tea.KeyEsc:
		return m.handleAskAction(f.back())
	case k.Code == tea.KeyUp:
		f.move(-1)
		return m, nil
	case k.Code == tea.KeyDown:
		f.move(+1)
		return m, nil
	case k.Code == tea.KeyEnter || k.Code == tea.KeySpace:
		return m.handleAskAction(f.activate())
	}
	if k.Text != "" && !k.Mod.Contains(tea.ModCtrl) {
		upper := strings.ToUpper(k.Text)
		if r := []rune(upper); len(r) > 0 {
			if row := f.letterRow(r[0]); row >= 0 {
				f.selected = row
				return m.handleAskAction(f.activate())
			}
		}
	}
	return m, nil
}

// handleAskAction executes ask-flow action strings.
func (m *appModel) handleAskAction(action string) (tea.Model, tea.Cmd) {
	switch action {
	case "close":
		m.overlay.open(overlayNone)
		m.state.AddActionDeduped("问询已最小化：:ask 重开（或直接回复文本）")
		m.syncViewport()
	case "skip":
		m.overlay.open(overlayNone)
		return m, m.sendPlain(askDelegateMessage)
	case "submit":
		if body, ok := m.overlay.ask.buildSubmitBody(); ok {
			m.overlay.open(overlayNone)
			return m, m.sendPlain(body)
		}
	case "submit-blocked":
		m.overlay.askFlash = "尚有未答题——↑↓ 选题回改后再提交"
	}
	return m, nil
}

// sendPlain ships a plain user message（问询回答/委托共用，走既有链路；
// 生成的回答不进输入回溯历史）。
func (m *appModel) sendPlain(content string) tea.Cmd {
	m.state.AddUser(content)
	if err := m.client.Send(context.Background(), content, nil); err != nil {
		m.state.AddLocalError("发送失败：" + err.Error())
	}
	m.syncViewport()
	return nil
}

// openAskForLatest opens the ask overlay for the newest unanswered ask card.
func (m *appModel) openAskForLatest() bool {
	for i := len(m.state.Items) - 1; i >= 0; i-- {
		it := &m.state.Items[i]
		if it.Ask != nil && it.Sealed == "" {
			m.overlay.open(overlayAsk)
			m.overlay.ask = newAskFlow(askQuestionsFromInfo(it.Ask))
			m.overlay.askItem = it.ID
			return true
		}
	}
	return false
}

// askQuestionsFromInfo maps the decoded protocol questions onto flow views.
func askQuestionsFromInfo(info *picoclient.AskInfo) []askQuestionView {
	out := make([]askQuestionView, 0, len(info.Questions))
	for _, q := range info.Questions {
		qv := askQuestionView{
			Question:    q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
		}
		for _, opt := range q.Options {
			qv.Options = append(qv.Options, askOptionView{Label: opt.Label, Description: opt.Description})
		}
		out = append(out, qv)
	}
	return out
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
			if !m.fullscreen {
				// 清屏 → 头部 → 只读历史整段入 scrollback（顺序执行，P4 §1.2）。
				return m, tea.Sequence(m.resetMainScreen(), m.commitAll())
			}
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
		if !m.fullscreen {
			return m, tea.Sequence(m.resetMainScreen(), loadHistory(m.client, m.historyLoad))
		}
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
			m.pendingCmd = m.resetMainScreen()
		}
	case "clear":
		// 只读态与 :raw 偏好跨 clear 保留（评审修复：NewState 直接替换
		// 会把非 pico 只读会话“解锁”，而 composer 仍 blurred、行为错乱）。
		ro, roCh := m.state.ReadOnly, m.state.ReadOnlyChannel
		raw := m.state.RawMarkdown
		m.state = NewState()
		m.state.ReadOnly, m.state.ReadOnlyChannel, m.state.RawMarkdown = ro, roCh, raw
		m.syncViewport()
		m.pendingCmd = m.resetMainScreen()
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
	case "ask":
		if !m.openAskForLatest() {
			m.state.AddAction("当前没有待回答的问询")
		}
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
	deferred := m.pendingCmd
	m.pendingCmd = nil
	return deferred
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

// openExternalEditor hands the composer to $EDITOR via tea.ExecProcess（P3
// §2：存盘退出后回填，不自动发送；非 0 退出码保持原内容）。
func (m *appModel) openExternalEditor() (tea.Model, tea.Cmd) {
	if m.state.ReadOnly {
		m.state.AddActionDeduped("只读会话：外部编辑器不可用")
		m.syncViewport()
		return m, nil
	}
	dir, path, err := newEditorTempFile(m.composer.Value())
	if err != nil {
		m.state.AddLocalError("外部编辑器启动失败：" + err.Error())
		m.syncViewport()
		return m, nil
	}
	cmd := buildEditorCmd(resolveEditorCommand(os.Getenv), path)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{err: err, dir: dir}
	})
}

func (m *appModel) handleEditorDone(msg editorDoneMsg) (tea.Model, tea.Cmd) {
	defer os.RemoveAll(msg.dir)
	if msg.err != nil {
		m.state.AddAction("编辑器退出异常，内容未变：" + msg.err.Error())
		m.syncViewport()
		return m, nil
	}
	content, err := readEditorResult(filepath.Join(msg.dir, "prompt.md"))
	if err != nil {
		m.state.AddLocalError("读回编辑内容失败：" + err.Error())
		m.syncViewport()
		return m, nil
	}
	m.composer.SetValue(content)
	m.composer.CursorEnd()
	m.completion.reset()
	m.histIdx = -1
	m.syncComposerHeight()
	return m, nil
}

// pasteImage reads the system clipboard image off the UI loop（P3 §4）。
func (m *appModel) pasteImage() tea.Cmd {
	if m.state.ReadOnly {
		m.state.AddActionDeduped("只读会话：发送已禁用")
		m.syncViewport()
		return nil
	}
	reader := m.clipReader
	if reader == nil {
		reader = newClipboardReader()
	}
	m.state.AddActionDeduped("读取剪贴板图片…")
	m.syncViewport()
	return func() tea.Msg {
		img, err := reader.readImage()
		return pasteImageMsg{img: img, err: err}
	}
}

func (m *appModel) handlePasteImage(msg pasteImageMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.state.AddActionDeduped(msg.err.Error())
		m.syncViewport()
		return m, nil
	}
	img := msg.img
	if !img.sizeOK() {
		m.state.AddActionDeduped(fmt.Sprintf("图片过大（%.1fMB > 20MB 上限），未发送", float64(len(img.data))/1048576))
		m.syncViewport()
		return m, nil
	}
	// 本地回显：用户块 + 📎（服务端不回显用户消息，与文本发送同构）。
	m.state.AddUserMedia(picoclient.Attachment{
		Type: "image", ContentType: img.mimeType, Filename: "clipboard.png",
	})
	if err := m.client.Send(context.Background(), "", []string{img.dataURL()}); err != nil {
		m.state.AddLocalError("图片发送失败：" + err.Error())
	}
	m.syncViewport()
	return m, nil
}

// bell emits a terminal BEL（P3 §3：bubbletea v2 无原生命令，直接写
// stdout；BEL 不可见，与渲染帧交错无害）。
func bell() tea.Cmd {
	return func() tea.Msg {
		_, _ = os.Stdout.WriteString("\a")
		return nil
	}
}

func (m *appModel) requestStop() {
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

// resetMainScreen clears the terminal and reprints the startup header for a
// fresh view（切会话 / 新会话 / :clear 主屏路径，P4 §1.2）。清屏与头部
// 打印必须有序。
func (m *appModel) resetMainScreen() tea.Cmd {
	if m.fullscreen {
		return nil
	}
	return tea.Sequence(tea.ClearScreen, tea.Println(renderStartupHeader(m.version, m.gatewayURL)))
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
	// P4：顶部 statusbar 已删除，高度预算 = progress + editor + footer。
	vh := m.height - progressLines - m.composerLines - editorFrameLines - footerLines
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
	if !m.ready || !m.fullscreen {
		// 主屏模式不维护 viewport：活区每帧直读 liveLines（P4 §1）。
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

	if !m.fullscreen {
		return m.viewMain()
	}
	return m.viewFullscreen()
}

// viewMain 主屏滚动模式（P4 §1）：活区 = 未提交尾部（裁到预算）+ progress
// + editor + pi 式 footer；浮层激活时占据活区内容位（scrollback 不动）。
func (m *appModel) viewMain() tea.View {
	var b strings.Builder

	budget := m.liveBudget()
	content := m.liveLines()
	// 溢出时保尾部（进行中内容的顶部暂隐，完成后整段提交回 scrollback）。
	if len(content) > budget && budget > 0 {
		content = content[len(content)-budget:]
	}

	if m.overlay.active() {
		maxLines := overlayMaxHeight(m.height - m.composerLines - editorFrameLines - footerLines - 2)
		box := renderOverlayBox(&m.overlay, overlayRenderCtx{
			usage:     m.state.CtxUsage,
			turnUsage: m.state.Usage,
			modelName: m.state.ModelName,
		}, m.width, maxLines-2)
		if len(box) > maxLines {
			box = box[:maxLines]
		}
		for _, ln := range box {
			b.WriteString(clipLine(ln, m.width))
			b.WriteString("\n")
		}
	} else {
		if rows := renderCompletionPopup(&m.completion, m.width); len(rows) > 0 {
			keep := max(0, budget-len(rows))
			if len(content) > keep {
				content = content[len(content)-keep:]
			}
			for _, ln := range rows {
				b.WriteString(ln)
				b.WriteString("\n")
			}
		}
		for _, ln := range content {
			b.WriteString(clipLine(ln, m.width))
			b.WriteString("\n")
		}
		if m.state.Generating && m.state.Progress != "" {
			b.WriteString(stWarning.MaxWidth(m.width).Render("· " + m.state.Progress))
			b.WriteString("\n")
		}
	}

	b.WriteString(renderEditor(m))
	b.WriteString("\n")
	b.WriteString(renderFooterPi(m))

	v := tea.NewView(b.String())
	v.AltScreen = false // 主屏：对话沉淀 scrollback（P4）
	v.WindowTitle = "Limulus tui"
	v.MouseMode = tea.MouseModeNone // 滚轮/选择归终端原生
	return v
}

// viewFullscreen 保留 P1–P3 的 alt-screen 路径（--fullscreen），但 footer
// 换 pi 式两行、去掉顶部 statusbar（P4 §2）。
func (m *appModel) viewFullscreen() tea.View {
	var b strings.Builder

	if m.state.Generating && m.state.Progress != "" {
		b.WriteString(stWarning.MaxWidth(m.width).Render("· " + m.state.Progress))
		b.WriteString("\n")
	}

	m.relayout() // keep bounds fresh for progress-line toggling

	viewportLines := strings.Split(m.viewport.View(), "\n")
	if overlayLines := m.overlayLines(len(viewportLines)); len(overlayLines) > 0 {
		// 叠绘在 viewport 区域底部（P2 §3.4）：主布局不变，浮层盖住
		// timeline 的最后 h 行。
		start := max(0, len(viewportLines)-len(overlayLines))
		copy(viewportLines[start:], overlayLines)
	}

	b.WriteString(strings.Join(viewportLines, "\n"))
	b.WriteString("\n")
	b.WriteString(renderEditor(m))
	b.WriteString("\n")
	b.WriteString(renderFooterPi(m))

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "Limulus tui"
	v.MouseMode = tea.MouseModeCellMotion // 滚轮滚动；Shift+拖动选文本
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
