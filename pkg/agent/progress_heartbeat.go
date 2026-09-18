// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

// Progress heartbeat (fork feature, docs/design/long-task-execution.zh.md §4):
// while a turn shows no observable activity (no LLM chunk, no tool completion,
// no iteration advance) for longer than the configured interval, publish a
// KindText panel step summarizing progress. The step lands in the streaming
// card's 「💬 本轮说明」 archive — zero feishu-side changes — and doubles as a
// keep-alive write against the card's server-side streaming timeout (200850),
// which the existing reopen/degrade path already covers when it fails.

// startProgressHeartbeat launches the heartbeat goroutine for a turn. It
// polls at interval/4 (min 100ms), publishes when the idle threshold is
// crossed, and exits when stop is called or turnCtx ends. Returns a no-op
// when the heartbeat is disabled (interval <= 0).
func (al *AgentLoop) startProgressHeartbeat(turnCtx context.Context, ts *turnState) (stop func()) {
	interval := ts.heartbeatInterval
	if interval <= 0 && al != nil && al.GetConfig() != nil {
		interval = time.Duration(al.GetConfig().Agents.Defaults.GetProgressHeartbeatSeconds()) * time.Second
	}
	if interval <= 0 {
		return func() {}
	}
	tick := interval / 4
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-turnCtx.Done():
				return
			case <-done:
				return
			case now := <-ticker.C:
				publisher := ts.loadStreamPublisher()
				if publisher == nil {
					continue
				}
				if ts.activityIdleFor(now) < interval {
					continue
				}
				publisher.AppendToolStep(turnCtx, bus.ToolStep{
					Kind: bus.ToolStepKindText,
					Result: fmt.Sprintf("⏱ 进度：任务仍在进行——第 %d 轮迭代，已运行 %s",
						ts.currentIteration(), time.Since(ts.startedAt).Round(time.Second)),
				})
			}
		}
	}()
	return func() { close(done) }
}
