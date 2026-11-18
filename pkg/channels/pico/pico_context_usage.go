package pico

import (
	"net/http"
	"strings"
)

// handleContextUsage implements GET /pico/context-usage — the most recent
// ContextUsage for a session, so a refreshing web client can restore the
// context ring (live payloads only reach currently-connected clients).
func (c *PicoChannel) handleContextUsage(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	if sessionID == "" {
		http.Error(w, "missing session parameter", http.StatusBadRequest)
		return
	}
	chatID := sessionID
	if !strings.Contains(chatID, ":") {
		chatID = "pico:" + chatID
	}

	usage := c.lastUsageFor(chatID)
	if usage == nil {
		// No turn has run (or gateway restarted): empty 200, the client
		// simply keeps the ring hidden.
		writeJSONResponse(w, map[string]any{"usage": nil})
		return
	}
	writeJSONResponse(w, map[string]any{"usage": usage})
}
