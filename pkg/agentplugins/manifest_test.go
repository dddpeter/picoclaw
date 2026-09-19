package agentplugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalManifestJSON = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin"}`

func TestValidatePluginName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"my-plugin", false}, {"acme.tools", false}, {"lint3r", false}, {"a", false},
		{"a.b", false}, {"a.b.c", false},
		{"My-Plugin", true}, {"-start", true}, {"end-", true}, {".dot", true},
		{"has--double", true}, {"too.many..dots", true}, {"", true},
		{"has_underscore", true}, {"has space", true},
		{strings.Repeat("a", 65), true}, {strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		err := ValidatePluginName(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidatePluginName(%q) err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

// writeManifestDir creates a temp plugin root with the given plugin.json body.
func writeManifestDir(t *testing.T, pluginJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadManifest(t *testing.T) {
	cases := []struct {
		name      string
		plugin    string
		file      bool // write plugin.json at all
		wantErr   bool
		wantWarn  bool
		checkName string
	}{
		{
			name:      "minimal valid",
			plugin:    minimalManifestJSON,
			file:      true,
			checkName: "hello-plugin",
		},
		{
			name: "full valid",
			plugin: `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"plugin-name",
				"version":"1.2.0","description":"desc","author":{"name":"A","email":"a@example.com","url":"https://x.example.com"},
				"homepage":"https://docs.example.com","repository":"https://github.com/example/plugin","license":"MIT",
				"keywords":["k1","k2"],"extensions":{"com.example.client":{"setting":true}}}`,
			file:      true,
			checkName: "plugin-name",
		},
		{name: "missing schema", plugin: `{"name":"hello"}`, file: true, wantErr: true},
		{name: "missing name", plugin: `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"}`, file: true, wantErr: true},
		{name: "schema 1.1.0 rejected", plugin: `{"$schema":"https://agent-plugins.org/schemas/1.1.0/plugin.schema.json","name":"hello-plugin"}`, file: true, wantErr: true},
		{name: "invalid name", plugin: `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"Bad-Name"}`, file: true, wantErr: true},
		{
			name:     "unknown top-level field warns",
			plugin:   `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin","bogus":true}`,
			file:     true,
			wantWarn: true,
		},
		{
			name:    "author unknown subfield fatal",
			plugin:  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin","author":{"nick":"x"}}`,
			file:    true,
			wantErr: true,
		},
		{
			name:    "author string fatal",
			plugin:  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin","author":"someone"}`,
			file:    true,
			wantErr: true,
		},
		{
			name:    "keywords string fatal",
			plugin:  `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin","keywords":"k"}`,
			file:    true,
			wantErr: true,
		},
		{name: "not json", plugin: `not json`, file: true, wantErr: true},
		{name: "no plugin.json", file: false, wantErr: true},
		{
			name:     "extensions array warns only",
			plugin:   `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"hello-plugin","extensions":[1,2]}`,
			file:     true,
			wantWarn: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.file {
				if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(tc.plugin), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var r Report
			m, err := LoadManifest(dir, &r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadManifest(%q) expected error, got nil (m=%v)", tc.name, m)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadManifest(%q) unexpected error: %v", tc.name, err)
			}
			if tc.checkName != "" && m.Name != tc.checkName {
				t.Errorf("Name = %q, want %q", m.Name, tc.checkName)
			}
			if tc.wantWarn && len(r.Warnings) == 0 {
				t.Errorf("expected warning in report, got %v", r.Warnings)
			}
			if !tc.wantWarn && len(r.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", r.Warnings)
			}
		})
	}
}

func TestLoadManifestFullFields(t *testing.T) {
	dir := writeManifestDir(t, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"plugin-name",
		"version":"1.2.0","description":"d","license":"MIT","homepage":"https://h.example.com",
		"repository":"https://github.com/example/plugin","author":{"name":"A","email":"a@example.com","url":"https://u.example.com"},
		"keywords":["x","y"]}`)
	var r Report
	m, err := LoadManifest(dir, &r)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.0" || m.Description != "d" || m.License != "MIT" ||
		m.Homepage != "https://h.example.com" || m.Repository != "https://github.com/example/plugin" {
		t.Errorf("scalar fields mismatch: %+v", m)
	}
	if m.Author == nil || m.Author.Name != "A" || m.Author.Email != "a@example.com" || m.Author.URL != "https://u.example.com" {
		t.Errorf("author mismatch: %+v", m.Author)
	}
	if len(m.Keywords) != 2 || m.Keywords[0] != "x" {
		t.Errorf("keywords mismatch: %v", m.Keywords)
	}
	if m.SchemaURL != ManifestSchemaURL {
		t.Errorf("SchemaURL = %q", m.SchemaURL)
	}
	if !strings.HasPrefix(m.Name, "plugin") {
		t.Errorf("Name = %q", m.Name)
	}
}
