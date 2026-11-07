package api

// Workspace checkpoint query endpoints (trae borrowing 增量清单 §2.1):
// read-only access to the snapshot store the gateway writes. The launcher
// resolves the same on-disk manifests — writes stay gateway-owned.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/sipeed/picoclaw/pkg/snapshot"
)

// handleSessionVersions lists the workspace checkpoint versions recorded for
// a session, oldest first. Versions carry their file entries (path/op/stored)
// so a UI can show what each turn touched before offering a revert.
//
//	GET /api/sessions/{id}/versions?channel=
func (h *Handler) handleSessionVersions(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	dir, _, err := h.sessionRuntimeSettings()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}
	ref, err := h.findJSONLSession(dir, sessionID, r.URL.Query().Get("channel"))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	store, err := snapshot.NewStore(snapshot.DefaultRoot(), snapshot.Config{})
	if err != nil {
		http.Error(w, "failed to open checkpoint store", http.StatusInternalServerError)
		return
	}
	versions, err := store.Versions(ref.Key)
	if err != nil {
		http.Error(w, "failed to list checkpoint versions", http.StatusInternalServerError)
		return
	}
	if versions == nil {
		versions = []snapshot.Version{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":       sessionID,
		"versions": versions,
	})
}

// handleSessionVersionDiff serves the masked unified diff of one committed
// version (diffs.txt). Masking happens at commit time — the raw pre-images
// are only ever read back for byte-exact restore, never served.
//
//	GET /api/sessions/{id}/versions/{version}/diff?channel=
func (h *Handler) handleSessionVersionDiff(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	versionID := r.PathValue("version")
	if sessionID == "" || versionID == "" {
		http.Error(w, "missing session or version id", http.StatusBadRequest)
		return
	}
	dir, _, err := h.sessionRuntimeSettings()
	if err != nil {
		http.Error(w, "failed to resolve sessions directory", http.StatusInternalServerError)
		return
	}
	ref, err := h.findJSONLSession(dir, sessionID, r.URL.Query().Get("channel"))
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	store, err := snapshot.NewStore(snapshot.DefaultRoot(), snapshot.Config{})
	if err != nil {
		http.Error(w, "failed to open checkpoint store", http.StatusInternalServerError)
		return
	}
	if _, err := store.Version(ref.Key, versionID); err != nil {
		http.Error(w, "version not found", http.StatusNotFound)
		return
	}
	data, err := store.ReadDiff(ref.Key, versionID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "diff not available for this version", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to read diff", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}
