// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// newSeahorseMultiAgentLoop builds an AgentLoop with a default "main" agent
// and a routed "research" agent, both on the seahorse context manager, each
// with its own workspace (and therefore its own JSONL session store).
func newSeahorseMultiAgentLoop(t *testing.T) (*AgentLoop, *seahorseContextManager) {
	t.Helper()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			List: []config.AgentConfig{
				{ID: "main", Default: true},
				{ID: "research", Workspace: t.TempDir()},
			},
			Defaults: config.AgentDefaults{
				Workspace:      t.TempDir(),
				ModelName:      "test-model",
				MaxTokens:      4096,
				ContextManager: "seahorse",
			},
		},
	}

	al := NewAgentLoop(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "ok"})
	cm, ok := al.contextManager.(*seahorseContextManager)
	if !ok {
		t.Fatalf("expected seahorseContextManager, got %T", al.contextManager)
	}
	// Close the engine before t.TempDir cleanup, or Windows fails to remove
	// seahorse.db while the sqlite handle is still open (the same failure that
	// puts the four legacy TestSeahorse* tests on the Windows baseline).
	t.Cleanup(func() { _ = cm.engine.Close() })
	return al, cm
}

// writeJSONLHistory persists messages into an agent's JSONL store, simulating
// a pre-existing session from before a seahorse migration (or a DB rebuild).
func writeJSONLHistory(t *testing.T, ag *AgentInstance, sessionKey string, msgs []providers.Message) {
	t.Helper()
	if ag == nil || ag.Sessions == nil {
		t.Fatal("agent has no session store")
	}
	ag.Sessions.SetHistory(sessionKey, msgs)
	if err := ag.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("save JSONL history: %v", err)
	}
}

func dbMessageCount(t *testing.T, cm *seahorseContextManager, sessionKey string) (int, bool) {
	t.Helper()
	store := cm.engine.GetRetrieval().Store()
	ctx := context.Background()
	conv, err := store.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		t.Fatalf("GetConversationBySessionKey: %v", err)
	}
	if conv == nil {
		return 0, false
	}
	msgs, err := store.GetMessages(ctx, conv.ConversationID, 1000, 0)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	return len(msgs), true
}

func historyContent(resp *AssembleResponse) string {
	var b strings.Builder
	for _, m := range resp.History {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// A session owned by a routed (non-default) agent must get its JSONL history
// into the assembled context even though the startup bootstrap only walks the
// default agent's store. This is the silent-context-loss regression test.
func TestSeahorseAssembleBootstrapsRoutedAgentHistory(t *testing.T) {
	al, cm := newSeahorseMultiAgentLoop(t)
	ctx := context.Background()

	routed, ok := al.registry.GetAgent("research")
	if !ok || routed == nil {
		t.Fatal("expected routed 'research' agent")
	}
	sessionKey := "agent:research:s1"
	writeJSONLHistory(t, routed, sessionKey, []providers.Message{
		{Role: "user", Content: "routed q1"},
		{Role: "assistant", Content: "routed a1"},
	})

	resp, err := cm.Assemble(ctx, &AssembleRequest{SessionKey: sessionKey, Budget: 100000, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(historyContent(resp), "routed q1") {
		t.Fatalf("routed agent's JSONL history missing from assembled context: %q", historyContent(resp))
	}

	n, exists := dbMessageCount(t, cm, sessionKey)
	if !exists || n != 2 {
		t.Fatalf("expected 2 bootstrapped messages in DB, got exists=%v n=%d", exists, n)
	}
}

// When the DB already holds part of the history (e.g. the session ran under
// seahorse for a while, then the DB was rebuilt from an older snapshot), the
// lazy bootstrap must reconcile via longest-prefix matching: no duplicates,
// missing tail appended.
func TestSeahorseAssembleReconcilesPartialDBWithoutDuplicates(t *testing.T) {
	al, cm := newSeahorseMultiAgentLoop(t)
	ctx := context.Background()

	routed, _ := al.registry.GetAgent("research")
	sessionKey := "agent:research:s2"

	// Two messages already ingested into the DB during normal operation.
	for _, m := range []providers.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
	} {
		if err := cm.Ingest(ctx, &IngestRequest{SessionKey: sessionKey, Message: m}); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	// JSONL holds the full history (the two above plus a newer turn).
	writeJSONLHistory(t, routed, sessionKey, []providers.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	})

	resp, err := cm.Assemble(ctx, &AssembleRequest{SessionKey: sessionKey, Budget: 100000, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	content := historyContent(resp)
	for _, want := range []string{"q1", "a1", "q2", "a2"} {
		if !strings.Contains(content, want) {
			t.Fatalf("assembled context missing %q: %q", want, content)
		}
	}

	n, _ := dbMessageCount(t, cm, sessionKey)
	if n != 4 {
		t.Fatalf("expected 4 messages in DB after reconcile, got %d (duplicate ingest?)", n)
	}
}

// The first Assemble creates an empty conversation shell
// (GetOrCreateConversation). A later bootstrap decision must therefore look
// at the message count, not at conversation existence — an empty shell with
// JSONL history must still be bootstrapped.
func TestSeahorseAssembleBootstrapsEmptyShellConversation(t *testing.T) {
	al, cm := newSeahorseMultiAgentLoop(t)
	ctx := context.Background()

	routed, _ := al.registry.GetAgent("research")
	sessionKey := "agent:research:s3"

	// Create an empty conversation shell directly in the store, bypassing
	// the manager (simulates a failed/interrupted earlier bootstrap attempt).
	store := cm.engine.GetRetrieval().Store()
	if _, err := store.GetOrCreateConversation(ctx, sessionKey); err != nil {
		t.Fatalf("GetOrCreateConversation: %v", err)
	}

	writeJSONLHistory(t, routed, sessionKey, []providers.Message{
		{Role: "user", Content: "shell q1"},
		{Role: "assistant", Content: "shell a1"},
	})

	resp, err := cm.Assemble(ctx, &AssembleRequest{SessionKey: sessionKey, Budget: 100000, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(historyContent(resp), "shell q1") {
		t.Fatalf("empty-shell conversation with JSONL history was not bootstrapped: %q", historyContent(resp))
	}
}

// Sessions the engine refuses to persist (hardcoded "heartbeat" ignore
// pattern) must not be bootstrapped: no conversation row, no messages, and
// Assemble returns an empty context instead of panicking on the engine's
// nil, nil result.
func TestSeahorseAssembleSkipsIgnoredHeartbeatSession(t *testing.T) {
	al, cm := newSeahorseMultiAgentLoop(t)
	ctx := context.Background()

	// Heartbeat turns run with NoHistory, so the JSONL store of the default
	// agent is the only place such a session could accumulate history.
	def := al.registry.GetDefaultAgent()
	writeJSONLHistory(t, def, "heartbeat", []providers.Message{
		{Role: "user", Content: "hb ping"},
	})

	resp, err := cm.Assemble(ctx, &AssembleRequest{SessionKey: "heartbeat", Budget: 100000, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if resp == nil {
		t.Fatal("Assemble must return a response for ignored sessions, not nil")
	}
	if len(resp.History) != 0 {
		t.Fatalf("ignored session must assemble empty history, got %d messages", len(resp.History))
	}

	if n, exists := dbMessageCount(t, cm, "heartbeat"); exists {
		t.Fatalf("ignored session must not be bootstrapped into DB, found %d messages", n)
	}
}

// The default agent's sessions keep working exactly as before: startup
// bootstrap from its own store, no reliance on the lazy path.
func TestSeahorseAssembleDefaultAgentHistoryUnchanged(t *testing.T) {
	al, cm := newSeahorseMultiAgentLoop(t)
	ctx := context.Background()

	def := al.registry.GetDefaultAgent()
	sessionKey := "default-session"
	writeJSONLHistory(t, def, sessionKey, []providers.Message{
		{Role: "user", Content: "default q1"},
		{Role: "assistant", Content: "default a1"},
	})

	resp, err := cm.Assemble(ctx, &AssembleRequest{SessionKey: sessionKey, Budget: 100000, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if !strings.Contains(historyContent(resp), "default q1") {
		t.Fatalf("default agent history missing from assembled context: %q", historyContent(resp))
	}
}
