package a2a

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStreamRegistryLifecycle(t *testing.T) {
	reg := NewStreamRegistry()

	// No active stream for a fresh context.
	if _, ok := reg.GetStreamer(context.Background(), "a2a", "ctx-1", "s"); ok {
		t.Fatal("fresh context must not have a streamer")
	}
	// Non-a2a channels never route here.
	if _, ok := reg.GetStreamer(context.Background(), "pico", "ctx-1", "s"); ok {
		t.Fatal("non-a2a channel must not hit the a2a delegate")
	}

	s := reg.Register("ctx-1", "task-1")
	defer reg.Unregister("ctx-1")

	got, ok := reg.GetStreamer(context.Background(), "a2a", "ctx-1", "s")
	if !ok || got == nil {
		t.Fatal("registered context must yield a streamer")
	}

	// Update → working event with full-content snapshot.
	if err := got.Update(context.Background(), "第一段"); err != nil {
		t.Fatalf("update: %v", err)
	}
	ev := <-s.Events()
	if !strings.Contains(ev, `"state":"working"`) || !strings.Contains(ev, "第一段") {
		t.Errorf("working event missing state/artifact: %s", ev)
	}

	// Finalize → completed + stream closes.
	if err := got.Finalize(context.Background(), "第一段 第二段"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	final := <-s.Events()
	if !strings.Contains(final, `"state":"completed"`) || !strings.Contains(final, "第二段") {
		t.Errorf("completed event wrong: %s", final)
	}
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Error("stream did not close after Finalize")
	}
}

func TestCardStreamingCapability(t *testing.T) {
	card := NewAgentCard("PicoClaw", "d", "http://127.0.0.1:18790", true)
	if !card.Capabilities.Streaming {
		t.Error("capabilities.streaming = false, want true once message/stream exists")
	}
}

// TestSSEEventTypeShape pins the wire format: SSE frames are `data: {jsonrpc envelope}\n\n`
// whose result is a Task with kind "task", id, contextId, status.state and artifacts.
func TestSSEEventTypeShape(t *testing.T) {
	reg := NewStreamRegistry()
	s := reg.Register("ctx-x", "task-x")
	defer reg.Unregister("ctx-x")

	st, ok := reg.GetStreamer(context.Background(), "a2a", "ctx-x", "s")
	if !ok {
		t.Fatal("no streamer")
	}
	_ = st.Update(context.Background(), "hello")

	raw := <-s.Events()
	if !strings.HasPrefix(raw, "data: ") {
		t.Fatalf("SSE frame must be data-prefixed, got: %q", raw[:40])
	}
	var envelope struct {
		Result struct {
			Kind      string `json:"kind"`
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
			Status    struct {
				State string `json:"state"`
			} `json:"status"`
			Artifacts []struct {
				Parts []A2APart `json:"parts"`
			} `json:"artifacts"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(raw, "data: ")), &envelope); err != nil {
		t.Fatalf("frame not JSON: %v", err)
	}
	if envelope.Result.Kind != "task" || envelope.Result.ID != "task-x" || envelope.Result.ContextID != "ctx-x" {
		t.Errorf("task identity wrong: %+v", envelope.Result)
	}
	if len(envelope.Result.Artifacts) == 0 || envelope.Result.Artifacts[0].Parts[0].Text != "hello" {
		t.Errorf("artifact text missing: %+v", envelope.Result.Artifacts)
	}
}
