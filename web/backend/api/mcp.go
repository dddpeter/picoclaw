package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	mcp "github.com/sipeed/picoclaw/pkg/mcp"
)

// ---- MCP DTOs（camelCase，与 web 前端共享形状；见 docs/design/web-mcp-page-design.zh.md §4）----

type mcpDiscoveryDTO struct {
	Enabled          bool `json:"enabled"`
	TTLSeconds       int  `json:"ttlSeconds"`
	MaxSearchResults int  `json:"maxSearchResults"`
	UseBM25          bool `json:"useBM25"`
	UseRegex         bool `json:"useRegex"`
}

type mcpServerDTO struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Enabled  bool              `json:"enabled"`
	Deferred *bool             `json:"deferred"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	EnvFile  string            `json:"envFile"`
}

type mcpConfigResponse struct {
	Enabled            bool            `json:"enabled"`
	MaxInlineTextChars int             `json:"maxInlineTextChars"`
	Discovery          mcpDiscoveryDTO `json:"discovery"`
	Servers            []mcpServerDTO  `json:"servers"`
}

type mcpConfigRequest struct {
	Enabled            bool            `json:"enabled"`
	MaxInlineTextChars int             `json:"maxInlineTextChars"`
	Discovery          mcpDiscoveryDTO `json:"discovery"`
	Servers            []mcpServerDTO  `json:"servers"`
}

// registerMCPRoutes binds MCP management endpoints to the ServeMux.
func (h *Handler) registerMCPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp/config", h.handleGetMCPConfig)
	mux.HandleFunc("PUT /api/mcp/config", h.handlePutMCPConfig)
	mux.HandleFunc("POST /api/mcp/servers/test", h.handleTestMCPServer)
	mux.HandleFunc("GET /api/mcp/status", h.handleGetMCPStatus)
}

// normalizeMCPServerDisplayType maps stored server type to the UI-facing
// three-value set. Empty type falls back by command/url presence (the same
// defaults the gateway applies); "streamable-http" is equivalent to "http".
func normalizeMCPServerDisplayType(s config.MCPServerConfig) string {
	switch s.Type {
	case "sse":
		return "sse"
	case "http", "streamable-http":
		return "http"
	case "stdio":
		return "stdio"
	}
	if s.Command != "" {
		return "stdio"
	}
	if s.URL != "" {
		return "sse"
	}
	return "stdio"
}

func buildMCPConfigResponse(cfg *config.Config) mcpConfigResponse {
	mcpCfg := cfg.Tools.MCP
	resp := mcpConfigResponse{
		Enabled:            mcpCfg.Enabled,
		MaxInlineTextChars: mcpCfg.MaxInlineTextChars,
		Discovery: mcpDiscoveryDTO{
			Enabled:          mcpCfg.Discovery.Enabled,
			TTLSeconds:       mcpCfg.Discovery.TTL,
			MaxSearchResults: mcpCfg.Discovery.MaxSearchResults,
			UseBM25:          mcpCfg.Discovery.UseBM25,
			UseRegex:         mcpCfg.Discovery.UseRegex,
		},
		Servers: []mcpServerDTO{},
	}
	names := make([]string, 0, len(mcpCfg.Servers))
	for name := range mcpCfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		s := mcpCfg.Servers[name]
		args := s.Args
		if args == nil {
			args = []string{}
		}
		env := s.Env
		if env == nil {
			env = map[string]string{}
		}
		headers := s.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		resp.Servers = append(resp.Servers, mcpServerDTO{
			Name:     name,
			Type:     normalizeMCPServerDisplayType(s),
			Enabled:  s.Enabled,
			Deferred: s.Deferred,
			URL:      s.URL,
			Headers:  headers,
			Command:  s.Command,
			Args:     args,
			Env:      env,
			EnvFile:  s.EnvFile,
		})
	}
	return resp
}

// handleGetMCPConfig returns the tools.mcp subtree of the current config.
//
//	GET /api/mcp/config
func (h *Handler) handleGetMCPConfig(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := config.LoadConfigLenient(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(buildMCPConfigResponse(cfg))
}

// validateMCPServers enforces the UI contract: unique non-empty names,
// known types, command for stdio, url for sse/http.
func validateMCPServers(servers []mcpServerDTO) error {
	seen := map[string]bool{}
	for _, s := range servers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return fmt.Errorf("server name must not be empty")
		}
		if seen[name] {
			return fmt.Errorf("duplicate server name %q", name)
		}
		seen[name] = true
		switch s.Type {
		case "stdio":
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("server %q: command is required for stdio servers", name)
			}
		case "sse", "http":
			if strings.TrimSpace(s.URL) == "" {
				return fmt.Errorf("server %q: url is required for %s servers", name, s.Type)
			}
		default:
			return fmt.Errorf("server %q: invalid type %q (want stdio, sse or http)", name, s.Type)
		}
	}
	return nil
}

func mcpServerDTOToConfig(s mcpServerDTO) config.MCPServerConfig {
	return config.MCPServerConfig{
		Enabled:  s.Enabled,
		Deferred: s.Deferred,
		Command:  strings.TrimSpace(s.Command),
		Args:     s.Args,
		Env:      s.Env,
		EnvFile:  strings.TrimSpace(s.EnvFile),
		Type:     s.Type,
		URL:      strings.TrimSpace(s.URL),
		Headers:  s.Headers,
	}
}

func applyMCPConfigRequest(cfg *config.Config, req mcpConfigRequest) {
	mcpCfg := &cfg.Tools.MCP
	mcpCfg.Enabled = req.Enabled
	if req.MaxInlineTextChars > 0 {
		mcpCfg.MaxInlineTextChars = req.MaxInlineTextChars
	}
	mcpCfg.Discovery.Enabled = req.Discovery.Enabled
	mcpCfg.Discovery.TTL = req.Discovery.TTLSeconds
	mcpCfg.Discovery.MaxSearchResults = req.Discovery.MaxSearchResults
	mcpCfg.Discovery.UseBM25 = req.Discovery.UseBM25
	mcpCfg.Discovery.UseRegex = req.Discovery.UseRegex
	servers := make(map[string]config.MCPServerConfig, len(req.Servers))
	for _, s := range req.Servers {
		servers[strings.TrimSpace(s.Name)] = mcpServerDTOToConfig(s)
	}
	mcpCfg.Servers = servers
}

// handlePutMCPConfig validates and replaces the tools.mcp subtree. It follows
// the same lenient-load → mutate → validate → SaveConfig pipeline as
// PATCH /api/config (unknown fields are dropped by design, see
// applyConfigPatch), serialized through configWriteMu.
//
//	PUT /api/mcp/config
func (h *Handler) handlePutMCPConfig(w http.ResponseWriter, r *http.Request) {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req mcpConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := validateMCPServers(req.Servers); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, _, err := config.LoadConfigLenient(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	applyMCPConfigRequest(cfg, req)

	if errs := validateConfig(cfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	logger.Infof("MCP configuration updated")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// mcpProbeFunc is injectable for tests (same pattern as gatewayHealthGet).
var mcpProbeFunc = func(ctx context.Context, name string, cfg config.MCPServerConfig, workspace string, timeout time.Duration) (*mcp.ProbeResult, error) {
	return mcp.ProbeServer(ctx, name, cfg, workspace, timeout)
}

type mcpToolInfoDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type mcpServerTestResponse struct {
	OK        bool             `json:"ok"`
	LatencyMS int64            `json:"latencyMs"`
	ToolCount int              `json:"toolCount"`
	Tools     []mcpToolInfoDTO `json:"tools"`
	Error     string           `json:"error,omitempty"`
}

// handleTestMCPServer dials a single not-yet-saved MCP server and reports the
// outcome. Probe failures are a 200 with ok:false — the test result is the
// payload; only malformed requests are HTTP errors.
//
//	POST /api/mcp/servers/test
func (h *Handler) handleTestMCPServer(w http.ResponseWriter, r *http.Request) {
	var req mcpServerDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if err := validateMCPServers([]mcpServerDTO{req}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, _, err := config.LoadConfigLenient(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	res, probeErr := mcpProbeFunc(ctx, strings.TrimSpace(req.Name), mcpServerDTOToConfig(req), cfg.WorkspacePath(), 15*time.Second)

	resp := mcpServerTestResponse{Tools: []mcpToolInfoDTO{}}
	if probeErr != nil {
		resp.Error = probeErr.Error()
	} else {
		resp.OK = true
		resp.LatencyMS = res.LatencyMS
		resp.ToolCount = res.ToolCount
		for _, tool := range res.Tools {
			if tool == nil {
				continue
			}
			resp.Tools = append(resp.Tools, mcpToolInfoDTO{Name: tool.Name, Description: tool.Description})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGetMCPStatus proxies the gateway's protected /mcp/status endpoint.
// The bearer token comes from the pid file (the same source the gateway used
// to configure its health server). Any failure to reach or authenticate maps
// to {"gateway":"offline"} — the UI renders configured values with an offline
// banner, per spec §3.
//
//	GET /api/mcp/status
func (h *Handler) handleGetMCPStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := config.LoadConfigLenient(h.configPath)
	if err != nil {
		cfg = nil
	}
	baseURL, token := h.gatewayProbeBaseURL(cfg)

	offline := func() {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"gateway": "offline"})
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+"/mcp/status", nil)
	if err != nil {
		offline()
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		offline()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		offline()
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}
