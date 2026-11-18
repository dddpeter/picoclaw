package pico

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SessionModelController is the agent-side surface the web chat needs to
// bind a model to ONE conversation (fork: session-scoped model selection).
//
// The channel cannot import pkg/agent (the gateway wires the two together),
// so the gateway injects these callbacks.
//
// Contract:
//   - SetModel(sessionID, modelName) assigns the model to the session;
//     an empty model name clears the override (the session falls back to
//     the configured model). It returns an error for unknown models.
//   - GetModel(sessionID) returns the model currently assigned to the
//     session ("" = the session follows the configured model).
type SessionModelController interface {
	SetModel(sessionID, modelName string) error
	GetModel(sessionID string) string
	// ModelWindows returns the effective context window of the session's
	// model and the threshold at which it starts compressing. Both are 0
	// when the session follows the configured model (the client then uses
	// the config default's window).
	ModelWindows(sessionID string) (contextWindow int, compressAt int)
}

// sessionModelHandler serves GET/POST /pico/session-model.
//
// The endpoint is session-scoped by construction: it never touches
// config.json and never changes the agent's global model — it only decides
// which configured model THIS conversation runs on.
func (c *PicoChannel) handleSessionModel(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	controller := c.getSessionModelController()
	if controller == nil {
		writeJSONResponse(w, map[string]any{
			"error": "session model selection is not available",
		})
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		http.Error(w, "missing session parameter", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		model := controller.GetModel(sessionID)
		contextWindow, compressAt := controller.ModelWindows(sessionID)
		writeJSONResponse(w, map[string]any{
			"session":        sessionID,
			"model":          model,
			"context_window": contextWindow,
			"compress_at":    compressAt,
		})
	case http.MethodPost:
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		modelName := strings.TrimSpace(body.Model)
		if err := controller.SetModel(sessionID, modelName); err != nil {
			writeJSONResponse(w, map[string]any{
				"error": err.Error(),
			})
			return
		}
		model := controller.GetModel(sessionID)
		contextWindow, compressAt := controller.ModelWindows(sessionID)
		writeJSONResponse(w, map[string]any{
			"session":        sessionID,
			"model":          model,
			"context_window": contextWindow,
			"compress_at":    compressAt,
		})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
