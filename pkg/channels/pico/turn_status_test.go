package pico

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialTestPicoWS opens a real WS connection through the channel's HTTP
// handler with Bearer auth and a session id.
func dialTestPicoWS(t *testing.T, serverURL, sessionID string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") + "/pico/ws?session_id=" + sessionID
	header := http.Header{"Authorization": {"Bearer test-token"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Dial(%s): %v", sessionID, err)
	}
	_ = resp.Body.Close()
	return conn
}

func readTestPicoFrame(t *testing.T, conn *websocket.Conn) PicoMessage {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg PicoMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	return msg
}

// TestTurnStatusFrameOnConnect pins the reconnect signal (octop borrowing
// §四): a freshly connected client immediately learns whether a turn is
// live for its session, so a page refresh no longer loses the typing state.
func TestTurnStatusFrameOnConnect(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())

	ch.SetTurnActiveProbe(func(chatID string) bool { return chatID == "pico:sess-live" })

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	live := dialTestPicoWS(t, server.URL, "sess-live")
	defer live.Close()
	frame := readTestPicoFrame(t, live)
	if frame.Type != "turn.status" {
		t.Fatalf("first frame for a live session = %q, want turn.status", frame.Type)
	}
	if frame.SessionID != "sess-live" {
		t.Fatalf("frame session = %q, want sess-live", frame.SessionID)
	}
	if active, _ := frame.Payload["active"].(bool); !active {
		t.Fatalf("live session frame must carry active=true: %+v", frame.Payload)
	}

	idle := dialTestPicoWS(t, server.URL, "sess-idle")
	defer idle.Close()
	frame = readTestPicoFrame(t, idle)
	if frame.Type != "turn.status" {
		t.Fatalf("first frame for an idle session = %q, want turn.status", frame.Type)
	}
	if active, _ := frame.Payload["active"].(bool); active {
		t.Fatalf("idle session frame must carry active=false: %+v", frame.Payload)
	}
}

// TestNoTurnStatusWithoutProbe pins the opt-in shape: channels without a
// probe wired (e.g. unit setups) send no extra frame — the first reply to a
// ping is the pong.
func TestNoTurnStatusWithoutProbe(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	conn := dialTestPicoWS(t, server.URL, "sess-noprobe")
	defer conn.Close()

	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteJSON(PicoMessage{Type: TypePing, ID: "p1"}); err != nil {
		t.Fatalf("WriteJSON ping: %v", err)
	}
	frame := readTestPicoFrame(t, conn)
	if frame.Type != TypePong {
		t.Fatalf("first frame without probe = %q, want pong (no turn.status)", frame.Type)
	}
}
