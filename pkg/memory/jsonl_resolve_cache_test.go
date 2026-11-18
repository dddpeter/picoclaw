package memory

import (
	"context"
	"os"
	"testing"
)

// TestResolveSessionKey_CacheRebuildsAfterAliasRemap pins the invalidation
// hook on UpsertSessionMeta: alias mapping changes must invalidate memoized
// resolutions, otherwise a stale alias→canonical answer survives the remap.
func TestResolveSessionKey_CacheRebuildsAfterAliasRemap(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "canonical-a", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.AddMessage(ctx, "canonical-b", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "canonical-a", nil, []string{"legacy:key"}); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil || !found || resolved != "canonical-a" {
		t.Fatalf("first resolve = (%q, %v, %v), want (canonical-a, true, nil)", resolved, found, err)
	}

	// Warm cache already holds "legacy:key" → "canonical-a": a later resolve
	// must come from the memo (no rescan) and still answer canonical-a.
	resolved, found, err = store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil || !found || resolved != "canonical-a" {
		t.Fatalf("cached resolve = (%q, %v, %v), want (canonical-a, true, nil)", resolved, found, err)
	}

	// Remap: drop the alias from canonical-a first (UpsertSessionMeta only
	// writes its own session's alias list), then attach it to canonical-b.
	if err := store.UpsertSessionMeta(ctx, "canonical-a", nil, nil); err != nil {
		t.Fatalf("UpsertSessionMeta() detach error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "canonical-b", nil, []string{"legacy:key"}); err != nil {
		t.Fatalf("UpsertSessionMeta() remap error = %v", err)
	}

	resolved, found, err = store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil || !found || resolved != "canonical-b" {
		t.Fatalf("resolve after remap = (%q, %v, %v), want (canonical-b, true, nil)", resolved, found, err)
	}
}

// TestResolveSessionKey_NegativeCacheTurnsPositiveAfterFirstMessage pins the
// negative-cache liveness: resolving an unknown key memoizes "missing", and
// the memo must fall through to a rescan once the session gains its files.
func TestResolveSessionKey_NegativeCacheTurnsPositiveAfterFirstMessage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, found, err := store.ResolveSessionKey(ctx, "brand:new:key")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if found {
		t.Fatal("unknown key must not resolve before its first message")
	}

	if err := store.AddMessage(ctx, "brand:new:key", "user", "first"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "brand:new:key")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if !found || resolved != "brand:new:key" {
		t.Fatalf("resolve after first message = (%q, %v), want (brand:new:key, true)", resolved, found)
	}
}

// TestResolveSessionKey_CacheDetectsExternalDelete pins the positive-cache
// liveness: when the launcher deletes the session files out-of-band, the
// memoized alias resolution must not keep answering for a dead session.
func TestResolveSessionKey_CacheDetectsExternalDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "doomed", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "doomed", nil, []string{"alias:gone"}); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	if _, found, _ := store.ResolveSessionKey(ctx, "alias:gone"); !found {
		t.Fatal("alias must resolve before the external delete")
	}

	// Out-of-band delete (the launcher removes both files directly, without
	// going through this store instance).
	if err := os.Remove(store.jsonlPath("doomed")); err != nil {
		t.Fatalf("remove jsonl: %v", err)
	}
	if err := os.Remove(store.metaPath("doomed")); err != nil {
		t.Fatalf("remove meta: %v", err)
	}

	_, found, err := store.ResolveSessionKey(ctx, "alias:gone")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if found {
		t.Fatal("deleted session must not resolve after external delete")
	}
}
