package a2a

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// TaskTracker indexes long-running tasks so retrying clients that gave up
// on the SSE stream can poll tasks/get instead of losing the answer (fix
// v4: long tasks must not drop the question when the caller times out).
// Terminal records are swept lazily after 1h.
type TaskTracker struct {
	mu    sync.Mutex
	tasks map[string]*taskRecord
	now   func() time.Time
}

type taskRecord struct {
	MessageID string
	ContextID string
	State     string // working | completed | failed
	Answer    string // set on completion
	ErrText   string // set on failure
	updatedAt time.Time
}

// NewTaskTracker builds the tracker.
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{tasks: make(map[string]*taskRecord), now: time.Now}
}

// Register mounts a task as working (called when a message/stream is
// accepted for it).
func (t *TaskTracker) Register(taskID, messageID, contextID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()
	t.tasks[taskID] = &taskRecord{
		MessageID: messageID,
		ContextID: contextID,
		State:     TaskStateWorking,
		updatedAt: t.now(),
	}
}

// Complete records the final answer for a task.
func (t *TaskTracker) Complete(taskID, answer string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.tasks[taskID]; ok {
		r.State = TaskStateCompleted
		r.Answer = answer
		r.updatedAt = t.now()
	}
}

// Fail records a terminal failure for a task.
func (t *TaskTracker) Fail(taskID, errText string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.tasks[taskID]; ok {
		r.State = TaskStateFailed
		r.ErrText = errText
		r.updatedAt = t.now()
	}
}

// Get returns (state, text, contextID, found). text is the completed
// answer or the failure message; empty while working.
func (t *TaskTracker) Get(taskID string) (state, text, contextID string, found bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.tasks[taskID]
	if !ok {
		return "", "", "", false
	}
	switch r.State {
	case TaskStateCompleted:
		return r.State, r.Answer, r.ContextID, true
	case TaskStateFailed:
		return r.State, r.ErrText, r.ContextID, true
	default:
		return r.State, "", r.ContextID, true
	}
}

// sweepLocked drops terminal records older than an hour (lazy, on writes).
func (t *TaskTracker) sweepLocked() {
	cutoff := t.now().Add(-time.Hour)
	for id, r := range t.tasks {
		if r.State != TaskStateWorking && r.updatedAt.Before(cutoff) {
			delete(t.tasks, id)
		}
	}
}

// serveTasksGet answers the A2A standard tasks/get method: the bridge's
// watcher polls it after detaching from the SSE stream (task 6 detached
// delivery). Result is a Task snapshot; unknown ids get -32002.
func (s *Server) serveTasksGet(w http.ResponseWriter, r *http.Request, req JSONRPCRequest) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="picoclaw-a2a"`)
		writeJSONRPC(w, http.StatusUnauthorized, nil, nil, &JSONRPCError{
			Code: http.StatusUnauthorized, Message: "missing or invalid bearer token",
		})
		return
	}
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code: CodeInvalidParams, Message: "params.id (task id) required",
		})
		return
	}
	state, text, contextID, found := s.tracker.Get(params.ID)
	if !found {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code:    -32002,
			Message: "task not found: " + params.ID,
		})
		return
	}
	task := Task{ID: params.ID, ContextID: contextID, Kind: "task"}
	switch state {
	case TaskStateCompleted:
		task.Status = TaskStatus{State: TaskStateCompleted}
		if text != "" {
			task.Artifacts = []A2AArtifact{{Parts: []A2APart{{Kind: "text", Text: text}}}}
		}
	case TaskStateFailed:
		task.Status = TaskStatus{State: TaskStateFailed,
			Message: &A2AMessage{Role: "agent", Parts: []A2APart{{Kind: "text", Text: text}}}}
	default:
		task.Status = TaskStatus{State: state}
	}
	writeJSONRPC(w, 0, req.ID, task, nil)
}
