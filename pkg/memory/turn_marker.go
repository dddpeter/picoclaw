package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// Turn markers (fork feature, pkg/agent/restart_recovery.go). A turn writes
// its marker before doing any work (write-ahead) and clears it on every exit
// path. After a gateway restart a surviving marker means a turn was in flight
// when the process died — including the case that leaves a clean tail (killed
// mid-LLM-generation, before any message of the turn persisted) and which the
// dangling-tool-call seal alone cannot detect. The marker also records the
// model the turn was using so recovery can restore it.

// TurnMarker is the on-disk record of one in-flight turn.
type TurnMarker struct {
	SessionKey string    `json:"session_key"`
	AgentID    string    `json:"agent_id,omitempty"`
	Model      string    `json:"model,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

// TurnMarkerSuffix is the marker filename suffix. Exported so every
// directory scanner over the sessions dir (migration, legacy session
// manager) can skip marker files instead of misreading them as session
// snapshots — a marker renamed to ".migrated" by the migrator would be
// lost to restart recovery.
const TurnMarkerSuffix = ".turnmarker.json"

func (s *JSONLStore) turnMarkerPath(key string) string {
	return filepath.Join(s.dir, sanitizeKey(key)+TurnMarkerSuffix)
}

// WriteTurnMarker atomically records the in-flight turn for a session.
// Overwrites any previous marker: sessions run turns strictly sequentially,
// so a stale marker can only predate the turn about to start.
func (s *JSONLStore) WriteTurnMarker(sessionKey, agentID, model string) error {
	marker := TurnMarker{
		SessionKey: sessionKey,
		AgentID:    agentID,
		Model:      model,
		StartedAt:  time.Now().UTC(),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("memory: encode turn marker: %w", err)
	}
	return fileutil.WriteFileAtomic(s.turnMarkerPath(sessionKey), data, 0o644)
}

// ClearTurnMarker removes the session's marker; a missing marker is not an
// error (idempotent clear on turn-exit paths).
func (s *JSONLStore) ClearTurnMarker(sessionKey string) error {
	if err := os.Remove(s.turnMarkerPath(sessionKey)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: clear turn marker: %w", err)
	}
	return nil
}

// ListTurnMarkers returns every surviving marker in the store directory.
// Corrupt marker files are removed (self-healing): an unparseable marker can
// never be acted on and would otherwise linger forever.
func (s *JSONLStore) ListTurnMarkers() ([]TurnMarker, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: list turn markers: %w", err)
	}
	var markers []TurnMarker
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), TurnMarkerSuffix) {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var marker TurnMarker
		if err := json.Unmarshal(data, &marker); err != nil || marker.SessionKey == "" {
			_ = os.Remove(path)
			continue
		}
		markers = append(markers, marker)
	}
	return markers, nil
}
