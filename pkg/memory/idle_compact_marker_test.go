package memory

import (
	"context"
	"testing"
	"time"
)

// TestIdleCompactMarkerRoundtrip pins the persisted idle-compaction marker:
// write → reopen the store (simulated restart) → read back; and the write
// must NOT advance meta.UpdatedAt (the marker is bookkeeping, not activity —
// the session list orders by UpdatedAt and the scanner's idle gate must not
// be reset by it either).
func TestIdleCompactMarkerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	if err := store.AddMessage(context.Background(), "s1", "user", "hello"); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	metaBefore, err := store.GetSessionMeta(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetSessionMeta: %v", err)
	}

	seen := time.Now().Add(-time.Hour)
	if err := store.WriteIdleCompactMarker("s1", seen); err != nil {
		t.Fatalf("WriteIdleCompactMarker: %v", err)
	}

	// Reopen = restart: the marker must come back from disk.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	at, gotSeen, ok := reopened.ReadIdleCompactMarker("s1")
	if !ok {
		t.Fatal("marker lost across reopen")
	}
	if !gotSeen.Equal(seen) {
		t.Fatalf("seen = %v, want %v", gotSeen, seen)
	}
	if at.IsZero() || time.Since(at) > time.Minute {
		t.Fatalf("compactedAt = %v, want ~now", at)
	}

	// The marker write must not have touched UpdatedAt.
	metaAfter, err := reopened.GetSessionMeta(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetSessionMeta after: %v", err)
	}
	if !metaAfter.UpdatedAt.Equal(metaBefore.UpdatedAt) {
		t.Fatalf("marker write advanced UpdatedAt: %v -> %v", metaBefore.UpdatedAt, metaAfter.UpdatedAt)
	}

	// Unmarked session reports no marker.
	if _, _, ok := reopened.ReadIdleCompactMarker("never-compacted"); ok {
		t.Fatal("marker reported for unmarked session")
	}
}
