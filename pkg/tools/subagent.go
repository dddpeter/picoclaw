package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// SubTurnSpawner is an interface for spawning sub-turns.
// This avoids circular dependency between tools and agent packages.
type SubTurnSpawner interface {
	SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*ToolResult, error)
}

// SubTurnConfig holds configuration for spawning a sub-turn.
type SubTurnConfig struct {
	Model              string
	Tools              []Tool
	SystemPrompt       string
	MaxTokens          int
	Async              bool          // true for async (spawn), false for sync (subagent)
	Critical           bool          // continue running after parent finishes gracefully
	Timeout            time.Duration // 0 = use default (5 minutes)
	ActualSystemPrompt string
	InitialMessages    []providers.Message
	InitialTokenBudget *atomic.Int64 // Shared token budget for team members; nil if no budget
	TargetAgentID      string        // If set, run as this agent (its workspace, model, tools)
}

type SubagentTask struct {
	ID            string
	Task          string
	Label         string
	AgentID       string
	OriginChannel string
	OriginChatID  string
	Status        string
	Result        string
	Created       int64
}

// SubagentManager serves two purposes:
//  1. Seeds the spawn/subagent tools with the agent's default model and LLM
//     options at construction time (SetLLMOptions).
//  2. Backs the spawn_status tool's task registry (GetTaskCopy /
//     ListTaskCopies). Actual sub-turn execution lives in pkg/agent's
//     SubTurn mechanism and does not register tasks here yet — see
//     spawn_status.go for the resulting capability gap.
type SubagentManager struct {
	tasks          map[string]*SubagentTask
	nextID         int
	mu             sync.RWMutex
	defaultModel   string
	maxTokens    int
	hasMaxTokens bool
}

func NewSubagentManager(defaultModel string) *SubagentManager {
	return &SubagentManager{
		tasks:        make(map[string]*SubagentTask),
		nextID:       1,
		defaultModel: defaultModel,
	}
}

// RegisterTask records a spawned sub-turn so the spawn_status tool can report
// it. Returns the generated task ID ("subagent-N"). originChannel/originChatID
// scope the task to the spawning conversation (C-2: without them spawn_status
// leaks tasks across conversations; both come from the tool context).
func (sm *SubagentManager) RegisterTask(task, label, agentID, originChannel, originChatID string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	id := fmt.Sprintf("subagent-%d", sm.nextID)
	sm.nextID++
	sm.tasks[id] = &SubagentTask{
		ID:            id,
		Task:          task,
		Label:         label,
		AgentID:       agentID,
		OriginChannel: originChannel,
		OriginChatID:  originChatID,
		Status:        "running",
		Created:       time.Now().UnixMilli(),
	}
	return id
}

// CompleteTask marks a registered task finished and stores its result text.
// Unknown IDs are ignored (the task may have been cleared).
// M-2: finished tasks hold the full LLM result text, so the table is capped —
// oldest finished tasks are evoked beyond maxFinishedTasks (running tasks are
// never evoked; the gateway is long-lived and nothing else removes entries).
func (sm *SubagentManager) CompleteTask(taskID, status, result string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if t, ok := sm.tasks[taskID]; ok {
		t.Status = status
		t.Result = result
	}
	if len(sm.tasks) <= maxFinishedSubagentTasks {
		return
	}
	type idAt struct {
		id string
		at int64
	}
	done := make([]idAt, 0, len(sm.tasks))
	for id, t := range sm.tasks {
		if t.Status != "running" {
			done = append(done, idAt{id, t.Created})
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].at < done[j].at })
	evict := len(sm.tasks) - maxFinishedSubagentTasks
	for i := 0; i < len(done) && evict > 0; i++ {
		delete(sm.tasks, done[i].id)
		evict--
	}
}

// maxFinishedSubagentTasks caps the spawn_status task table (M-2).
const maxFinishedSubagentTasks = 50

// SetLLMOptions sets the max tokens seeded into spawn/subagent tools.
func (sm *SubagentManager) SetLLMOptions(maxTokens int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxTokens = maxTokens
	sm.hasMaxTokens = true
}

// GetTaskCopy returns a copy of the task with the given ID, taken under the
// read lock, so the caller receives a consistent snapshot with no data race.
func (sm *SubagentManager) GetTaskCopy(taskID string) (SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return SubagentTask{}, false
	}
	return *task, true
}

// ListTaskCopies returns value copies of all tasks, taken under the read lock,
// so callers receive consistent snapshots with no data race.
func (sm *SubagentManager) ListTaskCopies() []SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	copies := make([]SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		copies = append(copies, *task)
	}
	return copies
}

// SubagentTool executes a subagent task synchronously and returns the result.
// It directly calls SubTurnSpawner with Async=false for synchronous execution.
type SubagentTool struct {
	spawner      SubTurnSpawner
	manager      *SubagentManager
	defaultModel string
	maxTokens    int
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	if manager == nil {
		return &SubagentTool{}
	}
	return &SubagentTool{
		manager:      manager,
		defaultModel: manager.defaultModel,
		maxTokens:    manager.maxTokens,
	}
}

// SetSpawner sets the SubTurnSpawner for direct sub-turn execution.
func (t *SubagentTool) SetSpawner(spawner SubTurnSpawner) {
	t.spawner = spawner
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and return the result. Use this for delegating specific tasks to an independent agent instance. Returns execution summary to user and full details to LLM."
}

func (t *SubagentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return ErrorResult("task is required").WithError(fmt.Errorf("task parameter is required"))
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}

	// Build system prompt for subagent
	systemPrompt := fmt.Sprintf(
		`You are a subagent. Complete the given task independently and provide a clear, concise result.

Task: %s`,
		task,
	)

	if label != "" {
		systemPrompt = fmt.Sprintf(
			`You are a subagent labeled "%s". Complete the given task independently and provide a clear, concise result.

Task: %s`,
			label,
			task,
		)
	}

	// Use spawner if available (direct SpawnSubTurn call)
	if t.spawner != nil {
		// Register synchronously with conversation origin (C-2), like the
		// async spawn path.
		var taskID string
		if t.manager != nil {
			taskID = t.manager.RegisterTask(task, label, "", ToolChannel(ctx), ToolChatID(ctx))
		}
		result, err := t.spawner.SpawnSubTurn(ctx, SubTurnConfig{
			Model:        t.defaultModel,
			Tools:        nil, // Will inherit from parent via context
			SystemPrompt: systemPrompt,
			MaxTokens:    t.maxTokens,
			Async:        false, // Synchronous execution
		})
		if t.manager != nil && taskID != "" {
			status := "completed"
			if err != nil || result.IsError {
				status = "failed"
			}
			t.manager.CompleteTask(taskID, status, result.ForLLM)
		}
		if err != nil {
			return ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}

		// Format result for display
		userContent := result.ForLLM
		if result.ForUser != "" {
			userContent = result.ForUser
		}
		maxUserLen := 500
		if len(userContent) > maxUserLen {
			userContent = userContent[:maxUserLen] + "..."
		}

		labelStr := label
		if labelStr == "" {
			labelStr = "(unnamed)"
		}
		llmContent := fmt.Sprintf("Subagent task completed:\nLabel: %s\nResult: %s",
			labelStr, result.ForLLM)

		return &ToolResult{
			ForLLM:  llmContent,
			ForUser: userContent,
			Silent:  false,
			IsError: result.IsError,
			Async:   false,
		}
	}

	// Fallback: spawner not configured
	return ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("spawner not set"))
}
