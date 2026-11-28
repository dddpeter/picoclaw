package a2a

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// streamReqBody builds a message/stream JSON-RPC body.
func streamReqBody(messageID, contextID string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"message/stream","params":{"message":{"role":"user","parts":[{"kind":"text","text":"q"}],"messageId":%q,"contextId":%q}}}`,
		messageID, contextID)
}

// postStream issues a real-TCP message/stream request; the caller reads
// frames and decides when to disconnect. Real TCP (not
// httptest.ResponseRecorder) is the point: the P1/P2 fixes live exactly
// on the disconnect path, which needs r.Context() to actually fire.
func postStream(t *testing.T, url string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("message/stream request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message/stream status = %d", resp.StatusCode)
	}
	return resp
}

// readFirstTaskID scans the first data: frame and returns its Task id
// (the handle a detached watcher would poll tasks/get with).
func readFirstTaskID(t *testing.T, resp *http.Response) string {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
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
		return env.Result.ID
	}
	t.Fatal("no data frame before EOF")
	return ""
}

// tasksGet posts tasks/get over real TCP and returns (state, answerText).
func tasksGet(t *testing.T, url, taskID string) (string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url,
		strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"id":%q}}`, taskID)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("tasks/get: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Status struct {
				State string `json:"state"`
			} `json:"status"`
			Artifacts []A2AArtifact `json:"artifacts"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("tasks/get decode: %v", err)
	}
	text := ""
	if len(out.Result.Artifacts) > 0 {
		text = out.Result.Artifacts[0].Parts[0].Text
	}
	return out.Result.Status.State, text
}

// TestMessageStreamDetachedDelivery pins the P1 fix: a client that
// disconnects mid-turn (the whole point of detached delivery) must still
// leave tasks/get with the answer once the turn finishes — the old code
// left the tracker record "working" forever and leaked the runCancels
// entry, because only the (now gone) handler loop harvested askDone.
func TestMessageStreamDetachedDelivery(t *testing.T) {
	stub := &recordingAsk{answer: "分离后的答案", block: 300 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	reg := srv.Registry()
	// While the turn "runs", push a snapshot exactly like the pipeline
	// would — gives the client an acceptance frame carrying the task id.
	stub.onRun = func(_, chatID string) {
		if st, ok := reg.GetStreamer(context.Background(), "a2a", chatID, ""); ok {
			_ = st.Update(context.Background(), "起步快照")
		}
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := postStream(t, ts.URL, streamReqBody("m-det", "ctx-det"))
	taskDone := make(chan string, 1)
	go func() { taskDone <- readFirstTaskID(t, resp) }()
	var taskID string
	select {
	case taskID = <-taskDone:
	case <-time.After(3 * time.Second):
		t.Fatal("no acceptance frame within 3s")
	}
	resp.Body.Close() // walk away mid-turn — turn keeps running detached

	deadline := time.Now().Add(5 * time.Second)
	for {
		state, text := tasksGet(t, ts.URL, taskID)
		if state == TaskStateCompleted {
			if text != "分离后的答案" {
				t.Fatalf("completed artifact = %q, want the answer", text)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task still %q after 5s — detached answer lost (P1 regression)", state)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The runCancels entry must be reclaimed by the harvester.
	srv.runMu.Lock()
	leaked := len(srv.runCancels)
	srv.runMu.Unlock()
	if leaked != 0 {
		t.Errorf("runCancels holds %d entries after turn end — leaked", leaked)
	}
}

// TestMessageStreamIdempotentRetry pins the P2 fix: a same-messageId retry
// must NOT start a second turn while the detached original still runs — it
// waits for the leader and replays the terminal outcome.
func TestMessageStreamIdempotentRetry(t *testing.T) {
	stub := &recordingAsk{answer: "重试拿回的答案", block: 300 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Leader: client disconnects right after the response head.
	resp := postStream(t, ts.URL, streamReqBody("m-idem", "ctx-idem"))
	resp.Body.Close()

	// Follower: same messageId while the leader's turn is still running.
	resp2 := postStream(t, ts.URL, streamReqBody("m-idem", "ctx-idem"))
	states := []string{}
	sc := bufio.NewScanner(resp2.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env struct {
			Result Task `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env); err != nil {
			t.Fatalf("bad replay frame: %v", err)
		}
		states = append(states, env.Result.Status.State)
		if len(env.Result.Artifacts) > 0 && env.Result.Artifacts[0].Parts[0].Text != "重试拿回的答案" {
			t.Errorf("replay artifact = %q", env.Result.Artifacts[0].Parts[0].Text)
		}
	}
	resp2.Body.Close()
	if len(states) == 0 || states[len(states)-1] != TaskStateCompleted {
		t.Errorf("replay states = %v, want ending completed", states)
	}
	if stub.count() != 1 {
		t.Errorf("AskFunc ran %d times, want 1 (retry must not re-run the turn)", stub.count())
	}

	// Late retry (turn long finished): the cache serves it instantly.
	resp3 := postStream(t, ts.URL, streamReqBody("m-idem", "ctx-idem"))
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	body3Str := string(body3)
	if !strings.Contains(body3Str, `"completed"`) || !strings.Contains(body3Str, "重试拿回的答案") {
		t.Errorf("late retry body = %q, want cached completed answer", body3Str)
	}
	if stub.count() != 1 {
		t.Errorf("late retry re-ran the turn (count=%d)", stub.count())
	}
}

// TestMessageStreamGateHeldUntilTurnEnd pins the gate-lifetime change: a
// different-messageId stream on the same context must QUEUE while a
// detached turn still runs, not start a concurrent second turn on the
// session.
func TestMessageStreamGateHeldUntilTurnEnd(t *testing.T) {
	var active, maxActive int32
	stub := &recordingAsk{answer: "ok", block: 300 * time.Millisecond}
	baseAsk := stub.ask
	srv := NewServer(func(ctx context.Context, prompt, channel, chatID string) (string, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		return baseAsk(ctx, prompt, channel, chatID)
	}, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Turn 1: client disconnects mid-turn; the turn detaches.
	resp := postStream(t, ts.URL, streamReqBody("m-g1", "ctx-gate"))
	resp.Body.Close()

	// Turn 2: different messageId, same context — must queue behind the
	// detached turn, not run concurrently.
	resp2 := postStream(t, ts.URL, streamReqBody("m-g2", "ctx-gate"))
	sc := bufio.NewScanner(resp2.Body)
	sawCompleted := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"state":"completed"`) {
			sawCompleted = true
		}
	}
	resp2.Body.Close()
	if !sawCompleted {
		t.Error("second stream never completed")
	}
	if stub.count() != 2 {
		t.Errorf("AskFunc ran %d times, want 2", stub.count())
	}
	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Errorf("max concurrent turns = %d, want 1 (gate must hold until turn end)", got)
	}
}

// fakePrimaryDelegate implements bus.StreamDelegate only.
type fakePrimaryDelegate struct{}

func (fakePrimaryDelegate) GetStreamer(context.Context, string, string, string) (bus.Streamer, bool) {
	return nil, false
}

// fakeFallbackDelegate implements bus.StreamDelegate + the steering
// extension, recording invocations.
type fakeFallbackDelegate struct{ steered *bool }

func (fakeFallbackDelegate) GetStreamer(context.Context, string, string, string) (bus.Streamer, bool) {
	return nil, false
}

func (f fakeFallbackDelegate) NotifySteering(context.Context, string, string, string, string) bool {
	if f.steered != nil {
		*f.steered = true
	}
	return true
}

// TestChainedStreamDelegateNotifySteering pins the P2 fix: the chain must
// forward bus.SteeringNotifyDelegate — bus.NotifySteering asserts the
// TOP-LEVEL delegate, so without this method enabling A2A silently killed
// steering acks for every channel.
func TestChainedStreamDelegateNotifySteering(t *testing.T) {
	steered := false
	chain := &ChainedStreamDelegate{
		Primary:  fakePrimaryDelegate{},
		Fallback: fakeFallbackDelegate{steered: &steered},
	}
	if !chain.NotifySteering(context.Background(), "pico", "c1", "s1", "hello") {
		t.Error("chain must forward to the fallback's SteeringNotifyDelegate")
	}
	if !steered {
		t.Error("fallback NotifySteering was never invoked")
	}

	// Neither implements it → false.
	empty := &ChainedStreamDelegate{Primary: fakePrimaryDelegate{}, Fallback: fakePrimaryDelegate{}}
	if empty.NotifySteering(context.Background(), "pico", "c1", "s1", "hello") {
		t.Error("chain without any SteeringNotifyDelegate must return false")
	}
}
