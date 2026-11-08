package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// newTestApp builds an appModel without starting the event loop.
func newTestApp(t *testing.T) *appModel {
	t.Helper()
	c := picoclient.New(picoclient.Config{
		URL: "ws://127.0.0.1:1/pico/ws", Token: "x", SessionID: "11111111-2222-3333-4444-555555555555",
	})
	m := newAppModel(c, "", "ws://127.0.0.1:1/pico/ws", "v0.5.4 (git: test)", nil)
	m.width, m.height = 80, 24
	m.ready = true
	return m
}

// TestViewLayoutInvariants: fullscreen 模式下 alt-screen 渲染必须精确等于
// 终端高度，有无 progress/浮层/补全均不变。主屏模式的活区不变量另测。
func TestViewLayoutInvariants(t *testing.T) {
	m := newTestApp(t)
	m.setFullscreen(true)

	check := func(name string) {
		t.Helper()
		v := m.View()
		if h := lipgloss.Height(v.Content); h != m.height {
			t.Fatalf("%s: view height = %d, want %d\n%s", name, h, m.height, v.Content)
		}
	}

	check("welcome")

	// items + generating + progress line
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	m.state.Progress = "⏱ 3m 无新进展"
	m.state.Apply(picoclient.Event{
		Type: "message.create", MessageID: "a1", Content: "你好，世界",
	})
	m.syncViewport()
	check("timeline+progress")

	// each overlay splices over the viewport area: height must not change
	for _, kind := range []overlayKind{overlaySessions, overlayModel, overlayHelp, overlayUsage, overlayAsk} {
		m.overlay.open(kind)
		switch kind {
		case overlaySessions:
			m.overlay.entries = []SessionEntry{
				{Key: "a", Channel: "pico", PicoID: "id-1", Title: "会话一"},
				{Key: "b", Channel: "feishu", Title: "飞书"},
			}
			m.overlay.current = "11111111-2222-3333-4444-555555555555"
		case overlayModel:
			m.overlay.models = []string{"glm-4.7", "kimi-k2"}
		case overlayAsk:
			m.overlay.ask = newAskFlow([]askQuestionView{{
				Question: "选一个", Header: "偏好",
				Options: []askOptionView{{Label: "A"}, {Label: "B"}}, MultiSelect: true,
			}})
		}
		m.overlay.refilter()
		check("overlay-" + strconvI(int(kind)))
	}
	m.overlay.open(overlayNone)

	// completion popup
	updateCompletion(&m.completion, "/sw")
	if !m.completion.active() {
		t.Fatal("completion should be active")
	}
	check("completion")

	// read-only session
	m.state = NewState()
	m.state.ReadOnly = true
	m.state.ReadOnlyChannel = "feishu"
	m.syncViewport()
	check("readonly")
}

// TestViewOverlayCoversViewportTail: the overlay's border must replace the
// bottom of the viewport region, not extend past the editor.
func TestViewOverlayCoversViewportTail(t *testing.T) {
	m := newTestApp(t)
	m.setFullscreen(true)
	m.state.Apply(picoclient.Event{Type: "message.create", MessageID: "a1", Content: "答案内容"})
	m.syncViewport()

	m.overlay.open(overlayHelp)
	v := m.View().Content
	lines := strings.Split(v, "\n")
	// The help overlay's top border must exist somewhere above the editor.
	found := false
	for _, ln := range lines[:len(lines)-6] {
		if strings.Contains(ln, "帮助") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("help overlay header missing:\n%s", v)
	}
}

// TestNarrowTerminal: 60-column terminal keeps every line within width.
func TestNarrowTerminal(t *testing.T) {
	m := newTestApp(t)
	m.setFullscreen(true)
	m.width, m.height = 60, 20
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	m.state.Progress = "⏱ 3m 无新进展"
	m.state.Apply(picoclient.Event{
		Type: "message.create", MessageID: "a1",
		Content: "一段比较长的回答内容，用来验证窄终端下的自动换行是否遵守宽度约束，不会撑破 60 列的终端。",
	})
	m.overlay.open(overlaySessions)
	m.overlay.entries = []SessionEntry{
		{Key: "a", Channel: "pico", PicoID: "id-1", Title: "超长会话标题超长会话标题超长会话标题超长会话标题"},
	}
	m.overlay.refilter()
	m.syncViewport()

	v := m.View().Content
	for i, ln := range strings.Split(v, "\n") {
		if w := lipgloss.Width(ln); w > 60 {
			t.Fatalf("line %d width %d > 60: %q", i, w, ln)
		}
	}
}

func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestViewMainScreen：主屏模式活区不变量（P4 §1）——
// ①活区高度恒 ≤ 终端高；②footer 固定两行且宽度合规；③溢出时保尾部。
func TestViewMainScreen(t *testing.T) {
	m := newTestApp(t) // 默认主屏模式
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	m.state.Progress = "⏱ 3m 无新进展"
	for i := 0; i < 5; i++ {
		m.state.Apply(picoclient.Event{
			Type: "message.create", MessageID: "a" + strconvI(i),
			Content: "第 " + strconvI(i) + " 条较长的回答内容，占两行以上，验证活区裁剪与提交边界逻辑。",
		})
	}
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) > m.height {
		t.Fatalf("main view height %d > terminal %d\n%s", len(lines), m.height, v.Content)
	}
	if v.AltScreen {
		t.Fatal("main mode must not use alt screen")
	}
	// footer 两行（末尾两行：L1 连接态 / L2 统计）
	l1, l2 := lines[len(lines)-2], lines[len(lines)-1]
	if !strings.Contains(l1, "connected") && !strings.Contains(l1, "connecting") && !strings.Contains(l1, "disconnected") {
		t.Fatalf("footer L1 must carry connection state: %q", l1)
	}
	_ = l2
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w > m.width {
			t.Fatalf("line %d width %d > %d: %q", i, w, m.width, ln)
		}
	}
}

// TestViewMainScreenFlushCommits：turn 结束后终态项被提交、活区清空。
func TestViewMainScreenFlushCommits(t *testing.T) {
	m := newTestApp(t)
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	m.state.Apply(picoclient.Event{
		Type: "message.create", MessageID: "a1", Content: "答案内容",
	})
	m.state.Apply(picoclient.Event{Type: "typing.stop"})
	cmd := m.flushCommits()
	if cmd == nil {
		t.Fatal("turn end must flush stable items")
	}
	if m.state.CommitIndex != len(m.state.Items) {
		t.Fatalf("commit index = %d, want %d", m.state.CommitIndex, len(m.state.Items))
	}
	// 活区清空后 View 不含已提交内容。
	v := m.View()
	if strings.Contains(v.Content, "答案内容") {
		t.Fatalf("committed content must leave the live region:\n%s", v.Content)
	}
}

// TestViewMainScreenStreamingBlocksCommit：流式项不提交、不阻挡后续。
func TestViewMainScreenStreamingBlocksCommit(t *testing.T) {
	m := newTestApp(t)
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	m.state.Apply(picoclient.Event{Type: "message.create", MessageID: "a1", Content: "流式答案", ModelName: "glm"})
	// 未 typing.stop：a1 仍 Streaming → 不得提交。
	if cmd := m.flushCommits(); cmd != nil {
		t.Fatal("streaming item must not be committed")
	}
	if m.state.CommitIndex != 0 {
		t.Fatal("commit index must stay 0 while streaming")
	}
	// 结束后可提交。
	m.state.Apply(picoclient.Event{Type: "typing.stop"})
	if cmd := m.flushCommits(); cmd == nil || m.state.CommitIndex == 0 {
		t.Fatal("turn end must commit")
	}
}

// TestViewMainScreenOverflowCommits：活区超预算时从顶部提交到预算内。
func TestViewMainScreenOverflowCommits(t *testing.T) {
	m := newTestApp(t)
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	for i := 0; i < 20; i++ {
		m.state.Apply(picoclient.Event{
			Type: "message.create", MessageID: "m" + strconvI(i),
			Content: "动作行 " + strconvI(i), Kind: "progress_note",
		})
	}
	// progress_note 不入时间线——换 action 行；保持生成中态（否则 force
	// 模式会全量提交，测不出溢出路径）。
	m.state = NewState()
	m.state.Apply(picoclient.Event{Type: "typing.start"})
	for i := 0; i < 30; i++ {
		m.state.AddAction("动作行 " + strconvI(i))
	}
	if cmd := m.flushCommits(); cmd == nil {
		t.Fatal("overflow must flush top items")
	}
	live, total := m.itemHeights()
	budget := m.liveBudget()
	if len(live) == 0 || total > budget {
		t.Fatalf("live overflow after flush: total=%d budget=%d", total, budget)
	}
}
