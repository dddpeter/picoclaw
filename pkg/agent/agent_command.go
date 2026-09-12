// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"time"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func (al *AgentLoop) handleCommand(
	ctx context.Context,
	msg bus.InboundMessage,
	agent *AgentInstance,
	opts *processOptions,
) (string, bool) {
	normalizeProcessOptionsInPlace(opts)

	if !commands.HasCommandPrefix(msg.Content) {
		return "", false
	}

	if matched, handled, reply := al.applyExplicitSkillCommand(msg.Content, agent, opts); matched {
		return reply, handled
	}

	if al.cmdRegistry == nil {
		return "", false
	}

	rt := al.buildCommandsRuntime(ctx, agent, opts)
	executor := commands.NewExecutor(al.cmdRegistry, rt)

	var commandReply string
	result := executor.Execute(ctx, commands.Request{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: msg.SenderID,
		Text:     msg.Content,
		Reply: func(text string) error {
			commandReply = text
			return nil
		},
	})

	switch result.Outcome {
	case commands.OutcomeHandled:
		if result.Err != nil {
			return mapCommandError(result), true
		}
		if commandReply != "" {
			return commandReply, true
		}
		return "", true
	default: // OutcomePassthrough — let the message fall through to LLM
		return "", false
	}
}

func (al *AgentLoop) applyExplicitSkillCommand(
	raw string,
	agent *AgentInstance,
	opts *processOptions,
) (matched bool, handled bool, reply string) {
	normalizeProcessOptionsInPlace(opts)

	cmdName, ok := commands.CommandName(raw)
	if !ok || cmdName != "use" {
		return false, false, ""
	}

	if agent == nil || agent.ContextBuilder == nil {
		return true, true, commandsUnavailableSkillMessage()
	}

	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) < 2 {
		return true, true, buildUseCommandHelp(agent)
	}

	arg := strings.TrimSpace(parts[1])
	if strings.EqualFold(arg, "clear") || strings.EqualFold(arg, "off") {
		if opts != nil {
			al.clearPendingSkills(opts.Dispatch.SessionKey)
		}
		return true, true, "Cleared pending skill override."
	}

	skillName, ok := agent.ContextBuilder.ResolveSkillName(arg)
	if !ok {
		return true, true, fmt.Sprintf("Unknown skill: %s\nUse /list skills to see installed skills.", arg)
	}

	if len(parts) < 3 {
		if opts == nil || strings.TrimSpace(opts.Dispatch.SessionKey) == "" {
			return true, true, commandsUnavailableSkillMessage()
		}
		al.setPendingSkills(opts.Dispatch.SessionKey, []string{skillName})
		return true, true, fmt.Sprintf(
			"Skill %q is armed for your next message. Send your next prompt normally, or use /use clear to cancel.",
			skillName,
		)
	}

	message := strings.TrimSpace(strings.Join(parts[2:], " "))
	if message == "" {
		return true, true, buildUseCommandHelp(agent)
	}

	if opts != nil {
		opts.ForcedSkills = append(opts.ForcedSkills, skillName)
		opts.Dispatch.UserMessage = message
		opts.UserMessage = message
	}

	return true, false, ""
}

func (al *AgentLoop) buildCommandsRuntime(
	ctx context.Context,
	agent *AgentInstance,
	opts *processOptions,
) *commands.Runtime {
	normalizeProcessOptionsInPlace(opts)

	registry := al.GetRegistry()
	cfg := al.GetConfig()
	rt := &commands.Runtime{
		Config:          cfg,
		ListAgentIDs:    registry.ListAgentIDs,
		ListDefinitions: al.cmdRegistry.Definitions,
		ListMCPServers: func(ctx context.Context) []commands.MCPServerInfo {
			if cfg == nil {
				return nil
			}

			if len(cfg.Tools.MCP.Servers) == 0 {
				return nil
			}

			if err := al.ensureMCPInitialized(ctx); err != nil {
				logger.WarnCF("agent", "Failed to refresh MCP status for command",
					map[string]any{
						"error": err.Error(),
					})
			}

			connected := make(map[string]int)
			if manager := al.mcp.getManager(); manager != nil {
				for serverName, conn := range manager.GetServers() {
					connected[serverName] = len(conn.Tools)
				}
			}

			servers := make([]commands.MCPServerInfo, 0, len(cfg.Tools.MCP.Servers))
			for serverName, serverCfg := range cfg.Tools.MCP.Servers {
				toolCount, isConnected := connected[serverName]
				servers = append(servers, commands.MCPServerInfo{
					Name:      serverName,
					Enabled:   serverCfg.Enabled,
					Deferred:  serverIsDeferred(cfg.Tools.MCP.Discovery.Enabled, serverCfg),
					Connected: isConnected,
					ToolCount: toolCount,
				})
			}

			sort.Slice(servers, func(i, j int) bool {
				return strings.ToLower(servers[i].Name) < strings.ToLower(servers[j].Name)
			})

			return servers
		},
		ListMCPTools: func(ctx context.Context, serverName string) ([]commands.MCPToolInfo, error) {
			if cfg == nil {
				return nil, fmt.Errorf("command unavailable: config not loaded")
			}

			serverName = strings.TrimSpace(serverName)
			if serverName == "" {
				return nil, fmt.Errorf("server name is required")
			}

			resolvedName := ""
			var serverCfg config.MCPServerConfig
			for name, candidate := range cfg.Tools.MCP.Servers {
				if strings.EqualFold(name, serverName) {
					resolvedName = name
					serverCfg = candidate
					break
				}
			}
			if resolvedName == "" {
				return nil, fmt.Errorf("MCP server '%s' is not configured", serverName)
			}
			if !serverCfg.Enabled {
				return nil, fmt.Errorf("MCP server '%s' is configured but disabled", resolvedName)
			}
			if !cfg.Tools.IsToolEnabled("mcp") {
				return nil, fmt.Errorf("MCP integration is disabled")
			}

			if err := al.ensureMCPInitialized(ctx); err != nil {
				logger.WarnCF("agent", "Failed to initialize MCP runtime for command",
					map[string]any{
						"server": resolvedName,
						"error":  err.Error(),
					})
			}

			manager := al.mcp.getManager()
			if manager == nil {
				return nil, fmt.Errorf("MCP server '%s' is configured but not connected", resolvedName)
			}

			conn, ok := manager.GetServer(resolvedName)
			if !ok {
				return nil, fmt.Errorf("MCP server '%s' is configured but not connected", resolvedName)
			}

			toolInfos := make([]commands.MCPToolInfo, 0, len(conn.Tools))
			for _, tool := range conn.Tools {
				if tool == nil {
					continue
				}
				name := strings.TrimSpace(tool.Name)
				if name == "" {
					continue
				}

				description := strings.TrimSpace(tool.Description)
				if description == "" {
					description = fmt.Sprintf("MCP tool from %s server", resolvedName)
				}

				toolInfos = append(toolInfos, commands.MCPToolInfo{
					Name:        name,
					Description: description,
					Parameters:  summarizeMCPToolParameters(tool.InputSchema),
				})
			}
			sort.Slice(toolInfos, func(i, j int) bool {
				return toolInfos[i].Name < toolInfos[j].Name
			})
			return toolInfos, nil
		},
		GetEnabledChannels: func() []string {
			if al.channelManager == nil {
				return nil
			}
			return al.channelManager.GetEnabledChannels()
		},
		GetActiveTurn: func() any {
			info := al.GetActiveTurn()
			if info == nil {
				return nil
			}
			return info
		},
		SwitchChannel: func(value string) error {
			if al.channelManager == nil {
				return fmt.Errorf("channel manager not initialized")
			}
			if _, exists := al.channelManager.GetChannel(value); !exists && value != "cli" {
				return fmt.Errorf("channel '%s' not found or not enabled", value)
			}
			return nil
		},
	}
	rt.StopActiveTurn = func() (commands.StopResult, error) {
		if opts == nil {
			return commands.StopResult{}, fmt.Errorf("process options not available")
		}
		return al.stopActiveTurnForSession(opts.Dispatch.SessionKey)
	}
	if agent != nil && agent.ContextBuilder != nil {
		rt.ListSkillNames = agent.ContextBuilder.ListSkillNames
	}
	rt.ReloadConfig = func() error {
		if al.reloadFunc == nil {
			return fmt.Errorf("reload not configured")
		}
		return al.reloadFunc()
	}
	if agent != nil {
		if agent.ContextBuilder != nil {
			rt.ListSkillNames = agent.ContextBuilder.ListSkillNames
		}
		rt.GetModelInfo = func() (string, string) {
			modelMu := agent.modelStateMutex()
			modelMu.RLock()
			defer modelMu.RUnlock()
			return agent.Model, resolvedCandidateProvider(agent.Candidates, cfg.Agents.Defaults.Provider)
		}
		rt.SwitchModel = func(value string) (string, error) {
			value = strings.TrimSpace(value)
			modelFound := false
			for _, modelCfg := range cfg.ModelList {
				if modelCfg != nil && modelCfg.ModelName == value {
					modelFound = true
					break
				}
			}
			if !modelFound {
				return "", fmt.Errorf("model %q not found in model_list or providers", value)
			}
			// runTurn holds the model state read lock for the whole turn, so
			// blocking here would hang /switch behind any in-flight (or hung)
			// turn. Degrade to an explicit "busy" error instead of waiting.
			modelMu := agent.modelStateMutex()
			if !modelMu.TryLock() {
				return "", fmt.Errorf("model switch skipped: a task is currently running, try again after it finishes")
			}
			defer modelMu.Unlock()
			return al.swapAgentModelLocked(cfg, agent, value)
		}

		// ResetModel restores the configured default model for the agent. It
		// re-reads the config file first so a default model edited after
		// gateway startup applies on /new even when hot reload never ran
		// (disabled, or enabled only after the process started).
		//
		// runTurn holds the model state read lock for the whole turn —
		// including the LLM HTTP call — so taking the write lock here
		// unconditionally would deadlock /new behind any in-flight (or hung)
		// turn. Resolve and compare outside the lock, and only attempt the
		// write lock when a switch is actually needed; if an active turn
		// holds the lock, skip the reset instead of blocking the command.
		rt.ResetModel = func() (string, error) {
			if cfg == nil {
				return agent.Model, nil
			}

			resolvedCfg := cfg
			if path := al.getConfigPath(); path != "" {
				if _, statErr := os.Stat(path); statErr != nil {
					logger.WarnCF("agent", "Config file unavailable for model reset; using in-memory defaults",
						map[string]any{"config_path": path, "error": statErr.Error()})
				} else if diskCfg, err := config.LoadConfig(path); err != nil {
					logger.WarnCF("agent", "Failed to re-read config for model reset; using in-memory defaults",
						map[string]any{"config_path": path, "error": err.Error()})
				} else if err := diskCfg.ValidateModelList(); err != nil {
					logger.WarnCF("agent", "Config on disk failed validation for model reset; using in-memory defaults",
						map[string]any{"config_path": path, "error": err.Error()})
				} else {
					resolvedCfg = diskCfg
				}
			}

			defaultModel := resolveAgentDefaultModel(resolvedCfg, agent)
			modelMu := agent.modelStateMutex()
			modelMu.RLock()
			current := agent.Model
			modelMu.RUnlock()
			if defaultModel == "" || defaultModel == current {
				return current, nil
			}
			if !modelMu.TryLock() {
				return current, fmt.Errorf("model reset skipped: a task is currently holding model state")
			}
			defer modelMu.Unlock()
			if agent.Model == defaultModel {
				return defaultModel, nil
			}
			if _, err := al.swapAgentModelLocked(resolvedCfg, agent, defaultModel); err != nil {
				return agent.Model, err
			}
			return defaultModel, nil
		}

		rt.ClearHistory = func() error {
			if opts == nil {
				return fmt.Errorf("process options not available")
			}
			// /clear can arrive before any turn has persisted session scope
			// metadata (runAgentLoop records it per turn), so record it here to
			// let the ContextManager resolve which agent owns the session.
			ensureSessionMetadata(
				agent.Sessions,
				opts.Dispatch.SessionKey,
				opts.Dispatch.SessionScope,
				opts.Dispatch.SessionAliases,
			)
			return al.contextManager.Clear(ctx, opts.SessionKey)
		}

		// /new archives the previous conversation into its own session and
		// clears the live one, so history views keep the old chat.
		rt.NewSession = func() (bool, error) {
			if opts == nil {
				return false, fmt.Errorf("process options not available")
			}
			// Same reasoning as /clear above: record scope metadata before the
			// rotation resolves the owning agent's store.
			ensureSessionMetadata(
				agent.Sessions,
				opts.Dispatch.SessionKey,
				opts.Dispatch.SessionScope,
				opts.Dispatch.SessionAliases,
			)
			return al.rotateSession(ctx, agent, opts)
		}

		// /title: manual session titles outrank derived and light-model ones.
		rt.SetSessionTitle = func(title string) bool {
			if opts == nil {
				return false
			}
			return al.setSessionTitleUser(agent, opts.Dispatch.SessionKey, title)
		}
		rt.GetSessionTitle = func() (string, string, bool) {
			if opts == nil {
				return "", "", false
			}
			return al.getSessionTitleInfo(agent, opts.Dispatch.SessionKey)
		}

		rt.AskSideQuestion = func(ctx context.Context, question string) (string, error) {
			return al.askSideQuestion(ctx, agent, opts, question)
		}

		rt.GetStatusOverview = func() *commands.StatusOverview {
			overview := &commands.StatusOverview{
				Version: config.FormatVersion(),
			}
			if agent != nil {
				modelMu := agent.modelStateMutex()
				modelMu.RLock()
				overview.Model = agent.Model
				modelMu.RUnlock()
				overview.Provider = resolvedCandidateProvider(agent.Candidates, cfg.Agents.Defaults.Provider)
			}
			if al.channelManager != nil {
				overview.Channels = al.channelManager.GetEnabledChannels()
			}
			if !al.startedAt.IsZero() {
				overview.Uptime = time.Since(al.startedAt)
			}
			al.activeTurnStates.Range(func(key, value any) bool {
				ts, ok := value.(*turnState)
				if !ok || ts == nil {
					return true
				}
				snap := ts.snapshot()
				if strings.HasPrefix(snap.TurnID, pendingTurnPrefix) {
					return true
				}
				overview.ActiveTurns = append(overview.ActiveTurns, commands.ActiveTurnStatus{
					SessionKey: snap.SessionKey,
					AgentID:    snap.AgentID,
					Channel:    snap.Channel,
					Task:       snap.UserMessage,
					Phase:      string(snap.Phase),
					Iteration:  snap.Iteration,
					RunningFor: time.Since(snap.StartedAt),
				})
				return true
			})
			return overview
		}
		rt.GetContextStats = func() *commands.ContextStats {
			if opts == nil || agent.Sessions == nil {
				return nil
			}
			usage := computeContextUsage(agent, opts.SessionKey)
			if usage == nil {
				return nil
			}
			history := agent.Sessions.GetHistory(opts.SessionKey)
			return &commands.ContextStats{
				UsedTokens:        usage.UsedTokens,
				TotalTokens:       usage.TotalTokens,
				HistoryTokens:     usage.HistoryTokens,
				CompressAtTokens:  usage.CompressAtTokens,
				SummarizeAtTokens: usage.SummarizeAtTokens,
				UsedPercent:       usage.UsedPercent,
				MessageCount:      len(history),
			}
		}
	}
	return rt
}

// swapAgentModelLocked rebuilds the provider and candidate chains so the
// agent's primary model becomes value. It returns the previous model name.
// Callers must hold the agent's model state mutex.
func (al *AgentLoop) swapAgentModelLocked(
	cfg *config.Config,
	agent *AgentInstance,
	value string,
) (string, error) {
	nextCandidates := resolveModelCandidates(cfg, cfg.Agents.Defaults.Provider, value, agent.Fallbacks)
	if len(nextCandidates) == 0 {
		return "", fmt.Errorf("model %q did not resolve to any provider candidates", value)
	}
	modelCfg, err := resolvedCandidateModelConfig(cfg, nextCandidates[0], agent.Workspace)
	if err != nil {
		return "", err
	}
	nextProvider, _, err := providers.CreateProviderFromConfig(modelCfg)
	if err != nil {
		return "", fmt.Errorf("failed to initialize model %q: %w", value, err)
	}
	nextCandidateProviders := make(map[string]providers.LLMProvider)
	copyInitializedCandidateProviders(
		agent.CandidateProviders,
		nextCandidateProviders,
		agent.ImageCandidates,
	)
	copyInitializedCandidateProviders(
		agent.CandidateProviders,
		nextCandidateProviders,
		agent.LightCandidates,
	)
	inheritPrimaryProviderForCandidates(
		cfg,
		agent.Workspace,
		nextCandidates[0],
		nextCandidates[1:],
		nextProvider,
		nextCandidateProviders,
	)
	populateCandidateProvidersFromCandidates(
		cfg,
		agent.Workspace,
		nextCandidates[1:],
		nextCandidateProviders,
	)

	oldModel := agent.Model
	oldProvider := agent.Provider
	oldCandidateProviders := agent.CandidateProviders
	previousProviders := make(map[string]providers.LLMProvider, len(oldCandidateProviders)+1)
	for key, provider := range oldCandidateProviders {
		previousProviders[key] = provider
	}
	previousProviders["previous-primary"] = oldProvider
	agent.Model = value
	agent.Provider = nextProvider
	agent.Candidates = nextCandidates
	agent.CandidateProviders = nextCandidateProviders
	agent.ThinkingLevel = parseThinkingLevel(modelCfg.ThinkingLevel)
	agent.ThinkingLevelConfigured = isConfiguredThinkingLevel(modelCfg.ThinkingLevel)

	closeUnreferencedStatefulProviders(
		previousProviders,
		nextCandidateProviders,
		nextProvider,
		agent.LightProvider,
	)
	return oldModel, nil
}

// resolveAgentDefaultModel mirrors the precedence used when the agent was
// created (AGENT.md frontmatter > agents.list entry > defaults), but against
// the config passed in — which may be a freshly re-read disk config.
func resolveAgentDefaultModel(cfg *config.Config, agent *AgentInstance) string {
	if agent == nil {
		return ""
	}
	if agent.Definition.Agent != nil && strings.TrimSpace(agent.Definition.Agent.Frontmatter.Model) != "" {
		return strings.TrimSpace(agent.Definition.Agent.Frontmatter.Model)
	}
	if cfg != nil {
		for i := range cfg.Agents.List {
			entry := &cfg.Agents.List[i]
			if entry.Model != nil && strings.TrimSpace(entry.Model.Primary) != "" &&
				strings.EqualFold(entry.ID, agent.ID) {
				return strings.TrimSpace(entry.Model.Primary)
			}
		}
		return cfg.Agents.Defaults.GetModelName()
	}
	return ""
}

func summarizeMCPToolParameters(schema any) []commands.MCPToolParameterInfo {
	schemaMap := normalizeMCPSchema(schema)
	properties, ok := schemaMap["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return nil
	}

	required := make(map[string]struct{})
	switch raw := schemaMap["required"].(type) {
	case []string:
		for _, name := range raw {
			required[name] = struct{}{}
		}
	case []any:
		for _, value := range raw {
			name, ok := value.(string)
			if ok {
				required[name] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	params := make([]commands.MCPToolParameterInfo, 0, len(names))
	for _, name := range names {
		param := commands.MCPToolParameterInfo{Name: name}
		if propMap, ok := properties[name].(map[string]any); ok {
			if typeName, ok := propMap["type"].(string); ok {
				param.Type = strings.TrimSpace(typeName)
			}
			if desc, ok := propMap["description"].(string); ok {
				param.Description = strings.TrimSpace(desc)
			}
		}
		_, param.Required = required[name]
		params = append(params, param)
	}
	return params
}

func normalizeMCPSchema(schema any) map[string]any {
	if schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		}
	}

	if schemaMap, ok := schema.(map[string]any); ok {
		return schemaMap
	}

	var jsonData []byte
	switch raw := schema.(type) {
	case json.RawMessage:
		jsonData = raw
	case []byte:
		jsonData = raw
	}

	if jsonData == nil {
		var err error
		jsonData, err = json.Marshal(schema)
		if err != nil {
			return map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			}
		}
	}

	var result map[string]any
	if err := json.Unmarshal(jsonData, &result); err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
			"required":   []string{},
		}
	}

	return result
}

func (al *AgentLoop) setPendingSkills(sessionKey string, skillNames []string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || len(skillNames) == 0 {
		return
	}

	filtered := make([]string, 0, len(skillNames))
	for _, name := range skillNames {
		name = strings.TrimSpace(name)
		if name != "" {
			filtered = append(filtered, name)
		}
	}
	if len(filtered) == 0 {
		return
	}

	al.pendingSkills.Store(sessionKey, filtered)
}

func (al *AgentLoop) takePendingSkills(sessionKey string) []string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}

	value, ok := al.pendingSkills.LoadAndDelete(sessionKey)
	if !ok {
		return nil
	}

	skills, ok := value.([]string)
	if !ok {
		return nil
	}

	return append([]string(nil), skills...)
}

func (al *AgentLoop) clearPendingSkills(sessionKey string) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}
	al.pendingSkills.Delete(sessionKey)
}
