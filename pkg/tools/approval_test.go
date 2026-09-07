package tools

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func newApprovalExecTool(t *testing.T, patterns []string) *ExecTool {
	t.Helper()
	cfg := &config.Config{}
	cfg.Tools.Exec.ApprovalPatterns = patterns
	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error: %v", err)
	}
	return tool
}

func TestExecToolNeedsApproval(t *testing.T) {
	tool := newApprovalExecTool(t, []string{`\bsudo\b`, `\bgit\s+push\b`})

	cases := []struct {
		command string
		want    bool
	}{
		{"sudo reboot", true},
		{"git push origin main", true},
		{"echo sudo | grep su", true}, // pattern matches anywhere in the line
		{"ls -la", false},
		{"go build ./...", false},
	}
	for _, tc := range cases {
		cmd, needed := tool.NeedsApproval(map[string]any{"action": "run", "command": tc.command})
		if needed != tc.want {
			t.Errorf("NeedsApproval(%q) = %v, want %v", tc.command, needed, tc.want)
		}
		if needed && cmd != tc.command {
			t.Errorf("NeedsApproval(%q) returned command %q", tc.command, cmd)
		}
	}

	// Non-run args without a command never require approval.
	if _, needed := tool.NeedsApproval(map[string]any{"action": "kill", "session_id": "s"}); needed {
		t.Error("args without a command must not require approval")
	}
}

func TestExecToolNeedsApprovalDisabledWithoutPatterns(t *testing.T) {
	tool := newApprovalExecTool(t, nil)
	if _, needed := tool.NeedsApproval(map[string]any{"command": "sudo reboot"}); needed {
		t.Error("no approval patterns configured means no approval gate")
	}
}

func TestExecToolInvalidApprovalPatternFailsClosed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.ApprovalPatterns = []string{`\b(?:sudo\b`}
	if _, err := NewExecToolWithConfig(t.TempDir(), false, cfg); err == nil {
		t.Fatal("invalid approval pattern must fail construction (fail-closed)")
	}
}
