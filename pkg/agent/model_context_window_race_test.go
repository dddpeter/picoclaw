// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"sync"
	"testing"
	"time"
)

// TestSwapAgentModelVsUnlockedWindowReads pins the concurrency contract of
// the per-model window follow (fork feature): swapAgentModelLocked mutates
// ContextWindow/DeclaredContextWindow under the model-state write lock, and
// code outside a live turn — idle compaction scanner, post-turn scheduled
// compaction, /context command, legacy async summarize — must read the
// windows through the locked snapshot helpers instead of the bare fields.
// Before the cap feature the fields were immutable after construction, so
// those paths legitimately took no lock. Run with -race this test fails on
// any regression back to bare unlocked reads.
func TestSwapAgentModelVsUnlockedWindowReads(t *testing.T) {
	cfg := newPerModelWindowConfig(t.TempDir(), "flagship", true)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	al := &AgentLoop{}
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			target := "flagship"
			if i%2 == 1 {
				target = "speed"
			}
			if _, err := al.swapAgentModelLocked(cfg, agent, target); err != nil {
				t.Errorf("swap to %s: %v", target, err)
				return
			}
		}
	}()

	// Reader goroutines mirror the unlocked async paths post-fix: the idle
	// scanner's snapshotContextUsage + snapshotCompactionBudget and the
	// /context command's snapshotContextUsage.
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = agent.snapshotContextUsage("race-session")
				_ = agent.snapshotCompactionBudget()
			}
		}()
	}

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()
}
