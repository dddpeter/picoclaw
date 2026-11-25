package config

import (
	"encoding/json"
	"strings"
	"sync/atomic"
)

// rrCounter is a global counter for round-robin load balancing across models.
var rrCounter atomic.Uint64

// CurrentVersion is the latest config schema version
const CurrentVersion = 3

func init() {
	initChannel()
}

// Config is the current config structure with version support.
type Config struct {
	// Config schema version for migration.
	Version   int             `json:"version"              yaml:"-"`
	Isolation IsolationConfig `json:"isolation,omitempty"  yaml:"-"`
	Agents    AgentsConfig    `json:"agents"               yaml:"-"`
	Session   SessionConfig   `json:"session,omitempty"    yaml:"-"`
	Evolution EvolutionConfig `json:"evolution,omitempty"  yaml:"-"`
	Channels  ChannelsConfig  `json:"channel_list"         yaml:"channel_list"`
	ModelList SecureModelList `json:"model_list"           yaml:"model_list"` // New model-centric provider configuration
	Gateway   GatewayConfig   `json:"gateway"              yaml:"-"`
	Events    EventsConfig    `json:"events,omitempty"     yaml:"-"`
	Hooks     HooksConfig     `json:"hooks,omitempty"      yaml:"-"`
	Tools     ToolsConfig     `json:"tools"                yaml:",inline"`
	Heartbeat HeartbeatConfig `json:"heartbeat"            yaml:"-"`
	Devices   DevicesConfig   `json:"devices"              yaml:"-"`
	Voice     VoiceConfig     `json:"voice"                yaml:"-"`
	Memory    MemoryConfig    `json:"memory,omitempty"     yaml:"-"`
	Web       WebConfig       `json:"web,omitempty"        yaml:"-"`
	// BuildInfo contains build-time version information
	BuildInfo BuildInfo `json:"build_info,omitempty" yaml:"-"`

	// cache for sensitive values and compiled regex (computed once)
	sensitiveCache *SensitiveDataCache
}

// WebConfig scopes web-surface (launcher/pico) options. The web file
// endpoints apply a stricter posture than the agent file tools: reads are
// confined to the workspace plus these explicitly configured roots
// (design web-file-upload-preview §5.2).
type WebConfig struct {
	// FileAccessRoots lists extra host directories the web preview may
	// read. Entries may start with ~ (home-relative) or be relative to the
	// workspace root. Empty = workspace only.
	FileAccessRoots []string `json:"file_access_roots,omitempty" env:"PICOCLAW_WEB_FILE_ACCESS_ROOTS"`
}

// GetFileAccessRoots returns the configured web file-access roots.
func (c *Config) GetFileAccessRoots() []string {
	if c == nil || len(c.Web.FileAccessRoots) == 0 {
		return nil
	}
	return append([]string(nil), c.Web.FileAccessRoots...)
}

// MemoryConfig configures agent memory backends.
type MemoryConfig struct {
	// Recall enables shared-memory recall injection at turn start: the
	// user's message is used as a semantic query against an MCP memory
	// server (e.g. OpenViking's `search` tool) and matches are injected
	// into the system prompt's memory slot.
	Recall MemoryRecallConfig `json:"recall,omitempty"`
	// Commit enables session auto-commit: completed turns are pushed to an
	// MCP memory server (e.g. OpenViking's `remember` tool), whose async
	// extraction then distills long-term memories.
	Commit MemoryCommitConfig `json:"commit,omitempty"`
}

type MemoryRecallConfig struct {
	Enabled bool `json:"enabled"`
	// Server names the MCP server (under tools.mcp.servers) used for recall.
	Server string `json:"server"`
	// Tool is the recall tool name on that server. Defaults to "search".
	Tool string `json:"tool,omitempty"`
	// MaxChars caps injected recall text. Defaults to 2400.
	MaxChars int `json:"max_chars,omitempty"`
	// TimeoutMs bounds each recall call. Defaults to 3000.
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

type MemoryCommitConfig struct {
	Enabled bool `json:"enabled"`
	// Server names the MCP server (under tools.mcp.servers) used for commits.
	Server string `json:"server"`
	// Tool is the commit tool name on that server. Defaults to "remember".
	Tool string `json:"tool,omitempty"`
	// TimeoutMs bounds each commit call. Defaults to 5000.
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

type EvolutionConfig struct {
	Enabled         bool     `json:"enabled,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	StateDir        string   `json:"state_dir,omitempty"`
	MinTaskCount    int      `json:"min_task_count,omitempty"`
	MinSuccessRatio float64  `json:"min_success_ratio,omitempty"`
	ColdPathTrigger string   `json:"cold_path_trigger,omitempty"`
	ColdPathTimes   []string `json:"cold_path_times,omitempty"`
	// Lifecycle idle-day thresholds for skill status transitions. Zero or
	// negative values fall back to the defaults (90/180/365).
	ColdAfterDays    int `json:"cold_after_days,omitempty"`
	ArchiveAfterDays int `json:"archive_after_days,omitempty"`
	DeleteAfterDays  int `json:"delete_after_days,omitempty"`
	// AutomationSuggestionsEnabled gates the cron-automation suggestion
	// generator (evolution proposes; the user always accepts explicitly).
	// Nil means enabled whenever the mode reaches draft.
	AutomationSuggestionsEnabled *bool `json:"suggestions_enabled,omitempty"`
	// Deprecated: use MinTaskCount.
	MinCaseCount int `json:"min_case_count,omitempty"`
	// Deprecated: use MinSuccessRatio.
	MinSuccessRate float64 `json:"min_success_rate,omitempty"`
}

// EffectiveSuggestionsEnabled resolves the automation-suggestion gate.
func (c EvolutionConfig) EffectiveSuggestionsEnabled() bool {
	if c.AutomationSuggestionsEnabled != nil {
		return *c.AutomationSuggestionsEnabled
	}
	mode := c.EffectiveMode()
	return mode == "draft" || mode == "apply"
}

func (c EvolutionConfig) MarshalJSON() ([]byte, error) {
	out := struct {
		Enabled                      bool     `json:"enabled,omitempty"`
		Mode                         string   `json:"mode,omitempty"`
		StateDir                     string   `json:"state_dir,omitempty"`
		MinTaskCount                 int      `json:"min_task_count,omitempty"`
		MinSuccessRatio              float64  `json:"min_success_ratio,omitempty"`
		ColdPathTrigger              string   `json:"cold_path_trigger,omitempty"`
		ColdPathTimes                []string `json:"cold_path_times,omitempty"`
		ColdAfterDays                int      `json:"cold_after_days,omitempty"`
		ArchiveAfterDays             int      `json:"archive_after_days,omitempty"`
		DeleteAfterDays              int      `json:"delete_after_days,omitempty"`
		AutomationSuggestionsEnabled *bool    `json:"suggestions_enabled,omitempty"`
	}{
		Enabled:                      c.Enabled,
		Mode:                         c.Mode,
		StateDir:                     c.StateDir,
		MinTaskCount:                 c.EffectiveMinTaskCount(),
		MinSuccessRatio:              c.EffectiveMinSuccessRatio(),
		ColdPathTrigger:              strings.TrimSpace(c.ColdPathTrigger),
		ColdPathTimes:                c.EffectiveColdPathTimes(),
		ColdAfterDays:                c.ColdAfterDays,
		ArchiveAfterDays:             c.ArchiveAfterDays,
		DeleteAfterDays:              c.DeleteAfterDays,
		AutomationSuggestionsEnabled: c.AutomationSuggestionsEnabled,
	}
	if !out.Enabled {
		out.Mode = ""
		out.ColdPathTrigger = ""
		out.ColdPathTimes = nil
	}
	return json.Marshal(out)
}

func (c EvolutionConfig) EffectiveMode() string {
	if !c.Enabled {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "draft":
		return "draft"
	case "apply":
		return "apply"
	case "", "observe":
		return "observe"
	default:
		return "observe"
	}
}

func (c EvolutionConfig) RunsColdPathAutomatically() bool {
	return c.RunsColdPathAfterTurn() || c.RunsColdPathScheduled()
}

func (c EvolutionConfig) ColdPathTriggerMode() string {
	if c.EffectiveMode() != "draft" && c.EffectiveMode() != "apply" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.ColdPathTrigger)) {
	case "", "after_turn":
		return "after_turn"
	case "scheduled":
		return "scheduled"
	case "manual", "none", "off":
		return "manual"
	default:
		return "after_turn"
	}
}

func (c EvolutionConfig) RunsColdPathAfterTurn() bool {
	return c.ColdPathTriggerMode() == "after_turn"
}

func (c EvolutionConfig) RunsColdPathScheduled() bool {
	return c.ColdPathTriggerMode() == "scheduled"
}

func (c EvolutionConfig) EffectiveMinTaskCount() int {
	if c.MinTaskCount > 0 {
		return c.MinTaskCount
	}
	if c.MinCaseCount > 0 {
		return c.MinCaseCount
	}
	return 2
}

func (c EvolutionConfig) EffectiveMinSuccessRatio() float64 {
	if c.MinSuccessRatio > 0 {
		return c.MinSuccessRatio
	}
	if c.MinSuccessRate > 0 {
		return c.MinSuccessRate
	}
	return 0.7
}

func (c EvolutionConfig) EffectiveColdPathTimes() []string {
	out := make([]string, 0, len(c.ColdPathTimes))
	for _, value := range c.ColdPathTimes {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func (c EvolutionConfig) AutoAppliesDrafts() bool {
	return c.EffectiveMode() == "apply"
}

// IsolationConfig controls subprocess isolation for commands started by PicoClaw.
// It is applied by the isolation package rather than by sandboxing the main process.
type IsolationConfig struct {
	Enabled     bool         `json:"enabled,omitempty"`
	ExposePaths []ExposePath `json:"expose_paths,omitempty"`
}

// ExposePath describes a host path that should remain visible inside the isolated
// child-process environment. This is currently implemented on Linux only.
type ExposePath struct {
	Source string `json:"source"`
	Target string `json:"target,omitempty"`
	Mode   string `json:"mode"`
}

// FilterSensitiveData filters sensitive values from content before sending to LLM.
// This prevents the LLM from seeing its own credentials.
// Uses strings.Replacer for O(n+m) performance (computed once per SecurityConfig).
// Short content (below FilterMinLength) is returned unchanged for performance.
func (c *Config) FilterSensitiveData(content string) string {
	if c == nil {
		return content
	}
	// Check if filtering is enabled (default: true)
	if !c.Tools.IsFilterSensitiveDataEnabled() {
		return content
	}
	// Fast path: skip filtering for short content
	if len(content) < c.Tools.GetFilterMinLength() {
		return content
	}
	return c.SensitiveDataReplacer().Replace(content)
}

type HooksConfig struct {
	Enabled   bool                         `json:"enabled"`
	Defaults  HookDefaultsConfig           `json:"defaults,omitempty"`
	Builtins  map[string]BuiltinHookConfig `json:"builtins,omitempty"`
	Processes map[string]ProcessHookConfig `json:"processes,omitempty"`
}

type HookDefaultsConfig struct {
	ObserverTimeoutMS    int `json:"observer_timeout_ms,omitempty"`
	InterceptorTimeoutMS int `json:"interceptor_timeout_ms,omitempty"`
	ApprovalTimeoutMS    int `json:"approval_timeout_ms,omitempty"`
}

type BuiltinHookConfig struct {
	Enabled  bool            `json:"enabled"`
	Priority int             `json:"priority,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

type ProcessHookConfig struct {
	Enabled   bool              `json:"enabled"`
	Priority  int               `json:"priority,omitempty"`
	Transport string            `json:"transport,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Observe   []string          `json:"observe,omitempty"`
	Intercept []string          `json:"intercept,omitempty"`
}

// BuildInfo contains build-time version information
type BuildInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// MarshalJSON implements custom JSON marshaling for Config
// to omit providers section when empty and session when empty.
func (c *Config) MarshalJSON() ([]byte, error) {
	type Alias Config
	aux := &struct {
		Session *SessionConfig `json:"session,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if len(c.Session.Dimensions) > 0 || len(c.Session.IdentityLinks) > 0 || c.Session.DmScope != "" {
		sessionCfg := c.Session
		aux.Session = &sessionCfg
	}

	return json.Marshal(aux)
}
