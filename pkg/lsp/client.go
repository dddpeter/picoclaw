package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Client manages one language-server process and its JSON-RPC session.
type Client struct {
	serverID string
	cfg      ServerConfig
	root     string
	timeout  time.Duration

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exitCh chan struct{} // closed when the server process is gone

	mu        sync.Mutex
	nextID    int64
	pending   map[int64]chan rpcResponse
	published map[string]*publishedDiags // documentKey -> latest
	waiters   map[string][]chan struct{}
	stderr    bytes.Buffer
	dying     bool

	capabilities map[string]any

	// openDocs tracks documents opened in this session so re-touching a
	// file across pooled calls sends didChange instead of a second didOpen
	// (LSP forbids didOpen on an already-open document).
	openDocs map[string]docVersion // documentKey -> current version

	// terminate kills the whole process tree on shutdown; nil on the happy
	// path after a graceful exit.
	terminate func()
}

type docVersion struct {
	uri     string
	version int
}

type publishedDiags struct {
	version     int
	diagnostics []Diagnostic
	at          time.Time
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// Start launches the server process and performs the initialize handshake.
func Start(ctx context.Context, cfg ServerConfig, root string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	c := &Client{
		serverID:  cfg.Name,
		cfg:       cfg,
		root:      root,
		timeout:   timeout,
		pending:   make(map[int64]chan rpcResponse),
		published: make(map[string]*publishedDiags),
		waiters:   make(map[string][]chan struct{}),
		openDocs:  make(map[string]docVersion),
	}

	proc, err := spawnServer(cfg, root)
	if err != nil {
		return nil, err
	}
	c.cmd = proc.cmd
	c.stdin = proc.stdin
	c.terminate = proc.terminate
	c.exitCh = make(chan struct{})
	go func() {
		_ = proc.cmd.Wait()
		close(c.exitCh)
	}()
	go c.readLoop(proc.stdout)
	go c.drainStderr(proc.stderr)

	if err := c.initialize(ctx); err != nil {
		c.Shutdown(500 * time.Millisecond)
		return nil, err
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   DirectoryURI(c.root),
		"workspaceFolders": []map[string]any{
			{"uri": DirectoryURI(c.root), "name": filepath.Base(c.root)},
		},
		"initializationOptions": c.cfg.Initialization,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"codeAction": map[string]any{
					"dynamicRegistration": false,
					"resolveSupport":      map[string]any{"properties": []string{"edit"}},
				},
				"diagnostic":         map[string]any{"dynamicRegistration": false},
				"publishDiagnostics": map[string]any{},
				"synchronization":    map[string]any{"didSave": true},
			},
			"workspace": map[string]any{
				"configuration":    true,
				"workspaceFolders": true,
			},
		},
	}
	var result struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := c.request(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("%s: initialize failed: %w", c.serverID, err)
	}
	c.capabilities = result.Capabilities
	c.notify(ctx, "initialized", map[string]any{})
	if len(c.cfg.Initialization) > 0 {
		c.notify(ctx, "workspace/didChangeConfiguration", map[string]any{
			"settings": c.cfg.Initialization,
		})
	}
	return nil
}

// TouchFile opens the document (or syncs it if already open in this session)
// and returns the URI used.
func (c *Client) TouchFile(ctx context.Context, path, text, languageID string) (string, error) {
	uri := FileURI(path)
	key := DocumentKey(uri)

	// Version bookkeeping under one lock: concurrent touches of the same
	// document must not observe the same version number.
	c.mu.Lock()
	doc, open := c.openDocs[key]
	if !open {
		c.openDocs[key] = docVersion{uri: uri, version: 1}
		c.mu.Unlock()
		c.notify(ctx, "textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": languageID, "version": 1, "text": text,
			},
		})
		return uri, nil
	}
	doc.version++
	c.openDocs[key] = doc
	c.mu.Unlock()

	// Already open: full-text didChange (legal for both full and
	// incremental server sync kinds; the range-less form replaces the
	// document).
	c.notify(ctx, "textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": doc.version},
		"contentChanges": []map[string]any{
			{"text": text},
		},
	})
	return uri, nil
}

// CloseDoc closes a document if this session has it open (best effort).
func (c *Client) CloseDoc(ctx context.Context, path string) {
	uri := FileURI(path)
	key := DocumentKey(uri)
	c.mu.Lock()
	_, open := c.openDocs[key]
	delete(c.openDocs, key)
	c.mu.Unlock()
	if open {
		c.notify(ctx, "textDocument/didClose", map[string]any{
			"textDocument": map[string]any{"uri": uri},
		})
	}
}

// Diagnostics returns diagnostics for a document, using pull when the server
// supports it and push otherwise (design §2.3): pull first; empty pull plus
// a configured pull grace keeps waiting for a push; push-only waits for the
// next publish with an 800ms settle, falling back to clean after the
// per-server push grace.
func (c *Client) Diagnostics(ctx context.Context, uri string) ([]Diagnostic, error) {
	if c.supportsPull() {
		return c.pullDiagnostics(ctx, uri)
	}
	return c.waitPushedDiagnostics(ctx, uri)
}

func (c *Client) supportsPull() bool {
	_, ok := c.capabilities["diagnosticProvider"]
	return ok
}

func (c *Client) pullDiagnostics(ctx context.Context, uri string) ([]Diagnostic, error) {
	var result struct {
		Items []Diagnostic `json:"items"`
	}
	if err := c.request(ctx, "textDocument/diagnostic", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}, &result); err != nil {
		return nil, err
	}
	if len(result.Items) > 0 || c.cfg.PullDiagnosticsGraceMs <= 0 {
		return result.Items, nil
	}
	// Empty pull: maybe the server answered before analysis finished — wait
	// briefly for a real push before trusting the empty answer.
	if diags, ok := c.waitForPublish(ctx, DocumentKey(uri), time.Duration(c.cfg.PullDiagnosticsGraceMs)*time.Millisecond); ok {
		return diags, nil
	}
	return result.Items, nil
}

func (c *Client) waitPushedDiagnostics(ctx context.Context, uri string) ([]Diagnostic, error) {
	key := DocumentKey(uri)
	// A publish may already be sitting in the buffer from before the wait
	// started (race between didOpen handling and server publish).
	c.mu.Lock()
	pub := c.published[key]
	c.mu.Unlock()
	if pub != nil && time.Since(pub.at) < DiagnosticsSettle {
		return c.settleDiagnostics(ctx, key, pub), nil
	}

	deadline := c.timeout
	if c.cfg.PushDiagnosticsGraceMs > 0 {
		deadline = time.Duration(c.cfg.PushDiagnosticsGraceMs) * time.Millisecond
	}
	if _, ok := c.waitForPublish(ctx, key, deadline); ok {
		return c.settleDiagnostics(ctx, key, nil), nil
	}
	// No publish within the window: servers configured with a push grace
	// treat silence as a clean document; otherwise this is a timeout.
	if c.cfg.PushDiagnosticsGraceMs > 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("%s: no diagnostics published for %s within %s", c.serverID, uri, c.timeout)
}

// settleDiagnostics waits DiagnosticsSettle for a newer publish before
// returning the current set, coalescing rapid republishes.
func (c *Client) settleDiagnostics(ctx context.Context, key string, current *publishedDiags) []Diagnostic {
	if current == nil {
		c.mu.Lock()
		current = c.published[key]
		c.mu.Unlock()
		if current == nil {
			return nil
		}
	}
	for {
		remaining := DiagnosticsSettle - time.Since(current.at)
		if remaining <= 0 {
			c.mu.Lock()
			defer c.mu.Unlock()
			if latest := c.published[key]; latest != nil && latest.at.After(current.at) {
				return latest.diagnostics
			}
			return current.diagnostics
		}
		select {
		case <-ctx.Done():
			c.mu.Lock()
			defer c.mu.Unlock()
			if latest := c.published[key]; latest != nil {
				return latest.diagnostics
			}
			return current.diagnostics
		case <-time.After(remaining):
			c.mu.Lock()
			latest := c.published[key]
			c.mu.Unlock()
			if latest != nil && latest.at.After(current.at) {
				current = latest
				continue
			}
			return current.diagnostics
		}
	}
}

// waitForPublish blocks until a publish arrives for key (returning the
// diagnostics) or the wait expires. ok=false means nothing arrived.
func (c *Client) waitForPublish(ctx context.Context, key string, wait time.Duration) ([]Diagnostic, bool) {
	deadline := time.After(wait)
	for {
		c.mu.Lock()
		pub := c.published[key]
		if pub == nil {
			ch := make(chan struct{}, 1)
			c.waiters[key] = append(c.waiters[key], ch)
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-deadline:
				c.removeWaiter(key, ch)
				return nil, false
			case <-ctx.Done():
				c.removeWaiter(key, ch)
				return nil, false
			}
		}
		c.mu.Unlock()
		return pub.diagnostics, true
	}
}

func (c *Client) removeWaiter(key string, ch chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.waiters[key]
	for i, w := range list {
		if w == ch {
			c.waiters[key] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// CodeActions requests code actions of the given kind covering the whole
// document (pi-lsp semantics: full-document range + only filter).
func (c *Client) CodeActions(ctx context.Context, uri, text string, diags []Diagnostic, kind string) ([]CodeAction, error) {
	endLine, endChar := OffsetToPosition(text, len(text))
	var actions []CodeAction
	if err := c.request(ctx, "textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range": map[string]any{
			"start": map[string]any{"line": 0, "character": 0},
			"end":   map[string]any{"line": endLine, "character": endChar},
		},
		"context": map[string]any{"diagnostics": diags, "only": []string{kind}},
	}, &actions); err != nil {
		return nil, err
	}
	return actions, nil
}

// ResolveActions fills in the edit payload for actions that need
// codeAction/resolve (only when the server advertised resolveProvider).
func (c *Client) ResolveActions(ctx context.Context, actions []CodeAction) ([]CodeAction, error) {
	canResolve := false
	if cap, ok := c.capabilities["codeActionProvider"].(map[string]any); ok {
		canResolve, _ = cap["resolveProvider"].(bool)
	}
	out := make([]CodeAction, 0, len(actions))
	for _, a := range actions {
		if len(a.Edit.DocumentChanges) > 0 || len(a.Edit.Changes) > 0 || !canResolve {
			out = append(out, a)
			continue
		}
		var resolved CodeAction
		if err := c.request(ctx, "codeAction/resolve", a, &resolved); err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

// Shutdown asks the server to exit gracefully, then force-kills the process
// tree if it lingers.
func (c *Client) Shutdown(timeout time.Duration) {
	c.mu.Lock()
	if c.dying {
		c.mu.Unlock()
		return
	}
	c.dying = true
	pending := c.pending
	c.pending = make(map[int64]chan rpcResponse)
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- rpcResponse{err: fmt.Errorf("%s: server shutting down", c.serverID)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Force: the dying flag above already rejects normal requests, but
		// the shutdown handshake itself must still go through.
		_ = c.requestForce(ctx, "shutdown", nil, nil, true)
		c.notify(context.Background(), "exit", nil)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	case <-c.exitCh:
	}
	_ = c.stdin.Close()
	c.killTree()
	<-c.exitCh // the reaper goroutine owns Wait
}

func (c *Client) killTree() {
	if c.terminate != nil {
		c.terminate()
		c.terminate = nil
	}
}

// ServerID reports the configured server name.
func (c *Client) ServerID() string { return c.serverID }

// StderrTail returns the tail of captured server stderr (for error context).
func (c *Client) StderrTail() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stderr.String()
	if len(s) > 512 {
		s = s[len(s)-512:]
	}
	return strings.TrimSpace(s)
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	return c.requestForce(ctx, method, params, out, false)
}

func (c *Client) requestForce(ctx context.Context, method string, params any, out any, force bool) error {
	c.mu.Lock()
	if c.dying && !force {
		c.mu.Unlock()
		return fmt.Errorf("%s: server shutting down", c.serverID)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		if resp.err != nil {
			return fmt.Errorf("%s: %s: %w", c.serverID, method, resp.err)
		}
		if out == nil || len(resp.result) == 0 || string(resp.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(resp.result, out); err != nil {
			return fmt.Errorf("%s: %s: decode response: %w", c.serverID, method, err)
		}
		return nil
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %s timed out after %s.%s", c.serverID, method, c.timeout, c.stderrSuffix())
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.exitCh:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: server exited before responding to %s.%s", c.serverID, method, c.stderrSuffix())
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) {
	if params == nil {
		_ = c.send(map[string]any{"jsonrpc": "2.0", "method": method})
		return
	}
	_ = c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) send(msg map[string]any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("%s: server is not running", c.serverID)
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(c.stdin, header+string(body)); err != nil {
		return fmt.Errorf("%s: stdin write failed: %w.%s", c.serverID, err, c.stderrSuffixLocked())
	}
	return nil
}

func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewReaderSize(r, 64*1024)
	for {
		length, err := readFrameHeader(scanner)
		if err != nil {
			c.failPending(fmt.Errorf("%s: server output ended: %w.%s", c.serverID, err, c.StderrTail()))
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(scanner, body); err != nil {
			c.failPending(fmt.Errorf("%s: truncated server message: %w", c.serverID, err))
			return
		}
		c.handleMessage(body)
	}
}

func readFrameHeader(r *bufio.Reader) (int, error) {
	var length int
	headerSeen := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if headerSeen {
				return length, nil
			}
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(line[:colon]))
		if name == "content-length" {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(line[colon+1:]), "%d", &n); err != nil {
				return 0, fmt.Errorf("invalid Content-Length %q", line)
			}
			length = n
			headerSeen = true
		}
	}
}

type rpcMessage struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) handleMessage(body []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return
	}
	if msg.ID != nil && msg.Method == "" || (msg.ID != nil && (msg.Result != nil || msg.Error != nil)) {
		c.mu.Lock()
		ch := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.mu.Unlock()
		if ch == nil {
			return
		}
		if msg.Error != nil {
			ch <- rpcResponse{err: fmt.Errorf("server error %d: %s", msg.Error.Code, msg.Error.Message)}
		} else {
			ch <- rpcResponse{result: msg.Result}
		}
		return
	}
	switch msg.Method {
	case "textDocument/publishDiagnostics":
		var params struct {
			URI         string       `json:"uri"`
			Version     int          `json:"version"`
			Diagnostics []Diagnostic `json:"diagnostics"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return
		}
		key := DocumentKey(params.URI)
		c.mu.Lock()
		c.published[key] = &publishedDiags{
			version:     params.Version,
			diagnostics: params.Diagnostics,
			at:          time.Now(),
		}
		waiters := c.waiters[key]
		delete(c.waiters, key)
		c.mu.Unlock()
		for _, w := range waiters {
			select {
			case w <- struct{}{}:
			default:
			}
		}
	}
	// window/logMessage, telemetry and friends are ignored.
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[int64]chan rpcResponse)
	c.dying = true
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- rpcResponse{err: err}
	}
}

func (c *Client) drainStderr(r io.Reader) {
	buf := make([]byte, 4*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.stderr.Write(buf[:n])
			if c.stderr.Len() > 8*1024 {
				c.stderr.Next(c.stderr.Len() - 8*1024)
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) stderrSuffix() string {
	if tail := c.StderrTail(); tail != "" {
		return " Server stderr: " + tail
	}
	return ""
}

func (c *Client) stderrSuffixLocked() string {
	s := c.stderr.String()
	if len(s) > 256 {
		s = s[len(s)-256:]
	}
	if s = strings.TrimSpace(s); s != "" {
		return " Server stderr: " + s
	}
	return ""
}
