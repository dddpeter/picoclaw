package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
	mux.HandleFunc("PUT /api/plugins/{name}/enabled", h.handleSetPluginEnabled)
	mux.HandleFunc("DELETE /api/plugins/{name}", h.handleRemovePlugin)
}

// validatePluginNameSegment rejects path-traversal, reserved and
// spec-invalid plugin names with 400. ok=false means the response is written.
func validatePluginNameSegment(w http.ResponseWriter, name string) bool {
	if err := agentplugins.ValidatePluginName(name); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Invalid plugin name: %v", err)
		return false
	}
	if agentplugins.IsReservedInstallName(name) {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Plugin name %q collides with a reserved install-root path", name)
		return false
	}
	return true
}

// handleSetPluginEnabled toggles a registered plugin's enabled flag.
//
//	PUT /api/plugins/{name}/enabled  {"enabled": bool}
func (h *Handler) handleSetPluginEnabled(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validatePluginNameSegment(w, name) {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Request body must be {\"enabled\": bool}")
		return
	}

	installRoot, _, err := h.pluginsRoots()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to resolve plugin roots: %v", err)
		return
	}

	h.pluginsMu.Lock()
	defer h.pluginsMu.Unlock()

	reg, err := agentplugins.LoadRegistry(filepath.Join(installRoot, agentplugins.RegistryFileName))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to load registry: %v", err)
		return
	}
	if err := reg.SetEnabled(name, *req.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := reg.Save(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to save registry: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleRemovePlugin deletes the plugin directory (and optionally its data
// directory) plus the registry entry. A registry-only "ghost" entry is
// cleanable even when the directory is already gone.
//
//	DELETE /api/plugins/{name}[?purgeData=true]
func (h *Handler) handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validatePluginNameSegment(w, name) {
		return
	}
	purgeData := r.URL.Query().Get("purgeData") == "true"

	installRoot, _, err := h.pluginsRoots()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to resolve plugin roots: %v", err)
		return
	}

	h.pluginsMu.Lock()
	defer h.pluginsMu.Unlock()

	reg, regErr := agentplugins.LoadRegistry(filepath.Join(installRoot, agentplugins.RegistryFileName))
	if regErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to load registry: %v", regErr)
		return
	}
	_, statErr := os.Stat(filepath.Join(installRoot, name))
	dirExists := statErr == nil
	_, hasEntry := reg.Entries[name]
	if !dirExists && !hasEntry {
		http.Error(w, fmt.Sprintf("plugin %q is not installed", name), http.StatusNotFound)
		return
	}

	if dirExists {
		if err := agentplugins.Remove(name, installRoot, purgeData); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			writeErrorf(w, "Failed to remove plugin: %v", err)
			return
		}
	}
	delete(reg.Entries, name)
	if err := reg.Save(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to save registry: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// handleListPlugins returns the merged registry + scan view of installed
// plugins, including directories that failed to load (loadError) and registry
// entries whose directory is gone.
//
//	GET /api/plugins
func (h *Handler) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	installRoot, dataRoot, err := h.pluginsRoots()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
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
