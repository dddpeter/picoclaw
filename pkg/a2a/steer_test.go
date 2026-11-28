package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSteer stubs the injected Steer func (P4.2 message/steer).
type recordingSteer struct {
	mu      sync.Mutex
	prompts []string
	err     error
	block   time.Duration
}

func (r *recordingSteer) steer(prompt string) error {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	err, block := r.err, r.block
	r.mu.Unlock()
	if block > 0 {
		time.Sleep(block)
	}
	return err
}

func (r *recordingSteer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

func steerBody(text string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"message/steer","params":{"message":{"role":"user","parts":[{"kind":"text","text":%q}],"messageId":"s1","contextId":"ctx-steer"}}}`, text)
}

// decodeSteerResult extracts the JSON-RPC reply: (errorView, accepted).
type steerReply struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeSteerReply(t *testing.T, body string) (steerReply, bool) {
	t.Helper()
	var resp steerReply
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode reply: %v\nbody: %s", err, body)
	}
	if resp.Error == nil {
		accepted, ok := resp.Result["accepted"].(bool)
		if !ok || !accepted {
			t.Fatalf("result = %+v, want accepted:true", resp.Result)
		}
		return resp, true
	}
	return resp, false
}

func TestMessageSteerAccepted(t *testing.T) {
	stub := &recordingSteer{}
	askStub := &recordingAsk{answer: "x"}
	srv := NewServer(askStub.ask, Options{Steer: stub.steer})
	h := srv.Handler()

	rec := postSend(t, h, steerBody("顺便把 CHANGELOG 也补上"))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if _, ok := decodeSteerReply(t, rec.Body.String()); !ok {
		t.Fatalf("expected accepted:true, body: %s", rec.Body.String())
	}
	if stub.count() != 1 {
		t.Fatalf("steer called %d times, want 1", stub.count())
	}
}

func TestMessageSteerNoActiveTurn(t *testing.T) {
	stub := &recordingSteer{err: errors.New("no active turn to steer")}
	askStub := &recordingAsk{answer: "x"}
	srv := NewServer(askStub.ask, Options{Steer: stub.steer})
	h := srv.Handler()

	rec := postSend(t, h, steerBody("插话"))
	reply, ok := decodeSteerReply(t, rec.Body.String())
	if ok {
		t.Fatalf("expected server error, body: %s", rec.Body.String())
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "no active turn to steer") {
		t.Errorf("error must carry the fallback marker, body: %s", rec.Body.String())
	}
}

func TestMessageSteerNotWired(t *testing.T) {
	askStub := &recordingAsk{answer: "x"}
	srv := NewServer(askStub.ask, Options{})
	rec := postSend(t, srv.Handler(), steerBody("插话"))
	reply, ok := decodeSteerReply(t, rec.Body.String())
	if ok {
		t.Fatalf("expected -32601 when Steer is not wired, body: %s", rec.Body.String())
	}
	if reply.Error.Code != CodeMethodNotFound {
		t.Fatalf("code = %d, want %d", reply.Error.Code, CodeMethodNotFound)
	}
}

// TestMessageSteerDoesNotBlockOnGate pins the P4.2 incident: steer must reach
// the steering queue even while a same-context message/send holds the
// serialization lock (the 30s accept-window starvation that started P4.2).
func TestMessageSteerDoesNotBlockOnGate(t *testing.T) {
	stubAsk := &recordingAsk{answer: "slow turn", block: 1500 * time.Millisecond}
	stubSteer := &recordingSteer{}
	srv := NewServer(stubAsk.ask, Options{Steer: stubSteer.steer})
	h := srv.Handler()

	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		postSend(t, h, `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"长回合"}],"messageId":"busy1","contextId":"ctx-busy"}}}`)
	}()
	time.Sleep(150 * time.Millisecond) // let message/send take the context lock

	steerDone := make(chan struct{})
	go func() {
		defer close(steerDone)
		rec := postSend(t, h, steerBody("插话注入"))
		if _, ok := decodeSteerReply(t, rec.Body.String()); !ok {
			t.Errorf("steer rejected while gate held: %s", rec.Body.String())
		}
	}()
	select {
	case <-steerDone:
		// steer returned while message/send still in flight — no lock taken.
	case <-time.After(2 * time.Second):
		t.Fatal("message/steer blocked behind the context lock — gate starvation regression")
	}
	<-sendDone
	if stubSteer.count() != 1 {
		t.Fatalf("steer called %d times, want 1", stubSteer.count())
	}
}
