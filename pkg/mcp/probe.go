package mcp

import (
	"context"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
)

// ProbeResult reports the outcome of a one-shot MCP server connectivity probe.
type ProbeResult struct {
	LatencyMS int64
	ToolCount int
	Tools     []*mcp.Tool
}

// ProbeServer dials a single MCP server, lists its tools and closes the
// connection. It shares the transport and env-file resolution logic with the
// regular manager load path (relative envFile paths resolve against
// workspacePath), and is intended for launcher-side "test connection"
// actions; it never mutates persistent state.
func ProbeServer(
	ctx context.Context,
	serverName string,
	cfg config.MCPServerConfig,
	workspacePath string,
	timeout time.Duration,
) (*ProbeResult, error) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if serverName == "" {
		serverName = "probe"
	}
	cfg.Enabled = true

	started := time.Now()
	manager := NewManager()
	defer func() { _ = manager.Close() }()

	probeCfg := config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers:    map[string]config.MCPServerConfig{serverName: cfg},
	}
	if err := manager.LoadFromMCPConfig(ctx, probeCfg, workspacePath); err != nil {
		return nil, err
	}

	tools := manager.GetAllTools()[serverName]
	if tools == nil {
		tools = []*mcp.Tool{}
	}
	return &ProbeResult{
		LatencyMS: time.Since(started).Milliseconds(),
		ToolCount: len(tools),
		Tools:     tools,
	}, nil
}
