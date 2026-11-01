package session

import (
	"log"
	"sync"
	"time"
)

// IdleCompactMarkerStore persists idle-compaction success bookkeeping (fork
// feature, docs/design/idle-compaction-design.zh.md §3.6). The scanner's
// in-memory records die with the process; the marker in SessionMeta is what
// stops a gateway restart from re-compacting every idle session whose water
// level never falls (seahorse compaction does not shrink the JSONL history).
// Mirrors AskMarkerStore: JSONLBackend persists via its wrapped memory store,
// the legacy SessionManager keeps an in-memory fallback. Best effort:
// failures are logged, never returned.
type IdleCompactMarkerStore interface {
	// MarkIdleCompacted records a successful idle compaction of sessionKey,
	// covering the UpdatedAt snapshot seenUpdated (no new activity below
	// that snapshot ⇒ do not re-compact).
	MarkIdleCompacted(sessionKey string, seenUpdated time.Time)
	// IdleCompactedMarker returns the persisted marker, if any.
	IdleCompactedMarker(sessionKey string) (compactedAt, seenUpdated time.Time, ok bool)
}

var (
	_ IdleCompactMarkerStore = (*JSONLBackend)(nil)
	_ IdleCompactMarkerStore = (*SessionManager)(nil)
)

// idleCompactMarkerStore is the capability JSONLBackend requires from its
// wrapped memory.Store to persist the marker next to the session files.
type idleCompactMarkerStore interface {
	WriteIdleCompactMarker(sessionKey string, seenUpdated time.Time) error
	ReadIdleCompactMarker(sessionKey string) (compactedAt, seenUpdated time.Time, ok bool)
}

// MarkIdleCompacted implements IdleCompactMarkerStore.
func (b *JSONLBackend) MarkIdleCompacted(sessionKey string, seenUpdated time.Time) {
	ms, ok := b.store.(idleCompactMarkerStore)
	if !ok {
		return
	}
	if err := ms.WriteIdleCompactMarker(sessionKey, seenUpdated); err != nil {
		log.Printf("session: write idle-compact marker: %v", err)
	}
}

// IdleCompactedMarker implements IdleCompactMarkerStore.
func (b *JSONLBackend) IdleCompactedMarker(sessionKey string) (time.Time, time.Time, bool) {
	ms, ok := b.store.(idleCompactMarkerStore)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return ms.ReadIdleCompactMarker(sessionKey)
}

// idleCompactMem backs the legacy SessionManager's in-memory fallback (its
// sessions are not persisted either, so the fallback never outlives the
// sessions it describes).
type idleCompactMem struct {
	mu     sync.Mutex
	marked map[string]idleCompactMark
}

type idleCompactMark struct {
	at   time.Time
	seen time.Time
}

// MarkIdleCompacted implements IdleCompactMarkerStore.
func (sm *SessionManager) MarkIdleCompacted(sessionKey string, seenUpdated time.Time) {
	sm.idleCompact.mu.Lock()
	defer sm.idleCompact.mu.Unlock()
	if sm.idleCompact.marked == nil {
		sm.idleCompact.marked = make(map[string]idleCompactMark)
	}
	sm.idleCompact.marked[sessionKey] = idleCompactMark{at: time.Now(), seen: seenUpdated}
}

// IdleCompactedMarker implements IdleCompactMarkerStore.
func (sm *SessionManager) IdleCompactedMarker(sessionKey string) (time.Time, time.Time, bool) {
	sm.idleCompact.mu.Lock()
	defer sm.idleCompact.mu.Unlock()
	m, ok := sm.idleCompact.marked[sessionKey]
	return m.at, m.seen, ok
}
