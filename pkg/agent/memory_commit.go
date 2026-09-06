// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Defaults for session commit. The timeout bounds the detached commit
// goroutine only — commits never run on the turn's critical path.
const (
	defaultMemoryCommitTool      = "remember"
	defaultMemoryCommitTimeoutMs = 5000
	// memoryCommitDrainTimeout bounds shutdown draining: a commit already
	// exceeding it has also exceeded its own call timeout.
	memoryCommitDrainTimeout = 8 * time.Second
)

func normalizeMemoryCommitConfig(cfg config.MemoryCommitConfig) config.MemoryCommitConfig {
	if strings.TrimSpace(cfg.Tool) == "" {
		cfg.Tool = defaultMemoryCommitTool
	}
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = defaultMemoryCommitTimeoutMs
	}
	return cfg
}

// memoryCommitter pushes completed turns to a shared-memory backend (e.g.
// OpenViking's `remember` tool: messages in, async extraction out). Commits
// are fire-and-forget: they run on a background goroutine with their own
// timeout and can never delay or fail a turn. The WaitGroup lets shutdown
// drain in-flight commits instead of losing them to a hard process exit.
type memoryCommitter struct {
	cfg    config.MemoryCommitConfig
	wg     sync.WaitGroup
	commit func(ctx context.Context, messages []map[string]string) error
}

// Drain waits for in-flight commits, bounded by timeout; anything still
// running past it is abandoned (best effort, same as before). Call before
// tearing down the MCP manager the commits depend on.
func (m *memoryCommitter) Drain(timeout time.Duration) {
	if m == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.WarnCF("agent", "Memory commit drain timed out; abandoning in-flight commits",
			map[string]any{"timeout": timeout.String()})
	}
}

// commitTurnMemory enqueues an async commit of one completed turn. Empty
// messages are skipped; a missing committer (commit disabled) is a no-op.
func (al *AgentLoop) commitTurnMemory(sessionKey, userMessage, assistantMessage string) {
	committer := al.memoryCommitter
	if committer == nil {
		return
	}
	userMessage = strings.TrimSpace(userMessage)
	assistantMessage = strings.TrimSpace(assistantMessage)
	if userMessage == "" && assistantMessage == "" {
		return
	}

	messages := make([]map[string]string, 0, 2)
	if userMessage != "" {
		messages = append(messages, map[string]string{"role": "user", "content": userMessage})
	}
	if assistantMessage != "" {
		messages = append(messages, map[string]string{"role": "assistant", "content": assistantMessage})
	}

	committer.wg.Add(1)
	go func() {
		defer committer.wg.Done()
		// Detached context: the turn's contexts are typically canceled by
		// the time finalize returns, and a commit must outlive the turn.
		ctx, cancel := context.WithTimeout(context.Background(),
			time.Duration(committer.cfg.TimeoutMs)*time.Millisecond)
		defer cancel()
		if err := committer.commit(ctx, messages); err != nil {
			logger.WarnCF("agent", "Memory commit failed (turn unaffected)", map[string]any{
				"session_key": sessionKey,
				"server":      committer.cfg.Server,
				"error":       err.Error(),
			})
			return
		}
		logger.DebugCF("agent", "Memory commit delivered", map[string]any{
			"session_key": sessionKey,
			"server":      committer.cfg.Server,
			"messages":    len(messages),
		})
	}()
}

// registerMemoryCommitter wires the shared-memory committer onto the agent
// loop when memory.commit is enabled. The commit closure resolves the MCP
// manager lazily so deferred and late-connecting servers both work.
func registerMemoryCommitter(al *AgentLoop, cfg *config.Config) {
	if cfg == nil || !cfg.Memory.Commit.Enabled {
		return
	}
	commitCfg := normalizeMemoryCommitConfig(cfg.Memory.Commit)
	if strings.TrimSpace(commitCfg.Server) == "" {
		logger.WarnCF("agent", "Memory commit enabled but no MCP server configured", nil)
		return
	}
	if _, ok := cfg.Tools.MCP.Servers[commitCfg.Server]; !ok {
		logger.WarnCF("agent", "Memory commit server not found in tools.mcp.servers", map[string]any{
			"server": commitCfg.Server,
		})
		return
	}

	al.memoryCommitter = &memoryCommitter{
		cfg: commitCfg,
		commit: func(ctx context.Context, messages []map[string]string) error {
			if err := al.ensureMCPInitialized(ctx); err != nil {
				return fmt.Errorf("mcp init: %w", err)
			}
			manager := al.mcp.getManager()
			if manager == nil {
				return fmt.Errorf("mcp manager unavailable")
			}
			result, err := manager.CallTool(ctx, commitCfg.Server, commitCfg.Tool,
				map[string]any{"messages": messages})
			if err != nil {
				return err
			}
			if result != nil && result.IsError {
				return fmt.Errorf("commit tool %q returned an error result", commitCfg.Tool)
			}
			return nil
		},
	}
	logger.InfoCF("agent", "Session memory commit enabled", map[string]any{
		"server": commitCfg.Server,
		"tool":   commitCfg.Tool,
	})
}
