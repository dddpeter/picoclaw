package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

func newPluginsTestServer(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	h := NewHandler(configPath)
	installRoot := t.TempDir()
	h.setPluginsRoots(installRoot, filepath.Join(installRoot, "data"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, installRoot
}

// writeTestPlugin writes a minimal-but-complete plugin (manifest, one skill,
// one stdio MCP server) into root/<name>.
func writeTestPlugin(t *testing.T, installRoot, name string) string {
	t.Helper()
	root := filepath.Join(installRoot, name)
	if err := os.MkdirAll(filepath.Join(root, "skills", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: alpha\ndescription: test\n---\n\nbody\n"
	files := map[string]string{
		"plugin.json": fmt.Sprintf(`{"$schema":%q,"name":%q,"version":"1.2.0"}`, agentplugins.ManifestSchemaURL, name),
		"mcp.json":    fmt.Sprintf(`{"$schema":%q,"mcpServers":{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]}}}`, agentplugins.MCPConfigSchemaURL),
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "alpha", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeTestRegistry(t *testing.T, installRoot, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(installRoot, "registry.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
}

func TestPluginsList_EmptyInstallRoot(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)

	rec := doJSON(t, mux, http.MethodGet, "/api/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp pluginsResponse
	decodeJSON(t, rec, &resp)
	if resp.InstallRoot != installRoot {
		t.Errorf("installRoot = %q, want %q", resp.InstallRoot, installRoot)
	}
	if len(resp.Plugins) != 0 {
		t.Errorf("plugins = %+v, want empty", resp.Plugins)
	}
}

func TestPluginsList_MergesRegistryAndScan(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "golden")
	writeTestRegistry(t, installRoot,
		`{"golden":{"name":"golden","version":"1.2.0","source":"https://github.com/example/golden","ref":"v1","installedAt":"2026-09-19T10:00:00Z","enabled":true}}`)

	rec := doJSON(t, mux, http.MethodGet, "/api/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp pluginsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Plugins) != 1 {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	p := resp.Plugins[0]
	if p.Name != "golden" || p.Version != "1.2.0" || !p.Enabled || !p.Registered {
		t.Errorf("plugin = %+v", p)
	}
	if p.Source != "https://github.com/example/golden" || p.Ref != "v1" {
		t.Errorf("source/ref = %q/%q", p.Source, p.Ref)
	}
	if p.InstalledAt == "" {
		t.Error("installedAt must be set from registry")
	}
	if p.Skills != 1 || p.MCPServers != 1 {
		t.Errorf("counts = skills %d, mcp %d; want 1/1", p.Skills, p.MCPServers)
	}
	if p.LoadError != "" {
		t.Errorf("loadError = %q, want empty", p.LoadError)
	}
}

func TestPluginsList_BrokenDirShowsLoadError(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	if err := os.MkdirAll(filepath.Join(installRoot, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, http.MethodGet, "/api/plugins", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp pluginsResponse
	decodeJSON(t, rec, &resp)
	if len(resp.Plugins) != 1 || resp.Plugins[0].Name != "broken" {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	p := resp.Plugins[0]
	if p.LoadError == "" || p.Skills != 0 {
		t.Errorf("broken plugin = %+v", p)
	}
	if !p.Enabled {
		t.Error("orphan broken dir defaults to enabled=true for display")
	}
}

func TestPluginsList_OrphanDirUnregistered(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "orphan")

	var resp pluginsResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &resp)
	if len(resp.Plugins) != 1 {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	p := resp.Plugins[0]
	if p.Registered || !p.Enabled {
		t.Errorf("orphan = %+v, want registered=false enabled=true", p)
	}
}

func TestPluginsList_RegistryEntryWithoutDir(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestRegistry(t, installRoot,
		`{"ghost":{"name":"ghost","version":"0.1.0","enabled":false}}`)

	var resp pluginsResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &resp)
	if len(resp.Plugins) != 1 || resp.Plugins[0].Name != "ghost" {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	p := resp.Plugins[0]
	if p.LoadError != "installed but directory missing" {
		t.Errorf("loadError = %q", p.LoadError)
	}
	if !p.Registered || p.Enabled {
		t.Errorf("ghost = %+v, want registered=true enabled=false", p)
	}
}

func TestPluginsList_DisabledPlugin(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "golden")
	writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":false}}`)

	var resp pluginsResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &resp)
	if len(resp.Plugins) != 1 {
		t.Fatalf("plugins = %+v", resp.Plugins)
	}
	if resp.Plugins[0].Enabled {
		t.Error("must be disabled")
	}
	if resp.Plugins[0].Skills != 0 {
		t.Errorf("disabled plugin must not discover components, skills = %d", resp.Plugins[0].Skills)
	}
}
