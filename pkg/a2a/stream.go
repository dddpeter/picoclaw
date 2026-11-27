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
	ID        string       `json:"id"`
	ContextID string       `json:"contextId"`
	Kind      string       `json:"kind"`
	Status    TaskStatus   `json:"status"`
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

	// Queue on the per-context gate BEFORE Register: concurrent streams on
	// one contextId serialize here (caller-ctx-bounded), so retrying
	// clients get a normal stream after the current turn instead of -32000.
	release, acquired := reg.Acquire(r.Context(), contextID)
	if !acquired {
		// Caller gave up while queued — connection is gone anyway.
		return
	}
	defer release()

	stream := reg.Register(contextID, taskID)
	if stream == nil {
		// Defensive: only reachable if registry state drifts from the gate.
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code:    CodeServerError,
			Message: "a message/stream is already running for this contextId",
		})
		return
	}
	defer reg.Unregister(contextID)

	// Task 6: index the task so tasks/get can serve detached watchers.
	s.tracker.Register(taskID, params.Message.MessageID, contextID)

	flusher, canFlush := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if canFlush {
		flusher.Flush()
	}

	// Agent loop in the background; snapshots flow via the registry.
	// Task 6: the turn MUST outlive the HTTP connection — detached clients
	// walk away right after the acceptance frame. askCtx is therefore
	// TASK-scoped (30min guard), NOT request-scoped: handler returns on
	// client disconnect must NOT cancel it. askDone reclaims it.
	askCtx, askCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	askDone := make(chan error, 1)
	var answer string
	go func() {
		ans, err := s.ask(askCtx, params.Message.Text(), "a2a", contextID)
		answer = ans
		askDone <- err
	}()

	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case err := <-askDone:
			askCancel() // reclaim the task-scoped ctx; timeout is the backstop
			if err != nil {
				s.tracker.Fail(taskID, err.Error())
				_ = stream.push(Task{
					ID: taskID, ContextID: contextID, Kind: "task",
					Status: TaskStatus{State: TaskStateFailed,
						Message: &A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: err.Error()}}}},
				})
			} else if stream.snapshot() != TaskStateCompleted {
				// Non-streaming provider: no Finalize arrived — close with the answer.
				s.tracker.Complete(taskID, answer)
				_ = stream.push(Task{
					ID: taskID, ContextID: contextID, Kind: "task",
					Status:    TaskStatus{State: TaskStateCompleted},
					Artifacts: []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: answer}}}},
				})
			} else {
				s.tracker.Complete(taskID, answer)
			}
			// The final frames were pushed onto the channel; without this
			// drain they die with the handler (return would race the select
			// and the queued terminating frame would never reach the client).
			drainStreamFrames(w, flusher, canFlush, stream)
			stream.finish()
			return
		case frame := <-stream.events:
			if _, err := fmt.Fprintf(w, "%s\n\n", frame); err != nil {
				stream.finish()
				return
			}
			if canFlush {
				flusher.Flush()
			}
			if strings.Contains(frame, `"state":"completed"`) || strings.Contains(frame, `"state":"canceled"`) {
				stream.finish()
				return
			}
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
			return
		}
	}
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
