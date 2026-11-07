package snapshot

// Turn-level undo (trae borrowing 增量清单 §2.1): restore workspace files
// from version pre-images. /revert undoes the newest version; /revert <id>
// cascades from that version to the newest, applied newest-first, so the
// workspace ends at the pre-turn state of the target version. Undone
// versions are consumed — re-running /revert walks one turn further back,
// it never replays stale pre-images over newer content.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RevertResult reports what a cascade revert did, including every entry it
// could not restore (honest degradation — never silent).
type RevertResult struct {
	FromID   string      `json:"from_id"` // oldest undone version
	ToID     string      `json:"to_id"`   // newest undone version
	Versions int         `json:"versions"`
	Restored int         `json:"restored"` // files written back / recreated
	Deleted  int         `json:"deleted"`  // files removed (undo of created)
	Skipped  []FileEntry `json:"skipped,omitempty"`
	Notes    []string    `json:"notes,omitempty"`
}

// ErrNoVersions is returned when the session has nothing to revert.
var ErrNoVersions = errors.New("snapshot: no versions recorded for session")

// Revert undoes versions from targetID (inclusive) through the newest,
// newest-first. An empty targetID undoes only the newest version.
func (s *Store) Revert(sessionKey, targetID string) (*RevertResult, error) {
	if targetID != "" && !validVersionID(targetID) {
		return nil, fmt.Errorf("snapshot: invalid version id %q", targetID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	versions, err := s.Versions(sessionKey)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrNoVersions
	}

	start := len(versions) - 1
	if targetID != "" {
		start = -1
		for i, v := range versions {
			if v.ID == targetID {
				start = i
				break
			}
		}
		if start < 0 {
			return nil, fmt.Errorf("snapshot: version %q not found", targetID)
		}
	}

	result := &RevertResult{
		FromID: versions[start].ID,
		ToID:   versions[len(versions)-1].ID,
	}
	// Newest-first: each restore overwrites what newer versions had done,
	// leaving the workspace at the target version's pre-turn state.
	for i := len(versions) - 1; i >= start; i-- {
		v := versions[i]
		result.Versions++
		for _, entry := range v.Files {
			if !entry.Stored && entry.Op != OpCreated {
				// Created entries have no pre-image by definition — they are
				// still revertible (the file gets removed). Everything else
				// without retained content is an honest skip.
				result.Skipped = append(result.Skipped, entry)
				continue
			}
			if err := s.restoreEntry(sessionKey, entry); err != nil {
				result.Notes = append(result.Notes, fmt.Sprintf("restore of %s failed: %v", entry.Rel, err))
				continue
			}
			if entry.Op == OpCreated {
				result.Deleted++
			} else {
				result.Restored++
			}
		}
	}

	// Consume the undone versions so a repeated /revert walks further back
	// instead of replaying stale pre-images over newer content.
	for i := len(versions) - 1; i >= start; i-- {
		if err := s.deleteVersionLocked(sessionKey, versions[i].ID); err != nil {
			result.Notes = append(result.Notes, fmt.Sprintf("cleanup of %s failed: %v", versions[i].ID, err))
		}
	}
	return result, nil
}

// restoreEntry writes back (or removes) one file from its retained
// pre-image. Restores never touch paths outside the recorded set — the
// entries were boundary-validated at capture time.
func (s *Store) restoreEntry(sessionKey string, entry FileEntry) error {
	switch entry.Op {
	case OpCreated:
		err := os.Remove(entry.Path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default: // modified / deleted: write the pre-image back
		data, err := os.ReadFile(preImagePath(s.preDir(sessionKey), entry.SHA256))
		if err != nil {
			return fmt.Errorf("pre-image unavailable: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if info, statErr := os.Stat(entry.Path); statErr == nil {
			perm = info.Mode().Perm()
		}
		// Atomic replace keeps a crashed restore from truncating the target.
		tmp := entry.Path + ".picoclaw-revert.tmp"
		if err := os.WriteFile(tmp, data, perm); err != nil {
			return err
		}
		if err := os.Rename(tmp, entry.Path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
}
