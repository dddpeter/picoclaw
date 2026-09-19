package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/agentplugins"
)

// pluginDTO is one plugin row for the /plugins dashboard page: a merge of
// the filesystem scan (ScanPlugins) and the registry entry when present.
type pluginDTO struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Source      string   `json:"source,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	InstalledAt string   `json:"installedAt,omitempty"`
	Enabled     bool     `json:"enabled"`
	Registered  bool     `json:"registered"`
	LoadError   string   `json:"loadError,omitempty"`
	Skills      int      `json:"skills"`
	MCPServers  int      `json:"mcpServers"`
	Warnings    []string `json:"warnings,omitempty"`
}

type pluginsResponse struct {
	InstallRoot   string      `json:"installRoot"`
	RegistryError string      `json:"registryError,omitempty"`
	Plugins       []pluginDTO `json:"plugins"`
}

// pluginsRoots resolves the plugin install/data roots, preferring injected
// test roots over the user defaults.
func (h *Handler) pluginsRoots() (installRoot, dataRoot string, err error) {
	h.pluginsMu.Lock()
	injectedInstall, injectedData := h.pluginsInstallRoot, h.pluginsDataRoot
	h.pluginsMu.Unlock()
	if injectedInstall != "" {
		return injectedInstall, injectedData, nil
	}
	installRoot, err = agentplugins.DefaultInstallRoot()
	if err != nil {
		return "", "", err
	}
	dataRoot, err = agentplugins.DefaultDataRoot()
	if err != nil {
		return "", "", err
	}
	return installRoot, dataRoot, nil
}

// setPluginsRoots overrides the plugin install/data roots (tests only).
func (h *Handler) setPluginsRoots(installRoot, dataRoot string) {
	h.pluginsMu.Lock()
	defer h.pluginsMu.Unlock()
	h.pluginsInstallRoot = installRoot
	h.pluginsDataRoot = dataRoot
}

// registerPluginRoutes binds Agent Plugins management endpoints to the mux.
func (h *Handler) registerPluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/plugins", h.handleListPlugins)
}

// handleListPlugins returns the merged registry + scan view of installed
// plugins, including directories that failed to load (loadError) and registry
// entries whose directory is gone.
//
//	GET /api/plugins
func (h *Handler) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	installRoot, dataRoot, err := h.pluginsRoots()
	if err != nil {
		writeErrorf(w, "Failed to resolve plugin roots: %v", err)
		return
	}

	resp := pluginsResponse{InstallRoot: installRoot, Plugins: []pluginDTO{}}
	registry, regErr := agentplugins.LoadRegistry(filepath.Join(installRoot, agentplugins.RegistryFileName))
	if regErr != nil {
		// Tolerate a corrupt registry: the scan still works with
		// default-enabled semantics; surface the problem to the UI.
		resp.RegistryError = regErr.Error()
		registry = nil
	}

	byName := make(map[string]*pluginDTO)
	add := func(dto pluginDTO) {
		byName[dto.Name] = &dto
		resp.Plugins = append(resp.Plugins, dto)
	}

	plugins, failed := agentplugins.ScanPlugins(installRoot, dataRoot)
	for _, p := range plugins {
		dto := pluginDTO{
			Name:       p.Name,
			Version:    p.Version,
			Enabled:    p.Enabled,
			Skills:     len(p.Skills),
			MCPServers: len(p.MCPServers),
		}
		if p.Report != nil {
			dto.Warnings = p.Report.Warnings
		}
		add(dto)
	}
	for _, f := range failed {
		add(pluginDTO{Name: f.Name, Enabled: true, LoadError: f.Err.Error()})
	}
	if registry != nil {
		for name, entry := range registry.Entries {
			if _, ok := byName[name]; !ok {
				add(pluginDTO{
					Name:      name,
					Version:   entry.Version,
					Source:    entry.Source,
					Ref:       entry.Ref,
					Enabled:   entry.Enabled,
					LoadError: "installed but directory missing",
				})
			}
		}
	}

	// Registry metadata wins where present; the registry is the source of
	// truth for enabled/installed metadata.
	if registry != nil {
		for i := range resp.Plugins {
			p := &resp.Plugins[i]
			entry, ok := registry.Entries[p.Name]
			if !ok {
				continue
			}
			p.Registered = true
			p.Enabled = entry.Enabled
			if entry.Version != "" {
				p.Version = entry.Version
			}
			if entry.Source != "" {
				p.Source = entry.Source
			}
			if entry.Ref != "" {
				p.Ref = entry.Ref
			}
			if !entry.InstalledAt.IsZero() {
				p.InstalledAt = entry.InstalledAt.UTC().Format(time.RFC3339)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
