package memory

import (
	"context"
	"testing"
)

// TestAddMessage_MetaDeferredUntilCompact pins the batching contract: a
// message append accumulates Count/UpdatedAt in memory (no per-message meta
// rewrite, no per-message fsync), the history is fully readable through the
// in-memory view, and the durability point — Compact, which the finalize
// path calls as Save before publishing the reply — flushes both the JSONL
// fsync and the pending metadata to disk.
func TestAddMessage_MetaDeferredUntilCompact(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "batched", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	// History readable immediately through the cache-aware view.
	history, err := store.GetHistory(ctx, "batched")
	if err != nil {
		t.Fatalf("GetHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d, want 1", len(history))
	}

	// On-disk meta still lags: the append deferred the meta write.
	disk, err := store.readMeta("batched")
	if err != nil {
		t.Fatalf("readMeta() error = %v", err)
	}
	if disk.Count != 0 {
		t.Fatalf("on-disk meta Count = %d before Compact, want 0 (deferred)", disk.Count)
	}

	// The durability point flushes the pending metadata.
	if err := store.Compact(ctx, "batched"); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	disk, err = store.readMeta("batched")
	if err != nil {
		t.Fatalf("readMeta() after Compact error = %v", err)
	}
	if disk.Count != 1 {
		t.Fatalf("on-disk meta Count = %d after Compact, want 1", disk.Count)
	}
	if disk.UpdatedAt.IsZero() {
		t.Fatal("on-disk meta UpdatedAt still zero after Compact")
	}
}

// TestCloseFlushesPendingMeta pins the shutdown flush: appends that never
// saw a Save still land their metadata on Close, so a fresh instance reads
// the truthful Count.
func TestCloseFlushesPendingMeta(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}

	if err := store.AddMessage(ctx, "tail", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.AddMessage(ctx, "tail", "assistant", "hi"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	fresh, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore (fresh): %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	meta, err := fresh.GetSessionMeta(ctx, "tail")
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	if meta.Count != 2 {
		t.Fatalf("meta Count after Close = %d, want 2", meta.Count)
	}
	if meta.UpdatedAt.IsZero() {
		t.Fatal("meta UpdatedAt after Close is zero")
	}
}

// TestMetaFlushMergesExternalTitleWrite pins the flush merge semantics: the
// launcher writes session titles out-of-band (its own store instance), and a
// pending append-count flush must not clobber that title — the flush merges
// into a freshly read disk meta instead of rewriting the cached copy whole.
func TestMetaFlushMergesExternalTitleWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.AddMessage(ctx, "shared", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	// Second instance plays the launcher process: out-of-band title write
	// straight to the shared directory.
	launcher, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore (launcher): %v", err)
	}
	t.Cleanup(func() { _ = launcher.Close() })
	if ok, err := launcher.SetSessionTitle(ctx, "shared", "外部标题", SessionTitleSourceUser); err != nil || !ok {
		t.Fatalf("SetSessionTitle() = (%v, %v), want (true, nil)", ok, err)
	}

	// The gateway store's pending Count flush must preserve the title.
	if err := store.Compact(ctx, "shared"); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	verifier, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore (verifier): %v", err)
	}
	t.Cleanup(func() { _ = verifier.Close() })
	meta, err := verifier.GetSessionMeta(ctx, "shared")
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	if meta.Title != "外部标题" {
		t.Fatalf("meta Title = %q, want the out-of-band title preserved", meta.Title)
	}
	if meta.Count != 1 {
		t.Fatalf("meta Count = %d, want 1 (pending append flushed)", meta.Count)
	}
	if meta.TitleSource != SessionTitleSourceUser {
		t.Fatalf("meta TitleSource = %q, want user", meta.TitleSource)
	}
}
