package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// seedJSONLSession writes a minimal meta+jsonl pair for an opaque session.
func seedJSONLSession(t *testing.T, dir, key, content string) {
	t.Helper()
	base := filepath.Join(dir, sanitizeSessionKey(key))
	if err := os.WriteFile(base+".meta.json", []byte(`{"key":"`+key+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, err := json.Marshal(providers.Message{Role: "user", Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".jsonl", append(msg, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// opaqueTestKey builds a syntactically valid opaque key with a distinct
// trailing letter.
func opaqueTestKey(letter byte) string {
	return "sk_v1_" + strings.Repeat("0", 57) + string(letter)
}

// newSessionTestHandler wires the standard test env: config + sessions dir.
func newSessionTestHandler(t *testing.T) (*Handler, string, *http.ServeMux) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	dir := sessionsTestDir(t, configPath)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, dir, mux
}

// TestFindJSONLSessionOpaqueFastPath pins the direct-stat resolution: an
// opaque-key session (ID == Key == base) is found via its meta file alone,
// without the whole-directory scan.
func TestFindJSONLSessionOpaqueFastPath(t *testing.T) {
	h, dir, _ := newSessionTestHandler(t)
	key := opaqueTestKey('a')
	seedJSONLSession(t, dir, key, "你好")
	// A decoy session that would only be found by the full scan.
	seedJSONLSession(t, dir, opaqueTestKey('b'), "其他")

	ref, err := h.findJSONLSession(dir, key, "")
	if err != nil {
		t.Fatalf("findJSONLSession() error = %v", err)
	}
	if ref.Key != key || ref.ID != key {
		t.Fatalf("ref = %+v, want Key/ID == %q", ref, key)
	}
}

// TestHandleListSessionsPaginationWindow pins the two-phase listing: with
// more sessions than one page, the response equals the full-parse result's
// page (order by updated desc) — MessageCount/preview semantics unchanged.
func TestHandleListSessionsPaginationWindow(t *testing.T) {
	_, dir, mux := newSessionTestHandler(t)

	const total = 7
	base := time.Now()
	for i := 0; i < total; i++ {
		key := opaqueTestKey(byte('a' + i))
		seedJSONLSession(t, dir, key, "内容 "+string(rune('a'+i)))
		// Distinct mtimes so the sort order is deterministic; seed order is
		// oldest first so "g" (last) sorts first.
		stamp := base.Add(time.Duration(i) * time.Second)
		jsonlPath := filepath.Join(dir, sanitizeSessionKey(key)+".jsonl")
		if err := os.Chtimes(jsonlPath, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	listPage := func(offset string) []sessionListItem {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/sessions?offset="+offset+"&limit=3", nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var items []sessionListItem
		if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		return items
	}

	page0 := listPage("0")
	page2 := listPage("6")
	if len(page0) != 3 || len(page2) != 1 {
		t.Fatalf("page sizes = %d/%d, want 3/1", len(page0), len(page2))
	}
	// Newest first: page0[0] is the last-seeded session (g), page2 is the oldest (a).
	if page0[0].Preview != "内容 g" {
		t.Fatalf("page0[0].Preview = %q, want newest session", page0[0].Preview)
	}
	if page2[0].Preview != "内容 a" {
		t.Fatalf("page2[0].Preview = %q, want oldest session", page2[0].Preview)
	}
	if page0[0].MessageCount != 1 {
		t.Fatalf("MessageCount = %d, want 1 (transcript semantics preserved)", page0[0].MessageCount)
	}
}

// TestHandleListSessionsEmptyJSONLStillFiltered pins the emptiness prefilter:
// a zero-byte jsonl never occupies a pagination slot.
func TestHandleListSessionsEmptyJSONLStillFiltered(t *testing.T) {
	_, dir, mux := newSessionTestHandler(t)

	key := opaqueTestKey('e')
	if err := os.WriteFile(filepath.Join(dir, sanitizeSessionKey(key)+".meta.json"), []byte(`{"key":"`+key+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sanitizeSessionKey(key)+".jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	mux.ServeHTTP(rec, req)
	var items []sessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %d, want 0 (empty jsonl filtered)", len(items))
	}
}
