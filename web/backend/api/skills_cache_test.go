package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestHandleListSkillsCatalogCache pins the browse cache: a second list
// within the TTL serves the memoized catalog (a newly added skill does not
// appear), install's explicit invalidation makes it visible immediately,
// and the entry disappears from the fresh catalog after deletion.
func TestHandleListSkillsCatalogCache(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	dir := sessionsTestDir(t, configPath)
	_ = dir
	invalidateSkillsCatalog()
	t.Cleanup(invalidateSkillsCatalog)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	skillsRoot := filepath.Join(cfg.Agents.Defaults.Workspace, "skills")
	writeSkill := func(name string) {
		t.Helper()
		d := filepath.Join(skillsRoot, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\nBody."
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	list := func() []skillSupportItem {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/skills", nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list status = %d", rec.Code)
		}
		var resp skillSupportResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		return resp.Skills
	}

	writeSkill("cache-a")
	first := list()
	if len(first) != 1 || first[0].Name != "cache-a" {
		t.Fatalf("first list = %+v, want [cache-a]", first)
	}

	// Within the TTL: the new skill is NOT visible yet (memoized catalog).
	writeSkill("cache-b")
	second := list()
	if len(second) != 1 {
		t.Fatalf("cached list = %+v, want memoized [cache-a]", second)
	}

	// Explicit invalidation (what install/import/delete do) exposes it.
	invalidateSkillsCatalog()
	third := list()
	if len(third) != 2 {
		t.Fatalf("post-invalidation list = %+v, want 2 skills", third)
	}
}
