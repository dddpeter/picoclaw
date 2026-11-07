// Package snapshot implements workspace checkpointing + turn-level undo
// (trae borrowing 增量清单 §2.1): per-turn records of pre-write file
// contents under ~/.picoclaw/checkpoints/, so /revert can restore files the
// agent changed.
//
// Storage model ("pre-write copy + manifest", no git): each committed turn
// becomes a version directory holding a manifest.json (the authoritative
// record) and a masked diffs.txt; pre-write contents live content-addressed
// in <session>/pre/<sha256>.bin, deduplicated across versions. There is no
// separate index to corrupt — the version list is derived from manifests —
// and pre-images orphaned by a crashed turn are swept by age at store open.
//
// Locking discipline (AGENTS.md): the store owns one mutex and never touches
// connsMu or any model-state lock. Capture runs on the turn goroutine inside
// BeforeTool (bounded by the hook's own timeout); commit runs on the deferred
// agent.turn.end observer (small JSON writes — heavy content reads happen at
// capture time).
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Op classifies what the turn did to a file relative to the pre-turn state.
type Op string

const (
	OpModified Op = "modified"
	OpCreated  Op = "created"
	OpDeleted  Op = "deleted"
)

// Config bounds the store. Zero values are replaced by the Effective*
// methods; every limit degrades honestly (entries beyond a cap are recorded
// with stored=false + reason, never silently dropped).
type Config struct {
	MaxVersions   int
	MaxTotalBytes int64
	MaxFileSize   int64
	TrackExec     bool
	ScanDepth     int
	ScanMaxFiles  int
}

func (c Config) effectiveMaxVersions() int {
	if c.MaxVersions <= 0 {
		return 20
	}
	return c.MaxVersions
}

func (c Config) effectiveMaxTotalBytes() int64 {
	if c.MaxTotalBytes <= 0 {
		return 128 << 20
	}
	return c.MaxTotalBytes
}

func (c Config) effectiveMaxFileSize() int64 {
	if c.MaxFileSize <= 0 {
		return 4 << 20
	}
	return c.MaxFileSize
}

func (c Config) effectiveScanDepth() int {
	if c.ScanDepth <= 0 {
		return 24
	}
	return c.ScanDepth
}

func (c Config) effectiveScanMaxFiles() int {
	if c.ScanMaxFiles <= 0 {
		return 20000
	}
	return c.ScanMaxFiles
}

// FileEntry records one file's change within a committed version.
type FileEntry struct {
	Path   string `json:"path"`             // absolute path in the workspace
	Rel    string `json:"rel"`              // workspace-relative display path
	Op     Op     `json:"op"`               // relative to the pre-turn state
	Size   int64  `json:"size"`             // pre-image size (0 for created)
	SHA256 string `json:"sha256,omitempty"` // pre-image hash; "" = content not retained
	Stored bool   `json:"stored"`
	Reason string `json:"reason,omitempty"` // why the pre-image was not retained
}

// Version is one committed turn's change record.
type Version struct {
	ID          string      `json:"id"`
	Num         int         `json:"num"`
	TurnID      string      `json:"turn_id,omitempty"`
	StartedAt   time.Time   `json:"started_at"`
	CommittedAt time.Time   `json:"committed_at"`
	ExecUsed    bool        `json:"exec_used,omitempty"`
	Files       []FileEntry `json:"files"`
	Notes       []string    `json:"notes,omitempty"`
	// Diffs summarizes the masked diffs.txt companion (diffs: N file(s),
	// M bytes). The full text is served via ReadDiff, not embedded.
	DiffFiles int   `json:"diff_files,omitempty"`
	DiffBytes int64 `json:"diff_bytes,omitempty"`
}

// pendingEntry is one in-flight capture (pre-turn observation of a file).
type pendingEntry struct {
	path    string // absolute, cleaned
	rel     string
	op      Op
	preHash string // stored pre-image hash ("" = not retained)
	preSize int64
	stored  bool
	reason  string
}

// TurnCapture accumulates one turn's pre-write observations. Owned by the
// turn goroutine (hook BeforeTool + turn_end); no internal locking.
type TurnCapture struct {
	SessionKey string
	TurnID     string
	Workspace  string
	StartedAt  time.Time
	ExecUsed   bool

	entries map[string]*pendingEntry // keyed by pathKey(abs)
	order   []string                 // capture order
}

// Store manages all sessions under one root directory.
type Store struct {
	root string
	cfg  Config

	mu sync.Mutex
}

// NewStore opens (and creates) the checkpoint root, sweeping pre-images
// orphaned by crashed turns.
func NewStore(root string, cfg Config) (*Store, error) {
	if root == "" {
		return nil, errors.New("snapshot: root directory is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("snapshot: create root: %w", err)
	}
	s := &Store{root: root, cfg: cfg}
	// Sweeping is best-effort housekeeping; a failure here must not disable
	// checkpointing.
	_ = s.sweepOrphans(time.Hour)
	return s, nil
}

// Root returns the store root directory.
func (s *Store) Root() string { return s.root }

// DefaultRoot is the conventional checkpoint location: ~/.picoclaw/checkpoints
// (outside every agent workspace, so exec-scan walks can never self-track).
func DefaultRoot() string {
	return filepath.Join(config.GetHome(), "checkpoints")
}

// sessionDir is the per-session directory (sanitized session key).
func (s *Store) sessionDir(sessionKey string) string {
	return filepath.Join(s.root, sanitizeSessionKey(sessionKey))
}

// preDir holds the session's content-addressed pre-images.
func (s *Store) preDir(sessionKey string) string {
	return filepath.Join(s.sessionDir(sessionKey), "pre")
}

// PreRoot exposes the session's content-addressed pre-image directory
// (used by tests and diagnostics).
func (s *Store) PreRoot(sessionKey string) string {
	return s.preDir(sessionKey)
}

// BeginTurn starts a fresh capture for the session, replacing any stale one
// (a crashed turn's capture never persisted anything but content-addressed
// pre-images, which the orphan sweep collects).
func (s *Store) BeginTurn(sessionKey, turnID, workspace string) *TurnCapture {
	return &TurnCapture{
		SessionKey: sessionKey,
		TurnID:     turnID,
		Workspace:  workspace,
		StartedAt:  time.Now(),
		entries:    make(map[string]*pendingEntry),
	}
}

// CaptureFile records the pre-turn state of absPath. The FIRST capture of a
// path within a turn wins (earliest pre-state). Validation failures and IO
// errors are recorded as unstored entries with a reason instead of failing
// the turn — a capture must never break the tool it wraps.
func (s *Store) CaptureFile(tc *TurnCapture, absPath, relPath string) {
	if tc == nil || absPath == "" {
		return
	}
	key := pathKey(absPath)
	if _, seen := tc.entries[key]; seen {
		return
	}

	entry := &pendingEntry{path: filepath.Clean(absPath), rel: relPath}
	info, err := os.Stat(absPath)
	switch {
	case err == nil && info.IsDir():
		return // directories are not captured
	case errors.Is(err, os.ErrNotExist):
		entry.op = OpCreated
		entry.stored = false
	case err != nil:
		entry.op = OpModified
		entry.stored = false
		entry.reason = fmt.Sprintf("stat failed: %v", err)
	default:
		entry.op = OpModified
		entry.preSize = info.Size()
		if info.Size() > s.cfg.effectiveMaxFileSize() {
			entry.stored = false
			entry.reason = fmt.Sprintf("file size %d exceeds max_file_size %d", info.Size(), s.cfg.effectiveMaxFileSize())
		} else if hash, err := s.storePreImage(tc.SessionKey, absPath); err != nil {
			entry.stored = false
			entry.reason = fmt.Sprintf("capture failed: %v", err)
		} else {
			entry.preHash = hash
			entry.stored = true
		}
	}

	tc.entries[key] = entry
	tc.order = append(tc.order, key)
}

// storePreImage writes content-addressed pre-image bytes and returns the hash.
func (s *Store) storePreImage(sessionKey, absPath string) (string, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", err
	}
	return s.storePreImageBytes(sessionKey, data)
}

func (s *Store) storePreImageBytes(sessionKey string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	dir := s.preDir(sessionKey)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, hash+".bin")
	if _, err := os.Stat(dst); err == nil {
		return hash, nil // deduplicated
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return hash, nil
}

// MarkExecUsed flags the turn as having run exec (its change detection is
// scan-based rather than interception-based).
func (s *Store) MarkExecUsed(tc *TurnCapture) {
	if tc != nil {
		tc.ExecUsed = true
	}
}

// CommitTurn turns the capture into a version. Entries whose net effect is a
// no-op (content hash identical to the pre-image) are dropped, so failed
// writes do not produce empty versions. A capture with no remaining entries
// commits nothing (returns nil, nil) — the workspace_never_modified
// short-circuit.
func (s *Store) CommitTurn(tc *TurnCapture) (*Version, error) {
	if tc == nil || len(tc.entries) == 0 {
		return nil, nil
	}

	entries := make([]FileEntry, 0, len(tc.order))
	for _, key := range tc.order {
		entry := tc.entries[key]
		final, keep := s.finalizeEntry(tc.SessionKey, entry)
		if keep {
			entries = append(entries, final)
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// Diffs read current file contents — done before the store lock.
	diffText, diffFiles := s.buildVersionDiff(tc.SessionKey, entries)

	s.mu.Lock()
	defer s.mu.Unlock()

	num, err := s.nextVersionNum(tc.SessionKey)
	if err != nil {
		return nil, err
	}

	// Evict BEFORE writing the manifest: the new version counts toward the
	// limits via its stored pre-images, and the newest version is protected
	// by not existing yet — a turn larger than the whole session budget is
	// retained and noted rather than instantly self-evicted (undo of the
	// last turn is the primary use case).
	incoming := make(map[string]int64)
	for _, e := range entries {
		if e.Stored && e.SHA256 != "" {
			incoming[e.SHA256] = e.Size
		}
	}
	version := Version{
		ID:          fmt.Sprintf("v%03d-%s", num, strconv36(tc.StartedAt.Unix())),
		Num:         num,
		TurnID:      tc.TurnID,
		StartedAt:   tc.StartedAt,
		CommittedAt: time.Now(),
		ExecUsed:    tc.ExecUsed,
		Files:       entries,
		DiffFiles:   diffFiles,
		DiffBytes:   int64(len(diffText)),
	}
	version.Notes = s.evictLocked(tc.SessionKey, incoming)

	dir := filepath.Join(s.sessionDir(tc.SessionKey), version.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("snapshot: create version dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "diffs.txt"), []byte(diffText), 0o600); err != nil {
		return nil, fmt.Errorf("snapshot: write diffs: %w", err)
	}
	if err := writeJSONFile(filepath.Join(dir, "manifest.json"), version); err != nil {
		return nil, fmt.Errorf("snapshot: write manifest: %w", err)
	}
	return &version, nil
}

// ReadDiff returns the masked unified diffs of a committed version
// (diffs.txt). The text is safe to display: pre-images stay raw for
// byte-exact restore, but every query/display surface is masked.
func (s *Store) ReadDiff(sessionKey, id string) ([]byte, error) {
	if !validVersionID(id) {
		return nil, fmt.Errorf("snapshot: invalid version id %q", id)
	}
	if _, err := s.readManifest(sessionKey, id); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.sessionDir(sessionKey), id, "diffs.txt"))
}

// finalizeEntry reconciles a pending entry against the file's current
// (post-turn) state, dropping net no-ops and reclassifying net effects
// (e.g. a fs-write followed by an exec delete nets to "deleted").
func (s *Store) finalizeEntry(sessionKey string, entry *pendingEntry) (FileEntry, bool) {
	out := FileEntry{
		Path:   entry.path,
		Rel:    entry.rel,
		Op:     entry.op,
		Size:   entry.preSize,
		SHA256: entry.preHash,
		Stored: entry.stored,
		Reason: entry.reason,
	}

	info, err := os.Stat(entry.path)
	exists := err == nil && !info.IsDir()

	switch entry.op {
	case OpCreated:
		if !exists {
			return out, false // created then deleted within the turn: net no-op
		}
		return out, true
	case OpDeleted:
		if exists {
			// Deleted then recreated within the turn: nets to a content
			// comparison against the retained pre-image.
			if entry.stored && s.hashEquals(sessionKey, entry.preHash, entry.path) {
				return out, false
			}
			out.Op = OpModified
			return out, true
		}
		return out, true
	default: // OpModified
		if !exists {
			// Modified then deleted within the turn: the pre-image (if
			// retained) still describes the restore target.
			out.Op = OpDeleted
			return out, true
		}
		if entry.stored && s.hashEquals(sessionKey, entry.preHash, entry.path) {
			return out, false // write failed or made no change: net no-op
		}
		return out, true
	}
}

// hashEquals reports whether the stored pre-image content equals the current
// file content (streaming hash, so oversized current files are fine).
func (s *Store) hashEquals(sessionKey, hash, absPath string) bool {
	if hash == "" {
		return false
	}
	f, err := os.Open(absPath)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == hash
}

// Versions lists the session's committed versions, oldest first. Version
// directories without a readable manifest (crashed mid-commit) are skipped.
func (s *Store) Versions(sessionKey string) ([]Version, error) {
	ids, err := s.versionIDs(sessionKey)
	if err != nil {
		return nil, err
	}
	versions := make([]Version, 0, len(ids))
	for _, id := range ids {
		v, err := s.readManifest(sessionKey, id)
		if err != nil {
			continue // torn directory: treated as not-yet-committed
		}
		versions = append(versions, *v)
	}
	return versions, nil
}

// Version returns one committed version.
func (s *Store) Version(sessionKey, id string) (*Version, error) {
	if !validVersionID(id) {
		return nil, fmt.Errorf("snapshot: invalid version id %q", id)
	}
	return s.readManifest(sessionKey, id)
}

func (s *Store) versionIDs(sessionKey string) ([]string, error) {
	entries, err := os.ReadDir(s.sessionDir(sessionKey))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && validVersionID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids) // v-prefixed zero-padded names sort chronologically
	return ids, nil
}

func (s *Store) readManifest(sessionKey, id string) (*Version, error) {
	data, err := os.ReadFile(filepath.Join(s.sessionDir(sessionKey), id, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var v Version
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// nextVersionNum returns max(existing)+1 under the store lock.
func (s *Store) nextVersionNum(sessionKey string) (int, error) {
	ids, err := s.versionIDs(sessionKey)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, id := range ids {
		if n := versionNum(id); n > max {
			max = n
		}
	}
	return max + 1, nil
}

// evictLocked enforces MaxVersions and MaxTotalBytes for the session by
// deleting the oldest versions, returning human-readable notes about forced
// evictions (empty when nothing was evicted). incoming counts the pre-image
// bytes the not-yet-written version will add (net-new hashes only), so the
// budget check includes it; when even the incoming version alone exceeds the
// budget it is retained with a note instead of being self-evicted. Caller
// holds s.mu.
func (s *Store) evictLocked(sessionKey string, incoming map[string]int64) []string {
	var notes []string
	for {
		ids, err := s.versionIDs(sessionKey)
		if err != nil {
			return notes
		}
		referenced, err := s.referencedBlobsLocked(sessionKey, ids)
		if err != nil {
			return notes
		}
		total := int64(0)
		for _, size := range referenced {
			total += size
		}
		incomingNet := int64(0)
		for hash, size := range incoming {
			if _, dup := referenced[hash]; !dup {
				incomingNet += size
			}
		}

		overCount := len(ids)+1 > s.cfg.effectiveMaxVersions()
		overBudget := total+incomingNet > s.cfg.effectiveMaxTotalBytes()
		if !overCount && !overBudget {
			return notes
		}
		if len(ids) == 0 {
			if overBudget {
				notes = append(notes, fmt.Sprintf(
					"disk budget exceeded: %d bytes needed > %d budget; the newest version is retained",
					total+incomingNet, s.cfg.effectiveMaxTotalBytes()))
			}
			return notes
		}

		oldest := ids[0]
		if err := s.deleteVersionLocked(sessionKey, oldest); err != nil {
			return append(notes, fmt.Sprintf("eviction of %s failed: %v", oldest, err))
		}
		notes = append(notes, fmt.Sprintf("evicted oldest version %s to stay within limits", oldest))
		if len(notes) > 64 {
			return notes // pathological loop guard
		}
	}
}

// referencedBlobsLocked maps every pre-image hash referenced by the given
// versions to its file size. Caller holds s.mu.
func (s *Store) referencedBlobsLocked(sessionKey string, ids []string) (map[string]int64, error) {
	referenced := make(map[string]int64)
	for _, id := range ids {
		v, err := s.readManifest(sessionKey, id)
		if err != nil {
			continue
		}
		for _, f := range v.Files {
			if !f.Stored || f.SHA256 == "" {
				continue
			}
			if _, dup := referenced[f.SHA256]; dup {
				continue
			}
			if info, err := os.Stat(filepath.Join(s.preDir(sessionKey), f.SHA256+".bin")); err == nil {
				referenced[f.SHA256] = info.Size()
			}
		}
	}
	return referenced, nil
}

func (s *Store) deleteVersionLocked(sessionKey, id string) error {
	v, err := s.readManifest(sessionKey, id)
	if err != nil {
		// No readable manifest: just drop the directory.
		return os.RemoveAll(filepath.Join(s.sessionDir(sessionKey), id))
	}
	if err := os.RemoveAll(filepath.Join(s.sessionDir(sessionKey), id)); err != nil {
		return err
	}
	// Remove pre-images that no surviving version references.
	ids, err := s.versionIDs(sessionKey)
	if err != nil {
		return nil
	}
	live := make(map[string]struct{})
	for _, other := range ids {
		ov, err := s.readManifest(sessionKey, other)
		if err != nil {
			continue
		}
		for _, f := range ov.Files {
			if f.Stored && f.SHA256 != "" {
				live[f.SHA256] = struct{}{}
			}
		}
	}
	for _, f := range v.Files {
		if !f.Stored || f.SHA256 == "" {
			continue
		}
		if _, ok := live[f.SHA256]; ok {
			continue
		}
		_ = os.Remove(filepath.Join(s.preDir(sessionKey), f.SHA256+".bin"))
	}
	return nil
}

// sweepOrphans deletes pre-images not referenced by any manifest and older
// than maxAge (crashed-turn leftovers), plus empty version directories.
func (s *Store) sweepOrphans(maxAge time.Duration) error {
	sessionEntries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, se := range sessionEntries {
		if !se.IsDir() {
			continue
		}
		sessionKey := se.Name() // sanitized key; only needed for path building
		ids, _ := s.versionIDs(sessionKey)
		live := make(map[string]struct{})
		for _, id := range ids {
			v, err := s.readManifest(sessionKey, id)
			if err != nil {
				// Torn version directory: remove if old enough.
				dir := filepath.Join(s.sessionDir(sessionKey), id)
				if info, statErr := os.Stat(dir); statErr == nil && info.ModTime().Before(cutoff) {
					_ = os.RemoveAll(dir)
				}
				continue
			}
			for _, f := range v.Files {
				if f.Stored && f.SHA256 != "" {
					live[f.SHA256] = struct{}{}
				}
			}
		}
		preEntries, err := os.ReadDir(s.preDir(sessionKey))
		if err != nil {
			continue
		}
		for _, pe := range preEntries {
			name := pe.Name()
			hash := strings.TrimSuffix(name, ".bin")
			if !strings.HasSuffix(name, ".bin") || !validHash(hash) {
				continue
			}
			if _, ok := live[hash]; ok {
				continue
			}
			info, err := pe.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			_ = os.Remove(filepath.Join(s.preDir(sessionKey), name))
		}
	}
	return nil
}

// --- helpers ---

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sanitizeSessionKey maps a session key to a single safe directory name.
func sanitizeSessionKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "session"
	}
	return out
}

// SanitizeSessionKey exposes the session-key → directory-name mapping so
// external readers (web launcher) address the same directories the store
// writes. One implementation, one mapping.
func SanitizeSessionKey(key string) string {
	return sanitizeSessionKey(key)
}

// pathKey normalizes an absolute path for map keys (Windows case-insensitivity).
func pathKey(abs string) string {
	cleaned := filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func validVersionID(id string) bool {
	if len(id) < 6 || !strings.HasPrefix(id, "v") {
		return false
	}
	for _, r := range id[1:] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && r != '-' {
			return false
		}
	}
	return true
}

func versionNum(id string) int {
	// "vNNN-<ts36>"
	end := strings.IndexByte(id[1:], '-')
	if end < 0 {
		return 0
	}
	n := 0
	for _, r := range id[1 : 1+end] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, r := range hash {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func strconv36(n int64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%36]
		n /= 36
	}
	return string(b[i:])
}
