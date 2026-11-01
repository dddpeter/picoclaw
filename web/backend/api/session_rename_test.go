package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

// newRenameTestHandler builds a Handler whose workspace lives in a temp dir
// with one real session JSONL on disk.
func newRenameTestHandler(t *testing.T) (*Handler, *http.ServeMux, string, string) {
	t.Helper()

	ws := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = ws
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	sessionsDir := resolveSessionsDir(ws)
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A real session file so SessionExists passes for the happy path.
	sessionKey := "test-session"
	if err := os.WriteFile(filepath.Join(sessionsDir, sessionKey+".jsonl"), []byte(""), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux, sessionsDir, sessionKey
}

// legacyTestUUID is a bare legacy pico session id as the list API exposes it.
const legacyTestUUID = "9f1c2ab4-7d3e-4f5a-8b6c-1e2d3f4a5b6c"

// A legacy-prefixed .jsonl session (on disk agent_main_pico_direct_pico_<uuid>.jsonl,
// list ID = bare UUID) must be renamable: the handler resolves the bare UI id
// back to the full on-disk key instead of letting the m-7 ghost check 404 it.
func TestRenameSessionLegacyPicoPrefixedJSONL(t *testing.T) {
	h, mux, sessionsDir, _ := newRenameTestHandler(t)
	_ = h

	fullKey := legacyPicoSessionPrefix + legacyTestUUID
	legacyPath := filepath.Join(sessionsDir, sanitizeSessionKey(fullKey)+".jsonl")
	if err := os.WriteFile(legacyPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+legacyTestUUID+"/title", strings.NewReader(`{"title":"legacy renamed"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for legacy-prefixed session: %s", rec.Code, rec.Body.String())
	}

	// Title must persist under the FULL legacy key, not the bare UUID.
	store, err := memory.NewJSONLStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.GetSessionMeta(t.Context(), fullKey)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "legacy renamed" || meta.TitleSource != memory.SessionTitleSourceUser {
		t.Fatalf("meta = %+v, want legacy renamed/user under full key", meta)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, legacyTestUUID+".meta.json")); err == nil {
		t.Fatal("orphan meta created under the bare UUID")
	}
}

// Older legacy .json sessions cannot carry a stored title (the list shows
// first-message previews for them); rename must refuse honestly instead of
// writing a meta file that would never surface in the UI.
func TestRenameSessionLegacyJSONRefused(t *testing.T) {
	h, mux, sessionsDir, _ := newRenameTestHandler(t)
	_ = h

	fullKey := legacyPicoSessionPrefix + legacyTestUUID
	body := `{"key":"` + fullKey + `","messages":[{"role":"user","content":"hi"}]}`
	legacyPath := filepath.Join(sessionsDir, sanitizeSessionKey(fullKey)+".json")
	if err := os.WriteFile(legacyPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+legacyTestUUID+"/title", strings.NewReader(`{"title":"nope"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for legacy .json session: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, sanitizeSessionKey(fullKey)+".meta.json")); err == nil {
		t.Fatal("meta file written for a session whose title can never display")
	}
}

func TestRenameSessionRejectsGhostID(t *testing.T) {
	h, mux, sessionsDir, _ := newRenameTestHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/sessions/ghost-id/title", strings.NewReader(`{"title":"boo"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	_ = h

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for nonexistent session", rec.Code)
	}

	// No orphan meta file may be created for the ghost ID.
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "ghost-id") {
			t.Fatalf("ghost ID left an artifact: %s", e.Name())
		}
	}
}

func TestRenameSessionHappyPath(t *testing.T) {
	h, mux, sessionsDir, key := newRenameTestHandler(t)

	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+key+"/title", strings.NewReader(`{"title":"renamed"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	_ = h

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK || resp.Title != "renamed" {
		t.Fatalf("response = %+v", resp)
	}

	// Title must actually be persisted under the user source.
	store, err := memory.NewJSONLStore(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.GetSessionMeta(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "renamed" || meta.TitleSource != memory.SessionTitleSourceUser {
		t.Fatalf("meta = %+v, want renamed/user", meta)
	}
}
