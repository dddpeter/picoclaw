package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func danglingHistory() []providers.Message {
	return []providers.Message{
		{Role: "user", Content: "big task"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID: "call_dangling_1", Type: "function", Name: "exec",
			Arguments: map[string]any{"command": "sleep 600"},
		}}},
	}
}

func TestDetectDanglingToolCalls_FindsMissingResults(t *testing.T) {
	missing := detectDanglingToolCalls(danglingHistory())
	if len(missing) != 1 || missing[0].ID != "call_dangling_1" {
		t.Fatalf("missing = %+v, want call_dangling_1", missing)
	}
	sealed := sealDanglingToolCallsForRestart(danglingHistory())
	if len(sealed) != 3 || sealed[2].Role != "tool" || sealed[2].Content != restartToolResultNote {
		t.Fatalf("sealed = %+v", sealed)
	}
	// Idempotent on already-sealed history.
	if got := detectDanglingToolCalls(sealed); len(got) != 0 {
		t.Fatalf("already-sealed history should have no dangling calls, got %+v", got)
	}
}

// outboundCapture accumulates outbound bus messages for assertions.
type outboundCapture struct {
	mu       sync.Mutex
	messages []bus.OutboundMessage
}

func captureOutbound(msgBus *bus.MessageBus) *outboundCapture {
	oc := &outboundCapture{}
	go func() {
		for msg := range msgBus.OutboundChan() {
			oc.mu.Lock()
			oc.messages = append(oc.messages, msg)
			oc.mu.Unlock()
		}
	}()
	return oc
}

func (oc *outboundCapture) text() string {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	var sb strings.Builder
	for _, m := range oc.messages {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func newRestartRecoveryLoop(t *testing.T, cfgMutate func(*config.Config)) (*AgentLoop, *AgentInstance, *bus.MessageBus, *outboundCapture) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 3,
				RestartRecovery: config.RestartRecoveryConfig{
					NotifyWindowHours:       24,
					ReminderIntervalMinutes: 0, // disable reminder loop for the scan tests
				},
			},
		},
	}
	if cfgMutate != nil {
		cfgMutate(cfg)
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "ok"})
	agent := al.GetRegistry().GetDefaultAgent()
	return al, agent, msgBus, captureOutbound(msgBus)
}

func seedSessionScope(t *testing.T, agent *AgentInstance, key, channel, chatValue string) {
	t.Helper()
	metaStore, ok := agent.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatalf("session store does not support metadata")
	}
	metaStore.EnsureSessionMetadata(key, &session.SessionScope{
		Version: 1, AgentID: "default", Channel: channel,
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": chatValue},
	}, nil)
}

func TestRunRestartRecovery_SealsAndNotifies(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:restart"
	// SetHistory writes through to the jsonl file, giving it a fresh mtime
	// inside the notification window.
	agent.Sessions.SetHistory(key, danglingHistory())
	seedSessionScope(t, agent, key, "test", "direct:chat1")

	al.RunRestartRecovery(context.Background())

	history := agent.Sessions.GetHistory(key)
	sealed := false
	for _, m := range history {
		if m.Role == "tool" && m.Content == restartToolResultNote {
			sealed = true
		}
	}
	if !sealed {
		t.Fatalf("history after recovery has no restart seal: %d msgs", len(history))
	}
	if !strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatalf("expected user notification, got: %q", oc.text())
	}
}

func TestRunRestartRecovery_SkipsCleanSessions(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:clean"
	agent.Sessions.SetHistory(key, []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "done"},
	})
	seedSessionScope(t, agent, key, "test", "direct:chat1")

	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("clean sessions must not trigger notification")
	}
	if h := agent.Sessions.GetHistory(key); len(h) != 2 {
		t.Fatalf("clean session mutated: %d msgs", len(h))
	}
}

func TestRunRestartRecovery_RespectsNotifyWindow(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, func(c *config.Config) {
		c.Agents.Defaults.RestartRecovery.NotifyWindowHours = 1
	})

	key := "agent_default:test:stale"
	agent.Sessions.SetHistory(key, danglingHistory())
	seedSessionScope(t, agent, key, "test", "direct:chat1")

	// Backdate the jsonl file to simulate an interruption outside the
	// notification window. This exercises the real mtime path (os.Stat on
	// the jsonl): the window verdict must be captured BEFORE recovery seals,
	// because SetHistory rewrites the file and refreshes its mtime — a check
	// made after the write would always report the session as active.
	jsonl := filepath.Join(agent.Workspace, "sessions", "agent_default_test_stale.jsonl")
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(jsonl, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("stale session must be sealed silently, without notification")
	}
	if h := agent.Sessions.GetHistory(key); len(h) != 3 {
		t.Fatalf("stale session should still be sealed: %d msgs", len(h))
	}
}

func TestRunRestartRecovery_Disabled(t *testing.T) {
	al, agent, _, oc := newRestartRecoveryLoop(t, func(c *config.Config) {
		f := false
		c.Agents.Defaults.RestartRecovery.Enabled = &f
	})

	key := "agent_default:test:disabled"
	agent.Sessions.SetHistory(key, danglingHistory())
	seedSessionScope(t, agent, key, "test", "direct:chat1")

	al.RunRestartRecovery(context.Background())

	if strings.Contains(oc.text(), "检测到上次任务被中断") {
		t.Fatal("disabled recovery must not notify")
	}
	if h := agent.Sessions.GetHistory(key); len(h) != 2 {
		t.Fatalf("disabled recovery must not seal: %d msgs", len(h))
	}
}

func TestRunRestartRecovery_ReminderCancelledByUserMessage(t *testing.T) {
	al, _, _, _ := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:reminder"
	al.registerRecoveryReminderForTest(key, "test", "chat1", time.Millisecond, 5)
	al.cancelRecoveryReminder(key)
	time.Sleep(30 * time.Millisecond)
	if n := al.recoveryReminderCount(key); n != 0 {
		t.Fatalf("reminder should be cancelled, sent=%d", n)
	}
}

func TestRunRestartRecovery_ReminderStopsAfterMax(t *testing.T) {
	al, _, _, oc := newRestartRecoveryLoop(t, nil)

	key := "agent_default:test:reminder-max"
	al.registerRecoveryReminderForTest(key, "test", "chat1", time.Millisecond, 2)

	ctx, cancel := context.WithCancel(context.Background())
	go al.runRecoveryReminderLoop(ctx, 5*time.Millisecond)
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	for al.recoveryReminderCount(key) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // give the loop room to over-send if buggy
	if n := al.recoveryReminderCount(key); n != 2 {
		t.Fatalf("reminders should cap at 2, sent=%d", n)
	}
	if got := strings.Count(oc.text(), "检测到上次任务被中断"); got != 2 {
		t.Fatalf("expected exactly 2 re-reminder messages, got %d", got)
	}
}

func TestRunRestartRecovery_ScansAllAgents(t *testing.T) {
	wsMain, wsHelper := t.TempDir(), t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: wsMain},
				{ID: "helper", Workspace: wsHelper},
			},
			Defaults: config.AgentDefaults{
				Workspace:         wsMain,
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 3,
				RestartRecovery: config.RestartRecoveryConfig{
					NotifyWindowHours:       24,
					ReminderIntervalMinutes: 0,
				},
			},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "ok"})

	helper, ok := al.GetRegistry().GetAgent("helper")
	if !ok || helper == nil {
		t.Fatal("helper agent not registered")
	}
	key := "agent_helper:test:restart"
	helper.Sessions.SetHistory(key, danglingHistory())

	al.RunRestartRecovery(context.Background())

	sealed := false
	for _, m := range helper.Sessions.GetHistory(key) {
		if m.Role == "tool" && m.Content == restartToolResultNote {
			sealed = true
		}
	}
	if !sealed {
		t.Fatal("non-default agent's interrupted session must also be sealed")
	}
}
