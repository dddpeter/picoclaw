package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApprovalMarkerRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}

	if m, err := store.ReadApprovalMarker("agent:main:pico:x"); err != nil || m != nil {
		t.Fatalf("fresh read = (%v, %v), want (nil, nil)", m, err)
	}

	if err := store.WriteApprovalMarker(ApprovalMarker{
		SessionKey: "agent:main:pico:x",
		AgentID:    "agent:main",
		Tool:       "exec",
		Preview:    "git push origin main",
		TimeoutMs:  300000,
	}); err != nil {
		t.Fatalf("WriteApprovalMarker() error = %v", err)
	}

	m, err := store.ReadApprovalMarker("agent:main:pico:x")
	if err != nil || m == nil {
		t.Fatalf("read after write = (%v, %v), want marker", m, err)
	}
	if m.Tool != "exec" || m.Preview != "git push origin main" || m.TimeoutMs != 300000 {
		t.Fatalf("marker = %+v", *m)
	}
	if m.StartedAt == 0 {
		t.Fatal("StartedAt should default to now when unset")
	}

	if err := store.ClearApprovalMarker("agent:main:pico:x"); err != nil {
		t.Fatalf("ClearApprovalMarker() error = %v", err)
	}
	if m, err := store.ReadApprovalMarker("agent:main:pico:x"); err != nil || m != nil {
		t.Fatalf("read after clear = (%v, %v), want (nil, nil)", m, err)
	}
	// 幂等清除
	if err := store.ClearApprovalMarker("agent:main:pico:x"); err != nil {
		t.Fatalf("idempotent clear error = %v", err)
	}
}

func TestApprovalMarkerCorruptSelfHeals(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	path := ApprovalMarkerFile(dir, "agent:main:pico:x")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m, err := store.ReadApprovalMarker("agent:main:pico:x"); err != nil || m != nil {
		t.Fatalf("corrupt read = (%v, %v), want (nil, nil)", m, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt marker should be removed (self-healing)")
	}
}

func TestApprovalMarkerRejectsEmptyKey(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteApprovalMarker(ApprovalMarker{Tool: "exec"}); err == nil {
		t.Fatal("empty session key should be rejected")
	}
}
