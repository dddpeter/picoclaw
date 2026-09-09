package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func newDegradedTurnExecution(t *testing.T, al *AgentLoop, provider providers.LLMProvider) (*turnState, *turnExecution) {
	t.Helper()
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}
	opts := configuredStreamingProcessOptions("pico")
	normalizeProcessOptionsInPlace(&opts)
	ts := newTurnState(agent, opts, al.newTurnEventScope(agent.ID, opts.SessionKey, nil))
	exec := newTurnExecution(agent, opts, nil, "", nil)
	exec.activeProvider = provider
	exec.activeModel = "test-model"
	exec.activeCandidates = []providers.FallbackCandidate{{Provider: "openai", Model: "openai/test-model"}}
	exec.activeModelConfig = &config.ModelConfig{ModelName: "test-model", Streaming: config.ModelStreamingConfig{Enabled: boolPtr(true)}}
	exec.llmModel = "test-model"
	exec.messages = []providers.Message{{Role: "user", Content: "hello"}}
	exec.callMessages = exec.messages
	exec.providerToolDefs = []providers.ToolDefinition{}
	return ts, exec
}

// TestStreamingDegradedSkipsLaterFirstHops pins the sticky degradation: a
// ChatStream failure before any visible output marks the turn degraded, and
// later iterations must not attempt the streaming first hop again — otherwise
// every iteration opens (and then interrupt-seals) another live card while
// the Chat fallback chain keeps the turn alive.
func TestStreamingDegradedSkipsLaterFirstHops(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	recorder := &recordingStreamer{}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: recorder})

	provider := &configuredStreamingProvider{
		eventPlan: []configuredStreamingEventCall{{err: errors.New("Status: 429")}},
	}
	al := NewAgentLoop(cfg, msgBus, provider)
	ts, exec := newDegradedTurnExecution(t, al, provider)
	pipeline := NewPipeline(al)

	resp, streamed, err := pipeline.tryConfiguredStreamingLLM(context.Background(), ts, exec, nil, nil)
	if streamed || err != nil || resp != nil {
		t.Fatalf("first call: expected handoff to chain (nil,false,nil), got (%v,%v,%v)", resp, streamed, err)
	}
	if !exec.streamingDegraded {
		t.Fatal("expected exec.streamingDegraded after pre-output stream failure")
	}

	_, streamed, err = pipeline.tryConfiguredStreamingLLM(context.Background(), ts, exec, nil, nil)
	if streamed || err != nil {
		t.Fatalf("second call: expected silent skip (false,nil), got streamed=%v err=%v", streamed, err)
	}
	if provider.eventCalls != 1 {
		t.Fatalf("ChatStreamEvents calls = %d, want 1 (sticky skip after the first failure)", provider.eventCalls)
	}
}

// TestStreamingSkippedWhenPrimaryInCooldown pins the first-hop cooldown check:
// when the primary candidate is cooling down (e.g. after a recent 429), the
// streaming attempt is skipped so it does not hammer the rate-limited model —
// the fallback chain rotates straight to an available candidate instead.
func TestStreamingSkippedWhenPrimaryInCooldown(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	recorder := &recordingStreamer{}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: recorder})

	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	cooldown := providers.NewCooldownTracker()
	al.fallback = providers.NewFallbackChain(cooldown, nil)
	ts, exec := newDegradedTurnExecution(t, al, provider)
	pipeline := NewPipeline(al)

	stableKey := exec.activeCandidates[0].StableKey()
	if !al.fallback.Available(stableKey) {
		t.Fatal("precondition: primary must start available")
	}
	cooldown.MarkFailure(stableKey, providers.FailoverRateLimit)
	if al.fallback.Available(stableKey) {
		t.Fatal("precondition: primary must be cooling down after a rate-limit failure")
	}

	resp, streamed, err := pipeline.tryConfiguredStreamingLLM(context.Background(), ts, exec, nil, nil)
	if streamed || err != nil || resp != nil {
		t.Fatalf("expected skip while primary is in cooldown (nil,false,nil), got (%v,%v,%v)", resp, streamed, err)
	}
	if provider.eventCalls != 0 {
		t.Fatalf("ChatStreamEvents calls = %d, want 0 (cooldown must gate the first hop)", provider.eventCalls)
	}
}

// TestPublishTurnErrorNotifiesUser: a turn that dies with an error must tell
// the user instead of leaving only a sealed streaming card — the message
// names the failure so a model outage is distinguishable from a hang.
func TestPublishTurnErrorNotifiesUser(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, false, nil)
	msgBus := bus.NewMessageBus()
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	ts, _ := newDegradedTurnExecution(t, al, provider)

	al.publishTurnError(context.Background(), ts,
		errors.New("LLM call failed after retries: all candidates exhausted"))

	select {
	case msg := <-msgBus.OutboundChan():
		if msg.Channel != "pico" {
			t.Fatalf("outbound channel = %q, want pico", msg.Channel)
		}
		if !strings.Contains(msg.Content, "模型调用失败") {
			t.Fatalf("message must name the failure, got %q", msg.Content)
		}
		if !strings.Contains(msg.Content, "all candidates exhausted") {
			t.Fatalf("message must carry the cause, got %q", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected an outbound turn-error message")
	}
}

// TestPublishTurnErrorSkipsInternalChannels: internal channels (cli, system,
// subagent) must not receive user-facing turn errors.
func TestPublishTurnErrorSkipsInternalChannels(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, false, nil)
	msgBus := bus.NewMessageBus()
	provider := &configuredStreamingProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	ts, _ := newDegradedTurnExecution(t, al, provider)
	ts.channel = "system"

	al.publishTurnError(context.Background(), ts, errors.New("boom"))

	select {
	case msg := <-msgBus.OutboundChan():
		t.Fatalf("internal channel must be skipped, got outbound %q", msg.Content)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestPreOutputFailureFinalizeFailDeliversPlainText: hermes-style last resort
// — when the stream fails before output AND the card cannot be finalized
// either, the chain's answer must still reach the user as a plain message.
func TestPreOutputFailureFinalizeFailDeliversPlainText(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	streamer := &failingFinalizeStreamer{err: errors.New("card api down")}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: streamer})
	provider := &configuredStreamingProvider{
		eventPlan:   []configuredStreamingEventCall{{err: errors.New("Status: 429")}},
		chatResponse: &providers.LLMResponse{Content: "chain answer"},
	}
	al := NewAgentLoop(cfg, msgBus, provider)

	got := runConfiguredStreamingTurn(t, al, "pico")
	if got != "chain answer" {
		t.Fatalf("response = %q, want chain answer", got)
	}

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "chain answer" {
			t.Fatalf("text fallback content = %q, want chain answer", outbound.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("expected plain-text fallback when the card cannot be finalized")
	}
}
