// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Progress heartbeat (fork feature, docs/design/long-task-execution.zh.md §4
// and §8.2): while a turn shows no observable activity (no LLM chunk, no
// tool completion, no iteration advance) for longer than the configured
// interval, emit a progress beat. Beats are throttled to one per interval
// during sustained silence (not one per poll tick). Delivery has two faces:
// streaming sessions get a KindText panel step (「💬 本轮说明」 archive,
// doubling as a keep-alive write against Feishu's 200850 streaming timeout);
// non-streaming sessions with a deliverable channel get a plain outbound
// message tagged progress_note.

// startProgressHeartbeat launches the heartbeat goroutine for a turn. It
// polls at interval/4 (min 100ms), emits when due, and exits when stop is
// called or turnCtx ends. Returns a no-op when the heartbeat is disabled
// (interval <= 0).
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
				if publisher == nil && (ts.channel == "" || constants.IsInternalChannel(ts.channel)) {
					// No streaming surface and no deliverable channel —
					// nothing to do.
					continue
				}
				if !ts.heartbeatDue(now, interval) {
					continue
				}
				ts.markHeartbeat()
				text := fmt.Sprintf("⏱ 进度：任务仍在进行——第 %d 轮迭代，已运行 %s",
					ts.currentIteration(), time.Since(ts.startedAt).Round(time.Second))
				if publisher != nil {
					publisher.AppendToolStep(turnCtx, bus.ToolStep{
						Kind:   bus.ToolStepKindText,
						Result: text,
					})
					continue
				}
				// Non-streaming fallback: deliver the beat as a plain
				// outbound message tagged progress_note (channels that don't
				// recognize the kind render it as ordinary text).
				if pubErr := al.bus.PublishOutbound(turnCtx, outboundMessageForTurnWithOptions(
					ts, text, outboundTurnMessageOptions{kind: messageKindProgressNote},
				)); pubErr != nil {
					logger.WarnCF("agent", "progress heartbeat: outbound fallback failed",
						map[string]any{"turn_id": ts.turnID, "error": pubErr.Error()})
				}
			}
		}
	}()
	return func() { close(done) }
}
