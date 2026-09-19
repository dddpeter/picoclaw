//go:build !windows

package lsp

import (
	"os/exec"
	"syscall"
)

func wrapNeeded(command string) bool { return false }

func comSpec() string { return "" }

// trackForTermination puts the child in its own process group so shutdown
// can signal the whole tree; LSP servers are direct children in practice,
// so Kill on the direct pid is the primary path.
func trackForTermination(cmd *exec.Cmd) func() {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.Process == nil {
		return func() {}
	}
	pid := cmd.Process.Pid
	return func() {
		// Negative pid signals the process group (children included).
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
	}
}
