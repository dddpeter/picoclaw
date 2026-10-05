package session

import (
	"log"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// InflightTurn describes a turn that was in flight when the process stopped
// (fork feature, pkg/agent/restart_recovery.go).
type InflightTurn struct {
	SessionKey string
	AgentID    string
	Model      string
	StartedAt  time.Time
}

// InflightTurnStore persists per-session turn-in-flight markers used by
// restart recovery. The JSONL backend writes them next to the session files
// (durable across restarts); the legacy SessionManager keeps them in memory
// only, so recovery degrades to dangling-tail detection for that backend.
// All operations are best effort: failures are logged, never returned — a
// missing marker costs one notification, it must never fail a turn.
type InflightTurnStore interface {
	// MarkTurnInFlight records the write-ahead marker for a starting turn.
	MarkTurnInFlight(sessionKey, agentID, model string)
	// ClearTurnInFlight removes the marker on any turn-exit path.
	ClearTurnInFlight(sessionKey string)
	// ListTurnsInFlight returns every surviving marker (restart scan).
	ListTurnsInFlight() []InflightTurn
}

var (
	_ InflightTurnStore = (*JSONLBackend)(nil)
	_ InflightTurnStore = (*SessionManager)(nil)
)

// turnMarkerStore is the capability JSONLBackend requires from its wrapped
// memory.Store to back InflightTurnStore durably.
type turnMarkerStore interface {
	WriteTurnMarker(sessionKey, agentID, model string) error
	ClearTurnMarker(sessionKey string) error
	ListTurnMarkers() ([]memory.TurnMarker, error)
}

// MarkTurnInFlight implements InflightTurnStore.
func (b *JSONLBackend) MarkTurnInFlight(sessionKey, agentID, model string) {
	markerStore, ok := b.store.(turnMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.WriteTurnMarker(sessionKey, agentID, model); err != nil {
		log.Printf("session: write turn marker: %v", err)
	}
}

// ClearTurnInFlight implements InflightTurnStore.
func (b *JSONLBackend) ClearTurnInFlight(sessionKey string) {
	markerStore, ok := b.store.(turnMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.ClearTurnMarker(sessionKey); err != nil {
		log.Printf("session: clear turn marker: %v", err)
	}
}

// ListTurnsInFlight implements InflightTurnStore.
func (b *JSONLBackend) ListTurnsInFlight() []InflightTurn {
	markerStore, ok := b.store.(turnMarkerStore)
	if !ok {
		return nil
	}
	markers, err := markerStore.ListTurnMarkers()
	if err != nil {
		log.Printf("session: list turn markers: %v", err)
		return nil
	}
	turns := make([]InflightTurn, 0, len(markers))
	for _, m := range markers {
		turns = append(turns, InflightTurn{
			SessionKey: m.SessionKey,
			AgentID:    m.AgentID,
			Model:      m.Model,
			StartedAt:  m.StartedAt,
		})
	}
	return turns
}

// SessionManager (legacy fallback backend) tracks in-flight turns in memory
// only: after a restart the map is empty and recovery falls back to
// dangling-tail detection — the pre-marker behavior.
type inflightTurnMem struct {
	mu    sync.Mutex
	turns map[string]InflightTurn
}

// MarkTurnInFlight implements InflightTurnStore.
func (sm *SessionManager) MarkTurnInFlight(sessionKey, agentID, model string) {
	sm.inflight.mu.Lock()
	defer sm.inflight.mu.Unlock()
	sm.inflight.turns[sessionKey] = InflightTurn{
		SessionKey: sessionKey,
		AgentID:    agentID,
		Model:      model,
		StartedAt:  time.Now(),
	}
}

// ClearTurnInFlight implements InflightTurnStore.
func (sm *SessionManager) ClearTurnInFlight(sessionKey string) {
	sm.inflight.mu.Lock()
	defer sm.inflight.mu.Unlock()
	delete(sm.inflight.turns, sessionKey)
}

// ListTurnsInFlight implements InflightTurnStore.
func (sm *SessionManager) ListTurnsInFlight() []InflightTurn {
	sm.inflight.mu.Lock()
	defer sm.inflight.mu.Unlock()
	turns := make([]InflightTurn, 0, len(sm.inflight.turns))
	for _, t := range sm.inflight.turns {
		turns = append(turns, t)
	}
	return turns
}
