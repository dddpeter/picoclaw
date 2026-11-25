package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// syncOutputRetainLimit caps how much output runSync retains in memory per
// stream (head + tail around a marker). Background sessions already cap
// their output buffer; the synchronous path had no bound, so a chatty
// command (`yes`, a runaway loop) accumulated unbounded bytes.Buffer data
// within its timeout window — enough to OOM the gateway. Full-fidelity
// capture of huge outputs remains available via shell redirection.
const syncOutputRetainLimit = 4 * 1024 * 1024

// cappedOutputBuffer is an io.Writer that retains at most limit bytes: the
// first limit/2 bytes and a rolling tail of the last limit/2 bytes, with a
// truncation marker in between. Concurrency-safe (the reader goroutines of
// the shell tool may share one instance).
type cappedOutputBuffer struct {
	limit   int
	written int64
	head    []byte
	tail    []byte
	tailPos int
	tailLen int
	dropped int64
}

func newCappedOutputBuffer(limit int) *cappedOutputBuffer {
	if limit < 64 {
		limit = 64
	}
	return &cappedOutputBuffer{limit: limit, tail: make([]byte, limit/2)}
}

func (b *cappedOutputBuffer) Write(p []byte) (int, error) {
	total := len(p)
	b.written += int64(total)
	half := b.limit / 2
	if len(b.head) < half {
		space := half - len(b.head)
		if len(p) <= space {
			b.head = append(b.head, p...)
			return total, nil
		}
		b.head = append(b.head, p[:space]...)
		p = p[space:]
	}
	if len(b.tail) == 0 {
		return total, nil
	}
	b.dropped += int64(len(p))
	for len(p) > 0 {
		n := copy(b.tail[b.tailPos:], p)
		b.tailPos = (b.tailPos + n) % len(b.tail)
		if b.tailLen < len(b.tail) {
			b.tailLen += n
		}
		p = p[n:]
	}
	return total, nil
}

func (b *cappedOutputBuffer) Len() int {
	return int(b.written)
}

func (b *cappedOutputBuffer) String() string {
	// Reassemble the retained tail in stream order from the ring.
	tail := make([]byte, 0, b.tailLen)
	start := (b.tailPos - b.tailLen + len(b.tail)) % len(b.tail)
	first := len(b.tail) - start
	if first > b.tailLen {
		first = b.tailLen
	}
	tail = append(tail, b.tail[start:start+first]...)
	tail = append(tail, b.tail[:b.tailLen-first]...)
	// dropped counts every byte routed past the head, including the ones
	// still retained in the ring above — only the difference is actually
	// lost, so an output of exactly `limit` bytes reports no truncation.
	lost := b.dropped - int64(b.tailLen)
	if lost <= 0 {
		return string(b.head) + string(tail)
	}
	marker := fmt.Sprintf(
		"\n…[%s of output truncated; re-run with output redirected to a file for full capture]…\n",
		formatBytes(lost),
	)
	return string(b.head) + marker + string(tail)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// execIOWaitDelay bounds how long runSync waits for the command's output
// pipes to close after the process exits or is killed. Children that inherit
// the pipe write handles (daemons, browsers, npm shim chains) can keep them
// open indefinitely; past this delay Wait abandons the pipes
// (exec.ErrWaitDelay) and the call returns the output collected so far.
const execIOWaitDelay = 5 * time.Second

func (t *ExecTool) runSync(ctx context.Context, command, cwd string, timeout time.Duration) *ToolResult {
	// timeout == 0 means no timeout
	var cmdCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		cmdCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cmdCtx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.CommandContext(cmdCtx, "sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	prepareCommandForTermination(cmd)

	// Bound the io wait after the process exits or is canceled: when a
	// surviving child (daemon, browser, npm shim chain) inherits the output
	// pipe write handles, cmd.Wait() would otherwise block until every
	// holder exits — on Windows potentially forever, because taskkill /T
	// cannot reach orphans outside the live PPID chain. WaitDelay makes Wait
	// abandon the pipes after the delay and return exec.ErrWaitDelay
	// alongside the output collected so far.
	cmd.WaitDelay = execIOWaitDelay

	stdout := newCappedOutputBuffer(syncOutputRetainLimit)
	stderr := newCappedOutputBuffer(syncOutputRetainLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Route shell execution through the shared isolation entry point so exec tool
	// subprocesses receive the same isolation policy as other integrations.
	if err := isolation.Start(cmd); err != nil {
		return ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}
	trackProcessTree(cmd)
	defer releaseProcessTree(cmd)

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorCF("shell", "cmd.Wait goroutine panic recovered",
					map[string]any{
						"panic": fmt.Sprintf("%v", r),
						"stack": string(debug.Stack()),
					})
				done <- fmt.Errorf("panic in cmd.Wait: %v", r)
			}
		}()
		done <- cmd.Wait()
	}()

	var err error
	select {
	case err = <-done:
	case <-cmdCtx.Done():
		_ = terminateProcessTree(cmd)
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			err = <-done
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\nSTDERR:\n" + stderr.String()
	}

	if err != nil {
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			msg := fmt.Sprintf("Command timed out after %v", timeout)
			if output != "" {
				// Same inline-cleaning path as the success branch (review
				// P2-9): the partial output reaches the model verbatim
				// otherwise, bypassing both the cleanup pipeline and the
				// API-key mask.
				msg += "\n\nPartial output before timeout:\n" + CleanCommandOutput(command, output)
			}
			msg += "\n\nTip: if this command needs longer, re-run it with background=true (returns a sessionId) and use poll/read to check progress, or pass timeout=<seconds> to extend this run's wait."
			return &ToolResult{
				ForLLM:  msg,
				ForUser: msg,
				IsError: true,
				Err:     fmt.Errorf("command timeout: %w", err),
			}
		}

		// Wait can return ErrWaitDelay when background children kept the
		// output pipes open past the bounded io wait: the command itself may
		// still have exited cleanly (daemon-start pattern), so report a note
		// instead of a failure in that case.
		waitAbandoned := errors.Is(err, exec.ErrWaitDelay)
		var exitErr *exec.ExitError
		hasExitErr := errors.As(err, &exitErr)

		if hasExitErr {
			exitCode := exitErr.ExitCode()
			output += fmt.Sprintf("\n\n[Command exited with code %d]", exitCode)

			// Add signal information if killed by signal (Unix)
			if exitCode == -1 {
				output += " (killed by signal)"
			}
		} else if !waitAbandoned {
			output += fmt.Sprintf("\n\n[Command failed: %v]", err)
		}
		if waitAbandoned {
			output += fmt.Sprintf(
				"\n\n[Note: background processes still hold the command's output pipes; waited %s and returned the output collected so far.]",
				execIOWaitDelay,
			)
			// A clean process exit whose io wait was abandoned is a success
			// with a note; killed + abandoned stays an error via err.
			if !hasExitErr {
				err = nil
			}
		}
	}

	if output == "" {
		output = "(no output)"
	}

	// Clean the inline path only (progress redraws, ANSI escapes, oversized
	// lines burn context tokens without carrying information). The raw
	// output stays untouched for persistence below except for the API-key
	// mask (see there); never-worse guarded, so commands whose output is
	// already clean see no change at all.
	rawOutput := output
	output = CleanCommandOutput(command, output)

	// Keep the tail: command output usually matters at the end (errors,
	// final results). Line+byte caps with an explicit notice for the model;
	// the untruncated output is preserved to a file the model can read back.
	// The persisted copy is masked first (review P2-9): masking only
	// shortens, so the "persist the raw output" guardrail against the
	// shape-rewriting cleanup pipeline is untouched — but an unmasked
	// full-output file plus a "Full output: <path>" pointer would let the
	// model read leaked API keys straight back.
	if truncation := TruncateTail(output, TruncationOptions{}); truncation.Truncated {
		fullPath := t.persistFullOutput(MaskAPIKeys(rawOutput))
		output = truncation.Content + "\n\n" + truncation.Notice()
		if fullPath != "" {
			output += fmt.Sprintf(" Full output: %s", fullPath)
		}
	}

	if err != nil {
		return &ToolResult{
			ForLLM:  output,
			ForUser: output,
			IsError: true,
		}
	}

	return &ToolResult{
		ForLLM:  output,
		ForUser: output,
		IsError: false,
	}
}

// persistFullOutput saves the untruncated command output to a file inside the
// workspace (so read_file can access it even under workspace sandboxing).
// Returns the file path, or "" when persistence failed.
func (t *ExecTool) persistFullOutput(output string) string {
	return PersistOutputFile(t.workingDir, "shell-output-*.log", output)
}

func (t *ExecTool) runBackground(ctx context.Context, command, cwd string, ptyEnabled bool) *ToolResult {
	sessionID := generateSessionID()
	session := &ProcessSession{
		ID:         sessionID,
		Command:    command,
		PTY:        ptyEnabled,
		Background: true,
		StartTime:  time.Now().Unix(),
		Status:     "running",
		ptyKeyMode: PtyKeyModeCSI,
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	prepareCommandForTermination(cmd)

	var stdoutReader io.ReadCloser
	var stderrReader io.ReadCloser
	var stdinWriter io.WriteCloser
	var ptyTTY *os.File

	if ptyEnabled {
		ptmx, tty, err := pty.Open()
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to create PTY: %v", err))
		}

		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = tty

		// For PTY, we need Setsid to create a new session.
		// Note: Setsid and Setpgid conflict, so we must replace SysProcAttr entirely.
		setSysProcAttrForPty(cmd)

		session.ptyMaster = ptmx
		ptyTTY = tty
	} else {
		var err error
		stdoutReader, err = cmd.StdoutPipe()
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to create stdout pipe: %v", err))
		}
		stderrReader, err = cmd.StderrPipe()
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to create stderr pipe: %v", err))
		}
		stdinWriter, err = cmd.StdinPipe()
		if err != nil {
			return ErrorResult(fmt.Sprintf("failed to create stdin pipe: %v", err))
		}
		session.stdinWriter = stdinWriter
	}

	// Background sessions use the same startup path so isolation stays
	// consistent with synchronous exec runs.
	if err := isolation.Start(cmd); err != nil {
		if session.ptyMaster != nil {
			_ = session.ptyMaster.Close()
		}
		if ptyTTY != nil {
			_ = ptyTTY.Close()
		}
		return ErrorResult(fmt.Sprintf("failed to start command: %v", err))
	}
	if ptyTTY != nil {
		// The child dup'ed its own slave descriptors at exec; the parent's
		// copy must go or the master never sees EOF and both the fd and the
		// reader goroutine below leak for the lifetime of the process.
		_ = ptyTTY.Close()
	}

	session.PID = cmd.Process.Pid
	t.sessionManager.Add(session)
	// Track the whole tree so a later kill reaches orphaned grandchildren
	// (job object on Windows, process group on Unix); the handle is released
	// in the wait goroutines once the process is reaped.
	trackProcessTree(cmd)
	session.mu.Lock()
	session.terminator = func() error { return terminateProcessTree(cmd) }
	session.mu.Unlock()

	session.outputBuffer = &bytes.Buffer{}

	// PTY mode: read from ptyMaster and wait for process
	// Note: On Linux, closing ptyMaster doesn't interrupt blocking Read() calls,
	// so we need cmd.Wait() in a separate goroutine to detect process exit.
	if session.PTY && session.ptyMaster != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.ErrorCF("shell", "PTY cmd.Wait goroutine panic recovered",
						map[string]any{
							"panic": fmt.Sprintf("%v", r),
							"stack": string(debug.Stack()),
						})
					session.mu.Lock()
					session.Status = "error"
					session.mu.Unlock()
				}
			}()
			cmd.Wait() // Wait for process to exit
			releaseProcessTree(cmd)
			// The child is gone: close the master so the reader goroutine's
			// Read unblocks (EOF/EIO) and the fd is released. The session is
			// done, so no writer can race this (Write rejects done sessions).
			_ = session.ptyMaster.Close()
			session.mu.Lock()
			if cmd.ProcessState != nil {
				session.ExitCode = cmd.ProcessState.ExitCode()
			}
			session.Status = "done"
			session.finishedAt = time.Now().Unix()
			session.mu.Unlock()
		}()

		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.ErrorCF("shell", "PTY read goroutine panic recovered",
						map[string]any{
							"panic": fmt.Sprintf("%v", r),
							"stack": string(debug.Stack()),
						})
				}
			}()
			buf := make([]byte, 4096)
			for {
				n, err := session.ptyMaster.Read(buf)
				if n > 0 {
					raw := string(buf[:n])
					if mode := detectPtyKeyMode(raw); mode != PtyKeyModeNotFound && mode != session.GetPtyKeyMode() {
						session.SetPtyKeyMode(mode)
					}

					session.mu.Lock()
					if session.outputBuffer.Len() >= maxOutputBufferSize {
						if !session.outputTruncated {
							session.outputBuffer.WriteString(outputTruncateMarker)
							session.outputTruncated = true
						}
					} else {
						session.outputBuffer.Write(buf[:n])
					}
					session.mu.Unlock()
				}
				if err != nil {
					break
				}
			}
		}()
	} else {
		// Non-PTY mode: stdout and stderr are drained concurrently — the old
		// sequential drain (stdout to EOF, then stderr) kept stderr invisible
		// for as long as the process kept stdout open. The child is reaped
		// via os.Process.Wait rather than exec.Cmd.Wait: the latter closes
		// our pipe read ends as soon as the process exits, racing the final
		// drain (lost tail output). A grandchild inheriting the pipe write
		// ends must not wedge the session in "running" forever either: after
		// the process is reaped, the copiers get a grace period to hit EOF,
		// then the read ends are closed to unblock them.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.ErrorCF("shell", "pipe read goroutine panic recovered",
						map[string]any{
							"panic": fmt.Sprintf("%v", r),
							"stack": string(debug.Stack()),
						})
				}
			}()

			appendOutput := func(data []byte) {
				session.mu.Lock()
				if session.outputBuffer.Len() >= maxOutputBufferSize {
					if !session.outputTruncated {
						session.outputBuffer.WriteString(outputTruncateMarker)
						session.outputTruncated = true
					}
				} else {
					session.outputBuffer.Write(data)
				}
				session.mu.Unlock()
			}

			var wg sync.WaitGroup
			drain := func(r io.ReadCloser) {
				defer wg.Done()
				if r == nil {
					return
				}
				buf := make([]byte, 4096)
				for {
					n, err := r.Read(buf)
					if n > 0 {
						appendOutput(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}
			wg.Add(2)
			go drain(stdoutReader)
			go drain(stderrReader)

			// Reap the child without touching the session pipes.
			type waitResult struct {
				state *os.ProcessState
			}
			reaped := make(chan waitResult, 1)
			go func() {
				state, _ := cmd.Process.Wait()
				reaped <- waitResult{state: state}
			}()
			exitState := (<-reaped).state
			releaseProcessTree(cmd)

			drained := make(chan struct{})
			go func() {
				wg.Wait()
				close(drained)
			}()
			select {
			case <-drained:
			case <-time.After(execIOWaitDelay):
				// Grandchildren hold the write ends; abandon the pipes like
				// runSync's WaitDelay does.
				_ = stdoutReader.Close()
				_ = stderrReader.Close()
				<-drained
			}
			_ = stdoutReader.Close()
			_ = stderrReader.Close()
			if stdinWriter != nil {
				_ = stdinWriter.Close()
			}

			session.mu.Lock()
			if exitState != nil {
				session.ExitCode = exitState.ExitCode()
			}
			session.Status = "done"
			session.finishedAt = time.Now().Unix()
			session.mu.Unlock()
		}()
	}

	resp := ExecResponse{
		SessionID: sessionID,
		Status:    "running",
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return &ToolResult{
		ForLLM:  string(data),
		ForUser: fmt.Sprintf("Session %s started", sessionID),
		IsError: false,
	}
}
