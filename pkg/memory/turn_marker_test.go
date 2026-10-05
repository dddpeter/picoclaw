package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnMarker_WriteListClearRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}

	if markers, err := store.ListTurnMarkers(); err != nil || len(markers) != 0 {
		t.Fatalf("initial list = %v, %v; want empty", markers, err)
	}

	if err := store.WriteTurnMarker("agent_default:test:one", "default", "model-a"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}
	if err := store.WriteTurnMarker("agent_default:test:two", "default", "model-b"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}

	markers, err := store.ListTurnMarkers()
	if err != nil {
		t.Fatalf("ListTurnMarkers: %v", err)
	}
	if len(markers) != 2 {
		t.Fatalf("markers = %d, want 2", len(markers))
	}
	byKey := map[string]TurnMarker{}
	for _, m := range markers {
		byKey[m.SessionKey] = m
	}
	if m := byKey["agent_default:test:one"]; m.AgentID != "default" || m.Model != "model-a" || m.StartedAt.IsZero() {
		t.Fatalf("marker one = %+v", m)
	}

	// Durability across store instances: a restarted process opens a fresh
	// store over the same directory and must see the surviving markers.
	restarted, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore(restart): %v", err)
	}
	if markers, err := restarted.ListTurnMarkers(); err != nil || len(markers) != 2 {
		t.Fatalf("restarted list = %v, %v; want 2 markers", markers, err)
	}

	// Clear is idempotent.
	for i := 0; i < 2; i++ {
		if err := restarted.ClearTurnMarker("agent_default:test:one"); err != nil {
			t.Fatalf("ClearTurnMarker(%d): %v", i, err)
		}
	}
	if markers, err := restarted.ListTurnMarkers(); err != nil || len(markers) != 1 {
		t.Fatalf("after clear list = %v, %v; want 1 marker", markers, err)
	}
}

func TestTurnMarker_OverwriteRewritesMarker(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	if err := store.WriteTurnMarker("k:1", "default", "old"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}
	if err := store.WriteTurnMarker("k:1", "default", "new"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}
	markers, err := store.ListTurnMarkers()
	if err != nil || len(markers) != 1 {
		t.Fatalf("list = %v, %v; want exactly 1 marker", markers, err)
	}
	if markers[0].Model != "new" {
		t.Fatalf("model = %q, want new (latest write wins)", markers[0].Model)
	}
}

func TestTurnMarker_ListRemovesCorruptMarkers(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	if err := store.WriteTurnMarker("k:good", "default", "m"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}
	corruptPath := filepath.Join(dir, sanitizeKey("k:bad")+TurnMarkerSuffix)
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt marker: %v", err)
	}
	emptyKeyPath := filepath.Join(dir, sanitizeKey("k:nokey")+TurnMarkerSuffix)
	if err := os.WriteFile(emptyKeyPath, []byte(`{"model":"m"}`), 0o644); err != nil {
		t.Fatalf("write keyless marker: %v", err)
	}

	markers, err := store.ListTurnMarkers()
	if err != nil {
		t.Fatalf("ListTurnMarkers: %v", err)
	}
	if len(markers) != 1 || markers[0].SessionKey != "k:good" {
		t.Fatalf("markers = %+v, want only k:good", markers)
	}
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Fatal("corrupt marker should be removed by the scan")
	}
	if _, err := os.Stat(emptyKeyPath); !os.IsNotExist(err) {
		t.Fatal("keyless marker should be removed by the scan")
	}
}

func TestTurnMarker_FilenameSanitization(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	key := "agent_default:weird/key\\path"
	if err := store.WriteTurnMarker(key, "default", "m"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.ContainsAny(e.Name(), `:/\`) {
			t.Fatalf("marker filename %q contains path separators", e.Name())
		}
	}
	markers, err := store.ListTurnMarkers()
	if err != nil || len(markers) != 1 || markers[0].SessionKey != key {
		t.Fatalf("markers = %v, %v; want original key preserved", markers, err)
	}
}

// Regression: MigrateFromJSON must not touch marker files — renaming one to
// ".migrated" would destroy the record restart recovery depends on, and
// importing it would create a garbage "<key>.turnmarker" session.
func TestTurnMarker_MigrationSkipsMarkerFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	if err := store.WriteTurnMarker("agent_default:test:mid", "default", "m"); err != nil {
		t.Fatalf("WriteTurnMarker: %v", err)
	}

	migrated, err := MigrateFromJSON(context.Background(), dir, store)
	if err != nil {
		t.Fatalf("MigrateFromJSON: %v", err)
	}
	if migrated != 0 {
		t.Fatalf("migrated = %d, want 0 (marker must not count as a session)", migrated)
	}

	markers, err := store.ListTurnMarkers()
	if err != nil || len(markers) != 1 {
		t.Fatalf("markers after migration = %v, %v; want the marker intact", markers, err)
	}
	if _, err := os.Stat(filepath.Join(dir, sanitizeKey("agent_default:test:mid")+TurnMarkerSuffix+".migrated")); !os.IsNotExist(err) {
		t.Fatal("marker must not be renamed to .migrated")
	}
	for _, key := range store.ListSessions() {
		if strings.HasSuffix(key, ".turnmarker") {
			t.Fatalf("garbage session created from marker: %q", key)
		}
	}
}
