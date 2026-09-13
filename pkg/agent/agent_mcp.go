// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type mcpRuntime struct {
	initOnce sync.Once
	mu       sync.Mutex
	manager  *mcp.Manager
	initErr  error
}

func (r *mcpRuntime) reset() *mcp.Manager {
	r.mu.Lock()
	manager := r.manager
	r.manager = nil
	r.initErr = nil
	r.initOnce = sync.Once{}
	r.mu.Unlock()
	return manager
}

func (r *mcpRuntime) setManager(manager *mcp.Manager) {
	r.mu.Lock()
	r.manager = manager
	r.initErr = nil
	r.mu.Unlock()
}

func (r *mcpRuntime) setInitErr(err error) {
	r.mu.Lock()
	r.initErr = err
	r.mu.Unlock()
}

func (r *mcpRuntime) getInitErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initErr
}

func (r *mcpRuntime) takeManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	manager := r.manager
	r.manager = nil
	return manager
}

func (r *mcpRuntime) hasManager() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager != nil
}

func (r *mcpRuntime) getManager() *mcp.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.manager
}

// ensureMCPInitialized loads MCP servers/tools once so both Run() and direct
// agent mode share the same initialization path.
func (al *AgentLoop) ensureMCPInitialized(ctx context.Context) error {
	if !al.cfg.Tools.IsToolEnabled("mcp") {
		return nil
	}

	if al.cfg.Tools.MCP.Servers == nil || len(al.cfg.Tools.MCP.Servers) == 0 {
		logger.WarnCF("agent", "MCP is enabled but no servers are configured, skipping MCP initialization", nil)
		return nil
	}

	mcpCfg := filterMCPConfigServers(al.cfg.Tools.MCP, al.registry.allowedMCPServers())
	if mcpCfg.Servers == nil || len(mcpCfg.Servers) == 0 {
		logger.InfoCF(
			"agent",
			"No MCP servers selected after applying per-agent mcpServers allowlists",
			nil,
		)
		return nil
	}

	findValidServer := false
	for _, serverCfg := range mcpCfg.Servers {
		if serverCfg.Enabled {
			findValidServer = true
		}
	}
	if !findValidServer {
		logger.WarnCF("agent", "MCP is enabled but no valid servers are configured, skipping MCP initialization", nil)
		return nil
	}

	al.mcp.initOnce.Do(func() {
		mcpManager := mcp.NewManager(mcp.WithRuntimeEvents(al.runtimeEvents))

		defaultAgent := al.registry.GetDefaultAgent()
		workspacePath := al.cfg.WorkspacePath()
		if defaultAgent != nil && defaultAgent.Workspace != "" {
			workspacePath = defaultAgent.Workspace
		}

		if err := mcpManager.LoadFromMCPConfig(ctx, mcpCfg, workspacePath); err != nil {
			al.mcp.setInitErr(fmt.Errorf("failed to load MCP servers: %w", err))
			logger.WarnCF("agent", "Failed to load MCP servers, MCP tools will not be available",
				map[string]any{
					"error": err.Error(),
				})
			if closeErr := mcpManager.Close(); closeErr != nil {
				logger.ErrorCF("agent", "Failed to close MCP manager",
					map[string]any{
						"error": closeErr.Error(),
					})
			}
			return
		}

		// Register MCP tools for all agents
		servers := mcpManager.GetServers()
		uniqueTools := 0
		totalRegistrations := 0
		agentIDs := al.registry.ListAgentIDs()
		agentCount := len(agentIDs)

		for serverName, conn := range servers {
			uniqueTools += len(conn.Tools)

			// Determine whether this server's tools should be deferred (hidden).
			// Per-server "deferred" field takes precedence over the global Discovery.Enabled.
			serverCfg := mcpCfg.Servers[serverName]
			registerAsHidden := serverIsDeferred(al.cfg.Tools.MCP.Discovery.Enabled, serverCfg)
			registeredToolsByAgent := make(map[string]map[string]struct{}, len(agentIDs))

			for _, tool := range conn.Tools {
				for _, agentID := range agentIDs {
					agent, ok := al.registry.GetAgent(agentID)
					if !ok {
						continue
					}
					if !agent.AllowsMCPServer(serverName) {
						logger.DebugCF("agent", "Skipped MCP tool registration by agent mcpServers allowlist",
							map[string]any{
								"agent_id": agentID,
								"server":   serverName,
								"tool":     tool.Name,
							})
						continue
					}

					mcpTool := tools.NewMCPTool(mcpManager, serverName, tool)
					toolName := mcpTool.Name()
					mcpTool.SetWorkspace(agent.Workspace)
					mcpTool.SetMaxInlineTextRunes(al.cfg.Tools.MCP.GetMaxInlineTextChars())
					mcpTool.SetEventPublisher(al.runtimeEvents)

					if registerAsHidden {
						agent.Tools.RegisterHidden(mcpTool)
					} else {
						agent.Tools.Register(mcpTool)
					}
					if !toolRegistryIncludes(agent.Tools, toolName) {
						continue
					}

					recordRegisteredMCPTool(registeredToolsByAgent, agentID, toolName)
					totalRegistrations++
					logger.DebugCF("agent", "Registered MCP tool",
						map[string]any{
							"agent_id": agentID,
							"server":   serverName,
							"tool":     tool.Name,
							"name":     toolName,
							"deferred": registerAsHidden,
						})
				}
			}

			for _, agentID := range agentIDs {
				agent, ok := al.registry.GetAgent(agentID)
				if !ok {
					continue
				}
				registerMCPServerPromptContributor(
					agentID,
					agent,
					serverName,
					len(registeredToolsByAgent[agentID]),
					registerAsHidden,
				)
			}
		}
		logger.InfoCF("agent", "MCP tools registered successfully",
			map[string]any{
				"server_count":        len(servers),
				"unique_tools":        uniqueTools,
				"total_registrations": totalRegistrations,
				"agent_count":         agentCount,
			})

		// Initializes Discovery Tools only if enabled by configuration
		if al.cfg.Tools.MCP.Enabled && al.cfg.Tools.MCP.Discovery.Enabled {
			useBM25 := al.cfg.Tools.MCP.Discovery.UseBM25
			useRegex := al.cfg.Tools.MCP.Discovery.UseRegex

			// Fail fast: If discovery is enabled but no search method is turned on
			if !useBM25 && !useRegex {
				al.mcp.setInitErr(fmt.Errorf(
					"tool discovery is enabled but neither 'use_bm25' nor 'use_regex' is set to true in the configuration",
				))
				if closeErr := mcpManager.Close(); closeErr != nil {
					logger.ErrorCF("agent", "Failed to close MCP manager",
						map[string]any{
							"error": closeErr.Error(),
						})
				}
				return
			}

			ttl := al.cfg.Tools.MCP.Discovery.TTL
			if ttl <= 0 {
				ttl = 5 // Default value
			}

			maxSearchResults := al.cfg.Tools.MCP.Discovery.MaxSearchResults
			if maxSearchResults <= 0 {
				maxSearchResults = 5 // Default value
			}

			logger.InfoCF("agent", "Initializing tool discovery", map[string]any{
				"bm25": useBM25, "regex": useRegex, "ttl": ttl, "max_results": maxSearchResults,
			})

			for _, agentID := range agentIDs {
				agent, ok := al.registry.GetAgent(agentID)
				if !ok {
					continue
				}
				if !agentHasDiscoverableMCPServers(al.cfg, agent.MCPServerAllowlist) {
					continue
				}

				if useRegex {
					agent.Tools.Register(tools.NewRegexSearchTool(agent.Tools, ttl, maxSearchResults))
				}
				if useBM25 {
					agent.Tools.Register(tools.NewBM25SearchTool(agent.Tools, ttl, maxSearchResults))
				}
			}
		}

		al.mcp.setManager(mcpManager)
	})

	return al.mcp.getInitErr()
}

func registerMCPServerPromptContributor(
	agentID string,
	agent *AgentInstance,
	serverName string,
	toolCount int,
	registerAsHidden bool,
) {
	if agent == nil || agent.ContextBuilder == nil || toolCount <= 0 {
		return
	}
	if err := agent.ContextBuilder.RegisterPromptContributor(mcpServerPromptContributor{
		serverName: serverName,
		toolCount:  toolCount,
		deferred:   registerAsHidden,
	}); err != nil {
		logger.WarnCF("agent", "Failed to register MCP prompt contributor",
			map[string]any{
				"agent_id": agentID,
				"server":   serverName,
				"error":    err.Error(),
			})
	}
}

func recordRegisteredMCPTool(
	registeredToolsByAgent map[string]map[string]struct{},
	agentID, toolName string,
) {
	if registeredToolsByAgent[agentID] == nil {
		registeredToolsByAgent[agentID] = make(map[string]struct{})
	}
	registeredToolsByAgent[agentID][toolName] = struct{}{}
}

func toolRegistryIncludes(registry *tools.ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	return registry.HasRegistered(name)
}

func filterMCPConfigServers(
	mcpCfg config.MCPConfig,
	allowed map[string]struct{},
) config.MCPConfig {
	if allowed == nil {
		return mcpCfg
	}

	filtered := mcpCfg
	filtered.Servers = make(map[string]config.MCPServerConfig)
	normalizedAllowed := make(map[string]struct{}, len(allowed))
	for serverName := range allowed {
		name := normalizeMCPServerName(serverName)
		if name == "" {
			continue
		}
		normalizedAllowed[name] = struct{}{}
	}
	for serverName, serverCfg := range mcpCfg.Servers {
		if _, ok := normalizedAllowed[normalizeMCPServerName(serverName)]; ok {
			filtered.Servers[serverName] = serverCfg
		}
	}

	return filtered
}

func agentHasDiscoverableMCPServers(cfg *config.Config, allowed map[string]struct{}) bool {
	if cfg == nil || !cfg.Tools.MCP.Enabled || !cfg.Tools.MCP.Discovery.Enabled {
		return false
	}

	filtered := filterMCPConfigServers(cfg.Tools.MCP, allowed)
	for _, serverCfg := range filtered.Servers {
		if serverCfg.Enabled && serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg) {
			return true
		}
	}

	return false
}

// serverIsDeferred reports whether an MCP server's tools should be registered
// as hidden (deferred/discovery mode).
//
// The per-server Deferred field takes precedence over the global discoveryEnabled
// default. When Deferred is nil, discoveryEnabled is used as the fallback.
func serverIsDeferred(discoveryEnabled bool, serverCfg config.MCPServerConfig) bool {
	if !discoveryEnabled {
		return false
	}
	if serverCfg.Deferred != nil {
		return *serverCfg.Deferred
	}
	return true
}

// MCPToolStatus describes a single tool exposed by a connected MCP server.
type MCPToolStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MCPServerStatus is the runtime status of one MCP server as seen by the manager.
type MCPServerStatus struct {
	Connected bool            `json:"connected"`
	ToolCount int             `json:"toolCount"`
	Tools     []MCPToolStatus `json:"tools"`
	Error     string          `json:"error,omitempty"`
}

// MCPStatusSnapshot is the payload served by the gateway /mcp/status endpoint.
type MCPStatusSnapshot struct {
	Initialized bool                       `json:"initialized"`
	Enabled     bool                       `json:"enabled"`
	Servers     map[string]MCPServerStatus `json:"servers,omitempty"`
}

const mcpStatusDescriptionLimit = 200

func truncateMCPDescription(s string) string {
	runes := []rune(s)
	if len(runes) <= mcpStatusDescriptionLimit {
		return s
	}
	return string(runes[:mcpStatusDescriptionLimit]) + "…"
}

// buildMCPStatusSnapshot merges configured servers with live manager state.
// initialized=false (manager not yet created — MCP loads lazily on first turn)
// yields an empty server map so the frontend renders every server as
// "not loaded", never as an error. Servers that are enabled in config but
// absent from the manager are reported as not connected: the manager does not
// retain failed connections, and per-agent allowlists can also filter them.
func buildMCPStatusSnapshot(
	cfg *config.Config,
	servers map[string]*mcp.ServerConnection,
	initialized bool,
) MCPStatusSnapshot {
	snapshot := MCPStatusSnapshot{Servers: map[string]MCPServerStatus{}}
	if cfg == nil {
		return snapshot
	}
	snapshot.Enabled = cfg.Tools.IsToolEnabled("mcp")
	if !initialized {
		return snapshot
	}
	snapshot.Initialized = true

	seen := make(map[string]bool, len(servers))
	for name, conn := range servers {
		status := MCPServerStatus{Connected: true, Tools: []MCPToolStatus{}}
		if conn != nil {
			for _, tool := range conn.Tools {
				if tool == nil {
					continue
				}
				status.Tools = append(status.Tools, MCPToolStatus{
					Name:        tool.Name,
					Description: truncateMCPDescription(tool.Description),
				})
			}
		}
		status.ToolCount = len(status.Tools)
		snapshot.Servers[name] = status
		seen[name] = true
	}

	for name, serverCfg := range cfg.Tools.MCP.Servers {
		if seen[name] || !serverCfg.Enabled {
			continue
		}
		snapshot.Servers[name] = MCPServerStatus{
			Connected: false,
			Tools:     []MCPToolStatus{},
			Error:     "not loaded (connection failed or filtered by per-agent allowlist)",
		}
	}
	return snapshot
}

// MCPStatusSnapshot returns the current MCP runtime status for the health endpoint.
func (al *AgentLoop) MCPStatusSnapshot() any {
	manager := al.mcp.getManager()
	initialized := manager != nil
	var servers map[string]*mcp.ServerConnection
	if initialized {
		servers = manager.GetServers()
	}
	return buildMCPStatusSnapshot(al.cfg, servers, initialized)
}
