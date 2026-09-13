package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
)

// --- blueprint action ---

func TestCronTool_BlueprintsActionListsCatalog(t *testing.T) {
	tool := newTestCronTool(t)

	result := tool.Execute(context.Background(), map[string]any{"action": "blueprints"})
	if result.IsError {
		t.Fatalf("blueprints action failed: %q", result.ForLLM)
	}
	for _, name := range []string{"daily_report", "interval_check", "heartbeat_patrol"} {
		if !strings.Contains(result.ForLLM, name) {
			t.Fatalf("catalog missing blueprint %q, got %q", name, result.ForLLM)
		}
	}
}

func TestCronTool_AddWithBlueprint(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":           "add",
		"blueprint":        "daily_report",
		"blueprint_values": map[string]any{"time": "07:15", "text": "morning digest"},
	})
	if result.IsError {
		t.Fatalf("blueprint add failed: %q", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	job := jobs[0]
	if job.Schedule.Kind != "cron" || job.Schedule.Expr != "15 7 * * *" {
		t.Fatalf("unexpected schedule %+v", job.Schedule)
	}
	if !strings.Contains(job.Payload.Message, "morning digest") {
		t.Fatalf("message should come from the blueprint template, got %q", job.Payload.Message)
	}
}

func TestCronTool_AddWithUnknownBlueprint(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":    "add",
		"blueprint": "nope",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "unknown blueprint") {
		t.Fatalf("expected unknown-blueprint error, got %+v", result)
	}
}

func TestCronTool_AddTruncatedCronExprRejected(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")

	result := tool.Execute(ctx, map[string]any{
		"action":    "add",
		"message":   "broken",
		"cron_expr": "45 16",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "invalid cron expression") {
		t.Fatalf("truncated cron_expr must be rejected with a clear error, got %+v", result)
	}
}

// --- suggestions actions ---

func seedSuggestion(t *testing.T, tool *CronTool, name, dedup string) cron.Suggestion {
	t.Helper()
	every := int64(86400000)
	s, ok, err := tool.suggestions.Add(cron.Suggestion{
		DedupKey: dedup,
		Name:     name,
		Message:  "do " + name,
		Schedule: cron.CronSchedule{Kind: "every", EveryMS: &every},
		Source:   "evolution",
	})
	if err != nil || !ok {
		t.Fatalf("seed suggestion failed: ok=%v err=%v", ok, err)
	}
	return s
}

func TestCronTool_SuggestionsListAcceptDismiss(t *testing.T) {
	executor := &stubJobExecutor{response: "ok"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	s := seedSuggestion(t, tool, "nightly summary", "k1")

	// List.
	result := tool.Execute(context.Background(), map[string]any{"action": "suggestions"})
	if result.IsError || !strings.Contains(result.ForLLM, s.ID) {
		t.Fatalf("suggestions listing must contain the seeded id, got %q", result.ForLLM)
	}

	// Accept → real job bound to the accepting channel.
	result = tool.Execute(WithToolContext(context.Background(), "feishu", "chat9"), map[string]any{
		"action":        "accept_suggestion",
		"suggestion_id": s.ID,
	})
	if result.IsError {
		t.Fatalf("accept failed: %q", result.ForLLM)
	}
	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 || jobs[0].Payload.Channel != "feishu" || jobs[0].Payload.To != "chat9" {
		t.Fatalf("accepted job must be bound to the accepting channel, got %+v", jobs)
	}

	// Accepted suggestion leaves the pending list.
	result = tool.Execute(context.Background(), map[string]any{"action": "suggestions"})
	if strings.Contains(result.ForLLM, s.ID) {
		t.Fatalf("accepted suggestion must leave the pending list, got %q", result.ForLLM)
	}
}

func TestCronTool_DismissLatchesSuggestion(t *testing.T) {
	tool := newTestCronTool(t)
	s := seedSuggestion(t, tool, "noisy job", "k2")

	result := tool.Execute(context.Background(), map[string]any{
		"action":        "dismiss_suggestion",
		"suggestion_id": s.ID,
	})
	if result.IsError {
		t.Fatalf("dismiss failed: %q", result.ForLLM)
	}
	if jobs := tool.cronService.ListJobs(false); len(jobs) != 0 {
		t.Fatalf("dismissal must not create a job")
	}
	// Re-adding the same dedup key is latched by the store.
	if _, ok, _ := tool.suggestions.Add(cron.Suggestion{
		DedupKey: "k2", Name: "noisy job v2", Message: "m",
		Schedule: cron.CronSchedule{Kind: "every", EveryMS: int64Ptr(86400000)}, Source: "evolution",
	}); ok {
		t.Fatalf("dismissed dedup key must stay latched")
	}
}

func TestCronTool_SuggestionsRequireId(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{"action": "accept_suggestion"})
	if !result.IsError || !strings.Contains(result.ForLLM, "suggestion_id is required") {
		t.Fatalf("expected suggestion_id requirement, got %+v", result)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}
