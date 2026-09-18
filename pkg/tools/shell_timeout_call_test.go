package tools

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sleepCmd returns a cross-platform "sleep N seconds" shell command. runSync
// executes via powershell on Windows and sh elsewhere.
func sleepCmd(secs int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + strconv.Itoa(secs)
	}
	return "sleep " + strconv.Itoa(secs)
}

func TestExecTool_TimeoutErrorSuggestsBackground(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	tool.SetTimeout(1 * time.Second)

	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": sleepCmd(5),
	})
	if !result.IsError {
		t.Fatalf("expected timeout error, got success: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "background=true") {
		t.Fatalf("timeout message should suggest background=true, got: %s", result.ForLLM)
	}
}

func TestExecTool_PerCallTimeoutOverridesDefault(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	tool.SetTimeout(30 * time.Second) // generous default; per-call must cut it short

	start := time.Now()
	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": sleepCmd(5),
		"timeout": float64(1),
	})
	elapsed := time.Since(start)
	if !result.IsError {
		t.Fatalf("expected per-call timeout to kill the command, got success: %s", result.ForLLM)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("per-call timeout=1s ignored; elapsed=%v", elapsed)
	}
}

func TestExecTool_PerCallTimeoutZeroMeansNoTimeout(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	tool.SetTimeout(1 * time.Second) // default would kill it

	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": sleepCmd(2),
		"timeout": float64(0),
	})
	if result.IsError {
		t.Fatalf("timeout=0 should disable timeout, got error: %s", result.ForLLM)
	}
}

func TestExecTool_NoTimeoutArgKeepsConfigDefault(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewExecTool: %v", err)
	}
	tool.SetTimeout(1 * time.Second)

	result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": sleepCmd(3),
	})
	if !result.IsError {
		t.Fatalf("expected config-default timeout to apply, got success")
	}
}

func TestResolveRunTimeout(t *testing.T) {
	const def = 60 * time.Second
	cases := []struct {
		name string
		args map[string]any
		want time.Duration
	}{
		{"absent falls back to default", nil, def},
		{"float override", map[string]any{"timeout": float64(5)}, 5 * time.Second},
		{"int override", map[string]any{"timeout": 7}, 7 * time.Second},
		{"int64 override", map[string]any{"timeout": int64(3)}, 3 * time.Second},
		{"zero disables", map[string]any{"timeout": float64(0)}, 0},
		{"negative ignored", map[string]any{"timeout": float64(-1)}, def},
		{"string ignored", map[string]any{"timeout": "10"}, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveRunTimeout(def, tc.args); got != tc.want {
				t.Fatalf("resolveRunTimeout(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
