//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

package feishu

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// cancelAPIFake extends the degrade fake with a scriptable deleteMessage.
type cancelAPIFake struct {
	degradeAPIFake
	deleteCalls   int
	lastDeletedID string
	deleteErr     error
}

func (f *cancelAPIFake) deleteMessage(_ context.Context, _, messageID string) error {
	f.deleteCalls++
	f.lastDeletedID = messageID
	return f.deleteErr
}

func newCancelTestStreamer(t *testing.T, fake *cancelAPIFake) *feishuCardStreamer {
	t.Helper()
	s := newDegradeTestStreamer(t, &fake.degradeAPIFake)
	s.deleteMessage = fake.deleteMessage
	s.msgID = "msg-1"
	return s
}

// TestCancelDeletesEmptyCard: cancelling a card that never showed content (no
// answer, no reasoning/tool panel) deletes its message instead of sealing an
// "⚠ 已中断" marker — transient pre-output failures (e.g. upstream 429) must
// not litter the chat with interrupt cards.
func TestCancelDeletesEmptyCard(t *testing.T) {
	fake := &cancelAPIFake{}
	s := newCancelTestStreamer(t, fake)

	s.CancelWithReason(context.Background(), "stream_error")

	if fake.deleteCalls != 1 || fake.lastDeletedID != "msg-1" {
		t.Fatalf("delete calls = %d (id %q), want 1 (msg-1)", fake.deleteCalls, fake.lastDeletedID)
	}
	if fake.updateCardCalls != 0 {
		t.Fatalf("seal calls = %d, want 0 (empty card must not be sealed)", fake.updateCardCalls)
	}
}

// TestCancelSealsCardWithContent: a card that streamed visible content keeps
// the interrupt seal so the user knows why the reply stopped mid-sentence.
func TestCancelSealsCardWithContent(t *testing.T) {
	fake := &cancelAPIFake{}
	s := newCancelTestStreamer(t, fake)

	s.mu.Lock()
	s.answer = "回答到一半"
	s.mu.Unlock()

	s.CancelWithReason(context.Background(), "stream_error")

	if fake.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0 (card with content must be sealed, not deleted)", fake.deleteCalls)
	}
	if fake.updateCardCalls != 1 {
		t.Fatalf("seal calls = %d, want 1", fake.updateCardCalls)
	}
}

// TestCancelSealsWhenDeleteFails: if the delete API rejects the call, the
// empty card falls back to the normal seal instead of disappearing silently.
func TestCancelSealsWhenDeleteFails(t *testing.T) {
	fake := &cancelAPIFake{deleteErr: errors.New("delete rejected")}
	s := newCancelTestStreamer(t, fake)

	s.CancelWithReason(context.Background(), "stream_error")

	if fake.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", fake.deleteCalls)
	}
	if fake.updateCardCalls != 1 {
		t.Fatalf("seal calls = %d, want 1 (seal is the fallback when delete fails)", fake.updateCardCalls)
	}
}

// TestCancelSealsCardWithPanel: a card with reasoning/tool panel content (but
// no answer text) still seals — the user watched activity, deletion would
// erase evidence of what happened.
func TestCancelSealsCardWithPanel(t *testing.T) {
	fake := &cancelAPIFake{}
	s := newCancelTestStreamer(t, fake)

	s.mu.Lock()
	s.state.Tools = append(s.state.Tools, bus.ToolStep{Tool: "web_search", Result: "3 results"})
	s.mu.Unlock()

	s.CancelWithReason(context.Background(), "stream_error")

	if fake.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0 (panel content must be kept)", fake.deleteCalls)
	}
	if fake.updateCardCalls != 1 {
		t.Fatalf("seal calls = %d, want 1", fake.updateCardCalls)
	}
}
