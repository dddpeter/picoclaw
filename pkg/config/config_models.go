package config

import (
	"encoding/json"
	"fmt"
	"strings"

	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
)

type HeartbeatConfig struct {
	Enabled  bool `json:"enabled"  env:"PICOCLAW_HEARTBEAT_ENABLED"`
	Interval int  `json:"interval" env:"PICOCLAW_HEARTBEAT_INTERVAL"` // minutes, min 5
}

type DevicesConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_DEVICES_ENABLED"`
	MonitorUSB bool `json:"monitor_usb" env:"PICOCLAW_DEVICES_MONITOR_USB"`
}

type VoiceConfig struct {
	ModelName         string `json:"model_name,omitempty"     env:"PICOCLAW_VOICE_MODEL_NAME"`
	TTSModelName      string `json:"tts_model_name,omitempty" env:"PICOCLAW_VOICE_TTS_MODEL_NAME"`
	EchoTranscription bool   `json:"echo_transcription"       env:"PICOCLAW_VOICE_ECHO_TRANSCRIPTION"`
}

// ModelStreamingConfig gates provider streaming per model_list entry.
// Enabled is a *bool where nil counts as ENABLED (fork default-on): the
// streaming card UX is the fork's flagship, and the pre-visible-failure
// fallback to Chat makes default-on safe. Write {"enabled": false} to opt
// out explicitly. Same nil=on convention as loop_detection / session_titles.
type ModelStreamingConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

func (c ModelStreamingConfig) IsZero() bool {
	return c.Enabled == nil
}

// EffectiveEnabled resolves the tri-state: omitted (nil) streams, explicit
// false opts out.
func (c ModelStreamingConfig) EffectiveEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ModelConfig represents a model-centric provider configuration.
// It allows adding new providers (especially OpenAI-compatible ones) via configuration only.
// The Model field may be either a plain model identifier or a provider-prefixed
// identifier such as "openai/gpt-5.4" or "nvidia/z-ai/glm-5.1".
// Supported providers include openai, anthropic, antigravity, claude-cli,
// codex-cli, github-copilot, and named OpenAI-compatible protocols such as
// groq, deepseek, modelscope, and novita.
type ModelConfig struct {
	// Required fields
	ModelName string `json:"model_name"` // User-facing alias for the model
	Provider  string `json:"provider"`   // Provider name for routing and selection. When empty, provider resolution infers it from Model.
	Model     string `json:"model"`      // Model identifier, optionally provider-prefixed.

	// HTTP-based providers
	APIBase   string   `json:"api_base,omitempty"`  // API endpoint URL
	Proxy     string   `json:"proxy,omitempty"`     // HTTP proxy URL
	Fallbacks []string `json:"fallbacks,omitempty"` // Fallback model names for failover

	// Special providers (CLI-based, OAuth, etc.)
	AuthMethod  string `json:"auth_method,omitempty"`  // Authentication method: oauth, token
	ConnectMode string `json:"connect_mode,omitempty"` // Connection mode: stdio, grpc
	Workspace   string `json:"workspace,omitempty"`    // Workspace path for CLI-based providers

	// Optional optimizations
	RPM            int    `json:"rpm,omitempty"`              // Requests per minute limit
	MaxTokensField string `json:"max_tokens_field,omitempty"` // Field name for max tokens (e.g., "max_completion_tokens")
	RequestTimeout int    `json:"request_timeout,omitempty"`
	// ContextWindow optionally caps the agent-level context window while
	// this model is the active primary (fork feature, mirrors
	// trust_configured_context_window). The effective window is
	// min(agents.defaults context window, this value); 0 = unset. Use it
	// when models with different real windows share one agent — e.g. a 1M
	// flagship and a 204k speed variant behind /switch — so compaction
	// gates track the active model instead of the agent-wide ceiling.
	ContextWindow       int                  `json:"context_window,omitempty"`
	ThinkingLevel       string               `json:"thinking_level,omitempty"`        // Extended thinking: off|low|medium|high|xhigh|adaptive
	ToolSchemaTransform string               `json:"tool_schema_transform,omitempty"` // Optional tool schema compatibility transform (e.g. "simple")
	Streaming           ModelStreamingConfig `json:"streaming,omitzero"`              // Opt-in for provider streaming on this model entry
	ExtraBody           map[string]any       `json:"extra_body,omitempty"`            // Additional fields to inject into request body
	CustomHeaders       map[string]string    `json:"custom_headers,omitempty"`        // Additional headers to inject into every HTTP request

	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty"` // API authentication keys (multiple keys for failover)

	// Enabled indicates whether this model entry is active. When omitted in
	// existing configs, the field is inferred during load: models with API keys
	// or the reserved "local-model" name are auto-enabled.
	Enabled bool `json:"enabled,omitempty"    yaml:"enabled,omitempty"`
	// UserAgent is the user agent string to use for HTTP requests.
	UserAgent string `json:"user_agent,omitempty" yaml:"-"`

	// isVirtual marks this model as a virtual model generated from multi-key expansion.
	// Virtual models should not be persisted to config files.
	isVirtual bool
}

// APIKey returns the first API key from apiKeys
func (c *ModelConfig) APIKey() string {
	if len(c.APIKeys) > 0 {
		return c.APIKeys[0].String()
	}
	return ""
}

// IsVirtual returns true if this model was generated from multi-key expansion.
func (c *ModelConfig) IsVirtual() bool {
	return c.isVirtual
}

// Validate checks if the ModelConfig has all required fields.
func (c *ModelConfig) Validate() error {
	if c.ModelName == "" {
		return fmt.Errorf("model_name is required")
	}
	if c.Model == "" {
		return fmt.Errorf("model is required")
	}
	if _, err := providercommon.NormalizeToolSchemaTransform(c.ToolSchemaTransform); err != nil {
		return err
	}

	// Reject whitespace in model identifier
	if strings.ContainsAny(c.Model, " \t\n\r") {
		return fmt.Errorf("model identifier contains whitespace")
	}

	// Reject leading slash
	if strings.HasPrefix(c.Model, "/") {
		return fmt.Errorf("model identifier must not start with /")
	}

	// Reject consecutive slashes
	if strings.Contains(c.Model, "//") {
		return fmt.Errorf("model identifier must not contain //")
	}
	if c.ContextWindow < 0 {
		return fmt.Errorf("context_window must be a positive token count when set")
	}
	return nil
}

func (c *ModelConfig) SetAPIKey(value string) {
	if len(c.APIKeys) > 0 {
		c.APIKeys[0].Set(value)
	} else {
		c.APIKeys = append(c.APIKeys, NewSecureString(value))
	}
}

type BraveConfig struct {
	Enabled bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_BRAVE_ENABLED"`
	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_BRAVE_API_KEYS"`
	// LegacyAPIKey tolerates the deprecated singular "api_key" spelling:
	// sibling providers (gemini/glm_search/baidu_search) use it and older
	// docs/examples advertised it here too. It is folded into APIKeys on
	// decode (APIKeys wins when both are set) and never persisted back.
	LegacyAPIKey SecureString `json:"api_key,omitzero"  yaml:"-"                  env:"-"`
	MaxResults   int          `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_BRAVE_MAX_RESULTS"`
}

// UnmarshalJSON folds the deprecated singular "api_key" alias into APIKeys.
func (c *BraveConfig) UnmarshalJSON(data []byte) error {
	type braveConfigAlias BraveConfig
	if err := json.Unmarshal(data, (*braveConfigAlias)(c)); err != nil {
		return err
	}
	if len(c.APIKeys) == 0 && c.LegacyAPIKey.String() != "" {
		c.APIKeys = SimpleSecureStrings(c.LegacyAPIKey.String())
	}
	return nil
}

// APIKey returns the Brave API key
func (c *BraveConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Brave API key
func (c *BraveConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

func (c *BraveConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type TavilyConfig struct {
	Enabled bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_ENABLED"`
	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_TAVILY_API_KEYS"`
	// LegacyAPIKey tolerates the deprecated singular "api_key" spelling; see
	// BraveConfig.LegacyAPIKey.
	LegacyAPIKey SecureString `json:"api_key,omitzero"  yaml:"-"                  env:"-"`
	BaseURL      string       `json:"base_url"          yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_BASE_URL"`
	MaxResults   int          `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_TAVILY_MAX_RESULTS"`
}

// UnmarshalJSON folds the deprecated singular "api_key" alias into APIKeys.
func (c *TavilyConfig) UnmarshalJSON(data []byte) error {
	type tavilyConfigAlias TavilyConfig
	if err := json.Unmarshal(data, (*tavilyConfigAlias)(c)); err != nil {
		return err
	}
	if len(c.APIKeys) == 0 && c.LegacyAPIKey.String() != "" {
		c.APIKeys = SimpleSecureStrings(c.LegacyAPIKey.String())
	}
	return nil
}

// APIKey returns the Tavily API key
func (c *TavilyConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Tavily API key
func (c *TavilyConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Tavily API keys
func (c *TavilyConfig) SetAPIKeys(keys []string) {
	c.APIKeys = make(SecureStrings, len(keys))
	for i, k := range keys {
		c.APIKeys[i] = NewSecureString(k)
	}
}

type KagiConfig struct {
	Enabled bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_ENABLED"`
	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_KAGI_API_KEYS"`
	// LegacyAPIKey tolerates the deprecated singular "api_key" spelling; see
	// BraveConfig.LegacyAPIKey.
	LegacyAPIKey SecureString `json:"api_key,omitzero"  yaml:"-"                  env:"-"`
	BaseURL      string       `json:"base_url"          yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_BASE_URL"`
	MaxResults   int          `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_KAGI_MAX_RESULTS"`
}

// UnmarshalJSON folds the deprecated singular "api_key" alias into APIKeys.
func (c *KagiConfig) UnmarshalJSON(data []byte) error {
	type kagiConfigAlias KagiConfig
	if err := json.Unmarshal(data, (*kagiConfigAlias)(c)); err != nil {
		return err
	}
	if len(c.APIKeys) == 0 && c.LegacyAPIKey.String() != "" {
		c.APIKeys = SimpleSecureStrings(c.LegacyAPIKey.String())
	}
	return nil
}

// APIKey returns the Kagi API key
func (c *KagiConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Kagi API key
func (c *KagiConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

// SetAPIKeys sets the Kagi API keys
func (c *KagiConfig) SetAPIKeys(keys []string) {
	c.APIKeys = SimpleSecureStrings(keys...)
}

type DuckDuckGoConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_DUCKDUCKGO_MAX_RESULTS"`
}

type SogouConfig struct {
	Enabled    bool `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_SOGOU_ENABLED"`
	MaxResults int  `json:"max_results" env:"PICOCLAW_TOOLS_WEB_SOGOU_MAX_RESULTS"`
}

type GeminiSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_GEMINI_API_KEY"`
	Model      string       `json:"model"            yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_MODEL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GEMINI_MAX_RESULTS"`
}

type PerplexityConfig struct {
	Enabled bool          `json:"enabled"           yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_ENABLED"`
	APIKeys SecureStrings `json:"api_keys,omitzero" yaml:"api_keys,omitempty" env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_API_KEYS"`
	// LegacyAPIKey tolerates the deprecated singular "api_key" spelling; see
	// BraveConfig.LegacyAPIKey.
	LegacyAPIKey SecureString `json:"api_key,omitzero"  yaml:"-"                  env:"-"`
	MaxResults   int          `json:"max_results"       yaml:"-"                  env:"PICOCLAW_TOOLS_WEB_PERPLEXITY_MAX_RESULTS"`
}

// UnmarshalJSON folds the deprecated singular "api_key" alias into APIKeys.
func (c *PerplexityConfig) UnmarshalJSON(data []byte) error {
	type perplexityConfigAlias PerplexityConfig
	if err := json.Unmarshal(data, (*perplexityConfigAlias)(c)); err != nil {
		return err
	}
	if len(c.APIKeys) == 0 && c.LegacyAPIKey.String() != "" {
		c.APIKeys = SimpleSecureStrings(c.LegacyAPIKey.String())
	}
	return nil
}

// APIKey returns the Perplexity API key
func (c *PerplexityConfig) APIKey() string {
	if len(c.APIKeys) == 0 {
		return ""
	}
	return c.APIKeys[0].String()
}

// SetAPIKey sets the Perplexity API key
func (c *PerplexityConfig) SetAPIKey(key string) {
	c.APIKeys = SimpleSecureStrings(key)
}

type SearXNGConfig struct {
	Enabled    bool   `json:"enabled"     env:"PICOCLAW_TOOLS_WEB_SEARXNG_ENABLED"`
	BaseURL    string `json:"base_url"    env:"PICOCLAW_TOOLS_WEB_SEARXNG_BASE_URL"`
	MaxResults int    `json:"max_results" env:"PICOCLAW_TOOLS_WEB_SEARXNG_MAX_RESULTS"`
}

type GLMSearchConfig struct {
	Enabled bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_ENABLED"`
	APIKey  SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_GLM_API_KEY"`
	BaseURL string       `json:"base_url"         yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_BASE_URL"`
	// SearchEngine specifies the search backend: "search_std" (default),
	// "search_pro", "search_pro_sogou", or "search_pro_quark".
	SearchEngine string `json:"search_engine"    yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_SEARCH_ENGINE"`
	MaxResults   int    `json:"max_results"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_GLM_MAX_RESULTS"`
}

type BaiduSearchConfig struct {
	Enabled    bool         `json:"enabled"          yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_ENABLED"`
	APIKey     SecureString `json:"api_key,omitzero" yaml:"api_key,omitempty" env:"PICOCLAW_TOOLS_WEB_BAIDU_API_KEY"`
	BaseURL    string       `json:"base_url"         yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_BASE_URL"`
	MaxResults int          `json:"max_results"      yaml:"-"                 env:"PICOCLAW_TOOLS_WEB_BAIDU_MAX_RESULTS"`
}
