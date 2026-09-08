package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// gatedStreamProvider blocks inside ChatStream until released, letting the
// test observe turn state while the LLM call is in flight (the card shows
// "正在思考" at that moment).
type gatedStreamProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *gatedStreamProvider) Chat(_ context.Context, _ []providers.Message,
	_ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func (p *gatedStreamProvider) GetDefaultModel() string { return "gated-model" }

func (p *gatedStreamProvider) ChatStream(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	_ func(string),
) (*providers.LLMResponse, error) {
	close(p.started)
	select {
	case <-p.release:
		return nil, errors.New("stopped mid-call")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestTryConfiguredStreamingOwnsPublisherDuringLLMCall pins the /stop fix:
// the streaming publisher must be owned for the whole duration of the LLM
// call, so an abort mid-call can still reach and seal the live card. Before
// the fix the publisher was cleared at the start of each attempt and only
// re-assigned after a successful response — an abort during the call left
// the card in streaming mode ("正在思考") forever.
func TestTryConfiguredStreamingOwnsPublisherDuringLLMCall(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	msgBus := bus.NewMessageBus()
	recorder := &recordingStreamer{}
	msgBus.SetStreamDelegate(configuredStreamingDelegate{streamer: recorder})

	provider := &gatedStreamProvider{started: make(chan struct{}), release: make(chan struct{})}
	al := NewAgentLoop(cfg, msgBus, provider)
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	opts := configuredStreamingProcessOptions("pico")
	opts.SendResponse = true
	normalizeProcessOptionsInPlace(&opts)
	ts := newTurnState(agent, opts, al.newTurnEventScope(agent.ID, opts.SessionKey, nil))
	exec := newTurnExecution(agent, opts, nil, "", nil)
	exec.activeProvider = provider
	exec.activeModel = "test-model"
	exec.activeCandidates = []providers.FallbackCandidate{{Provider: "test", Model: "test-model"}}
	exec.activeModelConfig = &config.ModelConfig{ModelName: "test-model", Streaming: config.ModelStreamingConfig{Enabled: boolPtr(true)}}
	exec.llmModel = "test-model"

	pipeline := NewPipeline(al)

	type result struct {
		streamed bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		_, streamed, err := pipeline.tryConfiguredStreamingLLM(context.Background(), ts, exec, nil, nil)
		done <- result{streamed: streamed, err: err}
	}()

	<-provider.started
	// The invariant: while the LLM call is in flight, the publisher is owned
	// and an abort path can reach the live card through it.
	if exec.streamingPublisher == nil {
		close(provider.release)
		t.Fatal("streaming publisher was orphaned during the LLM call; " +
			"an aborted turn could no longer seal the live card")
	}

	// Simulate /stop: cancel the turn context mid-call, then let the call
	// return.
	cancelConfiguredStreamingLLMWithReason(context.Background(), exec, streamCancelReasonStopCommand)
	if recorder.canceled == 0 {
		t.Error("expected the abort cancellation to reach the streamer")
	}

	close(provider.release)
	res := <-done
	// The ownership invariant above is confirmed by the failure shape too: no
	// visible output was published, so the stream failure must hand the turn
	// back to the caller's Chat fallback chain (streamed=false, err=nil),
	// never surface as a turn error.
	if res.streamed || res.err != nil {
		t.Fatalf("expected stream failure before visible output to hand off to the fallback chain (streamed=false, err=nil), got streamed=%v err=%v", res.streamed, res.err)
	}
}
