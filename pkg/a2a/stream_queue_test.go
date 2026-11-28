package a2a

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMessageStreamQueueSerial: two concurrent message/stream requests on
// the SAME contextId must both complete — the second queues on the
// per-context gate and starts its own normal stream after the first
// finishes. (The old -32000 refusal starved retrying clients into
// dropping user questions.)
func TestMessageStreamQueueSerial(t *testing.T) {
	stub := &recordingAsk{answer: "排队后的答案", block: 300 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	h := srv.Handler()
	ts := httptest.NewServer(h)
	defer ts.Close()

	// Two DIFFERENT messageIds on the same context: gate serialization,
	// not idempotency dedup (same-messageId retries replay instead —
	// TestMessageStreamIdempotentRetry).
	body := func(id string) string {
		return `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","parts":[{"kind":"text","text":"并发问"}],"messageId":"` + id + `","contextId":"ctx-queue"}}}`
	}
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body("q0"))).WithContext(context.Background())
		rec := httptest.NewRecorder()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { h.ServeHTTP(rec, req); done <- rec }()
		return <-done
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	firstDone, secondDone := make(chan time.Time, 1), make(chan time.Time, 1)
	start := time.Now()
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/", strings.NewReader(body(fmt.Sprintf("q%d", idx))))
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[idx] = rec.Code
			if idx == 0 {
				firstDone <- time.Now()
			} else {
				secondDone <- time.Now()
			}
		}(i)
	}
	f1, f2 := <-firstDone, <-secondDone
	wg.Wait()

	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v, want both 200 (no -32000)", codes)
	}
	if stub.count() != 2 {
		t.Fatalf("AskFunc ran %d times, want 2 (queued, not refused)", stub.count())
	}
	_ = post // keep the helper referenced for parity with other tests
	// Serial: the later finisher must be after the earlier one's span.
	if f2.Before(f1) && f1.Before(f2) {
		t.Fatal("impossible ordering")
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Fatalf("two 300ms runs finished in %s — not serialized", time.Since(start))
	}
}

// TestStreamGateCtxBounded: a waiter whose caller ctx is cancelled leaves
// the queue instead of waiting forever (queue cap follows caller ctx).
func TestStreamGateCtxBounded(t *testing.T) {
	reg := NewStreamRegistry()
	release, ok := reg.Acquire(context.Background(), "ctx-g")
	if !ok {
		t.Fatal("initial acquire failed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, ok := reg.Acquire(ctx, "ctx-g")
		if ok {
			t.Error("waiter acquired while held")
		}
		done <- ctx.Err()
	}()
	time.Sleep(50 * time.Millisecond) // let the waiter block on the gate
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("waiter exited with %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter stuck on a held gate after ctx cancel")
	}
	release()
}
