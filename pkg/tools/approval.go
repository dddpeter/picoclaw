package tools

import (
	"regexp"
	"strings"
)

// ApprovalChecker is implemented by tools whose calls can require interactive
// human approval before execution. The agent's tool pipeline asks before
// executing; when approval is required it blocks on the channel's approval
// surface (see channels.ApprovalCapable) until the user decides or the
// request times out.
type ApprovalChecker interface {
	// NeedsApproval reports whether the given call arguments match the
	// tool's approval patterns, returning the human-facing target (the
	// command) when approval is required.
	NeedsApproval(args map[string]any) (string, bool)
}

// NeedsApproval implements ApprovalChecker for the exec tool: commands
// matching the configured approval patterns must be approved by a human in
// the chat before they run. Hard deny patterns always take precedence and
// are enforced separately inside Execute — approval never bypasses them.
func (t *ExecTool) NeedsApproval(args map[string]any) (string, bool) {
	if len(t.approvalPatterns) == 0 {
		return "", false
	}
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return "", false
	}
	lower := strings.ToLower(command)
	for _, re := range t.approvalPatterns {
		if re.MatchString(lower) {
			return command, true
		}
	}
	return "", false
}

// compileApprovalPatterns compiles the configured approval patterns; any
// invalid pattern is a construction error (fail-closed, same policy as deny
// patterns).
func compileApprovalPatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}
