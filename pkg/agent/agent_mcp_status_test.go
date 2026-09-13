package agent

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
)

func statusTestConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"live": {Enabled: true},
		"dead": {Enabled: true},
		"off":  {Enabled: false},
	}
	return cfg
}

func TestBuildMCPStatusSnapshot_NotInitialized(t *testing.T) {
	snap := buildMCPStatusSnapshot(statusTestConfig(), nil, false)
	if snap.Initialized {
		t.Fatal("Initialized = true, want false when manager is nil")
	}
	if len(snap.Servers) != 0 {
		t.Fatalf("Servers = %v, want empty (frontend renders not_initialized for all)", snap.Servers)
	}
	if !snap.Enabled {
		t.Fatal("Enabled = false, want true")
	}
}

func TestBuildMCPStatusSnapshot_ConnectedAndMissing(t *testing.T) {
	servers := map[string]*mcp.ServerConnection{
		"live": {
			Name:  "live",
			Tools: []*sdkmcp.Tool{{Name: "t1", Description: "d1"}, {Name: "t2"}, nil},
		},
	}
	snap := buildMCPStatusSnapshot(statusTestConfig(), servers, true)
	if !snap.Initialized {
		t.Fatal("Initialized = false, want true")
	}

	live := snap.Servers["live"]
	if !live.Connected || live.ToolCount != 2 {
		t.Fatalf("live = %+v, want connected with 2 tools (nil tool skipped)", live)
	}
	if len(live.Tools) != 2 || live.Tools[0].Name != "t1" || live.Tools[0].Description != "d1" {
		t.Fatalf("live.Tools = %+v", live.Tools)
	}

	dead := snap.Servers["dead"]
	if dead.Connected {
		t.Fatal("dead.Connected = true, want false")
	}
	if dead.Error == "" {
		t.Fatal("dead.Error is empty, want explanation")
	}

	if _, exists := snap.Servers["off"]; exists {
		t.Fatal("disabled server should not appear in snapshot")
	}
}

func TestBuildMCPStatusSnapshot_DescriptionTruncated(t *testing.T) {
	long := make([]rune, 300)
	for i := range long {
		long[i] = '字'
	}
	servers := map[string]*mcp.ServerConnection{
		"live": {Name: "live", Tools: []*sdkmcp.Tool{{Name: "t", Description: string(long)}}},
	}
	snap := buildMCPStatusSnapshot(statusTestConfig(), servers, true)
	got := []rune(snap.Servers["live"].Tools[0].Description)
	if len(got) != 201 || got[200] != '…' {
		t.Fatalf("description length = %d, want 200 runes + ellipsis", len(got))
	}
}

func TestMCPStatusSnapshotMethod_MarshalsToContractShape(t *testing.T) {
	// 形状契约：前端与代理端点依赖 camelCase 字段名。
	snap := buildMCPStatusSnapshot(statusTestConfig(), map[string]*mcp.ServerConnection{}, true)
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"initialized", "enabled", "servers"} {
		if _, ok := probe[key]; !ok {
			t.Fatalf("missing key %q in %s", key, data)
		}
	}
}
