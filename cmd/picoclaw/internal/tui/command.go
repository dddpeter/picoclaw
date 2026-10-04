package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bubbletea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/picoclient"
)

// defaultGatewayPort mirrors config defaults (pkg/config/defaults.go).
const defaultGatewayPort = 18790

type tuiOptions struct {
	GatewayURL string
	Token      string
	SessionID  string
	NewSession bool
}

// NewTUICommand builds the "picoclaw tui" subcommand: a terminal client for a
// running gateway (docs/design/tui-client-design.zh.md).
func NewTUICommand() *cobra.Command {
	opts := &tuiOptions{}
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "在终端里连接运行中的网关聊天（Pico Protocol /pico/ws）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd.Context(), cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "",
		"网关 WS 端点（默认 ws://<gateway.host>:<gateway.port>/pico/ws，读 config.json）")
	cmd.Flags().StringVar(&opts.Token, "token", "",
		"pico 渠道 token（默认读 config.json channels.pico.token；仅调试用，会进 shell history）")
	cmd.Flags().StringVar(&opts.SessionID, "session", "",
		"pico 会话 ID（默认恢复上次会话）")
	cmd.Flags().BoolVar(&opts.NewSession, "new", false,
		"忽略已保存的会话，开一个全新会话")
	return cmd
}

func runTUI(ctx context.Context, cmd *cobra.Command, opts *tuiOptions) error {
	token := opts.Token
	gatewayURL := opts.GatewayURL

	if token == "" || gatewayURL == "" {
		cfg, cfgErr := internal.LoadConfig()
		if cfgErr != nil {
			return fmt.Errorf("读取配置失败（%v）；可改用 --gateway-url 与 --token 显式指定", cfgErr)
		}
		if token == "" {
			token = picoTokenFromConfig(cfg)
			if token == "" {
				return errors.New("未找到 pico 渠道 token：请检查 config.json 的 channels.pico.token，或用 --token 指定")
			}
		}
		if gatewayURL == "" {
			gatewayURL = gatewayURLFromConfig(cfg)
		}
	}
	if strings.HasPrefix(gatewayURL, "ws://") && !isLocalhostURL(gatewayURL) {
		fmt.Fprintln(cmd.ErrOrStderr(), "⚠ --gateway-url 使用明文 ws:// 连接非本机地址，token 将明文传输；生产环境建议 wss://")
	}

	sessionFile := sessionStorePath()
	sessionID := opts.SessionID
	if sessionID == "" && !opts.NewSession {
		sessionID = loadStoredSession(sessionFile)
	}
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	client := picoclient.New(picoclient.Config{
		URL:       gatewayURL,
		Token:     token,
		SessionID: sessionID,
	})
	client.Start(ctx)

	model := newAppModel(client, sessionFile)
	program := bubbletea.NewProgram(model, bubbletea.WithContext(ctx))
	if _, runErr := program.Run(); runErr != nil {
		client.Stop()
		return fmt.Errorf("tui 运行失败: %w", runErr)
	}
	client.Stop()
	return nil
}

func picoTokenFromConfig(cfg *config.Config) string {
	if cfg == nil || cfg.Channels == nil {
		return ""
	}
	bc, ok := cfg.Channels[config.ChannelPico]
	if !ok {
		return ""
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		return ""
	}
	ps, ok := decoded.(*config.PicoSettings)
	if !ok {
		return ""
	}
	return ps.Token.String()
}

func gatewayURLFromConfig(cfg *config.Config) string {
	host := cfg.Gateway.Host
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	port := cfg.Gateway.Port
	if port <= 0 {
		port = defaultGatewayPort
	}
	return fmt.Sprintf("ws://%s:%d/pico/ws", host, port)
}

func isLocalhostURL(raw string) bool {
	u := strings.TrimPrefix(strings.TrimPrefix(raw, "wss://"), "ws://")
	host := u
	if i := strings.IndexByte(u, '/'); i >= 0 {
		host = u[:i]
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host, "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// sessionStorePath keeps the last pico session id next to config.json so the
// TUI resumes where it left off. Only the id is stored, never the token.
func sessionStorePath() string {
	return filepath.Join(filepath.Dir(internal.GetConfigPath()), "tui_session")
}

func loadStoredSession(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(data))
	if id == "" || len(id) > 64 || strings.ContainsAny(id, " \t\r\n/\\") {
		return ""
	}
	return id
}
