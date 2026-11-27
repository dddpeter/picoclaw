package a2a

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AskFunc is the only agent-loop entry the A2A server needs; the gateway
// wires it to AgentLoop.ProcessDirectWithChannel (channel always "a2a",
// chatID == the A2A contextId, session key derived from the contextId so
// conversation context persists across calls — no new session mechanism).
type AskFunc func(ctx context.Context, prompt, channel, chatID string) (string, error)

// Options tunes the server.
type Options struct {
	// Token requires callers to present "Authorization: Bearer <token>".
	// Empty = loopback-only access (127.0.0.1 / ::1 may call without auth).
	Token string
}

// Server serves A2A message/send on the shared gateway mux.
type Server struct {
	ask   AskFunc
	token string

	mu       sync.Mutex
	contexts map[string]*sync.Mutex // per-context serialization
}

// NewServer builds the A2A JSON-RPC server.
func NewServer(ask AskFunc, opts Options) *Server {
	return &Server{
		ask:      ask,
		token:    opts.Token,
		contexts: make(map[string]*sync.Mutex),
	}
}

// Handler returns the http.Handler for POST <base-url> JSON-RPC traffic.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveJSONRPC)
}

// contextLock returns (and memoizes) the mutex serializing one contextId.
func (s *Server) contextLock(contextID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.contexts[contextID]
	if !ok {
		m = &sync.Mutex{}
		s.contexts[contextID] = m
	}
	return m
}

func (s *Server) serveJSONRPC(w http.ResponseWriter, r *http.Request) {
	// Review focus 1: this server sits behind the shared health mux whose
	// http.Server sets WriteTimeout (5s). A synchronous agent turn can run
	// for minutes — clear this request's write deadline so the middleware
	// stack never cuts the answer short. (ReadTimeout stays: the small
	// JSON-RPC body is read long before the turn finishes.)
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONRPC(w, 0, nil, nil, &JSONRPCError{
			Code: http.StatusMethodNotAllowed, Message: "A2A endpoint accepts POST only",
		})
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="picoclaw-a2a"`)
		writeJSONRPC(w, http.StatusUnauthorized, nil, nil, &JSONRPCError{
			Code: http.StatusUnauthorized, Message: "missing or invalid bearer token",
		})
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPC(w, 0, nil, nil, &JSONRPCError{Code: CodeParseError, Message: err.Error()})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code: CodeInvalidRequest, Message: "jsonrpc must be 2.0 with a method",
		})
		return
	}
	if req.Method != "message/send" {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("method %q not supported; this MVP serves message/send only", req.Method),
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
		contextID = uuid.NewString() // first call: mint and echo back
	}

	// Review focus 5: serialize per context so concurrent same-context
	// requests keep turn order instead of interleaving into one session.
	lock := s.contextLock(contextID)
	lock.Lock()
	answer, err := s.ask(r.Context(), params.Message.Text(), "a2a", contextID)
	lock.Unlock()
	if err != nil {
		writeJSONRPC(w, 0, req.ID, nil, &JSONRPCError{
			Code:    CodeServerError,
			Message: "message/send failed: " + err.Error(),
		})
		return
	}

	result := A2AMessage{
		Role:      "agent",
		Parts:     []A2APart{{Kind: "text", Text: answer}},
		MessageID: uuid.NewString(),
		ContextID: contextID,
	}
	writeJSONRPC(w, 0, req.ID, result, nil)
}

// authorized enforces review focus 4: with a token configured, non-loopback
// callers MUST present it — failures never reach the agent loop. Loopback
// (127.0.0.1 / ::1) callers are exempt so local tools work without config.
func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return isLoopback(r.RemoteAddr) // no token: loopback only
	}
	if isLoopback(r.RemoteAddr) {
		return true // local callers trusted even without the header
	}
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	return strings.HasPrefix(auth, prefix) && constantTimeEqual(auth[len(prefix):], s.token)
}

func isLoopback(remoteAddr string) bool {
	host := remoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1"
}

// constantTimeEqual avoids leaking token prefix matches by timing.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeJSONRPC(w http.ResponseWriter, status int, id json.RawMessage, result any, rpcErr *JSONRPCError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	resp := JSONRPCResponse{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
