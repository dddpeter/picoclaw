package deltachat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// rpcRequest is a single JSON-RPC 2.0 request. Delta Chat uses positional
// (array) parameters.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params,omitempty"`
}

// rpcResponse is a single JSON-RPC 2.0 response.
type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("deltachat rpc error %d: %s", e.Code, e.Message)
}

// rpcClient drives a `deltachat-rpc-server` child process over stdio using
// newline-delimited JSON-RPC 2.0. The server answers requests asynchronously
// and out of order, so every call is correlated back to its caller by id.
type rpcClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	serverPath string
	dataDir    string
	// onRestart re-runs channel-level bootstrap (account selection + IO
	// start) against the respawned server; nil skips it.
	onRestart func(ctx context.Context) error

	mu           sync.Mutex
	nextID       uint64
	pending      map[uint64]chan rpcResponse
	closed       bool
	restarting   bool
	shuttingDown bool
}

const (
	rpcRestartBackoffMin = 2 * time.Second
	rpcRestartBackoffMax = 5 * time.Minute
)

// startRPC spawns the RPC server with DC_ACCOUNTS_PATH pointing at dataDir and
// begins the background read loop.
func startRPC(serverPath, dataDir string) (*rpcClient, error) {
	cmd := exec.Command(serverPath)
	cmd.Env = append(cmd.Environ(), "DC_ACCOUNTS_PATH="+dataDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("deltachat rpc stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("deltachat rpc stdout: %w", err)
	}
	// Let the server's logs flow to our stderr for easy diagnostics.
	cmd.Stderr = logWriter{}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start deltachat-rpc-server (%s): %w", serverPath, err)
	}

	c := &rpcClient{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     stdout,
		serverPath: serverPath,
		dataDir:    dataDir,
		pending:    make(map[uint64]chan rpcResponse),
	}
	go c.readLoop()
	return c, nil
}

// readLoop reads newline-delimited responses and dispatches them to waiters.
func (c *rpcClient) readLoop() {
	reader := bufio.NewReader(c.stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var resp rpcResponse
			if jsonErr := json.Unmarshal(line, &resp); jsonErr == nil && resp.ID != 0 {
				c.mu.Lock()
				ch := c.pending[resp.ID]
				delete(c.pending, resp.ID)
				c.mu.Unlock()
				if ch != nil {
					ch <- resp
				}
			}
		}
		if err != nil {
			c.failAll(err)
			// The server process is gone: without respawning it the channel
			// kept "running" while every call failed forever (2026-10-31
			// review C8). close() disables supervision during shutdown.
			c.supervise()
			return
		}
	}
}

// supervise respawns a dead deltachat-rpc-server with exponential backoff,
// rewires the same client in place (callers keep their *rpcClient pointer),
// waits for readiness, and re-runs onRestart so account IO resumes.
func (c *rpcClient) supervise() {
	c.mu.Lock()
	if c.shuttingDown || c.restarting {
		c.mu.Unlock()
		return
	}
	c.restarting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.restarting = false
		c.mu.Unlock()
	}()

	backoff := rpcRestartBackoffMin
	for {
		time.Sleep(backoff)
		backoff *= 2
		if backoff > rpcRestartBackoffMax {
			backoff = rpcRestartBackoffMax
		}

		c.mu.Lock()
		if c.shuttingDown {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		newCmd := exec.Command(c.serverPath)
		newCmd.Env = append(newCmd.Environ(), "DC_ACCOUNTS_PATH="+c.dataDir)
		stdin, err := newCmd.StdinPipe()
		if err != nil {
			logger.ErrorCF("deltachat", "rpc restart: stdin pipe failed", map[string]any{"error": err.Error()})
			continue
		}
		stdout, err := newCmd.StdoutPipe()
		if err != nil {
			logger.ErrorCF("deltachat", "rpc restart: stdout pipe failed", map[string]any{"error": err.Error()})
			continue
		}
		newCmd.Stderr = logWriter{}
		if err := newCmd.Start(); err != nil {
			logger.ErrorCF("deltachat", "rpc restart: spawn failed", map[string]any{
				"error": err.Error(), "backoff": backoff.String(),
			})
			continue
		}

		c.mu.Lock()
		c.cmd = newCmd
		c.stdin = stdin
		c.stdout = stdout
		c.closed = false
		c.nextID = 0 // pending map already drained by failAll
		c.restarting = false
		c.mu.Unlock()
		go c.readLoop()

		if err := c.waitReadyInternal(); err != nil {
			logger.ErrorCF("deltachat", "rpc restart: server never became ready", map[string]any{"error": err.Error()})
			c.failAll(err)
			continue
		}
		if c.onRestart != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			err := c.onRestart(ctx)
			cancel()
			if err != nil {
				logger.ErrorCF("deltachat", "rpc restart: bootstrap failed", map[string]any{"error": err.Error()})
				c.failAll(err)
				continue
			}
		}
		logger.InfoC("deltachat", "deltachat-rpc-server restarted successfully")
		return
	}
}

// waitReadyInternal polls get_system_info until the respawned server answers.
func (c *rpcClient) waitReadyInternal() error {
	for attempt := 0; attempt < 40; attempt++ {
		callCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := c.call(callCtx, "get_system_info")
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("deltachat: restarted rpc server did not become ready")
}

// failAll wakes every pending caller with an error (used on EOF / shutdown).
func (c *rpcClient) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- rpcResponse{Error: &rpcError{Code: -1, Message: "rpc closed: " + err.Error()}}
		delete(c.pending, id)
	}
}

// call issues one request and blocks until the matching response arrives, the
// context is canceled, or the server goes away.
func (c *rpcClient) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("deltachat rpc: connection closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		c.clearPending(id)
		return nil, err
	}
	data = append(data, '\n')

	c.mu.Lock()
	_, err = c.stdin.Write(data)
	c.mu.Unlock()
	if err != nil {
		c.clearPending(id)
		return nil, fmt.Errorf("deltachat rpc write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.clearPending(id)
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *rpcClient) clearPending(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// close terminates the RPC server process and disables supervision.
func (c *rpcClient) close() {
	c.mu.Lock()
	c.shuttingDown = true
	c.closed = true
	c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

// logWriter forwards deltachat-rpc-server stderr lines into the logger.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logger.DebugC("deltachat", string(p))
	return len(p), nil
}
