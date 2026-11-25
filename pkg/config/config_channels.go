package config

import (
	"math/rand"
)

// GroupTriggerConfig controls when the bot responds in group chats.
type GroupTriggerConfig struct {
	MentionOnly bool     `json:"mention_only,omitempty"`
	Prefixes    []string `json:"prefixes,omitempty"`
}

// TypingConfig controls typing indicator behavior (Phase 10).
type TypingConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// PlaceholderConfig controls placeholder message behavior (Phase 10).
type PlaceholderConfig struct {
	Enabled bool                `json:"enabled"`
	Text    FlexibleStringSlice `json:"text,omitempty"`
}

// GetRandomText returns a random placeholder text, or default if none set.
func (p *PlaceholderConfig) GetRandomText() string {
	if len(p.Text) == 0 {
		return "Thinking..."
	}
	if len(p.Text) == 1 {
		return p.Text[0]
	}
	idx := rand.Intn(len(p.Text))
	return p.Text[idx]
}

type StreamingConfig struct {
	Enabled         bool `json:"enabled,omitempty"`
	ThrottleSeconds int  `json:"throttle_seconds,omitempty"`
	MinGrowthChars  int  `json:"min_growth_chars,omitempty"`
}

func (c StreamingConfig) IsZero() bool {
	return !c.Enabled && c.ThrottleSeconds == 0 && c.MinGrowthChars == 0
}

func (c StreamingConfig) WithDefaults(throttleSeconds, minGrowthChars int) StreamingConfig {
	if c.Enabled {
		if c.ThrottleSeconds == 0 {
			c.ThrottleSeconds = throttleSeconds
		}
		if c.MinGrowthChars == 0 {
			c.MinGrowthChars = minGrowthChars
		}
	}
	return c
}

type WhatsAppSettings struct {
	BridgeURL        string `json:"bridge_url"         yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_BRIDGE_URL"`
	UseNative        bool   `json:"use_native"         yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_USE_NATIVE"`
	SessionStorePath string `json:"session_store_path" yaml:"-" env:"PICOCLAW_CHANNELS_WHATSAPP_SESSION_STORE_PATH"`
}

type TelegramSettings struct {
	Token             SecureString    `json:"token,omitzero"       yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_TELEGRAM_TOKEN"`
	BaseURL           string          `json:"base_url"             yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_BASE_URL"`
	Proxy             string          `json:"proxy"                yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_PROXY"`
	Streaming         StreamingConfig `json:"streaming,omitzero"   yaml:"-"`
	UseMarkdownV2     bool            `json:"use_markdown_v2"      yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_USE_MARKDOWN_V2"`
	MediaGroupDelayMS int             `json:"media_group_delay_ms" yaml:"-"               env:"PICOCLAW_CHANNELS_TELEGRAM_MEDIA_GROUP_DELAY_MS"`
}

type FeishuSettings struct {
	AppID               string              `json:"app_id"                      yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_APP_ID"`
	AppSecret           SecureString        `json:"app_secret,omitzero"         yaml:"app_secret,omitempty"         env:"PICOCLAW_CHANNELS_FEISHU_APP_SECRET"`
	EncryptKey          SecureString        `json:"encrypt_key,omitzero"        yaml:"encrypt_key,omitempty"        env:"PICOCLAW_CHANNELS_FEISHU_ENCRYPT_KEY"`
	VerificationToken   SecureString        `json:"verification_token,omitzero" yaml:"verification_token,omitempty" env:"PICOCLAW_CHANNELS_FEISHU_VERIFICATION_TOKEN"`
	RandomReactionEmoji FlexibleStringSlice `json:"random_reaction_emoji"       yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_RANDOM_REACTION_EMOJI"`
	IsLark              bool                `json:"is_lark"                     yaml:"-"                            env:"PICOCLAW_CHANNELS_FEISHU_IS_LARK"`
	Streaming           StreamingConfig     `json:"streaming,omitzero"          yaml:"streaming,omitempty"`
}

type DiscordSettings struct {
	Token SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_DISCORD_TOKEN"`
	Proxy string       `json:"proxy"          yaml:"-"               env:"PICOCLAW_CHANNELS_DISCORD_PROXY"`
}

type MaixCamSettings struct {
	Host string `json:"host" yaml:"-" env:"PICOCLAW_CHANNELS_MAIXCAM_HOST"`
	Port int    `json:"port" yaml:"-" env:"PICOCLAW_CHANNELS_MAIXCAM_PORT"`
}

type QQSettings struct {
	AppID                string       `json:"app_id"                   yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_APP_ID"`
	AppSecret            SecureString `json:"app_secret,omitzero"      yaml:"app_secret,omitempty" env:"PICOCLAW_CHANNELS_QQ_APP_SECRET"`
	MaxMessageLength     int          `json:"max_message_length"       yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_MAX_MESSAGE_LENGTH"`
	MaxBase64FileSizeMiB int64        `json:"max_base64_file_size_mib" yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_MAX_BASE64_FILE_SIZE_MIB"`
	SendMarkdown         bool         `json:"send_markdown"            yaml:"-"                    env:"PICOCLAW_CHANNELS_QQ_SEND_MARKDOWN"`
}

type DingTalkSettings struct {
	ClientID     string       `json:"client_id"              yaml:"-"                       env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_ID"`
	ClientSecret SecureString `json:"client_secret,omitzero" yaml:"client_secret,omitempty" env:"PICOCLAW_CHANNELS_DINGTALK_CLIENT_SECRET"`
}

type SlackSettings struct {
	BotToken SecureString `json:"bot_token,omitzero" yaml:"bot_token,omitempty" env:"PICOCLAW_CHANNELS_SLACK_BOT_TOKEN"`
	AppToken SecureString `json:"app_token,omitzero" yaml:"app_token,omitempty" env:"PICOCLAW_CHANNELS_SLACK_APP_TOKEN"`
}

type MatrixSettings struct {
	Homeserver         string       `json:"homeserver"                     yaml:"-"                      env:"PICOCLAW_CHANNELS_MATRIX_HOMESERVER"`
	UserID             string       `json:"user_id"                        yaml:"-"                      env:"PICOCLAW_CHANNELS_MATRIX_USER_ID"`
	AccessToken        SecureString `json:"access_token,omitzero"          yaml:"access_token,omitempty" env:"PICOCLAW_CHANNELS_MATRIX_ACCESS_TOKEN"`
	DeviceID           string       `json:"device_id,omitempty"            yaml:"-"`
	JoinOnInvite       bool         `json:"join_on_invite"                 yaml:"-"`
	MessageFormat      string       `json:"message_format,omitempty"       yaml:"-"`
	CryptoDatabasePath string       `json:"crypto_database_path,omitempty" yaml:"-"`
	CryptoPassphrase   string       `json:"crypto_passphrase,omitempty"    yaml:"-"`
}

// DeltaChatSettings configures the Delta Chat channel. Delta Chat is an
// email-based, end-to-end encrypted messenger; PicoClaw talks to a local
// `deltachat-rpc-server` process over JSON-RPC (stdio).
//
// Email is the only required setting. A full address selects an already
// configured account in DataDir; a first-run marker such as "@nine.testrun.org"
// creates a chatmail account and tells the user which full email to save.
// Mailbox credentials stay in the Delta Chat account store. DisplayName and
// AvatarImage are optional profile settings applied on startup. Password remains
// only for legacy PicoClaw-managed email configuration.
type DeltaChatSettings struct {
	Email          string       `json:"email"                     yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_EMAIL"`
	Password       SecureString `json:"password,omitzero"         yaml:"password,omitempty" env:"PICOCLAW_CHANNELS_DELTACHAT_PASSWORD"`
	DisplayName    string       `json:"display_name,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_DISPLAY_NAME"`
	AvatarImage    string       `json:"avatar_image,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_AVATAR_IMAGE"`
	DataDir        string       `json:"data_dir,omitempty"        yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_DATA_DIR"`
	RPCServerPath  string       `json:"rpc_server_path,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_RPC_SERVER_PATH"`
	InviteLink     string       `json:"invite_link,omitempty"     yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_INVITE_LINK"`
	AllowCrosspost bool         `json:"allow_crosspost,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_DELTACHAT_ALLOW_CROSSPOST"`
	IMAPServer     string       `json:"imap_server,omitempty"     yaml:"-"`
	IMAPPort       int          `json:"imap_port,omitempty"       yaml:"-"`
	SMTPServer     string       `json:"smtp_server,omitempty"     yaml:"-"`
	SMTPPort       int          `json:"smtp_port,omitempty"       yaml:"-"`
}

type LINESettings struct {
	ChannelSecret      SecureString `json:"channel_secret,omitzero"       yaml:"channel_secret,omitempty"       env:"PICOCLAW_CHANNELS_LINE_CHANNEL_SECRET"`
	ChannelAccessToken SecureString `json:"channel_access_token,omitzero" yaml:"channel_access_token,omitempty" env:"PICOCLAW_CHANNELS_LINE_CHANNEL_ACCESS_TOKEN"`
	WebhookPath        string       `json:"webhook_path"                  yaml:"-"                              env:"PICOCLAW_CHANNELS_LINE_WEBHOOK_PATH"`
}

type OneBotSettings struct {
	WSUrl             string       `json:"ws_url"                yaml:"-"                      env:"PICOCLAW_CHANNELS_ONEBOT_WS_URL"`
	AccessToken       SecureString `json:"access_token,omitzero" yaml:"access_token,omitempty" env:"PICOCLAW_CHANNELS_ONEBOT_ACCESS_TOKEN"`
	ReconnectInterval int          `json:"reconnect_interval"    yaml:"-"                      env:"PICOCLAW_CHANNELS_ONEBOT_RECONNECT_INTERVAL"`
}

type WeComGroupConfig struct {
	AllowFrom FlexibleStringSlice `json:"allow_from,omitempty"`
}

type WeComSettings struct {
	BotID               string          `json:"bot_id"                  yaml:"-"                env:"BOT_ID"`
	Secret              SecureString    `json:"secret,omitzero"         yaml:"secret,omitempty" env:"SECRET"`
	WebSocketURL        string          `json:"websocket_url,omitempty" yaml:"-"                env:"WEBSOCKET_URL"`
	SendThinkingMessage bool            `json:"send_thinking_message"   yaml:"-"                env:"SEND_THINKING_MESSAGE"`
	Streaming           StreamingConfig `json:"streaming,omitzero"      yaml:"-"`
}

func (c *WeComSettings) SetSecret(secret string) {
	c.Secret = *NewSecureString(secret)
}

type WeixinSettings struct {
	Token      SecureString `json:"token,omitzero"       yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_WEIXIN_TOKEN"`
	AccountID  string       `json:"account_id,omitempty" yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_ACCOUNT_ID"`
	BaseURL    string       `json:"base_url"             yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_BASE_URL"`
	CDNBaseURL string       `json:"cdn_base_url"         yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_CDN_BASE_URL"`
	Proxy      string       `json:"proxy"                yaml:"-"               env:"PICOCLAW_CHANNELS_WEIXIN_PROXY"`
}

// SetToken sets the Weixin token and marks it as dirty for security saving
func (c *WeixinSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type PicoSettings struct {
	Token           SecureString    `json:"token,omitzero"              yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_PICO_TOKEN"`
	AllowTokenQuery bool            `json:"allow_token_query,omitempty" yaml:"-"`
	AllowOrigins    []string        `json:"allow_origins,omitempty"     yaml:"-"`
	Streaming       StreamingConfig `json:"streaming,omitzero"          yaml:"-"`
	PingInterval    int             `json:"ping_interval,omitempty"     yaml:"-"`
	ReadTimeout     int             `json:"read_timeout,omitempty"      yaml:"-"`
	MaxConnections  int             `json:"max_connections,omitempty"   yaml:"-"`
}

// SetToken sets the Pico token and marks it as dirty for security saving
func (c *PicoSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

type PicoClientSettings struct {
	URL          string       `json:"url"                     yaml:"-"               env:"PICOCLAW_CHANNELS_PICO_CLIENT_URL"`
	Token        SecureString `json:"token,omitzero"          yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_PICO_CLIENT_TOKEN"`
	SessionID    string       `json:"session_id,omitempty"    yaml:"-"`
	PingInterval int          `json:"ping_interval,omitempty" yaml:"-"`
	ReadTimeout  int          `json:"read_timeout,omitempty"  yaml:"-"`
}

type IRCSettings struct {
	Server           string              `json:"server"                     yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_SERVER"`
	TLS              bool                `json:"tls"                        yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_TLS"`
	Nick             string              `json:"nick"                       yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_NICK"`
	User             string              `json:"user,omitempty"             yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_USER"`
	RealName         string              `json:"real_name,omitempty"        yaml:"-"`
	Password         SecureString        `json:"password,omitzero"          yaml:"password,omitempty"          env:"PICOCLAW_CHANNELS_IRC_PASSWORD"`
	NickServPassword SecureString        `json:"nickserv_password,omitzero" yaml:"nickserv_password,omitempty" env:"PICOCLAW_CHANNELS_IRC_NICKSERV_PASSWORD"`
	SASLUser         string              `json:"sasl_user"                  yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_SASL_USER"`
	SASLPassword     SecureString        `json:"sasl_password,omitzero"     yaml:"sasl_password,omitempty"     env:"PICOCLAW_CHANNELS_IRC_SASL_PASSWORD"`
	Channels         FlexibleStringSlice `json:"channels"                   yaml:"-"                           env:"PICOCLAW_CHANNELS_IRC_CHANNELS"`
	RequestCaps      FlexibleStringSlice `json:"request_caps,omitempty"     yaml:"-"`
}

type VKSettings struct {
	Token   SecureString `json:"token,omitzero" yaml:"token,omitempty" env:"PICOCLAW_CHANNELS_VK_TOKEN"`
	GroupID int          `json:"group_id"       yaml:"-"               env:"PICOCLAW_CHANNELS_VK_GROUP_ID"`
}

func (c *VKSettings) SetToken(token string) {
	c.Token = *NewSecureString(token)
}

// TeamsWebhookSettings configures the output-only Microsoft Teams webhook channel.
// Multiple webhook targets can be configured and selected via ChatID at send time.
type TeamsWebhookSettings struct {
	Webhooks map[string]TeamsWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// TeamsWebhookTarget represents a single Teams webhook destination.
type TeamsWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Title      string       `json:"title,omitempty"      yaml:"-"`
}

type MQTTSettings struct {
	Broker      string       `json:"broker"                    yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_BROKER"`
	AgentID     string       `json:"agent_id"                  yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_AGENT_ID"`
	TopicPrefix string       `json:"topic_prefix,omitempty"    yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_TOPIC_PREFIX"`
	Username    SecureString `json:"username,omitzero"         yaml:"username,omitempty" env:"PICOCLAW_CHANNELS_MQTT_USERNAME"`
	Password    SecureString `json:"password,omitzero"         yaml:"password,omitempty" env:"PICOCLAW_CHANNELS_MQTT_PASSWORD"`
	ClientID    string       `json:"client_id,omitempty"       yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_CLIENT_ID"`
	KeepAlive   int          `json:"keep_alive,omitempty"      yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_KEEP_ALIVE"`
	QoS         int          `json:"qos,omitempty"             yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_QOS"`
	// TLSSkipVerify keeps the historical insecure default (nil = skip cert
	// verification) so existing self-signed deployments keep working; set it
	// to false to enable certificate verification (2026-10-31 review B7).
	TLSSkipVerify *bool `json:"tls_skip_verify,omitempty" yaml:"-"                  env:"PICOCLAW_CHANNELS_MQTT_TLS_SKIP_VERIFY"`
}

// SlackWebhookSettings configures the output-only Slack webhook channel.
type SlackWebhookSettings struct {
	Webhooks map[string]SlackWebhookTarget `json:"webhooks" yaml:"webhooks,omitempty"`
}

// SlackWebhookTarget represents a single Slack Incoming Webhook destination.
type SlackWebhookTarget struct {
	WebhookURL SecureString `json:"webhook_url,omitzero" yaml:"webhook_url,omitempty"`
	Username   string       `json:"username,omitempty"   yaml:"-"`
	IconEmoji  string       `json:"icon_emoji,omitempty" yaml:"-"`
}
