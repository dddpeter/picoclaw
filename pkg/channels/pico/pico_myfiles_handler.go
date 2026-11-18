package pico

import (
	"net/http"
)

// handleMyFiles implements GET /pico/myfiles — the user's own files across
// all sessions: uploads (inbound index) + files the agent generated
// (recovered from persisted session history). Deliberately NOT the whole
// workspace tree: that is mostly noise (cron jobs, plans, backups).
func (c *PicoChannel) handleMyFiles(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	files := c.collectMyFiles()
	if files == nil {
		files = []MyFile{}
	}
	writeJSONResponse(w, map[string]any{
		"files": files,
		"count": len(files),
	})
}
