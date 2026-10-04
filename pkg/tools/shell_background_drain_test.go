//go:build windows

package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestShellTool_BackgroundDaemonHoldingPipesCompletes pins the non-PTY
// background path's drain semantics (T4): a shell that starts a holder
// process inheriting the output pipes and then exits must still produce a
// "done" session within the bounded grace period — never wedged in
// "running" — with BOTH the shell's stdout and the holder's stderr visible
// (the concurrent-drain fix), while the holder itself survives as an
// intentional daemon. The synchronous twin of this shape is covered by
// shell_orphan_deadlock_test.go; this file covers the divergent background
// path (os.Process.Wait + 5s grace + reader close), which previously had no
// coverage at all.
func TestShellTool_BackgroundDaemonHoldingPipesCompletes(t *testing.T) {
	tmp := t.TempDir()
	holderPIDPath := filepath.Join(tmp, "holder.pid")

	tool, err := NewExecTool(tmp, false)
	if err != nil {
		t.Fatalf("unable to configure exec tool: %v", err)
	}
	tool.SetTimeout(60 * time.Second)

	// The holder prints one stderr line (concurrent-drain visibility) and
	// then holds both pipe write ends for two minutes.
	script := strings.Join([]string{
		`$psi = New-Object System.Diagnostics.ProcessStartInfo`,
		`$psi.FileName = 'powershell.exe'`,
		`$psi.Arguments = '-NoProfile -NonInteractive -Command "[Console]::Error.WriteLine(''holder stderr alive''); Start-Sleep -Seconds 120"'`,
		`$psi.UseShellExecute = $false`,
		`$p = [System.Diagnostics.Process]::Start($psi)`,
		`[IO.File]::WriteAllText('` + holderPIDPath + `', $p.Id.ToString())`,
		`Write-Output 'shell stdout done'`,
	}, "\n")

	start := time.Now()
	runResult := tool.Execute(t.Context(), map[string]any{
		"action":     "run",
		"command":    script,
		"background": "true",
	})
	if runResult.IsError {
		t.Fatalf("background run failed: %s", runResult.ForLLM)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("background run must return immediately, took %s", elapsed)
	}
	var resp ExecResponse
	if err := json.Unmarshal([]byte(runResult.ForLLM), &resp); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if resp.SessionID == "" {
		t.Fatalf("no session id in %s", runResult.ForLLM)
	}

	holderPID := 0
	spawnDeadline := time.Now().Add(20 * time.Second)
	for holderPID == 0 {
		if data, err := os.ReadFile(holderPIDPath); err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "\ufeff"))); perr == nil {
				holderPID = pid
				break
			}
		}
		if time.Now().After(spawnDeadline) {
			t.Fatalf("holder never spawned within 20s")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(holderPID)).Run()
	})

	// The shell exits ~immediately; the session must reach "done" through
	// the grace-period abandonment (5s) even though the holder keeps the
	// pipes open for 120s. 30s is a generous bound for slow CI machines.
	doneDeadline := time.Now().Add(30 * time.Second)
	for {
		pollResult := tool.Execute(t.Context(), map[string]any{
			"action":    "poll",
			"sessionId": resp.SessionID,
		})
		if pollResult.IsError {
			t.Fatalf("poll failed: %s", pollResult.ForLLM)
		}
		var pollResp ExecResponse
		if err := json.Unmarshal([]byte(pollResult.ForLLM), &pollResp); err != nil {
			t.Fatalf("decode poll response: %v", err)
		}
		if pollResp.Status == "done" || pollResp.Status == "error" {
			if pollResp.Status != "done" {
				t.Fatalf("session status = %q, want done", pollResp.Status)
			}
			if pollResp.ExitCode != 0 {
				t.Fatalf("shell exited cleanly but ExitCode = %d", pollResp.ExitCode)
			}
			break
		}
		if time.Now().After(doneDeadline) {
			t.Fatalf("session never completed; holder pid %d still holds the pipes — background path wedged in %q",
				holderPID, pollResp.Status)
		}
		time.Sleep(250 * time.Millisecond)
	}

	// Both streams must be visible: stdout from the shell, stderr written
	// by the holder while still holding the pipes (the concurrent drain).
	readResult := tool.Execute(t.Context(), map[string]any{
		"action":    "read",
		"sessionId": resp.SessionID,
	})
	if readResult.IsError {
		t.Fatalf("read failed: %s", readResult.ForLLM)
	}
	var readResp ExecResponse
	if err := json.Unmarshal([]byte(readResult.ForLLM), &readResp); err != nil {
		t.Fatalf("decode read response: %v", err)
	}
	if !strings.Contains(readResp.Output, "shell stdout done") {
		t.Fatalf("shell stdout missing from session output: %s", readResp.Output)
	}
	if !strings.Contains(readResp.Output, "holder stderr alive") {
		t.Fatalf("holder stderr missing from session output (concurrent drain broken?): %s", readResp.Output)
	}

	// Intentional daemons survive the session completing.
	if !windowsProcessExists(holderPID) {
		t.Fatalf("holder pid %d died when the session completed; surviving daemons must not be killed", holderPID)
	}
}
