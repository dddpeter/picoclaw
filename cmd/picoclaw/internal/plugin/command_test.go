package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goldenPlugin(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "skills", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `","version":"1.0.0"}`,
		"mcp.json":    `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]}}}`,
	}
	skill := "---\nname: alpha\ndescription: test\n---\n\nbody\n"
	for file, body := range files {
		path := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skillPath := filepath.Join(root, "skills", "alpha", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

func executePluginCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewPluginCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestValidateCommand_Golden(t *testing.T) {
	root := t.TempDir()
	goldenPlugin(t, root, "golden")

	out, err := executePluginCommand(t, "validate", root)
	if err != nil {
		t.Fatalf("validate must succeed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "OK: golden") {
		t.Fatalf("output must contain OK line, got %q", out)
	}
	if !strings.Contains(out, "1 skills") || !strings.Contains(out, "1 mcp servers") {
		t.Fatalf("output must include component counts, got %q", out)
	}
}

func TestValidateCommand_Fatal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "plugin.json"),
		[]byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"Bad-Name"}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	_, err := executePluginCommand(t, "validate", root)
	if err == nil {
		t.Fatal("validate of an invalid plugin must fail")
	}
	if !strings.Contains(err.Error(), "Bad-Name") {
		t.Fatalf("error must name the reason, got %v", err)
	}
}

func TestValidateCommand_MissingPath(t *testing.T) {
	if _, err := executePluginCommand(t, "validate", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("validate of nonexistent path must fail")
	}
}
