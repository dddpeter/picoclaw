package feishu

import (
	"context"
	"testing"
	"time"
)

// TestNotifySteeringInChatRecordsWithoutBlocking covers the freeze fix:
// NotifySteeringInChat runs on the agent's inbound loop, so it must record
// the notice synchronously (fast, no API call) and leave the panel refresh
// to the background goroutine. The throttle window keeps that goroutine from
// touching the (nil in tests) API client.
func TestNotifySteeringInChatRecordsWithoutBlocking(t *testing.T) {
	ch := &FeishuChannel{}
	s := &feishuCardStreamer{ch: ch, chatID: "chat-1", lastAt: time.Now()}
	// Keep the async refresh inside the throttle window so it no-ops instead
	// of reaching the card API (unavailable in this test).
	s.panelSentAt = time.Now()
	s.phase = feishuPhaseThinking
	s.renderedPhase = feishuPhaseThinking
	ch.streams.Store("chat-1", s)

	start := time.Now()
	if !ch.NotifySteeringInChat(context.Background(), "chat-1", "你好") {
		t.Fatal("expected steering notice to be accepted")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("NotifySteeringInChat blocked for %v; inbound loop must not wait on card work", elapsed)
	}

	s.mu.Lock()
	gotCount, gotLast := s.state.SteeringCount, s.state.SteeringLast
	s.mu.Unlock()
	if gotCount != 1 || gotLast != "你好" {
		t.Fatalf("steering notice not recorded: count=%d last=%q", gotCount, gotLast)
	}
	if !s.panelDirty {
		t.Error("panel dirty flag should be set for the async refresh")
	}
}

func TestNotifySteeringInChatNoActiveCard(t *testing.T) {
	ch := &FeishuChannel{}
	if ch.NotifySteeringInChat(context.Background(), "chat-none", "hi") {
		t.Fatal("expected false when no streaming card is active")
	}
}

func TestNotifySteeringInChatDoneCard(t *testing.T) {
	ch := &FeishuChannel{}
	s := &feishuCardStreamer{ch: ch, chatID: "chat-2", done: true}
	ch.streams.Store("chat-2", s)
	if ch.NotifySteeringInChat(context.Background(), "chat-2", "hi") {
		t.Fatal("expected false when the card is already sealed")
	}
}
