package a2a

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMessageStreamE2E drives the SSE handler end to end: an AskFunc stub
// that simulates the pipeline pushing snapshots through the StreamRegistry
// (the bus.StreamDelegate path), then finishing. Frames must arrive as
// data:-prefixed JSON-RPC envelopes with Task results, ending completed.
func TestMessageStreamE2E(t *testing.T) {
	stub := &recordingAsk{answer: "最终答案", block: 800 * time.Millisecond}
	reg := NewStreamRegistry()
	srv := NewServer(stub.ask, Options{})
	h := srv.HandleMessageStream(reg)

	// The stub, while "running", pushes snapshots exactly like the agent
	// pipeline would via GetStreamer.
	stub.onRun = func(prompt, chatID string) {
		if st, ok := reg.GetStreamer(context.Background(), "a2a", chatID, "s"); ok {
			_ = st.Update(context.Background(), "思考中的中间快照")
		}
	}

	req := httptest.NewRequest("POST", "/", strings.NewReader(
		`{"jsonrpc":"2.0","id":11,"method":"message/stream","params":{"message":{"role":"user","parts":[{"kind":"text","text":"流式问题"}],"messageId":"m-s"}}}`))
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h(rec, req)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("SSE handler returned before the stream finished")
	case <-time.After(300 * time.Millisecond):
	}

	// Simulate pipeline finish: streamer Finalize closes the stream.
	if st, ok := reg.GetStreamer(context.Background(), "a2a", stub.call(0).chatID, "s"); ok {
		_ = st.Finalize(context.Background(), "最终答案")
	} else {
		t.Fatalf("stream not registered under chatID %q", stub.call(0).chatID)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after Finalize")
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}

	var states []string
	var lastArtifact string
	var ctxID string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env struct {
			Result Task `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err != nil {
			t.Fatalf("bad SSE frame: %v", err)
		}
		states = append(states, env.Result.Status.State)
		ctxID = env.Result.ContextID
		if len(env.Result.Artifacts) > 0 {
			lastArtifact = env.Result.Artifacts[0].Parts[0].Text
		}
	}

	if len(states) < 2 || states[0] != "working" || states[len(states)-1] != "completed" {
		t.Errorf("state sequence = %v, want working…completed", states)
	}
	if ctxID == "" {
		t.Error("task frames must carry contextId")
	}
	if lastArtifact != "最终答案" {
		t.Errorf("final artifact = %q, want 最终答案", lastArtifact)
	}
	if stub.call(0).channel != "a2a" {
		t.Errorf("channel = %q, want a2a", stub.call(0).channel)
	}
}

// TestMessageStreamRejectsSecondStreamOnSameContext: two concurrent streams
// for one contextId — the second registration overwrites the first map
// entry, which is wrong; the handler must instead refuse with -32000.
func TestMessageStreamRejectsSecondStreamOnSameContext(t *testing.T) {
	stub := &recordingAsk{answer: "x", block: 500 * time.Millisecond}
	reg := NewStreamRegistry()
	srv := NewServer(stub.ask, Options{})
	h := srv.HandleMessageStream(reg)

	body := `{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","parts":[{"kind":"text","text":"q"}],"messageId":"m1","contextId":"ctx-dup"}}}`

	go func() {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		h(rec, req)
	}()
	time.Sleep(100 * time.Millisecond) // let the first stream register

	req2 := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req2.RemoteAddr = "127.0.0.1:2"
	rec2 := httptest.NewRecorder()
	h(rec2, req2)

	if !strings.Contains(rec2.Body.String(), "-32000") {
		t.Errorf("second stream on live context must be refused with -32000, body: %s", rec2.Body.String())
	}
}
