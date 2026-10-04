package channels

// Regression test for B2-2 (docs/design/2026-10-04-feishu-streaming-card-
// bug-review.zh.md §3.4): GetStreamer must pass the turn's sessionKey to
// channels that implement SessionScopedBeginStreamer — both for the initial
// streamer and for splitMarkerStreamer's re-begin closure.

import (
	"context"
	"testing"
)

// sessionScopedMockChannel implements StreamingCapable +
// SessionScopedBeginStreamer, recording the session keys it was begun with.
type sessionScopedMockChannel struct {
	mockStreamingChannel
	begunSessions []string
}

func (c *sessionScopedMockChannel) BeginStream(ctx context.Context, chatID string) (Streamer, error) {
	c.begunSessions = append(c.begunSessions, "(unscoped)")
	return &mockStreamer{}, nil
}

func (c *sessionScopedMockChannel) BeginStreamForSession(ctx context.Context, chatID, sessionKey string) (Streamer, error) {
	c.begunSessions = append(c.begunSessions, sessionKey)
	return &mockStreamer{}, nil
}

func TestGetStreamerPassesSessionScope(t *testing.T) {
	m := newTestManager()
	ch := &sessionScopedMockChannel{mockStreamingChannel: mockStreamingChannel{mockMessageEditor: mockMessageEditor{}}}
	m.channels["scoped"] = ch

	if _, ok := m.GetStreamer(context.Background(), "scoped", "chat-1", "sess-A"); !ok {
		t.Fatal("expected streamer to be available")
	}
	if len(ch.begunSessions) != 1 || ch.begunSessions[0] != "sess-A" {
		t.Fatalf("first begin must carry the session key, got %v", ch.begunSessions)
	}

	// A second turn in the same chat with a different session must get its
	// own begin carrying its own key (B2a: no surface sharing).
	if _, ok := m.GetStreamer(context.Background(), "scoped", "chat-1", "sess-B"); !ok {
		t.Fatal("expected streamer to be available")
	}
	if len(ch.begunSessions) != 2 || ch.begunSessions[1] != "sess-B" {
		t.Fatalf("second begin must carry its session key, got %v", ch.begunSessions)
	}
}

// legacyMockChannelOnlyBegin pins the fallback: channels that only implement
// StreamingCapable still work through the unscoped path.
type legacyMockChannelOnlyBegin struct {
	mockStreamingChannel
	begunUnscoped int
}

func (c *legacyMockChannelOnlyBegin) BeginStream(ctx context.Context, chatID string) (Streamer, error) {
	c.begunUnscoped++
	return &mockStreamer{}, nil
}

func TestGetStreamerFallsBackToUnscopedBegin(t *testing.T) {
	m := newTestManager()
	ch := &legacyMockChannelOnlyBegin{mockStreamingChannel: mockStreamingChannel{mockMessageEditor: mockMessageEditor{}}}
	m.channels["legacy"] = ch

	if _, ok := m.GetStreamer(context.Background(), "legacy", "chat-1", "sess-A"); !ok {
		t.Fatal("expected streamer to be available")
	}
	if ch.begunUnscoped != 1 {
		t.Fatalf("legacy channel must go through BeginStream, got %d calls", ch.begunUnscoped)
	}
}
