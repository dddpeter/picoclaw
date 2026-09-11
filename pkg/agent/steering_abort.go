package agent

import (
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// hardAbortUnwindGrace bounds how long HardAbort waits for the aborted
// turn's goroutine to unwind on its own before force-releasing its
// activeTurnStates registration.
var hardAbortUnwindGrace = 10 * time.Second

// abortedToolResultNote is the synthetic tool-result content sealing dangling
// tool calls of a hard-aborted turn, keeping the assistant/tool pairing
// valid for the next LLM request.
const abortedToolResultNote = "[tool execution aborted: the task was stopped before this call completed]"

// sealDanglingToolCalls returns history with synthetic tool results appended
// for trailing assistant tool calls that never received a result — the shape
// a hard abort leaves behind when it interrupts a turn mid-tool-loop.
func sealDanglingToolCalls(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	// Walk back over trailing tool results to find the final assistant
	// message with tool calls; earlier history predates the aborted turn's
	// last exchange and is assumed complete.
	satisfied := make(map[string]bool)
	i := len(history) - 1
	for ; i >= 0 && history[i].Role == "tool"; i-- {
		satisfied[history[i].ToolCallID] = true
	}
	if i < 0 || history[i].Role != "assistant" || len(history[i].ToolCalls) == 0 {
		return history
	}

	var missing []providers.ToolCall
	for _, call := range history[i].ToolCalls {
		if !satisfied[call.ID] {
			missing = append(missing, call)
		}
	}
	if len(missing) == 0 {
		return history
	}

	sealed := make([]providers.Message, 0, len(history)+len(missing))
	sealed = append(sealed, history...)
	for _, call := range missing {
		sealed = append(sealed, providers.Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    abortedToolResultNote,
		})
	}
	return sealed
}

// watchHardAbortUnwind gives the aborted turn's goroutine a grace period to
// unwind and clear its own registration. A turn wedged inside a tool call
// (e.g. blocked on I/O that ignores context cancellation) never returns, so
// its deferred clearActiveTurn never runs: without this watchdog the session
// stays claimed forever and only a gateway restart recovers.
func (al *AgentLoop) watchHardAbortUnwind(sessionKey string, ts *turnState) {
	deadline := time.Now().Add(hardAbortUnwindGrace)
	for time.Now().Before(deadline) {
		if current := al.getActiveTurnState(sessionKey); current != ts {
			// The goroutine unwound, or a new turn already claimed the
			// session — release nothing.
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if al.getActiveTurnState(sessionKey) == ts {
		al.releaseSessionTurnState(sessionKey, ts)
		logger.ErrorCF("agent", "hard abort watchdog: turn goroutine failed to unwind; force-released session registration",
			map[string]any{
				"session_key": sessionKey,
				"turn_id":     ts.snapshot().TurnID,
				"grace":       hardAbortUnwindGrace.String(),
			})
	}
}
