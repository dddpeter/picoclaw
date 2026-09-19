package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	mux.HandleFunc("POST /api/plugins/validate", h.handleValidatePlugin)
	mux.HandleFunc("POST /api/plugins/install", h.handleInstallPlugin)
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

// pluginReportResponse is the shared shape of validate/install results: the
// load outcome plus diagnostics. Install success with a load failure keeps
// OK=true and fills Error (same semantics as the CLI).
type pluginReportResponse struct {
	OK         bool     `json:"ok"`
	Name       string   `json:"name,omitempty"`
	Version    string   `json:"version,omitempty"`
	Target     string   `json:"target,omitempty"`
	Skills     int      `json:"skills"`
	MCPServers int      `json:"mcpServers"`
	Warnings   []string `json:"warnings,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// loadPluginForReport re-loads an installed plugin to fill a report response.
func loadPluginForReport(target, dataRoot string) (*agentplugins.Plugin, error) {
	var rep agentplugins.Report
	m, err := agentplugins.LoadManifest(target, &rep)
	if err != nil {
		return nil, err
	}
	return agentplugins.LoadPlugin(target, filepath.Join(dataRoot, m.Name), true)
}

func reportFromPlugin(p *agentplugins.Plugin) pluginReportResponse {
	resp := pluginReportResponse{OK: true, Name: p.Name, Version: p.Version, Skills: len(p.Skills), MCPServers: len(p.MCPServers)}
	if p.Report != nil {
		resp.Warnings = p.Report.Warnings
	}
	return resp
}

// handleValidatePlugin runs the full spec load path on a directory without
// installing anything.
//
//	POST /api/plugins/validate  {"path": "..."}
func (h *Handler) handleValidatePlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Request body must be {\"path\": string}")
		return
	}
	if st, err := os.Stat(req.Path); err != nil || !st.IsDir() {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Path %q is not an accessible directory", req.Path)
		return
	}
	dataDir := filepath.Join(os.TempDir(), "picoclaw-plugin-validate", "data")
	p, err := agentplugins.LoadPlugin(req.Path, dataDir, true)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pluginReportResponse{OK: false, Error: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reportFromPlugin(p))
}

// handleInstallPlugin installs a plugin from a local directory or git URL and
// registers it (install ⇒ enabled).
//
//	POST /api/plugins/install  {"source": "<path|git-url>", "ref": "<branch|tag>"}
func (h *Handler) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Ref    string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Request body must be {\"source\": string}")
		return
	}
	installRoot, dataRoot, err := h.pluginsRoots()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Failed to resolve plugin roots: %v", err)
		return
	}

	h.pluginsMu.Lock()
	defer h.pluginsMu.Unlock()

	var target string
	if st, statErr := os.Stat(req.Source); statErr == nil && st.IsDir() {
		target, err = agentplugins.InstallFromLocal(req.Source, installRoot)
	} else if strings.HasPrefix(req.Source, "https://") || strings.HasPrefix(req.Source, "git@") || strings.HasPrefix(req.Source, "file://") {
		target, err = agentplugins.InstallFromGit(req.Source, req.Ref, installRoot)
	} else {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Source %q is neither an existing directory nor a git URL", req.Source)
		return
	}
	if err != nil {
		// Invalid source package / refused overwrite — a user-input problem.
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "Install failed: %v", err)
		return
	}

	// Register (install ⇒ enabled).
	reg, regErr := agentplugins.LoadRegistry(filepath.Join(installRoot, agentplugins.RegistryFileName))
	if regErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Installed at %s, but the registry failed to load: %v", target, regErr)
		return
	}
	var probe agentplugins.Report
	m, mErr := agentplugins.LoadManifest(target, &probe)
	if mErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Installed at %s, but the manifest became unreadable: %v", target, mErr)
		return
	}
	agentplugins.RegisterIn(reg, m.Name, m.Version, req.Source, req.Ref)
	if err := reg.Save(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "Installed at %s, but saving the registry failed: %v", target, err)
		return
	}

	resp := pluginReportResponse{Target: target, Name: m.Name, Version: m.Version}
	if p, loadErr := loadPluginForReport(target, dataRoot); loadErr != nil {
		resp.OK = true
		resp.Error = loadErr.Error()
	} else {
		resp.OK = true
		resp.Name = p.Name
		resp.Version = p.Version
		resp.Skills = len(p.Skills)
		resp.MCPServers = len(p.MCPServers)
		if p.Report != nil {
			resp.Warnings = p.Report.Warnings
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
