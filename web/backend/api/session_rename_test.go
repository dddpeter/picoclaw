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
