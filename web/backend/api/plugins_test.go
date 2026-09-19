package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

func newPluginsTestServer(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	mux, installRoot, _ := newPluginsTestServerWithConfig(t)
	return mux, installRoot
}

func newPluginsTestServerWithConfig(t *testing.T) (*http.ServeMux, string, string) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	h := NewHandler(configPath)
	installRoot := t.TempDir()
	h.setPluginsRoots(installRoot, filepath.Join(installRoot, "data"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux, installRoot, configPath
}

// writeTestPlugin writes a minimal-but-complete plugin (manifest, one skill,
// one stdio MCP server) into root/<name> and returns its directory.
func writeTestPlugin(t *testing.T, installRoot, name string) string {
	t.Helper()
	root := filepath.Join(installRoot, name)
	writeTestPluginFiles(t, root, name)
	return root
}

// writeTestPluginFiles writes the golden plugin files directly into dir
// (manifest carries the given name; dir layout is flat).
func writeTestPluginFiles(t *testing.T, dir, name string) {
	t.Helper()
	root := dir
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

func TestPluginsEnable_TogglePersists(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "golden")
	writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":true}}`)

	rec := doJSON(t, mux, http.MethodPut, "/api/plugins/golden/enabled", map[string]bool{"enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp pluginsResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &resp)
	if len(resp.Plugins) != 1 || resp.Plugins[0].Enabled {
		t.Fatalf("after disable: %+v", resp.Plugins)
	}

	raw, err := os.ReadFile(filepath.Join(installRoot, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	if e, ok := reg["golden"]; !ok || e.Enabled {
		t.Errorf("registry on disk = %s", raw)
	}
}

func TestPluginsEnable_GuardsAndNotFound(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "golden")
	writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":true}}`)

	for _, name := range []string{"data", "registry.json", "Golden", "a/b"} {
		rec := doJSON(t, mux, http.MethodPut, "/api/plugins/"+url.PathEscape(name)+"/enabled", map[string]bool{"enabled": true})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("enable(%q) status = %d, want 400", name, rec.Code)
		}
	}
	// ".." never reaches the handler: ServeMux cleans the path into a 307
	// redirect, so traversal cannot hit {name} — assert it stays unsuccessful.
	rec := doJSON(t, mux, http.MethodPut, "/api/plugins/../enabled", map[string]bool{"enabled": true})
	if rec.Code == http.StatusOK {
		t.Errorf("enable(traversal) status = 200, must not succeed")
	}
	rec = doJSON(t, mux, http.MethodPut, "/api/plugins/ghost/enabled", map[string]bool{"enabled": true})
	if rec.Code != http.StatusNotFound {
		t.Errorf("enable(unknown) status = %d, want 404", rec.Code)
	}
}

func TestPluginsRemove(t *testing.T) {
	t.Run("default keeps data dir and clears registry", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")
		dataDir := filepath.Join(installRoot, "data", "golden")
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":true}}`)

		rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/golden", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(installRoot, "golden")); !os.IsNotExist(err) {
			t.Error("plugin dir must be gone")
		}
		if _, err := os.Stat(dataDir); err != nil {
			t.Errorf("data dir must survive default remove: %v", err)
		}
		raw, _ := os.ReadFile(filepath.Join(installRoot, "registry.json"))
		if strings.Contains(string(raw), "golden") {
			t.Errorf("registry entry must be gone: %s", raw)
		}
	})

	t.Run("purgeData removes data dir", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")
		dataDir := filepath.Join(installRoot, "data", "golden")
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}

		rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/golden?purgeData=true", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
			t.Error("data dir must be purged")
		}
	})

	t.Run("registry ghost entry is cleanable", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestRegistry(t, installRoot, `{"ghost":{"name":"ghost","enabled":true}}`)

		rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/ghost", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		raw, _ := os.ReadFile(filepath.Join(installRoot, "registry.json"))
		if strings.Contains(string(raw), "ghost") {
			t.Errorf("ghost registry entry must be cleaned: %s", raw)
		}
	})

	t.Run("guards and unknown name", func(t *testing.T) {
		mux, _ := newPluginsTestServer(t)
		for _, name := range []string{"data", "registry.json"} {
			rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/"+url.PathEscape(name), nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("remove(%q) status = %d, want 400", name, rec.Code)
			}
		}
		// ".." never reaches the handler as a path segment (mux normalizes);
		// a percent-encoded traversal is rejected by the name guard instead.
		rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/ghost", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("remove(unknown) status = %d, want 404", rec.Code)
		}
	})
}

func TestPluginsValidate(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	_ = installRoot

	t.Run("golden dir passes", func(t *testing.T) {
		src := writeTestPlugin(t, t.TempDir(), "golden")
		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/validate", map[string]string{"path": src})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var resp pluginReportResponse
		decodeJSON(t, rec, &resp)
		if !resp.OK || resp.Name != "golden" || resp.Skills != 1 || resp.MCPServers != 1 {
			t.Errorf("resp = %+v", resp)
		}
	})

	t.Run("bad plugin reports error", func(t *testing.T) {
		dir := t.TempDir()
		bad := `{"$schema":"` + agentplugins.ManifestSchemaURL + `","name":"Golden"}`
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/validate", map[string]string{"path": dir})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var resp pluginReportResponse
		decodeJSON(t, rec, &resp)
		if resp.OK || resp.Error == "" {
			t.Errorf("resp = %+v, want ok=false + error", resp)
		}
	})

	t.Run("missing path is 400", func(t *testing.T) {
		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/validate", map[string]string{"path": filepath.Join(t.TempDir(), "nope")})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

func TestPluginsInstall(t *testing.T) {
	t.Run("local golden installs and registers", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		src := writeTestPlugin(t, t.TempDir(), "golden")

		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/install", map[string]string{"source": src})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var resp pluginReportResponse
		decodeJSON(t, rec, &resp)
		if !resp.OK || resp.Name != "golden" || resp.Target == "" || resp.Skills != 1 || resp.MCPServers != 1 {
			t.Fatalf("resp = %+v", resp)
		}
		if want := filepath.Join(installRoot, "golden"); resp.Target != want {
			t.Errorf("target = %q, want %q", resp.Target, want)
		}
		if _, err := os.Stat(filepath.Join(installRoot, "golden", "plugin.json")); err != nil {
			t.Errorf("installed plugin.json missing: %v", err)
		}
		raw, _ := os.ReadFile(filepath.Join(installRoot, "registry.json"))
		var reg map[string]struct {
			Source string `json:"source"`
			Ref    string `json:"ref"`
		}
		if err := json.Unmarshal(raw, &reg); err != nil {
			t.Fatal(err)
		}
		if e, ok := reg["golden"]; !ok || e.Source != src {
			t.Errorf("registry = %s", raw)
		}
	})

	t.Run("existing target is refused", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		src := writeTestPlugin(t, t.TempDir(), "golden")
		writeTestPlugin(t, installRoot, "golden")

		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/install", map[string]string{"source": src})
		var resp pluginReportResponse
		decodeJSON(t, rec, &resp)
		if resp.OK || !strings.Contains(resp.Error, "remove") {
			t.Errorf("resp = %+v, want ok=false + remove hint", resp)
		}
	})

	t.Run("invalid source shape is 400", func(t *testing.T) {
		mux, _ := newPluginsTestServer(t)
		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/install", map[string]string{"source": "ftp://example.com/plugin"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("git file source installs", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		gitRoot := t.TempDir()
		bare := filepath.Join(gitRoot, "bare.git")
		work := filepath.Join(gitRoot, "work")
		runGit := func(args ...string) error {
			cmd := exec.Command("git", args...)
			cmd.Dir = work
			cmd.Env = append(os.Environ(),
				"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Logf("git %v: %v: %s", args, err, out)
			}
			return err
		}
		if err := exec.Command("git", "init", "--bare", bare).Run(); err != nil {
			t.Skipf("git init --bare failed: %v", err)
		}
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"init"}, {"checkout", "-b", "main"},
		} {
			if err := runGit(args...); err != nil {
				t.Skipf("git setup failed: %v", err)
			}
		}
		writeTestPluginFiles(t, work, "golden-git")
		for _, args := range [][]string{
			{"add", "-A"}, {"commit", "-m", "plugin"}, {"push", "file://" + filepath.ToSlash(bare), "main"},
		} {
			if err := runGit(args...); err != nil {
				t.Skipf("git push failed: %v", err)
			}
		}

		mux, installRoot := newPluginsTestServer(t)
		rec := doJSON(t, mux, http.MethodPost, "/api/plugins/install", map[string]string{
			"source": "file://" + filepath.ToSlash(bare), "ref": "main",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var resp pluginReportResponse
		decodeJSON(t, rec, &resp)
		if !resp.OK || resp.Name != "golden-git" {
			t.Fatalf("resp = %+v", resp)
		}
		if _, err := os.Stat(filepath.Join(installRoot, "golden-git", "plugin.json")); err != nil {
			t.Errorf("installed plugin.json missing: %v", err)
		}
	})
}

func TestPluginsVisibleInMCPConfig(t *testing.T) {
	t.Run("enabled plugin servers are listed read-only", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		root := writeTestPlugin(t, installRoot, "golden")
		both := fmt.Sprintf(`{"$schema":%q,"mcpServers":{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]},"remote":{"type":"streamable-http","url":"https://deploy.example.com/mcp"}}}`, agentplugins.MCPConfigSchemaURL)
		if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(both), 0o644); err != nil {
			t.Fatal(err)
		}

		var resp mcpConfigResponse
		decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil), &resp)
		if len(resp.PluginServers) != 2 {
			t.Fatalf("pluginServers = %+v", resp.PluginServers)
		}
		byKey := map[string]pluginServerDTO{}
		for _, s := range resp.PluginServers {
			byKey[s.Key] = s
		}
		echo, ok := byKey["plugin/golden/echo"]
		if !ok || echo.Type != "stdio" || echo.Plugin != "golden" || echo.Server != "echo" || echo.Command != "node" {
			t.Errorf("echo = %+v", echo)
		}
		rem, ok := byKey["plugin/golden/remote"]
		if !ok || rem.Type != "streamable-http" || rem.URL != "https://deploy.example.com/mcp" {
			t.Errorf("remote = %+v", rem)
		}
	})

	t.Run("disabled plugin yields none", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")
		writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":false}}`)

		var resp mcpConfigResponse
		decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil), &resp)
		if len(resp.PluginServers) != 0 {
			t.Errorf("pluginServers = %+v", resp.PluginServers)
		}
	})

	t.Run("PUT ignores pluginServers and never persists plugin keys", func(t *testing.T) {
		mux, _, configPath := newPluginsTestServerWithConfig(t)

		rec := doJSON(t, mux, http.MethodPut, "/api/mcp/config", mcpConfigRequest{
			Enabled: true,
			Servers: []mcpServerDTO{{Name: "user-server", Type: "stdio", Command: "node"}},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}

		cfgRaw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(cfgRaw), "plugin/") {
			t.Errorf("config.json must never contain plugin keys: %s", cfgRaw)
		}
	})
}

func TestPluginSkillsInSkillsAPI(t *testing.T) {
	t.Run("plugin skill listed with plugin source", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")

		var resp struct {
			Skills []skillSupportItem `json:"skills"`
		}
		decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/skills", nil), &resp)
		var found *skillSupportItem
		for i := range resp.Skills {
			if resp.Skills[i].Name == "alpha" {
				found = &resp.Skills[i]
			}
		}
		if found == nil {
			t.Fatalf("alpha not listed: %+v", resp.Skills)
		}
		if found.Source != "plugin:golden" {
			t.Errorf("source = %q, want plugin:golden", found.Source)
		}
	})

	t.Run("plugin skill cannot be deleted via skills api", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")

		rec := doJSON(t, mux, http.MethodDelete, "/api/skills/alpha", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400 (only workspace skills deletable)", rec.Code)
		}
		if _, err := os.Stat(filepath.Join(installRoot, "golden", "skills", "alpha", "SKILL.md")); err != nil {
			t.Errorf("plugin skill file must survive: %v", err)
		}
	})

	t.Run("disabled plugin skill not listed", func(t *testing.T) {
		mux, installRoot := newPluginsTestServer(t)
		writeTestPlugin(t, installRoot, "golden")
		writeTestRegistry(t, installRoot, `{"golden":{"name":"golden","enabled":false}}`)

		var resp struct {
			Skills []skillSupportItem `json:"skills"`
		}
		decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/skills", nil), &resp)
		for _, s := range resp.Skills {
			if s.Name == "alpha" {
				t.Errorf("disabled plugin skill must not be listed: %+v", s)
			}
		}
	})
}

// TestPluginsEndToEnd walks the full dashboard flow over HTTP: install →
// list → visibility in MCP config and skills API → disable → remove → gone.
func TestPluginsEndToEnd(t *testing.T) {
	mux, installRoot, _ := newPluginsTestServerWithConfig(t)
	src := writeTestPlugin(t, t.TempDir(), "golden")

	// Install
	var install pluginReportResponse
	decodeJSON(t, doJSON(t, mux, http.MethodPost, "/api/plugins/install", map[string]string{"source": src}), &install)
	if !install.OK || install.Name != "golden" || install.Skills != 1 || install.MCPServers != 1 {
		t.Fatalf("install = %+v", install)
	}

	// List shows it
	var list pluginsResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &list)
	if len(list.Plugins) != 1 || !list.Plugins[0].Enabled || !list.Plugins[0].Registered {
		t.Fatalf("list = %+v", list.Plugins)
	}

	// MCP config exposes the bridged server read-only
	var mcpCfg mcpConfigResponse
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil), &mcpCfg)
	if len(mcpCfg.PluginServers) != 1 || mcpCfg.PluginServers[0].Key != "plugin/golden/echo" {
		t.Fatalf("pluginServers = %+v", mcpCfg.PluginServers)
	}

	// Skills API exposes the plugin skill with plugin source
	var skillsResp struct {
		Skills []skillSupportItem `json:"skills"`
	}
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/skills", nil), &skillsResp)
	var sawPluginSkill bool
	for _, s := range skillsResp.Skills {
		if s.Name == "alpha" && s.Source == "plugin:golden" {
			sawPluginSkill = true
		}
	}
	if !sawPluginSkill {
		t.Fatalf("plugin skill missing from /api/skills: %+v", skillsResp.Skills)
	}

	// Disable → MCP config loses the server, skills lose the skill, list reflects state
	if rec := doJSON(t, mux, http.MethodPut, "/api/plugins/golden/enabled", map[string]bool{"enabled": false}); rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d", rec.Code)
	}
	var mcpCfgAfter mcpConfigResponse // fresh var: pluginServers is omitempty, decoding into the old one would keep stale entries
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil), &mcpCfgAfter)
	if len(mcpCfgAfter.PluginServers) != 0 {
		t.Fatalf("disabled plugin still bridged: %+v", mcpCfgAfter.PluginServers)
	}
	var skillsAfter struct {
		Skills []skillSupportItem `json:"skills"`
	}
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/skills", nil), &skillsAfter)
	for _, s := range skillsAfter.Skills {
		if s.Name == "alpha" {
			t.Fatalf("disabled plugin skill still listed: %+v", s)
		}
	}

	// Remove with purge → everything gone
	if rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/golden?purgeData=true", nil); rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(installRoot, "golden")); !os.IsNotExist(err) {
		t.Error("plugin dir must be gone")
	}
	decodeJSON(t, doJSON(t, mux, http.MethodGet, "/api/plugins", nil), &list)
	if len(list.Plugins) != 0 {
		t.Fatalf("plugins after remove = %+v", list.Plugins)
	}
}

func TestPluginsRemove_CorruptRegistryDoesNotBlock(t *testing.T) {
	mux, installRoot := newPluginsTestServer(t)
	writeTestPlugin(t, installRoot, "golden")
	writeTestRegistry(t, installRoot, `{not valid json`)

	rec := doJSON(t, mux, http.MethodDelete, "/api/plugins/golden", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(installRoot, "golden")); !os.IsNotExist(err) {
		t.Error("plugin dir must be removable despite corrupt registry")
	}
}
