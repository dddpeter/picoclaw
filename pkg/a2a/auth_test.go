package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mkRemote posts with an explicit RemoteAddr (loopback exemption is
// RemoteAddr-based, review focus 4).
func mkRemote(h http.Handler, remote, header, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/", strings.NewReader(sameBody()))
	req.RemoteAddr = remote
	if header != "" {
		req.Header.Set(header, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAuthRequired pins review focus 4: with a token configured, requests
// from non-loopback callers without the right Authorization header must be
// rejected with HTTP 401 + a JSON-RPC error body, and must never reach the
// agent loop.
func TestAuthRequired(t *testing.T) {
	stub := &recordingAsk{answer: "secret answer"}
	srv := NewServer(stub.ask, Options{Token: "s3cret"})
	h := srv.Handler()
	const remote = "192.0.2.1:1234" // non-loopback (TEST-NET)

	// No header at all → 401, JSON-RPC error body, AskFunc untouched.
	rec := mkRemote(h, remote, "", "")
	if rec.Code != 401 {
		t.Fatalf("no-header status = %d, want 401", rec.Code)
	}
	var resp struct {
		Error *JSONRPCError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == nil {
		t.Fatalf("401 body must carry a JSON-RPC error, got: %s", rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want Bearer challenge", got)
	}
	if stub.count() != 0 {
		t.Fatalf("rejected request reached the agent loop (%d calls)", stub.count())
	}

	// Wrong token → 401 as well.
	if rec2 := mkRemote(h, remote, "Authorization", "Bearer wrong"); rec2.Code != 401 {
		t.Errorf("wrong-token status = %d, want 401", rec2.Code)
	}

	// Correct token → reaches AskFunc.
	rec3 := mkRemote(h, remote, "Authorization", "Bearer s3cret")
	if rec3.Code != 200 {
		t.Fatalf("valid-token status = %d, body: %s", rec3.Code, rec3.Body.String())
	}
	if stub.count() != 1 {
		t.Errorf("AskFunc ran %d times, want exactly 1 (valid token)", stub.count())
	}
}

// TestLocalBypass pins the loopback exemption: with a token configured,
// callers from 127.0.0.1 / ::1 are trusted even without the header;
// non-loopback callers are not. (httptest.NewRequest defaults RemoteAddr
// to 192.0.2.1:1234 — an explicit TEST-NET address, i.e. NOT loopback.)
func TestLocalBypass(t *testing.T) {
	stub := &recordingAsk{answer: "ok"}
	srv := NewServer(stub.ask, Options{Token: "s3cret"})
	h := srv.Handler()

	mk := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(sameBody()))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := mk("127.0.0.1:5555"); rec.Code != 200 {
		t.Errorf("loopback v4 without header status = %d, want 200 (bypass)", rec.Code)
	}
	if rec := mk("[::1]:5555"); rec.Code != 200 {
		t.Errorf("loopback v6 without header status = %d, want 200 (bypass)", rec.Code)
	}
	if rec := mk("192.0.2.1:1234"); rec.Code != 401 {
		t.Errorf("non-loopback without header status = %d, want 401", rec.Code)
	}
}

// TestLoopbackOnlyWithoutToken: empty token means loopback-only — loopback
// passes, anything else is 401.
func TestLoopbackOnlyWithoutToken(t *testing.T) {
	stub := &recordingAsk{answer: "ok"}
	srv := NewServer(stub.ask, Options{})
	h := srv.Handler()

	mk := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(sameBody()))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := mk("127.0.0.1:5555"); rec.Code != 200 {
		t.Errorf("loopback without token status = %d, want 200", rec.Code)
	}
	if rec := mk("10.1.2.3:999"); rec.Code != 401 {
		t.Errorf("LAN without token status = %d, want 401", rec.Code)
	}
}

// withHeader wraps a handler so every request carries the given header.
func withHeader(next http.Handler, key, val string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(key, val)
		next.ServeHTTP(w, r)
	})
}

func sameBody() string {
	return `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}],"messageId":"m1"}}}`
}
