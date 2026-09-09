package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// TestMaybePublishErrorSkipsAlreadyNotified pins the double-notification
// dedupe: when runAgentLoop already surfaced the failure via publishTurnError,
// the legacy error path must not send a second "Error processing message"
// for the same turn.
func TestMaybePublishErrorSkipsAlreadyNotified(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, false, nil)
	msgBus := newMessageBusForDedupe()
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	inner := errors.New("LLM call failed after retries: all candidates unavailable")
	if !al.maybePublishError(context.Background(), "pico", "chat1", "sess1", &turnErrorNotifiedError{err: inner}) {
		t.Fatal("already-notified error must report continue (true), like other non-cancel errors")
	}

	select {
	case msg := <-msgBus.OutboundChan():
		t.Fatalf("already-notified error must not be published again, got %q", msg.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestMaybePublishErrorStillPublishesUnknownErrors: errors that did not go
// through publishTurnError keep the legacy behavior.
func TestMaybePublishErrorStillPublishesUnknownErrors(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, false, nil)
	msgBus := newMessageBusForDedupe()
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)

	if !al.maybePublishError(context.Background(), "pico", "chat1", "sess1", errors.New("boom")) {
		t.Fatal("plain error must report continue (true)")
	}

	select {
	case msg := <-msgBus.OutboundChan():
		if !strings.Contains(msg.Content, "Error processing message: boom") {
			t.Fatalf("plain error must publish the legacy text, got %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected the legacy error message")
	}
}

// TestTurnErrorNotifiedErrorUnwraps: the marker must stay transparent to
// errors.Is/As so upstream classification and logging keep working.
func TestTurnErrorNotifiedErrorUnwraps(t *testing.T) {
	sentinel := errors.New("sentinel failure")
	wrapped := &turnErrorNotifiedError{err: sentinel}

	if !errors.Is(wrapped, sentinel) {
		t.Fatal("errors.Is must see through the marker")
	}
	if wrapped.Error() != sentinel.Error() {
		t.Fatalf("Error() = %q, want the wrapped text %q", wrapped.Error(), sentinel.Error())
	}
}

// TestPublishTurnErrorReportsWhetherNotified pins the return contract the
// dedupe relies on: true after a real publish, false for internal channels.
func TestPublishTurnErrorReportsWhetherNotified(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, false, nil)
	msgBus := newMessageBusForDedupe()
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	ts, _ := newDegradedTurnExecution(t, al, provider)

	if !al.publishTurnError(context.Background(), ts, errors.New("boom")) {
		t.Fatal("publish on a user channel must report true")
	}
	// Drain the published message so the bus is clean for the second check.
	select {
	case <-msgBus.OutboundChan():
	case <-time.After(2 * time.Second):
		t.Fatal("expected the turn-error message")
	}

	ts.channel = "system"
	if al.publishTurnError(context.Background(), ts, errors.New("boom")) {
		t.Fatal("internal channel must report false so the legacy path can decide")
	}
}

// newMessageBusForDedupe keeps the dedupe tests independent of the streaming
// delegate setup used elsewhere.
func newMessageBusForDedupe() *bus.MessageBus {
	return bus.NewMessageBus()
}
