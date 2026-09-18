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
