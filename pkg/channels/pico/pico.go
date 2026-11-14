package pico

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
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

var allowedInlineImageMIMETypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/webp": {},
	"image/bmp":  {},
}

func outboundMessageIsThought(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindThought)
}

func outboundMessageIsToolFeedback(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), "tool_feedback")
}

func outboundMessageIsToolCalls(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindToolCalls)
}

// outboundMessageIsProgressNote reports whether the message is a non-final
// beat rather than the turn's final answer: an explicit progress_note mark
// (heartbeat / stall-watchdog notice) or the pico-scoped receipt marker
// (approval/ask answer receipts — the model keeps generating behind them).
// Approval/ask CARDS are deliberately NOT marked here: older pico clients
// (the TUI) render progress_note as a status-line-only beat and would drop
// the card from the timeline — the web frontend derives the non-final
// semantics from the ask/approval payload itself instead (review P1-2).
func outboundMessageIsProgressNote(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindProgressNote) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["pico_kind"]), MessageKindProgressNote)
}

func outboundMessageFinalizesTrackedToolFeedback(msg bus.OutboundMessage) bool {
	return !outboundMessageIsToolFeedback(msg) &&
		!outboundMessageIsThought(msg) &&
		!outboundMessageIsToolCalls(msg) &&
		!outboundMessageIsProgressNote(msg)
}

// writeJSON sends a JSON message to the connection with write locking.
func (pc *picoConn) writeJSON(v any) error {
	if pc.closed.Load() {
		return fmt.Errorf("connection closed")
	}
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	return pc.conn.WriteJSON(v)
}

// deliver hands a session frame to this connection preserving per-connection
// order (trae borrowing §一, exactly-once contract). While the connection is
// catching up — between registration and the end of its handshake replay —
// frames are buffered instead of written, so a live broadcast can never
// interleave with, jump ahead of, or duplicate replayed frames.
func (pc *picoConn) deliver(msg PicoMessage) error {
	pc.deliveryMu.Lock()
	defer pc.deliveryMu.Unlock()
	if pc.catchingUp {
		pc.pending = append(pc.pending, msg)
		return nil
	}
	return pc.writeJSON(msg)
}

// endCatchup writes the replayed frames, leaves catch-up mode, then flushes
// the frames broadcast during the replay — replay first, then pending, then
// live delivers, strictly ordered. Frames already covered by the replay
// (seq <= maxReplayed: assigned between registration and the replay snapshot)
// are skipped, so every frame reaches the client exactly once. Seq-less
// frames (seq 0) never enter the ring and are always flushed.
func (pc *picoConn) endCatchup(replay []PicoMessage, maxReplayed uint64) error {
	pc.deliveryMu.Lock()
	defer pc.deliveryMu.Unlock()

	for i := range replay {
		if pc.closed.Load() {
			return fmt.Errorf("connection closed")
		}
		if err := pc.writeJSON(replay[i]); err != nil {
			return err
		}
	}
	pending := pc.pending
	pc.pending = nil
	pc.catchingUp = false
	for i := range pending {
		if pc.closed.Load() {
			return fmt.Errorf("connection closed")
		}
		if pending[i].Seq != 0 && pending[i].Seq <= maxReplayed {
			continue // already delivered as part of the replay
		}
		if err := pc.writeJSON(pending[i]); err != nil {
			return err
		}
	}
	return nil
}

// close closes the connection.
func (pc *picoConn) close() {
	if pc.closed.CompareAndSwap(false, true) {
		if pc.cancel != nil {
			pc.cancel()
		}
		pc.conn.Close()
	}
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

// ServeHTTP implements http.Handler for the shared HTTP server.
func (c *PicoChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pico")

	switch path {
	case "/ws", "/ws/":
		c.handleWebSocket(w, r)
	default:
		if strings.HasPrefix(path, "/media/") {
			c.handleMediaDownload(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

// a2uiSurfaceCap bounds the live A2UI surface registry (LRU) so a
// long-running gateway does not leak per-surface state.
const a2uiSurfaceCap = 64

// a2uiSurfaceKey scopes a live surface to its session: chatID is the pico
// session routing key ("pico:<sessionID>", the same key broadcastToSession
// routes on), surfaceID is the batch's single surface id (UIMeta.SurfaceID,
// enforced non-empty by the render_ui gates). Two sessions reusing the same
// surfaceId string never collide.
type a2uiSurfaceKey struct {
	chatID    string
	surfaceID string
}

// a2uiSurfaceState is one registered live surface: the message id the
// create batch was broadcast under (message.update targets it) plus the
// accumulated frames broadcast so far (create frames + merged update
// frames) so the next update merges against the full live state.
type a2uiSurfaceState struct {
	messageID string
	content   string
	frames    []any
}

// a2uiSurfaceRegistry is a bounded LRU map of live A2UI surfaces. Entries
// are in-memory only: a gateway restart clears the registry and later
// update-only batches degrade to fresh message.create cards (accepted
// boundary — the frontend shows a new card instead of a lost orphan).
//
// Concurrency contract: Send's caller (the manager worker) drains the
// channel queue with a single goroutine, so Sends — and hence the
// lookup→broadcast→record sequence around this registry — are serialized
// per channel today. That sequence is deliberately NOT atomic (merge and
// record run outside the registry lock): concurrentizing Send requires
// moving the merge into the lock as an atomic commit, or interleaved
// create/update batches can lose deltas.
type a2uiSurfaceRegistry struct {
	mu      sync.Mutex
	entries map[a2uiSurfaceKey]*a2uiSurfaceState
	// order holds the registry keys oldest-first (LRU eviction order).
	// Touch (lookup or record) moves a key to the back.
	order []a2uiSurfaceKey
}

func newA2uiSurfaceRegistry() *a2uiSurfaceRegistry {
	return &a2uiSurfaceRegistry{entries: make(map[a2uiSurfaceKey]*a2uiSurfaceState)}
}

// lookup returns a snapshot of the registered state for (chatID, surfaceID)
// and marks it most recently used. Nil registry (hand-constructed channel
// in tests) reports a miss so updates degrade to creates.
func (r *a2uiSurfaceRegistry) lookup(chatID, surfaceID string) (a2uiSurfaceState, bool) {
	if r == nil || surfaceID == "" {
		return a2uiSurfaceState{}, false
	}
	key := a2uiSurfaceKey{chatID, surfaceID}
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.entries[key]
	if !ok {
		return a2uiSurfaceState{}, false
	}
	r.touchLocked(key)
	return *state, true
}

// record inserts or replaces the entry for (chatID, surfaceID) and marks it
// most recently used, evicting the least recently used entry when the cap
// is exceeded. A repeated create for the same surface replaces the entry
// (the last card takes over subsequent merges).
func (r *a2uiSurfaceRegistry) record(chatID, surfaceID, messageID, content string, frames []any) {
	if r == nil || surfaceID == "" {
		return
	}
	key := a2uiSurfaceKey{chatID, surfaceID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[key]; !exists && len(r.entries) >= a2uiSurfaceCap {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	r.entries[key] = &a2uiSurfaceState{messageID: messageID, content: content, frames: frames}
	r.touchLocked(key)
}

// touchLocked moves key to the back of the LRU order. Caller holds r.mu.
func (r *a2uiSurfaceRegistry) touchLocked(key a2uiSurfaceKey) {
	for i, k := range r.order {
		if k == key {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	r.order = append(r.order, key)
}

// batchHasCreateSurface reports whether a decoded A2UI batch carries a
// createSurface frame, i.e. it establishes a new card rather than updating
// a registered one.
func batchHasCreateSurface(frames []any) bool {
	for _, frame := range frames {
		if msg, ok := frame.(map[string]any); ok {
			if _, ok := msg["createSurface"]; ok {
				return true
			}
		}
	}
	return false
}

// mergeA2UIFrames merges an update-only batch's frames into a live surface's
// accumulated frames: components/delete frames append; updateDataModel
// frames overwrite the frame targeting the same path in place (last value
// wins). This mirrors the history-replay fold semantics (pkg/tools
// RenderUIHistoryFold) so live cards and replayed cards converge on the
// same final state. Reimplemented locally instead of exporting the tools
// helper: the fold pipeline is batch/history-oriented (decodes persisted
// call args, derives fallbacks, emits at create positions) while the live
// path accumulates already-broadcast frames — the shared rule is these few
// lines, pinned by tests on both sides. The accumulated slice is copied
// first so the registry's stored frames are never mutated through the
// caller's copy (a failed broadcast must not corrupt the registry state).
func mergeA2UIFrames(acc []any, update []any) []any {
	merged := make([]any, len(acc), len(acc)+len(update))
	copy(merged, acc)
	for _, frame := range update {
		if !overwriteDataModelFrame(merged, frame) {
			merged = append(merged, frame)
		}
	}
	return merged
}

// overwriteDataModelFrame replaces an existing updateDataModel frame with
// the same path in frames (last value wins) and reports whether it did.
// Non-updateDataModel frames never overwrite.
func overwriteDataModelFrame(frames []any, frame any) bool {
	msg, ok := frame.(map[string]any)
	if !ok {
		return false
	}
	dm, ok := msg["updateDataModel"].(map[string]any)
	if !ok {
		return false
	}
	path, _ := dm["path"].(string)
	for i, existing := range frames {
		exMsg, ok := existing.(map[string]any)
		if !ok {
			continue
		}
		exDM, ok := exMsg["updateDataModel"].(map[string]any)
		if !ok {
			continue
		}
		if exPath, _ := exDM["path"].(string); exPath == path {
			frames[i] = frame
			return true
		}
	}
	return false
}

// Send implements Channel — sends a message to the appropriate WebSocket connection.
func (c *PicoChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	isThought := outboundMessageIsThought(msg)
	isToolFeedback := outboundMessageIsToolFeedback(msg)
	isToolCalls := outboundMessageIsToolCalls(msg)
	isProgressNote := outboundMessageIsProgressNote(msg)
	if isToolFeedback {
		if msgID, handled, err := c.progress.Update(ctx, msg.ChatID, msg.Content); handled {
			if err != nil {
				return nil, err
			}
			return []string{msgID}, nil
		}
	}
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(msg.ChatID)
	// NOTE: an A2UI card message counts as "finalizes tracked tool feedback"
	// (it is a plain message), so a live tracked feedback would swallow it
	// here — FinalizeToolFeedbackMessage edits the feedback message with
	// content only and returns before the a2ui block below. Unreachable
	// today: the agent never publishes pre-execution tool feedback on the
	// pico channel (publishToolFeedback guards `ts.channel != "pico"`), so
	// hasTrackedMsg is always false for pico sends; the tracker exists only
	// for the shared animator interfaces.
	if outboundMessageFinalizesTrackedToolFeedback(msg) {
		if msgIDs, handled := c.FinalizeToolFeedbackMessage(ctx, msg); handled {
			return msgIDs, nil
		}
	}

	content := msg.Content
	if isToolFeedback {
		content = channels.InitialAnimatedToolFeedbackContent(msg.Content)
	}
	msgID := uuid.New().String()

	payload := map[string]any{
		PayloadKeyContent: content,
		"message_id":      msgID,
	}
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		payload[PayloadKeyModelName] = modelName
	}
	switch {
	case isThought:
		payload[PayloadKeyKind] = MessageKindThought

		// This field is kept solely for compatibility with legacy pico clients that
		// do not yet support the newer "kind" field.
		// DO NOT use it for any purpose other than legacy client compatibility.
		payload[PayloadKeyThought] = true

	case isToolCalls:
		payload[PayloadKeyKind] = MessageKindToolCalls
		if toolCalls, ok := picoToolCallsPayload(msg); ok {
			payload[PayloadKeyToolCalls] = toolCalls
		}

	case isProgressNote:
		// Non-final progress beat: mark it so clients (web UI) do not
		// mistake it for the turn's final answer and clear their
		// generating state early.
		payload[PayloadKeyKind] = MessageKindProgressNote
	}
	if msg.Approval != nil {
		payload[PayloadKeyApproval] = map[string]any{
			"tool":       msg.Approval.Tool,
			"preview":    msg.Approval.Preview,
			"timeout_ms": msg.Approval.TimeoutMs,
		}
	}
	if msg.Ask != nil {
		questions := make([]any, 0, len(msg.Ask.Questions))
		for _, q := range msg.Ask.Questions {
			opts := make([]any, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, map[string]any{"label": o.Label, "description": o.Description})
			}
			questions = append(questions, map[string]any{
				"question":     q.Question,
				"header":       q.Header,
				"options":      opts,
				"multi_select": q.MultiSelect,
			})
		}
		payload[PayloadKeyAsk] = map[string]any{
			"questions":  questions,
			"timeout_ms": msg.Ask.TimeoutMs,
		}
	}
	var uiFrames []any
	uiSurfaceID := ""
	if msg.UI != nil {
		// A2UI pass-through (A2UI web render): the payload map is
		// map[string]any, so decode the raw message array into a generic
		// value first. The Go side never interprets the structure; the
		// frontend zod layer absorbs schema evolution.
		if err := json.Unmarshal(msg.UI.Messages, &uiFrames); err == nil {
			payload[PayloadKeyA2UI] = uiFrames
			uiSurfaceID = strings.TrimSpace(msg.UI.SurfaceID)
		}
	}
	setContextUsagePayload(payload, msg.ContextUsage)

	// A2UI streamed update (phase 2, task 5): an update-only batch (no
	// createSurface) targets a card this channel already broadcast — merge
	// its frames into the registered card's accumulated frames and
	// hot-update that exact message via message.update (the frontend swaps
	// the card data in place) instead of orphaning a second card.
	if uiSurfaceID != "" && len(uiFrames) > 0 && !batchHasCreateSurface(uiFrames) {
		if state, ok := c.a2uiSurfaces.lookup(msg.ChatID, uiSurfaceID); ok {
			merged := mergeA2UIFrames(state.frames, uiFrames)
			// The card caption stays the create call's content (matches
			// the history fold pinning the caption on the establishing
			// call); editMessagePayload would otherwise blank it.
			updatePayload := map[string]any{
				PayloadKeyContent: state.content,
				PayloadKeyA2UI:    merged,
			}
			if err := c.editMessagePayload(ctx, msg.ChatID, state.messageID, updatePayload, nil); err != nil && !errors.Is(err, errFrameRetainedNoDelivery) {
				// Real failure — the frame never entered the session seq
				// ring, so no client will ever receive it. Keep the
				// pre-merge registry frames so the delta stays retryable
				// instead of being absorbed invisibly.
				return nil, err
			}
			// The frame is in the seq ring regardless of live delivery: a
			// zero-connection send is replayed on reconnect (trae borrowing
			// §一), so commit the merged frames to keep the registry and
			// the replayed state in agreement.
			c.a2uiSurfaces.record(msg.ChatID, uiSurfaceID, state.messageID, state.content, merged)
			return []string{state.messageID}, nil
		}
		// Unknown surfaceId (registry miss — e.g. after a gateway restart
		// cleared it): an orphan update-only batch can NOT become a card.
		// The batch carries no createSurface frame, so the frontend A2UI
		// message processor rejects every frame with "Surface not found"
		// and renders a permanent error placeholder — and registering it
		// would only chain further updates into that dead card. Align with
		// the history-fold orphan semantics instead: publish the fallback
		// text only (no a2ui key) and leave the surface unregistered, so a
		// later create batch starts a clean card.
		delete(payload, PayloadKeyA2UI)
	}

	outMsg := newMessage(TypeMessageCreate, payload)

	if err := c.broadcastToSession(msg.ChatID, outMsg); err != nil {
		retained := errors.Is(err, errFrameRetainedNoDelivery)
		if !retained || uiSurfaceID == "" {
			// Real failure (frame never entered the seq ring), or a
			// non-A2UI message: surface the error unchanged.
			return nil, err
		}
		// Ring-retained A2UI create: a reconnecting client replays the
		// frame, so registration must commit to stay consistent with the
		// replayed state — and Send reports success, because the publish
		// did land (in the ring); reporting failure would make render_ui
		// re-publish and duplicate the card.
	}

	// Register the surface once the create frame is committed to the seq
	// ring (delivered live or retained for replay) so a never-sent frame
	// never leaves an entry pointing at a message nobody received. Orphan
	// update-only batches dropped the a2ui key above — they publish
	// fallback text only and stay unregistered.
	if uiSurfaceID != "" && len(uiFrames) > 0 && payload[PayloadKeyA2UI] != nil {
		c.a2uiSurfaces.record(msg.ChatID, uiSurfaceID, msgID, content, uiFrames)
	}
	if isToolFeedback {
		c.RecordToolFeedbackMessage(msg.ChatID, msgID, msg.Content)
	} else if hasTrackedMsg && outboundMessageFinalizesTrackedToolFeedback(msg) {
		c.dismissTrackedToolFeedbackMessage(ctx, msg.ChatID, trackedMsgID)
	}
	return []string{msgID}, nil
}

// EditMessage implements channels.MessageEditor.
func (c *PicoChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	return c.editMessage(ctx, chatID, messageID, content, nil)
}

func (c *PicoChannel) EditMessageWithPayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, payload, nil)
}

// DeleteMessage implements channels.MessageDeleter.
func (c *PicoChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	outMsg := newMessage(TypeMessageDelete, map[string]any{
		"message_id": messageID,
	})
	return c.broadcastToSession(chatID, outMsg)
}

// BroadcastRevert reports a completed /revert workspace restore to every
// connection of the session (trae borrowing 增量清单 §2.1). Session-level
// broadcast: the frame carries a seq and enters the replay ring, so a client
// that refreshes after the revert still learns the workspace changed.
func (c *PicoChannel) BroadcastRevert(chatID string, payload map[string]any) error {
	outMsg := newMessage(TypeRevertStatus, payload)
	return c.broadcastToSession(chatID, outMsg)
}

func (c *PicoChannel) currentToolFeedbackMessage(chatID string) (string, bool) {
	if c.progress == nil {
		return "", false
	}
	return c.progress.Current(chatID)
}

func (c *PicoChannel) takeToolFeedbackMessage(chatID string) (string, string, bool) {
	if c.progress == nil {
		return "", "", false
	}
	return c.progress.Take(chatID)
}

func (c *PicoChannel) RecordToolFeedbackMessage(chatID, messageID, content string) {
	if c.progress == nil {
		return
	}
	c.progress.Record(chatID, messageID, content)
}

func (c *PicoChannel) ClearToolFeedbackMessage(chatID string) {
	if c.progress == nil {
		return
	}
	c.progress.Clear(chatID)
}

func (c *PicoChannel) DismissToolFeedbackMessage(ctx context.Context, chatID string) {
	msgID, ok := c.currentToolFeedbackMessage(chatID)
	if !ok {
		return
	}
	c.dismissTrackedToolFeedbackMessage(ctx, chatID, msgID)
}

func (c *PicoChannel) dismissTrackedToolFeedbackMessage(ctx context.Context, chatID, messageID string) {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(messageID) == "" {
		return
	}
	c.ClearToolFeedbackMessage(chatID)
	deleteFn := c.deleteMessageFn
	if deleteFn == nil {
		deleteFn = c.DeleteMessage
	}
	_ = deleteFn(ctx, chatID, messageID)
}

func (c *PicoChannel) finalizeTrackedToolFeedbackMessage(
	ctx context.Context,
	chatID string,
	content string,
	editFn func(context.Context, string, string, map[string]any, *bus.ContextUsage) error,
	payload map[string]any,
	contextUsage *bus.ContextUsage,
) ([]string, bool) {
	msgID, baseContent, ok := c.takeToolFeedbackMessage(chatID)
	if !ok || editFn == nil {
		return nil, false
	}
	if payload == nil {
		payload = map[string]any{
			PayloadKeyContent: content,
		}
	}
	if _, ok := payload[PayloadKeyContent]; !ok {
		payload[PayloadKeyContent] = content
	}
	if err := editFn(ctx, chatID, msgID, payload, contextUsage); err != nil {
		c.RecordToolFeedbackMessage(chatID, msgID, baseContent)
		return nil, false
	}
	return []string{msgID}, true
}

func (c *PicoChannel) FinalizeToolFeedbackMessage(ctx context.Context, msg bus.OutboundMessage) ([]string, bool) {
	if !outboundMessageFinalizesTrackedToolFeedback(msg) {
		return nil, false
	}
	payload := map[string]any{
		PayloadKeyContent: msg.Content,
	}
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		payload[PayloadKeyModelName] = modelName
	}
	return c.finalizeTrackedToolFeedbackMessage(
		ctx,
		msg.ChatID,
		msg.Content,
		c.editMessagePayload,
		payload,
		msg.ContextUsage,
	)
}

// StartTyping implements channels.TypingCapable.
func (c *PicoChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	startMsg := newMessage(TypeTypingStart, nil)
	if err := c.broadcastToSession(chatID, startMsg); err != nil {
		return func() {}, err
	}
	return func() {
		stopMsg := newMessage(TypeTypingStop, nil)
		c.broadcastToSession(chatID, stopMsg)
	}, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message via the Pico Protocol that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *PicoChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.bc.Placeholder.Enabled {
		return "", nil
	}

	text := c.bc.Placeholder.GetRandomText()

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		PayloadKeyContent:     text,
		PayloadKeyPlaceholder: true,
		"message_id":          msgID,
	})

	if err := c.broadcastToSession(chatID, outMsg); err != nil {
		return "", err
	}

	return msgID, nil
}

// BeginStream implements channels.StreamingCapable for Pico WebUI.
func (c *PicoChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if c == nil || c.config == nil || !c.config.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	streamCfg := c.config.Streaming.WithDefaults(0, 1)
	return &picoStreamer{
		channel:          c,
		chatID:           chatID,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}

type picoStreamer struct {
	channel          *PicoChannel
	chatID           string
	modelName        string
	turnUsage        providers.UsageInfo
	turnLLMCalls     int
	messageID        string
	reasoningID      string
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	lastContent      string
	reasoningLastLen int
	reasoningLastAt  time.Time
	reasoningContent string
	mu               sync.Mutex
}

func (s *picoStreamer) SetModelName(modelName string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelName = strings.TrimSpace(modelName)
}

// SetTurnUsage records the real per-turn LLM token usage to emit on finalize.
func (s *picoStreamer) SetTurnUsage(usage providers.UsageInfo) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnUsage = usage
}

// SetTurnLLMCalls records the per-turn LLM API call count to emit on finalize
// (web status bar "API ×n"; parity with the feishu card footer).
func (s *picoStreamer) SetTurnLLMCalls(n int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnLLMCalls = n
}

func (s *picoStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content, false, nil)
}

func (s *picoStreamer) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

func (s *picoStreamer) FinalizeWithContext(ctx context.Context, content string, contextUsage *bus.ContextUsage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content, true, contextUsage)
}

func (s *picoStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateReasoningLocked(ctx, content, false)
}

func (s *picoStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateReasoningLocked(ctx, content, true)
}

func (s *picoStreamer) Cancel(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel == nil || s.messageID == "" {
		if s.channel != nil && s.reasoningID != "" {
			_ = s.channel.DeleteMessage(ctx, s.chatID, s.reasoningID)
			s.reasoningID = ""
		}
		return
	}
	_ = s.channel.DeleteMessage(ctx, s.chatID, s.messageID)
	s.messageID = ""
	if s.reasoningID != "" {
		_ = s.channel.DeleteMessage(ctx, s.chatID, s.reasoningID)
		s.reasoningID = ""
	}
}

func (s *picoStreamer) updateLocked(
	ctx context.Context,
	content string,
	force bool,
	contextUsage *bus.ContextUsage,
) error {
	if s == nil || s.channel == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	if strings.TrimSpace(content) == "" && s.messageID == "" {
		return nil
	}

	now := time.Now()
	contentLen := len([]rune(content))
	if s.messageID != "" && !force {
		growth := contentLen - s.lastLen
		if now.Sub(s.lastAt) < s.throttleInterval || growth < s.minGrowth {
			return nil
		}
	}

	return s.sendLocked(ctx, content, contextUsage)
}

func (s *picoStreamer) updateReasoningLocked(ctx context.Context, content string, force bool) error {
	if s == nil || s.channel == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	if strings.TrimSpace(content) == "" && s.reasoningID == "" {
		return nil
	}

	now := time.Now()
	contentLen := len([]rune(content))
	if s.reasoningID != "" && !force {
		growth := contentLen - s.reasoningLastLen
		if now.Sub(s.reasoningLastAt) < s.throttleInterval || growth < s.minGrowth {
			return nil
		}
	}

	return s.sendReasoningLocked(ctx, content)
}

func (s *picoStreamer) sendLocked(ctx context.Context, content string, contextUsage *bus.ContextUsage) error {
	now := time.Now()
	contentLen := len([]rune(content))

	if s.messageID == "" {
		s.messageID = uuid.New().String()
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.messageID,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		setContextUsagePayload(payload, contextUsage)
		setTurnUsagePayload(payload, s.turnUsage, s.turnLLMCalls)
		outMsg := newMessage(TypeMessageCreate, payload)
		if err := s.channel.broadcast(s.chatID, outMsg); err != nil {
			return err
		}
	} else if content != s.lastContent || contextUsage != nil {
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.messageID,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		setTurnUsagePayload(payload, s.turnUsage, s.turnLLMCalls)
		if err := s.channel.editMessagePayload(ctx, s.chatID, s.messageID, payload, contextUsage); err != nil {
			return err
		}
	}

	s.lastContent = content
	s.lastLen = contentLen
	s.lastAt = now
	return nil
}

func (s *picoStreamer) sendReasoningLocked(ctx context.Context, content string) error {
	now := time.Now()
	contentLen := len([]rune(content))

	if s.reasoningID == "" {
		s.reasoningID = uuid.New().String()
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.reasoningID,
			PayloadKeyKind:    MessageKindThought,
			PayloadKeyThought: true,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		outMsg := newMessage(TypeMessageCreate, payload)
		if err := s.channel.broadcastToSession(s.chatID, outMsg); err != nil {
			return err
		}
	} else if content != s.reasoningContent {
		payload := map[string]any{
			PayloadKeyContent: content,
			"message_id":      s.reasoningID,
			PayloadKeyKind:    MessageKindThought,
			PayloadKeyThought: true,
		}
		if s.modelName != "" {
			payload[PayloadKeyModelName] = s.modelName
		}
		outMsg := newMessage(TypeMessageUpdate, payload)
		if err := s.channel.broadcastToSession(s.chatID, outMsg); err != nil {
			return err
		}
	}

	s.reasoningContent = content
	s.reasoningLastLen = contentLen
	s.reasoningLastAt = now
	return nil
}

// SendMedia implements channels.MediaSender for the Pico web UI.
// Media is delivered as a normal assistant message carrying structured
// attachments plus an authenticated same-origin download URL.
func (c *PicoChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(msg.ChatID)

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	attachments := make([]map[string]any, 0, len(msg.Parts))
	caption := ""

	for _, part := range msg.Parts {
		localPath, meta, err := store.ResolveWithMeta(part.Ref)
		if err != nil {
			logger.ErrorCF("pico", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		filename := strings.TrimSpace(part.Filename)
		if filename == "" {
			filename = strings.TrimSpace(meta.Filename)
		}
		if filename == "" {
			filename = filepath.Base(localPath)
		}

		contentType := strings.TrimSpace(part.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(meta.ContentType)
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		attachmentType := strings.TrimSpace(part.Type)
		if attachmentType == "" {
			attachmentType = picoInferAttachmentType(filename, contentType)
		}

		attachmentURL, err := picoDownloadURLForRef(part.Ref)
		if err != nil {
			logger.ErrorCF("pico", "Failed to build media download URL", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		attachments = append(attachments, map[string]any{
			"type":         attachmentType,
			"url":          attachmentURL,
			"filename":     filename,
			"content_type": contentType,
		})

		if caption == "" && strings.TrimSpace(part.Caption) != "" {
			caption = strings.TrimSpace(part.Caption)
		}
	}

	if len(attachments) == 0 {
		return nil, fmt.Errorf("no deliverable media parts: %w", channels.ErrSendFailed)
	}

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		PayloadKeyContent: caption,
		"attachments":     attachments,
		"message_id":      msgID,
	})
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		outMsg.Payload[PayloadKeyModelName] = modelName
	}

	if err := c.broadcastToSession(msg.ChatID, outMsg); err != nil {
		return nil, err
	}
	if hasTrackedMsg {
		c.dismissTrackedToolFeedbackMessage(ctx, msg.ChatID, trackedMsgID)
	}

	return []string{msgID}, nil
}

func picoDownloadURLForRef(ref string) (string, error) {
	refID, err := picoMediaRefID(ref)
	if err != nil {
		return "", err
	}
	return "/pico/media/" + url.PathEscape(refID), nil
}

func picoMediaRefID(ref string) (string, error) {
	refID := strings.TrimSpace(strings.TrimPrefix(ref, "media://"))
	if refID == "" || strings.Contains(refID, "/") {
		return "", fmt.Errorf("invalid media ref %q", ref)
	}
	return refID, nil
}

func picoInferAttachmentType(filename, contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	filename = strings.ToLower(strings.TrimSpace(filename))

	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	}

	switch ext := filepath.Ext(filename); ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	default:
		return "file"
	}
}

func picoAllowsInlineDisplay(filename, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	filename = strings.ToLower(strings.TrimSpace(filename))

	if strings.Contains(contentType, "svg") || filepath.Ext(filename) == ".svg" {
		return false
	}

	return picoInferAttachmentType(filename, contentType) == "image"
}

func (c *PicoChannel) handleMediaDownload(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	refID := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/pico/media/"), "/"))
	if refID == "" {
		http.NotFound(w, r)
		return
	}

	store := c.GetMediaStore()
	if store == nil {
		http.Error(w, "media store unavailable", http.StatusServiceUnavailable)
		return
	}

	localPath, meta, err := store.ResolveWithMeta("media://" + refID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	file, err := os.Open(localPath)
	if err != nil {
		http.Error(w, "failed to open media", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		http.Error(w, "failed to stat media", http.StatusInternalServerError)
		return
	}

	filename := strings.TrimSpace(meta.Filename)
	if filename == "" {
		filename = filepath.Base(localPath)
	}
	contentType := strings.TrimSpace(meta.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	dispositionType := "attachment"
	if picoAllowsInlineDisplay(filename, contentType) {
		dispositionType = "inline"
	}

	if cd := mime.FormatMediaType(dispositionType, map[string]string{"filename": filename}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, filename, info.ModTime(), file)
}

// broadcast routes through broadcastFn when set (tests), else broadcastToSession.
func (c *PicoChannel) broadcast(chatID string, msg PicoMessage) error {
	if c.broadcastFn != nil {
		return c.broadcastFn(chatID, msg)
	}
	return c.broadcastToSession(chatID, msg)
}

// errFrameRetainedNoDelivery reports that an outbound frame was retained in
// the session seq ring (trae borrowing §一) but no live connection accepted
// it at send time — a reconnecting client replays it from the ring. Callers
// that keep follow-up state (the A2UI surface registry) must treat ring
// retention as delivery: the replayed frame and the committed state have to
// agree, so they commit against a retained frame just like a delivered one.
// It always chains channels.ErrSendFailed so existing errors.Is checks keep
// working.
var errFrameRetainedNoDelivery = errors.New("frame retained in session seq ring")

// broadcastToSession sends a message to all connections with a matching session.
func (c *PicoChannel) broadcastToSession(chatID string, msg PicoMessage) error {
	// chatID format: "pico:<sessionID>"
	sessionID := strings.TrimPrefix(chatID, "pico:")
	msg.SessionID = sessionID

	// Trae borrowing §一: stamp the monotonic seq and retain the frame in
	// the ring before delivery. Frames whose broadcast fails below (no
	// active connections) stay replayable for reconnecting clients — the
	// exact case that previously dropped stream deltas forever.
	c.seqs.assign(sessionID, &msg)

	var sent bool
	for _, pc := range c.sessionConnectionsSnapshot(sessionID) {
		// deliver buffers (counts as sent) while a connection is catching
		// up, writes directly otherwise — see picoConn.deliver.
		if err := pc.deliver(msg); err != nil {
			logger.DebugCF("pico", "Write to connection failed", map[string]any{
				"conn_id": pc.id,
				"error":   err.Error(),
			})
		} else {
			sent = true
		}
	}

	if !sent {
		return fmt.Errorf("no active connections for session %s (%w): %w", sessionID, errFrameRetainedNoDelivery, channels.ErrSendFailed)
	}
	return nil
}

// handleWebSocket upgrades the HTTP connection and manages the WebSocket lifecycle.
func (c *PicoChannel) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}

	// Authenticate
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check connection limit
	maxConns := c.config.MaxConnections
	if maxConns <= 0 {
		maxConns = 100
	}
	if c.currentConnCount() >= maxConns {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}

	// Echo the matched subprotocol back so the browser accepts the upgrade.
	var responseHeader http.Header
	if proto := c.matchedSubprotocol(r); proto != "" {
		responseHeader = http.Header{"Sec-WebSocket-Protocol": {proto}}
	}

	conn, err := c.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		logger.ErrorCF("pico", "WebSocket upgrade failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	// Determine session ID from query param or generate one
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	pc, err := c.createAndAddConnection(conn, sessionID, maxConns)
	if err != nil {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "too many connections"),
			time.Now().Add(2*time.Second),
		)
		_ = conn.Close()
		return
	}

	logger.InfoCF("pico", "WebSocket client connected", map[string]any{
		"conn_id":    pc.id,
		"session_id": sessionID,
	})

	// Reconnect signal (octop borrowing §四): the first frame tells the
	// client whether a turn is live for its session, so a page refresh no
	// longer loses the typing state. Weak-resume semantics: the client keeps
	// receiving subsequent frames; missed chunks are covered by history
	// replay after the turn ends.
	if probe := c.getTurnActiveProbe(); probe != nil {
		status := newMessage(TypeTurnStatus, map[string]any{"active": probe("pico:" + sessionID)})
		status.SessionID = sessionID
		pc.writeJSON(status)
	}

	// Catch-up replay (trae borrowing §一): after turn.status, replay the
	// frames the client missed while disconnected. Client presents its last
	// seen seq via ?last_seq=N; 0/absent means "start from now". Runs even
	// without a cursor: endCatchup also drains the frames broadcast since
	// this connection registered (they were buffered, not written).
	c.replayCatchup(pc, sessionID, parseLastSeqQuery(r))

	go c.readLoop(pc)
}

// parseLastSeqQuery extracts the catch-up cursor from the handshake query.
// Invalid or missing values yield 0 (no replay).
func parseLastSeqQuery(r *http.Request) uint64 {
	raw := strings.TrimSpace(r.URL.Query().Get("last_seq"))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// replayCatchup hands the missed frames to the connection via endCatchup
// (replay, then the frames broadcast during it, strictly ordered — other
// connections already received everything live), then a stream.catchup
// boundary frame when part of the gap was evicted.
func (c *PicoChannel) replayCatchup(pc *picoConn, sessionID string, lastSeq uint64) {
	replay := seqReplay{complete: true}
	if lastSeq > 0 {
		replay = c.seqs.replaySince(sessionID, lastSeq)
	}
	maxReplayed := uint64(0)
	if n := len(replay.frames); n > 0 {
		maxReplayed = replay.frames[n-1].Seq
	}
	if err := pc.endCatchup(replay.frames, maxReplayed); err != nil {
		return
	}
	if lastSeq > 0 && !replay.complete {
		boundary := newMessage(TypeStreamCatchup, map[string]any{
			"last_seq": lastSeq,
			"complete": false,
			"dropped":  replay.dropped,
		})
		boundary.SessionID = sessionID
		_ = pc.writeJSON(boundary)
	}
}

// authenticate checks the request for a valid token:
//  1. Authorization: Bearer <token> header
//  2. Sec-WebSocket-Protocol "token.<value>" (for browsers that can't set headers)
//  3. Query parameter "token" (only when AllowTokenQuery is on)
func (c *PicoChannel) authenticate(r *http.Request) bool {
	token := c.config.Token.String()
	if token == "" {
		return false
	}

	// Check Authorization header
	auth := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if after == token {
			return true
		}
	}

	// Check Sec-WebSocket-Protocol subprotocol ("token.<value>")
	if c.matchedSubprotocol(r) != "" {
		return true
	}

	// Check query parameter only when explicitly allowed
	if c.config.AllowTokenQuery {
		if r.URL.Query().Get("token") == token {
			return true
		}
	}

	return false
}

// matchedSubprotocol returns the "token.<value>" subprotocol that matches
// the configured token, or "" if none do.
func (c *PicoChannel) matchedSubprotocol(r *http.Request) string {
	token := c.config.Token.String()
	for _, proto := range websocket.Subprotocols(r) {
		if after, ok := strings.CutPrefix(proto, "token."); ok && after == token {
			return proto
		}
	}
	return ""
}

// readLoop reads messages from a WebSocket connection.
func (c *PicoChannel) readLoop(pc *picoConn) {
	defer func() {
		pc.close()
		if removed := c.removeConnection(pc.id); removed != nil {
			logger.InfoCF("pico", "WebSocket client disconnected", map[string]any{
				"conn_id":    removed.id,
				"session_id": removed.sessionID,
			})
		}
	}()

	readTimeout := time.Duration(c.config.ReadTimeout) * time.Second
	if readTimeout <= 0 {
		readTimeout = 60 * time.Second
	}

	_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
	pc.conn.SetPongHandler(func(appData string) error {
		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	// Start ping ticker
	pingInterval := time.Duration(c.config.PingInterval) * time.Second
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	go c.pingLoop(pc, pingInterval)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_, rawMsg, err := pc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.DebugCF("pico", "WebSocket read error", map[string]any{
					"conn_id": pc.id,
					"error":   err.Error(),
				})
			}
			return
		}

		_ = pc.conn.SetReadDeadline(time.Now().Add(readTimeout))

		var msg PicoMessage
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			errMsg := newError("invalid_message", "failed to parse message")
			pc.writeJSON(errMsg)
			continue
		}

		c.handleMessage(pc, msg)
	}
}

// pingLoop sends periodic ping frames to keep the connection alive.
func (c *PicoChannel) pingLoop(pc *picoConn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if pc.closed.Load() {
				return
			}
			pc.writeMu.Lock()
			err := pc.conn.WriteMessage(websocket.PingMessage, nil)
			pc.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// handleMessage processes an inbound Pico Protocol message.
func (c *PicoChannel) handleMessage(pc *picoConn, msg PicoMessage) {
	switch msg.Type {
	case TypePing:
		pong := newMessage(TypePong, nil)
		pong.ID = msg.ID
		pc.writeJSON(pong)

	case TypeMessageSend:
		c.handleMessageSend(pc, msg)

	case TypeMediaSend:
		c.handleMessageSend(pc, msg)

	default:
		errMsg := newError("unknown_type", fmt.Sprintf("unknown message type: %s", msg.Type))
		pc.writeJSON(errMsg)
	}
}

// handleMessageSend processes an inbound message.send from a client.
func (c *PicoChannel) handleMessageSend(pc *picoConn, msg PicoMessage) {
	content, _ := msg.Payload["content"].(string)
	media, err := parseInlineImageMedia(msg.Payload)
	if err != nil {
		errMsg := newErrorWithPayload("invalid_media", err.Error(), map[string]any{
			"request_id": msg.ID,
		})
		pc.writeJSON(errMsg)
		return
	}

	if strings.TrimSpace(content) == "" && len(media) == 0 {
		errMsg := newErrorWithPayload("empty_content", "message content is empty", map[string]any{
			"request_id": msg.ID,
		})
		pc.writeJSON(errMsg)
		return
	}

	sessionID := msg.SessionID
	if sessionID == "" {
		sessionID = pc.sessionID
	}

	chatID := "pico:" + sessionID
	senderID := "pico-user"

	metadata := map[string]string{
		"platform":   "pico",
		"session_id": sessionID,
		"conn_id":    pc.id,
	}

	logger.DebugCF("pico", "Received message", map[string]any{
		"session_id": sessionID,
		"preview":    truncate(content, 50),
		"media":      len(media),
	})

	sender := bus.SenderInfo{
		Platform:    "pico",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("pico", senderID),
	}

	if !c.IsAllowedSender(sender) {
		return
	}

	// Inbound idempotency (trae borrowing 增量清单 §2.2): after validation
	// and the sender whitelist, so failed validations and unauthorized
	// senders never consume window slots. A repeated ID is acknowledged on
	// the same connection instead of dispatching a second turn.
	if msg.ID != "" && c.seenIDs.Seen(sessionID, msg.ID) {
		ack := newMessage(TypeMessageAck, map[string]any{
			"request_id": msg.ID,
			"duplicate":  true,
		})
		ack.ID = msg.ID
		pc.writeJSON(ack)
		return
	}

	inboundCtx := bus.InboundContext{
		Channel:   "pico",
		ChatID:    chatID,
		ChatType:  "direct",
		SenderID:  senderID,
		MessageID: msg.ID,
		Raw:       metadata,
	}

	// Delivery failure used to be swallowed silently; roll the idempotency
	// record back so a client retry with the same ID can still be delivered
	// instead of being acked as a duplicate.
	if err := c.HandleInboundContext(c.ctx, chatID, content, media, inboundCtx, sender); err != nil {
		c.seenIDs.Unseen(sessionID, msg.ID)
		logger.DebugCF("pico", "Inbound delivery failed, idempotency rolled back", map[string]any{
			"session_id": sessionID,
			"request_id": msg.ID,
			"error":      err.Error(),
		})
	}
}

// truncate truncates a string to maxLen runes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func parseInlineImageMedia(payload map[string]any) ([]string, error) {
	if len(payload) == 0 {
		return nil, nil
	}

	media, err := parseInlineImageValues(payload["media"])
	if err != nil {
		return nil, err
	}

	attachments, err := parseInlineImageAttachments(payload["attachments"])
	if err != nil {
		return nil, err
	}
	media = append(media, attachments...)

	return media, nil
}

func parseInlineImageValues(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch values := raw.(type) {
	case []any:
		media := make([]string, 0, len(values))
		for i, item := range values {
			value, err := inlineImageValue(item)
			if err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case []string:
		media := make([]string, 0, len(values))
		for i, value := range values {
			value = strings.TrimSpace(value)
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case string:
		value := strings.TrimSpace(values)
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, err
		}
		return []string{value}, nil
	default:
		return nil, fmt.Errorf("media must be a string or array of strings")
	}
}

func parseInlineImageAttachments(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}

	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}

	media := make([]string, 0, len(values))
	for i, item := range values {
		attachment, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachments[%d]: attachment must be an object", i)
		}

		attachmentType, _ := attachment["type"].(string)
		attachmentType = strings.ToLower(strings.TrimSpace(attachmentType))
		if attachmentType != "" && attachmentType != "image" {
			continue
		}

		value, err := inlineImageValue(attachment)
		if err != nil {
			if attachmentType == "image" {
				return nil, fmt.Errorf("attachments[%d]: %w", i, err)
			}
			continue
		}
		if !strings.HasPrefix(value, "data:") {
			continue
		}
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, fmt.Errorf("attachments[%d]: %w", i, err)
		}
		media = append(media, value)
	}
	return media, nil
}

func inlineImageValue(item any) (string, error) {
	switch value := item.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("image payload is empty")
		}
		return value, nil
	case map[string]any:
		for _, key := range []string{"url", "data_url"} {
			if raw, ok := value[key].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw), nil
			}
		}
		return "", fmt.Errorf("image payload must include url or data_url")
	default:
		return "", fmt.Errorf("image payload must be a string or object")
	}
}

func validateInlineImageDataURL(mediaURL string) error {
	if mediaURL == "" {
		return fmt.Errorf("image payload is empty")
	}
	if !strings.HasPrefix(mediaURL, "data:image/") {
		return fmt.Errorf("only inline image data URLs are supported")
	}

	header, data, found := strings.Cut(mediaURL, ",")
	if !found || strings.TrimSpace(data) == "" {
		return fmt.Errorf("image data URL is malformed")
	}
	if !strings.Contains(header, ";base64") {
		return fmt.Errorf("image data URL must be base64 encoded")
	}
	mimeType, _, _ := strings.Cut(strings.TrimPrefix(header, "data:"), ";")
	if _, ok := allowedInlineImageMIMETypes[mimeType]; !ok {
		return fmt.Errorf("unsupported image format: %s", mimeType)
	}

	data = strings.TrimSpace(data)
	if base64.StdEncoding.DecodedLen(len(data)) > config.DefaultMaxMediaSize {
		return fmt.Errorf("image exceeds %d byte limit", config.DefaultMaxMediaSize)
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return fmt.Errorf("invalid base64 image data")
	}

	return nil
}

// setContextUsagePayload adds context window usage stats to a pico payload.
func setContextUsagePayload(payload map[string]any, u *bus.ContextUsage) {
	if u == nil {
		return
	}
	usage := map[string]any{
		"used_tokens":         u.UsedTokens,
		"total_tokens":        u.TotalTokens,
		"history_tokens":      u.HistoryTokens,
		"compress_at_tokens":  u.CompressAtTokens,
		"summarize_at_tokens": u.SummarizeAtTokens,
		"used_percent":        u.UsedPercent,
	}
	// Segments (octop borrowing §五): the web ring's stacked breakdown reads
	// context_usage.segments — dropping them here left the whole feature dead
	// on the wire (review P1-6).
	if len(u.Segments) > 0 {
		segments := make([]map[string]any, 0, len(u.Segments))
		for _, seg := range u.Segments {
			segments = append(segments, map[string]any{
				"key":    seg.Key,
				"tokens": seg.Tokens,
			})
		}
		usage["segments"] = segments
	}
	payload["context_usage"] = usage
}

// setTurnUsagePayload attaches real per-turn LLM token usage to the payload.
// Input and output are kept separate (billed at different rates); total is a
// convenience sum; llm_calls is the per-turn API call count for the web status
// bar; cached_tokens is the prompt-cache hit count inside input_tokens
// (omitted when the upstream reports none). Omitted entirely when all counts
// are zero.
func setTurnUsagePayload(payload map[string]any, usage providers.UsageInfo, llmCalls int) {
	if usage.PromptTokens <= 0 && usage.CompletionTokens <= 0 && llmCalls <= 0 {
		return
	}
	u := map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
		"total_tokens":  usage.PromptTokens + usage.CompletionTokens,
	}
	if cached := usage.CachedTokens(); cached > 0 {
		u["cached_tokens"] = cached
	}
	if llmCalls > 0 {
		u["llm_calls"] = llmCalls
	}
	payload[PayloadKeyUsage] = u
}

func picoToolCallsPayload(msg bus.OutboundMessage) ([]utils.VisibleToolCall, bool) {
	raw := strings.TrimSpace(msg.Context.Raw[PayloadKeyToolCalls])
	if raw == "" {
		return nil, false
	}

	var toolCalls []utils.VisibleToolCall
	if err := json.Unmarshal([]byte(raw), &toolCalls); err != nil || len(toolCalls) == 0 {
		return nil, false
	}
	return toolCalls, true
}

func (c *PicoChannel) editMessage(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, map[string]any{
		PayloadKeyContent: content,
	}, contextUsage)
}

func (c *PicoChannel) editMessagePayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
	contextUsage *bus.ContextUsage,
) error {
	if payload == nil {
		payload = map[string]any{}
	}
	normalized := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		normalized[key] = value
	}
	if _, ok := normalized[PayloadKeyContent]; !ok {
		normalized[PayloadKeyContent] = ""
	}
	normalized["message_id"] = messageID
	setContextUsagePayload(normalized, contextUsage)
	outMsg := newMessage(TypeMessageUpdate, normalized)
	return c.broadcastToSession(chatID, outMsg)
}
