package pico

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRevertBroadcastsSeqedFrame pins the §2.1 fan-out contract: the
// revert.status frame is a session-level broadcast — it carries a seq and
// enters the replay ring, so a client that refreshes after the revert still
// learns the workspace changed (replayed by its last_seq catch-up).
func TestRevertBroadcastsSeqedFrame(t *testing.T) {
	ch := newTestPicoChannel(t)
	if err := ch.Start(ch.ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer ch.Stop(ch.ctx)

	server := httptest.NewServer(http.HandlerFunc(ch.ServeHTTP))
	defer server.Close()

	conn := dialTestPicoWS(t, server.URL, "sess-revert")
	defer conn.Close()
	waitForPicoConnCount(t, ch, 1, 3*time.Second)

	err := ch.BroadcastRevert("pico:sess-revert", map[string]any{
		"version":  "v001-abc",
		"restored": 2,
		"deleted":  1,
		"skipped":  0,
	})
	if err != nil {
		t.Fatalf("BroadcastRevert: %v", err)
	}

	frame := readTestPicoFrame(t, conn)
	if frame.Type != TypeRevertStatus {
		t.Fatalf("frame type = %q, want %q", frame.Type, TypeRevertStatus)
	}
	if frame.Seq != 1 {
		t.Fatalf("frame seq = %d, want 1 (session-level frames are seqed)", frame.Seq)
	}
	if frame.SessionID != "sess-revert" {
		t.Fatalf("frame session = %q, want sess-revert", frame.SessionID)
	}
	if got, _ := frame.Payload["version"].(string); got != "v001-abc" {
		t.Fatalf("payload version = %q, want v001-abc", got)
	}

	// A follow-up revert continues the same session seq stream.
	if err := ch.BroadcastRevert("pico:sess-revert", map[string]any{"version": "v002-def"}); err != nil {
		t.Fatalf("BroadcastRevert 2: %v", err)
	}
	frame = readTestPicoFrame(t, conn)
	if frame.Seq != 2 {
		t.Fatalf("second frame seq = %d, want 2", frame.Seq)
	}
}
