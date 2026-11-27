package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingAsk is a stub AskFunc that records calls.
type recordingAsk struct {
	mu      sync.Mutex
	calls   []askCall
	answer  string
	err     error
	block   time.Duration
	blockMu sync.Mutex // held while blocked: lets tests observe serialization
}

type askCall struct {
	prompt  string
	channel string
	chatID  string
}

func (r *recordingAsk) ask(ctx context.Context, prompt, channel, chatID string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, askCall{prompt, channel, chatID})
	r.mu.Unlock()
	if r.block > 0 {
		r.blockMu.Lock()
		time.Sleep(r.block)
		r.blockMu.Unlock()
	}
	if r.err != nil {
		return "", r.err
	}
	return r.answer, nil
}

func (r *recordingAsk) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingAsk) call(i int) askCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[i]
}

// postSend posts a message/send request to the handler.
func postSend(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeResult extracts result as an A2AMessage from a 200 JSON-RPC reply.
func decodeResult(t *testing.T, body string) (json.RawMessage, *A2AMessage) {
	t.Helper()
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *JSONRPCError   `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("response not JSON-RPC: %v; body: %s", err, body)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", resp.Error)
	}
	var msg A2AMessage
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		t.Fatalf("result does not decode to A2AMessage: %v; result: %s", err, string(resp.Result))
	}
	return resp.ID, &msg
}

// TestMessageSend covers the happy path: no contextId → server generates one,
// the agent answer comes back as an agent-role A2AMessage, and the JSON-RPC
// id echoes the request's.
func TestMessageSend(t *testing.T) {
	stub := &recordingAsk{answer: "答案文本"}
	srv := NewServer(stub.ask, Options{}) // no token: loopback open
	h := srv.Handler()

	rec := postSend(t, h, `{"jsonrpc":"2.0","id":7,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"你好"}],"messageId":"m1"}}}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}

	id, msg := decodeResult(t, rec.Body.String())
	if string(id) != "7" {
		t.Errorf("jsonrpc id = %s, want 7", id)
	}
	if msg.Role != "agent" {
		t.Errorf("role = %q, want agent", msg.Role)
	}
	if len(msg.Parts) == 0 || msg.Parts[0].Text != "答案文本" {
		t.Errorf("parts = %+v, want [答案文本]", msg.Parts)
	}
	if msg.ContextID == "" {
		t.Error("contextId empty: server must generate one for the first call")
	}

	// AskFunc mapping: channel == "a2a", chatID == contextId, prompt passed through.
	if stub.count() != 1 {
		t.Fatalf("AskFunc called %d times, want 1", stub.count())
	}
	c := stub.call(0)
	if c.channel != "a2a" {
		t.Errorf("channel = %q, want a2a", c.channel)
	}
	if c.chatID != msg.ContextID {
		t.Errorf("chatID = %q, want contextId %q", c.chatID, msg.ContextID)
	}
	if c.prompt != "你好" {
		t.Errorf("prompt = %q, want 你好", c.prompt)
	}
}

// TestMessageSendContextReuse pins review focus 2: the same contextId twice
// must reach AskFunc with the same chatID both times (session continuity).
func TestMessageSendContextReuse(t *testing.T) {
	stub := &recordingAsk{answer: "ok"}
	srv := NewServer(stub.ask, Options{})
	h := srv.Handler()

	for i := 0; i < 2; i++ {
		rec := postSend(t, h, `{"jsonrpc":"2.0","id":`+fmt.Sprint(10+i)+`,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"问"}],"messageId":"m`+fmt.Sprint(i)+`","contextId":"ctx-1"}}}`)
		if rec.Code != 200 {
			t.Fatalf("call %d status = %d, body: %s", i, rec.Code, rec.Body.String())
		}
		_, msg := decodeResult(t, rec.Body.String())
		if msg.ContextID != "ctx-1" {
			t.Errorf("call %d contextId = %q, want ctx-1", i, msg.ContextID)
		}
	}
	if stub.call(0).chatID != "ctx-1" || stub.call(1).chatID != "ctx-1" {
		t.Errorf("chatIDs = %q, %q; both must be ctx-1", stub.call(0).chatID, stub.call(1).chatID)
	}
}

// TestMessageSendSerialization pins review focus 5. Chosen approach:
// per-context mutex SERIALIZATION (second request waits, does not error) —
// the GroupBridge oracle never fires concurrent same-context requests, but
// retry storms could; serializing preserves turn order with no client
// changes.
func TestMessageSendSerialization(t *testing.T) {
	stub := &recordingAsk{answer: "done", block: 200 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	h := srv.Handler()
	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"并发"}],"messageId":"m1","contextId":"ctx-c"}}}`

	start := time.Now()
	done := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			req := httptest.NewRequest("POST", "/", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:54321"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			done <- rec
		}()
	}
	<-done
	<-done
	elapsed := time.Since(start)

	if stub.count() != 2 {
		t.Fatalf("AskFunc called %d times, want 2", stub.count())
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("two same-context requests finished in %v; want serialized (≥200ms)", elapsed)
	}
}

// TestMessageSendError pins the error path: AskFunc failures surface as a
// JSON-RPC server error (-32000 range) carrying the root-cause chain.
func TestMessageSendError(t *testing.T) {
	stub := &recordingAsk{err: errors.New("provider 503: upstream down")}
	srv := NewServer(stub.ask, Options{})
	rec := postSend(t, srv.Handler(), `{"jsonrpc":"2.0","id":9,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"会失败"}],"messageId":"m1"}}}`)

	var resp JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON-RPC: %v; body: %s", err, rec.Body.String())
	}
	if resp.Error == nil {
		t.Fatalf("want JSON-RPC error, got result: %s", rec.Body.String())
	}
	if resp.Error.Code > -32000 || resp.Error.Code < -32099 {
		t.Errorf("error code = %d, want in [-32000,-32099]", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "upstream down") {
		t.Errorf("error message %q lacks root cause", resp.Error.Message)
	}
}

// TestMessageSendMethodNotFound pins -32601 for unknown methods.
func TestMessageSendMethodNotFound(t *testing.T) {
	stub := &recordingAsk{answer: "x"}
	srv := NewServer(stub.ask, Options{})
	rec := postSend(t, srv.Handler(), `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{}}`)
	if !strings.Contains(rec.Body.String(), "-32601") {
		t.Errorf("unknown method must yield -32601, body: %s", rec.Body.String())
	}
	if stub.count() != 0 {
		t.Errorf("AskFunc must not run for unknown methods (ran %d times)", stub.count())
	}
}

// TestNoWriteTimeout pins review focus 1: a server middleware stack with a
// short WriteTimeout must not cut off a slow agent answer — the handler
// clears the per-request write deadline via http.ResponseController.
func TestNoWriteTimeout(t *testing.T) {
	stub := &recordingAsk{answer: "长思考后的答案", block: 300 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	h := srv.Handler()

	s := &http.Server{
		Handler:      h,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond, // deliberately shorter than the turn
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve(ln) }()
	defer func() { _ = s.Close() }()

	resp, err := http.Post("http://"+ln.Addr().String()+"/",
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"慢"}],"messageId":"m1"}}}`))
	if err != nil {
		t.Fatalf("request died (write timeout cut it off?): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *JSONRPCError   `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("body unreadable: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("unexpected error: %+v", out.Error)
	}
	var msg A2AMessage
	if err := json.Unmarshal(out.Result, &msg); err != nil || msg.Parts[0].Text != "长思考后的答案" {
		t.Errorf("slow answer lost: err=%v msg=%+v", err, msg)
	}
}
