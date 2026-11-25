// Package providers is a compatibility facade: re-exports pkg/providers/cli types and constructors under the historic root-package import path. The implementations live in pkg/providers/cli — new code should import that package directly; this facade exists so external configs / plugins with the old import path keep compiling and is not extended with new symbols.
package providers

import (
	cliprovider "github.com/sipeed/picoclaw/pkg/providers/cli"
)

type (
	ClaudeCliProvider     = cliprovider.ClaudeCliProvider
	CodexCliProvider      = cliprovider.CodexCliProvider
	GitHubCopilotProvider = cliprovider.GitHubCopilotProvider
)

func NewClaudeCliProvider(workspace string) *ClaudeCliProvider {
	return cliprovider.NewClaudeCliProvider(workspace)
}

func NewCodexCliProvider(workspace string) *CodexCliProvider {
	return cliprovider.NewCodexCliProvider(workspace)
}

func NewGitHubCopilotProvider(uri string, connectMode string, model string) (*GitHubCopilotProvider, error) {
	return cliprovider.NewGitHubCopilotProvider(uri, connectMode, model)
}

func NormalizeToolCall(tc ToolCall) ToolCall {
	return cliprovider.NormalizeToolCall(tc)
}
