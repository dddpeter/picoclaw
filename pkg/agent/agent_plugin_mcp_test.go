package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	pluginManifestSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	pluginMCPSchema      = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

// writeGoldenMCPPlugin creates a temp plugin with one stdio and one
// streamable-http MCP server.
func writeGoldenMCPPlugin(t *testing.T, installRoot, name string) string {
	t.Helper()
	root := filepath.Join(installRoot, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"$schema":"` + pluginManifestSchema + `","name":"` + name + `","version":"1.0.0"}`
	mcpJSON := `{"$schema":"` + pluginMCPSchema + `","mcpServers":{` +
		`"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]},` +
		`"remote":{"type":"streamable-http","url":"https://deploy.example.com/mcp"}` +
		`}}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mcp.json"), []byte(mcpJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPluginMCP_MergeBasic(t *testing.T) {
	installRoot := t.TempDir()
	dataRoot := filepath.Join(installRoot, "data")
	root := writeGoldenMCPPlugin(t, installRoot, "golden")

	cfg := &config.MCPConfig{}
	merged := mergePluginServersRoots(cfg, installRoot, dataRoot)

	echo, ok := merged.Servers["plugin/golden/echo"]
	if !ok {
		t.Fatalf("plugin server key missing: %v", merged.Servers)
	}
	if !echo.Enabled {
		t.Error("bridged server must be enabled")
	}
	if echo.Command != "node" {
		t.Errorf("Command = %q", echo.Command)
	}
	if echo.Dir != root { // default cwd = plugin root (§7.2.1 MUST)
		t.Errorf("Dir = %q, want plugin root %q", echo.Dir, root)
	}
	if echo.PluginRoot != root {
		t.Errorf("PluginRoot = %q, want %q", echo.PluginRoot, root)
	}
	wantData := filepath.Join(dataRoot, "golden")
	if echo.PluginData != wantData {
		t.Errorf("PluginData = %q, want %q", echo.PluginData, wantData)
	}

	remote, ok := merged.Servers["plugin/golden/remote"]
	if !ok || remote.URL != "https://deploy.example.com/mcp" || remote.Type != "streamable-http" {
		t.Errorf("remote bridged entry = %+v", remote)
	}

	// §9.1 MUST: PLUGIN_DATA dir created before launch.
	if st, err := os.Stat(wantData); err != nil || !st.IsDir() {
		t.Errorf("plugin data dir must be created, err=%v", err)
	}

	if len(cfg.Servers) != 0 {
		t.Errorf("input config must not be mutated, got %v", cfg.Servers)
	}
}

func TestPluginMCP_UserKeyNotOverwritten(t *testing.T) {
	installRoot := t.TempDir()
	dataRoot := filepath.Join(installRoot, "data")
	writeGoldenMCPPlugin(t, installRoot, "golden")

	cfg := &config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"plugin/golden/echo": {Enabled: true, Command: "user-manual-cmd"},
	}}
	merged := mergePluginServersRoots(cfg, installRoot, dataRoot)
	if got := merged.Servers["plugin/golden/echo"].Command; got != "user-manual-cmd" {
		t.Fatalf("user-configured key must win, got %q", got)
	}
}

func TestPluginMCP_AllowlistInteraction(t *testing.T) {
	installRoot := t.TempDir()
	dataRoot := filepath.Join(installRoot, "data")
	writeGoldenMCPPlugin(t, installRoot, "golden")

	cfg := mergePluginServersRoots(&config.MCPConfig{}, installRoot, dataRoot)

	allowed := map[string]struct{}{"plugin/golden/echo": {}}
	filtered := filterMCPConfigServers(*cfg, allowed)
	if _, ok := filtered.Servers["plugin/golden/echo"]; !ok {
		t.Fatal("allowlisted plugin server must pass the filter")
	}

	denied := filterMCPConfigServers(*cfg, map[string]struct{}{"other": {}})
	if _, ok := denied.Servers["plugin/golden/echo"]; ok {
		t.Fatal("plugin server must be filtered out when not allowlisted")
	}
}
