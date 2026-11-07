package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/snapshot"
)

// newVersionsTestHandler builds a Handler with one session whose workspace
// file changed across a committed checkpoint version.
func newVersionsTestHandler(t *testing.T) (*http.ServeMux, string, *snapshot.Version) {
	t.Helper()

	h, mux, sessionsDir, _ := newRenameTestHandler(t)
	_ = h
	// findJSONLSession resolves legacy pico sessions by their bare UUID and
	// returns the full on-disk key — use that pairing so the versions
	// endpoints address the session the way the UI does.
	sessionKey := legacyPicoSessionPrefix + legacyTestUUID

	// findJSONLSession parses the JSONL — seed one real message so the
	// session resolves (the rename tests only need existence).
	memStore, err := memory.NewJSONLStore(sessionsDir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer memStore.Close()
	if err := memStore.AddFullMessage(nil, sessionKey, providers.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("AddFullMessage: %v", err)
	}
	// A second legacy session with no checkpoint history (empty-list case).
	noCheckpointKey := legacyPicoSessionPrefix + emptyVersionsUUID
	if err := memStore.AddFullMessage(nil, noCheckpointKey, providers.Message{Role: "user", Content: "hello too"}); err != nil {
		t.Fatalf("AddFullMessage(second): %v", err)
	}

	t.Setenv("PICOCLAW_HOME", t.TempDir())
	ws := t.TempDir()
	store, err := snapshot.NewStore(snapshot.DefaultRoot(), snapshot.Config{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	target := filepath.Join(ws, "notes.txt")
	if err := os.WriteFile(target, []byte("token = sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcd\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tc := store.BeginTurn(sessionKey, "t1", ws)
	store.CaptureFile(tc, target, "notes.txt")
	if err := os.WriteFile(target, []byte("token = rotated\n"), 0o644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	v, err := store.CommitTurn(tc)
	if err != nil || v == nil {
		t.Fatalf("CommitTurn: %v, %v", v, err)
	}

	return mux, sessionKey, v
}

func TestSessionVersions_ListAndDiff(t *testing.T) {
	mux, _, v := newVersionsTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+legacyTestUUID+"/versions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID       string             `json:"id"`
		Versions []snapshot.Version `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != legacyTestUUID || len(resp.Versions) != 1 {
		t.Fatalf("resp = id %q, %d versions; want %q, 1", resp.ID, len(resp.Versions), legacyTestUUID)
	}
	if resp.Versions[0].ID != v.ID || len(resp.Versions[0].Files) != 1 {
		t.Fatalf("version mismatch: %+v", resp.Versions[0])
	}

	// The diff endpoint serves the masked unified diff.
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+legacyTestUUID+"/versions/"+v.ID+"/diff", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("diff content-type = %q, want text/plain", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "-token = sk-ant-") && strings.Contains(body, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatal("diff leaks the raw API key")
	}
	if !strings.Contains(body, "a/notes.txt") || !strings.Contains(body, "+token = rotated") {
		t.Fatalf("diff missing expected hunks:\n%s", body)
	}
}

func TestSessionVersions_Errors(t *testing.T) {
	mux, _, _ := newVersionsTestHandler(t)

	// Unknown session → 404.
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/no-such-session/versions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", rec.Code)
	}

	// Unknown version → 404.
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+legacyTestUUID+"/versions/v999-dead/diff", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown version status = %d, want 404", rec.Code)
	}

	// Path traversal in the version id is rejected, not served.
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+legacyTestUUID+"/versions/..%2F..%2Fescape/diff", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
		t.Fatalf("traversal version status = %d, want 400/404", rec.Code)
	}
}

// TestSessionVersions_EmptyListIsArray pins the JSON contract: a session
// with no checkpoint history returns [] rather than null.
// emptyVersionsUUID is a second legacy session id with no checkpoint
// history (exercises the empty-list JSON contract).
const emptyVersionsUUID = "a2c3d4e5-1111-2222-3333-444455556666"

func TestSessionVersions_EmptyListIsArray(t *testing.T) {
	mux, _, _ := newVersionsTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+emptyVersionsUUID+"/versions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"versions":[]`) {
		t.Fatalf("empty versions must serialize as [], got: %s", rec.Body.String())
	}
}
