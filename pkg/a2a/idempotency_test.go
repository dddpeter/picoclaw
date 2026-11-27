package a2a

import (
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// readAll slurps a response body (test helper shared by dedup tests).
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// TestIdempotencyDedup: Put then Get returns the cached result; a repeated
// Put on the same key overwrites (documented contract — the store is a
// cache of the *latest* successful response for a messageId).
func TestIdempotencyDedup(t *testing.T) {
	store := NewIdempotencyStore(24 * time.Hour)

	if got, ok := store.Get("m1"); ok {
		t.Fatalf("fresh store returned %v for m1, want miss", got)
	}

	first := A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: "first"}}, ContextID: "ctx-1"}
	store.Put("m1", first, "ctx-1")

	got, ok := store.Get("m1")
	if !ok {
		t.Fatal("Get(m1) after Put, want hit")
	}
	if msg, ok := got.(A2AMessage); !ok || msg.Parts[0].Text != "first" {
		t.Errorf("cached value = %#v, want the stored A2AMessage", got)
	}
	if ctx := store.ContextOf("m1"); ctx != "ctx-1" {
		t.Errorf("ContextOf(m1) = %q, want ctx-1", ctx)
	}

	// Overwrite contract: same messageId retried with a different result
	// (e.g. server restarted mid-life) replaces the cache entry.
	second := A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: "second"}}, ContextID: "ctx-1"}
	store.Put("m1", second, "ctx-1")
	got, _ = store.Get("m1")
	if msg := got.(A2AMessage); msg.Parts[0].Text != "second" {
		t.Errorf("second Put did not overwrite: got %q", msg.Parts[0].Text)
	}
}

// TestIdempotencyExpiry: TTL expiry returns a miss (injected clock).
func TestIdempotencyExpiry(t *testing.T) {
	now := time.Now()
	store := NewIdempotencyStore(1 * time.Hour)
	store.now = func() time.Time { return now }

	store.Put("m-old", A2AMessage{Role: "agent"}, "ctx")
	if _, ok := store.Get("m-old"); !ok {
		t.Fatal("entry within TTL must hit")
	}

	now = now.Add(2 * time.Hour) // past TTL
	if _, ok := store.Get("m-old"); ok {
		t.Error("expired entry must miss")
	}
}

// TestServerDedupSkipsAgentLoop: the same messageId sent twice must hit the
// cache — AskFunc runs once, both responses carry the identical result.
func TestServerDedupSkipsAgentLoop(t *testing.T) {
	stub := &recordingAsk{answer: "deduped answer"}
	srv := NewServer(stub.ask, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sendOnce := func() string {
		resp, err := ts.Client().Post(ts.URL, "application/json", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"问题"}],"messageId":"msg-dup-1"}}}`))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		body := readAll(t, resp.Body)
		if !strings.Contains(body, "deduped answer") {
			t.Errorf("response missing answer: %s", body)
		}
		return body
	}

	first := sendOnce()
	second := sendOnce()

	if n := stub.count(); n != 1 {
		t.Errorf("AskFunc ran %d times for one messageId, want 1", n)
	}
	if first != second {
		t.Errorf("cached response differs:\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestServerFailedCallNotCached: an errored message/send must not be cached —
// the retry is expected to re-run the agent loop.
func TestServerFailedCallNotCached(t *testing.T) {
	stub := &recordingAsk{err: errBoom}
	srv := NewServer(stub.ask, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func() {
		resp, err := ts.Client().Post(ts.URL, "application/json", strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"会失败"}],"messageId":"msg-fail"}}}`))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
	}
	post()
	post()

	if n := stub.count(); n != 2 {
		t.Errorf("failed calls were cached: AskFunc ran %d times, want 2 (retry re-runs)", n)
	}
}

// TestIdempotencyConcurrentSameMessage: two concurrent requests with the same
// messageId — exactly one reaches the agent loop (singleflight via the
// in-flight map), both get the same answer.
func TestIdempotencyConcurrentSameMessage(t *testing.T) {
	stub := &recordingAsk{answer: "once", block: 200 * time.Millisecond}
	srv := NewServer(stub.ask, Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"并发同id"}],"messageId":"msg-race"}}}`
	var wg sync.WaitGroup
	results := make([]string, 2)
	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := ts.Client().Post(ts.URL, "application/json", strings.NewReader(body))
			if err != nil {
				t.Errorf("post: %v", err)
				return
			}
			defer resp.Body.Close()
			results[idx] = readAll(t, resp.Body)
		}(i)
	}
	wg.Wait()

	if n := stub.count(); n != 1 {
		t.Errorf("concurrent same-messageId ran AskFunc %d times, want 1", n)
	}
	if results[0] != results[1] {
		t.Errorf("concurrent responses differ:\n%s\n%s", results[0], results[1])
	}
}
