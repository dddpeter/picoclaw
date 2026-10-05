package tui

import (
	"context"
	"fmt"
	"os"
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
		initCmd:       tea.Batch(focusCmd, waitForEvent(client), waitForState(client), spin(), loadHistory(client, historyLoad)),
	}
}

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
		m.relayout()
		m.syncViewport()
		return m, nil

	case eventMsg:
		ev := picoclient.Event(msg)
		if ev.Type == "typing.start" {
			m.stopRequestedAt = time.Time{} // fresh turn: Esc/Ctrl+C stop again
		}
		if ev.Type == "typing.stop" || ev.Type == "error" {
			m.stopRequestedAt = time.Time{}
		}
		if m.state.Apply(ev) {
			m.syncViewport()
		}
		return m, waitForEvent(m.client)

	case historyMsg:
		if msg.sessionID == m.client.SessionID() {
			m.historyDone = true
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

	default:
		return m, nil
	}
}

func (m *appModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()

	switch {
	case k.Code == 'c' && k.Mod.Contains(tea.ModCtrl):
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
	return m, cmd
}

// sendCurrent ships the composer content: ":" local commands are handled
// in-process, everything else goes to the gateway as message.send.
func (m *appModel) sendCurrent() tea.Cmd {
	content := strings.TrimSpace(m.composer.Value())
	if content == "" {
		return nil
	}
	m.composer.Reset()
	m.syncComposerHeight()

	if strings.HasPrefix(content, ":") {
		return m.localCommand(content)
	}

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
		m.state.AddAction("指令：:q 退出 · :stop 停止回合 · :new 新会话 · :clear 清屏（本地）· / 前缀发送服务端命令（/status /help /stop …）")
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
	default:
		m.state.AddLocalError(fmt.Sprintf("未知本地命令 :%s（服务端命令用 / 前缀发送）", name))
		_ = rest
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
	content := renderTimeline(m.state, m.width)
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
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(renderEditor(m))
	b.WriteString("\n")
	b.WriteString(renderFooter(m))

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "Limulus tui"
	return v
}
