package picoclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	pico "github.com/sipeed/picoclaw/pkg/channels/pico"
)

// fakePicoServer upgrades the request and hands the connection to handler.
type fakePicoServer struct {
	server *httptest.Server
	auth   string // captured Authorization header, "" when dial rejected
	dials  atomic.Int32
}

func newFakePicoServer(t *testing.T, checkAuth bool, handler func(t *testing.T, conn *websocket.Conn, r *http.Request)) *fakePicoServer {
	t.Helper()
	fs := &fakePicoServer{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if checkAuth {
			const token = "test-token"
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			fs.auth = r.Header.Get("Authorization")
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fs.dials.Add(1)
		if handler != nil {
			handler(t, conn, r)
		}
	}))
	t.Cleanup(fs.server.Close)
	return fs
}

func (fs *fakePicoServer) url() string {
	return "ws" + strings.TrimPrefix(fs.server.URL, "http") + "/pico/ws"
}

// writeJSON sends a PicoMessage from the fake server to the client.
func writeJSON(t *testing.T, conn *websocket.Conn, msgType string, payload map[string]any) {
	t.Helper()
	msg := pico.PicoMessage{Type: msgType, Timestamp: time.Now().UnixMilli(), Payload: payload}
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("fake server write %s: %v", msgType, err)
	}
}

func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	c := New(Config{URL: url, Token: "test-token"})
	c.Start(t.Context())
	t.Cleanup(c.Stop)
	return c
}

func waitEvent(t *testing.T, c *Client) Event {
	t.Helper()
	select {
	case ev, ok := <-c.Events():
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func waitState(t *testing.T, c *Client) ConnState {
	t.Helper()
	select {
	case s, ok := <-c.States():
		if !ok {
			t.Fatal("states channel closed unexpectedly")
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for state")
		return StateDisconnected
	}
}

func TestClient_EventSequence(t *testing.T) {
	fs := newFakePicoServer(t, false, func(t *testing.T, conn *websocket.Conn, _ *http.Request) {
		writeJSON(t, conn, pico.TypeTypingStart, nil)
		writeJSON(t, conn, pico.TypeMessageCreate, map[string]any{
			"message_id": "t1", "content": "想一下", "kind": "thought",
		})
		writeJSON(t, conn, pico.TypeMessageUpdate, map[string]any{
			"message_id": "t1", "content": "想一下，然后动手",
		})
		writeJSON(t, conn, pico.TypeMessageCreate, map[string]any{
			"message_id": "a1", "content": "答案",
			"model_name": "glm-4.7",
			"context_usage": map[string]any{
				"used_tokens": 87000, "total_tokens": 256000, "used_percent": 34.0,
			},
			"usage": map[string]any{"input_tokens": 12300, "output_tokens": 1100},
		})
		writeJSON(t, conn, pico.TypeTypingStop, nil)
	})
	c := newTestClient(t, fs.url())

	waitState(t, c) // connecting
	waitState(t, c) // connected

	ev := waitEvent(t, c)
	if ev.Type != pico.TypeTypingStart {
		t.Fatalf("first event = %s, want typing.start", ev.Type)
	}

	ev = waitEvent(t, c)
	if !ev.IsThought() || ev.MessageID != "t1" || ev.Content != "想一下" {
		t.Fatalf("thought event wrong: %+v", ev)
	}

	ev = waitEvent(t, c)
	if ev.Type != pico.TypeMessageUpdate || ev.Content != "想一下，然后动手" {
		t.Fatalf("thought update wrong: %+v", ev)
	}

	ev = waitEvent(t, c)
	if !ev.IsAnswer() || ev.Content != "答案" || ev.ModelName != "glm-4.7" {
		t.Fatalf("answer event wrong: %+v", ev)
	}
	if ev.ContextUsage == nil || ev.ContextUsage.UsedTokens != 87000 || ev.ContextUsage.UsedPercent != 34 {
		t.Fatalf("context usage wrong: %+v", ev.ContextUsage)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 12300 || ev.Usage.OutputTokens != 1100 {
		t.Fatalf("turn usage wrong: %+v", ev.Usage)
	}

	ev = waitEvent(t, c)
	if ev.Type != pico.TypeTypingStop {
		t.Fatalf("last event = %s, want typing.stop", ev.Type)
	}
}

func TestClient_AuthBearer(t *testing.T) {
	done := make(chan struct{})
	fs := newFakePicoServer(t, true, func(_ *testing.T, conn *websocket.Conn, _ *http.Request) {
		writeJSON(t, conn, pico.TypeTypingStart, nil)
		close(done)
	})
	c := newTestClient(t, fs.url())

	ev := waitEvent(t, c)
	if ev.Type != pico.TypeTypingStart {
		t.Fatalf("expected typing.start after auth, got %s", ev.Type)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not complete")
	}
	if fs.auth != "Bearer test-token" {
		t.Fatalf("server saw Authorization %q", fs.auth)
	}
}

func TestClient_AuthRejectedKeepsReconnecting(t *testing.T) {
	fs := newFakePicoServer(t, true, nil) // token mismatch → always 401
	c := New(Config{URL: fs.url(), Token: "wrong-token"})
	c.Start(t.Context())
	t.Cleanup(c.Stop)

	states := map[ConnState]int{}
	for i := 0; i < 4; i++ {
		states[waitState(t, c)]++
	}
	if states[StateConnecting] == 0 || states[StateDisconnected] == 0 {
		t.Fatalf("expected reconnect cycling, got %v", states)
	}
	if states[StateConnected] != 0 {
		t.Fatalf("unexpected connected state with bad token: %v", states)
	}
	if dials := fs.dials.Load(); dials != 0 {
		t.Fatalf("handler saw %d dials, want 0 (all rejected pre-upgrade)", dials)
	}
}

func TestClient_Reconnect(t *testing.T) {
	var conns int
	fs := newFakePicoServer(t, false, func(t *testing.T, conn *websocket.Conn, _ *http.Request) {
		conns++
		if conns == 1 {
			// First connection: server drops it immediately.
			_ = conn.Close()
			return
		}
		writeJSON(t, conn, pico.TypeTypingStart, nil)
	})
	c := newTestClient(t, fs.url())

	// Must eventually see a second connected phase and the event from it.
	deadline := time.After(5 * time.Second)
	connectedCount := 0
	for {
		select {
		case s := <-c.States():
			if s == StateConnected {
				connectedCount++
			}
		case ev := <-c.Events():
			if ev.Type == pico.TypeTypingStart && connectedCount >= 2 {
				return // reconnected and received fresh event
			}
		case <-deadline:
			t.Fatalf("no reconnect: connected %d times", connectedCount)
		}
	}
}

func TestClient_Send(t *testing.T) {
	received := make(chan pico.PicoMessage, 1)
	fs := newFakePicoServer(t, false, func(t *testing.T, conn *websocket.Conn, r *http.Request) {
		// Echo the session_id query so the test can assert it.
		sessionID := r.URL.Query().Get("session_id")
		writeJSON(t, conn, pico.TypeMessageCreate, map[string]any{
			"message_id": "m1", "content": sessionID,
		})
		for {
			var msg pico.PicoMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Type == pico.TypeMessageSend {
				received <- msg
			}
		}
	})
	c := newTestClient(t, fs.url())
	waitEvent(t, c) // consume the session echo before sending

	if err := c.Send(t.Context(), "你好", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case msg := <-received:
		if msg.Payload["content"] != "你好" {
			t.Fatalf("server got content %v", msg.Payload["content"])
		}
		if msg.ID == "" {
			t.Fatal("message.send missing client id")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received message.send")
	}
}

func TestClient_SendNotConnected(t *testing.T) {
	c := New(Config{URL: "ws://127.0.0.1:1/pico/ws", Token: "t"})
	if err := c.Send(t.Context(), "hello", nil); err != ErrNotConnected {
		t.Fatalf("Send without connection = %v, want ErrNotConnected", err)
	}
}

func TestClient_SessionIDQueryEcho(t *testing.T) {
	fs := newFakePicoServer(t, false, func(t *testing.T, conn *websocket.Conn, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			t.Error("server: missing session_id query")
		}
		// session_id is optional on the wire; echo it to verify decode.
		msg := pico.PicoMessage{
			Type:      pico.TypeMessageCreate,
			SessionID: sessionID,
			Timestamp: time.Now().UnixMilli(),
			Payload:   map[string]any{"message_id": "m1", "content": "hi"},
		}
		if err := conn.WriteJSON(msg); err != nil {
			t.Errorf("fake server write: %v", err)
		}
	})
	c := New(Config{URL: fs.url(), Token: "test-token", SessionID: "fixed-session"})
	c.Start(t.Context())
	t.Cleanup(c.Stop)

	if c.SessionID() != "fixed-session" {
		t.Fatalf("SessionID = %q", c.SessionID())
	}
	ev := waitEvent(t, c)
	if ev.SessionID != "fixed-session" {
		t.Fatalf("event SessionID = %q", ev.SessionID)
	}
}

func TestClient_SetSessionIDRedials(t *testing.T) {
	sessions := make(chan string, 4)
	fs := newFakePicoServer(t, false, func(_ *testing.T, conn *websocket.Conn, r *http.Request) {
		sessions <- r.URL.Query().Get("session_id")
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	c := newTestClient(t, fs.url())

	first := <-sessions
	c.SetSessionID("next-session")
	second := <-sessions
	if first == "" || second != "next-session" {
		t.Fatalf("sessions: first=%q second=%q", first, second)
	}
	if c.SessionID() != "next-session" {
		t.Fatalf("SessionID = %q", c.SessionID())
	}
}

func TestDecode_ToolCallsAndKinds(t *testing.T) {
	msg := pico.PicoMessage{
		Type: pico.TypeMessageCreate,
		Payload: map[string]any{
			"message_id": "tc1",
			"kind":       "tool_calls",
			"content":    "我要执行工具",
			"tool_calls": []any{
				map[string]any{
					"id": "call-1",
					"function": map[string]any{
						"name":      "exec",
						"arguments": `{"command":"ls"}`,
					},
					"extra_content": map[string]any{
						"tool_feedback_explanation": "看看目录",
					},
				},
			},
		},
	}
	ev := Decode(msg)
	if !ev.IsToolCalls() || ev.MessageID != "tc1" {
		t.Fatalf("tool_calls event wrong: %+v", ev)
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].Name != "exec" ||
		ev.ToolCalls[0].Arguments != `{"command":"ls"}` ||
		ev.ToolCalls[0].Explanation != "看看目录" {
		t.Fatalf("tool calls decoded wrong: %+v", ev.ToolCalls)
	}
}

func TestDecode_ProgressNoteAndPlaceholderAndLegacyThought(t *testing.T) {
	progress := Decode(pico.PicoMessage{Type: pico.TypeMessageCreate, Payload: map[string]any{
		"message_id": "p1", "content": "⏱ 3m 无新进展", "kind": "progress_note",
	}})
	if !progress.IsProgressNote() || progress.IsAnswer() {
		t.Fatalf("progress note wrong: %+v", progress)
	}

	placeholder := Decode(pico.PicoMessage{Type: pico.TypeMessageCreate, Payload: map[string]any{
		"message_id": "ph1", "content": "…", "placeholder": true,
	}})
	if placeholder.IsAnswer() || !placeholder.Placeholder {
		t.Fatalf("placeholder wrong: %+v", placeholder)
	}

	legacy := Decode(pico.PicoMessage{Type: pico.TypeMessageCreate, Payload: map[string]any{
		"message_id": "t0", "content": "旧服务端思考", "thought": true,
	}})
	if !legacy.IsThought() {
		t.Fatalf("legacy thought flag not honored: %+v", legacy)
	}

	toolFeedback := Decode(pico.PicoMessage{Type: pico.TypeMessageCreate, Payload: map[string]any{
		"message_id": "f1", "content": "🔧 `exec` 运行中…",
	}})
	if !toolFeedback.IsToolFeedback() || toolFeedback.IsAnswer() {
		t.Fatalf("tool feedback wrong: %+v", toolFeedback)
	}
}

func TestDecode_Error(t *testing.T) {
	ev := Decode(pico.PicoMessage{Type: pico.TypeError, Payload: map[string]any{
		"code": "invalid_media", "message": "bad media", "request_id": "msg-3",
	}})
	if ev.Code != "invalid_media" || ev.ErrMessage != "bad media" || ev.RequestID != "msg-3" {
		t.Fatalf("error event wrong: %+v", ev)
	}
}

func TestDecode_MalformedPayloadsNeverPanic(t *testing.T) {
	cases := []pico.PicoMessage{
		{Type: pico.TypeMessageCreate, Payload: map[string]any{
			"message_id": 42, "content": 7, "tool_calls": "nope",
			"attachments": 3, "context_usage": "x", "usage": []int{1},
		}},
		{Type: pico.TypeMessageCreate},
		{Type: pico.TypeMessageUpdate, Payload: map[string]any{
			"tool_calls": []any{1, "x", nil},
			"attachments": []any{
				map[string]any{"type": "image"}, // no url → dropped
				map[string]any{"url": "ok"},
			},
		}},
	}
	for i, msg := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d panicked: %v", i, r)
				}
			}()
			Decode(msg)
		}()
	}
}

func TestConnState_String(t *testing.T) {
	if StateConnected.String() != "connected" || StateConnecting.String() != "connecting" ||
		StateDisconnected.String() != "disconnected" {
		t.Fatal("unexpected ConnState strings")
	}
}
