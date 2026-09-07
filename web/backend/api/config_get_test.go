package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newConfigTestServer writes rawConfig to a temp config.json and returns a
// mux serving the API routes with that config.
func newConfigTestServer(t *testing.T, rawConfig string) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(rawConfig), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

// GET /api/config with unknown fields must load the config (200) and attach
// config_warnings so the UI can render a notice instead of failing hard.
// Regression: *Config has a custom MarshalJSON; the first implementation
// embedded *config.Config in the response struct, and method promotion made
// the encoder call Config.MarshalJSON, silently dropping config_warnings.
func TestGetConfig_UnknownFieldsRespondsWithWarnings(t *testing.T) {
	mux := newConfigTestServer(t, `{
		"version": 3,
		"tools": {"exec": {"timeout_seconds": 600, "approval_patterns": ["x"]}},
		"mystery_section": {"a": 1}
	}`)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	warnings, ok := body["config_warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Fatalf("config_warnings missing or empty; body sample: %s", truncate(rec.Body.String(), 300))
	}
	joined := strings.Join(toStrings(warnings), ",")
	if !strings.Contains(joined, "approval_patterns") {
		t.Fatalf("warnings should mention approval_patterns, got: %s", joined)
	}
	if _, exists := body["version"]; !exists {
		t.Fatal("config body lost normal fields (version)")
	}
}

// GET /api/config with a syntax error must hard-fail (500) with diagnostics.
func TestGetConfig_SyntaxErrorFailsWithDiagnostics(t *testing.T) {
	mux := newConfigTestServer(t, `{"version": 3, "mcp": {"servers": {"serve"`)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Failed to load config") {
		t.Fatalf("body should contain load-failure text, got: %s", truncate(rec.Body.String(), 300))
	}
}

// GET /api/config with a clean config must respond without config_warnings.
func TestGetConfig_CleanConfigHasNoWarnings(t *testing.T) {
	mux := newConfigTestServer(t, `{"version": 3}`)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, exists := body["config_warnings"]; exists {
		t.Fatal("config_warnings should be absent for a clean config")
	}
}

func toStrings(items []any) []string {
	out := make([]string, 0, len(items))
	for _, v := range items {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
