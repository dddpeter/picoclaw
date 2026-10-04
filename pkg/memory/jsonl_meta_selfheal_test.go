package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// F4 regression: a corrupt .meta.json must degrade to the zero value (the
// not-yet-titled state) instead of bricking the whole session — no history,
// no appends — until someone manually deleted the file. The next meta write
// overwrites the corrupt bytes (self-healing).
func TestCorruptMetaSelfHeals(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	const key = "agent_test/session"

	metaPath := filepath.Join(dir, sanitizeKey(key)+".meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"title": "half-written`), 0o600); err != nil {
		t.Fatalf("seed corrupt meta: %v", err)
	}

	meta, err := store.GetSessionMeta(t.Context(), key)
	if err != nil {
		t.Fatalf("corrupt meta must not fail the read: %v", err)
	}
	if meta.Key != key {
		t.Errorf("degraded meta Key = %q, want %q", meta.Key, key)
	}
	if meta.Title != "" {
		t.Errorf("degraded meta must be the zero value, got Title %q", meta.Title)
	}

	// Self-heal: the next write replaces the corrupt bytes wholesale.
	healed := SessionMeta{Key: key, Title: "fixed", UpdatedAt: time.Now()}
	if err := store.writeMeta(key, healed); err != nil {
		t.Fatalf("writeMeta over corrupt file: %v", err)
	}
	after, err := store.GetSessionMeta(t.Context(), key)
	if err != nil {
		t.Fatalf("read after heal: %v", err)
	}
	if after.Title != "fixed" {
		t.Fatalf("meta did not self-heal: %+v", after)
	}
}
