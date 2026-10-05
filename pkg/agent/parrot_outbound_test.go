package agent

import (
	"context"
	"testing"
	"time"
)

// TestSealParrotOnlyAnswerShowsInterceptionNote (fork, 2026-10-05): when the
// display filter strips the ENTIRE final answer, the sealed card must explain
// the interception instead of showing no body at all — the raw parrot payload
// stays out of the chat either way.
func TestSealParrotOnlyAnswerShowsInterceptionNote(t *testing.T) {
	recorder := &recordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: recorder}

	parrot := `[tool_use: message, args: {"content":"师兄，总结"}]`
	if err := publisher.Finalize(context.Background(), parrot, nil); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if len(recorder.finalized) != 1 {
		t.Fatalf("finalized = %v, want one entry", recorder.finalized)
	}
	if recorder.finalized[0] != parrotOnlyAnswerNote {
		t.Fatalf("sealed content = %q, want interception note", recorder.finalized[0])
	}
}

// TestSealMixedParrotContentKeepsProse: a partially-parroted answer keeps its
// prose and gets no interception note.
func TestSealMixedParrotContentKeepsProse(t *testing.T) {
	recorder := &recordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: recorder}

	mixed := `[tool_use: exec, args: {"command":"ls"}]` + "\n\n正经结论。"
	if err := publisher.Finalize(context.Background(), mixed, nil); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if len(recorder.finalized) != 1 || recorder.finalized[0] != "正经结论。" {
		t.Fatalf("sealed content = %q, want 正经结论。", recorder.finalized)
	}
}

// TestSealEmptyContentStaysEmpty: a genuinely empty final (nothing to show,
// nothing filtered) must not grow an interception note.
func TestSealEmptyContentStaysEmpty(t *testing.T) {
	recorder := &recordingStreamer{}
	publisher := &streamingChunkPublisher{streamer: recorder}
	publisher.Update(context.Background(), "hello")

	if err := publisher.Finalize(context.Background(), "", nil); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if len(recorder.finalized) != 1 || recorder.finalized[0] != "" {
		t.Fatalf("sealed content = %q, want empty", recorder.finalized)
	}
}

// TestPublishResponseIfNeededDropsParrotOnlyResponse: the plain outbound path
// (defense in depth behind the card suppression marker) must not ship a
// response that is entirely tool-call parrot replay.
func TestPublishResponseIfNeededDropsParrotOnlyResponse(t *testing.T) {
	al, _, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()
	_ = provider

	al.PublishResponseIfNeeded(context.Background(), "pico", "pico:session-1", "session-1",
		`[tool_use: message, args: {"content":"师兄"}]`)

	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("expected no outbound for parrot-only response, got %q", outbound.Content)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestPublishResponseIfNeededStripsParrotLinesFromResponse: mixed responses
// keep their prose, dropping only the parrot lines.
func TestPublishResponseIfNeededStripsParrotLinesFromResponse(t *testing.T) {
	al, _, msgBus, provider, cleanup := newTestAgentLoop(t)
	defer cleanup()
	_ = provider

	al.PublishResponseIfNeeded(context.Background(), "pico", "pico:session-1", "session-1",
		`[tool_use: exec, args: {"command":"ls"}]`+"\n\n正经回复。")

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "正经回复。" {
			t.Fatalf("outbound content = %q, want 正经回复。", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("expected outbound with prose only")
	}
}
