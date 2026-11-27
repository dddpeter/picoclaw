package a2a

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cancelServer is a server with a blocking ask whose ctx the test can
// observe.
type cancelServer struct {
	srv      *Server
	handler  http.Handler
	askDone  chan error
	sawReset chan error // ctx cancellation cause
}

func newCancelServer(t *testing.T) *cancelServer {
	t.Helper()
	cs := &cancelServer{
		askDone:  make(chan error, 1),
		sawReset: make(chan error, 1),
	}
	ask := func(ctx context.Context, _ string, _ string, _ string) (string, error) {
		<-ctx.Done() // detached turn runs until canceled/guard timeout
		select {
		case cs.sawReset <- context.Cause(ctx):
		default:
		}
		return "", ctx.Err()
	}
	cs.srv = NewServer(ask, Options{})
	cs.handler = cs.srv.Handler()
	return cs
}

func (cs *cancelServer) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321" // loopback: pass the empty-token gate
	rec := httptest.NewRecorder()
	cs.handler.ServeHTTP(rec, req)
	return rec
}

// TestTasksCancelLifecycle: unknown → -32001; working → canceled (turn
// stopped, record sticky); completed → -32002 not cancelable.
func TestTasksCancelLifecycle(t *testing.T) {
	cs := newCancelServer(t)
	cancel := func(id string) string {
		return cs.post(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tasks/cancel","params":{"id":%q}}`, id)).Body.String()
	}

	if body := cancel("task-unknown"); !strings.Contains(body, "-32001") {
		t.Errorf("unknown task must be -32001, body: %s", body)
	}

	// Working task (registered the way serveMessageStream does).
	cs.srv.tracker.Register("task-c1", "m1", "ctx-1")
	askCtx, askCancel := context.WithCancel(context.Background())
	cs.srv.trackRun("task-c1", askCancel)
	go func() {
		<-askCtx.Done()
		cs.askDone <- context.Cause(askCtx)
	}()

	body := cancel("task-c1")
	if !strings.Contains(body, `"canceled"`) {
		t.Errorf("cancel must return the canceled task, body: %s", body)
	}
	select {
	case <-cs.askDone: // the ask ctx fired
	case <-time.After(2 * time.Second):
		t.Errorf("tasks/cancel did not stop the detached ask ctx")
	}
	if state, _, _, found := cs.srv.tracker.Get("task-c1"); !found || state != TaskStateCanceled {
		t.Errorf("tracker state = (%q, %v), want canceled", state, found)
	}
	// Sticky: a late Complete/Fail from the dying goroutine must not win.
	cs.srv.tracker.Complete("task-c1", "迟到答案")
	cs.srv.tracker.Fail("task-c1", "context canceled")
	if state, _, _, _ := cs.srv.tracker.Get("task-c1"); state != TaskStateCanceled {
		t.Errorf("canceled record was overwritten, state=%q", state)
	}
	// Re-cancel: now terminal → -32002.
	if body := cancel("task-c1"); !strings.Contains(body, "-32002") {
		t.Errorf("re-cancel of canceled task must be -32002, body: %s", body)
	}

	// Completed task → not cancelable.
	cs.srv.tracker.Register("task-c2", "m2", "ctx-2")
	cs.srv.tracker.Complete("task-c2", "done")
	if body := cancel("task-c2"); !strings.Contains(body, "-32002") || !strings.Contains(body, "completed") {
		t.Errorf("cancel of completed task must be -32002, body: %s", body)
	}
}

// TestCancelStreamNotifiesSSEClient: tasks/cancel pushes a canceled frame
// the attached stream consumer can read, then closes the stream.
func TestCancelStreamNotifiesSSEClient(t *testing.T) {
	cs := newCancelServer(t)
	reg := cs.srv.registry
	if release, ok := reg.Acquire(context.Background(), "ctx-sse"); ok {
		defer release()
	} else {
		t.Fatalf("acquire failed")
	}
	stream := reg.Register("ctx-sse", "task-sse")
	if stream == nil {
		t.Fatalf("register failed")
	}
	cs.srv.tracker.Register("task-sse", "m", "ctx-sse")

	if !reg.CancelStream("ctx-sse", "task-sse") {
		t.Fatalf("CancelStream reported no stream")
	}
	select {
	case frame := <-stream.Events():
		if !strings.Contains(frame, `"canceled"`) {
			t.Errorf("frame = %q, want canceled state", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no SSE frame after CancelStream")
	}
	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("stream not finished after CancelStream")
	}
}
