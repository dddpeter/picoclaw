//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"sync"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// jobObjects tracks the job object assigned to each running exec command so
// termination can kill the whole tree with one syscall. taskkill /T walks
// the PPID chain and misses orphans whose intermediate parent already
// exited (dead PIDs are not in the snapshot) or after PID reuse.
var jobObjects sync.Map // *exec.Cmd -> windows.Handle

func prepareCommandForTermination(cmd *exec.Cmd) {
	// no-op on Windows: a job object can only be attached after Start.
}

// trackProcessTree assigns the freshly started process to a job object so a
// later terminateProcessTree kills all of its descendants at once. The job
// is created WITHOUT JOB_OBJECT_LIMIT_KILL_ON_CLOSE on purpose: closing our
// handle after a normal completion must never harm children that are meant
// to outlive the command (agent-browser daemon, browsers).
//
// Failure is non-fatal: termination falls back to the taskkill path.
func trackProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		logger.DebugCF("shell", "job object creation failed; falling back to taskkill termination",
			map[string]any{"pid": cmd.Process.Pid, "error": err.Error()})
		return
	}
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		// E.g. the parent already sits in a job that forbids nesting —
		// uncommon; the taskkill fallback keeps today's behavior.
		logger.DebugCF("shell", "job object assignment failed; falling back to taskkill termination",
			map[string]any{"pid": cmd.Process.Pid, "error": err.Error()})
		_ = windows.CloseHandle(job)
		return
	}
	jobObjects.Store(cmd, job)
}

// releaseProcessTree drops the job handle once the command is done. Safe
// for surviving children: the job carries no KILL_ON_JOB_CLOSE flag.
func releaseProcessTree(cmd *exec.Cmd) {
	if v, ok := jobObjects.LoadAndDelete(cmd); ok {
		if job, ok := v.(windows.Handle); ok {
			_ = windows.CloseHandle(job)
		}
	}
}

func terminateProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}

	// Preferred: terminate the job object — kills every descendant in one
	// call regardless of dead intermediates or PID reuse in the tree.
	if v, ok := jobObjects.LoadAndDelete(cmd); ok {
		if job, ok := v.(windows.Handle); ok {
			if err := windows.TerminateJobObject(job, 1); err != nil {
				logger.DebugCF("shell", "TerminateJobObject failed; falling back to taskkill",
					map[string]any{"pid": pid, "error": err.Error()})
			}
			_ = windows.CloseHandle(job)
			// The job covered the tree; still kill the direct process in
			// case assignment raced a just-spawned escapee.
			_ = cmd.Process.Kill()
			return nil
		}
	}

	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	_ = cmd.Process.Kill()
	return nil
}
