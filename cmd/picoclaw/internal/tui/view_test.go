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

// TestViewLayoutInvariants: alt-screen rendering must be exactly terminal
// height, with or without progress line, overlays and completion popup.
func TestViewLayoutInvariants(t *testing.T) {
	m := newTestApp(t)

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
