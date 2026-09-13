package tools

import (
	"context"

	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
)

// Wake-gate tests pin the fork behavior borrowed from hermes-agent's cron
// scheduler (docs/design/hermes-borrowing-analysis.zh.md §五): a cron job may
// carry a pre-run script; when its LAST non-empty stdout line is JSON
// {"wakeAgent": false}, the whole run is skipped — no agent turn, no command
// execution, no delivery. Anything else (non-JSON, missing flag, gate absent)
// wakes the agent and injects the script stdout as prompt context.

func TestParseWakeGate(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"gate false skips", "{\"wakeAgent\": false}\n", false},
		{"gate false no spaces", "{\"wakeAgent\":false}", false},
		{"gate true wakes", "{\"wakeAgent\": true}", true},
		{"json without flag wakes", "{\"other\": 1}", true},
		{"plain text wakes", "all good", true},
		{"empty output wakes", "", true},
		{"gate must be last non-empty line", "{\"wakeAgent\": false}\n\ntrailing text\n", true},
		{"gate after blank lines", "data\n\n{\"wakeAgent\": false}\n\n", false},
		{"broken json wakes", "{not json", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseWakeGate(tt.output); got != tt.want {
				t.Fatalf("parseWakeGate(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func addCronJobWithScript(t *testing.T, tool *CronTool, executor *stubJobExecutor, script string) *cron.CronJob {
	t.Helper()
	job := addTestCronJob(t, tool, "gated", "cli", "direct", "")
	job.Payload.Script = script
	if err := tool.cronService.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob() error: %v", err)
	}
	return job
}

func TestCronTool_WakeGateFalseSkipsAgentTurn(t *testing.T) {
	executor := &stubJobExecutor{response: "should not run"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := addCronJobWithScript(t, tool, executor, "echo 'checking'; echo '{\"wakeAgent\": false}'")

	result := tool.ExecuteJob(context.Background(), job)

	if !strings.Contains(result, "skipped by wake gate") {
		t.Fatalf("ExecuteJob() = %q, want wake-gate skip", result)
	}
	if executor.lastPrompt != "" {
		t.Fatalf("agent turn must not run when wake gate says skip, got prompt %q", executor.lastPrompt)
	}
}

func TestCronTool_WakeGateTrueInjectsScriptContext(t *testing.T) {
	executor := &stubJobExecutor{response: "ok"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := addCronJobWithScript(t, tool, executor, "echo 'disk usage 91%'; echo '{\"wakeAgent\": true}'")

	tool.ExecuteJob(context.Background(), job)

	if executor.lastPrompt == "" {
		t.Fatalf("agent turn should run when wake gate says wake")
	}
	if !strings.Contains(executor.lastPrompt, "Pre-run script output") {
		t.Fatalf("prompt must contain the pre-run script context block, got %q", executor.lastPrompt)
	}
	if !strings.Contains(executor.lastPrompt, "disk usage 91%") {
		t.Fatalf("prompt must contain script stdout, got %q", executor.lastPrompt)
	}
	if !strings.Contains(executor.lastPrompt, job.Payload.Message) {
		t.Fatalf("prompt must retain the job message, got %q", executor.lastPrompt)
	}
}

func TestCronTool_ScriptOutputMissingMeansWake(t *testing.T) {
	executor := &stubJobExecutor{response: "ok"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := addCronJobWithScript(t, tool, executor, "exit 0")

	tool.ExecuteJob(context.Background(), job)

	if executor.lastPrompt == "" {
		t.Fatalf("silent script (no output) must wake the agent, gate is fail-open")
	}
}

func TestCronTool_FailingScriptMarksJobErrorWithoutAgentTurn(t *testing.T) {
	executor := &stubJobExecutor{response: "should not run"}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := addCronJobWithScript(t, tool, executor, "exit 3")

	result := tool.ExecuteJob(context.Background(), job)

	if !strings.Contains(result, "pre-run script failed") {
		t.Fatalf("ExecuteJob() = %q, want script failure", result)
	}
	if executor.lastPrompt != "" {
		t.Fatalf("agent turn must not run when the pre-run script fails")
	}
}

func TestCronTool_AddJobScriptGuard(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.AllowCommand = false
	tool := newTestCronToolWithConfig(t, cfg)
	ctx := WithToolContext(context.Background(), "feishu", "chat1")

	// Remote channel + script: same guard rail as command scheduling.
	result := tool.Execute(ctx, map[string]any{
		"action":        "add",
		"message":       "gated job",
		"script":        "echo hi",
		"every_seconds": float64(3600),
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "internal channels") {
		t.Fatalf("script add from remote channel must be restricted, got %+v", result)
	}

	// command_confirm override still applies to script — from an internal channel.
	result = tool.Execute(WithToolContext(context.Background(), "cli", "direct"), map[string]any{
		"action":          "add",
		"message":         "gated job",
		"script":          "echo hi",
		"every_seconds":   float64(3600),
		"command_confirm": true,
	})
	if result.IsError {
		t.Fatalf("script add with command_confirm should pass, got %q", result.ForLLM)
	}

	updated, ok := tool.cronService.GetJob(parseAddedJobID(t, result.ForLLM))
	if !ok {
		t.Fatalf("job not found after add")
	}
	if updated.Payload.Script != "echo hi" {
		t.Fatalf("script payload not persisted, got %q", updated.Payload.Script)
	}
}
func TestCronTool_UpdateScript(t *testing.T) {
	executor := &stubJobExecutor{}
	tool := newTestCronToolWithExecutorAndConfig(t, executor, config.DefaultConfig())
	job := addTestCronJob(t, tool, "upd", "cli", "direct", "")

	result := tool.Execute(WithToolContext(context.Background(), "cli", "direct"), map[string]any{
		"action": "update",
		"job_id": job.ID,
		"script": "echo updated",
	})
	if result.IsError {
		t.Fatalf("update script failed: %q", result.ForLLM)
	}

	updated, ok := tool.cronService.GetJob(job.ID)
	if !ok || updated.Payload.Script != "echo updated" {
		t.Fatalf("script not updated, got %+v", updated)
	}

	// Clearing the script with an empty string.
	result = tool.Execute(WithToolContext(context.Background(), "cli", "direct"), map[string]any{
		"action": "update",
		"job_id": job.ID,
		"script": "",
	})
	if result.IsError {
		t.Fatalf("clearing script failed: %q", result.ForLLM)
	}
	updated, _ = tool.cronService.GetJob(job.ID)
	if updated.Payload.Script != "" {
		t.Fatalf("script not cleared, got %q", updated.Payload.Script)
	}
}

// parseAddedJobID extracts the job id from a SilentResult "Cron job added: name (id: xyz)".
func parseAddedJobID(t *testing.T, text string) string {
	t.Helper()
	marker := "(id: "
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("no job id in %q", text)
	}
	rest := text[idx+len(marker):]
	if end := strings.Index(rest, ")"); end >= 0 {
		return rest[:end]
	}
	t.Fatalf("malformed add result %q", text)
	return ""
}
