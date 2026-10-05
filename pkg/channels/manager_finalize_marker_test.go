package channels

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// TestGetStreamer_EmptyFinalizeStillArmsSuppressionMarker pins the 2026-10-05
// feishu incident: display-side filters (tool-call parrot stripping) can empty
// the final answer, and the finalize hook used to infer "this is a Cancel"
// from empty content — leaving the suppression marker unarmed so the raw
// response escaped as a duplicate plain message next to the sealed card.
func TestGetStreamer_EmptyFinalizeStillArmsSuppressionMarker(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer:          &mockStreamer{},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamer(context.Background(), "test", "123", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	if err := streamer.Finalize(context.Background(), ""); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if _, ok := m.streamActive.Load("test:123"); !ok {
		t.Fatal("expected streamActive marker after finalize with empty content")
	}

	// The incident's failing step: the raw final outbound must be suppressed
	// instead of shipping the payload dump as a second message.
	finalMsg := testOutboundMessage(bus.OutboundMessage{
		Channel: "test",
		ChatID:  "123",
		Content: `[tool_use: message, args: {"content":"..."}]`,
		Context: bus.InboundContext{
			Channel: "test",
			ChatID:  "123",
			Raw:     map[string]string{"outbound_kind": "final"},
		},
	})
	if _, handled := m.preSend(context.Background(), "test", finalMsg, ch); !handled {
		t.Fatal("expected final outbound to be suppressed after empty-content finalize")
	}
}

// TestGetStreamer_CancelDoesNotArmSuppressionMarker: cancellation must keep
// the normal outbound path open so the fallback (non-streamed) response still
// reaches the user.
func TestGetStreamer_CancelDoesNotArmSuppressionMarker(t *testing.T) {
	m := newTestManager()
	ch := &mockStreamingChannel{
		mockMessageEditor: mockMessageEditor{},
		streamer:          &mockStreamer{},
	}
	m.channels["test"] = ch

	streamer, ok := m.GetStreamer(context.Background(), "test", "123", "")
	if !ok {
		t.Fatal("expected streamer to be available")
	}
	streamer.Cancel(context.Background())
	if _, armed := m.streamActive.Load("test:123"); armed {
		t.Fatal("expected no streamActive marker after cancel")
	}
}

// TestFinalizeHookStreamerCancelFlag: cancellation is reported as an explicit
// flag, never inferred from empty finalize content.
func TestFinalizeHookStreamerCancelFlag(t *testing.T) {
	var flags []bool
	wrapper := &finalizeHookStreamer{
		Streamer: &mockStreamer{},
		onFinalize: func(_ context.Context, _ string, cancelled bool) {
			flags = append(flags, cancelled)
		},
	}
	if err := wrapper.Finalize(context.Background(), ""); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if err := wrapper.FinalizeWithContext(context.Background(), "", nil); err != nil {
		t.Fatalf("FinalizeWithContext() error = %v", err)
	}
	wrapper.CancelWithReason(context.Background(), "stop_command")
	if len(flags) != 3 || flags[0] || flags[1] || !flags[2] {
		t.Fatalf("cancelled flags = %v, want [false false true]", flags)
	}
}
