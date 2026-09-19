//go:build windows

package lsp

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"unsafe"

	"golang.org/x/sys/windows"
)

var batCmdPattern = regexp.MustCompile(`(?i)\.(bat|cmd)$`)

func wrapNeeded(command string) bool { return batCmdPattern.MatchString(command) }

func comSpec() string {
	if v := os.Getenv("ComSpec"); v != "" {
		return v
	}
	return "cmd.exe"
}

// trackForTermination assigns the freshly started process to a job object
// carrying JOB_OBJECT_LIMIT_KILL_ON_CLOSE: unlike exec-tool daemons, LSP
// servers must never outlive the gateway — if picoclaw crashes without a
// graceful shutdown, the OS closing our job handle reaps the whole tree
// (cmd.exe shims and npx wrappers included). Failure is non-fatal; the
// taskkill fallback keeps termination working.
func trackForTermination(cmd *exec.Cmd) func() {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return func() { _ = cmd.Process.Kill() }
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return treeKillFallback(cmd)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, _ = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	proc, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return treeKillFallback(cmd)
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return treeKillFallback(cmd)
	}
	return func() {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
		_ = cmd.Process.Kill() // just-spawned escapee
	}
}

func treeKillFallback(cmd *exec.Cmd) func() {
	return func() {
		if cmd.Process == nil {
			return
		}
		_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprint(cmd.Process.Pid)).Run()
		_ = cmd.Process.Kill()
	}
}
