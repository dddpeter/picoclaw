package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// Ask markers (fork feature, web ask card — octop borrowing §三). The ask
// twin of approval markers: written when an ask_user question is published,
// cleared on every exit path (answer / skip / timeout / abort). The ask
// itself is outbound-only and never enters the transcript, so without this
// marker a page reload mid-ask would lose the card (the answer routing
// itself is state-driven and keeps working — the marker only restores the
// visualization).

// AskMarker is the on-disk record of one pending ask_user question set.
// Questions is the raw JSON of []bus.AskQuestion (kept opaque here so the
// memory layer does not depend on the bus wire types).
type AskMarker struct {
	SessionKey string          `json:"session_key"`
	AgentID    string          `json:"agent_id,omitempty"`
	Questions  json.RawMessage `json:"questions,omitempty"`
	TimeoutMs  int64           `json:"timeout_ms,omitempty"`
	StartedAt  int64           `json:"started_at"` // unix ms
}

// AskMarkerSuffix is exported so directory scanners over the sessions dir
// can skip marker files instead of misreading them as session snapshots.
const AskMarkerSuffix = ".ask.json"

// AskMarkerFile resolves the marker path for a session key within dir.
// Exported for the web session API (same sanitizeKey semantics as the store).
func AskMarkerFile(dir, sessionKey string) string {
	return filepath.Join(dir, sanitizeKey(sessionKey)+AskMarkerSuffix)
}

// WriteAskMarker atomically records the pending question set. Overwrites
// any previous marker: one waiter per session.
func (s *JSONLStore) WriteAskMarker(marker AskMarker) error {
	if marker.SessionKey == "" {
		return fmt.Errorf("memory: ask marker needs a session key")
	}
	if marker.StartedAt == 0 {
		marker.StartedAt = nowUnixMilli()
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("memory: encode ask marker: %w", err)
	}
	return fileutil.WriteFileAtomic(AskMarkerFile(s.dir, marker.SessionKey), data, 0o644)
}

// ClearAskMarker removes the session's marker; missing is not an error
// (idempotent clear on every resolution path).
func (s *JSONLStore) ClearAskMarker(sessionKey string) error {
	if err := os.Remove(AskMarkerFile(s.dir, sessionKey)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: clear ask marker: %w", err)
	}
	return nil
}

// ReadAskMarker returns the session's pending ask marker, or nil when none
// survives.
func (s *JSONLStore) ReadAskMarker(sessionKey string) (*AskMarker, error) {
	path := AskMarkerFile(s.dir, sessionKey)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: read ask marker: %w", err)
	}
	var marker AskMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.SessionKey == "" {
		// Corrupt marker: self-heal (same policy as the other markers).
		_ = os.Remove(path)
		return nil, nil
	}
	return &marker, nil
}
