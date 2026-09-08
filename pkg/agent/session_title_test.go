package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

func newTitleTestAgent(t *testing.T) (*AgentLoop, *AgentInstance, session.SessionStore) {
	t.Helper()
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := session.NewJSONLBackend(store)
	al := &AgentLoop{}
	agent := &AgentInstance{
		Sessions: backend,
		Provider: &mockProvider{},
		Model:    "mock-model",
	}
	return al, agent, backend
}

func titleOpts(sessionKey, senderID, userMsg string) *processOptions {
	return &processOptions{
		SenderID: senderID,
		UserMessage: userMsg,
		Dispatch: DispatchRequest{
			SessionKey:  sessionKey,
			UserMessage: userMsg,
		},
	}
}

func waitForTitleSource(t *testing.T, store session.SessionStore, key, wantSource string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ts, ok := store.(titleCapableStore); ok {
			if title, source, has := ts.GetSessionTitle(key); has && source == wantSource {
				return title, source
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("title source %q not reached in time", wantSource)
	return "", ""
}

func TestDeriveSessionTitle(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"plain":           {"帮我看下网关日志", "帮我看下网关日志"},
		"collapse lines":  {"第一行\n第二行\t第三行", "第一行 第二行 第三行"},
		"strip quotes":    {"\"部署脚本\"", "部署脚本"},
		"strip zero宽":    {"​标题​", "标题"},
		"trim space":      {"   spaces   ", "spaces"},
		"empty":           {"   ", ""},
		"only quotes":     {`"  "`, ""},
	}
	for name, tc := range cases {
		if got := deriveSessionTitle(tc.in); got != tc.want {
			t.Errorf("%s: deriveSessionTitle(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("长", 100)
	got := deriveSessionTitle(long)
	if runes := []rune(got); len(runes) != derivedTitleMaxRunes+1 { // cap + ellipsis
		t.Errorf("truncation: got %d runes, want %d", len(runes), derivedTitleMaxRunes+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated title should end with ellipsis: %q", got)
	}
}

func TestMachineTitleDetection(t *testing.T) {
	for _, c := range []string{"[System note: model switched]", "[cron] daily report", "[image: photo]", ""} {
		if !isMachineTitleOpening(c) {
			t.Errorf("isMachineTitleOpening(%q) = false, want true", c)
		}
	}
	if isMachineTitleOpening("正常的用户消息") {
		t.Error("normal message must not be machine-flagged")
	}
	for _, s := range []string{"heartbeat", "heartbeat:tick", "async:exec", "cron:daily", "system"} {
		if !machineTitleSender(s) {
			t.Errorf("machineTitleSender(%q) = false, want true", s)
		}
	}
	if machineTitleSender("ou_user123") {
		t.Error("human sender must not be machine-flagged")
	}
}

func TestSanitizeLLMTitle(t *testing.T) {
	if got := sanitizeLLMTitle("  「部署巡检」  "); got != "部署巡检" {
		t.Errorf("quote strip: got %q", got)
	}
	if got := sanitizeLLMTitle("标题。"); got != "标题" {
		t.Errorf("trailing period: got %q", got)
	}
	// Answer-shaped guard: oversized or multi-line output is rejected, not truncated.
	if got := sanitizeLLMTitle(strings.Repeat("字", llmTitleMaxRunes+1)); got != "" {
		t.Errorf("oversized answer must be rejected, got %q", got)
	}
	if got := sanitizeLLMTitle("这是对您问题的回答\n第二行"); got != "" {
		t.Errorf("multi-line answer must be rejected, got %q", got)
	}
}

func TestMaybeTitleSessionDerivedThenLLM(t *testing.T) {
	// The upgrade tracker is per-process and single-shot per session; reset
	// it so repeated runs (-count=N) exercise the full path again.
	titleUpgrades.mu.Lock()
	titleUpgrades.inFlight = make(map[string]struct{})
	titleUpgrades.tried = make(map[string]struct{})
	titleUpgrades.mu.Unlock()

	al, agent, store := newTitleTestAgent(t)
	const key = "agent:main:feishu:direct:oc_title1"

	al.maybeTitleSession(agent, titleOpts(key, "ou_peter", "帮我排查网关为什么挂死"))

	// Phase 1 writes synchronously, but the phase-2 goroutine may already
	// have upgraded the title by the time we look — accept either state and
	// then wait for the upgrade to land.
	ts := store.(titleCapableStore)
	title, source, ok := ts.GetSessionTitle(key)
	if !ok {
		t.Fatal("no title written after maybeTitleSession")
	}
	switch source {
	case titleSourceDerived:
		if title != "帮我排查网关为什么挂死" {
			t.Fatalf("derived title = %q", title)
		}
	case titleSourceLLM:
		// upgrade already landed
	default:
		t.Fatalf("unexpected title source %q", source)
	}
	// Phase 2: mockProvider returns "Mock response" — an acceptable title.
	if finalTitle, _ := waitForTitleSource(t, store, key, titleSourceLLM); finalTitle != "Mock response" {
		t.Fatalf("llm title = %q, want Mock response", finalTitle)
	}
}

func TestMaybeTitleSessionNeverOverwritesUserTitle(t *testing.T) {
	al, agent, store := newTitleTestAgent(t)
	const key = "agent:main:feishu:direct:oc_title2"
	ts := store.(titleCapableStore)
	if !ts.SetSessionTitle(key, "手动命名", titleSourceUser) {
		t.Fatal("seed user title failed")
	}

	al.maybeTitleSession(agent, titleOpts(key, "ou_peter", "新消息内容"))

	title, source, ok := ts.GetSessionTitle(key)
	if !ok || source != titleSourceUser || title != "手动命名" {
		t.Fatalf("user title clobbered: %q (%q)", title, source)
	}
}

func TestMaybeTitleSessionSkipsMachineTraffic(t *testing.T) {
	al, agent, store := newTitleTestAgent(t)
	ts := store.(titleCapableStore)

	heartbeatKey := "agent:main:feishu:direct:oc_hb"
	al.maybeTitleSession(agent, titleOpts(heartbeatKey, "heartbeat", "今日心跳巡检"))
	if _, _, ok := ts.GetSessionTitle(heartbeatKey); ok {
		t.Fatal("heartbeat sender must not title the session")
	}

	systemKey := "agent:main:feishu:direct:oc_sys"
	al.maybeTitleSession(agent, titleOpts(systemKey, "ou_peter", "[System note: compaction]"))
	if _, _, ok := ts.GetSessionTitle(systemKey); ok {
		t.Fatal("machine opening must not title the session")
	}
}

func TestMaybeTitleSessionConfigDisabled(t *testing.T) {
	al, agent, store := newTitleTestAgent(t)
	disabled := false
	al.cfg = &config.Config{}
	al.cfg.Agents.Defaults.SessionTitles.Enabled = &disabled
	const key = "agent:main:feishu:direct:oc_off"

	al.maybeTitleSession(agent, titleOpts(key, "ou_peter", "正常消息"))

	if _, _, ok := store.(titleCapableStore).GetSessionTitle(key); ok {
		t.Fatal("disabled config must suppress titling")
	}
}

func TestSessionTitlesEnabledDefault(t *testing.T) {
	var d config.AgentDefaults
	if !d.SessionTitlesEnabled() {
		t.Fatal("unset session_titles must default to enabled")
	}
	off := false
	d.SessionTitles.Enabled = &off
	if d.SessionTitlesEnabled() {
		t.Fatal("explicit disable must be honored")
	}
}
