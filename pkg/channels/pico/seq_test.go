package pico

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// --- unit-level: seq buffer math ---

// TestPicoSeqMonotonicUnderConcurrency pins the seq counter (trae borrowing
// §一): concurrent broadcasts must produce a gap-free monotonic sequence.
// Monotonicity is guaranteed under the buffer's own mutex — never under
// connsMu or any model lock (AGENTS.md).
func TestPicoSeqMonotonicUnderConcurrency(t *testing.T) {
	buf := newSessionSeqBuffer(DefaultSeqBufferCapacity)

	const goroutines, perGoroutine = 16, 100
	var wg sync.WaitGroup
	seqs := make(chan uint64, goroutines*perGoroutine)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				msg := newMessage(TypeMessageUpdate, nil)
				buf.assign(0, &msg)
				seqs <- msg.Seq
			}
		}()
	}
	wg.Wait()
	close(seqs)

	seen := make(map[uint64]bool, goroutines*perGoroutine)
	for seq := range seqs {
		if seq == 0 {
			t.Fatal("assigned seq must never be 0 (reserved for seq-less frames)")
		}
		if seen[seq] {
			t.Fatalf("duplicate seq %d under concurrency", seq)
		}
		seen[seq] = true
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("assigned %d seqs, want %d", len(seen), goroutines*perGoroutine)
	}
	for seq := uint64(1); seq <= uint64(goroutines*perGoroutine); seq++ {
		if !seen[seq] {
			t.Fatalf("seq %d missing from monotonic series", seq)
		}
	}
}

// TestSeqBufferIsolationBetweenSessions pins per-session counters: seqs in
// one session never advance another's, and one session's cursor is invalid
// in another's stream.
func TestSeqBufferIsolationBetweenSessions(t *testing.T) {
	store := newSeqStore(DefaultSeqBufferCapacity)

	msgA := newMessage(TypeMessageUpdate, nil)
	store.assign("sess-a", &msgA)
	store.assign("sess-a", &msgA)
	if msgA.Seq != 2 {
		t.Fatalf("sess-a second seq = %d, want 2", msgA.Seq)
	}

	msgB := newMessage(TypeMessageUpdate, nil)
	store.assign("sess-b", &msgB)
	if msgB.Seq != 1 {
		t.Fatalf("sess-b first seq = %d, want 1 (independent counter)", msgB.Seq)
	}

	// Unknown session: empty, complete replay.
	replay := store.replaySince("sess-unknown", 0)
	if !replay.complete || len(replay.frames) != 0 {
		t.Fatalf("unknown session replay = %+v, want empty+complete", replay)
	}

	// Empty session id never creates a buffer.
	empty := newMessage(TypeMessageUpdate, nil)
	store.assign("", &empty)
	if empty.Seq != 0 {
		t.Fatalf("empty session id must not stamp a seq, got %d", empty.Seq)
	}
}

// TestSeqReplayBoundaryMath pins the catch-up boundary semantics against a
// tiny ring: frames older than capacity are evicted, the boundary frame says
// so explicitly (never silently), and a client ahead of the counter is safe.
func TestSeqReplayBoundaryMath(t *testing.T) {
	buf := newSessionSeqBuffer(4)
	for i := 0; i < 6; i++ {
		msg := newMessage(TypeMessageUpdate, map[string]any{"n": i})
		buf.assign(0, &msg)
	}
	// Ring holds seqs 3..6; next would be 7.

	// Client saw seq 1: seq 2 was evicted — replay 3..6 plus a boundary.
	replay := buf.replaySince(1)
	if len(replay.frames) != 4 {
		t.Fatalf("replay frames = %d, want 4", len(replay.frames))
	}
	for i, frame := range replay.frames {
		if want := uint64(3 + i); frame.Seq != want {
			t.Fatalf("replay[%d].Seq = %d, want %d", i, frame.Seq, want)
		}
	}
	if replay.complete {
		t.Fatal("replay after eviction must not claim complete")
	}
	if replay.dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (seq 2)", replay.dropped)
	}

	// Client saw seq 2 exactly at the boundary: contiguous, no boundary frame.
	replay = buf.replaySince(2)
	if !replay.complete || replay.dropped != 0 {
		t.Fatalf("boundary-contiguous replay = %+v, want complete", replay)
	}
	if len(replay.frames) != 4 {
		t.Fatalf("replay frames = %d, want 4", len(replay.frames))
	}

	// Client ahead of the counter (server restarted): empty, complete.
	replay = buf.replaySince(100)
	if !replay.complete || len(replay.frames) != 0 {
		t.Fatalf("ahead-of-counter replay = %+v, want empty+complete", replay)
	}

	// Fresh buffer with nothing assigned: nothing to replay, complete.
	replay = newSessionSeqBuffer(4).replaySince(0)
	if !replay.complete || len(replay.frames) != 0 {
		t.Fatalf("fresh buffer replay = %+v, want empty+complete", replay)
	}
}

// TestSeqStoreSessionEviction pins the session map bound: a new session past
// maxTrackedSeqSessions evicts the least-recently-assigned buffer instead of
// growing unboundedly.
func TestSeqStoreSessionEviction(t *testing.T) {
	store := newSeqStore(DefaultSeqBufferCapacity)
	for i := 0; i < maxTrackedSeqSessions; i++ {
		msg := newMessage(TypeMessageUpdate, nil)
		store.assign(fmt.Sprintf("sess-%03d", i), &msg)
	}

	// This must evict the oldest session, not grow the map.
	msg := newMessage(TypeMessageUpdate, nil)
	store.assign("sess-new", &msg)
	if len(store.bufs) > maxTrackedSeqSessions {
		t.Fatalf("session map grew to %d, bound is %d", len(store.bufs), maxTrackedSeqSessions)
	}
	if _, ok := store.bufs["sess-000"]; ok {
		t.Fatal("least-recently-assigned session buffer was not evicted")
	}
	if _, ok := store.bufs["sess-new"]; !ok {
		t.Fatal("newly assigned session buffer missing")
	}
}

// waitForPicoConnCount polls the server-side connection count: Dial
// returns before createAndAddConnection runs, and Close before the server
// notices, so tests must not broadcast on the strength of a returned Dial
// alone (the flake this helper replaced raced exactly that window).
func waitForPicoConnCount(t *testing.T, ch *PicoChannel, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ch.currentConnCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("connection count never reached %d within %v", want, timeout)
}

// TestSeqRingCoalescesConsecutiveUpdates pins the memory strategy: a burst
// of message.update frames for one message collapses into the latest
// snapshot — full-replacement edits make intermediate snapshots useless for
// catch-up — while frames for other messages (or other types) stay separate.
func TestSeqRingCoalescesConsecutiveUpdates(t *testing.T) {
	buf := newSessionSeqBuffer(8)

	// Three consecutive updates of message "m1": only the latest survives.
	for i := 1; i <= 3; i++ {
		msg := newMessage(TypeMessageUpdate, map[string]any{
			"message_id": "m1",
			"content":    fmt.Sprintf("draft-%d", i),
		})
		buf.assign(0, &msg)
	}
	// A create frame and an update for another message do not coalesce.
	create := newMessage(TypeMessageCreate, map[string]any{"message_id": "m2"})
	buf.assign(0, &create)
	otherUpdate := newMessage(TypeMessageUpdate, map[string]any{"message_id": "m2", "content": "x"})
	buf.assign(0, &otherUpdate)

	// Client saw seq 2 (a superseded draft snapshot live): the coalesced
	// frame carries seq 3, so the replay is contiguous and complete.
	replay := buf.replaySince(2)
	if !replay.complete {
		t.Fatal("replay from within the coalesced range must be complete")
	}
	if len(replay.frames) != 3 {
		t.Fatalf("replay frames = %d, want 3 (latest m1 update + m2 create + m2 update)", len(replay.frames))
	}
	if got, _ := replay.frames[0].Payload["content"].(string); got != "draft-3" {
		t.Fatalf("coalesced m1 content = %q, want draft-3", got)
	}
	if replay.frames[0].Seq != 3 {
		t.Fatalf("coalesced m1 seq = %d, want 3 (supersedes 1 and 2)", replay.frames[0].Seq)
	}
	if replay.frames[1].Type != TypeMessageCreate || replay.frames[2].Seq != 5 {
		t.Fatalf("non-coalescable frames were merged: %+v", replay.frames)
	}

	// A cursor below the coalesced range reports the boundary; superseded
	// snapshots count toward dropped (documented over-count — safe, since
	// the client falls back to history and the latest snapshot wins).
	replay = buf.replaySince(0)
	if replay.complete || replay.dropped != 2 {
		t.Fatalf("replay from 0 = %+v, want boundary with dropped=2", replay)
	}
	if len(replay.frames) != 3 {
		t.Fatalf("replay frames = %d, want 3", len(replay.frames))
	}
}

// --- integration: real WS handshake replay ---

// dialTestPicoWSWithLastSeq opens a WS connection with a catch-up cursor.
func dialTestPicoWSWithLastSeq(t *testing.T, serverURL, sessionID string, lastSeq uint64) *websocket.Conn {
	t.Helper()
	wsURL := wsURL(serverURL) + "/pico/ws?session_id=" + sessionID
	if lastSeq > 0 {
		wsURL += "&last_seq=" + strconv.FormatUint(lastSeq, 10)
	}
	header := http.Header{"Authorization": {"Bearer test-token"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Dial(%s, last_seq=%d): %v", sessionID, lastSeq, err)
	}
	_ = resp.Body.Close()
	return conn
}

// broadcastSeqed pushes a seq-stamped frame through the channel's session
// broadcast (the same path every outbound frame takes).
func broadcastSeqed(t *testing.T, ch *PicoChannel, sessionID, content string) PicoMessage {
	t.Helper()
	msg := newMessage(TypeMessageUpdate, map[string]any{"content": content})
	msg.SessionID = sessionID
	if err := ch.broadcastToSession("pico:"+sessionID, msg); err != nil {
		t.Fatalf("broadcastToSession: %v", err)
	}
	return msg
}

// TestCatchupReplaysOnlyMissedFrames pins the core §一 promise: a client
// that reconnects with last_seq receives exactly the frames it missed, in
// order, with their original seqs — not the whole buffer.
func TestCatchupReplaysOnlyMissedFrames(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	// First client goes live and sees seq 1.
	first := dialTestPicoWS(t, server.URL, "sess-catchup")
	waitForPicoConnCount(t, ch, 1, 3*time.Second)
	broadcastSeqed(t, ch, "sess-catchup", "seen-1")
	frame := readTestPicoFrame(t, first) // no probe wired → first inbound broadcast
	if frame.Seq != 1 {
		t.Fatalf("first broadcast seq = %d, want 1", frame.Seq)
	}
	_ = first.Close()
	waitForPicoConnCount(t, ch, 0, 3*time.Second) // disconnect settled server-side

	// While disconnected, three more frames go out (no connections attached,
	// broadcast errors are expected and the frames stay replayable).
	for _, content := range []string{"missed-1", "missed-2", "missed-3"} {
		msg := newMessage(TypeMessageUpdate, map[string]any{"content": content})
		if err := ch.broadcastToSession("pico:sess-catchup", msg); err == nil {
			t.Fatal("broadcast without connections must fail (expected path)")
		}
	}

	// Reconnect with the cursor: exactly the three missed frames.
	second := dialTestPicoWSWithLastSeq(t, server.URL, "sess-catchup", 1)
	defer second.Close()
	for i, want := range []string{"missed-1", "missed-2", "missed-3"} {
		frame := readTestPicoFrame(t, second)
		if frame.Seq != uint64(2+i) {
			t.Fatalf("replay[%d].Seq = %d, want %d", i, frame.Seq, 2+i)
		}
		if got, _ := frame.Payload["content"].(string); got != want {
			t.Fatalf("replay[%d].content = %q, want %q", i, got, want)
		}
	}

	// Live traffic continues with the next seq, no gaps.
	broadcastSeqed(t, ch, "sess-catchup", "live-1")
	frame = readTestPicoFrame(t, second)
	if frame.Seq != 5 {
		t.Fatalf("post-replay seq = %d, want 5", frame.Seq)
	}
}

// TestCatchupOrderedExactlyOnceUnderLiveTraffic pins the interleaving
// contract of the catch-up mode: when live broadcasts race the handshake
// replay, the client must receive every frame exactly once with strictly
// increasing seqs — replayed frames first, then the frames broadcast during
// the replay. The old direct-write path raced this window into duplicates
// and reorders (a live frame jumping ahead of replayed cumulative updates
// left the final render stale).
func TestCatchupOrderedExactlyOnceUnderLiveTraffic(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	// Pre-seed three frames the client has "seen" (fail-broadcast, ring-only).
	for i := 0; i < 3; i++ {
		msg := newMessage(TypeMessageCreate, map[string]any{"n": i})
		_ = ch.broadcastToSession("pico:sess-race", msg)
	}

	const liveFrames = 50
	broadcastDone := make(chan struct{})
	go func() {
		defer close(broadcastDone)
		for i := 3; i < 3+liveFrames; i++ {
			msg := newMessage(TypeMessageCreate, map[string]any{"n": i})
			_ = ch.broadcastToSession("pico:sess-race", msg)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	conn := dialTestPicoWSWithLastSeq(t, server.URL, "sess-race", 3)
	defer conn.Close()

	seqs := make([]uint64, 0, liveFrames)
	for range liveFrames {
		frame := readTestPicoFrame(t, conn)
		if frame.Type != TypeMessageCreate {
			t.Fatalf("frame type = %q, want message.create", frame.Type)
		}
		seqs = append(seqs, frame.Seq)
	}
	<-broadcastDone

	if len(seqs) != liveFrames {
		t.Fatalf("received %d frames, want %d", len(seqs), liveFrames)
	}
	for i, seq := range seqs {
		if want := uint64(4 + i); seq != want {
			t.Fatalf("frame[%d].Seq = %d, want %d (exactly once, in order)", i, seq, want)
		}
	}
}

// TestCatchupBoundaryWhenBufferEvicted pins the explicit boundary: when the
// client's cursor predates the ring's oldest frame, the replay is followed
// by a stream.catchup frame with complete=false — never silence.
func TestCatchupBoundaryWhenBufferEvicted(t *testing.T) {
	ch := newTestPicoChannel(t)
	ch.seqs = newSeqStore(4) // tiny ring to force eviction
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	// Six frames go out with no connections attached (all fail-broadcast,
	// ring keeps the last four: seq 3..6).
	for i := 0; i < 6; i++ {
		msg := newMessage(TypeMessageUpdate, map[string]any{"n": i})
		if err := ch.broadcastToSession("pico:sess-evict", msg); err == nil {
			t.Fatalf("broadcast %d without connections must fail (expected path)", i)
		}
	}

	// Client claims it saw seq 1: seq 2 is gone — boundary must say so.
	conn := dialTestPicoWSWithLastSeq(t, server.URL, "sess-evict", 1)
	defer conn.Close()

	for i := 0; i < 4; i++ {
		frame := readTestPicoFrame(t, conn)
		if want := uint64(3 + i); frame.Seq != want {
			t.Fatalf("replay[%d].Seq = %d, want %d", i, frame.Seq, want)
		}
	}
	boundary := readTestPicoFrame(t, conn)
	if boundary.Type != TypeStreamCatchup {
		t.Fatalf("boundary frame type = %q, want %q", boundary.Type, TypeStreamCatchup)
	}
	if complete, _ := boundary.Payload["complete"].(bool); complete {
		t.Fatal("boundary frame must carry complete=false after eviction")
	}
	if dropped, _ := boundary.Payload["dropped"].(float64); int(dropped) != 1 {
		t.Fatalf("boundary dropped = %v, want 1", boundary.Payload["dropped"])
	}

	// A cursor at the exact boundary produces no catch-up frame at all.
	ch2 := newTestPicoChannel(t)
	ch2.seqs = newSeqStore(4)
	if err := ch2.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch2.Stop(context.Background())
	server2 := httptest.NewServer(http.HandlerFunc(ch2.ServeHTTP))
	defer server2.Close()

	for i := 0; i < 6; i++ {
		msg := newMessage(TypeMessageUpdate, map[string]any{"n": i})
		_ = ch2.broadcastToSession("pico:sess-evict2", msg)
	}
	conn2 := dialTestPicoWSWithLastSeq(t, server2.URL, "sess-evict2", 2)
	defer conn2.Close()
	for i := 0; i < 4; i++ {
		frame := readTestPicoFrame(t, conn2)
		if want := uint64(3 + i); frame.Seq != want {
			t.Fatalf("contiguous replay[%d].Seq = %d, want %d", i, frame.Seq, want)
		}
	}
	// No boundary frame expected: prove the wire is quiet by sending a ping
	// and confirming the pong is the next frame.
	_ = conn2.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := conn2.WriteJSON(PicoMessage{Type: TypePing, ID: "p1"}); err != nil {
		t.Fatalf("WriteJSON ping: %v", err)
	}
	frame := readTestPicoFrame(t, conn2)
	if frame.Type != TypePong {
		t.Fatalf("frame after contiguous replay = %q, want pong (no catch-up boundary)", frame.Type)
	}
}

// TestTurnStatusCoexistsWithCatchup pins the ordering contract: turn.status
// (octop §四) stays the first frame, catch-up replay follows — the two
// reconnect mechanisms stack instead of replacing each other.
func TestTurnStatusCoexistsWithCatchup(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())
	ch.SetTurnActiveProbe(func(chatID string) bool { return true })

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	// Two frames go out before any client connects: they fail-broadcast
	// (no connections) but stay in the ring — the replayable case §一 exists
	// for.
	for i := 0; i < 2; i++ {
		msg := newMessage(TypeMessageUpdate, map[string]any{"n": i})
		if err := ch.broadcastToSession("pico:sess-stack", msg); err == nil {
			t.Fatalf("broadcast %d without connections must fail (expected path)", i)
		}
	}

	conn := dialTestPicoWSWithLastSeq(t, server.URL, "sess-stack", 1)
	defer conn.Close()

	status := readTestPicoFrame(t, conn)
	if status.Type != TypeTurnStatus {
		t.Fatalf("first frame = %q, want %q", status.Type, TypeTurnStatus)
	}
	if status.Seq != 0 {
		t.Fatalf("turn.status must stay seq-less, got seq %d", status.Seq)
	}
	replay := readTestPicoFrame(t, conn)
	if replay.Seq != 2 {
		t.Fatalf("replayed seq = %d, want 2", replay.Seq)
	}
}

// TestSeqlessFramesOmitSeq pins wire compatibility: per-connection frames
// (turn.status, pong) never enter the seq stream — old clients parsing the
// wire see no new fields on them.
func TestSeqlessFramesOmitSeq(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(context.Background())
	ch.SetTurnActiveProbe(func(chatID string) bool { return false })

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	conn := dialTestPicoWS(t, server.URL, "sess-plain")
	defer conn.Close()

	status := readTestPicoFrame(t, conn)
	if status.Type != TypeTurnStatus || status.Seq != 0 {
		t.Fatalf("turn.status = %+v, want seq-less turn.status", status)
	}

	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if err := conn.WriteJSON(PicoMessage{Type: TypePing, ID: "p1"}); err != nil {
		t.Fatalf("WriteJSON ping: %v", err)
	}
	pong := readTestPicoFrame(t, conn)
	if pong.Type != TypePong || pong.Seq != 0 {
		t.Fatalf("pong = %+v, want seq-less pong", pong)
	}

	// Session broadcasts meanwhile do carry seqs, with stable JSON shape.
	broadcastSeqed(t, ch, "sess-plain", "hello")
	frame := readTestPicoFrame(t, conn)
	if frame.Seq != 1 {
		t.Fatalf("broadcast seq = %d, want 1", frame.Seq)
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := generic["seq"]; !ok {
		t.Fatal("session broadcast wire format must expose seq")
	}
}

// TestClientDialCarriesLastSeq pins the client side of §一: the pico_client
// channel presents its highest seen seq as ?last_seq= on dial, so reconnects
// trigger server-side replay automatically.
func TestClientDialCarriesLastSeq(t *testing.T) {
	type dialInfo struct {
		lastSeq string
	}
	dials := make(chan dialInfo, 4)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials <- dialInfo{lastSeq: r.URL.Query().Get("last_seq")}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Hold the socket open until the client goes away.
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	bc := &config.Channel{Type: config.ChannelPicoClient, Enabled: true}
	ch, err := NewPicoClientChannel(bc, &config.PicoClientSettings{
		URL:          wsURL(server.URL),
		Token:        *config.NewSecureString("test-token"),
		PingInterval: 60,
		ReadTimeout:  10,
	}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate having processed seq 42 earlier (unit shortcut: the counter
	// is what handleInbound updates on every inbound frame).
	ch.lastSeq.Store(42)

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ch.Stop(ctx)

	select {
	case info := <-dials:
		if info.lastSeq != "42" {
			t.Fatalf("dial last_seq = %q, want 42", info.lastSeq)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the dial")
	}
}
