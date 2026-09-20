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
//
// The goroutine also hosts the stall watchdog (fork feature): when the turn
// shows no activity for longer than the configured stall threshold it (1)
// requests a graceful interrupt and cancels the in-flight provider call so
// the turn can finalize at the next iteration boundary; if the turn is still
// silent one heartbeat interval later it (2) hard-aborts, which cancels
// turnCtx and unblocks tools that ignore cancellation. Delivery has two
// faces: streaming sessions get a KindText panel step; non-streaming
// sessions with a deliverable channel get a plain outbound message tagged
// progress_note.
func (al *AgentLoop) startProgressHeartbeat(turnCtx context.Context, ts *turnState) (stop func()) {
	interval := ts.heartbeatInterval
	if interval <= 0 && al != nil && al.GetConfig() != nil {
		interval = time.Duration(al.GetConfig().Agents.Defaults.GetProgressHeartbeatSeconds()) * time.Second
	}
	if interval <= 0 {
		return func() {}
	}
	stallAfter := ts.stallInterruptAfter
	if stallAfter <= 0 && al != nil && al.GetConfig() != nil {
		stallAfter = time.Duration(al.GetConfig().Agents.Defaults.GetProgressStallInterruptSeconds()) * time.Second
	}
	// A stall threshold shorter than two heartbeat intervals would fire
	// before the turn even had a chance to beat once — clamp it.
	if stallAfter > 0 && stallAfter < 2*interval {
		stallAfter = 2 * interval
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
				idle := ts.activityIdleFor(now)

				// Stall watchdog first — it must not be throttled by the
				// heartbeat beat pacing, and it applies regardless of
				// whether the turn has a deliverable surface.
				if stallAfter > 0 {
					if at := ts.stallInterruptedAt(); at.IsZero() && idle >= stallAfter {
						hint := fmt.Sprintf("stall watchdog: no observable activity for %s", idle.Round(time.Second))
						if ts.requestGracefulInterrupt(hint) && ts.markStallInterrupted(now) {
							ts.cancelProviderCall()
							ts.markHeartbeat()
							logger.WarnCF("agent", "stall watchdog: requesting graceful interrupt", map[string]any{
								"turn_id": ts.turnID, "idle": idle.Round(time.Second).String(),
							})
							publisher := ts.loadStreamPublisher()
							al.publishProgressBeat(turnCtx, ts, publisher, fmt.Sprintf(
								"⛔ 本轮已 %s 无任何进展，正在请求模型收尾总结（第 %d 轮迭代，已运行 %s）",
								idle.Round(time.Second), ts.currentIteration(),
								time.Since(ts.startedAt).Round(time.Second)))
						}
						continue
					} else if !at.IsZero() && now.Sub(at) >= interval && idle >= stallAfter {
						// The graceful interrupt did not unblock the turn
						// (e.g. a tool stuck in an uninterruptible call) —
						// escalate to a hard abort. The cause is recorded
						// in the same critical section as the abort flag:
						// turnCancel wakes the turn loop, which reads
						// abortReasonCode() immediately (turn_coord's
						// stream-cleanup defer), so setting it afterwards
						// would lose that race.
						if ts.requestHardAbortWithReason("stall_watchdog") {
							logger.WarnCF("agent", "stall watchdog: escalating to hard abort", map[string]any{
								"turn_id": ts.turnID, "idle": idle.Round(time.Second).String(),
							})
							// turnCancel fires inside requestHardAbort, so
							// the farewell notice needs a detached context
							// (mirrors turn_coord's cleanup pattern).
							noticeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							publisher := ts.loadStreamPublisher()
							al.publishProgressBeat(noticeCtx, ts, publisher,
								"⛔ 长时间无进展且无法收尾，本轮已强制中止；可重新发送消息继续。")
							cancel()
						}
						continue
					}
				}

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
				if idle >= 2*interval {
					text = fmt.Sprintf("⚠️ 进度：已 %s 无新进展（第 %d 轮迭代，已运行 %s），持续无响应时将自动收尾本轮",
						idle.Round(time.Second), ts.currentIteration(),
						time.Since(ts.startedAt).Round(time.Second))
				}
				al.publishProgressBeat(turnCtx, ts, publisher, text)
			}
		}
	}()
	return func() { close(done) }
}

// publishProgressBeat delivers one progress beat via the streaming panel
// step, falling back to a plain outbound message tagged progress_note.
func (al *AgentLoop) publishProgressBeat(ctx context.Context, ts *turnState, publisher *streamingChunkPublisher, text string) {
	if publisher != nil {
		publisher.AppendToolStep(ctx, bus.ToolStep{
			Kind:   bus.ToolStepKindText,
			Result: text,
		})
		return
	}
	if pubErr := al.bus.PublishOutbound(ctx, outboundMessageForTurnWithOptions(
		ts, text, outboundTurnMessageOptions{kind: messageKindProgressNote},
	)); pubErr != nil {
		logger.WarnCF("agent", "progress heartbeat: outbound fallback failed",
			map[string]any{"turn_id": ts.turnID, "error": pubErr.Error()})
	}
}
