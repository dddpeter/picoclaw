package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNormalizeMemoryCommitConfig(t *testing.T) {
	got := normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"})
	if got.Tool != "remember" {
		t.Errorf("default tool = %q, want remember", got.Tool)
	}
	if got.TimeoutMs != defaultMemoryCommitTimeoutMs {
		t.Errorf("default timeout = %d, want %d", got.TimeoutMs, defaultMemoryCommitTimeoutMs)
	}
}

func TestCommitTurnMemoryAsync(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})

	var mu sync.Mutex
	var committed [][]map[string]string
	al.memoryCommitter = &memoryCommitter{
		cfg: normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"}),
		commit: func(_ context.Context, messages []map[string]string) error {
			mu.Lock()
			committed = append(committed, messages)
			mu.Unlock()
			return nil
		},
	}

	start := time.Now()
	al.commitTurnMemory("session-1", "帮我画一只柴犬", "好的，已经画好了。")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("commitTurnMemory blocked for %v; commits must be fire-and-forget", elapsed)
	}

	waitForCondition(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(committed) == 1
	})

	mu.Lock()
	msgs := committed[0]
	mu.Unlock()
	if len(msgs) != 2 || msgs[0]["role"] != "user" || msgs[0]["content"] != "帮我画一只柴犬" ||
		msgs[1]["role"] != "assistant" || msgs[1]["content"] != "好的，已经画好了。" {
		t.Fatalf("unexpected committed messages: %+v", msgs)
	}
}

func TestCommitTurnMemorySkipsEmpty(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	called := false
	al.memoryCommitter = &memoryCommitter{
		cfg: normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"}),
		commit: func(_ context.Context, _ []map[string]string) error {
			called = true
			return nil
		},
	}

	al.commitTurnMemory("session-1", "   ", "")
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("empty turn must not be committed")
	}

	// Disabled committer is a no-op.
	al.memoryCommitter = nil
	al.commitTurnMemory("session-1", "hi", "answer") // must not panic
}

func TestCommitTurnMemoryAssistantOnly(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})

	var mu sync.Mutex
	var committed [][]map[string]string
	al.memoryCommitter = &memoryCommitter{
		cfg: normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"}),
		commit: func(_ context.Context, messages []map[string]string) error {
			mu.Lock()
			committed = append(committed, messages)
			mu.Unlock()
			return nil
		},
	}

	// Tool-delivered turns may have an empty user-visible answer but a
	// streamed assistant text; commit it alone.
	al.commitTurnMemory("session-2", "", "刀盾狗到位。")
	waitForCondition(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(committed) == 1
	})
	mu.Lock()
	defer mu.Unlock()
	if len(committed[0]) != 1 || committed[0][0]["role"] != "assistant" {
		t.Fatalf("expected assistant-only commit, got %+v", committed[0])
	}
}

func TestCommitTurnMemoryCommitErrorNonFatal(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	al.memoryCommitter = &memoryCommitter{
		cfg: normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"}),
		commit: func(_ context.Context, _ []map[string]string) error {
			return errors.New("openviking down")
		},
	}
	// Must return without panicking; the error is only logged.
	al.commitTurnMemory("session-3", "q", "a")
	time.Sleep(50 * time.Millisecond)
}

// TestFinalizeCommitsTurnToMemory verifies the wiring: both finalize paths
// (normal and tool-delivered) enqueue a memory commit.
func TestFinalizeCommitsTurnToMemory(t *testing.T) {
	al := newLegacyTestAgentLoop(t, &summarizingRecordingProvider{response: "unused"})
	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	var mu sync.Mutex
	var committed []string
	al.memoryCommitter = &memoryCommitter{
		cfg: normalizeMemoryCommitConfig(config.MemoryCommitConfig{Enabled: true, Server: "openviking"}),
		commit: func(_ context.Context, messages []map[string]string) error {
			var sb strings.Builder
			for _, m := range messages {
				sb.WriteString(m["role"])
				sb.WriteString(":")
				sb.WriteString(m["content"])
				sb.WriteString("|")
			}
			mu.Lock()
			committed = append(committed, sb.String())
			mu.Unlock()
			return nil
		},
	}

	ts := newTurnState(agent, processOptions{SessionKey: "session-commit"},
		al.newTurnEventScope(agent.ID, "session-commit", nil))
	ts.userMessage = "记一下我喜欢柴犬"
	exec := &turnExecution{llmModelName: "test-model"}

	pipeline := NewPipeline(al)
	if _, err := pipeline.Finalize(context.Background(), context.Background(), ts, exec,
		TurnEndStatusCompleted, "已记住"); err != nil {
		t.Fatalf("Finalize failed: %v", err)
	}

	waitForCondition(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(committed) == 1
	})
	mu.Lock()
	got := committed[0]
	mu.Unlock()
	if !strings.Contains(got, "user:记一下我喜欢柴犬") || !strings.Contains(got, "assistant:已记住") {
		t.Fatalf("unexpected commit payload: %q", got)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
