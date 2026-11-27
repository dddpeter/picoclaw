package pico

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// picoWriteTimeout bounds every server-side frame write. A wedged peer (TCP
// zero window / half-dead connection) must not hold the delivery chain
// indefinitely: when the deadline expires the write fails and the connection
// is closed, and the read loop's deferred cleanup removes it from the
// broadcast indexes.
const picoWriteTimeout = 10 * time.Second

// maxPendingCatchupFrames caps the per-connection catch-up buffer. A client
// that cannot keep up even in buffered mode is beyond saving: the connection
// is closed and a reconnect replays the missed frames from the seq ring, so
// nothing the seq contract guarantees is lost.
const maxPendingCatchupFrames = 4096

// writeJSON sends a JSON message to the connection with write locking.
func (pc *picoConn) writeJSON(v any) error {
	if pc.closed.Load() {
		return fmt.Errorf("connection closed")
	}
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()
	_ = pc.conn.SetWriteDeadline(time.Now().Add(picoWriteTimeout))
	err := pc.conn.WriteJSON(v)
	_ = pc.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		// The peer is gone or wedged: close the connection so the read
		// loop unblocks and its deferred cleanup drops this conn from the
		// broadcast indexes. Leaving it in place would stall every future
		// frame for the full write timeout.
		pc.close()
		return err
	}
	return nil
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
		if len(pc.pending) >= maxPendingCatchupFrames {
			// Give up on a client that cannot keep up even in buffered
			// mode; the reconnect replays the missed frames from the seq
			// ring (exactly-once contract unchanged).
			pc.close()
			return fmt.Errorf("catch-up pending buffer overflow (%d frames)", len(pc.pending))
		}
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

// ServeHTTP implements http.Handler for the shared HTTP server.
func (c *PicoChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pico")

	switch path {
	case "/ws", "/ws/":
		c.handleWebSocket(w, r)
	case "/upload", "/upload/":
		c.handleUpload(w, r)
	case "/file", "/file/":
		c.handleFileRead(w, r)
	case "/myfiles", "/myfiles/":
		c.handleMyFiles(w, r)
	case "/context-usage", "/context-usage/":
		c.handleContextUsage(w, r)
	case "/session-model", "/session-model/":
		c.handleSessionModel(w, r)
	case "/tree", "/tree/":
		c.handleTree(w, r)
	default:
		if strings.HasPrefix(path, "/media/") {
			c.handleMediaDownload(w, r)
			return
		}
		http.NotFound(w, r)
	}
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

// NotifyTurnEnd implements channels.TurnEndCapable: the agent loop calls
// Manager.InvokeTurnEnd exactly once per dispatched message (all LLM/tool
// iterations included), and this broadcasts the turn-final
// turn.status{active:false} frame. Protocol clients (groupbridge, TUI, web)
// can use it to tell a genuinely finished turn apart from the mid-iteration
// typing.stop beats. Errors are logged and swallowed: the frame is retained
// in the session seq ring for reconnect replay either way.
func (c *PicoChannel) NotifyTurnEnd(chatID string) {
	msg := newMessage(TypeTurnStatus, map[string]any{"active": false})
	if err := c.broadcast(chatID, msg); err != nil && !errors.Is(err, errFrameRetainedNoDelivery) {
		log.Printf("[pico] NotifyTurnEnd broadcast %s: %v", chatID, err)
	}
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
		return fmt.Errorf(
			"no active connections for session %s (%w): %w",
			sessionID,
			errFrameRetainedNoDelivery,
			channels.ErrSendFailed,
		)
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
			_ = pc.conn.SetWriteDeadline(time.Now().Add(picoWriteTimeout))
			err := pc.conn.WriteMessage(websocket.PingMessage, nil)
			_ = pc.conn.SetWriteDeadline(time.Time{})
			pc.writeMu.Unlock()
			if err != nil {
				// A failed ping means the connection is dead or wedged:
				// close it so the read loop's cleanup can remove it.
				pc.close()
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

	// Uploaded file references (web upload pipeline, design §4.4): validate
	// the inbound/ prefix contract, then embed [file:<path>] tags in content
	// so the model gains visibility and the tags persist with the message.
	fileRefs, fileTags, err := c.parseInboundFiles(msg.Payload, c.uploadOptions().workspace)
	if err != nil {
		errMsg := newErrorWithPayload("invalid_files", err.Error(), map[string]any{
			"request_id": msg.ID,
		})
		pc.writeJSON(errMsg)
		return
	}
	content = appendFileTags(content, fileTags)

	if strings.TrimSpace(content) == "" && len(media) == 0 && len(fileRefs) == 0 {
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
	var files []bus.InboundFile
	for _, ref := range fileRefs {
		files = append(files, bus.InboundFile{Path: ref.Path, Filename: ref.Filename, ContentType: ref.ContentType})
	}
	if err := c.HandleInboundContextWithFiles(c.ctx, chatID, content, media, files, inboundCtx, sender); err != nil {
		c.seenIDs.Unseen(sessionID, msg.ID)
		logger.DebugCF("pico", "Inbound delivery failed, idempotency rolled back", map[string]any{
			"session_id": sessionID,
			"request_id": msg.ID,
			"error":      err.Error(),
		})
	}
}
