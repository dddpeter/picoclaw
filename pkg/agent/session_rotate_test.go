package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// sessionTitleReader reads session titles in rotation tests via the same
// optional capability the title pipeline uses.
type sessionTitleReader interface {
	GetSessionTitle(sessionKey string) (title, source string, ok bool)
}

func TestProcessMessage_NewArchivesPreviousConversation(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := newResetTestConfig(tmpDir, "local")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	firstResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "help me plan a trip to Japan",
	})
	if !strings.Contains(firstResp, "LLM reply") {
		t.Fatalf("unexpected chat reply: %q", firstResp)
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.Sessions == nil {
		t.Fatal("agent/session store not initialized")
	}
	titleReader, ok := agent.Sessions.(sessionTitleReader)
	if !ok {
		t.Fatalf("sessions store does not expose titles: %T", agent.Sessions)
	}

	// Identify the live session (the only one carrying history so far).
	var liveKey string
	var liveHistory = map[string]int{}
	for _, key := range agent.Sessions.ListSessions() {
		liveHistory[key] = len(agent.Sessions.GetHistory(key))
		if liveHistory[key] > 0 && liveKey == "" {
			liveKey = key
		}
	}
	if liveKey == "" {
		t.Fatalf("no session with history found among %v", liveHistory)
	}
	liveTitle, liveSource, ok := titleReader.GetSessionTitle(liveKey)
	if !ok || strings.TrimSpace(liveTitle) == "" {
		t.Fatalf("live session should carry a derived title before /new (title=%q source=%q ok=%v)", liveTitle, liveSource, ok)
	}

	newResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/new",
	})
	if !strings.Contains(newResp, "New conversation started") {
		t.Fatalf("unexpected /new reply: %q", newResp)
	}
	if !strings.Contains(newResp, "Previous chat archived") {
		t.Fatalf("/new reply = %q, want archive confirmation", newResp)
	}

	// The live session must be empty; a new archive session must exist with
	// the previous conversation and the migrated title.
	after := agent.Sessions.ListSessions()
	var archiveKeys []string
	for _, key := range after {
		if _, existed := liveHistory[key]; !existed {
			archiveKeys = append(archiveKeys, key)
		}
	}
	if len(archiveKeys) != 1 {
		t.Fatalf("expected exactly one archive session, keys before=%v after=%v", liveHistory, after)
	}
	archiveKey := archiveKeys[0]
	archivedMsgs := agent.Sessions.GetHistory(archiveKey)
	if len(archivedMsgs) != liveHistory[liveKey] {
		t.Fatalf("archived history len = %d, want %d", len(archivedMsgs), liveHistory[liveKey])
	}
	if got := agent.Sessions.GetHistory(liveKey); len(got) != 0 {
		t.Fatalf("live session should be empty after /new, got %d messages", len(got))
	}
	if got := strings.TrimSpace(agent.Sessions.GetSummary(archiveKey)); got != "" {
		t.Fatalf("archive summary should be empty (none set), got %q", got)
	}

	archiveTitle, archiveSource, ok := titleReader.GetSessionTitle(archiveKey)
	if !ok || archiveTitle != liveTitle || archiveSource != liveSource {
		t.Fatalf("archive title = %q/%q (ok=%v), want migrated %q/%q",
			archiveTitle, archiveSource, ok, liveTitle, liveSource)
	}

	if liveTitleAfter, _, liveOk := titleReader.GetSessionTitle(liveKey); liveOk {
		t.Fatalf("live title should be cleared after /new, got %q", liveTitleAfter)
	}

	// The first chat after /new must re-title the live session from its own
	// first message; the archive keeps the migrated title untouched.
	secondResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "周末去哪里骑行比较好",
	})
	if !strings.Contains(secondResp, "LLM reply") {
		t.Fatalf("unexpected second chat reply: %q", secondResp)
	}
	retitled, retitledSource, retitledOk := titleReader.GetSessionTitle(liveKey)
	if !retitledOk || retitledSource != "derived" ||
		!strings.HasPrefix(retitled, "周末去哪里骑行比较好") {
		t.Fatalf("live title after first post-/new chat = %q/%q (ok=%v), want re-derived from the new first message",
			retitled, retitledSource, retitledOk)
	}
	archiveTitleAfter, archiveSourceAfter, archiveOk := titleReader.GetSessionTitle(archiveKey)
	if !archiveOk || archiveTitleAfter != liveTitle {
		t.Fatalf("archive title changed after new chat: %q/%q (ok=%v), want %q",
			archiveTitleAfter, archiveSourceAfter, archiveOk, liveTitle)
	}
}

// /new on an empty session has nothing to archive and must not create an
// archive session (but must still succeed).
func TestProcessMessage_NewOnEmptySessionSkipsArchive(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := newResetTestConfig(tmpDir, "local")
	msgBus := bus.NewMessageBus()
	provider := &countingMockProvider{response: "LLM reply"}
	al := NewAgentLoop(cfg, msgBus, provider)
	helper := testHelper{al: al}

	newResp := helper.executeAndGetResponse(t, context.Background(), bus.InboundMessage{
		Channel:  "telegram",
		SenderID: "user1",
		ChatID:   "chat1",
		Content:  "/new",
	})
	if !strings.Contains(newResp, "New conversation started") {
		t.Fatalf("unexpected /new reply: %q", newResp)
	}
	if strings.Contains(newResp, "archived") {
		t.Fatalf("/new reply = %q, empty session must not claim an archive", newResp)
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.Sessions == nil {
		t.Fatal("agent/session store not initialized")
	}
	for _, key := range agent.Sessions.ListSessions() {
		if strings.Contains(strings.ToLower(key), "archive") {
			t.Fatalf("no archive session expected, found key %q", key)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("LLM should not be called for /new, calls=%d", provider.calls)
	}
}
