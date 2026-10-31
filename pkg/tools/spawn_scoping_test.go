package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// C-2: tasks registered via spawn must carry the originating channel/chat so
// spawn_status can scope results to the calling conversation; m-6: the task
// must be registered (and its ID echoed in the ack) BEFORE Execute returns,
// not racing inside the spawn goroutine.
func TestSpawnRegistersOriginAndAcksTaskID(t *testing.T) {
	manager := NewSubagentManager("test-model")
	spawnTool := NewSpawnTool(manager)
	spawnTool.SetSpawner(&mockSpawner{})

	ctxA := WithToolContext(context.Background(), "telegram", "chat-A")
	res := spawnTool.Execute(ctxA, map[string]any{"task": "secret-A"})
	if res.IsError {
		t.Fatalf("spawn failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "subagent-") {
		t.Fatalf("spawn ack must include the registered task ID (m-6: registration must precede the goroutine), got: %s", res.ForLLM)
	}

	tasks := manager.ListTaskCopies()
	if len(tasks) != 1 {
		t.Fatalf("task must be registered synchronously, got %d tasks", len(tasks))
	}
	if tasks[0].OriginChannel != "telegram" || tasks[0].OriginChatID != "chat-A" {
		t.Fatalf("task origin not recorded: channel=%q chatID=%q", tasks[0].OriginChannel, tasks[0].OriginChatID)
	}
}

func TestSpawnStatusScopesTasksToCallingConversation(t *testing.T) {
	manager := NewSubagentManager("test-model")
	spawnTool := NewSpawnTool(manager)
	spawnTool.SetSpawner(&mockSpawner{})

	ctxA := WithToolContext(context.Background(), "telegram", "chat-A")
	if res := spawnTool.Execute(ctxA, map[string]any{"task": "secret-A"}); res.IsError {
		t.Fatalf("spawn failed: %s", res.ForLLM)
	}

	status := NewSpawnStatusTool(manager)

	// Same conversation sees the task.
	outA := status.Execute(WithToolContext(context.Background(), "telegram", "chat-A"), map[string]any{})
	if outA.IsError || !strings.Contains(outA.ForLLM, "secret-A") {
		t.Fatalf("same-conversation spawn_status must list the task, got: %s", outA.ForLLM)
	}

	// Another conversation must not see it (C-2: OriginChannel/ChatID filter).
	outB := status.Execute(WithToolContext(context.Background(), "slack", "chat-B"), map[string]any{})
	if strings.Contains(outB.ForLLM, "secret-A") {
		t.Fatalf("cross-conversation leak via spawn_status: %s", outB.ForLLM)
	}
	if !strings.Contains(outB.ForLLM, "No subagents") {
		t.Fatalf("other conversation should see an empty task list, got: %s", outB.ForLLM)
	}

	// Direct task_id lookup is scoped the same way.
	outID := status.Execute(WithToolContext(context.Background(), "slack", "chat-B"), map[string]any{"task_id": tasks0ID(manager)})
	if !outID.IsError {
		t.Fatalf("cross-conversation task_id lookup must fail, got: %s", outID.ForLLM)
	}
}

// tasks0ID returns the single registered task's ID.
func tasks0ID(manager *SubagentManager) string {
	tasks := manager.ListTaskCopies()
	if len(tasks) == 0 {
		return ""
	}
	return tasks[0].ID
}

// M-2: finished tasks (full LLM result text) must not accumulate unboundedly.
func TestCompleteTaskEvictsOldestFinishedTasks(t *testing.T) {
	manager := NewSubagentManager("test-model")

	// A running task must survive every eviction round.
	runningID := manager.RegisterTask("busy", "", "", "ch", "c")
	time.Sleep(2 * time.Millisecond)
	oldestID := manager.RegisterTask("oldest", "", "", "ch", "c")
	manager.CompleteTask(oldestID, "completed", "r")

	for i := 0; i < 60; i++ {
		id := manager.RegisterTask(fmt.Sprintf("task-%d", i), "", "", "ch", "c")
		manager.CompleteTask(id, "completed", "r")
		time.Sleep(time.Millisecond)
	}

	tasks := manager.ListTaskCopies()
	if len(tasks) != maxFinishedSubagentTasks {
		t.Fatalf("task table size = %d, want capped at %d", len(tasks), maxFinishedSubagentTasks)
	}
	if _, ok := manager.GetTaskCopy(oldestID); ok {
		t.Fatal("oldest finished task was not evicted")
	}
	if _, ok := manager.GetTaskCopy(runningID); !ok {
		t.Fatal("running task must never be evicted")
	}
}
