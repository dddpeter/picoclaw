package a2a

import (
	"encoding/json"
	"net/http"
)

// CardHandler serves the agent card discovery document
// (GET /.well-known/agent-card.json). Registered on the shared gateway mux
// only when gateway.a2a.enabled is true.
func CardHandler(card AgentCard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(card)
	}
}
