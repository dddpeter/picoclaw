package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/sipeed/picoclaw/pkg"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type ToolDiscoveryConfig struct {
	Enabled          bool `json:"enabled"            env:"PICOCLAW_TOOLS_DISCOVERY_ENABLED"`
	TTL              int  `json:"ttl"                env:"PICOCLAW_TOOLS_DISCOVERY_TTL"`
	MaxSearchResults int  `json:"max_search_results" env:"PICOCLAW_MAX_SEARCH_RESULTS"`
	UseBM25          bool `json:"use_bm25"           env:"PICOCLAW_TOOLS_DISCOVERY_USE_BM25"`
	UseRegex         bool `json:"use_regex"          env:"PICOCLAW_TOOLS_DISCOVERY_USE_REGEX"`
}

type ToolConfig struct {
	Enabled bool `json:"enabled" yaml:"-" env:"ENABLED"`
}

type MessageToolsConfig struct {
	ToolConfig `yaml:"-" envPrefix:"PICOCLAW_TOOLS_MESSAGE_"`

	MediaEnabled bool `json:"media_enabled" yaml:"-" env:"PICOCLAW_TOOLS_MESSAGE_MEDIA_ENABLED"`
}

type WebToolsConfig struct {
	ToolConfig  `                    yaml:"-"                      envPrefix:"PICOCLAW_TOOLS_WEB_"`
	Brave       BraveConfig        `yaml:"brave,omitempty"                                        json:"brave"`
	Tavily      TavilyConfig       `yaml:"tavily,omitempty"                                       json:"tavily"`
	Kagi        KagiConfig         `yaml:"kagi,omitempty"                                         json:"kagi"`
	Sogou       SogouConfig        `yaml:"-"                                                      json:"sogou"`
	DuckDuckGo  DuckDuckGoConfig   `yaml:"-"                                                      json:"duckduckgo"`
	Gemini      GeminiSearchConfig `yaml:"gemini,omitempty"                                       json:"gemini"`
	Perplexity  PerplexityConfig   `yaml:"perplexity,omitempty"                                   json:"perplexity"`
	SearXNG     SearXNGConfig      `yaml:"-"                                                      json:"searxng"`
	GLMSearch   GLMSearchConfig    `yaml:"glm_search,omitempty"                                   json:"glm_search"`
	BaiduSearch BaiduSearchConfig  `yaml:"baidu_search,omitempty"                                 json:"baidu_search"`
	Provider    string             `yaml:"-"                                                      json:"provider,omitempty"               env:"PICOCLAW_TOOLS_WEB_PROVIDER"`
	// PreferNative controls whether to use provider-native web search when
	// the active LLM supports it (e.g. OpenAI web_search_preview). When true,
	// the client-side web_search tool is hidden to avoid duplicate search surfaces,
	// and the provider's built-in search is used instead. Falls back to client-side
	// search when the provider does not support native search.
	PreferNative bool `yaml:"-"                                                      json:"prefer_native"                    env:"PICOCLAW_TOOLS_WEB_PREFER_NATIVE"`
	// Proxy is an optional proxy URL for web tools (http/https/socks5/socks5h).
	// For authenticated proxies, prefer HTTP_PROXY/HTTPS_PROXY env vars instead of embedding credentials in config.
	Proxy                string              `yaml:"-"                                                      json:"proxy,omitempty"                  env:"PICOCLAW_TOOLS_WEB_PROXY"`
	FetchLimitBytes      int64               `yaml:"-"                                                      json:"fetch_limit_bytes,omitempty"      env:"PICOCLAW_TOOLS_WEB_FETCH_LIMIT_BYTES"`
	Format               string              `yaml:"-"                                                      json:"format,omitempty"                 env:"PICOCLAW_TOOLS_WEB_FORMAT"`
	PrivateHostWhitelist FlexibleStringSlice `yaml:"-"                                                      json:"private_host_whitelist,omitempty" env:"PICOCLAW_TOOLS_WEB_PRIVATE_HOST_WHITELIST"`
}

type CronToolsConfig struct {
	ToolConfig `         envPrefix:"PICOCLAW_TOOLS_CRON_"`
	// 0 means no timeout.
	ExecTimeoutMinutes    int      `                                 json:"exec_timeout_minutes"    env:"PICOCLAW_TOOLS_CRON_EXEC_TIMEOUT_MINUTES"`
	AllowCommand          bool     `                                 json:"allow_command"           env:"PICOCLAW_TOOLS_CRON_ALLOW_COMMAND"`
	CommandAllowedRemotes []string `                                 json:"command_allowed_remotes" env:"PICOCLAW_TOOLS_CRON_COMMAND_ALLOWED_REMOTES"`
}

type ExecConfig struct {
	ToolConfig               `         envPrefix:"PICOCLAW_TOOLS_EXEC_"`
	EnableDenyPatterns       bool     `                                 json:"enable_deny_patterns"        env:"PICOCLAW_TOOLS_EXEC_ENABLE_DENY_PATTERNS"`
	EnableCustomDenyPatterns bool     `                                 json:"enable_custom_deny_patterns" env:"PICOCLAW_TOOLS_EXEC_ENABLE_CUSTOM_DENY_PATTERNS"`
	DenyProfile              string   `                                 json:"deny_profile"                env:"PICOCLAW_TOOLS_EXEC_DENY_PROFILE"`
	AllowRemote              bool     `                                 json:"allow_remote"                env:"PICOCLAW_TOOLS_EXEC_ALLOW_REMOTE"`
	CustomDenyPatterns       []string `                                 json:"custom_deny_patterns"        env:"PICOCLAW_TOOLS_EXEC_CUSTOM_DENY_PATTERNS"`
	CustomAllowPatterns      []string `                                 json:"custom_allow_patterns"       env:"PICOCLAW_TOOLS_EXEC_CUSTOM_ALLOW_PATTERNS"`
	TimeoutSeconds           int      `                                 json:"timeout_seconds"             env:"PICOCLAW_TOOLS_EXEC_TIMEOUT_SECONDS"` // 0 means use default (60s)
}

type SkillsToolsConfig struct {
	ToolConfig `                       yaml:"-"                    envPrefix:"PICOCLAW_TOOLS_SKILLS_"`
	Registries SkillsRegistriesConfig `yaml:"registries,omitempty"                                    json:"registries"`
	// Deprecated: use registries.github instead.
	Github                SkillsGithubConfig `yaml:"github,omitempty"                                        json:"github"`
	MaxConcurrentSearches int                `yaml:"-"                                                       json:"max_concurrent_searches" env:"PICOCLAW_TOOLS_SKILLS_MAX_CONCURRENT_SEARCHES"`
	SearchCache           SearchCacheConfig  `yaml:"-"                                                       json:"search_cache"`
}

type MediaCleanupConfig struct {
	ToolConfig `    envPrefix:"PICOCLAW_MEDIA_CLEANUP_"`
	MaxAge     int `                                    json:"max_age_minutes"  env:"PICOCLAW_MEDIA_CLEANUP_MAX_AGE"`
	Interval   int `                                    json:"interval_minutes" env:"PICOCLAW_MEDIA_CLEANUP_INTERVAL"`
}

type ReadFileToolConfig struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	MaxReadFileSize int    `json:"max_read_file_size"`
}

const (
	ReadFileModeBytes = "bytes"
	ReadFileModeLines = "lines"
)

// EffectiveProtectSystemPaths resolves the tri-state guard: nil = enabled.
func (c *ToolsConfig) EffectiveProtectSystemPaths() bool {
	return c.ProtectSystemPaths == nil || *c.ProtectSystemPaths
}

func (c ReadFileToolConfig) EffectiveMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ReadFileModeLines:
		return ReadFileModeLines
	case "", ReadFileModeBytes:
		return ReadFileModeBytes
	default:
		return ReadFileModeBytes
	}
}

type ToolsConfig struct {
	AllowReadPaths  []string `json:"allow_read_paths"                yaml:"-"                env:"PICOCLAW_TOOLS_ALLOW_READ_PATHS"`
	AllowWritePaths []string `json:"allow_write_paths"               yaml:"-"                env:"PICOCLAW_TOOLS_ALLOW_WRITE_PATHS"`
	// ProtectSystemPaths keeps OS system directories off-limits to file
	// tools (read and write) regardless of restrict_to_workspace. nil counts
	// as enabled — the same nil=on convention as loop_detection. Explicit
	// false disables the guard entirely (not recommended).
	ProtectSystemPaths *bool `json:"protect_system_paths,omitzero"   yaml:"-"                env:"PICOCLAW_TOOLS_PROTECT_SYSTEM_PATHS"`
	// RenderUI enables the render_ui tool (A2UI web render, fork feature).
	// nil counts as enabled — the same nil=on convention as
	// protect_system_paths/loop_detection. Explicit false removes the tool
	// from the agent registry entirely.
	RenderUI *bool `json:"render_ui,omitzero"              yaml:"-"                env:"PICOCLAW_TOOLS_RENDER_UI"`
	// FilterSensitiveData controls whether to filter sensitive values (API keys,
	// tokens, secrets) from tool results before sending to the LLM.
	// Default: true (enabled)
	FilterSensitiveData bool `json:"filter_sensitive_data"           yaml:"-"                env:"PICOCLAW_TOOLS_FILTER_SENSITIVE_DATA"`
	// FilterMinLength is the minimum content length required for filtering.
	// Content shorter than this will be returned unchanged for performance.
	// Default: 8
	FilterMinLength int `json:"filter_min_length"               yaml:"-"                env:"PICOCLAW_TOOLS_FILTER_MIN_LENGTH"`
	// MaxToolOutputBytes caps the ForLLM content of any single tool result as
	// a last-resort backstop (tools that have no budget of their own: MCP
	// servers, third-party tools). Built-in tools stay below the default.
	// 0 = default (128KB), negative = unlimited.
	MaxToolOutputBytes int               `json:"max_tool_output_bytes,omitempty" yaml:"-"                env:"PICOCLAW_TOOLS_MAX_TOOL_OUTPUT_BYTES"`
	Web                WebToolsConfig    `json:"web"                             yaml:"web,omitempty"`
	Cron               CronToolsConfig   `json:"cron"                            yaml:"-"`
	Exec               ExecConfig        `json:"exec"                            yaml:"-"`
	Skills             SkillsToolsConfig `json:"skills"                          yaml:"skills,omitempty"`
	// Lsp configures language-server diagnostics/fix tooling (fork
	// feature, docs/design/lsp-support-design.zh.md). Absent = enabled with
	// the built-in server catalog (missing commands are skipped silently).
	Lsp          LspToolsConfig     `json:"lsp"                             yaml:"lsp,omitempty"`
	MediaCleanup MediaCleanupConfig `json:"media_cleanup"                   yaml:"-"`
	MCP          MCPConfig          `json:"mcp"                             yaml:"-"`
	// Checkpoint configures workspace snapshots + turn-level undo (trae
	// borrowing 增量清单 §2.1). Default off — a plain bool is deliberate:
	// unlike loop_detection/streaming there is no legacy behavior to
	// preserve, so the *bool nil=on convention does not apply.
	Checkpoint   CheckpointConfig   `json:"checkpoint"                      yaml:"-"`
	AppendFile   ToolConfig         `json:"append_file"                     yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_APPEND_FILE_"`
	EditFile     ToolConfig         `json:"edit_file"                       yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_EDIT_FILE_"`
	FindSkills   ToolConfig         `json:"find_skills"                     yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_FIND_SKILLS_"`
	I2C          ToolConfig         `json:"i2c"                             yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_I2C_"`
	InstallSkill ToolConfig         `json:"install_skill"                   yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_INSTALL_SKILL_"`
	ListDir      ToolConfig         `json:"list_dir"                        yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_LIST_DIR_"`
	Glob         ToolConfig         `json:"glob"                            yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_GLOB_"`
	Grep         ToolConfig         `json:"grep"                            yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_GREP_"`
	LoadImage    ToolConfig         `json:"load_image"                      yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_LOAD_IMAGE_"`
	Message      MessageToolsConfig `json:"message"                         yaml:"-"`
	ReadFile     ReadFileToolConfig `json:"read_file"                       yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_READ_FILE_"`
	Serial       ToolConfig         `json:"serial"                          yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SERIAL_"`
	SendFile     ToolConfig         `json:"send_file"                       yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SEND_FILE_"`
	SendTTS      ToolConfig         `json:"send_tts"                        yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SEND_TTS_"`
	Spawn        ToolConfig         `json:"spawn"                           yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SPAWN_"`
	SpawnStatus  ToolConfig         `json:"spawn_status"                    yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SPAWN_STATUS_"`
	SPI          ToolConfig         `json:"spi"                             yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SPI_"`
	Subagent     ToolConfig         `json:"subagent"                        yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_SUBAGENT_"`
	WebFetch     ToolConfig         `json:"web_fetch"                       yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_WEB_FETCH_"`
	WriteFile    ToolConfig         `json:"write_file"                      yaml:"-"                                                           envPrefix:"PICOCLAW_TOOLS_WRITE_FILE_"`
}

// CheckpointConfig bounds the workspace snapshot store (trae borrowing
// 增量清单 §2.1). Every limit degrades honestly: entries beyond a cap are
// still recorded in the version manifest with stored=false + reason, never
// silently dropped.
type CheckpointConfig struct {
	// Enabled turns workspace checkpointing on. Default off.
	Enabled bool `json:"enabled"         yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_ENABLED"`
	// TrackExec covers file changes made through the exec tool via an
	// incremental mtime scan around each exec call. Default true when
	// checkpointing is on.
	TrackExec bool `json:"track_exec"      yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_TRACK_EXEC"`
	// MaxVersions caps retained versions per session; the oldest are evicted
	// at commit time. 0 = default (20).
	MaxVersions int `json:"max_versions"    yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_MAX_VERSIONS"`
	// MaxTotalBytes caps stored pre-image bytes per session (content-addressed,
	// deduplicated across versions). The cap is hard for history but soft for
	// the newest version: a turn larger than the whole budget is retained and
	// noted rather than instantly discarded (undoing the last turn is the
	// primary use case). 0 = default (128MB).
	MaxTotalBytes int64 `json:"max_total_bytes" yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_MAX_TOTAL_BYTES"`
	// MaxFileSize caps the size of a single captured file. Larger files are
	// recorded as changed but their pre-image is not retained. 0 = default
	// (4MB).
	MaxFileSize int64 `json:"max_file_size"   yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_MAX_FILE_SIZE"`
	// ScanDepth bounds the exec-change-scan workspace walk depth. 0 = default
	// (24, aligned with the glob tool).
	ScanDepth int `json:"scan_depth"      yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_SCAN_DEPTH"`
	// ScanMaxFiles bounds the exec-change-scan walk entry count. 0 = default
	// (20000).
	ScanMaxFiles int `json:"scan_max_files"  yaml:"-" env:"PICOCLAW_TOOLS_CHECKPOINT_SCAN_MAX_FILES"`
}

// EffectiveMaxVersions applies the default version cap.
func (c CheckpointConfig) EffectiveMaxVersions() int {
	if c.MaxVersions <= 0 {
		return 20
	}
	return c.MaxVersions
}

// EffectiveMaxTotalBytes applies the default per-session disk budget.
func (c CheckpointConfig) EffectiveMaxTotalBytes() int64 {
	if c.MaxTotalBytes <= 0 {
		return 128 << 20
	}
	return c.MaxTotalBytes
}

// EffectiveMaxFileSize applies the default per-file capture cap.
func (c CheckpointConfig) EffectiveMaxFileSize() int64 {
	if c.MaxFileSize <= 0 {
		return 4 << 20
	}
	return c.MaxFileSize
}

// EffectiveScanDepth applies the default exec-scan walk depth.
func (c CheckpointConfig) EffectiveScanDepth() int {
	if c.ScanDepth <= 0 {
		return 24
	}
	return c.ScanDepth
}

// EffectiveScanMaxFiles applies the default exec-scan entry cap.
func (c CheckpointConfig) EffectiveScanMaxFiles() int {
	if c.ScanMaxFiles <= 0 {
		return 20000
	}
	return c.ScanMaxFiles
}

// IsFilterSensitiveDataEnabled returns true if sensitive data filtering is enabled
func (c *ToolsConfig) IsFilterSensitiveDataEnabled() bool {
	return c.FilterSensitiveData
}

// GetFilterMinLength returns the minimum content length for filtering (default: 8)
func (c *ToolsConfig) GetFilterMinLength() int {
	if c.FilterMinLength <= 0 {
		return 8
	}
	return c.FilterMinLength
}

type SearchCacheConfig struct {
	MaxSize    int `json:"max_size"    env:"PICOCLAW_SKILLS_SEARCH_CACHE_MAX_SIZE"`
	TTLSeconds int `json:"ttl_seconds" env:"PICOCLAW_SKILLS_SEARCH_CACHE_TTL_SECONDS"`
}

type SkillsRegistriesConfig []*SkillRegistryConfig

func (c *SkillsRegistriesConfig) Get(name string) (SkillRegistryConfig, bool) {
	if c == nil {
		return SkillRegistryConfig{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return SkillRegistryConfig{}, false
	}
	for _, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		return *registry, true
	}
	return SkillRegistryConfig{}, false
}

func (c *SkillsRegistriesConfig) Set(name string, cfg SkillRegistryConfig) {
	if c == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	cfg.Name = name
	for i, registry := range *c {
		if registry == nil || registry.Name != name {
			continue
		}
		(*c)[i] = &cfg
		return
	}
	*c = append(*c, &cfg)
}

type SkillsGithubConfig struct {
	BaseURL string       `json:"base_url,omitempty" yaml:"-"               env:"PICOCLAW_TOOLS_SKILLS_GITHUB_BASE_URL"`
	Token   SecureString `json:"token,omitzero"     yaml:"token,omitempty" env:"PICOCLAW_TOOLS_SKILLS_GITHUB_TOKEN"`
	Proxy   string       `json:"proxy,omitempty"    yaml:"-"               env:"PICOCLAW_TOOLS_SKILLS_GITHUB_PROXY"`
}

type SkillRegistryConfig struct {
	Name      string         `json:"name,omitempty"      yaml:"-"                    env:"-"`
	Enabled   bool           `json:"enabled"             yaml:"-"                    env:"-"`
	BaseURL   string         `json:"base_url"            yaml:"-"                    env:"-"`
	AuthToken SecureString   `json:"auth_token,omitzero" yaml:"auth_token,omitempty" env:"-"`
	Param     map[string]any `json:"-"                   yaml:"-"                    env:"-"`
}

const (
	envSkillsClawHubEnabled         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_ENABLED"
	envSkillsClawHubBaseURL         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_BASE_URL"
	envSkillsClawHubAuthToken       = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_AUTH_TOKEN"
	envSkillsClawHubSearchPath      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SEARCH_PATH"
	envSkillsClawHubSkillsPath      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_SKILLS_PATH"
	envSkillsClawHubDownloadPath    = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_DOWNLOAD_PATH"
	envSkillsClawHubTimeout         = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_TIMEOUT"
	envSkillsClawHubMaxZipSize      = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_ZIP_SIZE"
	envSkillsClawHubMaxResponseSize = "PICOCLAW_SKILLS_REGISTRIES_CLAWHUB_MAX_RESPONSE_SIZE"
	envSkillsGitHubEnabled          = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_ENABLED"
	envSkillsGitHubBaseURL          = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_BASE_URL"
	envSkillsGitHubAuthToken        = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_AUTH_TOKEN"
	envSkillsGitHubProxy            = "PICOCLAW_SKILLS_REGISTRIES_GITHUB_PROXY"
)

func (c *SkillRegistryConfig) DecodeParam(target any) error {
	if c == nil {
		return nil
	}
	if len(c.Param) == 0 {
		return nil
	}
	data, err := json.Marshal(c.Param)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// MCPServerConfig defines configuration for a single MCP server
type MCPServerConfig struct {
	// Enabled indicates whether this MCP server is active
	Enabled bool `json:"enabled"`
	// Deferred controls whether this server's tools are registered as hidden (deferred/discovery mode).
	// When nil, the global Discovery.Enabled setting applies.
	// When explicitly set to true or false, it overrides the global setting for this server only.
	Deferred *bool `json:"deferred,omitempty"`
	// Command is the executable to run (e.g., "npx", "python", "/path/to/server")
	Command string `json:"command"`
	// Args are the arguments to pass to the command
	Args []string `json:"args,omitempty"`
	// Env are environment variables to set for the server process (stdio only)
	Env map[string]string `json:"env,omitempty"`
	// EnvFile is the path to a file containing environment variables (stdio only)
	EnvFile string `json:"env_file,omitempty"`
	// Type is "stdio", "sse", "http", or "streamable-http".
	// "http" and "streamable-http" both select streamable HTTP request-response
	// mode, while "sse" keeps the standalone SSE listener enabled for
	// server-initiated notifications. Defaults: stdio if command is set, sse if
	// url is set.
	Type string `json:"type,omitempty"`
	// URL is used for SSE/HTTP transport
	URL string `json:"url,omitempty"`
	// Headers are HTTP headers to send with requests (sse/http only)
	Headers map[string]string `json:"headers,omitempty"`
	// Dir is the working directory for the stdio process. Populated by the
	// Agent Plugins bridge (resolved plugin root or explicit cwd); empty for
	// plain user config (current process dir, as before).
	Dir string `json:"dir,omitempty"`
	// PluginRoot/PluginData carry the spec §9.1 hidden channel from the
	// Agent Plugins bridge to the MCP manager: they are injected into the
	// stdio env AFTER the configured env, overriding any forged entries.
	// Never serialized.
	PluginRoot string `json:"-"`
	PluginData string `json:"-"`
}

// MCPConfig defines configuration for all MCP servers
type MCPConfig struct {
	ToolConfig `                           envPrefix:"PICOCLAW_TOOLS_MCP_"`
	Discovery  ToolDiscoveryConfig `                                json:"discovery"`
	// MaxInlineTextChars controls how much MCP text stays inline before it is saved as an artifact.
	MaxInlineTextChars int `                                json:"max_inline_text_chars,omitempty"   env:"PICOCLAW_TOOLS_MCP_MAX_INLINE_TEXT_CHARS"`
	// DeferredThreshold enables fine-grained progressive tool loading
	// (octop borrowing §六): when the connected MCP tool count exceeds this
	// threshold, not-yet-loaded MCP tools go to the LLM as lightweight
	// deferred references and a tool_search meta-tool loads full schemas on
	// demand (session-sticky). 0/absent = default (20); NEGATIVE disables.
	DeferredThreshold int `                                json:"deferred_tool_threshold,omitempty" env:"PICOCLAW_TOOLS_MCP_DEFERRED_TOOL_THRESHOLD"`
	// Servers is a map of server name to server configuration
	Servers map[string]MCPServerConfig `                                json:"servers,omitempty"`
}

const DefaultMCPMaxInlineTextChars = 16 * 1024

// DefaultMCPDeferredToolThreshold matches the octop borrowing doc's
// recommendation: small catalogs are not worth the extra hop.
const DefaultMCPDeferredToolThreshold = 20

func (c *MCPConfig) GetMaxInlineTextChars() int {
	if c.MaxInlineTextChars > 0 {
		return c.MaxInlineTextChars
	}
	return DefaultMCPMaxInlineTextChars
}

// LoadConfig is the default load path for gateway startup and CLI commands.
// Unknown fields are skipped and logged as warnings instead of failing the
// whole load (2026-09-19: strict rejection turned binary/config version skew
// — e.g. a config.json written by a newer build containing tools.lsp — into
// a hard startup failure). JSON syntax errors and type errors on known
// fields still fail hard.
func LoadConfig(path string) (*Config, error) {
	cfg, warnings, err := LoadConfigWithWarnings(path, true)
	if err == nil && len(warnings) > 0 {
		logger.WarnF(
			"config contains unknown field(s), skipped: %s",
			map[string]any{"path": path, "fields": strings.Join(warnings, ", ")},
		)
	}
	return cfg, err
}

// LoadConfigLenient loads config tolerating unknown fields (reported as
// warnings) while still failing hard on JSON syntax errors. Intended for
// UI-facing surfaces (launcher dashboard) that prefer showing a degraded
// config plus warnings over refusing to load at all.
func LoadConfigLenient(path string) (*Config, []string, error) {
	return LoadConfigWithWarnings(path, true)
}

// LoadConfigWithWarnings is the shared loader. When lenient is true, unknown
// fields downgrade from errors to warnings; syntax errors always fail.
func LoadConfigWithWarnings(path string, lenient bool) (*Config, []string, error) {
	var warnings []string
	updateResolver(filepath.Dir(path))

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.WarnF(
				"config file not found, using default config",
				map[string]any{"path": path},
			)
			return DefaultConfig(), nil, nil
		}
		return nil, nil, err
	}

	// First, try to detect config version by reading the version field
	var versionInfo struct {
		Version int `json:"version"`
	}
	if e := json.Unmarshal(data, &versionInfo); e != nil {
		e = wrapJSONError(data, e, "config.json")
		logger.ErrorCF("config", formatDiagnosticLogMessage("Malformed config file", e), map[string]any{"path": path})
		return nil, nil, e
	}
	if len(data) <= 10 {
		logger.Warn(fmt.Sprintf("content is [%s]", string(data)))
		return DefaultConfig(), nil, nil
	}

	// Load config based on detected version
	var cfg *Config
	switch versionInfo.Version {
	case 0:
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}

		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}

		migrateErr := migrateV0ToV1(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V0→V1 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, nil, err
		}

		defer func(cfg *Config) {
			_ = SaveConfig(path, cfg)
		}(cfg)
	case 1:
		// V1→V3 migration: rename channels→channel_list, infer Enabled, migrate channel configs
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}

		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}

		migrateErr := migrateV1ToV2(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V1→V2 migration failed: %w", migrateErr)
		}
		migrateErr = migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, nil, err
		}

		defer func(cfg *Config) {
			_ = SaveConfig(path, cfg)
		}(cfg)
		logger.InfoF(
			"config migrate success",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
	case 2:
		// V2→V3 migration: rename channels→channel_list, convert flat→nested
		logger.InfoF(
			"config migrate start",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
		if err = validateLegacyConfigDiagnostics(data); err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}
		var m map[string]any
		m, err = loadConfigMap(path)
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}
		migrateErr := migrateV2ToV3(m)
		if migrateErr != nil {
			return nil, nil, fmt.Errorf("V2→V3 migration failed: %w", migrateErr)
		}

		var migrated []byte
		migrated, err = json.Marshal(m)
		if err != nil {
			return nil, nil, err
		}

		cfg, err = loadConfig(migrated)
		if err != nil {
			return nil, nil, err
		}

		err = MakeBackup(path)
		if err != nil {
			return nil, nil, err
		}

		defer func(cfg *Config) {
			_ = SaveConfig(path, cfg)
		}(cfg)
		logger.InfoF(
			"config migrate success",
			map[string]any{"from": versionInfo.Version, "to": CurrentVersion},
		)
	case CurrentVersion:
		// Current version
		if lenient {
			var w []string
			cfg, w, err = loadConfigLenient(data, true)
			warnings = w
		} else {
			// Strict mode (kept for the example-template regression pin):
			// unknown fields fail the load with exact paths.
			cfg, _, err = loadConfigLenient(data, false)
		}
		if err != nil {
			logger.ErrorCF(
				"config",
				formatDiagnosticLogMessage("Failed to load config", err),
				map[string]any{"path": path},
			)
			return nil, nil, err
		}
		// Load security configuration
		secPath := securityPath(path)
		err = loadSecurityConfig(cfg, secPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("failed to load security config: %w", err)
		}

	default:
		return nil, nil, fmt.Errorf("unsupported config version: %d", versionInfo.Version)
	}

	applyLegacyBindingsMigration(data, cfg)

	gatewayHostBeforeEnv := cfg.Gateway.Host

	if err = env.Parse(cfg); err != nil {
		return nil, nil, err
	}
	applySkillsRegistryEnvCompat(cfg)

	if err = InitChannelList(cfg.Channels); err != nil {
		return nil, nil, err
	}
	if err = cfg.ValidateTurnProfile(); err != nil {
		return nil, nil, err
	}
	cfg.Gateway.Host, err = resolveGatewayHostFromEnv(gatewayHostBeforeEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid gateway host: %w", err)
	}

	// Expand multi-key configs into separate entries for key-level failover
	cfg.ModelList = expandMultiKeyModels(cfg.ModelList)

	// Validate model_list for uniqueness and required fields
	if err = cfg.ValidateModelList(); err != nil {
		return nil, nil, err
	}

	// Ensure Workspace has a default if not set
	if cfg.Agents.Defaults.Workspace == "" {
		homePath := GetHome()
		cfg.Agents.Defaults.Workspace = filepath.Join(homePath, pkg.WorkspaceName)
	}

	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()

	return cfg, warnings, nil
}

func applySkillsRegistryEnvCompat(cfg *Config) {
	if cfg == nil {
		return
	}

	registryCfg, foundClawHub := cfg.Tools.Skills.Registries.Get("clawhub")
	if !foundClawHub {
		registryCfg = SkillRegistryConfig{
			Name:  "clawhub",
			Param: map[string]any{},
		}
	}
	if registryCfg.Param == nil {
		registryCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsClawHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			registryCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubBaseURL); envSet {
		registryCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubAuthToken); envSet {
		registryCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSearchPath); envSet {
		registryCfg.Param["search_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubSkillsPath); envSet {
		registryCfg.Param["skills_path"] = value
	}
	if value, envSet := os.LookupEnv(envSkillsClawHubDownloadPath); envSet {
		registryCfg.Param["download_path"] = value
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubTimeout); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["timeout"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxZipSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_zip_size"] = value
		}
	}
	if raw, envSet := os.LookupEnv(envSkillsClawHubMaxResponseSize); envSet {
		if value, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			registryCfg.Param["max_response_size"] = value
		}
	}

	cfg.Tools.Skills.Registries.Set("clawhub", registryCfg)

	githubCfg, foundGitHub := cfg.Tools.Skills.Registries.Get("github")
	if !foundGitHub {
		githubCfg = SkillRegistryConfig{
			Name:  "github",
			Param: map[string]any{},
		}
	}
	if githubCfg.Param == nil {
		githubCfg.Param = map[string]any{}
	}

	if raw, envSet := os.LookupEnv(envSkillsGitHubEnabled); envSet {
		if value, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			githubCfg.Enabled = value
		}
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubBaseURL); envSet {
		githubCfg.BaseURL = value
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubAuthToken); envSet {
		githubCfg.AuthToken = *NewSecureString(value)
	}
	if value, envSet := os.LookupEnv(envSkillsGitHubProxy); envSet {
		githubCfg.Param["proxy"] = value
	}

	cfg.Tools.Skills.Registries.Set("github", githubCfg)
}

func MakeBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	dateSuffix := time.Now().Format(".20060102.bak")
	// Backup config file
	bakPath := path + dateSuffix
	if err := fileutil.CopyFile(path, bakPath, 0o600); err != nil {
		logger.ErrorF("failed to create config backup", map[string]any{"error": err})
		return fmt.Errorf("failed to create config backup: %w", err)
	}
	// Backup security config file
	secPath := securityPath(path)
	if _, err := os.Stat(secPath); err == nil {
		secBakPath := secPath + dateSuffix
		if secErr := fileutil.CopyFile(secPath, secBakPath, 0o600); secErr != nil {
			logger.ErrorF("failed to create security backup", map[string]any{"error": secErr})
			return fmt.Errorf("failed to create security backup: %w", secErr)
		}
	}
	return nil
}

func toNameIndex(list []*ModelConfig) []string {
	nameList := make([]string, 0, len(list))
	countMap := make(map[string]int)
	for _, model := range list {
		name := model.ModelName
		index := countMap[name]
		nameList = append(nameList, fmt.Sprintf("%s:%d", name, index))
		countMap[name]++
	}
	return nameList
}

func SaveConfig(path string, cfg *Config) error {
	if cfg.Version < CurrentVersion {
		cfg.Version = CurrentVersion
	}
	// Filter out virtual models before serializing to config file
	nonVirtualModels := make([]*ModelConfig, 0, len(cfg.ModelList))
	for _, m := range cfg.ModelList {
		if !m.isVirtual {
			nonVirtualModels = append(nonVirtualModels, m)
		}
	}
	// Temporarily replace ModelList with filtered version for serialization
	originalModelList := cfg.ModelList
	defer func() {
		// Restore original ModelList after serialization
		cfg.ModelList = originalModelList
	}()
	cfg.ModelList = nonVirtualModels

	if err := saveSecurityConfig(securityPath(path), cfg); err != nil {
		logger.ErrorCF("config", "cannot save .security.yml", map[string]any{"error": err})
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func (c *Config) WorkspacePath() string {
	return expandHome(c.Agents.Defaults.Workspace)
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}

// GetModelConfig returns the ModelConfig for the given model name.
// If multiple configs exist with the same model_name, it uses round-robin
// selection for load balancing. Returns an error if the model is not found.
func (c *Config) GetModelConfig(modelName string) (*ModelConfig, error) {
	matches := c.findMatches(modelName)
	if len(matches) == 0 {
		return nil, fmt.Errorf("model %q not found in model_list or providers", modelName)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple configs - use round-robin for load balancing
	idx := (rrCounter.Add(1) - 1) % uint64(len(matches))
	return matches[idx], nil
}

// findMatches finds all ModelConfig entries with the given model_name.
func (c *Config) findMatches(modelName string) []*ModelConfig {
	var matches []*ModelConfig
	for i := range c.ModelList {
		if c.ModelList[i].ModelName == modelName {
			matches = append(matches, c.ModelList[i])
		}
	}
	return matches
}

// ValidateModelList validates all ModelConfig entries in the model_list.
// It checks that each model config is valid.
// Note: Multiple entries with the same model_name are allowed for load balancing.
func (c *Config) ValidateModelList() error {
	for i := range c.ModelList {
		if err := c.ModelList[i].Validate(); err != nil {
			return fmt.Errorf("model_list[%d]: %w", i, err)
		}
	}
	return nil
}

func (c *Config) SecurityCopyFrom(path string) error {
	return loadSecurityConfig(c, securityPath(path))
}

// ResetToDefaults backs up the current config, creates a default config,
// preserves security credentials from the existing config, and saves it.
func ResetToDefaults(configPath string) error {
	if err := MakeBackup(configPath); err != nil {
		return fmt.Errorf("backup before reset: %w", err)
	}
	cfg := DefaultConfig()
	cfg.Session.ApplyDmScope()
	cfg.Session.DeriveDmScope()
	if err := cfg.SecurityCopyFrom(configPath); err != nil {
		logger.WarnF("could not preserve security config", map[string]any{"error": err})
	}
	return SaveConfig(configPath, cfg)
}

func expandMultiKeyModels(models []*ModelConfig) []*ModelConfig {
	var expanded []*ModelConfig

	for _, m := range models {
		keys := m.APIKeys.Values()

		// Single key or no keys: keep as-is
		if len(keys) <= 1 {
			expanded = append(expanded, m)
			continue
		}

		// Multiple keys: expand
		originalName := m.ModelName

		// Create entries for additional keys (key_1, key_2, ...)
		var fallbackNames []string
		for i := 1; i < len(keys); i++ {
			suffix := fmt.Sprintf("__key_%d", i)
			expandedName := originalName + suffix

			// Create a copy for the additional key
			additionalEntry := &ModelConfig{
				ModelName:           expandedName,
				Provider:            m.Provider,
				Model:               m.Model,
				APIBase:             m.APIBase,
				APIKeys:             SimpleSecureStrings(keys[i]),
				Proxy:               m.Proxy,
				AuthMethod:          m.AuthMethod,
				ConnectMode:         m.ConnectMode,
				Workspace:           m.Workspace,
				RPM:                 m.RPM,
				MaxTokensField:      m.MaxTokensField,
				RequestTimeout:      m.RequestTimeout,
				ContextWindow:       m.ContextWindow,
				ThinkingLevel:       m.ThinkingLevel,
				ToolSchemaTransform: m.ToolSchemaTransform,
				Streaming:           m.Streaming,
				ExtraBody:           m.ExtraBody,
				CustomHeaders:       m.CustomHeaders,
				UserAgent:           m.UserAgent,
				isVirtual:           true,
			}
			expanded = append(expanded, additionalEntry)
			fallbackNames = append(fallbackNames, expandedName)
		}

		// Create the primary entry with first key and fallbacks
		primaryEntry := &ModelConfig{
			ModelName:           originalName,
			Provider:            m.Provider,
			Model:               m.Model,
			APIBase:             m.APIBase,
			Proxy:               m.Proxy,
			AuthMethod:          m.AuthMethod,
			ConnectMode:         m.ConnectMode,
			Workspace:           m.Workspace,
			RPM:                 m.RPM,
			MaxTokensField:      m.MaxTokensField,
			RequestTimeout:      m.RequestTimeout,
			ContextWindow:       m.ContextWindow,
			ThinkingLevel:       m.ThinkingLevel,
			ToolSchemaTransform: m.ToolSchemaTransform,
			Streaming:           m.Streaming,
			ExtraBody:           m.ExtraBody,
			CustomHeaders:       m.CustomHeaders,
			UserAgent:           m.UserAgent,
			APIKeys:             SimpleSecureStrings(keys[0]),
		}

		// Prepend new fallbacks to existing ones
		if len(fallbackNames) > 0 {
			primaryEntry.Fallbacks = append(fallbackNames, m.Fallbacks...)
		} else if len(m.Fallbacks) > 0 {
			primaryEntry.Fallbacks = m.Fallbacks
		}

		expanded = append(expanded, primaryEntry)
	}

	return expanded
}

func (t *ToolsConfig) IsToolEnabled(name string) bool {
	switch name {
	case "web":
		return t.Web.Enabled
	case "cron":
		return t.Cron.Enabled
	case "exec":
		return t.Exec.Enabled
	case "skills":
		return t.Skills.Enabled
	case "media_cleanup":
		return t.MediaCleanup.Enabled
	case "append_file":
		return t.AppendFile.Enabled
	case "edit_file":
		return t.EditFile.Enabled
	case "find_skills":
		return t.FindSkills.Enabled
	case "i2c":
		return t.I2C.Enabled
	case "install_skill":
		return t.InstallSkill.Enabled
	case "list_dir":
		return t.ListDir.Enabled
	case "glob":
		return t.Glob.Enabled
	case "grep":
		return t.Grep.Enabled
	case "load_image":
		return t.LoadImage.Enabled
	case "message":
		return t.Message.Enabled
	case "render_ui":
		return t.RenderUI == nil || *t.RenderUI
	case "read_file":
		return t.ReadFile.Enabled
	case "serial":
		return t.Serial.Enabled
	case "spawn":
		return t.Spawn.Enabled
	case "spawn_status":
		return t.SpawnStatus.Enabled
	case "spi":
		return t.SPI.Enabled
	case "subagent":
		return t.Subagent.Enabled
	case "web_fetch":
		return t.WebFetch.Enabled
	case "send_file":
		return t.SendFile.Enabled
	case "send_tts":
		return t.SendTTS.Enabled
	case "write_file":
		return t.WriteFile.Enabled
	case "mcp":
		return t.MCP.Enabled
	case "lsp_diagnostics", "lsp_fix":
		return t.Lsp.EffectiveEnabled()
	default:
		return true
	}
}
