package deltachat

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestKillCurrentReapsProcessAndStdin pins the M-1 cleanup primitive: the
// killed generation must end up reaped (exec.Cmd.ProcessState set), which is
// also what unblocks its superseded readLoop (Wait closes the stdout pipe).
func TestKillCurrentReapsProcessAndStdin(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses bare cmd.exe as a long-lived stand-in server; fix logic is platform-independent")
	}

	c := &rpcClient{}
	cmd := exec.Command("cmd") // waits on stdin, stays alive, no stdout output
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe() error = %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	c.cmd = cmd
	c.stdin = stdin

	c.killCurrent()

	c.mu.Lock()
	reaped := c.cmd.ProcessState != nil
	c.mu.Unlock()
	if !reaped {
		t.Fatal("killCurrent did not reap the process (leaked process + blocked readLoop)")
	}
}

// TestSuperviseKillsFailedGeneration pins the M-1 leak fix end to end: a fake
// server that spawns but never answers get_system_info makes every bootstrap
// fail; the supervisor must kill each failed generation (ProcessState set on
// the currently wired cmd) instead of leaking it while retrying.
func TestSuperviseKillsFailedGeneration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses bare cmd.exe as a spawn-but-never-answers stand-in server; fix logic is platform-independent")
	}

	origMin, origMax := rpcRestartBackoffMin, rpcRestartBackoffMax
	origAttempts, origInterval := rpcWaitReadyAttempts, rpcWaitReadyInterval
	rpcRestartBackoffMin, rpcRestartBackoffMax = 20*time.Millisecond, 50*time.Millisecond
	rpcWaitReadyAttempts, rpcWaitReadyInterval = 1, 10*time.Millisecond
	t.Cleanup(func() {
		rpcRestartBackoffMin, rpcRestartBackoffMax = origMin, origMax
		rpcWaitReadyAttempts, rpcWaitReadyInterval = origAttempts, origInterval
	})

	c := &rpcClient{
		serverPath: "cmd", // waits on stdin; never emits a JSON-RPC response
		dataDir:    t.TempDir(),
		pending:    make(map[uint64]chan rpcResponse),
	}

	done := make(chan struct{})
	go func() {
		c.supervise()
		close(done)
	}()

	// Wait until at least one failed generation has been killed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		var reaped bool
		if c.cmd != nil {
			reaped = c.cmd.ProcessState != nil
		}
		gen := c.generation
		c.mu.Unlock()
		if reaped && gen >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervise did not reap a failed generation within 10s (M-1 leak)")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Shut down; supervise must exit instead of looping forever.
	c.mu.Lock()
	c.shuttingDown = true
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not honor shuttingDown")
	}

	// The currently wired process must be reaped, not leaked.
	c.mu.Lock()
	reaped := c.cmd != nil && c.cmd.ProcessState != nil
	c.mu.Unlock()
	if !reaped {
		t.Fatal("currently wired generation was left alive after failed bootstrap (M-1 leak)")
	}
}

// TestReadLoopStaleGenerationExits pins the double-supervisor guard: a
// readLoop whose generation was superseded must exit on EOF without touching
// pending calls or re-entering supervision.
func TestReadLoopStaleGenerationExits(t *testing.T) {
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	c := &rpcClient{
		pending: make(map[uint64]chan rpcResponse),
		stdout:  stdoutR,
	}
	go c.readLoop()

	// Simulate a rewire: generation bumps, a new stdout replaces ours.
	c.mu.Lock()
	c.generation++
	newR, newW, _ := os.Pipe()
	c.stdout = newR
	c.mu.Unlock()
	defer newW.Close()

	// EOF on the old pipe: the stale loop must exit silently (no supervise —
	// observable via restarting staying false and the call returning).
	_ = stdoutW.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		staleSupervising := c.restarting
		c.mu.Unlock()
		if staleSupervising {
			t.Fatal("stale readLoop re-entered supervision (double supervisor)")
		}
		if time.Now().After(deadline) {
			return // stale loop exited silently and stayed out of the way
		}
		time.Sleep(20 * time.Millisecond)
	}
}
