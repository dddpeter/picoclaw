package tools

import (
	"fmt"
	"sync"
	"testing"
)

// T9 regression: the BM25 cache fast path reads cachedEngine/cacheVersion
// under cacheMu because the same tools execute in parallel within one turn.
// Hammer getOrBuildEngine while mutating the registry version to exercise
// fast path, rebuild and double-checked re-entry (run with -race to catch
// the unlocked-read bug this pins away).
func TestBM25GetOrBuildEngineConcurrent(t *testing.T) {
	reg := NewToolRegistry()
	for i := 0; i < 10; i++ {
		reg.RegisterHidden(&mockSearchableTool{
			name: fmt.Sprintf("tool_%d", i),
			desc: fmt.Sprintf("concurrent functionality %d", i),
		})
	}
	tool := NewBM25SearchTool(reg, 5, 10)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				eng := tool.getOrBuildEngine()
				if eng == nil {
					t.Error("engine must not be nil with registered hidden tools")
					return
				}
				if i%50 == 0 && g == 0 {
					// Bump the registry version to force rebuilds racing
					// the readers.
					reg.RegisterHidden(&mockSearchableTool{
						name: fmt.Sprintf("extra_%d_%d", g, i),
						desc: "extra tool",
					})
				}
			}
		}(g)
	}
	wg.Wait()

	// After the dust settles the cache must serve one consistent engine.
	first := tool.getOrBuildEngine()
	for i := 0; i < 10; i++ {
		if got := tool.getOrBuildEngine(); got != first {
			t.Fatal("stabilized cache returned a different engine pointer")
		}
	}
}
