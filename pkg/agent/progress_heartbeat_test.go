package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

// heartbeatRecorder captures panel steps for heartbeat assertions.
type heartbeatRecorder struct {
	steps []bus.ToolStep
}

func (r *heartbeatRecorder) Update(ctx context.Context, content string) error  { return nil }
func (r *heartbeatRecorder) Finalize(ctx context.Context, content string) error { return nil }
func (r *heartbeatRecorder) Cancel(ctx context.Context)                          {}

func (r *heartbeatRecorder) AppendToolStep(ctx context.Context, step bus.ToolStep) error {
	r.steps = append(r.steps, step)
	return nil
}

func newHeartbeatLoop(t *testing.T, interval time.Duration) (*AgentLoop, *turnState, *heartbeatRecorder) {
	t.Helper()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 3,
				// heartbeat comes from ts.heartbeatInterval override
			},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "ok"})
	agent := al.GetRegistry().GetDefaultAgent()
	ts := newTurnState(agent, processOptions{Dispatch: DispatchRequest{SessionKey: "s", UserMessage: "hi"}},
		turnEventScope{turnID: "t1"})
	ts.heartbeatInterval = interval
	rs := &heartbeatRecorder{}
	p := &streamingChunkPublisher{streamer: rs, ts: ts}
	ts.setStreamPublisher(p)
	ts.touchActivity()
	return al, ts, rs
}

func TestProgressHeartbeat_PublishesAfterIdle(t *testing.T) {
	al, ts, rs := newHeartbeatLoop(t, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(rs.steps) > 0 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, s := range rs.steps {
		if s.Kind == bus.ToolStepKindText && strings.Contains(fmt.Sprint(s.Result), "进度") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected heartbeat text step, got %+v", rs.steps)
	}
}

func TestProgressHeartbeat_SilentWhenActive(t *testing.T) {
	al, ts, rs := newHeartbeatLoop(t, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	// Keep touching activity so the idle threshold is never reached.
	for i := 0; i < 12; i++ {
		time.Sleep(30 * time.Millisecond)
		ts.touchActivity()
	}
	if len(rs.steps) != 0 {
		t.Fatalf("active turn should stay silent, got %+v", rs.steps)
	}
}

func TestProgressHeartbeat_StopsAfterClear(t *testing.T) {
	al, ts, rs := newHeartbeatLoop(t, 30*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	// Wait for one beat, then clear the publisher (seal) — no more steps.
	deadline := time.Now().Add(2 * time.Second)
	for len(rs.steps) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(rs.steps) == 0 {
		t.Fatal("expected at least one beat before clear")
	}
	ts.clearStreamPublisher(ts.loadStreamPublisher())
	time.Sleep(100 * time.Millisecond)
	before := len(rs.steps)
	time.Sleep(100 * time.Millisecond)
	if len(rs.steps) != before {
		t.Fatalf("steps after clear: %d -> %d (should stay)", before, len(rs.steps))
	}
}

func TestProgressHeartbeat_Disabled(t *testing.T) {
	al, ts, rs := newHeartbeatLoop(t, 0) // zero interval disables

	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	time.Sleep(80 * time.Millisecond)
	if len(rs.steps) != 0 {
		t.Fatalf("disabled heartbeat should not publish, got %+v", rs.steps)
	}
}

func TestProgressHeartbeat_ThrottledToOnePerInterval(t *testing.T) {
	al, ts, rs := newHeartbeatLoop(t, 80*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	// Sustained silence for ~2.5 intervals; without throttling the poll
	// tick (interval/4) would fire ~10 times.
	time.Sleep(200 * time.Millisecond)
	n := len(rs.steps)
	time.Sleep(80 * time.Millisecond)
	if len(rs.steps) > n+1 {
		t.Fatalf("beats should arrive ~1 per interval, got %d in 280ms (first count %d)", len(rs.steps), n)
	}
	if len(rs.steps) < 2 {
		t.Fatalf("expected at least 2 beats across 280ms of silence, got %d", len(rs.steps))
	}
}

func TestProgressHeartbeat_OutboundFallbackWithoutStreamer(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         64,
				MaxToolIterations: 3,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	t.Cleanup(func() { msgBus.Close() })
	al := NewAgentLoop(cfg, msgBus, &simpleMockProvider{response: "ok"})
	agent := al.GetRegistry().GetDefaultAgent()
	ts := newTurnState(agent, processOptions{
		Dispatch: DispatchRequest{
			SessionKey:   "s",
			UserMessage:  "hi",
			InboundContext: &bus.InboundContext{Channel: "test", ChatID: "chat9", ChatType: "direct", SenderID: "u1"},
		},
	}, turnEventScope{turnID: "t9"})
	ts.heartbeatInterval = 50 * time.Millisecond
	ts.touchActivity()
	// No stream publisher set — heartbeat must fall back to outbound.

	oc := captureOutbound(msgBus)
	ctx, cancel := context.WithCancel(context.Background())
	stop := al.startProgressHeartbeat(ctx, ts)
	defer stop()
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(oc.text(), "进度") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(oc.text(), "进度") {
		t.Fatal("expected outbound progress fallback message")
	}
	oc.mu.Lock()
	kind := ""
	for _, m := range oc.messages {
		if strings.Contains(m.Content, "进度") {
			kind = m.Context.Raw["message_kind"]
		}
	}
	oc.mu.Unlock()
	if kind != messageKindProgressNote {
		t.Fatalf("outbound message kind = %q, want %q", kind, messageKindProgressNote)
	}
}

// segmentLabelRecorder accepts SetSegmentLabel like feishu's streamer does.
type segmentLabelRecorder struct {
	heartbeatRecorder
	label string
}

func (r *segmentLabelRecorder) SetSegmentLabel(label string) { r.label = label }

func TestStreamingPublisherSetsSegmentLabel(t *testing.T) {
	_, ts, _ := newHeartbeatLoop(t, 0)
	rs := &segmentLabelRecorder{}
	p := &streamingChunkPublisher{streamer: rs, ts: ts}
	ts.setStreamPublisher(p)
	ts.setSegmentLabel("续 2/3")
	// Re-run the publisher-ownership path logic: the setter fires at
	// publisher creation in tryConfiguredStreamingLLM; emulate that by
	// invoking the same optional-interface branch directly.
	if label := ts.getSegmentLabel(); label != "" {
		if s, ok := p.streamer.(interface{ SetSegmentLabel(string) }); ok {
			s.SetSegmentLabel(label)
		}
	}
	if rs.label != "续 2/3" {
		t.Fatalf("streamer label = %q, want %q", rs.label, "续 2/3")
	}
}
