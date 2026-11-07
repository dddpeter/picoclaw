package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

// writeSessionMeta creates a sessions/<sanitized>.meta.json + jsonl pair.
// The store sanitizes ':' in keys for filenames while meta.Key keeps the raw
// key — the helper mirrors that (Windows filenames reject ':').
func writeSessionMeta(t *testing.T, dir, key string, meta memory.SessionMeta, jsonl string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sanitized := sanitizeSessionKey(key)
	meta.Key = key
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = time.Now()
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sanitized+".meta.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if jsonl != "" {
		if err := os.WriteFile(filepath.Join(dir, sanitized+".jsonl"), []byte(jsonl), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListSessions_ChannelsAndOrder(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = filepath.Dir(dir)
	sessionsDir := filepath.Join(filepath.Dir(dir), "sessions")

	picoScope, _ := json.Marshal(map[string]any{
		"channel": "pico",
		"values":  map[string]string{"sender": "pico:uuid-1111"},
	})
	writeSessionMeta(t, sessionsDir, "agent:main:pico:direct:pico:uuid-1111", memory.SessionMeta{
		Scope:     picoScope,
		Title:     "调试 429",
		UpdatedAt: time.Now().Add(-time.Hour),
	}, "")

	feishuScope, _ := json.Marshal(map[string]any{"channel": "feishu"})
	writeSessionMeta(t, sessionsDir, "agent:main:feishu:x", memory.SessionMeta{
		Scope:     feishuScope,
		Title:     "飞书会话",
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}, "")

	writeSessionMeta(t, sessionsDir, "agent:main:telegram:y", memory.SessionMeta{
		Title:     "旧格式（无 scope）",
		UpdatedAt: time.Now().Add(-3 * time.Hour),
	}, "")

	got := ListSessions(cfg)
	if len(got) != 3 {
		t.Fatalf("want 3 sessions, got %d: %+v", len(got), got)
	}
	if got[0].Channel != "pico" || got[0].PicoID != "uuid-1111" || got[0].Title != "调试 429" {
		t.Fatalf("newest should be the pico session: %+v", got[0])
	}
	if got[1].Channel != "feishu" || got[1].PicoID != "" {
		t.Fatalf("scope channel expected: %+v", got[1])
	}
	if got[2].Channel != "telegram" {
		t.Fatalf("key-derived channel expected: %+v", got[2])
	}
}

func TestListSessions_MissingDir(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "nope")
	if got := ListSessions(cfg); got != nil {
		t.Fatalf("missing dir should yield nil, got %+v", got)
	}
}

func TestOverlaySessions_FilterAndPick(t *testing.T) {
	var o overlayState
	o.open(overlaySessions)
	o.entries = []SessionEntry{
		{Key: "a", Channel: "pico", PicoID: "id-1", Title: "调试会话"},
		{Key: "b", Channel: "feishu", Title: "周报"},
		{Key: "c", Channel: "pico", PicoID: "id-2", Title: "ctx 分析"},
	}
	o.current = "id-2"
	o.refilter()
	if o.rows() != 3 {
		t.Fatalf("all should pass empty query: %d", o.rows())
	}

	o.insertQuery("调试")
	if o.rows() != 1 {
		t.Fatalf("fuzzy filter failed: %d rows", o.rows())
	}
	entry, _, ok := o.pick()
	if !ok || entry.PicoID != "id-1" {
		t.Fatalf("pick failed: %+v %v", entry, ok)
	}

	o.backspace() // "调"
	if o.rows() != 1 {
		t.Fatalf("backspace should refilter: %d", o.rows())
	}
}

func TestOverlayMoveWrapsAndScrolls(t *testing.T) {
	var o overlayState
	o.open(overlayModel)
	o.models = []string{"m1", "m2", "m3"}
	o.refilter()
	if !o.move(1, 2) || o.selected != 1 {
		t.Fatalf("move +1: sel=%d", o.selected)
	}
	if !o.move(1, 2) || o.selected != 2 || o.offset != 1 {
		t.Fatalf("move +1 should scroll: sel=%d offset=%d", o.selected, o.offset)
	}
	if !o.move(1, 2) || o.selected != 0 || o.offset != 0 {
		t.Fatalf("move should wrap: sel=%d offset=%d", o.selected, o.offset)
	}
}

func TestLoadSessionHistoryByKey(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = filepath.Dir(dir)
	sessionsDir := filepath.Join(filepath.Dir(dir), "sessions")

	jsonl := `{"role":"user","content":"你好"}
{"role":"assistant","content":"收到","model_name":"glm-4.7"}
`
	writeSessionMeta(t, sessionsDir, "agent:main:feishu:x", memory.SessionMeta{
		Title: "飞书历史", Count: 2,
	}, jsonl)

	h := LoadSessionHistoryByKey(cfg, "agent:main:feishu:x")
	if !h.Found || h.Title != "飞书历史" {
		t.Fatalf("history by key failed: %+v", h)
	}
	if len(h.Items) != 2 || h.Items[0].Kind != ItemUser || h.Items[1].Kind != ItemAnswer {
		t.Fatalf("items wrong: %+v", h.Items)
	}
	if LoadSessionHistoryByKey(cfg, "missing_key").Found {
		t.Fatal("missing key should not be found")
	}
}

func TestConfigModels(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.Defaults.ModelName = "glm-4.7"
	cfg.Agents.Defaults.ModelFallbacks = []string{"kimi-k2", "glm-4.7"}
	cfg.Agents.List = []config.AgentConfig{
		{ID: "coder", Model: &config.AgentModelConfig{Primary: "claude-x", Fallbacks: []string{"glm-4.7"}}},
	}
	got := configModels(cfg)
	want := []string{"glm-4.7", "kimi-k2", "claude-x"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}
