package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func historyTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: t.TempDir()}},
	}
}

func writeSessionFiles(t *testing.T, cfg *config.Config, key string, metaJSON string, lines ...string) {
	t.Helper()
	dir := filepath.Join(cfg.Agents.Defaults.Workspace, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sanitizeSessionKey(key)+".meta.json"), []byte(metaJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, sanitizeSessionKey(key)+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func msgLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLoadSessionHistory_ScopeBasedPico(t *testing.T) {
	cfg := historyTestConfig(t)
	now := time.Now().UTC().Truncate(time.Second)
	meta := map[string]any{
		"key":        "sk_v1_abc123",
		"title":      "排查 TUI 断连",
		"skip":       0,
		"created_at": now.Format(time.RFC3339Nano),
		"updated_at": now.Format(time.RFC3339Nano),
		"scope": map[string]any{
			"version": 1,
			"channel": "pico",
			"values":  map[string]string{"sender": "pico:11111111-2222-3333-4444-555555555555"},
		},
	}
	metaJSON, _ := json.Marshal(meta)
	writeSessionFiles(t, cfg, "sk_v1_abc123", string(metaJSON),
		msgLine(t, protocoltypes.Message{Role: "user", Content: "帮我看下 429"}),
		msgLine(t, protocoltypes.Message{Role: "assistant", Content: "正在分析", ReasoningContent: "先看日志", ModelName: "glm-4.7"}),
	)

	h := LoadSessionHistory(cfg, "11111111-2222-3333-4444-555555555555")
	if !h.Found || h.Title != "排查 TUI 断连" {
		t.Fatalf("history not resolved: %+v", h)
	}
	if len(h.Items) != 3 {
		t.Fatalf("items = %d, want 3: %+v", len(h.Items), h.Items)
	}
	if h.Items[0].Kind != ItemUser || h.Items[0].Content != "帮我看下 429" {
		t.Fatalf("user item wrong: %+v", h.Items[0])
	}
	// An assistant record with both reasoning and content yields the folded
	// thought line first, then the answer line.
	if h.Items[1].Kind != ItemThought || h.Items[1].Content != "先看日志" {
		t.Fatalf("thought item wrong: %+v", h.Items[1])
	}
	if h.Items[2].Kind != ItemAnswer || h.Items[2].Content != "正在分析" || h.Items[2].ModelName != "glm-4.7" {
		t.Fatalf("answer item wrong: %+v", h.Items[2])
	}
}

func TestLoadSessionHistory_LegacyKeyPrefix(t *testing.T) {
	cfg := historyTestConfig(t)
	meta := map[string]any{
		"key":   "agent:main:pico:direct:pico:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"title": "旧版会话",
	}
	metaJSON, _ := json.Marshal(meta)
	writeSessionFiles(t, cfg, "agent:main:pico:direct:pico:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", string(metaJSON),
		msgLine(t, protocoltypes.Message{Role: "user", Content: "旧会话内容"}),
	)

	h := LoadSessionHistory(cfg, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if !h.Found || h.Title != "旧版会话" || len(h.Items) != 1 {
		t.Fatalf("legacy session not resolved: %+v", h)
	}
}

func TestLoadSessionHistory_MissIsBestEffort(t *testing.T) {
	cfg := historyTestConfig(t)
	h := LoadSessionHistory(cfg, "nonexistent")
	if h.Found || len(h.Items) != 0 {
		t.Fatalf("expected clean miss: %+v", h)
	}
	// Malformed meta files must not break the scan.
	dir := filepath.Join(cfg.Agents.Defaults.Workspace, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := LoadSessionHistory(cfg, "nonexistent"); h.Found {
		t.Fatalf("broken meta must be skipped: %+v", h)
	}
}

func TestHistoryToItems_SkipsToolResultsAndMapsFeedback(t *testing.T) {
	msgs := []protocoltypes.Message{
		{Role: "user", Content: "跑个命令"},
		{Role: "assistant", ToolCalls: []protocoltypes.ToolCall{{
			ID: "c1", Type: "function",
			Function: &protocoltypes.FunctionCall{Name: "exec", Arguments: `{"command":"ls"}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "file.txt"},
		{Role: "assistant", Content: "🔧 `exec` 完成"},
		{Role: "assistant", Content: "结果如下：file.txt"},
		// Transient thought-only assistant message must be dropped.
		{Role: "assistant", ReasoningContent: "transient"},
	}
	items := historyToItems(msgs)
	var kinds []ItemKind
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	want := []ItemKind{ItemUser, ItemToolCalls, ItemToolFeedback, ItemAnswer}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds[%d] = %v, want %v (all: %v)", i, kinds[i], want[i], kinds)
		}
	}
	if items[1].ToolName != "exec" {
		t.Fatalf("tool name wrong: %+v", items[1])
	}
}

func TestState_ApplyHistoryPrependsAndKeepsLiveItems(t *testing.T) {
	s := NewState()
	s.AddUser("实况消息")
	s.ApplyHistory("历史标题", []Item{
		{Kind: ItemUser, Content: "历史一"},
		{Kind: ItemAnswer, Content: "历史回答"},
	})
	if s.Title != "历史标题" {
		t.Fatalf("title = %q", s.Title)
	}
	if len(s.Items) != 3 || s.Items[0].Content != "历史一" || s.Items[2].Content != "实况消息" {
		t.Fatalf("history/live merge wrong: %+v", s.Items)
	}
	// Empty history must not clobber an existing title.
	s.ApplyHistory("", nil)
	if s.Title != "历史标题" {
		t.Fatalf("empty history clobbered title: %q", s.Title)
	}
}

func TestState_AddActionDeduped(t *testing.T) {
	s := NewState()
	s.AddActionDeduped("再按一次 Ctrl+C 退出")
	s.AddActionDeduped("再按一次 Ctrl+C 退出")
	s.AddActionDeduped("再按一次 Ctrl+C 退出")
	if len(s.Items) != 1 {
		t.Fatalf("repeated hints must dedupe: %+v", s.Items)
	}
	s.AddActionDeduped("另一条")
	if len(s.Items) != 2 {
		t.Fatalf("different text must append: %+v", s.Items)
	}
}
