package memory

import (
	"context"
	"testing"
)

func setTitle(t *testing.T, s *JSONLStore, key, title, source string) bool {
	t.Helper()
	applied, err := s.SetSessionTitle(context.Background(), key, title, source)
	if err != nil {
		t.Fatalf("SetSessionTitle(%q, %q): %v", title, source, err)
	}
	return applied
}

func TestSetSessionTitlePriorityMatrix(t *testing.T) {
	s, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const key = "agent:main:feishu:direct:oc_test"

	// Empty titles are rejected outright.
	if setTitle(t, s, key, "  ", SessionTitleSourceUser) {
		t.Fatal("empty title must be rejected")
	}

	// derived fills an empty slot.
	if !setTitle(t, s, key, "部署巡检", SessionTitleSourceDerived) {
		t.Fatal("derived title must fill an empty session")
	}
	// Same-source replacement is allowed (llm/derived retry semantics).
	if !setTitle(t, s, key, "部署巡检 v2", SessionTitleSourceDerived) {
		t.Fatal("same-source replacement must be allowed")
	}
	// llm outranks derived.
	if !setTitle(t, s, key, "部署巡检与告警清理", SessionTitleSourceLLM) {
		t.Fatal("llm title must replace derived")
	}
	// derived cannot climb over llm.
	if setTitle(t, s, key, "stale derived", SessionTitleSourceDerived) {
		t.Fatal("derived must not replace llm title")
	}
	// user outranks everything.
	if !setTitle(t, s, key, "手动命名", SessionTitleSourceUser) {
		t.Fatal("user title must replace llm title")
	}
	// Neither llm nor derived may clobber a user title.
	if setTitle(t, s, key, "llm retry", SessionTitleSourceLLM) {
		t.Fatal("llm must not replace user title")
	}
	if setTitle(t, s, key, "derived retry", SessionTitleSourceDerived) {
		t.Fatal("derived must not replace user title")
	}

	meta, err := s.GetSessionMeta(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "手动命名" || meta.TitleSource != SessionTitleSourceUser {
		t.Fatalf("final title = %q (%q), want 手动命名 (user)", meta.Title, meta.TitleSource)
	}
}

func TestSessionTitlePersistsAcrossReopenAndMetaUpsert(t *testing.T) {
	dir := t.TempDir()
	s, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const key = "agent:main:pico:direct:pico:abc"

	if !setTitle(t, s, key, "调研 hermes", SessionTitleSourceLLM) {
		t.Fatal("llm title write failed")
	}
	// Scope/alias upserts must preserve the title (read-modify-write).
	if err := s.UpsertSessionMeta(ctx, key, nil, []string{"alias-a"}); err != nil {
		t.Fatal(err)
	}

	// Reopen the store from disk: the title survives.
	s2, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := s2.GetSessionMeta(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "调研 hermes" || meta.TitleSource != SessionTitleSourceLLM {
		t.Fatalf("title lost after reopen: %+v", meta)
	}
	if len(meta.Aliases) != 1 || meta.Aliases[0] != "alias-a" {
		t.Fatalf("aliases lost: %v", meta.Aliases)
	}
}
