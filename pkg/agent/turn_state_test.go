package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestMatchingTurnMessageTail_IgnoresInternalRuntimeFields(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "question"},
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: &providers.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"/tmp/test"}`,
					},
				},
			},
		},
	}

	persisted := []providers.Message{
		userPromptMessage("question", nil),
		{
			Role: "assistant",
			ToolCalls: []providers.ToolCall{
				{
					ID:               "call_1",
					Type:             "function",
					Name:             "read_file",
					Arguments:        map[string]any{"path": "/tmp/test"},
					ThoughtSignature: "internal-signature",
					Function: &providers.FunctionCall{
						Name:             "read_file",
						Arguments:        `{"path":"/tmp/test"}`,
						ThoughtSignature: "internal-signature",
					},
				},
			},
		},
	}

	if got := matchingTurnMessageTail(history, persisted); got != 2 {
		t.Fatalf("matchingTurnMessageTail() = %d, want 2", got)
	}
}

func TestSplitHistoryForActiveTurn_ProtectsPersistedTail(t *testing.T) {
	history := []providers.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "current question"},
		{Role: "tool", Content: "tool output", ToolCallID: "call_1"},
	}

	persisted := []providers.Message{
		userPromptMessage("current question", nil),
		{Role: "tool", Content: "tool output", ToolCallID: "call_1"},
	}

	stable, protected := splitHistoryForActiveTurn(history, persisted)
	if len(stable) != 2 {
		t.Fatalf("stable history len = %d, want 2", len(stable))
	}
	if len(protected) != 2 {
		t.Fatalf("protected tail len = %d, want 2", len(protected))
	}
	if protected[0].Content != "current question" {
		t.Fatalf("protected[0].Content = %q, want current question", protected[0].Content)
	}
}

func TestTrimHistoryToFitContextWindow_WithProtectedTurnTailKeepsActiveTurn(t *testing.T) {
	current := strings.Repeat("current turn ", 80)
	history := []providers.Message{
		{Role: "user", Content: strings.Repeat("old turn ", 60)},
		{Role: "assistant", Content: strings.Repeat("old reply ", 60)},
		{Role: "user", Content: current},
	}

	stable, protected := splitHistoryForActiveTurn(history, []providers.Message{
		userPromptMessage(current, nil),
	})
	trimmedStable, messages, fit := trimHistoryToFitContextWindow(
		stable,
		func(trimmedHistory []providers.Message) []providers.Message {
			return append(append([]providers.Message(nil), trimmedHistory...), protected...)
		},
		120,
		nil,
		0,
	)

	if fit {
		t.Fatal("expected protected active turn alone to remain over budget")
	}
	if len(trimmedStable) != 0 {
		t.Fatalf("trimmed stable history len = %d, want 0", len(trimmedStable))
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1 protected active-turn message", len(messages))
	}
	if messages[0].Content != current {
		t.Fatalf("messages[0].Content = %q, want protected current turn", messages[0].Content)
	}
}

func TestTurnState_ActivityTracking(t *testing.T) {
	agent := &AgentInstance{ID: "default"}
	ts := newTurnState(agent, processOptions{Dispatch: DispatchRequest{SessionKey: "s", UserMessage: "hi"}},
		turnEventScope{turnID: "t1"})
	if idle := ts.activityIdleFor(time.Now()); idle < 0 {
		t.Fatalf("idle duration must be non-negative, got %v", idle)
	}
	time.Sleep(5 * time.Millisecond)
	ts.touchActivity()
	if idle := ts.activityIdleFor(time.Now()); idle > 5*time.Millisecond {
		t.Fatalf("touchActivity not reflected, idle=%v", idle)
	}
}

func TestTurnState_StreamPublisherAtomicRef(t *testing.T) {
	agent := &AgentInstance{ID: "default"}
	ts := newTurnState(agent, processOptions{Dispatch: DispatchRequest{SessionKey: "s", UserMessage: "hi"}},
		turnEventScope{turnID: "t1"})
	if ts.loadStreamPublisher() != nil {
		t.Fatal("publisher should start nil")
	}
	p := &streamingChunkPublisher{}
	ts.setStreamPublisher(p)
	if ts.loadStreamPublisher() != p {
		t.Fatal("setStreamPublisher not visible")
	}
	// Clearing with the wrong pointer must not drop the live publisher.
	ts.clearStreamPublisher(&streamingChunkPublisher{})
	if ts.loadStreamPublisher() != p {
		t.Fatal("CAS clear with stale pointer must not clear the live publisher")
	}
	ts.clearStreamPublisher(p)
	if ts.loadStreamPublisher() != nil {
		t.Fatal("clearStreamPublisher failed")
	}
}
