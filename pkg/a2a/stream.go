package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Task states per A2A v1.0.
const (
	TaskStateWorking   = "working"
	TaskStateCompleted = "completed"
	TaskStateCanceled  = "canceled"
	TaskStateFailed    = "failed"
)

// Task is the A2A task object streamed as the message/stream result.
type Task struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Kind      string        `json:"kind"`
	Status    TaskStatus    `json:"status"`
	Artifacts []A2AArtifact `json:"artifacts,omitempty"`
	// Reasoning carries the in-progress model thinking snapshot on working
	// frames (ReasoningStreamer). Empty on terminal frames.
	Reasoning string `json:"reasoning,omitempty"`
}

// TaskStatus is the status block of a Task.
type TaskStatus struct {
	State   string      `json:"state"` // working | completed | canceled | failed
	Message *A2AMessage `json:"message,omitempty"`
}

// A2AArtifact carries task output parts.
type A2AArtifact struct {
	Parts []A2APart `json:"parts"`
}

// sseStream is one active message/stream exchange.
type sseStream struct {
	ctxID  string
	taskID string
	events chan string // preformatted SSE data lines (JSON-RPC envelopes)
	done   chan struct{}

	once     sync.Once
	lastText string
	state    string // last TaskStatus.state pushed
	mu       sync.Mutex
}

// Events yields preformatted SSE payload lines ("data: {...}").
func (s *sseStream) Events() <-chan string { return s.events }

// Done closes when the stream is finished (Finalize/Cancel or server shutdown).
func (s *sseStream) Done() <-chan struct{} { return s.done }

// StreamRegistry tracks active message/stream exchanges by contextId and
// implements the bus.StreamDelegate shape so the agent pipeline can push
// full-content snapshots into the SSE stream while the Ask runs.
type StreamRegistry struct {
	mu      sync.Mutex
	streams map[string]*sseStream

	// gates serializes streams per contextId: concurrent message/stream
	// requests QUEUE on a caller-ctx-bounded semaphore instead of being
	// refused with -32000 (refusal + retrying clients = dropped questions,
	// the 19:41 incident).
	gates   map[string]chan struct{}
	gatesMu sync.Mutex
}

// NewStreamRegistry builds the registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{
		streams: make(map[string]*sseStream),
		gates:   make(map[string]chan struct{}),
	}
}

// Acquire waits for exclusive stream rights on a contextId. The wait is
// bounded by ctx: a caller that gives up leaves the queue. The returned
// release func must be called when the stream finishes.
func (r *StreamRegistry) Acquire(ctx context.Context, contextID string) (release func(), ok bool) {
	r.gatesMu.Lock()
	gate, exists := r.gates[contextID]
	if !exists {
		gate = make(chan struct{}, 1)
		r.gates[contextID] = gate
	}
	r.gatesMu.Unlock()

	select {
	case gate <- struct{}{}:
		return func() { <-gate }, true
	case <-ctx.Done():
		return nil, false
	}
}

// Register mounts a stream for a contextId. Returns nil when that context
// already has a live stream — the caller must refuse the new request
// (one turn per context at a time).
func (r *StreamRegistry) Register(contextID, taskID string) *sseStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, live := r.streams[contextID]; live {
		select {
		case <-old.done: // finished earlier; safe to reuse
		default:
			return nil
		}
	}
	s := &sseStream{
		ctxID:  contextID,
		taskID: taskID,
		events: make(chan string, 64),
		done:   make(chan struct{}),
	}
	r.streams[contextID] = s
	return s
}

// Unregister removes the stream mapping.
func (r *StreamRegistry) Unregister(contextID string) {
	r.mu.Lock()
	delete(r.streams, contextID)
	r.mu.Unlock()
}

// UnregisterIf removes the stream mapping only when it still points at the
// given stream (its registrant). The gate is held for the TURN's lifetime,
// so a late handler return can race a successor's registration after the
// gate was already released by the harvester — an unconditional delete
// would tear down the successor's stream mid-flight.
func (r *StreamRegistry) UnregisterIf(contextID string, s *sseStream) {
	r.mu.Lock()
	if r.streams[contextID] == s {
		delete(r.streams, contextID)
	}
	r.mu.Unlock()
}

// CancelStream pushes a canceled Task frame onto the contextId's live
// stream and closes it (tasks/cancel side effect: attached SSE clients see
// the terminal frame; the serveMessageStream loop returns on canceled).
func (r *StreamRegistry) CancelStream(contextID, taskID string) bool {
	r.mu.Lock()
	s, ok := r.streams[contextID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	_ = s.push(Task{
		ID: taskID, ContextID: contextID, Kind: "task",
		Status: TaskStatus{State: TaskStateCanceled},
	})
	s.finish()
	return true
}

// GetStreamer implements bus.StreamDelegate: only the "a2a" channel routes
// here, keyed by chatID (== contextId).
func (r *StreamRegistry) GetStreamer(_ context.Context, channel, chatID, _ string) (bus.Streamer, bool) {
	if channel != "a2a" {
		return nil, false
	}
	r.mu.Lock()
	s, ok := r.streams[chatID]
	r.mu.Unlock()
	logger.InfoCF("a2a", "stream registry lookup", map[string]any{
		"chat_id": chatID, "hit": ok, "tracked": len(r.streams),
	})
	if !ok {
		return nil, false
	}
	return &registryStreamer{s: s}, true
}

// registryStreamer adapts an sseStream to bus.Streamer (the agent pipeline
// pushes full-content snapshots via Update/Finalize).
type registryStreamer struct{ s *sseStream }

func (rs *registryStreamer) Update(_ context.Context, content string) error {
	rs.s.mu.Lock()
	rs.s.lastText = content
	rs.s.mu.Unlock()
	return rs.s.push(Task{
		ID: rs.s.taskID, ContextID: rs.s.ctxID, Kind: "task",
		Status:    TaskStatus{State: TaskStateWorking},
		Artifacts: []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: content}}}},
	})
}

func (rs *registryStreamer) Finalize(_ context.Context, content string) error {
	rs.s.mu.Lock()
	rs.s.lastText = content
	rs.s.mu.Unlock()
	err := rs.s.push(Task{
		ID: rs.s.taskID, ContextID: rs.s.ctxID, Kind: "task",
		Status:    TaskStatus{State: TaskStateCompleted},
		Artifacts: []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: content}}}},
	})
	rs.s.finish()
	return err
}

// UpdateReasoning implements bus.ReasoningStreamer: the thinking snapshot
// rides working frames in a dedicated field, so streaming clients (bridge)
// can render a live process panel like the feishu card's.
func (rs *registryStreamer) UpdateReasoning(_ context.Context, content string) error {
	return rs.s.push(Task{
		ID: rs.s.taskID, ContextID: rs.s.ctxID, Kind: "task",
		Status:    TaskStatus{State: TaskStateWorking},
		Reasoning: content,
	})
}

// FinalizeReasoning implements bus.ReasoningStreamer: the reasoning phase
// is over (answer streaming starts) — push the final thinking snapshot on
// a working frame so clients can close their process panel.
func (rs *registryStreamer) FinalizeReasoning(_ context.Context, content string) error {
	return rs.s.push(Task{
		ID: rs.s.taskID, ContextID: rs.s.ctxID, Kind: "task",
		Status:    TaskStatus{State: TaskStateWorking},
		Reasoning: content,
	})
}

func (rs *registryStreamer) Cancel(_ context.Context) {
	_ = rs.s.push(Task{
		ID: rs.s.taskID, ContextID: rs.s.ctxID, Kind: "task",
		Status: TaskStatus{State: TaskStateCanceled},
	})
	rs.s.finish()
}

// push enqueues one SSE frame; a slow/dead consumer never blocks the agent
// pipeline (drop-on-full: the next snapshot supersedes).
func (s *sseStream) push(task Task) error {
	s.mu.Lock()
	s.state = task.Status.State
	s.mu.Unlock()
	frame, err := json.Marshal(JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  task,
	})
	if err != nil {
		return err
	}
	select {
	case s.events <- "data: " + string(frame):
	default:
	}
	return nil
}

// finish closes the stream exactly once.
func (s *sseStream) finish() {
	s.once.Do(func() { close(s.done) })
}

// HandleMessageStream serves POST message/stream standalone (tests). The
// gateway path enters via Server.Handler's method dispatch instead.
func (s *Server) HandleMessageStream(reg *StreamRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONRPC(w, 0, nil, nil, &JSONRPCError{Code: CodeParseError, Message: err.Error()})
			return
		}
		s.serveMessageStream(w, r, req, reg)
	}
}

// serveMessageStream writes the text/event-stream body: frames are
// JSON-RPC envelopes with Task results. The agent runs in the background
// via AskFunc; pipeline snapshots arrive through the StreamRegistry
// (bus.StreamDelegate), and the final answer closes the stream even when
// the provider ran non-streaming (no Finalize call). Auth and method
// dispatch are handled by the caller (serveJSONRPC).
//
// Lifecycle ownership (2026-11-28 review P1): the HARVESTER goroutine owns
// the ask outcome end to end — tracker completion, idempotency settle,
// gate release — regardless of whether the SSE handler is still alive.
// A client that walks away mid-turn (the whole point of detached
// delivery) must still leave tasks/get with the answer and no leaked
// runCancels entry; the SSE loop below only forwards frames.
func (s *Server) serveMessageStream(w http.ResponseWriter, r *http.Request, req JSONRPCRequest, reg *StreamRegistry) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="picoclaw-a2a"`)
		writeJSONRPC(w, http.StatusUnauthorized, nil, nil, &JSONRPCError{
			Code: http.StatusUnauthorized, Message: "missing or invalid bearer token",
		})
		return
	}
	var params MessageSendParams
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Message == nil {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code: CodeInvalidParams, Message: "params.message (A2AMessage) required",
		})
		return
	}

	contextID := params.Message.ContextID
	if contextID == "" {
		contextID = uuid.NewString()
	}
	taskID := "task-" + uuid.NewString()[:8]

	// Clear the per-request write deadline (shared mux has a short one;
	// review focus 1 from the plan).
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	// Stream idempotency (same contract as message/send): a retry with the
	// same messageId must not start a second turn while the original —
	// possibly detached after a client disconnect — still runs. Leaders run
	// the turn; followers wait (ctx-bounded) and replay the leader's
	// terminal outcome; completed answers are cached for late retries.
	messageID := params.Message.MessageID
	flightID := ""
	if s.idem != nil && messageID != "" {
		if cached, ok := s.idem.Get(messageID); ok {
			s.serveStreamReplay(w, contextID, taskID, cached, nil)
			return
		}
		leader, res, ferr := s.idem.beginSingleFlightCtx(r.Context(), messageID)
		if !leader {
			if r.Context().Err() != nil {
				return // caller gave up while queued; connection gone anyway
			}
			s.serveStreamReplay(w, contextID, taskID, res, ferr)
			return
		}
		flightID = messageID
	}

	// Queue on the per-context gate BEFORE Register: concurrent streams on
	// one contextId serialize here (caller-ctx-bounded), so retrying
	// clients get a normal stream after the current turn instead of -32000.
	// The gate is held for the TURN's lifetime (released by the harvester),
	// not the handler's — a new stream while a detached turn still runs
	// would otherwise start a second concurrent turn on the same session.
	release, acquired := reg.Acquire(r.Context(), contextID)
	if !acquired {
		s.settleStreamFlight(flightID, contextID, nil, fmt.Errorf("caller gave up before stream start"))
		return
	}

	stream := reg.Register(contextID, taskID)
	if stream == nil {
		release()
		s.settleStreamFlight(flightID, contextID, nil, fmt.Errorf("stream aborted: context already streaming"))
		// Defensive: only reachable if registry state drifts from the gate.
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code:    CodeServerError,
			Message: "a message/stream is already running for this contextId",
		})
		return
	}
	defer reg.UnregisterIf(contextID, stream)

	// Task 6: index the task so tasks/get can serve detached watchers.
	s.tracker.Register(taskID, messageID, contextID)

	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if canFlush {
		flusher.Flush()
	}

	// Acceptance frame immediately after headers. Tool-first turns (exec
	// before any text) used to leave the SSE silent past the client's
	// acceptance window — GroupBridge's 30s acceptTimeout surfaced every
	// 虾酱 dispatch as a 1m2s error card while the turn ran fine detached
	// (2026-11-28 M4 V1 live finding). Naming the task up front lets the
	// client stop its acceptance timer; real snapshots follow from the
	// pipeline as before.
	_ = stream.push(Task{
		ID:        taskID,
		ContextID: contextID,
		Kind:      "task",
		Status:    TaskStatus{State: TaskStateWorking},
	})

	// Agent loop in the background; snapshots flow via the registry.
	// Task 6: the turn MUST outlive the HTTP connection — detached clients
	// walk away right after the acceptance frame. askCtx is therefore
	// TASK-scoped (30min guard), NOT request-scoped: handler return on
	// client disconnect must NOT cancel it. The harvester reclaims it.
	askCtx, askCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	s.trackRun(taskID, askCancel) // tasks/cancel stops the turn via this ctx
	askDone := make(chan error, 1)
	var answer string
	go func() {
		ans, err := s.ask(askCtx, params.Message.Text(), "a2a", contextID)
		answer = ans
		askDone <- err
	}()

	// Harvester: single owner of the ask outcome. Runs whether or not the
	// SSE handler is still alive — this is what makes detached delivery
	// actually deliver (tasks/get serves the answer after the client is
	// gone) and what reclaims runCancels and the gate.
	go func() {
		err := <-askDone
		s.untrackRun(taskID)
		askCancel() // reclaim the task-scoped ctx; timeout is the backstop
		if err != nil {
			s.tracker.Fail(taskID, err.Error())
			s.settleStreamFlight(flightID, contextID, nil, err) // failures are not cached
		} else {
			s.tracker.Complete(taskID, answer)
			s.settleStreamFlight(flightID, contextID, answer, nil) // cached for late retries
		}
		// Non-streaming provider: no Finalize arrived — close with the
		// outcome. Skip when the stream already reached a terminal state
		// (canceled via tasks/cancel, completed via pipeline Finalize).
		if state := stream.snapshot(); state != TaskStateCompleted && state != TaskStateCanceled && state != TaskStateFailed {
			if err != nil {
				_ = stream.push(Task{
					ID: taskID, ContextID: contextID, Kind: "task",
					Status: TaskStatus{State: TaskStateFailed,
						Message: &A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: err.Error()}}}},
				})
			} else {
				_ = stream.push(Task{
					ID: taskID, ContextID: contextID, Kind: "task",
					Status:    TaskStatus{State: TaskStateCompleted},
					Artifacts: []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: answer}}}},
				})
			}
		}
		stream.finish()
		release() // turn over: the next stream on this context may start
	}()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case frame := <-stream.events:
			if _, err := fmt.Fprintf(w, "%s\n\n", frame); err != nil {
				stream.finish()
				return
			}
			if canFlush {
				flusher.Flush()
			}
			if strings.Contains(frame, `"state":"completed"`) || strings.Contains(frame, `"state":"canceled"`) || strings.Contains(frame, `"state":"failed"`) {
				// Terminal frame written; the harvester owns the rest
				// (tracker settle, gate release). No further draining
				// needed — the terminal frame is the last one.
				return
			}
		case <-stream.done:
			// Harvester (or CancelStream / pipeline Finalize) closed the
			// stream: drain whatever terminal frames are queued so the
			// client sees the state change, then end.
			drainStreamFrames(w, flusher, canFlush, stream)
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				stream.finish()
				return
			}
			if canFlush {
				flusher.Flush()
			}
		case <-r.Context().Done():
			stream.finish()
			return // the turn and its tracker record live on in the harvester
		}
	}
}

// settleStreamFlight publishes the leader's terminal outcome to the
// idempotency store. flightID is empty when the stream ran without dedup
// (empty messageId or store disabled) — settle is then a no-op.
func (s *Server) settleStreamFlight(flightID, contextID string, result any, err error) {
	if s.idem == nil || flightID == "" {
		return
	}
	if err != nil {
		s.idem.finishSingleFlight(flightID, nil, err)
		return
	}
	s.idem.Put(flightID, result, contextID)
	s.idem.finishSingleFlight(flightID, result, nil)
}

// serveStreamReplay writes a synthetic SSE stream carrying a leader's
// terminal outcome (idempotency follower or late cache hit). The replay's
// task id is fresh and deliberately NOT tracked: the original task record
// — the one a detached watcher polls — already holds the outcome.
func (s *Server) serveStreamReplay(w http.ResponseWriter, contextID, taskID string, result any, askErr error) {
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if canFlush {
		flusher.Flush()
	}
	task := Task{ID: taskID, ContextID: contextID, Kind: "task"}
	if askErr != nil {
		task.Status = TaskStatus{State: TaskStateFailed,
			Message: &A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: askErr.Error()}}}}
	} else {
		task.Status = TaskStatus{State: TaskStateCompleted}
		if ans := replayAnswer(result); ans != "" {
			task.Artifacts = []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: ans}}}}
		}
	}
	frame, err := json.Marshal(JSONRPCResponse{JSONRPC: "2.0", Result: task})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", frame)
	if canFlush {
		flusher.Flush()
	}
}

// replayAnswer extracts text from a cached idempotency result. The stream
// path caches the answer string; a message/send run sharing the same
// messageId caches an A2AMessage — honor both.
func replayAnswer(result any) string {
	switch v := result.(type) {
	case string:
		return v
	case A2AMessage:
		return v.Text()
	case *A2AMessage:
		if v != nil {
			return v.Text()
		}
	}
	return ""
}

// drainStreamFrames flushes whatever frames are already queued so the
// terminating state change reaches the client before the handler returns.
func drainStreamFrames(w http.ResponseWriter, flusher http.Flusher, canFlush bool, stream *sseStream) {
	for {
		select {
		case frame := <-stream.events:
			if _, err := fmt.Fprintf(w, "%s\n\n", frame); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		default:
			return
		}
	}
}

// snapshot returns the last known task state ("" if none).
func (s *sseStream) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// ChainedStreamDelegate tries the A2A registry first (channel "a2a"), then
// falls back to the previous delegate (e.g. the channel manager's pico
// streaming). Gateway wires this over the existing bus delegate when the
// A2A endpoint is enabled.
type ChainedStreamDelegate struct {
	Primary  bus.StreamDelegate
	Fallback bus.StreamDelegate
}

// GetStreamer implements bus.StreamDelegate.
func (c *ChainedStreamDelegate) GetStreamer(ctx context.Context, channel, chatID, sessionKey string) (bus.Streamer, bool) {
	if c.Primary != nil {
		if s, ok := c.Primary.GetStreamer(ctx, channel, chatID, sessionKey); ok {
			return s, true
		}
	}
	if c.Fallback == nil {
		return nil, false
	}
	return c.Fallback.GetStreamer(ctx, channel, chatID, sessionKey)
}

// NotifySteering forwards the mid-turn steering acknowledgement down the
// chain. The chain itself must implement bus.SteeringNotifyDelegate:
// bus.NotifySteering type-asserts the TOP-LEVEL delegate, so without this
// method, enabling the A2A endpoint (which swaps the channel manager out
// for the chain) silently disabled steering acks for every channel.
func (c *ChainedStreamDelegate) NotifySteering(ctx context.Context, channel, chatID, sessionKey, preview string) bool {
	for _, d := range []bus.StreamDelegate{c.Primary, c.Fallback} {
		if sd, ok := d.(bus.SteeringNotifyDelegate); ok && sd != nil {
			return sd.NotifySteering(ctx, channel, chatID, sessionKey, preview)
		}
	}
	return false
}
