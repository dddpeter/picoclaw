package config

import (
	"encoding/json"
	"slices"
	"time"
)

type AgentsConfig struct {
	Defaults AgentDefaults   `json:"defaults"`
	List     []AgentConfig   `json:"list,omitempty"`
	Dispatch *DispatchConfig `json:"dispatch,omitempty"`
}

// AgentModelConfig supports both string and structured model config.
// String format: "gpt-4" (just primary, no fallbacks)
// Object format: {"primary": "gpt-4", "fallbacks": ["claude-haiku"]}
type AgentModelConfig struct {
	Primary   string   `json:"primary,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

func (m *AgentModelConfig) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Primary = s
		m.Fallbacks = nil
		return nil
	}
	type raw struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Primary = r.Primary
	m.Fallbacks = r.Fallbacks
	return nil
}

func (m AgentModelConfig) MarshalJSON() ([]byte, error) {
	if len(m.Fallbacks) == 0 && m.Primary != "" {
		return json.Marshal(m.Primary)
	}
	type raw struct {
		Primary   string   `json:"primary,omitempty"`
		Fallbacks []string `json:"fallbacks,omitempty"`
	}
	return json.Marshal(raw{Primary: m.Primary, Fallbacks: m.Fallbacks})
}

type AgentConfig struct {
	ID        string            `json:"id"`
	Default   bool              `json:"default,omitempty"`
	Name      string            `json:"name,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Model     *AgentModelConfig `json:"model,omitempty"`
	Skills    []string          `json:"skills,omitempty"`
	Subagents *SubagentsConfig  `json:"subagents,omitempty"`
}

type SubagentsConfig struct {
	AllowAgents []string          `json:"allow_agents,omitempty"`
	Model       *AgentModelConfig `json:"model,omitempty"`
}

type DispatchConfig struct {
	Rules []DispatchRule `json:"rules,omitempty"`
}

type DispatchRule struct {
	Name              string           `json:"name,omitempty"`
	Agent             string           `json:"agent"`
	When              DispatchSelector `json:"when"`
	SessionDimensions []string         `json:"session_dimensions,omitempty"`
}

type DispatchSelector struct {
	Channel   string `json:"channel,omitempty"`
	Account   string `json:"account,omitempty"`
	Space     string `json:"space,omitempty"`
	Chat      string `json:"chat,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Mentioned *bool  `json:"mentioned,omitempty"`
}

type SessionConfig struct {
	Dimensions    []string            `json:"dimensions,omitempty"`
	IdentityLinks map[string][]string `json:"identity_links,omitempty"`
	DmScope       string              `json:"dm_scope,omitempty"`
}

// ApplyDmScope translates the user-facing dm_scope value into the internal
// dimensions array that the routing layer consumes. It is a no-op when
// DmScope is empty or when Dimensions is already set (explicit Dimensions
// take precedence over the derived value).
func (s *SessionConfig) ApplyDmScope() {
	if s.DmScope == "" || len(s.Dimensions) > 0 {
		return
	}
	switch s.DmScope {
	case "per-channel-peer":
		s.Dimensions = []string{"chat", "sender"}
	case "per-channel":
		s.Dimensions = []string{"chat"}
	case "per-peer":
		s.Dimensions = []string{"sender"}
	case "global":
		s.Dimensions = nil
	}
}

// DeriveDmScope sets DmScope based on Dimensions when DmScope is empty.
// This handles legacy/fresh configs that only have explicit Dimensions
// without a corresponding DmScope value, ensuring the API response always
// includes a dm_scope that matches the actual runtime dimensions.
func (s *SessionConfig) DeriveDmScope() {
	if s.DmScope != "" || len(s.Dimensions) == 0 {
		return
	}
	switch {
	case slices.Equal(s.Dimensions, []string{"chat", "sender"}):
		s.DmScope = "per-channel-peer"
	case slices.Equal(s.Dimensions, []string{"chat"}):
		s.DmScope = "per-channel"
	case slices.Equal(s.Dimensions, []string{"sender"}):
		s.DmScope = "per-peer"
	}
	// Dimensions not matching any known scope mapping (custom array)
	// is fine — DmScope stays empty and the UI can handle it.
}

// RoutingConfig controls the intelligent model routing feature.
// When enabled, each incoming message is scored against structural features
// (message length, code blocks, tool call history, conversation depth, attachments).
// Messages scoring below Threshold are sent to LightModel; all others use the
// agent's primary model. This reduces cost and latency for simple tasks without
// requiring any keyword matching — all scoring is language-agnostic.
type RoutingConfig struct {
	Enabled    bool    `json:"enabled"`
	LightModel string  `json:"light_model"` // model_name from model_list to use for simple tasks
	Threshold  float64 `json:"threshold"`   // complexity score in [0,1]; score >= threshold → primary model
}

// SubTurnConfig configures the SubTurn execution system.
type SubTurnConfig struct {
	MaxDepth              int `json:"max_depth"               env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_DEPTH"`
	MaxConcurrent         int `json:"max_concurrent"          env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_MAX_CONCURRENT"`
	DefaultTimeoutMinutes int `json:"default_timeout_minutes" env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TIMEOUT_MINUTES"`
	DefaultTokenBudget    int `json:"default_token_budget"    env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_DEFAULT_TOKEN_BUDGET"`
	ConcurrencyTimeoutSec int `json:"concurrency_timeout_sec" env:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_CONCURRENCY_TIMEOUT_SEC"`
}

type ToolFeedbackConfig struct {
	Enabled          bool `json:"enabled"           env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_ENABLED"`
	MaxArgsLength    int  `json:"max_args_length"   env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_MAX_ARGS_LENGTH"`
	SeparateMessages bool `json:"separate_messages" env:"PICOCLAW_AGENTS_DEFAULTS_TOOL_FEEDBACK_SEPARATE_MESSAGES"`
}

// IdleCompactConfig configures the idle-session compaction scanner.
// Mirrors the fork convention: block absent = defaults, Enabled nil = on.
type IdleCompactConfig struct {
	// Enabled gates the scanner. Nil (unset) = enabled.
	Enabled *bool `json:"enabled,omitempty"`
	// IdleAfterMinutes is how long a session must be idle before the
	// scanner considers it (default 30, clamped to >= 5).
	IdleAfterMinutes int `json:"idle_after_minutes,omitempty"`
	// ScanIntervalSeconds is the scanner tick (default 300, clamped to >= 60).
	ScanIntervalSeconds int `json:"scan_interval_seconds,omitempty"`
	// MinHistoryMessages gates out sessions too small to be worth a
	// summarization call (default 12).
	MinHistoryMessages int `json:"min_history_messages,omitempty"`
}

// EffectiveEnabled reports whether the idle scanner is on (nil = on).
func (c *IdleCompactConfig) EffectiveEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}

// EffectiveIdleAfter returns the idle threshold with its floor applied.
func (c *IdleCompactConfig) EffectiveIdleAfter() time.Duration {
	mins := 30
	if c != nil && c.IdleAfterMinutes > 0 {
		mins = c.IdleAfterMinutes
	}
	if mins < 5 {
		mins = 5
	}
	return time.Duration(mins) * time.Minute
}

// EffectiveScanInterval returns the tick interval with its floor applied.
func (c *IdleCompactConfig) EffectiveScanInterval() time.Duration {
	secs := 300
	if c != nil && c.ScanIntervalSeconds > 0 {
		secs = c.ScanIntervalSeconds
	}
	if secs < 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second
}

// EffectiveMinHistoryMessages returns the minimum-worth gate (default 12).
func (c *IdleCompactConfig) EffectiveMinHistoryMessages() int {
	if c == nil || c.MinHistoryMessages <= 0 {
		return 12
	}
	return c.MinHistoryMessages
}

// SplitTurnConfig configures split-turn prefix summarization: when the
// active turn's tail alone exceeds the window after regular compaction,
// summarize the older prefix of the turn and keep only the recent tail in
// the request (see docs/design/split-turn-compaction-design.zh.md).
type SplitTurnConfig struct {
	// Enabled gates split-turn summarization. Nil (unset) = enabled —
	// the fork's opt-out-by-config convention.
	Enabled *bool `json:"enabled,omitempty"`
	// KeepRecentTokens is the token budget of the preserved turn tail
	// (default 8192).
	KeepRecentTokens int `json:"keep_recent_tokens,omitempty"`
}

// EffectiveEnabled reports whether split-turn is on (nil = on).
func (c *SplitTurnConfig) EffectiveEnabled() bool {
	return c == nil || c.Enabled == nil || *c.Enabled
}

// DefaultSplitTurnKeepTokens is the canonical split-turn keep-tail budget
// default (single source; the agent-side cut-point fallback references it
// too — review P3-3 of the 2026-10-05 series).
const DefaultSplitTurnKeepTokens = 8192

// EffectiveKeepRecentTokens returns the tail budget.
func (c *SplitTurnConfig) EffectiveKeepRecentTokens() int {
	if c == nil || c.KeepRecentTokens <= 0 {
		return DefaultSplitTurnKeepTokens
	}
	return c.KeepRecentTokens
}

type AgentDefaults struct {
	Workspace                 string   `json:"workspace"                                  env:"PICOCLAW_AGENTS_DEFAULTS_WORKSPACE"`
	RestrictToWorkspace       bool     `json:"restrict_to_workspace"                      env:"PICOCLAW_AGENTS_DEFAULTS_RESTRICT_TO_WORKSPACE"`
	AllowReadOutsideWorkspace bool     `json:"allow_read_outside_workspace"               env:"PICOCLAW_AGENTS_DEFAULTS_ALLOW_READ_OUTSIDE_WORKSPACE"`
	Provider                  string   `json:"provider"                                   env:"PICOCLAW_AGENTS_DEFAULTS_PROVIDER"`
	ModelName                 string   `json:"model_name"                                 env:"PICOCLAW_AGENTS_DEFAULTS_MODEL_NAME"`
	ModelFallbacks            []string `json:"model_fallbacks,omitempty"`
	ImageModel                string   `json:"image_model,omitempty"                      env:"PICOCLAW_AGENTS_DEFAULTS_IMAGE_MODEL"`
	ImageModelFallbacks       []string `json:"image_model_fallbacks,omitempty"`
	MaxTokens                 int      `json:"max_tokens"                                 env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOKENS"`
	ContextWindow             int      `json:"context_window,omitempty"                   env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_WINDOW"`
	Temperature               *float64 `json:"temperature,omitempty"                      env:"PICOCLAW_AGENTS_DEFAULTS_TEMPERATURE"`
	MaxToolIterations         int      `json:"max_tool_iterations"                        env:"PICOCLAW_AGENTS_DEFAULTS_MAX_TOOL_ITERATIONS"`
	// AutoContinueTurns is the number of extra turns spawned after a turn
	// ends at max_tool_iterations without a final answer (fork feature:
	// long-task auto-continue). 0 disables it.
	AutoContinueTurns int `json:"auto_continue_turns,omitempty"              env:"PICOCLAW_AGENTS_DEFAULTS_AUTO_CONTINUE_TURNS"`
	// AnswerContinuationLimit caps the auto-continuation rounds that stitch
	// a direct answer cut by an output-token cap (server-side channel caps
	// surface as finish_reason=length regardless of the requested
	// max_tokens). nil = default (3); 0 disables continuation (the answer
	// ends with the truncation note immediately).
	AnswerContinuationLimit *int `json:"answer_continuation_limit,omitempty"        env:"PICOCLAW_AGENTS_DEFAULTS_ANSWER_CONTINUATION_LIMIT"`
	// ProgressHeartbeatSeconds is the idle interval for the long-task
	// progress heartbeat on streaming cards (fork feature). 0 disables it.
	ProgressHeartbeatSeconds int `json:"progress_heartbeat_seconds,omitempty"       env:"PICOCLAW_AGENTS_DEFAULTS_PROGRESS_HEARTBEAT_SECONDS"`
	// ProgressStallInterruptSeconds is the idle threshold after which the
	// stall watchdog requests a graceful interrupt (plus provider-call
	// cancel) so a hung turn can finalize and release the session. If the
	// turn still shows no activity one heartbeat interval later, a hard
	// abort follows. 0 disables the watchdog (fork feature).
	ProgressStallInterruptSeconds int `json:"progress_stall_interrupt_seconds,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_PROGRESS_STALL_INTERRUPT_SECONDS"`
	// AskTimeoutMS bounds one ask_user wait before it soft-fails as "user
	// did not answer" (octop borrowing §三; fork feature). Unlike the
	// approval timeout this is a UX bound, not a safety gate: expiry lets
	// the model continue with a safe default. 0/negative = default
	// (10 minutes, agent.defaultAskUserTimeout).
	AskTimeoutMS              int                 `json:"ask_timeout_ms,omitempty"                   env:"PICOCLAW_AGENTS_DEFAULTS_ASK_TIMEOUT_MS"`
	SummarizeMessageThreshold int                 `json:"summarize_message_threshold"                env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_MESSAGE_THRESHOLD"`
	SummarizeTokenPercent     int                 `json:"summarize_token_percent"                    env:"PICOCLAW_AGENTS_DEFAULTS_SUMMARIZE_TOKEN_PERCENT"`
	MaxMediaSize              int                 `json:"max_media_size,omitempty"                   env:"PICOCLAW_AGENTS_DEFAULTS_MAX_MEDIA_SIZE"`
	MaxContextImages          int                 `json:"max_context_images,omitempty"               env:"PICOCLAW_AGENTS_DEFAULTS_MAX_CONTEXT_IMAGES"`
	Routing                   *RoutingConfig      `json:"routing,omitempty"`
	SteeringMode              string              `json:"steering_mode,omitempty"                    env:"PICOCLAW_AGENTS_DEFAULTS_STEERING_MODE"`      // "one-at-a-time" (default) or "all"
	MaxParallelTurns          int                 `json:"max_parallel_turns"                         env:"PICOCLAW_AGENTS_DEFAULTS_MAX_PARALLEL_TURNS"` // Max concurrent turns (0 or 1 = sequential)
	SubTurn                   SubTurnConfig       `json:"subturn"                                                                                                    envPrefix:"PICOCLAW_AGENTS_DEFAULTS_SUBTURN_"`
	ToolFeedback              ToolFeedbackConfig  `json:"tool_feedback,omitempty"`
	SplitOnMarker             bool                `json:"split_on_marker"                            env:"PICOCLAW_AGENTS_DEFAULTS_SPLIT_ON_MARKER"` // split messages on <|[SPLIT]|> marker
	ContextManager            string              `json:"context_manager,omitempty"                  env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER"`
	ContextManagerConfig      json.RawMessage     `json:"context_manager_config,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER_CONFIG"`
	TurnProfile               TurnProfileConfig   `json:"turn_profile,omitempty"`
	MaxLLMRetries             int                 `json:"max_llm_retries,omitempty"                  env:"PICOCLAW_AGENTS_DEFAULTS_MAX_LLM_RETRIES"`
	LLMRetryBackoffSecs       int                 `json:"llm_retry_backoff_secs,omitempty"           env:"PICOCLAW_AGENTS_DEFAULTS_LLM_RETRY_BACKOFF_SECS"`
	LoopDetection             LoopDetectionConfig `json:"loop_detection,omitempty"`
	// RestartRecovery controls post-restart sealing/notification for sessions
	// whose turn was interrupted by a gateway restart (fork feature).
	RestartRecovery RestartRecoveryConfig `json:"restart_recovery,omitempty"`
	// SessionTitles controls two-phase session titling (fork feature, see
	// docs/design/hermes-borrowing-analysis.zh.md §二). Absent block = enabled.
	SessionTitles SessionTitleConfig `json:"session_titles,omitempty"`
	// ProjectDocs lists workspace-root markdown files auto-injected into the
	// system prompt (AGENTS.md, README.md, ...). Absent from the config file
	// means the default list; an explicit empty list disables the section.
	ProjectDocs []string `json:"project_docs"`
	// CompactUsageThreshold gates post-turn context compaction: raw
	// history is kept until used tokens reach this fraction of the
	// effective window (pi-style: compact only under real pressure, not
	// every turn). 0 = default 0.75. FreshTailMessages additionally
	// controls how many recent messages seahorse never summarizes (0 =
	// default 128).
	CompactUsageThreshold float64 `json:"compact_usage_threshold,omitempty"          env:"PICOCLAW_AGENTS_DEFAULTS_COMPACT_USAGE_THRESHOLD"`
	// CompactToolTriggerRatio is the usage ratio at which the model-initiated
	// compact_context tool agrees to request compaction. 0 = half the compact
	// usage threshold (agentscope-go borrowing §二: the automatic path already
	// fires at the full threshold, an identical tool threshold would leave the
	// tool nothing to do).
	CompactToolTriggerRatio float64 `json:"compact_tool_trigger_ratio,omitempty"       env:"PICOCLAW_AGENTS_DEFAULTS_COMPACT_TOOL_TRIGGER_RATIO"`
	// SplitTurn configures mid-turn prefix summarization for long heavy
	// turns whose active-turn tail alone overflows the context window
	// (fork feature). Nil = enabled with default keep_recent_tokens.
	SplitTurn *SplitTurnConfig `json:"split_turn,omitempty"`
	// IdleCompact configures the background idle-session compaction scanner
	// (fork feature, docs/design/idle-compaction-design.zh.md): sessions
	// idle past the threshold get a real summarization pass so the next
	// question starts from a compacted steady state. Nil = enabled with
	// defaults.
	IdleCompact       *IdleCompactConfig `json:"idle_compact,omitempty"`
	FreshTailMessages int                `json:"fresh_tail_messages,omitempty"              env:"PICOCLAW_AGENTS_DEFAULTS_FRESH_TAIL_MESSAGES"`
	// TrustConfiguredContextWindow opts out of the context-window sanity clamp
	// (fork feature). agents.defaults.context_window is often set from
	// marketing numbers (e.g. 1M) while the real model window is far smaller;
	// every compaction gate then trusts the inflated value and never fires,
	// which surfaces as silent empty responses on long sessions. Default
	// (false): a configured window above 256k is clamped to 256k and a
	// warning is logged. Set true only for models whose real window truly
	// exceeds the clamp.
	TrustConfiguredContextWindow bool `json:"trust_configured_context_window,omitempty"  env:"PICOCLAW_AGENTS_DEFAULTS_TRUST_CONFIGURED_CONTEXT_WINDOW"`
	// CooldownEnabled disables per-candidate failure cooldowns when false
	// (fork feature). Nil (omitted) keeps cooldowns enabled — the default
	// protects against 429 storms; personal deployments that prefer
	// always-try semantics can turn it off. Independent of the fallback
	// chain's exhaustion bypass (all-candidates-cooling still gets one
	// forced attempt either way).
	CooldownEnabled *bool `json:"cooldown_enabled,omitempty"                 env:"PICOCLAW_AGENTS_DEFAULTS_COOLDOWN_ENABLED"`
}

// EffectiveCooldownEnabled reports whether provider failure cooldowns are
// active; omitted = enabled (fork convention for escape hatches).
func (d *AgentDefaults) EffectiveCooldownEnabled() bool {
	if d == nil || d.CooldownEnabled == nil {
		return true
	}
	return *d.CooldownEnabled
}

// RestartRecoveryConfig controls post-restart sealing/notification for
// sessions whose turn was interrupted by a gateway restart (fork feature,
// pkg/agent/restart_recovery.go).
type RestartRecoveryConfig struct {
	// Enabled is nil when unset, which means enabled (fork convention).
	Enabled *bool `json:"enabled,omitempty"                   env:"PICOCLAW_AGENTS_DEFAULTS_RESTART_RECOVERY_ENABLED"`
	// NotifyWindowHours limits notifications to sessions whose on-disk record
	// changed within this window. 0 disables notifications (sealing still
	// happens). This is a freshness filter, NOT a repeat interval.
	NotifyWindowHours int `json:"notify_window_hours,omitempty"       env:"PICOCLAW_AGENTS_DEFAULTS_RESTART_RECOVERY_NOTIFY_WINDOW_HOURS"`
	// ReminderIntervalMinutes re-notifies sessions that received no user
	// message after the first notification. 0 disables re-reminders.
	ReminderIntervalMinutes int `json:"reminder_interval_minutes,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_RESTART_RECOVERY_REMINDER_INTERVAL_MINUTES"`
	// ReminderMax caps re-reminders per session.
	ReminderMax int `json:"reminder_max,omitempty"              env:"PICOCLAW_AGENTS_DEFAULTS_RESTART_RECOVERY_REMINDER_MAX"`
}

// IsEnabled reports whether restart recovery runs; absent block = enabled.
func (c RestartRecoveryConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// LoopDetectionConfig controls the low-progress loop detector (borrowed from
// MiMoCode's Try-Best, see docs/design/mimo-code-borrowing-analysis.zh.md).
// On a hit the turn is not killed; a warning is injected into the tool result
// so the model can replan on its own.
type LoopDetectionConfig struct {
	// Enabled is nil when unset, which means enabled — existing configs
	// without the loop_detection block must not silently turn it off.
	Enabled             *bool `json:"enabled,omitempty"     env:"PICOCLAW_AGENTS_DEFAULTS_LOOP_DETECTION_ENABLED"`
	BashRetryThreshold  int   `json:"bash_retry_threshold"  env:"PICOCLAW_AGENTS_DEFAULTS_LOOP_DETECTION_BASH_RETRY_THRESHOLD"`  // consecutive identical failed commands
	EditStreakThreshold int   `json:"edit_streak_threshold" env:"PICOCLAW_AGENTS_DEFAULTS_LOOP_DETECTION_EDIT_STREAK_THRESHOLD"` // consecutive edit-class calls
}

// SessionTitleConfig controls two-phase session titling: a deterministic
// derived title at turn start, upgraded in the background by a light model;
// manual /title always wins (source priority user > llm > derived).
type SessionTitleConfig struct {
	// Enabled is nil when unset, which means enabled — existing configs
	// without the session_titles block must not silently turn it off.
	Enabled *bool `json:"enabled,omitempty" env:"PICOCLAW_AGENTS_DEFAULTS_SESSION_TITLES_ENABLED"`
}

// SessionTitlesEnabled reports whether session titling is active (nil = on).
func (d *AgentDefaults) SessionTitlesEnabled() bool {
	if d == nil {
		return true
	}
	return d.SessionTitles.Enabled == nil || *d.SessionTitles.Enabled
}

// EffectiveLoopDetection returns thresholds with defaults filled in and a
// resolved Enabled flag (nil counts as enabled).
func (d *AgentDefaults) EffectiveLoopDetection() LoopDetectionConfig {
	lc := d.LoopDetection
	if lc.Enabled == nil {
		enabled := true
		lc.Enabled = &enabled
	}
	if lc.BashRetryThreshold <= 0 {
		lc.BashRetryThreshold = 3
	}
	if lc.EditStreakThreshold <= 0 {
		lc.EditStreakThreshold = 4
	}
	return lc
}

// IsEnabled reports whether loop detection should run for an
// agent (nil-safe: zero value means enabled with default thresholds).
func (c LoopDetectionConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

const DefaultMaxMediaSize = 20 * 1024 * 1024 // 20 MB

func (d *AgentDefaults) GetMaxMediaSize() int {
	if d.MaxMediaSize > 0 {
		return d.MaxMediaSize
	}
	return DefaultMaxMediaSize
}

// DefaultMaxContextImages caps how many current-turn tool-result images are
// base64-injected into the model context; older ones degrade to [image:/path]
// tags the model can re-load via load_image (fork feature, agentscope-go
// borrowing §三).
const DefaultMaxContextImages = 8

// GetMaxContextImages returns the per-turn cap on base64 tool images.
// 0 = default (8), negative = unlimited.
func (d *AgentDefaults) GetMaxContextImages() int {
	if d.MaxContextImages == 0 {
		return DefaultMaxContextImages
	}
	return d.MaxContextImages
}

// GetProgressHeartbeatSeconds returns the idle progress-heartbeat interval
// in seconds; <= 0 disables the heartbeat.
func (d *AgentDefaults) GetProgressHeartbeatSeconds() int {
	if d.ProgressHeartbeatSeconds < 0 {
		return 0
	}
	return d.ProgressHeartbeatSeconds
}

// GetProgressStallInterruptSeconds returns the stall-watchdog idle
// threshold in seconds; <= 0 disables the watchdog.
func (d *AgentDefaults) GetProgressStallInterruptSeconds() int {
	if d.ProgressStallInterruptSeconds < 0 {
		return 0
	}
	return d.ProgressStallInterruptSeconds
}

// GetAskTimeoutMS returns the ask_user wait bound in milliseconds; <= 0
// falls back to the 10-minute default (600000, agent.defaultAskUserTimeout).
func (d *AgentDefaults) GetAskTimeoutMS() int {
	if d == nil || d.AskTimeoutMS <= 0 {
		return 600000
	}
	return d.AskTimeoutMS
}

// GetAutoContinueTurns clamps negative values to 0 (disabled).
func (d *AgentDefaults) GetAutoContinueTurns() int {
	if d.AutoContinueTurns < 0 {
		return 0
	}
	return d.AutoContinueTurns
}

// GetAnswerContinuationLimit resolves the truncated-answer auto-continuation
// budget: nil = default 3; explicit 0 disables (negative is clamped to 0).
func (d *AgentDefaults) GetAnswerContinuationLimit() int {
	if d == nil || d.AnswerContinuationLimit == nil {
		return 3
	}
	if *d.AnswerContinuationLimit < 0 {
		return 0
	}
	return *d.AnswerContinuationLimit
}

// GetToolFeedbackMaxArgsLength returns the max visible text length for tool argument previews.
func (d *AgentDefaults) GetToolFeedbackMaxArgsLength() int {
	if d.ToolFeedback.MaxArgsLength > 0 {
		return d.ToolFeedback.MaxArgsLength
	}
	return 300
}

// IsToolFeedbackEnabled returns true when tool feedback messages should be sent to the chat.
func (d *AgentDefaults) IsToolFeedbackEnabled() bool {
	return d.ToolFeedback.Enabled
}

// IsToolFeedbackSeparateMessagesEnabled returns true when each tool feedback
// update should be sent as its own chat message instead of editing a single
// in-place progress message.
func (d *AgentDefaults) IsToolFeedbackSeparateMessagesEnabled() bool {
	return d.ToolFeedback.SeparateMessages
}

// GetModelName returns the effective model name for the agent defaults.
// It prefers the new "model_name" field but falls back to "model" for backward compatibility.
func (d *AgentDefaults) GetModelName() string {
	return d.ModelName
}
