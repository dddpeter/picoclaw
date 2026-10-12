// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT

package agent

import (
	"testing"
)

// TestSessionTurnActive pins the lock-free turn probe (octop borrowing §四):
// true when a live turn (placeholder or real) matches the channel+chat pair,
// false otherwise. The pico WS connection handler uses it to answer
// reconnecting clients whose turn survived a page refresh.
func TestSessionTurnActive(t *testing.T) {
	al, _, cleanup := newTurnCoordTestLoop(t, &summarizeStubProvider{})
	defer cleanup()

	if al.SessionTurnActive("pico", "pico:s1") {
		t.Fatal("no turns registered: probe must report false")
	}

	// A live turn claimed for the chat pair (placeholder shape, as the pump
	// registers before the worker swaps in the real state).
	al.activeTurnStates.Store("agent_default:turn-active:s1", &turnState{
		turnID:     makePendingTurnID("agent_default:turn-active:s1", 1),
		sessionKey: "agent_default:turn-active:s1",
		channel:    "pico",
		chatID:     "pico:s1",
		phase:      TurnPhaseSetup,
	})

	if !al.SessionTurnActive("pico", "pico:s1") {
		t.Fatal("live turn for the exact chat pair must report true")
	}
	if al.SessionTurnActive("pico", "pico:s2") {
		t.Fatal("a different chat must not report the turn as active")
	}
	if al.SessionTurnActive("feishu", "pico:s1") {
		t.Fatal("a different channel must not report the turn as active")
	}
}
