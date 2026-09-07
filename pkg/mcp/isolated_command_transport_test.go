package mcp

import (
	"context"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestIsolatedTransportHelper is not a real test: it is the re-exec entry
// point for the transport tests below, mirroring the classic TestHelperProcess
// idiom so fake stdio servers work identically on every platform.
func TestIsolatedTransportHelper(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a != "--" || i+1 >= len(args) {
			continue
		}
		switch args[i+1] {
		case "exit7":
			os.Exit(7)
		case "cat":
			_, _ = io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		case "ignore-term-sleep":
			signal.Reset(syscall.SIGTERM)
			signal.Ignore(syscall.SIGTERM)
			time.Sleep(60 * time.Second)
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func helperTransport(mode string, terminateDuration time.Duration) (*isolatedCommandTransport, *exec.Cmd) {
	cmd := exec.Command(os.Args[0], "-test.run=TestIsolatedTransportHelper$", "--", mode)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return &isolatedCommandTransport{Command: cmd, TerminateDuration: terminateDuration}, cmd
}

// TestIsolatedCommandTransport_ReapsWithoutClose is the zombie regression
// test: a stdio server that exits on its own must be reaped even when Close
// is never called (dead session, no further tool calls, no reconnect).
func TestIsolatedCommandTransport_ReapsWithoutClose(t *testing.T) {
	transport, cmd := helperTransport("exit7", 5*time.Second)
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	rwc := conn.(*isolatedIOConn).rwc.(*isolatedPipeRWC)

	select {
	case err := <-rwc.waitResult:
		var exitErr *exec.ExitError
		if !asExitError(err, &exitErr) {
			t.Fatalf("want *exec.ExitError, got %v", err)
		}
		if code := exitErr.ExitCode(); code != 7 {
			t.Fatalf("want exit code 7, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child was not reaped without Close")
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("ProcessState not populated after Wait: %v", cmd.ProcessState)
	}
}

// TestIsolatedCommandTransport_CloseOnExitedProcess ensures Close reports the
// original exit status instead of hanging on a child that already died.
func TestIsolatedCommandTransport_CloseOnExitedProcess(t *testing.T) {
	transport, _ := helperTransport("exit7", 5*time.Second)
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	err = conn.Close()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Close took %v on an already-exited child", elapsed)
	}
	var exitErr *exec.ExitError
	if !asExitError(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %v", err)
	}
	if code := exitErr.ExitCode(); code != 7 {
		t.Fatalf("want exit code 7, got %d", code)
	}
}

// TestIsolatedCommandTransport_CloseGracefulStdinExit covers the normal
// shutdown path: the server exits on stdin EOF and Close returns nil.
func TestIsolatedCommandTransport_CloseGracefulStdinExit(t *testing.T) {
	transport, _ := helperTransport("cat", 5*time.Second)
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestIsolatedCommandTransport_CloseEscalatesToKill verifies the SIGTERM ->
// SIGKILL escalation still fires when the child ignores SIGTERM.
func TestIsolatedCommandTransport_CloseEscalatesToKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM semantics are Unix-only")
	}
	transport, _ := helperTransport("ignore-term-sleep", 300*time.Millisecond)
	conn, err := transport.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	start := time.Now()
	err = conn.Close()
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Fatalf("Close returned before terminate duration (%v), escalation logic broken", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("Close took %v, SIGKILL escalation failed", elapsed)
	}
	var exitErr *exec.ExitError
	if !asExitError(err, &exitErr) {
		t.Fatalf("want *exec.ExitError from killed child, got %v", err)
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}
