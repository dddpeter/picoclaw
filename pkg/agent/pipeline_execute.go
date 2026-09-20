// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

func toolErrorSummary(result *tools.ToolResult) string {
	if result == nil || !result.IsError {
		return ""
	}
	content := strings.TrimSpace(result.ContentForLLM())
	if content == "" && result.Err != nil {
		content = strings.TrimSpace(result.Err.Error())
	}
	return utils.Truncate(content, 200)
}

func inferSkillNamesFromToolCall(ts *turnState, toolName string, toolArgs map[string]any) []string {
	if ts == nil || toolName != "read_file" {
		return nil
	}

	rawPath, ok := toolArgs["path"].(string)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil
	}

	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(ts.workspace, cleanPath)
	}
	if filepath.Base(cleanPath) != "SKILL.md" {
		return nil
	}

	var roots []string
	if ts.agent != nil && ts.agent.ContextBuilder != nil {
		roots = ts.agent.ContextBuilder.skillRoots()
	}
	if len(roots) == 0 && strings.TrimSpace(ts.workspace) != "" {
		roots = []string{filepath.Join(ts.workspace, "skills")}
	}

	found := make(map[string]struct{})
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 || parts[1] != "SKILL.md" {
			continue
		}

		skillName := strings.TrimSpace(parts[0])
		if skillName == "" {
			continue
		}
		if ts.agent != nil && ts.agent.ContextBuilder != nil {
			if canonical, ok := ts.agent.ContextBuilder.ResolveSkillName(skillName); ok {
				skillName = canonical
			}
		}
		found[skillName] = struct{}{}
	}

	if len(found) == 0 {
		return nil
	}

	names := make([]string, 0, len(found))
	for skillName := range found {
		names = append(names, skillName)
	}
	sort.Strings(names)
	return names
}

// toolStepKind labels a tool execution for streaming panels. MCP tools are
// registered with an "mcp_<server>_<tool>" name, so the prefix reliably
// identifies calls routed to MCP servers.
func toolStepKind(toolName string) string {
	if strings.HasPrefix(toolName, "mcp_") {
		return bus.ToolStepKindMCP
	}
	return bus.ToolStepKindTool
}

// ExecuteTools executes the tool loop, handling BeforeTool/ApproveTool/AfterTool hooks,
// tool execution with async callbacks, media delivery, and steering injection.
// Returns ToolControl indicating what the coordinator should do next:
//   - ToolControlContinue: all tool results handled, pendingMessages or steering exists, continue turn
//   - ToolControlBreak: tool loop exited, proceed to coordinator's hardAbort/finalContent/finalize
//
// The hook-decision and result-handling internals live in pipeline_execute_loop.go;
// this function owns the orchestration order.
func (p *Pipeline) ExecuteTools(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) ToolControl {
	al := p.al
	ts.setPhase(TurnPhaseTools)

	ls := &toolLoopState{
		al:                 al,
		ts:                 ts,
		exec:               exec,
		ctx:                ctx,
		turnCtx:            turnCtx,
		iteration:          iteration,
		toolCalls:          exec.normalizedToolCalls,
		messages:           exec.messages,
		handledAttachments: make([]providers.Attachment, 0),
	}

	// This iteration continues with tool calls, so the streamed answer slot
	// will be overwritten by the next LLM iteration. Archive the mid-turn
	// prose on the process panel before that happens — otherwise it is
	// visible only while streaming and lost in the final card.
	if exec.streamingPublisher != nil {
		if content := strings.TrimSpace(exec.response.Content); content != "" {
			exec.streamingPublisher.AppendToolStep(turnCtx, bus.ToolStep{
				Kind:   bus.ToolStepKindText,
				Result: utils.Truncate(content, 400),
			})
		}
	}

	// A "length" finish means the model output was cut off by the token
	// limit. Streamed tool-call arguments can then parse as valid JSON while
	// silently missing content, so executing them is unsafe — fail every
	// call from this batch and let the model re-issue them.
	truncatedByTokenLimit := ts.GetLastFinishReason() == "length"

toolLoop:
	for i, tc := range ls.toolCalls {
		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak
		}
		ls.index = i
		toolName := tc.Name
		toolArgs := cloneStringAnyMap(tc.Arguments)

		if truncatedByTokenLimit {
			p.appendDeniedToolResult(ls, tc, toolName, fmt.Sprintf(
				"Tool call %q was not executed: the model response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.",
				toolName,
			))
			continue
		}

		if p.denyToolByTurnProfile(ls, tc, toolName) {
			continue
		}

		if al.hooks != nil {
			action, nextName, nextArgs := p.handleBeforeToolDecision(ls, tc, toolName, toolArgs)
			toolName = nextName
			toolArgs = nextArgs
			switch action {
			case toolLoopNext:
				continue
			case toolLoopBreak:
				break toolLoop
			case toolLoopAbort:
				return ToolControlBreak
			}
		}

		if al.hooks != nil {
			approval := al.hooks.ApproveTool(turnCtx, &ToolApprovalRequest{
				Meta:      ts.eventMeta("runTurn", "turn.tool.approve"),
				Context:   cloneTurnContext(ts.turnCtx),
				Tool:      toolName,
				Arguments: toolArgs,
			})
			if !approval.Approved {
				p.appendDeniedToolResult(ls, tc, toolName,
					hookDeniedToolContent("Tool execution denied by approval hook", approval.Reason))
				continue
			}
		}

		if p.denyToolByTurnProfile(ls, tc, toolName) {
			continue
		}

		toolResult, toolDuration := p.runToolInvocation(ls, tc, toolName, toolArgs)

		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak
		}

		if al.hooks != nil {
			action, nextName, nextResult := p.handleAfterToolDecision(ls, toolName, toolArgs, toolResult, toolDuration)
			toolName = nextName
			toolResult = nextResult
			if action == toolLoopAbort {
				return ToolControlBreak
			}
		}

		p.handleToolResult(ls, tc, toolName, toolArgs, toolResult, toolDuration)

		if p.checkTurnCheckpoint(ls, false) {
			break toolLoop
		}
		p.drainPendingSubTurnResults(ls, false)
	}

	return p.finishToolExecution(ls)
}
