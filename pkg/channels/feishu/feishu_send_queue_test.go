package feishu

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// seqRecordingFake records every CardKit call as "kind:seq" in arrival
// order, with an optional artificial per-call latency.
type seqRecordingFake struct {
	mu     sync.Mutex
	events []string
	block  time.Duration
}

func (f *seqRecordingFake) record(kind string, seq int) {
	if f.block > 0 {
		time.Sleep(f.block)
	}
	f.mu.Lock()
	f.events = append(f.events, kind+":"+strconv.Itoa(seq))
	f.mu.Unlock()
}

func (f *seqRecordingFake) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *seqRecordingFake) streamContent(_ context.Context, _, _, _ string, seq int) error {
	f.record("stream", seq)
	return nil
}

func (f *seqRecordingFake) updateCard(_ context.Context, _ string, _ map[string]any, seq int) error {
	f.record("card", seq)
	return nil
}

func (f *seqRecordingFake) reopenStreaming(_ context.Context, _ string, seq int) error {
	f.record("reopen", seq)
	return nil
}

func (f *seqRecordingFake) closeStreaming(_ context.Context, _ string, _ map[string]any, seq int) error {
	f.record("close", seq)
	return nil
}

func newSeqTestStreamer(t *testing.T, fake *seqRecordingFake) *feishuCardStreamer {
	t.Helper()
	ch, _ := newCardActionTestChannel()
	s := newFeishuCardStreamer(ch, "chat-seq", "card-seq", "")
	s.streamContent = fake.streamContent
	s.updateCard = fake.updateCard
	s.reopenStreaming = fake.reopenStreaming
	s.closeStreaming = fake.closeStreaming
	return s
}

// forceWriteThroughLocked zeroes the answer throttle so the next Update
// performs an element write. Caller must hold s.mu.
func (s *feishuCardStreamer) forceWriteThroughLocked() {
	s.answerSentAt = time.Time{}
}

// TestSendQueuePreservesSeqOrder pins the queue's core contract: with
// concurrent producers (answer write-throughs + panel flushes), the wire
// order observed by the fake is strictly increasing in CardKit seq —
// queue order == seq assignment order, single pump.
func TestSendQueuePreservesSeqOrder(t *testing.T) {
	fake := &seqRecordingFake{}
	s := newSeqTestStreamer(t, fake)
	ctx := context.Background()

	const perGoroutine = 40
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < perGoroutine; i++ {
			s.mu.Lock()
			s.forceWriteThroughLocked()
			s.mu.Unlock()
			if err := s.Update(ctx, "答案片段"); err != nil {
				t.Errorf("Update: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < perGoroutine; i++ {
			s.mu.Lock()
			s.panelSentAt = time.Time{} // bypass the panel throttle
			s.mu.Unlock()
			if err := s.AppendToolStep(ctx, bus.ToolStep{Kind: bus.ToolStepKindProgress, Result: "拍"}); err != nil {
				t.Errorf("AppendToolStep: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	// Wait for the queue to fully drain before asserting wire order.
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		pending := len(s.pendingSends)
		s.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send queue did not drain: %d pending", pending)
		}
		time.Sleep(5 * time.Millisecond)
	}

	events := fake.snapshot()
	if len(events) == 0 {
		t.Fatal("no CardKit calls recorded")
	}
	prev := 0
	for i, ev := range events {
		seq := 0
		for j := len(ev) - 1; j >= 0; j-- {
			if ev[j] == ':' {
				v, err := strconv.Atoi(ev[j+1:])
				if err != nil {
					t.Fatalf("event %q: bad seq", ev)
				}
				seq = v
				break
			}
		}
		if seq <= prev {
			t.Fatalf("wire order broke seq monotonicity at event %d: %q (prev seq %d, got %d)", i, ev, prev, seq)
		}
		prev = seq
	}
}

// TestSendQueueKeepsLockFreeDuringAPICall pins the reason the queue exists:
// a slow CardKit call runs OUTSIDE s.mu, so concurrent streamer methods
// (LLM stream callbacks) are not stalled behind it.
func TestSendQueueKeepsLockFreeDuringAPICall(t *testing.T) {
	fake := &seqRecordingFake{block: 300 * time.Millisecond}
	s := newSeqTestStreamer(t, fake)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mu.Lock()
		s.forceWriteThroughLocked()
		s.mu.Unlock()
		_ = s.Update(ctx, "慢写穿")
	}()

	// Give the writer goroutine time to enter its blocked API call, then
	// probe the mutex: it must be free while the API call is in flight.
	time.Sleep(80 * time.Millisecond)
	probe := make(chan struct{})
	go func() {
		s.mu.Lock()
		s.mu.Unlock()
		close(probe)
	}()
	select {
	case <-probe:
	case <-time.After(2 * time.Second):
		t.Fatal("s.mu still held while the CardKit call was in flight — lock leaked into the send path")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Update did not finish within 5s")
	}
}

// TestSendQueuePendingCap pins the overflow policy: a wedged API call plus
// a burst of producers must drop ops (with a warning) instead of growing
// the queue unboundedly, and the queue must still drain afterwards.
func TestSendQueuePendingCap(t *testing.T) {
	fake := &seqRecordingFake{block: 100 * time.Millisecond}
	s := newSeqTestStreamer(t, fake)
	ctx := context.Background()

	const burst = 200
	var wg sync.WaitGroup
	wg.Add(burst)
	for i := 0; i < burst; i++ {
		go func() {
			defer wg.Done()
			s.mu.Lock()
			s.forceWriteThroughLocked()
			s.mu.Unlock()
			_ = s.Update(ctx, "突发")
		}()
	}
	wg.Wait()

	// The queue must drain (every enqueued op runs exactly once) and the
	// drop policy must have engaged (fewer recorded calls than attempts).
	deadline := time.Now().Add(30 * time.Second)
	for {
		s.mu.Lock()
		pending := len(s.pendingSends)
		s.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("send queue did not drain: %d pending", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
	recorded := len(fake.snapshot())
	if recorded >= burst {
		t.Fatalf("recorded %d calls for %d attempts — the pending cap never dropped anything", recorded, burst)
	}
	if recorded == 0 {
		t.Fatal("no calls recorded at all")
	}
}
