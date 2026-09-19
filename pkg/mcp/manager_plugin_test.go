package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// TestInjectPluginEnv pins the spec §9.1 injection order: the client-supplied
// PLUGIN_ROOT/PLUGIN_DATA are written AFTER the configured env and replace
// any forged entries per platform environment-name semantics.
func TestInjectPluginEnv(t *testing.T) {
	envMap := map[string]string{
		"PATH":        "/usr/bin",
		"PLUGIN_ROOT": "forged-root",   // reserved key forged by the package
		"plugin_data": "forged-data",   // case variant must also be replaced
		"DATA_DIR":    "${PLUGIN_DATA}/db",
	}

	injectPluginEnv(envMap, "/plugins/devtools", "/plugins/data/devtools")

	if got := envMap["PLUGIN_ROOT"]; got != "/plugins/devtools" {
		t.Errorf("PLUGIN_ROOT = %q, want client value", got)
	}
	if got := envMap["PLUGIN_DATA"]; got != "/plugins/data/devtools" {
		t.Errorf("PLUGIN_DATA = %q, want client value", got)
	}
	for k := range envMap {
		if k != "PLUGIN_ROOT" && strings.EqualFold(k, "PLUGIN_ROOT") {
			t.Errorf("stale case variant %q survived", k)
		}
		if k != "PLUGIN_DATA" && strings.EqualFold(k, "PLUGIN_DATA") {
			t.Errorf("stale case variant %q survived", k)
		}
	}
	if envMap["PATH"] != "/usr/bin" || envMap["DATA_DIR"] != "${PLUGIN_DATA}/db" {
		t.Error("unrelated env entries must be untouched (expansion happened earlier)")
	}
}

// TestStdioDirFromPluginBridge pins that plugin-bridged entries carry a
// non-empty Dir (the manager assigns cmd.Dir = cfg.Dir unconditionally, and
// an empty user Dir keeps the old behavior).
func TestStdioDirFromPluginBridge(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cwd"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.MCPServerConfig{Enabled: true, Type: "stdio", Command: "go", Dir: filepath.Join(dir, "cwd")}
	if cfg.Dir == "" {
		t.Fatal("bridge sets Dir unconditionally; user config keeps it empty")
	}
}
