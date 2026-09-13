package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newProbeTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "probe-test-server",
		Version: "1.0.0",
	}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Description: "Echo test tool",
	}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
		}, nil, nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "second"}, func(ctx context.Context, req *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, nil, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func TestProbeServer_StreamableHTTP(t *testing.T) {
	httpServer := newProbeTestServer(t)

	res, err := ProbeServer(context.Background(), "probe-target", config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     httpServer.URL,
	}, t.TempDir(), 10*time.Second)
	if err != nil {
		t.Fatalf("ProbeServer() error = %v", err)
	}
	if res.ToolCount != 2 {
		t.Fatalf("ToolCount = %d, want 2", res.ToolCount)
	}
	if res.LatencyMS < 0 {
		t.Fatalf("LatencyMS = %d, want >= 0", res.LatencyMS)
	}
	if len(res.Tools) != 2 || res.Tools[0].Name != "echo" {
		t.Fatalf("Tools = %+v", res.Tools)
	}
}

func TestProbeServer_ConnectionError(t *testing.T) {
	// 先建后关，拿到一个确定无人监听的端口
	httpServer := httptest.NewServer(nil)
	url := httpServer.URL
	httpServer.Close()

	_, err := ProbeServer(context.Background(), "probe-target", config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     url,
	}, t.TempDir(), 5*time.Second)
	if err == nil {
		t.Fatal("ProbeServer() on dead URL should fail")
	}
}
