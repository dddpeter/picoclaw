package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"

	mcp "github.com/sipeed/picoclaw/pkg/mcp"
)

func newMCPTestServer(t *testing.T) (string, *http.ServeMux) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return configPath, mux
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMCPConfigGet_EmptyByDefault(t *testing.T) {
	_, mux := newMCPTestServer(t)

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp mcpConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Enabled {
		t.Error("enabled should default false")
	}
	if resp.Servers == nil || len(resp.Servers) != 0 {
		t.Errorf("servers = %v, want empty non-nil array", resp.Servers)
	}
}

func TestMCPConfigPutThenGetRoundTrip(t *testing.T) {
	configPath, mux := newMCPTestServer(t)

	deferred := true
	putBody := mcpConfigRequest{
		Enabled:            true,
		MaxInlineTextChars: 8192,
		Discovery: mcpDiscoveryDTO{
			Enabled: true, TTLSeconds: 10, MaxSearchResults: 7,
			UseBM25: false, UseRegex: true,
		},
		Servers: []mcpServerDTO{
			{Name: "ov", Type: "http", Enabled: true, Deferred: &deferred,
				URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"},
				Args: []string{}, Env: map[string]string{}},
			{Name: "local", Type: "stdio", Enabled: false, Deferred: nil,
				Command: "npx", Args: []string{"-y", "server-foo"}, Env: map[string]string{"K": "V"},
				EnvFile: ".env"},
		},
	}
	rec := doJSON(t, mux, http.MethodPut, "/api/mcp/config", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil)
	var resp mcpConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("get unmarshal: %v", err)
	}
	if !resp.Enabled || resp.MaxInlineTextChars != 8192 {
		t.Fatalf("global fields = %+v", resp)
	}
	if !resp.Discovery.Enabled || resp.Discovery.TTLSeconds != 10 ||
		resp.Discovery.MaxSearchResults != 7 {
		t.Fatalf("discovery = %+v", resp.Discovery)
	}
	if resp.Discovery.UseBM25 || !resp.Discovery.UseRegex {
		t.Fatalf("discovery toggles = %+v, want useBM25=false useRegex=true", resp.Discovery)
	}
	if len(resp.Servers) != 2 {
		t.Fatalf("servers = %+v", resp.Servers)
	}
	byName := map[string]mcpServerDTO{}
	for _, s := range resp.Servers {
		byName[s.Name] = s
	}
	if got := byName["ov"]; got.URL != "https://example.com/mcp" || got.Deferred == nil || !*got.Deferred {
		t.Fatalf("ov server = %+v", got)
	}
	if got := byName["local"]; got.Command != "npx" || got.EnvFile != ".env" || got.Deferred != nil {
		t.Fatalf("local server = %+v", got)
	}

	// 落盘校验
	disk, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("disk load: %v", err)
	}
	if !disk.Tools.MCP.Enabled || len(disk.Tools.MCP.Servers) != 2 {
		t.Fatalf("disk servers = %+v", disk.Tools.MCP.Servers)
	}
	if got := disk.Tools.MCP.Servers["local"]; len(got.Args) != 2 {
		t.Fatalf("disk local args = %v", got.Args)
	}
}

func TestMCPConfigPut_ValidationMatrix(t *testing.T) {
	_, mux := newMCPTestServer(t)

	cases := []struct {
		name    string
		servers []mcpServerDTO
		wantSub string
	}{
		{"empty name", []mcpServerDTO{{Name: "  ", Type: "stdio", Command: "x"}}, "name"},
		{"dup name", []mcpServerDTO{
			{Name: "a", Type: "stdio", Command: "x"},
			{Name: "a", Type: "stdio", Command: "y"},
		}, "duplicate"},
		{"stdio without command", []mcpServerDTO{{Name: "a", Type: "stdio"}}, "command"},
		{"http without url", []mcpServerDTO{{Name: "a", Type: "http"}}, "url"},
		{"bad type", []mcpServerDTO{{Name: "a", Type: "websocket", Command: "x"}}, "invalid type"},
	}
	for _, tc := range cases {
		rec := doJSON(t, mux, http.MethodPut, "/api/mcp/config", mcpConfigRequest{Servers: tc.servers})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body=%s", tc.name, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.wantSub) {
			t.Errorf("%s: body %q missing %q", tc.name, rec.Body.String(), tc.wantSub)
		}
	}
}

func TestMCPConfigGet_LenientOnUnknownFields(t *testing.T) {
	configPath, mux := newMCPTestServer(t)

	// 写入带未知字段的配置：GET 仍须 200（lenient 加载）
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var disk map[string]any
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	disk["some_unknown_future_field"] = map[string]any{"a": 1}
	patched, err := json.Marshal(disk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(configPath, patched, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestMCPServer_Success(t *testing.T) {
	_, mux := newMCPTestServer(t)

	old := mcpProbeFunc
	mcpProbeFunc = func(ctx context.Context, name string, cfg config.MCPServerConfig, workspace string, timeout time.Duration) (*mcp.ProbeResult, error) {
		if name != "ov" || cfg.URL != "https://example.com/mcp" || cfg.Type != "http" {
			t.Errorf("probe args = %q %+v", name, cfg)
		}
		return &mcp.ProbeResult{
			LatencyMS: 42,
			ToolCount: 1,
			Tools:     []*sdkmcp.Tool{{Name: "t1", Description: "d1"}},
		}, nil
	}
	defer func() { mcpProbeFunc = old }()

	rec := doJSON(t, mux, http.MethodPost, "/api/mcp/servers/test", mcpServerDTO{
		Name: "ov", Type: "http", URL: "https://example.com/mcp", Enabled: true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp mcpServerTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK || resp.LatencyMS != 42 || resp.ToolCount != 1 || len(resp.Tools) != 1 || resp.Tools[0].Name != "t1" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHandleTestMCPServer_ProbeErrorAndValidation(t *testing.T) {
	_, mux := newMCPTestServer(t)

	old := mcpProbeFunc
	mcpProbeFunc = func(ctx context.Context, name string, cfg config.MCPServerConfig, workspace string, timeout time.Duration) (*mcp.ProbeResult, error) {
		return nil, fmt.Errorf("dial timeout")
	}
	defer func() { mcpProbeFunc = old }()

	rec := doJSON(t, mux, http.MethodPost, "/api/mcp/servers/test", mcpServerDTO{
		Name: "bad", Type: "http", URL: "http://127.0.0.1:1", Enabled: true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("probe-failure status = %d, want 200 (ok:false payload)", rec.Code)
	}
	var resp mcpServerTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "dial timeout") {
		t.Fatalf("resp = %+v", resp)
	}

	// 校验失败 → 400
	rec = doJSON(t, mux, http.MethodPost, "/api/mcp/servers/test", mcpServerDTO{Name: "x", Type: "http"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing-url status = %d, want 400", rec.Code)
	}
}

func TestHandleGetMCPStatus_ProxiesWithBearerToken(t *testing.T) {
	_, mux := newMCPTestServer(t)

	var gotAuth, gotPath string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"enabled":true,"servers":{}}`))
	}))
	defer gw.Close()

	u, err := url.Parse(gw.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	oldPID := gateway.pidData
	gateway.pidData = &ppid.PidFileData{Host: u.Hostname(), Port: port, Token: "secret-token"}
	defer func() { gateway.pidData = oldPID }()

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/mcp/status" {
		t.Fatalf("gateway path = %q", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(rec.Body.String(), `"initialized":true`) {
		t.Fatalf("body not proxied: %s", rec.Body.String())
	}
}

func TestHandleGetMCPStatus_OfflineWhenUnreachable(t *testing.T) {
	_, mux := newMCPTestServer(t)

	// 拿一个确定无人监听的端口
	dead := httptest.NewServer(nil)
	deadURL, _ := url.Parse(dead.URL)
	deadPort, _ := strconv.Atoi(deadURL.Port())
	dead.Close()

	oldPID := gateway.pidData
	gateway.pidData = &ppid.PidFileData{Host: "127.0.0.1", Port: deadPort, Token: "t"}
	defer func() { gateway.pidData = oldPID }()

	rec := doJSON(t, mux, http.MethodGet, "/api/mcp/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"offline"`) {
		t.Fatalf("body = %s, want offline", rec.Body.String())
	}
}
