package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

// execLikeTool implements tools.ApprovalChecker (as *tools.ExecTool does);
// plainTool does not.
type execLikeTool struct{}

func (execLikeTool) Name() string        { return "exec" }
func (execLikeTool) Description() string { return "fake exec for approval gate tests" }
func (execLikeTool) Parameters() map[string]any {
	return nil
}
func (execLikeTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return &tools.ToolResult{ForLLM: "ok"}
}

func (execLikeTool) NeedsApproval(args map[string]any) (string, bool) {
	command, _ := args["command"].(string)
	return command, strings.HasPrefix(command, "sudo")
}

type plainTool struct{}

func (plainTool) Name() string        { return "plain" }
func (plainTool) Description() string { return "fake plain tool" }
func (plainTool) Parameters() map[string]any {
	return nil
}
func (plainTool) Execute(_ context.Context, _ map[string]any) *tools.ToolResult {
	return &tools.ToolResult{ForLLM: "ok"}
}

// TestToolNeedsApprovalRoutesThroughRegistry ensures the registry lookup and
// ApprovalChecker assertion used by the pipeline gate work end to end.
func TestToolNeedsApprovalRoutesThroughRegistry(t *testing.T) {
	ts := &turnState{}
	ts.agent = &AgentInstance{ID: "a1", Tools: tools.NewToolRegistry()}
	ts.agent.Tools.Register(execLikeTool{})
	ts.agent.Tools.Register(plainTool{})

	p := &Pipeline{}
	command, needed := p.toolNeedsApproval(ts, "exec", map[string]any{"command": "sudo reboot"})
	if !needed || command != "sudo reboot" {
		t.Fatalf("expected approval required, got needed=%v command=%q", needed, command)
	}
	if _, needed := p.toolNeedsApproval(ts, "exec", map[string]any{"command": "ls"}); needed {
		t.Fatal("non-matching command must not require approval")
	}
	if _, needed := p.toolNeedsApproval(ts, "plain", map[string]any{"command": "sudo reboot"}); needed {
		t.Fatal("non-ApprovalChecker tool must never require approval")
	}
}

// TestRequestToolApprovalFailsClosed verifies the gate denies when no channel
// manager is available instead of executing silently.
func TestRequestToolApprovalFailsClosed(t *testing.T) {
	ts := &turnState{channel: "feishu", chatID: "c1"}
	ts.agent = &AgentInstance{ID: "a1"}

	approved, reason := (&Pipeline{}).requestToolApproval(context.Background(), ts, "exec", "sudo reboot")
	if approved {
		t.Fatal("missing channel manager must deny")
	}
	if !strings.Contains(reason, "unavailable") {
		t.Fatalf("unexpected reason: %q", reason)
	}
}
