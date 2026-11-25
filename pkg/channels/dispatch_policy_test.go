package channels

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// newDispatchTestManager builds a bare manager + a worker whose queue is
// small enough to fill cheaply (the policy tests never run the worker).
func newDispatchTestManager(t *testing.T) (*Manager, *channelWorker) {
	t.Helper()
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}
	w := newChannelWorker("testch", nil, "testch")
	m.channels["testch"] = nil
	m.workers["testch"] = w
	return m, w
}

func dispatchTestMsg(content, kind string) bus.OutboundMessage {
	msg := bus.OutboundMessage{Content: content}
	msg.Context.Channel = "testch"
	if kind != "" {
		msg.Context.Raw = map[string]string{"message_kind": kind}
	}
	return msg
}

func fillDispatchQueue(w *channelWorker, n int) {
	for i := 0; i < n; i++ {
		w.queue <- dispatchTestMsg("filler", "")
	}
}

// TestEnqueueOutboundDropsBeatOnFullQueue pins the zero-wait policy for
// narration beats: a full queue drops progress_note/thought immediately
// without stalling the dispatcher.
func TestEnqueueOutboundDropsBeatOnFullQueue(t *testing.T) {
	m, w := newDispatchTestManager(t)
	fillDispatchQueue(w, defaultChannelQueueSize)

	done := make(chan bool, 1)
	go func() {
		done <- m.enqueueOutboundForDispatch(context.Background(), w, dispatchTestMsg("beat", "progress_note"))
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("beat drop must not signal dispatcher shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("droppable beat blocked on a full queue — zero-wait policy missing")
	}
	if len(w.queue) != defaultChannelQueueSize {
		t.Fatalf(
			"queue length = %d, want %d (beat must not displace queued messages)",
			len(w.queue),
			defaultChannelQueueSize,
		)
	}
}

// TestEnqueueOutboundCriticalDropsAfterBoundedWait pins the bounded-wait
// policy: a full queue holds a critical message for the configured wait,
// then drops it loudly (error log + runtime event) and keeps the dispatcher
// alive.
func TestEnqueueOutboundCriticalDropsAfterBoundedWait(t *testing.T) {
	m, w := newDispatchTestManager(t)
	oldWait := outboundDropWaitCritical
	outboundDropWaitCritical = 50 * time.Millisecond
	t.Cleanup(func() { outboundDropWaitCritical = oldWait })
	fillDispatchQueue(w, defaultChannelQueueSize)

	start := time.Now()
	done := make(chan bool, 1)
	go func() {
		done <- m.enqueueOutboundForDispatch(context.Background(), w, dispatchTestMsg("最终回答", ""))
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("critical drop must not signal dispatcher shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("critical message blocked longer than the bounded wait")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("critical drop took %s, want ≈ the %s bound", elapsed, oldWait)
	}
	if len(w.queue) != defaultChannelQueueSize {
		t.Fatalf(
			"queue length = %d, want %d (dropped message must not displace queued ones)",
			len(w.queue),
			defaultChannelQueueSize,
		)
	}
}

// TestEnqueueOutboundEnqueuesWhenSpaceAvailable pins the fast path: with
// space, messages enqueue in order and none are dropped.
func TestEnqueueOutboundEnqueuesWhenSpaceAvailable(t *testing.T) {
	m, w := newDispatchTestManager(t)

	for i := 0; i < 5; i++ {
		if ok := m.enqueueOutboundForDispatch(context.Background(), w, dispatchTestMsg("msg", "")); !ok {
			t.Fatalf("enqueue %d signaled shutdown", i)
		}
	}
	if len(w.queue) != 5 {
		t.Fatalf("queue length = %d, want 5", len(w.queue))
	}
	// FIFO order preserved.
	for i := 0; i < 5; i++ {
		msg := <-w.queue
		if msg.Content != "msg" {
			t.Fatalf("queue order broken at %d", i)
		}
	}
}

// TestEnqueueOutboundCancelledContextReturnsFalse pins the shutdown
// contract: a canceled dispatcher context makes the bounded-wait enqueue
// return false (dispatcher exit), even on a full queue.
func TestEnqueueOutboundCancelledContextReturnsFalse(t *testing.T) {
	m, w := newDispatchTestManager(t)
	outboundDropWaitCritical = 10 * time.Second
	t.Cleanup(func() { outboundDropWaitCritical = 2 * time.Second })
	fillDispatchQueue(w, defaultChannelQueueSize)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok := m.enqueueOutboundForDispatch(ctx, w, dispatchTestMsg("最终回答", "")); ok {
		t.Fatal("canceled context must signal dispatcher shutdown")
	}
}
