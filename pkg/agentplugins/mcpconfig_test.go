package agentplugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mcpSchema = MCPConfigSchemaURL

// writeMCPConfig creates a temp plugin root containing the given mcp.json
// (and optionally a plugin.json so callers can vary the manifest version).
func writeMCPConfig(t *testing.T, mcpJSON, manifestSchema string) string {
	t.Helper()
	dir := t.TempDir()
	if manifestSchema == "" {
		manifestSchema = ManifestSchemaURL
	}
	manifest := `{"$schema":"` + manifestSchema + `","name":"mcp-plugin"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if mcpJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcpJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func loadMCP(t *testing.T, dir string) (map[string]MCPServerEntry, error, *Report) {
	t.Helper()
	var r Report
	v := Vars{Root: dir, Data: filepath.Join(filepath.Dir(dir), "data", filepath.Base(dir))}
	entries, err := LoadMCPConfig(dir, ManifestSchemaURL, v, &r)
	return entries, err, &r
}

func TestLoadMCPConfig_Missing(t *testing.T) {
	dir := writeMCPConfig(t, "", "")
	entries, err, r := loadMCP(t, dir)
	if err != nil {
		t.Fatalf("missing mcp.json must be silent (§6.2): %v", err)
	}
	if entries != nil {
		t.Fatalf("missing mcp.json must return nil map, got %v", entries)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("missing mcp.json must not warn, got %v", r.Warnings)
	}
}

func TestLoadMCPConfig_EmptyServers(t *testing.T) {
	dir := writeMCPConfig(t, `{"$schema":"`+mcpSchema+`","mcpServers":{}}`, "")
	entries, err, _ := loadMCP(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty mcpServers must yield empty map, got %v", entries)
	}
}

func TestLoadMCPConfig(t *testing.T) {
	cases := []struct {
		name        string
		mcp         string
		wantErr     bool   // whole-plugin MCP disabled
		wantSkipped string // server expected absent (entry-level skip)
		// optional checks on the surviving entry "ok"
		check func(t *testing.T, entries map[string]MCPServerEntry, dir string)
	}{
		{
			name: "stdio with placeholders and cwd",
			mcp: `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"stdio",
				"command":"./bin/validator","args":["--data","${PLUGIN_DATA}/x"],"cwd":"${PLUGIN_ROOT}"}}}`,
			check: func(t *testing.T, entries map[string]MCPServerEntry, dir string) {
				e := entries["ok"]
				if want := filepath.Join(dir, "bin", "validator"); e.Command != want {
					t.Errorf("Command = %q, want absolute %q", e.Command, want)
				}
				// Expansion is purely textual: ${PLUGIN_DATA} -> v.Data, the
				// literal "/x" suffix stays untouched.
				dataDir := filepath.Join(filepath.Dir(dir), "data", filepath.Base(dir))
				if want := dataDir + "/x"; e.Args[1] != want {
					t.Errorf("arg expansion = %q, want %q", e.Args[1], want)
				}
				if e.CWD != dir {
					t.Errorf("CWD = %q, want %q", e.CWD, dir)
				}
			},
		},
		{
			name: "bare command default cwd",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"stdio","command":"npx"}}}`,
			check: func(t *testing.T, entries map[string]MCPServerEntry, dir string) {
				e := entries["ok"]
				if e.Command != "npx" || e.CWD != dir {
					t.Errorf("bare command entry = %+v", e)
				}
			},
		},
		{
			name:        "command escapes root",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"../bin/server"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "command with space",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"node server.js"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "cwd bare relative",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","cwd":"data"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "cwd unknown placeholder",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","cwd":"${OTHER}/x"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "env reserved key",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","env":{"PLUGIN_ROOT":"x"}}}}`,
			wantSkipped: "bad",
		},
		{
			name: "streamable-http valid",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"streamable-http","url":"https://a.com/mcp","headers":{"X-Tenant":"t"}}}}`,
			check: func(t *testing.T, entries map[string]MCPServerEntry, dir string) {
				e := entries["ok"]
				if e.URL != "https://a.com/mcp" || e.Headers["X-Tenant"] != "t" {
					t.Errorf("remote entry = %+v", e)
				}
			},
		},
		{
			name:        "http non-loopback rejected",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"streamable-http","url":"http://a.com/mcp"}}}`,
			wantSkipped: "bad",
		},
		{
			name: "http loopback host allowed",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"streamable-http","url":"http://localhost:8080/mcp"}}}`,
		},
		{
			name: "http loopback IP literal allowed",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"streamable-http","url":"http://127.0.0.1:9000/x"}}}`,
		},
		{
			name:        "userinfo rejected",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"streamable-http","url":"https://u:p@a.com/mcp"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "fragment rejected",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"streamable-http","url":"https://a.com/mcp#frag"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "header case conflict rejected",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"streamable-http","url":"https://a.com/mcp","headers":{"X-Tenant":"a","x-tenant":"b"}}}}`,
			wantSkipped: "bad",
		},
		{
			name: "sse valid",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"sse","url":"https://legacy.example.com/sse"}}}`,
		},
		{
			name:    "schema version mismatch with manifest",
			mcp:     `{"$schema":"https://agent-plugins.org/schemas/1.1.0/mcp.schema.json","mcpServers":{}}`,
			wantErr: true,
		},
		{
			name:    "bad json",
			mcp:     `{oops`,
			wantErr: true,
		},
		{
			name:    "unknown top-level field",
			mcp:     `{"$schema":"` + mcpSchema + `","mcpServers":{},"bogus":1}`,
			wantErr: true,
		},
		{
			name:        "unknown transport type",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"websocket","url":"ws://a.com"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "missing type",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"command":"npx"}}}`,
			wantSkipped: "bad",
		},
		{
			name:    "mcpServers missing",
			mcp:     `{"$schema":"` + mcpSchema + `"}`,
			wantErr: true,
		},
		{
			name:    "schema missing",
			mcp:     `{"mcpServers":{}}`,
			wantErr: true,
		},
		{
			name:        "unknown field inside entry",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","bogus":1}}}`,
			wantSkipped: "bad",
		},
		{
			name: "cwd ./ form resolved",
			mcp:  `{"$schema":"` + mcpSchema + `","mcpServers":{"ok":{"type":"stdio","command":"npx","cwd":"./data"}}}`,
			check: func(t *testing.T, entries map[string]MCPServerEntry, dir string) {
				if got := entries["ok"].CWD; got != filepath.Join(dir, "data") {
					t.Errorf("CWD = %q, want %q", got, filepath.Join(dir, "data"))
				}
			},
		},
		{
			name:        "cwd ../ escape",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","cwd":"../data"}}}`,
			wantSkipped: "bad",
		},
		{
			name:        "env key case-variant reserved",
			mcp:         `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"npx","env":{"plugin_data":"x"}}}}`,
			wantSkipped: "bad",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeMCPConfig(t, tc.mcp, "")
			entries, err, r := loadMCP(t, dir)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected whole-config error, got entries=%v warnings=%v", entries, r.Warnings)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantSkipped != "" {
				if _, present := entries[tc.wantSkipped]; present {
					t.Fatalf("server %q must be skipped, warnings=%v", tc.wantSkipped, r.Warnings)
				}
				if len(r.Warnings) == 0 {
					t.Fatalf("skipped server must produce a warning")
				}
				return
			}
			if _, present := entries["ok"]; !present {
				t.Fatalf("server \"ok\" missing; warnings=%v", r.Warnings)
			}
			if tc.check != nil {
				tc.check(t, entries, dir)
			}
		})
	}
}

func TestLoadMCPConfig_PluginDataCWD(t *testing.T) {
	dir := writeMCPConfig(t, `{"$schema":"`+mcpSchema+`","mcpServers":{"ok":{"type":"stdio","command":"npx","cwd":"${PLUGIN_DATA}/run"}}}`, "")
	v := Vars{Root: dir, Data: filepath.Join(dir, "..", "data", "mcp-plugin")}
	// Data dir exists so containment can resolve it.
	if err := os.MkdirAll(v.Data, 0o755); err != nil {
		t.Fatal(err)
	}
	var r Report
	entries, err := LoadMCPConfig(dir, ManifestSchemaURL, v, &r)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Clean(filepath.Join(v.Data, "run"))
	if got := entries["ok"].CWD; got != want {
		t.Fatalf("CWD = %q, want %q (warnings: %v)", got, want, r.Warnings)
	}
}

func TestLoadMCPConfig_NotAFile(t *testing.T) {
	dir := writeMCPConfig(t, "", "")
	if err := os.MkdirAll(filepath.Join(dir, "mcp.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var r Report
	v := Vars{Root: dir, Data: filepath.Join(dir, "data")}
	entries, err := LoadMCPConfig(dir, ManifestSchemaURL, v, &r)
	if err != nil {
		t.Fatalf("non-regular mcp.json must not be a whole-config error: %v", err)
	}
	if entries != nil || len(r.Warnings) == 0 {
		t.Fatalf("non-regular mcp.json: entries=%v warnings=%v", entries, r.Warnings)
	}
}

func TestLoadMCPConfig_SymlinkEscapeCommand(t *testing.T) {
	dir := writeMCPConfig(t, "", "")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "server"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := os.Symlink(filepath.Join(outside, "server"), filepath.Join(dir, "bin", "escape"))
	if err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	mcp := `{"$schema":"` + mcpSchema + `","mcpServers":{"bad":{"type":"stdio","command":"./bin/escape"}}}`
	if werr := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcp), 0o644); werr != nil {
		t.Fatal(werr)
	}
	entries, lerr, r := loadMCP(t, dir)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, present := entries["bad"]; present {
		t.Fatalf("symlink-escaping command must be skipped, warnings=%v", r.Warnings)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("expected warning")
	}
	if !strings.Contains(r.Warnings[0], "bad") {
		t.Fatalf("warning should name the server, got %q", r.Warnings[0])
	}
}
