// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

// SessionTurnActive reports whether a live turn (placeholder or running)
// is bound to the channel+chat pair — the reconnect signal behind the pico
// WS turn.status frame (octop borrowing §四: a page refresh must not lose
// the typing state when the turn itself survived on the gateway).
//
// The Range over the active-turn map is lock-free; each entry's snapshot
// takes that turn's own mu.RLock (short critical sections — cancels happen
// outside the lock — so this never blocks meaningfully and never touches
// the model-state mutex). Placeholders count as active (the session is
// claimed and about to run, and carry channel/chatID from the claiming
// message). The turn count is bounded by concurrent sessions, so the scan
// is cheap.
func (al *AgentLoop) SessionTurnActive(channel, chatID string) bool {
	if al == nil || channel == "" || chatID == "" {
		return false
	}
	found := false
	al.activeTurnStates.Range(func(_, value any) bool {
		ts, ok := value.(*turnState)
		if !ok || ts == nil {
			return true
		}
		snap := ts.snapshot()
		if snap.Channel == channel && snap.ChatID == chatID {
			found = true
			return false
		}
		return true
	})
	return found
}
