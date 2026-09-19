package agentplugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Plugin is a fully loaded plugin package with its discovered components.
type Plugin struct {
	Name       string
	Version    string
	Root       string // filesystem-resolved plugin root (= PLUGIN_ROOT)
	Manifest   *Manifest
	Skills     []PluginSkill
	MCPServers map[string]MCPServerEntry
	Report     *Report
	Enabled    bool
}

// registryFileName is the CLI-maintained state file inside the install root.
// Implicitly read at load time: a missing file (or a missing entry for a
// plugin directory) means enabled — installed is trusted by default.
const registryFileName = "registry.json"

// LoadPlugin loads and validates one plugin from its root directory.
// dataDir is the client-managed PLUGIN_DATA for this plugin instance
// (<dataRoot>/<plugin name>, assembled by the caller). A non-nil error means
// the whole plugin is rejected; component-level issues land in Plugin.Report.
func LoadPlugin(root string, dataDir string, enabled bool) (*Plugin, error) {
	rep := &Report{Warnings: []string{}}

	// Resolve the plugin root to its filesystem-real location so PLUGIN_ROOT
	// and all containment checks use the resolved root (spec §4.1).
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("plugin root does not resolve: %w", err)
	}

	// §4.1.1: plugin.json itself must resolve within the plugin root.
	manifestPath := filepath.Join(root, "plugin.json")
	if !Contains(resolved, manifestPath) {
		return nil, fmt.Errorf("plugin.json resolves outside the plugin root")
	}

	m, err := LoadManifest(resolved, rep)
	if err != nil {
		return nil, err
	}

	p := &Plugin{
		Name:     m.Name,
		Version:  m.Version,
		Root:     resolved,
		Manifest: m,
		Report:   rep,
		Enabled:  enabled,
	}
	if !enabled {
		// Disabled: no component discovery at all.
		return p, nil
	}

	p.Skills = DiscoverSkills(resolved, rep)

	v := Vars{Root: resolved, Data: dataDir}
	entries, err := LoadMCPConfig(resolved, m.SchemaURL, v, rep)
	if err != nil {
		// §7.2.2.2 / §10.1: invalid, unsupported or version-mismatched
		// mcp.json disables plugin MCP but other components keep loading.
		rep.Warnf("MCP disabled for plugin %q: %v", m.Name, err)
		p.MCPServers = map[string]MCPServerEntry{}
	} else {
		p.MCPServers = entries
	}

	return p, nil
}

// registryEnabledEntry is the per-plugin slice of registry.json this package
// needs at load time. A missing file or entry means enabled.
type registryEnabledEntry struct {
	Enabled *bool `json:"enabled"`
}

// readEnabledMap reads <installRoot>/registry.json into name→enabled. Any
// read/parse problem yields nil (everything enabled, default-trust).
func readEnabledMap(installRoot string) map[string]registryEnabledEntry {
	data, err := os.ReadFile(filepath.Join(installRoot, registryFileName))
	if err != nil {
		return nil
	}
	var m map[string]registryEnabledEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// LoadPluginsDir scans every subdirectory of the install root, loading each
// as a plugin. Bad directories are skipped and reported. A disabled registry
// entry yields Plugin{Enabled:false} without component discovery. A missing
// install root yields an empty result silently.
func LoadPluginsDir(installRoot, dataRoot string) ([]*Plugin, *Report) {
	rep := &Report{Warnings: []string{}}

	entries, err := os.ReadDir(installRoot)
	if err != nil {
		return nil, rep // missing install root: silent (analogous to §6.2)
	}

	enabledMap := readEnabledMap(installRoot)
	enabledFor := func(name string) bool {
		if enabledMap == nil {
			return true
		}
		if e, ok := enabledMap[name]; ok && e.Enabled != nil {
			return *e.Enabled
		}
		return true
	}

	var plugins []*Plugin
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // stray files are not plugins
		}
		name := entry.Name()
		dir := filepath.Join(installRoot, name)

		// Reserved install-root entries are never plugins.
		if name == "data" || name == registryFileName {
			continue
		}

		enabled := enabledFor(name)
		p, err := LoadPlugin(dir, filepath.Join(dataRoot, name), enabled)
		if err != nil {
			rep.Warnf("plugin %q skipped: %v", name, err)
			continue
		}
		plugins = append(plugins, p)
	}
	return plugins, rep
}
