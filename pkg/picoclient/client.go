package picoclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	pico "github.com/sipeed/picoclaw/pkg/channels/pico"
)

// ConnState describes the client's connection lifecycle.
type ConnState int

const (
	StateDisconnected ConnState = iota
	StateConnecting
	StateConnected
)

func (s ConnState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	default:
		return "disconnected"
	}
}

const (
	defaultBackoffInitial = 1 * time.Second
	defaultBackoffMax     = 5 * time.Second
	defaultDialTimeout    = 10 * time.Second
	// Must stay above the server's ping interval (default 30s) so a healthy
	// connection is never dropped: the gateway pings, gorilla auto-pongs and
	// our ping handler below refreshes this deadline on every server ping.
	defaultReadTimeout = 60 * time.Second
	// Bounds control-frame writes (pong replies) from the read goroutine.
	writeControlTimeout = 10 * time.Second
	eventBuffer         = 256
	stateBuffer         = 16
)

// ErrNotConnected is returned by Send when no connection is up. Callers that
// queued a user message on the UI should surface it locally.
var ErrNotConnected = errors.New("picoclient: not connected")

// Config configures a Client.
type Config struct {
	// URL is the gateway WebSocket endpoint, e.g. ws://127.0.0.1:18790/pico/ws.
	URL string
	// Token is the pico channel token from the gateway config.
	Token string
	// SessionID pins the pico session; generated when empty.
	SessionID string
	// BackoffInitial/BackoffMax bound the reconnect delay (defaults 1s/5s).
	BackoffInitial time.Duration
	BackoffMax     time.Duration
	// DialTimeout bounds the initial WS handshake (default 10s).
	DialTimeout time.Duration
	// ReadTimeout bounds idle reads; refreshed by each data message and by
	// every server ping (default 60s).
	ReadTimeout time.Duration
}

// Client is a reconnecting Pico Protocol WebSocket client. Events and
// connection-state changes are delivered on dedicated channels; both block
// the sender when full, so consumers must keep reading. It is safe for
// concurrent use.
type Client struct {
	cfg       Config
	sessionID string

	events chan Event
	states chan ConnState

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
	lastErr string // most recent dial failure, surfaced for UI diagnostics

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New fills config defaults and returns a started-ready Client.
func New(cfg Config) *Client {
	if cfg.SessionID == "" {
		cfg.SessionID = uuid.NewString()
	}
	if cfg.BackoffInitial <= 0 {
		cfg.BackoffInitial = defaultBackoffInitial
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = defaultBackoffMax
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	return &Client{
		cfg:       cfg,
		sessionID: cfg.SessionID,
		events:    make(chan Event, eventBuffer),
		states:    make(chan ConnState, stateBuffer),
	}
}

// SessionID returns the current pico session id.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// SetSessionID switches the pico session: the live connection (if any) is
// dropped and the reconnect loop redials with the new id.
func (c *Client) SetSessionID(id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	c.sessionID = id
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Start launches the connect/reconnect loop. Stop terminates it.
func (c *Client) Start(ctx context.Context) {
	c.mu.Lock()
	if c.ctx != nil { // already started
		c.mu.Unlock()
		return
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(1)
	c.mu.Unlock()

	go c.run()
}

// Stop cancels the loop and closes the delivery channels once it exits.
func (c *Client) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.wg.Wait()
}

// Events returns the decoded protocol event stream. Closed after Stop.
func (c *Client) Events() <-chan Event { return c.events }

// States returns connection state transitions. Closed after Stop.
func (c *Client) States() <-chan ConnState { return c.states }

func (c *Client) run() {
	defer c.wg.Done()
	defer close(c.events)
	defer close(c.states)

	backoff := c.cfg.BackoffInitial
	for {
		if c.ctx.Err() != nil {
			return
		}
		c.setState(StateConnecting)

		c.mu.Lock()
		dialSession := c.sessionID
		c.mu.Unlock()
		conn, err := c.dial()
		if err != nil {
			c.setLastDialError(err.Error())
			c.setState(StateDisconnected)
			if !c.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, c.cfg.BackoffMax)
			continue
		}
		backoff = c.cfg.BackoffInitial
		c.setLastDialError("")

		c.mu.Lock()
		c.conn = conn
		// SetSessionID may have fired while the handshake was completing:
		// it found c.conn still nil and closed nothing, so detect the
		// session change here and drop the stale connection ourselves
		// instead of waiting out the read deadline.
		stale := c.sessionID != dialSession
		c.mu.Unlock()
		if !stale {
			c.setState(StateConnected)
			c.readLoop(conn)
		} else {
			_ = conn.Close()
		}

		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		if c.ctx.Err() != nil {
			return
		}
		c.setState(StateDisconnected)
		if !c.sleep(backoff) {
			return
		}
	}
}

// LastDialError returns the most recent dial failure reason (empty when the
// last dial succeeded or none was attempted). It exists so UIs can explain a
// stuck "disconnected" state instead of leaving the cause invisible.
func (c *Client) LastDialError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Client) setLastDialError(err string) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
}

func (c *Client) dial() (*websocket.Conn, error) {
	c.mu.Lock()
	url := c.cfg.URL + "?session_id=" + c.sessionID
	token := c.cfg.Token
	c.mu.Unlock()

	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{HandshakeTimeout: c.cfg.DialTimeout}
	conn, resp, err := dialer.DialContext(c.ctx, url, header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			err = fmt.Errorf("picoclient: dial %s: %w (HTTP %d)", c.cfg.URL, err, resp.StatusCode)
		} else {
			err = fmt.Errorf("picoclient: dial %s: %w", c.cfg.URL, err)
		}
		return nil, err
	}
	return conn, nil
}

func (c *Client) readLoop(conn *websocket.Conn) {
	defer conn.Close()

	// Closing the conn is the only way to interrupt a ReadMessage parked on
	// an idle session: the gateway's pings are consumed inside gorilla's
	// read machinery and never surface as messages, so the ctx checks in
	// this loop would otherwise only fire after the full ReadTimeout. The
	// watcher keeps the dial-to-assignment window race-free too.
	watcherDone := make(chan struct{})
	defer close(watcherDone)
	go func() {
		select {
		case <-c.ctx.Done():
			_ = conn.Close()
		case <-watcherDone:
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
	// The client never pings, so pongs never arrive; server pings are the
	// idle-session keepalive and must refresh the read deadline (the default
	// ping handler auto-pongs but leaves the deadline untouched, which would
	// needlessly sever a healthy idle connection every ReadTimeout).
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(writeControlTimeout))
	})
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))

		var msg pico.PicoMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == pico.TypePong {
			continue
		}
		select {
		case c.events <- Decode(msg):
		case <-c.ctx.Done():
			return
		}
	}
}

// Send delivers a message.send with the given content and optional media
// payloads (data URLs). It fails with ErrNotConnected when no connection is
// up; the reconnect loop will keep the session alive for the next attempt.
func (c *Client) Send(_ context.Context, content string, media []string) error {
	c.mu.Lock()
	conn := c.conn
	sessionID := c.sessionID
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}

	payload := map[string]any{pico.PayloadKeyContent: content}
	if len(media) > 0 {
		payload["media"] = media
	}
	msg := pico.PicoMessage{
		Type:      pico.TypeMessageSend,
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("picoclient: send: %w", err)
	}
	return nil
}

func (c *Client) setState(s ConnState) {
	select {
	case c.states <- s:
	case <-c.ctx.Done():
	}
}

func (c *Client) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
