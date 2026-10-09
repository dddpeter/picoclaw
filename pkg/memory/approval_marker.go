package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// Approval markers (fork feature, web approval card). The approval twin of
// turn markers: written when a HITL approval question is published, cleared
// on resolution (reply / timeout / abort). The web session API reads it so a
// page reload mid-approval can restore the pending approval card — the ask
// itself is outbound-only and never enters the transcript, so without this
// marker a reload would lose all evidence of the pending question.

// ApprovalMarker is the on-disk record of one pending approval question.
type ApprovalMarker struct {
	SessionKey string `json:"session_key"`
	AgentID    string `json:"agent_id,omitempty"`
	Tool       string `json:"tool"`
	Preview    string `json:"preview,omitempty"`
	TimeoutMs  int64  `json:"timeout_ms,omitempty"`
	StartedAt  int64  `json:"started_at"` // unix ms
}

// ApprovalMarkerSuffix is exported so directory scanners over the sessions
// dir can skip marker files instead of misreading them as session snapshots.
const ApprovalMarkerSuffix = ".approval.json"

// ApprovalMarkerFile resolves the marker path for a session key within dir.
// Exported for the web session API (same sanitizeKey semantics as the store).
func ApprovalMarkerFile(dir, sessionKey string) string {
	return filepath.Join(dir, sanitizeKey(sessionKey)+ApprovalMarkerSuffix)
}

// WriteApprovalMarker atomically records the pending approval question.
// Overwrites any previous marker: the hook allows one waiter per session, a
// stale marker can only predate the question about to be asked.
func (s *JSONLStore) WriteApprovalMarker(marker ApprovalMarker) error {
	if marker.SessionKey == "" {
		return fmt.Errorf("memory: approval marker needs a session key")
	}
	if marker.StartedAt == 0 {
		marker.StartedAt = nowUnixMilli()
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("memory: encode approval marker: %w", err)
	}
	return fileutil.WriteFileAtomic(ApprovalMarkerFile(s.dir, marker.SessionKey), data, 0o644)
}

// ClearApprovalMarker removes the session's marker; missing is not an error
// (idempotent clear on every resolution path).
func (s *JSONLStore) ClearApprovalMarker(sessionKey string) error {
	if err := os.Remove(ApprovalMarkerFile(s.dir, sessionKey)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("memory: clear approval marker: %w", err)
	}
	return nil
}

// ReadApprovalMarker returns the session's pending approval marker, or nil
// when none survives (resolved or never asked).
func (s *JSONLStore) ReadApprovalMarker(sessionKey string) (*ApprovalMarker, error) {
	return readApprovalMarkerAt(ApprovalMarkerFile(s.dir, sessionKey))
}

func readApprovalMarkerAt(path string) (*ApprovalMarker, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: read approval marker: %w", err)
	}
	var marker ApprovalMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.SessionKey == "" {
		// Corrupt marker: self-heal (same policy as turn markers).
		_ = os.Remove(path)
		return nil, nil
	}
	return &marker, nil
}

func nowUnixMilli() int64 {
	return time.Now().UnixMilli()
}
