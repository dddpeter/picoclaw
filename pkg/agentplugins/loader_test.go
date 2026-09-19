package agentplugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeGoldenPlugin writes a minimal-but-complete plugin: manifest, one skill,
// one stdio MCP server.
func makeGoldenPlugin(t *testing.T, root string) {
	t.Helper()
	writeSkill(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), "alpha")
	files := map[string]string{
		"plugin.json": `{"$schema":"` + ManifestSchemaURL + `","name":"golden","version":"1.0.0"}`,
		"mcp.json":    `{"$schema":"` + MCPConfigSchemaURL + `","mcpServers":{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadPlugin(t *testing.T) {
	t.Run("golden plugin components loaded", func(t *testing.T) {
		root := t.TempDir()
		makeGoldenPlugin(t, root)
		dataDir := filepath.Join(t.TempDir(), "data", "golden")
		p, err := LoadPlugin(root, dataDir, true)
		if err != nil {
			t.Fatal(err)
		}
		if p.Name != "golden" || p.Version != "1.0.0" {
			t.Errorf("identity mismatch: %+v", p)
		}
		if len(p.Skills) != 1 || p.Skills[0].Name != "alpha" {
			t.Errorf("skills = %v", p.Skills)
		}
		if len(p.MCPServers) != 1 {
			t.Fatalf("mcp servers = %v", p.MCPServers)
		}
		echo := p.MCPServers["echo"]
		if echo.CWD == "" || echo.CWD != p.Root {
			t.Errorf("default cwd must be the resolved plugin root: cwd=%q root=%q", echo.CWD, p.Root)
		}
		if !p.Enabled {
			t.Error("Enabled must be true")
		}
		if !p.Report.OK() {
			t.Errorf("unexpected diagnostics: %v", p.Report)
		}
	})

	t.Run("manifest fatal rejects plugin", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "plugin.json"),
			[]byte(`{"$schema":"`+ManifestSchemaURL+`","name":"Bad-Name"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPlugin(root, filepath.Join(t.TempDir(), "d"), true); err == nil {
			t.Fatal("expected error for fatal manifest")
		}
	})

	t.Run("mcp schema mismatch disables MCP but keeps skills", func(t *testing.T) {
		root := t.TempDir()
		makeGoldenPlugin(t, root)
		bad := `{"$schema":"https://agent-plugins.org/schemas/1.1.0/mcp.schema.json","mcpServers":{}}`
		if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := LoadPlugin(root, filepath.Join(t.TempDir(), "d"), true)
		if err != nil {
			t.Fatalf("mcp.json version mismatch must not reject the whole plugin: %v", err)
		}
		if len(p.MCPServers) != 0 {
			t.Errorf("plugin MCP must be disabled, got %v", p.MCPServers)
		}
		if len(p.Skills) != 1 {
			t.Errorf("skills must still load, got %v", p.Skills)
		}
		if len(p.Report.Warnings) == 0 {
			t.Errorf("expected warning, got %+v", p.Report)
		}
	})

	t.Run("plugin.json symlink escape rejected", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		realManifest := filepath.Join(outside, "plugin.json")
		if err := os.WriteFile(realManifest,
			[]byte(`{"$schema":"`+ManifestSchemaURL+`","name":"golden"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		err := os.Symlink(realManifest, filepath.Join(root, "plugin.json"))
		if err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := LoadPlugin(root, filepath.Join(t.TempDir(), "d"), true); err == nil {
			t.Fatal("escaping plugin.json must reject the plugin")
		}
	})
}

func TestLoadPluginsDir(t *testing.T) {
	t.Run("mixed install root", func(t *testing.T) {
		installRoot := t.TempDir()
		dataRoot := filepath.Join(installRoot, "data")
		makeGoldenPlugin(t, filepath.Join(installRoot, "golden"))

		// bad directory: fatal manifest
		bad := filepath.Join(installRoot, "bad")
		if err := os.MkdirAll(bad, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bad, "plugin.json"),
			[]byte(`{"$schema":"`+ManifestSchemaURL+`","name":"Bad-Name"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		// non-directory entry
		if err := os.WriteFile(filepath.Join(installRoot, "stray.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		plugins, rep := LoadPluginsDir(installRoot, dataRoot)
		if len(plugins) != 1 || plugins[0].Name != "golden" {
			t.Fatalf("plugins = %+v, want only golden", plugins)
		}
		if len(rep.Warnings) == 0 {
			t.Fatal("bad directory must be reported")
		}
		found := false
		for _, w := range rep.Warnings {
			if strings.Contains(w, "bad") {
				found = true
			}
		}
		if !found {
			t.Errorf("warning must name the bad dir: %v", rep.Warnings)
		}
	})

	t.Run("missing install root is silent", func(t *testing.T) {
		plugins, rep := LoadPluginsDir(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "data"))
		if plugins != nil {
			t.Errorf("plugins = %v", plugins)
		}
		if !rep.OK() {
			t.Errorf("unexpected report: %v", rep)
		}
	})

	t.Run("registry disabled entry skips components", func(t *testing.T) {
		installRoot := t.TempDir()
		makeGoldenPlugin(t, filepath.Join(installRoot, "golden"))
		registry := `{"golden":{"enabled":false}}`
		if err := os.WriteFile(filepath.Join(installRoot, "registry.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		plugins, _ := LoadPluginsDir(installRoot, filepath.Join(installRoot, "data"))
		if len(plugins) != 1 {
			t.Fatalf("plugins = %+v", plugins)
		}
		p := plugins[0]
		if p.Enabled {
			t.Error("Enabled must be false")
		}
		if len(p.Skills) != 0 || len(p.MCPServers) != 0 {
			t.Errorf("disabled plugin must not load components: skills=%v mcp=%v", p.Skills, p.MCPServers)
		}
	})

	t.Run("registry absent defaults to enabled", func(t *testing.T) {
		installRoot := t.TempDir()
		makeGoldenPlugin(t, filepath.Join(installRoot, "golden"))
		plugins, _ := LoadPluginsDir(installRoot, filepath.Join(installRoot, "data"))
		if len(plugins) != 1 || !plugins[0].Enabled {
			t.Fatalf("plugins = %+v, want golden enabled", plugins)
		}
	})
}
