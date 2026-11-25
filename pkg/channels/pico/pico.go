package pico

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// picoConn represents a single WebSocket connection.
type picoConn struct {
	id        string
	conn      *websocket.Conn
	sessionID string
	writeMu   sync.Mutex
	closed    atomic.Bool
	cancel    context.CancelFunc // cancels per-connection goroutines (e.g. pingLoop)
	// deliveryMu serializes every ordered session-frame write (deliver /
	// endCatchup) so catch-up replay and live broadcasts cannot interleave on
	// the wire. Lock order: deliveryMu → writeMu; connsMu is never held here.
	deliveryMu sync.Mutex
	// catchingUp and pending are guarded by deliveryMu. A fresh connection
	// starts in catch-up mode: session broadcasts buffer into pending until
	// the handshake replay (endCatchup) drains them in order.
	catchingUp bool
	pending    []PicoMessage
}

// PicoChannel implements the native Pico Protocol WebSocket channel.
// It serves as the reference implementation for all optional capability interfaces.
type PicoChannel struct {
	*channels.BaseChannel
	bc                 *config.Channel
	config             *config.PicoSettings
	upgrader           websocket.Upgrader
	connections        map[string]*picoConn            // connID -> *picoConn
	sessionConnections map[string]map[string]*picoConn // sessionID -> connID -> *picoConn
	connsMu            sync.RWMutex
	ctx                context.Context
	cancel             context.CancelFunc
	progress           *channels.ToolFeedbackAnimator
	deleteMessageFn    func(context.Context, string, string) error
	// turnActiveProbe reports whether an agent turn is live for a chatID
	// ("pico:<sessionID>"). Wired by the gateway; nil = no turn.status
	// frames (octop borrowing §四).
	turnActiveProbeMu sync.RWMutex
	turnActiveProbe   func(chatID string) bool
	// broadcastFn lets tests intercept outbound broadcasts. nil → broadcastToSession.
	broadcastFn func(chatID string, msg PicoMessage) error
	// seqs stamps session broadcasts with monotonic seqs and retains them
	// in a bounded ring for reconnect catch-up (trae borrowing §一).
	seqs *seqStore
	// seenIDs dedupes inbound message.send/media.send IDs so a client
	// retrying the same request gets message.ack instead of a second turn
	// (trae borrowing 增量清单 §2.2).
	seenIDs *idempotencyStore
	// a2uiSurfaces tracks the A2UI surfaces this channel already broadcast
	// as message.create, keyed by (chatID, surfaceId), so streamed
	// update-only batches merge into the live card and hot-update it via
	// message.update (A2UI phase 2, task 5).
	a2uiSurfaces *a2uiSurfaceRegistry
	// uploads carries the workspace-rooted options for POST /pico/upload
	// (web file upload pipeline, design §4.1); injected via SetUploadOptions.
	uploads uploadState
	// fileScanner supplies persisted session history for the "my files"
	// aggregation endpoint (design §11.1); injected by the gateway.
	fileScanner   SessionFileScanner
	fileScannerMu sync.RWMutex
	// sessionModel binds a model to ONE conversation (fork: session-scoped
	// model selection). The gateway injects the agent-side controller; nil
	// means the feature is unavailable.
	sessionModelMu         sync.RWMutex
	sessionModelController SessionModelController
	// lastUsage keeps the most recent ContextUsage per chatID so the web UI
	// can restore the context ring after a page refresh (live payloads only
	// reach the currently-connected client).
	lastUsage   map[string]*bus.ContextUsage
	lastUsageMu sync.RWMutex
}

// NewPicoChannel creates a new Pico Protocol channel.
func NewPicoChannel(
	bc *config.Channel,
	cfg *config.PicoSettings,
	messageBus *bus.MessageBus,
) (*PicoChannel, error) {
	if cfg.Token.String() == "" {
		return nil, fmt.Errorf("pico token is required")
	}

	base := channels.NewBaseChannel("pico", cfg, messageBus, bc.AllowFrom)

	allowOrigins := cfg.AllowOrigins
	checkOrigin := func(r *http.Request) bool {
		if len(allowOrigins) == 0 {
			return true // allow all if not configured
		}
		origin := r.Header.Get("Origin")
		for _, allowed := range allowOrigins {
			if allowed == "*" || allowed == origin {
				return true
			}
		}
		return false
	}

	ch := &PicoChannel{
		BaseChannel: base,
		bc:          bc,
		config:      cfg,
		upgrader: websocket.Upgrader{
			CheckOrigin:     checkOrigin,
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		connections:        make(map[string]*picoConn),
		sessionConnections: make(map[string]map[string]*picoConn),
		seqs:               newSeqStore(DefaultSeqBufferCapacity),
		seenIDs:            newIdempotencyStore(DefaultSeqBufferCapacity),
		a2uiSurfaces:       newA2uiSurfaceRegistry(),
		lastUsage:          make(map[string]*bus.ContextUsage),
	}
	ch.progress = channels.NewToolFeedbackAnimator(ch.EditMessage)
	ch.deleteMessageFn = ch.DeleteMessage
	return ch, nil
}

// SetTurnActiveProbe wires the agent-side turn probe used by the connect
// turn.status frame (octop borrowing §四).
func (c *PicoChannel) SetTurnActiveProbe(fn func(chatID string) bool) {
	c.turnActiveProbeMu.Lock()
	defer c.turnActiveProbeMu.Unlock()
	c.turnActiveProbe = fn
}

// SetSessionModelController wires the agent-side session-model controller
// (fork: session-scoped model selection). Called from BOTH the startup path
// and restartServices — a config reload may rebuild the pico channel, and a
// controller still pointing at the old instance would silently die.
func (c *PicoChannel) SetSessionModelController(controller SessionModelController) {
	c.sessionModelMu.Lock()
	defer c.sessionModelMu.Unlock()
	c.sessionModelController = controller
}

// getSessionModelController returns the wired controller (nil when unset).
func (c *PicoChannel) getSessionModelController() SessionModelController {
	c.sessionModelMu.RLock()
	defer c.sessionModelMu.RUnlock()
	return c.sessionModelController
}

// rememberUsage stashes the most recent ContextUsage for a chatID so a
// refreshing web client can restore the context ring via
// GET /pico/context-usage (live payloads only reach connected clients).
func (c *PicoChannel) rememberUsage(chatID string, u *bus.ContextUsage) {
	if u == nil || chatID == "" {
		return
	}
	c.lastUsageMu.Lock()
	defer c.lastUsageMu.Unlock()
	c.lastUsage[chatID] = u
}

// lastUsageFor returns the stashed ContextUsage for a chatID, or nil.
func (c *PicoChannel) lastUsageFor(chatID string) *bus.ContextUsage {
	c.lastUsageMu.RLock()
	defer c.lastUsageMu.RUnlock()
	return c.lastUsage[chatID]
}

func (c *PicoChannel) getTurnActiveProbe() func(chatID string) bool {
	c.turnActiveProbeMu.RLock()
	defer c.turnActiveProbeMu.RUnlock()
	return c.turnActiveProbe
}

// createAndAddConnection checks MaxConnections and registers a connection atomically.
func (c *PicoChannel) createAndAddConnection(conn *websocket.Conn, sessionID string, maxConns int) (*picoConn, error) {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()
	if len(c.connections) >= maxConns {
		return nil, channels.ErrTemporary
	}

	var connID string
	for {
		connID = uuid.New().String()
		if _, exists := c.connections[connID]; !exists {
			break
		}
	}

	pc := &picoConn{
		id:         connID,
		conn:       conn,
		sessionID:  sessionID,
		catchingUp: true, // broadcasts buffer until the handshake replay drains (§一)
	}

	c.connections[pc.id] = pc
	bySession, ok := c.sessionConnections[pc.sessionID]
	if !ok {
		bySession = make(map[string]*picoConn)
		c.sessionConnections[pc.sessionID] = bySession
	}
	bySession[pc.id] = pc

	return pc, nil
}

// removeConnection deletes a connection from indexes and returns it when found.
func (c *PicoChannel) removeConnection(connID string) *picoConn {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()

	pc, ok := c.connections[connID]
	if !ok {
		return nil
	}

	delete(c.connections, connID)
	if bySession, ok := c.sessionConnections[pc.sessionID]; ok {
		delete(bySession, connID)
		if len(bySession) == 0 {
			delete(c.sessionConnections, pc.sessionID)
		}
	}

	return pc
}

// takeAllConnections snapshots and clears all connection indexes.
func (c *PicoChannel) takeAllConnections() []*picoConn {
	c.connsMu.Lock()
	defer c.connsMu.Unlock()

	all := make([]*picoConn, 0, len(c.connections))
	for _, pc := range c.connections {
		all = append(all, pc)
	}
	clear(c.connections)
	clear(c.sessionConnections)

	return all
}

// sessionConnectionsSnapshot returns all active connections for a session.
func (c *PicoChannel) sessionConnectionsSnapshot(sessionID string) []*picoConn {
	c.connsMu.RLock()
	defer c.connsMu.RUnlock()

	bySession, ok := c.sessionConnections[sessionID]
	if !ok || len(bySession) == 0 {
		return nil
	}

	conns := make([]*picoConn, 0, len(bySession))
	for _, pc := range bySession {
		conns = append(conns, pc)
	}
	return conns
}

// currentConnCount returns a lock-protected snapshot of active connection count.
func (c *PicoChannel) currentConnCount() int {
	c.connsMu.RLock()
	defer c.connsMu.RUnlock()
	return len(c.connections)
}

// Start implements Channel.
func (c *PicoChannel) Start(ctx context.Context) error {
	logger.InfoC("pico", "Starting Pico Protocol channel")
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	logger.InfoC("pico", "Pico Protocol channel started")
	return nil
}

// Stop implements Channel.
func (c *PicoChannel) Stop(ctx context.Context) error {
	logger.InfoC("pico", "Stopping Pico Protocol channel")
	c.SetRunning(false)

	// Close all connections
	for _, pc := range c.takeAllConnections() {
		pc.close()
	}

	if c.cancel != nil {
		c.cancel()
	}
	if c.progress != nil {
		c.progress.StopAll()
	}

	logger.InfoC("pico", "Pico Protocol channel stopped")
	return nil
}

// WebhookPath implements channels.WebhookHandler.
func (c *PicoChannel) WebhookPath() string { return "/pico/" }
