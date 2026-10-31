// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/audio/tts"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) *AgentLoop {
	registry := NewAgentRegistry(cfg, provider)
	startedAt := time.Now()

	// Set up shared fallback chain with rate limiting.
	cooldown := providers.NewCooldownTracker()
	// agents.defaults.cooldown_enabled=false turns cooldowns off entirely
	// (always-try semantics); the exhaustion bypass in the chain applies
	// regardless.
	cooldown.SetEnabled(cfg.Agents.Defaults.EffectiveCooldownEnabled())
	rl := providers.NewRateLimiterRegistry()
	// Register rate limiters for all agents' candidates so that RPM limits
	// configured in ModelConfig are enforced before each LLM call.
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			rl.RegisterCandidates(agent.Candidates)
			rl.RegisterCandidates(agent.LightCandidates)
		}
	}
	fallbackChain := providers.NewFallbackChain(cooldown, rl)

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	bridge, err := newEvolutionBridge(registry, cfg, provider)
	if err != nil {
		logger.WarnCF("agent", "Failed to initialize evolution bridge", map[string]any{
			"error": err.Error(),
		})
	}

	// Determine worker pool size from config (default: 1 = sequential)
	workerPoolSize := cfg.Agents.Defaults.MaxParallelTurns
	if workerPoolSize <= 0 {
		workerPoolSize = 1
	}

	al := &AgentLoop{
		startedAt:         startedAt,
		bus:               msgBus,
		cfg:               cfg,
		registry:          registry,
		state:             stateManager,
		fallback:          fallbackChain,
		cmdRegistry:       commands.NewRegistry(commands.BuiltinDefinitions()),
		evolution:         bridge,
		steering:          newSteeringQueue(parseSteeringMode(cfg.Agents.Defaults.SteeringMode)),
		workerSem:         make(chan struct{}, workerPoolSize),
		ownsRuntimeEvents: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(al)
		}
	}
	if al.runtimeEvents == nil {
		al.runtimeEvents = runtimeevents.NewBus()
		al.ownsRuntimeEvents = true
	}
	if bridge != nil {
		bridge.setCurrentCheck(al.isCurrentEvolutionBridge)
		if err := bridge.subscribeRuntimeEvents(al.runtimeEvents.Channel()); err != nil {
			logger.WarnCF("agent", "Failed to subscribe evolution bridge to runtime events", map[string]any{
				"error": err.Error(),
			})
		}
	}
	al.activeReqCond = sync.NewCond(&al.activeReqMu)
	al.refreshRuntimeEventLogger(cfg)
	al.providerFactory = providers.CreateProviderFromConfig
	al.hooks = NewHookManager(al.runtimeEvents.Channel())
	configureHookManagerFromConfig(al.hooks, cfg)
	al.contextManager = al.resolveContextManager()
	al.compactScheduler = newCompactScheduler(al)
	al.toolDefer = newToolDeferState()
	// ask_user wait bound (octop borrowing §三): configurable per fork
	// convention (agents.defaults.ask_timeout_ms); tests may override the
	// field directly to shrink waits.
	al.askTimeout = time.Duration(cfg.Agents.Defaults.GetAskTimeoutMS()) * time.Millisecond
	// Fresh-tail size for seahorse sessions (0/absent = 128 messages).
	seahorse.SetFreshTailCount(cfg.Agents.Defaults.FreshTailMessages)

	// Register shared tools to all agents (now that al is created)
	registerSharedTools(al, cfg, msgBus, registry, provider)

	// Shared-memory recall (OpenViking-style MCP memory backends)
	registerMemoryRecallContributors(al, cfg, registry)
	registerMemoryCommitter(al, cfg)

	return al
}

func registerSharedTools(
	al *AgentLoop,
	cfg *config.Config,
	msgBus interfaces.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) {
	allowReadPaths := buildAllowReadPatterns(cfg)
	var ttsProvider tts.TTSProvider
	if cfg.Tools.IsToolEnabled("send_tts") {
		ttsProvider = tts.DetectTTS(cfg)
		if ttsProvider == nil {
			logger.WarnCF("voice-tts", "send_tts enabled but no TTS provider configured", nil)
		}
	}

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		if cfg.Tools.IsToolEnabled("web") {
			searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptionsFromConfig(cfg))
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"error": err.Error()})
			} else if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(
				50000,
				cfg.Tools.Web.Proxy,
				cfg.Tools.Web.Format,
				cfg.Tools.Web.FetchLimitBytes,
				cfg.Tools.Web.PrivateHostWhitelist)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}
		if cfg.Tools.IsToolEnabled("serial") {
			agent.Tools.Register(tools.NewSerialTool())
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			if cfg.Tools.Message.MediaEnabled {
				messageTool.ConfigureLocalMedia(
					agent.Workspace,
					cfg.Agents.Defaults.RestrictToWorkspace,
					cfg.Agents.Defaults.GetMaxMediaSize(),
					allowReadPaths,
				)
			}
			messageTool.SetSendCallback(func(
				ctx context.Context,
				channel, chatID, content, replyToMessageID string,
				mediaParts []bus.MediaPart,
			) error {
				outboundCtx := bus.NewOutboundContext(channel, chatID, replyToMessageID)
				outboundAgentID, outboundSessionKey, outboundScope := outboundTurnMetadata(
					tools.ToolAgentID(ctx),
					tools.ToolSessionKey(ctx),
					tools.ToolSessionScope(ctx),
				)
				if len(mediaParts) > 0 {
					outboundMedia := bus.OutboundMediaMessage{
						Channel:    channel,
						ChatID:     chatID,
						Context:    outboundCtx,
						AgentID:    outboundAgentID,
						SessionKey: outboundSessionKey,
						Scope:      outboundScope,
						Parts:      mediaParts,
					}
					if al.channelManager != nil && channel != "" {
						return al.channelManager.SendMedia(ctx, outboundMedia)
					}
					pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer pubCancel()
					return msgBus.PublishOutboundMedia(pubCtx, outboundMedia)
				}
				outboundMessage := bus.OutboundMessage{
					Channel:          channel,
					ChatID:           chatID,
					Context:          outboundCtx,
					AgentID:          outboundAgentID,
					SessionKey:       outboundSessionKey,
					Scope:            outboundScope,
					Content:          content,
					ReplyToMessageID: replyToMessageID,
				}
				if al.channelManager != nil && channel != "" {
					return al.channelManager.SendMessage(ctx, outboundMessage)
				}
				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				return msgBus.PublishOutbound(pubCtx, outboundMessage)
			})
			agent.Tools.Register(messageTool)
		}
		if cfg.Tools.IsToolEnabled("reaction") {
			reactionTool := tools.NewReactionTool()
			reactionTool.SetReactionCallback(func(ctx context.Context, channel, chatID, messageID string) error {
				if al.channelManager == nil {
					return fmt.Errorf("channel manager not configured")
				}
				ch, ok := al.channelManager.GetChannel(channel)
				if !ok {
					return fmt.Errorf("channel %s not found", channel)
				}
				rc, ok := ch.(channels.ReactionCapable)
				if !ok {
					return fmt.Errorf("channel %s does not support reactions", channel)
				}
				_, err := rc.ReactToMessage(ctx, chatID, messageID)
				return err
			})
			agent.Tools.Register(reactionTool)
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(sendFileTool)
		}

		if ttsProvider != nil {
			agent.Tools.Register(tools.NewSendTTSTool(ttsProvider, nil))
		}

		if cfg.Tools.IsToolEnabled("load_image") {
			loadImageTool := tools.NewLoadImageTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(loadImageTool)
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// Spawn and spawn_status tools share a SubagentManager.
		// Construct it when either tool is enabled (both require subagent).
		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		spawnStatusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
		if (spawnEnabled || spawnStatusEnabled) && cfg.Tools.IsToolEnabled("subagent") {
			subagentManager := tools.NewSubagentManager(agent.Model)
			subagentManager.SetLLMOptions(agent.MaxTokens)

			// Clone the parent's tool registry so subagents can use all
			// tools registered so far (file, web, etc.) but NOT spawn/
			// spawn_status which are added below — preventing recursive
			// subagent spawning.
			if spawnEnabled {
				spawnTool := tools.NewSpawnTool(subagentManager)
				spawnTool.SetSpawner(NewSubTurnSpawner(al))
				currentAgentID := agentID
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})

				agent.Tools.Register(spawnTool)

				// Also register the synchronous subagent tool
				subagentTool := tools.NewSubagentTool(subagentManager)
				subagentTool.SetSpawner(NewSubTurnSpawner(al))
				agent.Tools.Register(subagentTool)
			}
			if spawnStatusEnabled {
				agent.Tools.Register(tools.NewSpawnStatusTool(subagentManager))
			}
		} else if (spawnEnabled || spawnStatusEnabled) && !cfg.Tools.IsToolEnabled("subagent") {
			logger.WarnCF("agent", "spawn/spawn_status tools require subagent to be enabled", nil)
		}

		// Register delegate tool for multi-agent setups.
		// Auto-enabled when multiple agents exist. Delegation uses the SubTurn
		// mechanism directly (not SubagentManager) and is independent of the
		// subagent tool.
		if len(registry.ListAgentIDs()) > 1 {
			delegateTool := tools.NewDelegateTool()
			delegateTool.SetSpawner(NewSubTurnSpawner(al))
			currentAgentID := agentID
			delegateTool.SetSelfAgentID(currentAgentID)
			delegateTool.SetAllowlistChecker(func(targetAgentID string) bool {
				return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
			})
			agent.Tools.Register(delegateTool)
		}

		warnOnUnknownAgentToolDeclarations(agentID, agent.Workspace, agent.Definition, agent.Tools)
	}
}
