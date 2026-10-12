package session

import (
	"log"
	"sync"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// AskMarkerStore persists pending ask_user question sets (fork feature,
// web ask card — octop borrowing §三). Mirrors ApprovalMarkerStore: the
// JSONL backend writes them next to the session files so a page reload
// mid-ask can restore the card; the legacy SessionManager keeps them in
// memory only. Best effort: failures are logged, never returned.
type AskMarkerStore interface {
	// MarkAskPending records the question set being published.
	MarkAskPending(marker memory.AskMarker)
	// ClearAskPending removes the marker on every exit path.
	ClearAskPending(sessionKey string)
}

var (
	_ AskMarkerStore = (*JSONLBackend)(nil)
	_ AskMarkerStore = (*SessionManager)(nil)
)

// askMarkerStore is the capability JSONLBackend requires from its wrapped
// memory.Store to back AskMarkerStore durably.
type askMarkerStore interface {
	WriteAskMarker(marker memory.AskMarker) error
	ClearAskMarker(sessionKey string) error
}

// MarkAskPending implements AskMarkerStore.
func (b *JSONLBackend) MarkAskPending(marker memory.AskMarker) {
	markerStore, ok := b.store.(askMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.WriteAskMarker(marker); err != nil {
		log.Printf("session: write ask marker: %v", err)
	}
}

// ClearAskPending implements AskMarkerStore.
func (b *JSONLBackend) ClearAskPending(sessionKey string) {
	markerStore, ok := b.store.(askMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.ClearAskMarker(sessionKey); err != nil {
		log.Printf("session: clear ask marker: %v", err)
	}
}

// askPendingMem backs the legacy SessionManager's in-memory fallback.
type askPendingMem struct {
	mu      sync.Mutex
	pending map[string]memory.AskMarker
}

// MarkAskPending implements AskMarkerStore.
func (sm *SessionManager) MarkAskPending(marker memory.AskMarker) {
	sm.askPending.mu.Lock()
	defer sm.askPending.mu.Unlock()
	sm.askPending.pending[marker.SessionKey] = marker
}

// ClearAskPending implements AskMarkerStore.
func (sm *SessionManager) ClearAskPending(sessionKey string) {
	sm.askPending.mu.Lock()
	defer sm.askPending.mu.Unlock()
	delete(sm.askPending.pending, sessionKey)
}
