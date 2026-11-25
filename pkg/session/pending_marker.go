package session

import (
	"sync"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// pendingMarkerStore is the shared in-memory fallback used by the legacy
// SessionManager for both approval and ask markers. After a restart the map
// is empty, matching the legacy backend's degraded turn-marker behavior.
type pendingMarkerStore[T any] struct {
	mu      sync.Mutex
	pending map[string]T
}

// newPendingMarkerStore returns an initialized empty store.
func newPendingMarkerStore[T any]() pendingMarkerStore[T] {
	return pendingMarkerStore[T]{pending: make(map[string]T)}
}

// mark records the marker for a session.
func (m *pendingMarkerStore[T]) mark(sessionKey string, marker T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[sessionKey] = marker
}

// clear removes the marker for a session.
func (m *pendingMarkerStore[T]) clear(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, sessionKey)
}

// approvalPendingMem backs ApprovalMarkerStore on the legacy SessionManager.
type approvalPendingMem = pendingMarkerStore[memory.ApprovalMarker]

// askPendingMem backs AskMarkerStore on the legacy SessionManager.
type askPendingMem = pendingMarkerStore[memory.AskMarker]
