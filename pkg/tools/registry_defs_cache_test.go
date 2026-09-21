// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"fmt"
	"sync"
	"testing"
)

// Consumers append to the slice returned by ToProviderDefs (see
// agent.restoreToolDefinition), so the cache must hand out copies with capacity
// exactly len(): an append must allocate rather than write into the cached
// backing array.
func TestToProviderDefs_ReturnsExactCapacityCopies(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("alpha", "a"))
	r.Register(newMockTool("beta", "b"))

	first := r.ToProviderDefs()
	if len(first) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(first))
	}
	if cap(first) != len(first) {
		t.Fatalf("definitions must have cap == len so appends cannot reach the cache: len=%d cap=%d",
			len(first), cap(first))
	}

	if names := []string{first[0].Function.Name, first[1].Function.Name}; names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("definitions must stay in sorted name order, got %v", names)
	}

	// Simulate the append a caller may perform on the returned slice.
	first = first[:1]
	if first[0].Function.Name != "alpha" {
		t.Fatalf("expected 'alpha' as the first definition, got %q", first[0].Function.Name)
	}

	second := r.ToProviderDefs()
	if len(second) != 2 || second[0].Function.Name != "alpha" || second[1].Function.Name != "beta" {
		t.Fatalf("cached definitions differ from a fresh build: %d entries", len(second))
	}
}

// The definitions cache is keyed on the registry version, so the TTL updates
// that change tool visibility must invalidate it — while ticks inside a TTL
// epoch must not, or the cache would never survive to be used.
func TestToProviderDefs_CacheTracksTTLVisibility(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("core_tool", "core"))
	r.RegisterHidden(newMockTool("hidden_tool", "hidden"))

	if got := len(r.ToProviderDefs()); got != 1 {
		t.Fatalf("hidden tool must not be exposed before promotion: got %d definitions, want 1", got)
	}
	if got := len(r.ToProviderDefs()); got != 1 { // warm the cache
		t.Fatalf("second read must agree with the first: got %d definitions", got)
	}

	r.PromoteTools([]string{"hidden_tool"}, 2)
	if got := len(r.ToProviderDefs()); got != 2 {
		t.Fatalf("promotion must expose the hidden tool: got %d definitions, want 2", got)
	}

	r.TickTTL() // TTL 2 -> 1: still visible
	if got := len(r.ToProviderDefs()); got != 2 {
		t.Fatalf("a tick that keeps the tool visible must not change the definitions: got %d, want 2", got)
	}

	r.TickTTL() // TTL 1 -> 0: hidden again
	if got := len(r.ToProviderDefs()); got != 1 {
		t.Fatalf("an expired tool must disappear from the definitions: got %d, want 1", got)
	}
}

// The cache is filled under a lock upgrade (RLock fast path, then Lock to
// build) while mutators bump the version under the same lock. Hammer both
// sides concurrently: every observed definition must stay consistent with
// the registry, and once mutation stops the cache must agree with a fresh
// build. This is a logic-level check — the race detector cannot be relied
// upon on all platforms, and a torn cache would fail the final assertions.
func TestToProviderDefs_ConcurrentMutatorsKeepCacheConsistent(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("core_a", "core"))
	r.Register(newMockTool("core_b", "core"))
	r.RegisterHidden(newMockTool("hidden_a", "hidden"))
	r.RegisterHidden(newMockTool("hidden_b", "hidden"))

	known := map[string]bool{
		"core_a": true, "core_b": true, "hidden_a": true, "hidden_b": true,
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, d := range r.ToProviderDefs() {
					if !known[d.Function.Name] {
						t.Errorf("unexpected tool %q surfaced from the cache", d.Function.Name)
						return
					}
				}
			}
		}()
	}

	// Each round cycles both hidden tools through promote -> expire, forcing
	// the cache through invalidation and rebuild while readers are active.
	for range 50 {
		r.PromoteTools([]string{"hidden_a"}, 3)
		r.PromoteTools([]string{"hidden_b"}, 1)
		for range 3 {
			r.TickTTL()
		}
	}
	close(stop)
	wg.Wait()

	// Final state: both hidden tools expired, so only the core tools remain.
	// The cached answer must equal a fresh build from the registry contents.
	cached := r.ToProviderDefs()
	fresh := r.buildProviderDefsLocked()
	if len(cached) != 2 || len(fresh) != 2 {
		t.Fatalf("final definitions: cached %d, fresh %d, want 2 (core only)",
			len(cached), len(fresh))
	}
	for i := range cached {
		if cached[i].Function.Name != fresh[i].Function.Name {
			t.Errorf("cached def %q disagrees with fresh build %q",
				cached[i].Function.Name, fresh[i].Function.Name)
		}
	}
}

// BenchmarkToProviderDefs measures the cached path the pipeline hits several
// times per turn.
func BenchmarkToProviderDefs(b *testing.B) {
	r := NewToolRegistry()
	for i := range 20 {
		r.Register(newMockTool(fmt.Sprintf("tool_%02d", i), "a benchmark tool"))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.ToProviderDefs()
	}
}

// BenchmarkToProviderDefsRebuild measures the rebuild the cache avoids: same
// call, minus memoization.
func BenchmarkToProviderDefsRebuild(b *testing.B) {
	r := NewToolRegistry()
	for i := range 20 {
		r.Register(newMockTool(fmt.Sprintf("tool_%02d", i), "a benchmark tool"))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.buildProviderDefsLocked()
	}
}
