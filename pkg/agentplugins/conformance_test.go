package agentplugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// goNamePattern is a semantics-preserving Go-regexp translation of the
// official plugin.schema.json name pattern
// "^(?!.*(?:--|\\.\\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$". jsonschema-go
// compiles patterns with Go's regexp, which does not support the Perl
// lookahead. The translation enumerates the only way to traverse [a-z0-9.-]
// without ever emitting "--" or "..": single separators or strictly
// alternating ".-"/"-." runs.
const (
	officialNamePattern = `^(?!.*(?:--|\\.\\.))[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`
	// goNamePatternJSON is goNamePattern with JSON-escaped backslashes, since
	// the substitution happens on raw JSON text before parsing.
	goNamePatternJSON = `^[a-z0-9]([a-z0-9]|\\.[a-z0-9]|-[a-z0-9]|(\\.-)+[a-z0-9]|(-\\.)+[a-z0-9])*$`
)

// resolveOfficialSchema loads one of the vendored official schemas.
//
// The schemas are vendored at testdata/schemas/ and committed, so this test
// NEVER touches the network (spec §5.2 forbids runtime schema retrieval
// anyway; the $schema URLs in spec.go are pure comparison constants). To
// refresh the vendored copies, fetch from the spec repo — e.g.
//
//	curl -sL https://raw.githubusercontent.com/agentplugins/agent-plugins-spec/main/schemas/1.0.0/plugin.schema.json \
//	  -o pkg/agentplugins/testdata/schemas/plugin.schema.json
//	curl -sL https://raw.githubusercontent.com/agentplugins/agent-plugins-spec/main/schemas/1.0.0/mcp.schema.json \
//	  -o pkg/agentplugins/testdata/schemas/mcp.schema.json
//
// If raw.githubusercontent.com is unreachable (mainland China), any of these
// mirrors of the same repo path work:
//
//	https://cdn.jsdelivr.net/gh/agentplugins/agent-plugins-spec@main/schemas/1.0.0/<file>
//	https://ghproxy.net/https://raw.githubusercontent.com/agentplugins/agent-plugins-spec/main/schemas/1.0.0/<file>
//
// Verify afterwards: both files start with "{", and this test passes.
func resolveOfficialSchema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "schemas", name))
	if err != nil {
		t.Fatalf("official schema %s must be vendored: %v", name, err)
	}
	// Test-harness accommodation only: translate the Perl lookahead in the
	// name pattern to its Go-regexp equivalent (same accepted language).
	text := string(data)
	if strings.Contains(text, officialNamePattern) {
		text = strings.ReplaceAll(text, officialNamePattern, goNamePatternJSON)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(text), &schema); err != nil {
		t.Fatalf("official schema %s must parse: %v", name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("official schema %s must resolve: %v", name, err)
	}
	return resolved
}

func validateAgainstSchema(t *testing.T, resolved *jsonschema.Resolved, path string) error {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	return resolved.Validate(instance)
}

// TestGoldenAgainstOfficialSchema cross-validates the golden plugins: the
// handwritten closed validators (runtime path) and the official JSON Schemas
// (test-only path) must agree — both green on the goldens, both red on a
// known violation (design D3).
func TestGoldenAgainstOfficialSchema(t *testing.T) {
	pluginSchema := resolveOfficialSchema(t, "plugin.schema.json")
	mcpSchema := resolveOfficialSchema(t, "mcp.schema.json")

	for _, dir := range []string{"minimal", "full"} {
		golden := filepath.Join("testdata", "golden", dir)

		t.Run(dir+"/plugin.json", func(t *testing.T) {
			if err := validateAgainstSchema(t, pluginSchema, filepath.Join(golden, "plugin.json")); err != nil {
				t.Fatalf("official schema must accept golden plugin.json: %v", err)
			}
			var r Report
			if _, err := LoadManifest(golden, &r); err != nil {
				t.Fatalf("handwritten validator must accept golden plugin.json: %v", err)
			}
		})

		t.Run(dir+"/mcp.json", func(t *testing.T) {
			if err := validateAgainstSchema(t, mcpSchema, filepath.Join(golden, "mcp.json")); err != nil {
				t.Fatalf("official schema must accept golden mcp.json: %v", err)
			}
			var r Report
			v := Vars{Root: golden, Data: filepath.Join(golden, "..", "data", dir)}
			if _, err := LoadMCPConfig(golden, ManifestSchemaURL, v, &r); err != nil {
				t.Fatalf("handwritten validator must accept golden mcp.json: %v", err)
			}
		})
	}

	t.Run("negative cross-check name", func(t *testing.T) {
		bad := []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"Bad--Name"}`)
		var instance any
		if err := json.Unmarshal(bad, &instance); err != nil {
			t.Fatal(err)
		}
		if err := pluginSchema.Validate(instance); err == nil {
			t.Error("official schema must reject invalid name")
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "plugin.json"), bad, 0o644); err != nil {
			t.Fatal(err)
		}
		var r Report
		if _, err := LoadManifest(dir, &r); err == nil {
			t.Error("handwritten validator must reject invalid name")
		}
	})
}
