package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// newKeyModel builds an appModel wired with one fake pico session entry.
func newKeyModel(t *testing.T, readonly bool) *appModel {
	t.Helper()
	m := newAppModel(picoclient.New(picoclient.Config{}), "", "", "test", nil)
	m.setListSessions(func() []SessionEntry {
		return []SessionEntry{{Key: "k", PicoID: "p1", Title: "t"}}
	})
	if readonly {
		m.state.ReadOnly = true
	}
	return m
}

// typeRunes feeds printable characters through Update like a real keyboard.
func typeRunes(t *testing.T, m tea.Model, s string) tea.Model {
	t.Helper()
	for _, r := range s {
		model, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = model
	}
	return m
}

func pressEnter(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	model, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return model
}

// 完整命令 + 单次 Enter 必须直接执行：补全层的"选定"与已输入文本一致时
// 不得吞掉这次 Enter（修复前 :sessions 按一次回车无任何反馈）。
func TestSessionsCommandSingleEnterOpensOverlay(t *testing.T) {
	m := typeRunes(t, newKeyModel(t, false), ":sessions")
	m = pressEnter(t, m)
	app := m.(*appModel)
	if app.overlay.kind != overlaySessions {
		t.Fatalf("overlay.kind=%d, want overlaySessions(%d)", app.overlay.kind, overlaySessions)
	}
	if v := app.composer.Value(); v != "" {
		t.Fatalf("composer=%q, want cleared", v)
	}
}

// 部分前缀 + Enter 仍只选定补全（防误发半截命令，P2 设计 §3.3），
// 第二次 Enter 才发送。
func TestPartialCommandEnterSelectsThenSends(t *testing.T) {
	m := typeRunes(t, newKeyModel(t, false), ":sess")

	m = pressEnter(t, m) // 第一次：补全成 :sessions，不发送
	app := m.(*appModel)
	if app.composer.Value() != ":sessions" {
		t.Fatalf("composer=%q, want completed :sessions", app.composer.Value())
	}
	if app.overlay.active() {
		t.Fatalf("overlay opened on first Enter, want select-only")
	}

	m = pressEnter(t, m) // 第二次：发送执行
	app = m.(*appModel)
	if app.overlay.kind != overlaySessions {
		t.Fatalf("overlay.kind=%d after second Enter, want overlaySessions", app.overlay.kind)
	}
}

// 只读会话（非 pico 渠道视图）下本地命令不受"发送已禁用"拦截。
func TestSessionsCommandWorksWhenReadOnly(t *testing.T) {
	m := typeRunes(t, newKeyModel(t, true), ":sessions")
	m = pressEnter(t, m)
	app := m.(*appModel)
	if app.overlay.kind != overlaySessions {
		t.Fatalf("overlay.kind=%d in read-only session, want overlaySessions", app.overlay.kind)
	}
}

// 只读会话下普通聊天消息仍被拦截并提示。
func TestReadOnlyStillBlocksNormalSend(t *testing.T) {
	m := typeRunes(t, newKeyModel(t, true), "hello")
	m = pressEnter(t, m)
	app := m.(*appModel)
	if app.overlay.active() {
		t.Fatalf("overlay should not open for chat text")
	}
	blocked := false
	for _, it := range app.state.Items {
		if it.Kind == ItemAction && strings.Contains(it.Content, "发送已禁用") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatalf("no 只读 blocking hint; items=%v", func() []string {
			var out []string
			for _, it := range app.state.Items {
				out = append(out, fmt.Sprintf("%d:%s", it.Kind, it.Content))
			}
			return out
		}())
	}
}
