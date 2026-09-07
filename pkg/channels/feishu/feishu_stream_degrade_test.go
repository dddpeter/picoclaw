package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// degradeAPIFake records CardKit calls and lets each be scripted to fail.
type degradeAPIFake struct {
	streamContentCalls int
	updateCardCalls    int
	reopenCalls        int
	closeCalls         int

	streamErrs []error // per-call errors for streamContent; nil = success
	reopenErr  error
	lastCard   map[string]any
}

func (f *degradeAPIFake) streamContent(_ context.Context, _, _, _ string, _ int) error {
	f.streamContentCalls++
	if f.streamContentCalls <= len(f.streamErrs) {
		return f.streamErrs[f.streamContentCalls-1]
	}
	return nil
}

func (f *degradeAPIFake) updateCard(_ context.Context, _ string, card map[string]any, _ int) error {
	f.updateCardCalls++
	f.lastCard = card
	return nil
}

func (f *degradeAPIFake) reopenStreaming(context.Context, string, int) error {
	f.reopenCalls++
	return f.reopenErr
}

func (f *degradeAPIFake) closeStreaming(context.Context, string, map[string]any, int) error {
	f.closeCalls++
	return nil
}

func newDegradeTestStreamer(t *testing.T, fake *degradeAPIFake) *feishuCardStreamer {
	t.Helper()
	ch, _ := newCardActionTestChannel()
	s := newFeishuCardStreamer(ch, "chat-degrade", "card-degrade", "")
	s.streamContent = fake.streamContent
	s.updateCard = fake.updateCard
	s.reopenStreaming = fake.reopenStreaming
	s.closeStreaming = fake.closeStreaming
	return s
}

// TestUpdateRecoversFromStreamingTimeout: a 200850 rejection reopens
// streaming mode once and retries the element write; the LLM call must not
// see an error and the streamer must not degrade.
func TestUpdateRecoversFromStreamingTimeout(t *testing.T) {
	fake := &degradeAPIFake{streamErrs: []error{errFeishuStreamingEnded}}
	s := newDegradeTestStreamer(t, fake)

	if err := s.Update(context.Background(), "答案片段"); err != nil {
		t.Fatalf("Update must swallow a streaming-end rejection, got %v", err)
	}
	if fake.reopenCalls != 1 {
		t.Errorf("reopen calls = %d, want 1", fake.reopenCalls)
	}
	if fake.streamContentCalls != 2 {
		t.Errorf("stream content calls = %d, want 2 (failed write + retry)", fake.streamContentCalls)
	}
	s.mu.Lock()
	lost := s.streamingLost
	s.mu.Unlock()
	if lost {
		t.Error("streamer must not degrade when reopen succeeds")
	}
	if fake.updateCardCalls != 0 {
		t.Errorf("no full-card refresh expected on recovery, got %d", fake.updateCardCalls)
	}
}

// TestUpdateDegradesWhenReopenFails: if reopening streaming mode fails, the
// streamer degrades to full-card updates carrying the answer so far — still
// without failing the LLM call.
func TestUpdateDegradesWhenReopenFails(t *testing.T) {
	fake := &degradeAPIFake{
		streamErrs: []error{errFeishuStreamingEnded},
		reopenErr:  errors.New("reopen rejected"),
	}
	s := newDegradeTestStreamer(t, fake)

	if err := s.Update(context.Background(), "答案片段"); err != nil {
		t.Fatalf("Update must not fail the LLM call on degrade, got %v", err)
	}
	s.mu.Lock()
	lost := s.streamingLost
	s.mu.Unlock()
	if !lost {
		t.Fatal("streamer should be degraded after failed reopen")
	}
	if fake.updateCardCalls != 1 {
		t.Fatalf("degraded flush calls = %d, want 1", fake.updateCardCalls)
	}
	data, _ := json.Marshal(fake.lastCard)
	if !strings.Contains(string(data), "答案片段") {
		t.Errorf("degraded refresh should carry the answer, got: %.300s", string(data))
	}
	cfg, _ := fake.lastCard["config"].(map[string]any)
	if cfg["streaming_mode"] != nil || cfg["update_multi"] != true {
		t.Errorf("degraded refresh config should drop streaming and allow updates, got %#v", cfg)
	}
}

// TestDegradedUpdateSkipsElementWrites: once degraded, answer updates go
// through full-card refreshes only — no more doomed element writes.
func TestDegradedUpdateSkipsElementWrites(t *testing.T) {
	fake := &degradeAPIFake{}
	s := newDegradeTestStreamer(t, fake)
	s.mu.Lock()
	s.streamingLost = true
	s.mu.Unlock()

	if err := s.Update(context.Background(), "降级后的答案"); err != nil {
		t.Fatalf("degraded Update returned error: %v", err)
	}
	if fake.streamContentCalls != 0 {
		t.Errorf("degraded Update must not write elements, got %d calls", fake.streamContentCalls)
	}
	if fake.updateCardCalls != 1 {
		t.Fatalf("degraded Update should flush one full-card refresh, got %d", fake.updateCardCalls)
	}
	data, _ := json.Marshal(fake.lastCard)
	if !strings.Contains(string(data), "降级后的答案") {
		t.Errorf("full-card refresh should carry the answer, got: %.300s", string(data))
	}
}

// TestFinalizeSkipsCloseWhenDegraded: the settings close call is redundant
// when the server already ended streaming mode.
func TestFinalizeSkipsCloseWhenDegraded(t *testing.T) {
	fake := &degradeAPIFake{}
	s := newDegradeTestStreamer(t, fake)
	s.mu.Lock()
	s.streamingLost = true
	s.mu.Unlock()

	if err := s.FinalizeWithContext(context.Background(), "最终答案", nil); err != nil {
		t.Fatalf("FinalizeWithContext: %v", err)
	}
	if fake.updateCardCalls != 1 {
		t.Errorf("final full-card update calls = %d, want 1", fake.updateCardCalls)
	}
	if fake.closeCalls != 0 {
		t.Errorf("closeStreaming must be skipped when degraded, got %d calls", fake.closeCalls)
	}
	data, _ := json.Marshal(fake.lastCard)
	if !strings.Contains(string(data), "最终答案") {
		t.Errorf("final card should carry the answer, got: %.300s", string(data))
	}
}

// TestUpdateStillPropagatesOtherErrors: only 200850 is special-cased; any
// other element-write failure keeps the existing propagate-to-agent
// semantics (visible-output abort / pre-output Chat fallback).
func TestUpdateStillPropagatesOtherErrors(t *testing.T) {
	otherErr := errors.New("feishu stream content api error (code=999)")
	fake := &degradeAPIFake{streamErrs: []error{otherErr}}
	s := newDegradeTestStreamer(t, fake)

	if err := s.Update(context.Background(), "答案片段"); !errors.Is(err, otherErr) {
		t.Fatalf("non-200850 errors must propagate, got %v", err)
	}
	if fake.reopenCalls != 0 {
		t.Errorf("reopen must not run for other errors, got %d calls", fake.reopenCalls)
	}
	s.mu.Lock()
	lost := s.streamingLost
	s.mu.Unlock()
	if lost {
		t.Error("streamer must not degrade for other errors")
	}
}
