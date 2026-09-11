//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// windowsProcessExists reports whether a process with the given PID is
// currently running. tasklist prints localized text when nothing matches, so
// existence is detected by the quoted CSV PID field — never by a bare
// substring, which would false-positive on memory sizes like "1,024 K".
func windowsProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"`+strconv.Itoa(pid)+`",`)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "\ufeff")))
	if err != nil {
		t.Fatalf("failed to parse pid from %s (%q): %v", path, string(data), err)
	}
	return pid
}

// TestShellTool_CancelReturnsDespiteOrphanedPipeHolder reproduces the Windows
// deadlock behind the "agent-browser 验证" incident: once a hung command was
// stopped, the session stayed wedged and only a gateway restart recovered it.
//
// The mechanism, mirroring npm/.cmd shim chains that leave daemons behind:
//
//  1. The shell spawns a holder process that inherits its stdout/stderr pipe
//     write handles (ProcessStartInfo with UseShellExecute=false), then exits.
//  2. The holder is now orphaned: taskkill /T rooted at the dead shell PID
//     cannot reach it, so the output pipes stay open.
//  3. The context is canceled (the /stop path: turnCtx cancel cascades into
//     runSync's cmdCtx).
//
// Desired behavior: Execute must return within a bounded grace period after
// cancellation. Buggy behavior: after terminateProcessTree, runSync blocks
// forever on `err = <-done` because cmd.Wait() needs pipe EOF, which an
// orphaned handle holder never produces. A turn stuck this way never unwinds:
// it keeps the model-state read lock and its activeTurnStates registration,
// /stop keeps replying "Task stopped", and every new message queues forever.
func TestShellTool_CancelReturnsDespiteOrphanedPipeHolder(t *testing.T) {
	tmp := t.TempDir()
	holderPIDPath := filepath.Join(tmp, "holder.pid")
	shellPIDPath := filepath.Join(tmp, "shell.pid")

	// Long tool timeout: cancellation must come from the caller context,
	// mirroring /stop canceling the turn context mid-execution.
	tool, err := NewExecTool(tmp, false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	tool.SetTimeout(60 * time.Second)

	script := strings.Join([]string{
		`$psi = New-Object System.Diagnostics.ProcessStartInfo`,
		`$psi.FileName = 'ping.exe'`,
		`$psi.Arguments = '-n 120 127.0.0.1'`,
		`$psi.UseShellExecute = $false`,
		`$p = [System.Diagnostics.Process]::Start($psi)`,
		`[IO.File]::WriteAllText('` + holderPIDPath + `', $p.Id.ToString())`,
		`[IO.File]::WriteAllText('` + shellPIDPath + `', $PID.ToString())`,
	}, "\n")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	resultCh := make(chan *ToolResult, 1)
	go func() {
		resultCh <- tool.Execute(ctx, map[string]any{
			"action":  "run",
			"command": script,
		})
	}()

	// Wait for the shell to spawn the holder and report both PIDs.
	spawnDeadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(shellPIDPath); err == nil {
			break
		}
		if time.Now().After(spawnDeadline) {
			t.Fatalf("shell did not report its pid within 20s; holder never spawned")
		}
		select {
		case res := <-resultCh:
			t.Fatalf("Execute returned before the holder was spawned: %s", res.ForLLM)
		case <-time.After(100 * time.Millisecond):
		}
	}

	holderPID := readPIDFile(t, holderPIDPath)
	t.Cleanup(func() {
		// Kill the holder so a wedged goroutine can observe pipe EOF and the
		// test binary never leaves a stray ping behind. Best-effort: on the
		// buggy code this is what finally unblocks the abandoned goroutine.
		cancel()
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(holderPID)).Run()
		select {
		case <-resultCh:
		case <-time.After(5 * time.Second):
		}
	})

	// Wait until the shell has exited, so the holder is orphaned and outside
	// any taskkill /T tree rooted at the shell PID.
	shellPID := readPIDFile(t, shellPIDPath)
	exitDeadline := time.Now().Add(20 * time.Second)
	for windowsProcessExists(shellPID) && time.Now().Before(exitDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if windowsProcessExists(shellPID) {
		t.Fatalf("shell %d still running after 20s", shellPID)
	}

	// Precondition: with the shell dead but the holder keeping the pipe write
	// handles open, cmd.Wait() cannot see EOF — Execute must not have returned.
	select {
	case res := <-resultCh:
		t.Fatalf("precondition failed: holder did not keep the output pipes open (Execute returned: %s)", res.ForLLM)
	case <-time.After(2 * time.Second):
	}

	cancel()

	// The verdict: bounded return after cancellation is the desired behavior.
	// On the current code this fails after 12s — the deadlock.
	select {
	case <-resultCh:
	case <-time.After(12 * time.Second):
		t.Fatalf("deadlock reproduced: Execute did not return within 12s of cancellation; orphaned holder pid %d still holds the output pipes", holderPID)
	}

	// The cancellation must actually kill the handle holder, not just abandon
	// waiting on it — otherwise orphaned daemons/browsers keep burning machine
	// resources until their own lifetime expires.
	killDeadline := time.Now().Add(5 * time.Second)
	for windowsProcessExists(holderPID) && time.Now().Before(killDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if windowsProcessExists(holderPID) {
		t.Fatalf("holder pid %d survived cancellation; the kill path leaves orphans behind", holderPID)
	}
}

// TestShellTool_DaemonHoldingPipesReturnsPromptly: a command that starts a
// long-lived child inheriting the output pipes (the agent-browser daemon
// pattern) and then exits cleanly must not wedge the exec call until the
// child dies. Desired behavior: Execute returns promptly as a success with
// the output collected so far plus a note about the abandoned pipe wait.
func TestShellTool_DaemonHoldingPipesReturnsPromptly(t *testing.T) {
	tmp := t.TempDir()
	holderPIDPath := filepath.Join(tmp, "holder.pid")

	tool, err := NewExecTool(tmp, false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	tool.SetTimeout(60 * time.Second)

	script := strings.Join([]string{
		`$psi = New-Object System.Diagnostics.ProcessStartInfo`,
		`$psi.FileName = 'ping.exe'`,
		`$psi.Arguments = '-n 120 127.0.0.1'`,
		`$psi.UseShellExecute = $false`,
		`$p = [System.Diagnostics.Process]::Start($psi)`,
		`[IO.File]::WriteAllText('` + holderPIDPath + `', $p.Id.ToString())`,
		`Write-Output 'shell done'`,
	}, "\n")

	resultCh := make(chan *ToolResult, 1)
	go func() {
		resultCh <- tool.Execute(t.Context(), map[string]any{
			"action":  "run",
			"command": script,
		})
	}()

	holderPID := 0
	spawnDeadline := time.Now().Add(20 * time.Second)
	for {
		if data, err := os.ReadFile(holderPIDPath); err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "\ufeff"))); perr == nil {
				holderPID = pid
				break
			}
		}
		if time.Now().After(spawnDeadline) {
			t.Fatalf("holder never spawned within 20s")
		}
		select {
		case res := <-resultCh:
			t.Fatalf("Execute returned before the holder was spawned: %s", res.ForLLM)
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(holderPID)).Run()
		select {
		case <-resultCh:
		case <-time.After(5 * time.Second):
		}
	})

	// The shell exits immediately after spawning the holder; the call must
	// not wait for the holder's 120s lifetime.
	select {
	case res := <-resultCh:
		if res.IsError {
			t.Fatalf("expected success (clean exit), got error: %s", res.ForLLM)
		}
		if !strings.Contains(res.ForLLM, "shell done") {
			t.Fatalf("expected collected output, got: %s", res.ForLLM)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("deadlock reproduced: Execute still waiting on pipes held by daemon pid %d 15s after the shell exited", holderPID)
	}

	// The daemon must survive the exec call's cleanup: intentionally
	// long-lived children (agent-browser daemon, browsers) are the reason
	// this command pattern exists at all.
	if !windowsProcessExists(holderPID) {
		t.Fatalf("holder pid %d died after a clean command completion; surviving daemons must not be killed", holderPID)
	}
}
