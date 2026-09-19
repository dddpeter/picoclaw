package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/skills"
)

func installGoldenE2EPlugin(t *testing.T, installRoot, name string) string {
	t.Helper()
	root := filepath.Join(installRoot, name)
	if err := os.MkdirAll(filepath.Join(root, "skills", "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `","version":"1.0.0"}`,
		"mcp.json":    `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"echo":{"type":"stdio","command":"node","args":["-e","process.exit(0)"]}}}`,
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	skill := "---\nname: alpha\ndescription: plugin skill\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "skills", "alpha", "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPluginEndToEndLoadMergeRemove(t *testing.T) {
	installRoot := t.TempDir()
	dataRoot := filepath.Join(installRoot, "data")
	installGoldenE2EPlugin(t, installRoot, "golden")

	plugins, rep := agentplugins.LoadPluginsDir(installRoot, dataRoot)
	if len(plugins) != 1 || !plugins[0].Enabled {
		t.Fatalf("plugins = %+v (warnings %v)", plugins, rep.Warnings)
	}
	p := plugins[0]
	if len(p.Skills) != 1 || len(p.MCPServers) != 1 {
		t.Fatalf("components = %d skills, %d mcp", len(p.Skills), len(p.MCPServers))
	}

	loader := skills.NewSkillsLoaderFromRoots("", []skills.SkillRoot{
		{Dir: p.Root, Source: "plugin:" + p.Name, Kind: skills.SkillRootPlugin},
	})
	got := loader.ListSkills()
	if len(got) != 1 || got[0].Name != "alpha" || got[0].Source != "plugin:golden" {
		t.Fatalf("plugin skill through loader = %+v", got)
	}

	merged := mergePluginServersRoots(&config.MCPConfig{}, installRoot, dataRoot)
	if _, ok := merged.Servers["plugin/golden/echo"]; !ok {
		t.Fatalf("merged servers = %v", merged.Servers)
	}

	if err := agentplugins.Remove("golden", installRoot, true); err != nil {
		t.Fatal(err)
	}
	plugins2, _ := agentplugins.LoadPluginsDir(installRoot, dataRoot)
	if len(plugins2) != 0 {
		t.Fatalf("plugins after remove = %+v", plugins2)
	}
	merged2 := mergePluginServersRoots(&config.MCPConfig{}, installRoot, dataRoot)
	if _, ok := merged2.Servers["plugin/golden/echo"]; ok {
		t.Fatal("plugin server must disappear after remove")
	}
}
