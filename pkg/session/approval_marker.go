package session //nolint:dupl // deliberate parallel structure: approval and ask marker contracts mirror each other by design (fork-overview)

import (
	"log"

	"github.com/sipeed/picoclaw/pkg/memory"
)

// ApprovalMarkerStore persists pending HITL approval questions (fork feature,
// web approval card). The JSONL backend writes them next to the session files
// so a web page reload mid-approval can restore the pending card; the legacy
// SessionManager keeps them in memory only. Best effort like turn markers:
// failures are logged, never returned.
type ApprovalMarkerStore interface {
	// MarkApprovalPending records the question being published.
	MarkApprovalPending(marker memory.ApprovalMarker)
	// ClearApprovalPending removes the marker on every resolution path.
	ClearApprovalPending(sessionKey string)
}

var (
	_ ApprovalMarkerStore = (*JSONLBackend)(nil)
	_ ApprovalMarkerStore = (*SessionManager)(nil)
)

// approvalMarkerStore is the capability JSONLBackend requires from its
// wrapped memory.Store to back ApprovalMarkerStore durably.
type approvalMarkerStore interface {
	WriteApprovalMarker(marker memory.ApprovalMarker) error
	ClearApprovalMarker(sessionKey string) error
}

// MarkApprovalPending implements ApprovalMarkerStore.
func (b *JSONLBackend) MarkApprovalPending(marker memory.ApprovalMarker) {
	markerStore, ok := b.store.(approvalMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.WriteApprovalMarker(marker); err != nil {
		log.Printf("session: write approval marker: %v", err)
	}
}

// ClearApprovalPending implements ApprovalMarkerStore.
func (b *JSONLBackend) ClearApprovalPending(sessionKey string) {
	markerStore, ok := b.store.(approvalMarkerStore)
	if !ok {
		return
	}
	if err := markerStore.ClearApprovalMarker(sessionKey); err != nil {
		log.Printf("session: clear approval marker: %v", err)
	}
}

// MarkApprovalPending implements ApprovalMarkerStore (in-memory fallback).
func (sm *SessionManager) MarkApprovalPending(marker memory.ApprovalMarker) {
	sm.approvalPending.mark(marker.SessionKey, marker)
}

// ClearApprovalPending implements ApprovalMarkerStore (in-memory fallback).
func (sm *SessionManager) ClearApprovalPending(sessionKey string) {
	sm.approvalPending.clear(sessionKey)
}
