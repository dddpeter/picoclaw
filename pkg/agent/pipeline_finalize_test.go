package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// TestFinalize_SealsStreamingCardWhenToolHandledDelivery covers the
// send_file-style path: a tool delivers the response itself
// (allResponsesHandled=true) while the answer text was streamed from the same
// LLM response. Finalize must seal the streaming card as completed — before
// this fix the card was left live and the coordinator's last-resort cleanup
// mislabeled it as "回合中止".
func TestFinalize_SealsStreamingCardWhenToolHandledDelivery(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	ts := newTurnState(agent, processOptions{SessionKey: "session-handled"},
		al.newTurnEventScope(agent.ID, "session-handled", nil))

	recorder := &toolStepRecordingStreamer{}
	exec := &turnExecution{
		allResponsesHandled: true,
		response:            &providers.LLMResponse{Content: "streamed answer"},
		streamingPublisher:  &streamingChunkPublisher{streamer: recorder, ts: ts},
		llmModelName:        "test-model",
	}

	pipeline := NewPipeline(al)
	result, err := pipeline.Finalize(
		context.Background(),
		context.Background(),
		ts,
		exec,
		TurnEndStatusCompleted,
		"",
	)
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Fatalf("expected completed status, got %q", result.status)
	}

	if len(recorder.finalized) != 1 {
		t.Fatalf("expected exactly one stream Finalize call, got %d", len(recorder.finalized))
	}
	if recorder.finalized[0] != "streamed answer" {
		t.Fatalf("expected card sealed with the streamed content, got %q", recorder.finalized[0])
	}
	// The publisher must be consumed so the coordinator's last-resort
	// cleanup becomes a no-op instead of cancelling the sealed card.
	if exec.streamingPublisher != nil {
		t.Fatal("expected streamingPublisher to be cleared after finalize")
	}
}

// TestFinalize_ForcesSealWithoutStreamedContent covers image-generation
// turns: the tool delivers the output and the model never streams any text.
// The card must still be force-sealed — the empty-content guard in the
// publisher's Finalize would otherwise leave it in streaming mode, showing
// its loading status ("正在思考") forever.
func TestFinalize_ForcesSealWithoutStreamedContent(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	ts := newTurnState(agent, processOptions{SessionKey: "session-nocontent"},
		al.newTurnEventScope(agent.ID, "session-nocontent", nil))

	recorder := &toolStepRecordingStreamer{}
	exec := &turnExecution{
		allResponsesHandled: true,
		// Tool-call-only response: no text content ever streamed.
		response:           &providers.LLMResponse{Content: ""},
		streamingPublisher: &streamingChunkPublisher{streamer: recorder, ts: ts},
		llmModelName:       "test-model",
	}

	pipeline := NewPipeline(al)
	result, err := pipeline.Finalize(
		context.Background(),
		context.Background(),
		ts,
		exec,
		TurnEndStatusCompleted,
		"",
	)
	if err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}
	if result.status != TurnEndStatusCompleted {
		t.Fatalf("expected completed status, got %q", result.status)
	}

	if len(recorder.finalized) != 1 {
		t.Fatalf("expected the card to be force-sealed, got %d finalize calls", len(recorder.finalized))
	}
	if exec.streamingPublisher != nil {
		t.Fatal("expected streamingPublisher to be cleared after finalize")
	}
}
