package pico

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialRawConnPair upgrades one raw WebSocket pair (server side wrapped in a
// picoConn by the caller) without any channel wiring: the client is returned
// untouched so tests can deliberately not read from it.
func dialRawConnPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()

	upgrader := websocket.Upgrader{}
	connCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- conn
		// Hold the server side open until the underlying conn closes.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial raw websocket: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})

	select {
	case server := <-connCh:
		t.Cleanup(func() { _ = server.Close() })
		return server, client
	case <-time.After(5 * time.Second):
		t.Fatal("websocket upgrade did not complete within 5s")
		return nil, nil
	}
}

// TestPicoConnWriteTimeoutClosesWedgedConnection pins the write deadline
// contract: a peer that stops reading (TCP zero window) must not hold the
// delivery chain forever. writeJSON must fail within picoWriteTimeout and
// close the connection so the read loop's cleanup can remove it from the
// broadcast indexes.
func TestPicoConnWriteTimeoutClosesWedgedConnection(t *testing.T) {
	server, client := dialRawConnPair(t)
	_ = client // deliberately never read: kernel buffers fill up and the
	// server-side writes block until the deadline fires.

	pc := &picoConn{id: "wedged", conn: server}

	frame := newMessage(TypeMessageUpdate, map[string]any{
		"content": strings.Repeat("x", 1<<20), // 1 MiB per frame
	})

	done := make(chan error, 1)
	go func() {
		var last error
		for i := 0; i < 64; i++ {
			if err := pc.writeJSON(frame); err != nil {
				last = err
				break
			}
		}
		done <- last
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("writeJSON never failed on a wedged peer — no write deadline in effect")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("writeJSON did not return within 30s — writes blocked without a deadline")
	}

	if !pc.closed.Load() {
		t.Fatal("connection not closed after write failure (zombie conn would stay in broadcast indexes)")
	}
}

// TestDeliverPendingOverflowClosesConnection pins the catch-up buffer cap:
// a connection in catch-up mode that overflows maxPendingCatchupFrames must
// be closed with an error instead of growing the buffer unboundedly. A
// reconnect replays the missed frames from the seq ring, so the exactly-once
// contract is unaffected.
func TestDeliverPendingOverflowClosesConnection(t *testing.T) {
	server, _ := dialRawConnPair(t)

	pc := &picoConn{id: "slow-catchup", conn: server, catchingUp: true}

	for i := 0; i < maxPendingCatchupFrames; i++ {
		if err := pc.deliver(newMessage(TypeMessageUpdate, nil)); err != nil {
			t.Fatalf("deliver frame %d: %v", i, err)
		}
	}
	if len(pc.pending) != maxPendingCatchupFrames {
		t.Fatalf("pending = %d, want %d", len(pc.pending), maxPendingCatchupFrames)
	}

	err := pc.deliver(newMessage(TypeMessageUpdate, nil))
	if err == nil {
		t.Fatal("deliver over the pending cap returned nil — buffer is unbounded")
	}
	if !pc.closed.Load() {
		t.Fatal("connection not closed on pending overflow")
	}
}
