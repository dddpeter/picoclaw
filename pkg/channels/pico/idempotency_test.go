package pico

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// setupIdempotencyServer builds a started channel behind a real HTTP/WS
// server and returns it with one connected client and the observable bus.
func setupIdempotencyServer(t *testing.T, sessionID string) (*PicoChannel, *websocket.Conn, *bus.MessageBus) {
	t.Helper()

	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	t.Cleanup(server.Close)

	conn := dialTestPicoWS(t, server.URL, sessionID)
	t.Cleanup(func() { _ = conn.Close() })
	return ch, conn, msgBus
}

// TestMessageSendDuplicateDoesNotStartTurn pins §2.2: re-sending the same
// message ID must not dispatch a second turn — the bus sees exactly one
// inbound message, while a fresh ID still delivers normally.
func TestMessageSendDuplicateDoesNotStartTurn(t *testing.T) {
	_, conn, msgBus := setupIdempotencyServer(t, "sess-dup")

	sendMsg := PicoMessage{
		Type:      TypeMessageSend,
		ID:        "req-dup-1",
		SessionID: "sess-dup",
		Payload:   map[string]any{PayloadKeyContent: "hello"},
	}
	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("first WriteJSON: %v", err)
	}
	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Content != "hello" {
			t.Fatalf("first content = %q, want hello", inbound.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first send never reached the bus")
	}

	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("duplicate WriteJSON: %v", err)
	}
	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("duplicate dispatched a second delivery: content %q", inbound.Content)
	case <-time.After(300 * time.Millisecond):
	}

	sendMsg.ID = "req-dup-2"
	sendMsg.Payload = map[string]any{PayloadKeyContent: "again"}
	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("fresh WriteJSON: %v", err)
	}
	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Content != "again" {
			t.Fatalf("fresh content = %q, want again", inbound.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh send never reached the bus")
	}
}

// TestMessageSendAckCarriesRequestID pins the duplicate reply contract: a
// message.ack frame echoing the original request ID, marked duplicate, and
// seq-less (per-connection reply, never replayed by catch-up).
func TestMessageSendAckCarriesRequestID(t *testing.T) {
	_, conn, _ := setupIdempotencyServer(t, "sess-ack")

	sendMsg := PicoMessage{
		Type:      TypeMessageSend,
		ID:        "req-ack-1",
		SessionID: "sess-ack",
		Payload:   map[string]any{PayloadKeyContent: "hello"},
	}
	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("first WriteJSON: %v", err)
	}
	if err := conn.WriteJSON(sendMsg); err != nil {
		t.Fatalf("duplicate WriteJSON: %v", err)
	}

	ack := readTestPicoFrame(t, conn)
	if ack.Type != TypeMessageAck {
		t.Fatalf("duplicate reply type = %q, want %q", ack.Type, TypeMessageAck)
	}
	if ack.ID != "req-ack-1" {
		t.Fatalf("ack.ID = %q, want req-ack-1", ack.ID)
	}
	if got, _ := ack.Payload["request_id"].(string); got != "req-ack-1" {
		t.Fatalf("ack request_id = %q, want req-ack-1", got)
	}
	if dup, _ := ack.Payload["duplicate"].(bool); !dup {
		t.Fatalf("ack duplicate = %v, want true", dup)
	}
	if ack.Seq != 0 {
		t.Fatalf("ack seq = %d, want 0 (seq-less per-connection reply)", ack.Seq)
	}
}

// TestIdempotencyDoesNotBreakSeqlessClients: clients that never set an ID
// keep the legacy behavior — every send delivers, nothing is deduplicated.
// Uses a bare picoConn: the no-ID path never writes frames back.
func TestIdempotencyDoesNotBreakSeqlessClients(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}
	ch.ctx = context.Background()

	pc := &picoConn{id: "conn-1", sessionID: "sess-less"}
	for i := 0; i < 2; i++ {
		ch.handleMessageSend(pc, PicoMessage{
			Type:      TypeMessageSend,
			SessionID: "sess-less",
			Payload:   map[string]any{PayloadKeyContent: "hello"},
		})
	}
	for i := 0; i < 2; i++ {
		select {
		case <-msgBus.InboundChan():
		case <-time.After(time.Second):
			t.Fatalf("seq-less send #%d never reached the bus", i+1)
		}
	}
	select {
	case got := <-msgBus.InboundChan():
		t.Fatalf("unexpected extra delivery: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestMessageSendDeliveryFailureRollsBackIdempotency pins the rollback
// wiring: when delivery fails after the ID was recorded, the record is
// removed so a client retry with the same ID can still be delivered.
// The inbound publish policy has no drop timeout (blocks until ctx
// cancellation), so a pre-filled bus plus a cancelled channel context makes
// the delivery error deterministic.
func TestMessageSendDeliveryFailureRollsBackIdempotency(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch.ctx = ctx

	// Fill the inbound buffer so the next publish cannot succeed: with the
	// channel full and the context cancelled, publish deterministically
	// fails instead of delivering.
	capacity := msgBus.Stats().Inbound.Capacity
	if capacity <= 0 {
		t.Fatalf("inbound bus capacity = %d, want > 0", capacity)
	}
	dummy := bus.InboundMessage{Context: bus.InboundContext{Channel: "pico", ChatID: "pico:filler"}}
	for i := 0; i < capacity; i++ {
		if err := msgBus.PublishInbound(ctx, dummy); err != nil {
			t.Fatalf("filler publish #%d failed: %v", i, err)
		}
	}
	cancel()

	pc := &picoConn{id: "conn-1", sessionID: "sess-rb"}
	ch.handleMessageSend(pc, PicoMessage{
		Type:      TypeMessageSend,
		ID:        "req-rb-1",
		SessionID: "sess-rb",
		Payload:   map[string]any{PayloadKeyContent: "doomed"},
	})

	if ch.seenIDs.Seen("sess-rb", "req-rb-1") {
		t.Fatal("ID still recorded after delivery failure — rollback did not happen")
	}
}

// TestIdempotencyWindowBounded pins the eviction contract: a window at
// capacity forgets its oldest ID (which becomes deliverable again — the
// documented downgrade) while recent IDs stay deduplicated.
func TestIdempotencyWindowBounded(t *testing.T) {
	const capacity = 4
	w := newSessionDedupWindow(capacity)
	// Fill one past capacity: the overflow insert evicts the oldest (m-0).
	for i := 0; i < capacity+1; i++ {
		if w.seen(uint64(i+1), fmt.Sprintf("m-%d", i)) {
			t.Fatalf("m-%d reported duplicate on first sight", i)
		}
	}

	// Retained IDs stay deduplicated (duplicates never refresh FIFO order).
	for i := 1; i <= capacity; i++ {
		if !w.seen(uint64(100+i), fmt.Sprintf("m-%d", i)) {
			t.Fatalf("m-%d lost from the window too early", i)
		}
	}

	// The evicted oldest is deliverable again — the documented downgrade.
	// seen() re-records on a miss (evicting m-1), so this assertion runs last.
	if w.seen(200, "m-0") {
		t.Fatal("m-0 still deduplicated after being evicted")
	}
}

// TestIdempotencyUnseenRollsBack pins the store-level rollback contract.
func TestIdempotencyUnseenRollsBack(t *testing.T) {
	s := newIdempotencyStore(0)
	const session = "sess-rb"
	if s.Seen(session, "m-1") {
		t.Fatal("first Seen must be false")
	}
	s.Unseen(session, "m-1")
	if s.Seen(session, "m-1") {
		t.Fatal("after Unseen the same ID must be deliverable again")
	}
	s.Unseen(session, "m-unknown") // must be a no-op
}

// TestIdempotencyConcurrentSameIDDeliversOnce: check-and-record is atomic,
// so racing connections with the same ID get exactly one acceptance.
func TestIdempotencyConcurrentSameIDDeliversOnce(t *testing.T) {
	s := newIdempotencyStore(0)
	const racers = 16
	var dupCount int64
	var wg sync.WaitGroup
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if s.Seen("sess-race", "m-race") {
				atomic.AddInt64(&dupCount, 1)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt64(&dupCount); got != racers-1 {
		t.Fatalf("duplicate count = %d, want %d", got, racers-1)
	}
}

// TestIdempotencyStoreSessionLRU pins the session-map bound: the
// least-recently-seen window is evicted, not the first-inserted one.
func TestIdempotencyStoreSessionLRU(t *testing.T) {
	s := newIdempotencyStore(0)
	for i := 0; i < maxTrackedIdempotencySessions; i++ {
		s.Seen(fmt.Sprintf("sess-%d", i), "m-1")
	}
	// Touch sess-0 so it becomes the most-recently-seen window.
	if s.Seen("sess-0", "m-2") {
		t.Fatal("sess-0 m-2 must be fresh on first sight")
	}
	s.Seen("sess-new", "m-1") // pushes the map one over the bound

	if _, ok := s.windows["sess-0"]; !ok {
		t.Fatal("recently-seen sess-0 was evicted instead of the LRU window")
	}
	if _, ok := s.windows["sess-1"]; ok {
		t.Fatal("least-recently-seen sess-1 should have been evicted")
	}
	if _, ok := s.windows["sess-new"]; !ok {
		t.Fatal("sess-new missing after insertion")
	}
}
