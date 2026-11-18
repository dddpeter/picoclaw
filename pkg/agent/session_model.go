// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// SessionModel is a session-scoped model override: the model a single
// conversation runs on, chosen at runtime without touching the config file
// (config.json keeps its own default model).
//
// The override is deliberately NOT stored on the AgentInstance's model
// fields: the agent's model/provider/window are agent-global, and a
// per-session choice must not leak into other conversations served by the
// same agent. Instead the override is resolved per turn (see
// resolveSessionModel) and the resolved candidates/provider/window travel on
// the turn execution.
type SessionModel struct {
	// ModelName is the configured model_list entry name the session runs on.
	ModelName string
	// ContextWindow is the effective (gating) context window of the chosen
	// model, and DeclaredContextWindow the one reported to UIs.
	ContextWindow         int
	DeclaredContextWindow int
	// CompressAtTokens is the compression threshold for the chosen model
	// (window minus the output reserve) — the number the context ring shows
	// as "compress at".
	CompressAtTokens int

	// provider is the provider instance serving the model. Created lazily
	// on the session's first turn and reused afterwards (creating one per
	// turn would rebuild an HTTP client per message). It is closed when the
	// override is replaced or cleared.
	provider providers.LLMProvider
}

// sessionModelRegistry stores per-session model overrides.
//
// Persistence: overrides are in-memory only, exactly like /switch (whose
// state is documented as in-memory in pkg/agent/restart_recovery.go). A
// restart drops the session's model choice back to the config default —
// the same contract /switch already has, and the reason a session-scoped
// model is a safe thing to add: nothing on disk has to be reconciled.
//
// Eviction: entries are bounded by maxSessionModelEntries (LRU). Session
// deletion can happen in another process (the web backend removes session
// files directly, without a gateway round-trip), so the gateway cannot hook
// delete events; the LRU cap bounds both memory and cached providers
// instead. /new clears the live session's entry explicitly (same session
// key survives rotation), so the realistic unbounded growth path — users
// starting new conversations forever — is covered from both sides.
type sessionModelRegistry struct {
	mu         sync.Mutex
	entries    map[string]*SessionModel
	lastUsed   map[string]uint64
	seq        uint64 // monotonic LRU clock — wall clocks are too coarse for sub-ms touches
	maxEntries int
	// inUse reports whether a turn is currently running on the session (nil
	// = never in use). LRU eviction must not close a provider a live turn is
	// using: a long turn touches its entry only at resolve time, so 63 other
	// sessions' writes can push it to the head of the eviction order before
	// the turn finishes. Wired by AgentLoop to activeTurnStates.
	inUse func(sessionKey string) bool
}

// maxSessionModelEntries caps the registry. Well above any realistic
// concurrent-session count; the LRU evicts stale entries so long-running
// gateways cannot accumulate unbounded overrides.
const maxSessionModelEntries = 64

func newSessionModelRegistry() *sessionModelRegistry {
	return &sessionModelRegistry{
		entries:    make(map[string]*SessionModel),
		lastUsed:   make(map[string]uint64),
		maxEntries: maxSessionModelEntries,
	}
}

// touch records a use of sessionKey for LRU eviction. Caller holds r.mu.
func (r *sessionModelRegistry) touch(sessionKey string) {
	if r.lastUsed == nil {
		r.lastUsed = make(map[string]uint64)
	}
	r.seq++
	r.lastUsed[sessionKey] = r.seq
}

// evictLRU removes the least recently used entry (and closes its provider)
// when the registry is over capacity. Caller holds r.mu. neverKey is the
// entry being inserted and is never evicted; entries whose session has an
// active turn (r.inUse) are spared — closing their provider would pull the
// provider out from under the running turn (review P2-1). The registry may
// temporarily stay over capacity when every other entry is in use; the next
// write retries.
func (r *sessionModelRegistry) evictLRU(neverKey string) {
	for len(r.entries) > r.maxEntries {
		oldestKey := ""
		var oldest uint64
		for key, used := range r.lastUsed {
			if key == neverKey {
				continue
			}
			if r.inUse != nil && r.inUse(key) {
				continue
			}
			if oldestKey == "" || used < oldest {
				oldestKey = key
				oldest = used
			}
		}
		if oldestKey == "" {
			return
		}
		closeSessionModelProvider(r.entries[oldestKey])
		delete(r.entries, oldestKey)
		delete(r.lastUsed, oldestKey)
	}
}

// Set stores the override for a session. An empty model name clears the
// override (the session falls back to the agent's configured model).
//
// Replacing an override closes the previously cached provider: the old
// model may hold live upstream connections that would otherwise leak. The
// caller must guarantee no turn is using it — SetSessionModel rejects
// session-model writes while the session has an active turn.
func (r *sessionModelRegistry) Set(sessionKey string, model *SessionModel) {
	if r == nil {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]*SessionModel)
	}
	if r.lastUsed == nil {
		r.lastUsed = make(map[string]uint64)
	}
	if model == nil || strings.TrimSpace(model.ModelName) == "" {
		closeSessionModelProvider(r.entries[sessionKey])
		delete(r.entries, sessionKey)
		delete(r.lastUsed, sessionKey)
		return
	}
	stored := *model
	stored.ModelName = strings.TrimSpace(stored.ModelName)
	closeSessionModelProvider(r.entries[sessionKey])
	r.entries[sessionKey] = &stored
	r.touch(sessionKey)
	r.evictLRU(sessionKey)
}

// Get returns the override for a session, or nil when the session has none.
// The read refreshes the entry's LRU timestamp, so polling paths (context
// usage, idle compaction) keep live sessions from being evicted.
func (r *sessionModelRegistry) Get(sessionKey string) *SessionModel {
	if r == nil {
		return nil
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	model, ok := r.entries[sessionKey]
	if !ok || model == nil {
		return nil
	}
	r.touch(sessionKey)
	copied := *model
	return &copied
}

// Clear removes the override for a session.
func (r *sessionModelRegistry) Clear(sessionKey string) {
	if r == nil {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	closeSessionModelProvider(r.entries[sessionKey])
	delete(r.entries, sessionKey)
	delete(r.lastUsed, sessionKey)
}

// UpdateWindows refreshes the cached windows of a session's override. The
// write is conditional on expectModel: between the caller's resolve and this
// update, a concurrent Set could have moved the entry to another model —
// stamping the old model's windows onto the new entry would misreport the
// UI's context ring and the compaction budget until the next turn (review
// P2-2). On mismatch this is a no-op; the new model's resolve refreshes the
// windows itself.
func (r *sessionModelRegistry) UpdateWindows(
	sessionKey string,
	expectModel string,
	contextWindow int,
	declaredContextWindow int,
) {
	if r == nil {
		return
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[sessionKey]
	if !ok || entry == nil || strings.TrimSpace(entry.ModelName) != expectModel {
		return
	}
	entry.ContextWindow = contextWindow
	entry.DeclaredContextWindow = declaredContextWindow
}

// errSessionModelOverrideChanged is returned by ProviderFor when the
// session's override was replaced (or removed) while the provider was being
// created: the caller must re-resolve instead of storing a provider built
// for a model the session no longer runs.
var errSessionModelOverrideChanged = errors.New("session model override changed during provider creation")

// ProviderFor returns the cached provider of a session's override, creating
// one via create() when none is cached — atomically under the registry
// lock.
//
// The atomicity closes the resolve-side TOCTOU: the previous
// Provider→create→SetProvider dance let a concurrent Set/Clear race in
// between, so two resolvers could both build a provider (one leaked) or a
// provider could be stored into an entry that had already moved to another
// model (wrong-model execution). Here the entry is re-checked under the
// same lock that stores the result; when the override moved, the sentinel
// error tells the caller to re-resolve rather than stash a mismatched
// provider.
//
// create() runs under the registry lock: provider construction is
// in-memory (HTTP client build), so holding the lock costs milliseconds at
// worst and removes the whole class of interleavings.
func (r *sessionModelRegistry) ProviderFor(
	sessionKey string,
	expectModel string,
	create func() (providers.LLMProvider, error),
) (providers.LLMProvider, error) {
	if r == nil {
		return nil, errSessionModelOverrideChanged
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil, errSessionModelOverrideChanged
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.entries[sessionKey]
	if !ok || entry == nil || strings.TrimSpace(entry.ModelName) != expectModel {
		return nil, errSessionModelOverrideChanged
	}
	if entry.provider != nil {
		r.touch(sessionKey)
		return entry.provider, nil
	}
	created, err := create()
	if err != nil {
		return nil, err
	}
	entry.provider = created
	r.touch(sessionKey)
	return created, nil
}

// Close releases every cached provider (gateway shutdown).
func (r *sessionModelRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.entries {
		closeSessionModelProvider(entry)
		delete(r.entries, key)
	}
	r.lastUsed = make(map[string]uint64)
}

// closeSessionModelProvider closes a cached provider if it is stateful.
func closeSessionModelProvider(entry *SessionModel) {
	if entry == nil || entry.provider == nil {
		return
	}
	closeUniqueStatefulProviders(entry.provider)
	entry.provider = nil
}

// resolvedSessionModel is the outcome of resolving a session's model
// override into the concrete values a turn needs.
type resolvedSessionModel struct {
	// ModelName is the display name of the chosen model (model_list entry
	// name, or the raw reference when the entry has no name).
	ModelName string
	// Candidates is the resolved candidate chain (primary + fallbacks).
	Candidates []providers.FallbackCandidate
	// Provider is the provider instance serving the primary candidate.
	Provider providers.LLMProvider
	// ModelConfig is the resolved model_list entry (nil when the model has
	// no entry — e.g. a raw provider/model reference).
	ModelConfig *config.ModelConfig
	// ContextWindow is the effective (gating) context window for the
	// chosen model, and DeclaredContextWindow the one reported to UIs.
	ContextWindow         int
	DeclaredContextWindow int

	// overrideChanged is true when the override moved underneath this
	// resolve attempt and the caller must retry against the fresh one. No
	// other field is meaningful in that case.
	overrideChanged bool
}

// resolveSessionModel turns a session's model override into the concrete
// candidates/provider/window a turn runs with.
//
// It never mutates the AgentInstance: the agent's model, provider and
// context window stay exactly as configured, so a session-scoped choice
// cannot leak into other conversations (and the config file is untouched).
//
// The window follows the same min() rule as swapAgentModelLocked (fork
// §24): the per-model entry value may only tighten the agent-level window,
// never raise its ceiling.
func (al *AgentLoop) resolveSessionModel(
	agent *AgentInstance,
	sessionKey string,
) *resolvedSessionModel {
	if al == nil || agent == nil {
		return nil
	}

	// The override can move between reading it and caching a provider for it
	// (a concurrent SetSessionModel on another session path, a Clear, an
	// eviction). ProviderFor detects that and reports the sentinel; re-resolve
	// against the fresh override. Bounded: a storm of concurrent switches
	// converges, and giving up only means this turn runs on the configured
	// default — the next turn picks the override up.
	const maxResolveAttempts = 3
	for attempt := 0; attempt < maxResolveAttempts; attempt++ {
		resolved := al.resolveSessionModelOnce(agent, sessionKey)
		if resolved == nil || !resolved.overrideChanged {
			return resolved
		}
	}
	logger.WarnCF("agent", "session model override kept changing during resolve; running on configured model this turn",
		map[string]any{"session_key": sessionKey})
	return nil
}

// resolveSessionModelOnce performs a single resolve attempt. resolvedSessionModel.overrideChanged
// is true when the override moved underneath and the caller should retry.
func (al *AgentLoop) resolveSessionModelOnce(
	agent *AgentInstance,
	sessionKey string,
) *resolvedSessionModel {
	override := al.sessionModels.Get(sessionKey)
	if override == nil {
		return nil
	}

	cfg := al.GetConfig()
	if cfg == nil {
		return nil
	}
	defaultProvider := effectiveDefaultProvider(cfg.Agents.Defaults.Provider)

	// The session model replaces the primary; the configured fallback chain
	// stays as the chain behind it (same shape as /switch, which rebuilds
	// candidates from the agent's fallbacks).
	candidates := resolveModelCandidates(cfg, defaultProvider, override.ModelName, agent.Fallbacks)
	if len(candidates) == 0 {
		logger.WarnCF("agent", "session model override does not resolve to any provider candidate",
			map[string]any{
				"session_key": sessionKey,
				"model":       override.ModelName,
			})
		return nil
	}

	modelCfg, err := resolvedCandidateModelConfig(cfg, candidates[0], agent.Workspace)
	if err != nil {
		logger.WarnCF("agent", "session model override config failed to resolve",
			map[string]any{
				"session_key": sessionKey,
				"model":       override.ModelName,
				"error":       err.Error(),
			})
		return nil
	}

	factory := providers.CreateProviderFromConfig
	if al.providerFactory != nil {
		factory = al.providerFactory
	}
	modelCfgForProvider := modelCfg
	provider, providerErr := al.sessionModels.ProviderFor(
		sessionKey,
		override.ModelName,
		func() (providers.LLMProvider, error) {
			created, _, createErr := factory(modelCfgForProvider)
			return created, createErr
		},
	)
	if providerErr != nil {
		if errors.Is(providerErr, errSessionModelOverrideChanged) {
			// The override moved while we were resolving: retry against the
			// fresh one (the caller loops).
			return &resolvedSessionModel{overrideChanged: true}
		}
		logger.WarnCF("agent", "session model override provider init failed",
			map[string]any{
				"session_key": sessionKey,
				"model":       override.ModelName,
				"error":       providerErr.Error(),
			})
		return nil
	}

	// Window: min(per-model entry, agent-level window) — same rule as the
	// /switch path (fork §24), so a session-scoped model cannot claim a
	// larger window than the agent was configured for.
	contextWindow, declaredContextWindow := agent.baseGatingWindow, agent.baseDeclaredWindow
	if agent.baseGatingWindow > 0 {
		contextWindow, declaredContextWindow = applyModelContextWindowCap(
			modelCfg.ContextWindow,
			agent.baseGatingWindow,
			agent.baseDeclaredWindow,
		)
	} else {
		// Hand-built agents (tests) have no base window: keep the agent's
		// current window rather than dropping to zero.
		contextWindow, declaredContextWindow = agent.ContextWindow, agent.DeclaredContextWindow
	}
	// Keep the cached window in sync with the agent's current base so the
	// usage snapshots (which read the registry) and the turn agree.
	al.sessionModels.UpdateWindows(sessionKey, override.ModelName, contextWindow, declaredContextWindow)

	return &resolvedSessionModel{
		ModelName:             resolvedCandidateModelName(candidates, override.ModelName),
		Candidates:            candidates,
		Provider:              provider,
		ModelConfig:           modelCfg,
		ContextWindow:         contextWindow,
		DeclaredContextWindow: declaredContextWindow,
	}
}

// SessionKeyForChannelSession resolves the opaque session key of a channel
// session (the pico session UUID the web chat addresses) so callers outside
// a turn (the session-model endpoint) can act on the same session the turn
// will use.
//
// The key is derived with the SAME routing machinery the inbound pump uses,
// so it matches the key the agent stores history under.
func (al *AgentLoop) SessionKeyForChannelSession(channel, chatID string) string {
	if al == nil {
		return ""
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	chatID = strings.TrimSpace(chatID)
	if channel == "" || chatID == "" {
		return ""
	}
	registry := al.GetRegistry()
	if registry == nil {
		return ""
	}
	inboundCtx := bus.InboundContext{
		Channel:  channel,
		ChatID:   chatID,
		ChatType: "direct",
	}
	route := registry.ResolveRoute(inboundCtx)
	allocation := session.AllocateRouteSession(session.AllocationInput{
		AgentID:       route.AgentID,
		Context:       inboundCtx,
		SessionPolicy: route.SessionPolicy,
	})
	return allocation.SessionKey
}

// AgentForSessionKey returns the agent instance owning a session (nil when
// no agent is registered).
func (al *AgentLoop) AgentForSessionKey(sessionKey string) *AgentInstance {
	if al == nil {
		return nil
	}
	return al.agentForSession(sessionKey)
}

// SetSessionModel assigns a model to one session without touching the
// config file or the agent's global model. An empty model name clears the
// override (the session falls back to the configured model).
//
// Session-model writes are rejected while the session has an active turn
// (the same contract /switch and /new already have): a turn holds the
// session's resolved provider for its whole duration, and replacing or
// clearing the entry would close that provider under the live turn — fatal
// for stateful providers. The gate mutex closes the check/apply race
// against turn registration.
func (al *AgentLoop) SetSessionModel(sessionKey, modelName string) error {
	if al == nil {
		return fmt.Errorf("agent loop is nil")
	}
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return fmt.Errorf("session key is required")
	}
	modelName = strings.TrimSpace(modelName)

	if modelName == "" {
		return al.applySessionModelOverride(sessionKey, nil)
	}

	cfg := al.GetConfig()
	if cfg == nil {
		return fmt.Errorf("config unavailable")
	}
	// The model must exist in the configured model_list: a session-scoped
	// model is a choice among CONFIGURED models, never an ad-hoc provider.
	found := false
	for _, modelCfg := range cfg.ModelList {
		if modelCfg != nil && strings.TrimSpace(modelCfg.ModelName) == modelName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q not found in model_list", modelName)
	}

	agent := al.agentForSession(sessionKey)
	contextWindow, declaredContextWindow := 0, 0
	if agent != nil {
		contextWindow, declaredContextWindow = al.sessionModelWindows(agent, modelName)
	}
	err := al.applySessionModelOverride(sessionKey, &SessionModel{
		ModelName:             modelName,
		ContextWindow:         contextWindow,
		DeclaredContextWindow: declaredContextWindow,
	})
	if err != nil {
		return err
	}
	logger.InfoCF("agent", "session model override set",
		map[string]any{
			"session_key": sessionKey,
			"model":       modelName,
		})
	return nil
}

// errSessionBusy is returned by applySessionModelOverride (and surfaced by
// SetSessionModel) when the session has an active turn.
var errSessionBusy = errors.New("session is busy: a turn is active, try again after it completes")

// applySessionModelOverride atomically checks turn activity and stores (or,
// with a nil model, clears) the override.
//
// LOCK DISCIPLINE: takes al.sessionModelGate, the same mutex
// registerActiveTurn holds while publishing a turn to activeTurnStates —
// without it, a write could pass the busy check just before a turn
// registers, then close the provider the turn resolves moments later.
func (al *AgentLoop) applySessionModelOverride(sessionKey string, model *SessionModel) error {
	al.sessionModelGate.Lock()
	defer al.sessionModelGate.Unlock()
	if al.getActiveTurnState(sessionKey) != nil {
		return errSessionBusy
	}
	al.sessionModels.Set(sessionKey, model)
	return nil
}

// GetSessionModel returns the model currently assigned to a session (empty
// when the session follows the configured model).
func (al *AgentLoop) GetSessionModel(sessionKey string) string {
	if al == nil {
		return ""
	}
	override := al.sessionModels.Get(sessionKey)
	if override == nil {
		return ""
	}
	return override.ModelName
}

// SessionModelSnapshot returns the session's model override with its
// resolved windows (nil when the session follows the configured model).
func (al *AgentLoop) SessionModelSnapshot(sessionKey string) *SessionModel {
	if al == nil {
		return nil
	}
	return al.sessionModels.Get(sessionKey)
}

// sessionWindowOverride returns the session's effective context window
// override (0 when the session follows the configured model).
//
// Lock-free: safe to call from inside a live turn (the model-state read lock
// is already held there, and this touches only the session registry).
func (al *AgentLoop) sessionWindowOverride(sessionKey string) int {
	if al == nil {
		return 0
	}
	override := al.sessionModels.Get(sessionKey)
	if override == nil {
		return 0
	}
	return override.ContextWindow
}

// sessionDeclaredWindowOverride returns the session's declared context
// window override (0 when the session follows the configured model).
//
// Lock-free: safe to call from inside a live turn.
func (al *AgentLoop) sessionDeclaredWindowOverride(sessionKey string) int {
	if al == nil {
		return 0
	}
	override := al.sessionModels.Get(sessionKey)
	if override == nil {
		return 0
	}
	return override.DeclaredContextWindow
}

// sessionBudgetWithOverride returns the session's compaction budget, honoring
// a session-scoped model window.
//
// Lock-free: reads the agent's window fields directly, so it MUST only be
// called from inside a live turn (where the model-state read lock is
// already held). Outside a turn use SessionCompactionBudget.
func (al *AgentLoop) sessionBudgetWithOverride(agent *AgentInstance, sessionKey string) int {
	if agent == nil {
		return 0
	}
	window := al.sessionWindowOverride(sessionKey)
	if window <= 0 {
		return agent.liveCompactionBudget()
	}
	budget := window - agent.MaxTokens
	if budget <= 0 {
		budget = window / 2
	}
	return budget
}

// SessionCompactionBudget returns the compaction budget for a session,
// honoring a session-scoped model window (the session runs against its own
// window, so the budget must follow it).
//
// LOCK DISCIPLINE: this reads the agent's window fields, so it MUST only be
// called from OUTSIDE a live turn. runTurn holds the model-state read lock
// for the whole turn; a nested RLock from the turn goroutine deadlocks
// against Close()'s blocking write lock. In-turn callers use
// ts.effectiveCompactionBudget() instead.
func (al *AgentLoop) SessionCompactionBudget(agent *AgentInstance, sessionKey string) int {
	if al == nil || agent == nil {
		return 0
	}
	override := al.sessionModels.Get(sessionKey)
	if override == nil || override.ContextWindow <= 0 {
		return agent.snapshotCompactionBudget()
	}
	budget := override.ContextWindow - agent.MaxTokens
	if budget <= 0 {
		budget = override.ContextWindow / 2
	}
	return budget
}

// SessionContextUsage computes the context usage for a session, honoring a
// session-scoped model override (its window is what the session runs
// against, so the reported total matches the model in use).
//
// LOCK DISCIPLINE: like SessionCompactionBudget, this takes the model-state
// read lock, so it MUST only be called from OUTSIDE a live turn. In-turn
// callers use the turn's own snapshot (computeContextUsageWithWindows with
// the turn's effective window).
func (al *AgentLoop) SessionContextUsage(agent *AgentInstance, sessionKey string) *bus.ContextUsage {
	if al == nil || agent == nil {
		return nil
	}
	override := al.sessionModels.Get(sessionKey)
	if override == nil {
		return agent.snapshotContextUsage(sessionKey)
	}
	return agent.snapshotContextUsageWithWindows(
		sessionKey,
		override.ContextWindow,
		override.DeclaredContextWindow,
	)
}

// sessionModelWindows computes the effective (gating) and declared windows
// of a model_list entry for an agent: the agent's own base window (after the
// sanity clamp) min-capped by the model's context_window — the same rule the
// /switch path applies (fork §24).
func (al *AgentLoop) sessionModelWindows(agent *AgentInstance, modelName string) (int, int) {
	cfg := al.GetConfig()
	if cfg == nil {
		return 0, 0
	}
	modelWindow := 0
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || strings.TrimSpace(modelCfg.ModelName) != modelName {
			continue
		}
		modelWindow = modelCfg.ContextWindow
		break
	}
	if agent != nil && agent.baseGatingWindow > 0 {
		return applyModelContextWindowCap(
			modelWindow,
			agent.baseGatingWindow,
			agent.baseDeclaredWindow,
		)
	}
	// No agent base (hand-built agent or unknown session): fall back to the
	// config-derived window.
	gating, declared := defaultAgentContextWindows(cfg)
	if modelWindow <= 0 {
		return gating, declared
	}
	return min(modelWindow, gating), min(modelWindow, declared)
}

// defaultAgentContextWindows mirrors the agent's own context-window default
// and sanity clamp: the derived window (4x the output limit, floored at
// 256k) or the configured one, with the configured value clamped to 256k
// unless trust_configured_context_window says otherwise. The declared
// window keeps the pre-clamp value — the same split the agent uses.
func defaultAgentContextWindows(cfg *config.Config) (gating int, declared int) {
	if cfg == nil {
		return 0, 0
	}
	defaults := cfg.Agents.Defaults
	window := defaults.ContextWindow
	if window <= 0 {
		maxTokens := defaults.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 8192
		}
		window = max(maxTokens*4, 256_000)
	}
	declared = window
	if defaults.ContextWindow > 256_000 && !defaults.TrustConfiguredContextWindow {
		window = 256_000
	}
	return window, declared
}
