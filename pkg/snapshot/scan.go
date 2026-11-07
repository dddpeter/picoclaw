package snapshot

// exec change detection (trae borrowing 增量清单 §2.1, TrackExec): the exec
// tool can create/modify/delete any file, so its writes cannot be intercepted
// like the fs tools'. Instead, around every exec run the workspace is walked
// and diffed against a persistent per-session index:
//
//	ExecPreScan  — refresh the index, reading (into the content cache) every
//	               file whose (mtime,size) changed or whose content is not
//	               cached; files that vanished since the last scan are
//	               dropped from the index (deleted outside any exec — never
//	               attributed to the upcoming run).
//	ExecPostScan — walk again; entries that moved since the pre-scan become
//	               pending turn entries (modified / created / deleted), with
//	               the pre-exec content from the cache.
//
// The cache lives in the same content-addressed pre/ directory as version
// pre-images. Cache-only blobs (never referenced by a version) carry their
// own budget and are LRU-evicted at pre-scan time; blobs referenced by
// versions are never evicted by the cache. Everything beyond the caps
// degrades honestly: entries recorded with stored=false + reason.
//
// Concurrency: tool calls of one session run sequentially, so the pre/post
// pairing is guaranteed; the store mutex serializes cross-session walkers.
// Crash tolerance: the index is a pure cache — a stale or missing index only
// costs re-reads, never correctness.

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// scanExcludedDirs are never walked (workspace-relative noise: agent
// bookkeeping and transient offload files). Name-based anywhere in the
// tree — over-exclusion is harmless for undo coverage.
var scanExcludedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"sessions":     {},
	"tmp":          {},
}

// scanEntry is one indexed workspace file. Path/Rel keep the original
// casing so a later deletion can still record the real file identity
// (the map key is case-folded on Windows).
type scanEntry struct {
	Path     string `json:"path"`
	Rel      string `json:"rel"`
	MtimeNS  int64  `json:"mtime_ns"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash,omitempty"` // cached content hash; "" = stat-only
	Cached   bool   `json:"cached"`
	LastSeen int64  `json:"last_seen"`
}

// scanIndex is the per-session workspace cache, persisted next to the
// version manifests.
type scanIndex struct {
	Entries    map[string]scanEntry `json:"entries"`
	CacheBytes int64                `json:"cache_bytes"`
}

func loadScanIndex(sessionDir string) *scanIndex {
	data, err := os.ReadFile(filepath.Join(sessionDir, "scan-index.json"))
	if err != nil {
		return &scanIndex{Entries: make(map[string]scanEntry)}
	}
	var idx scanIndex
	if json.Unmarshal(data, &idx) != nil || idx.Entries == nil {
		return &scanIndex{Entries: make(map[string]scanEntry)}
	}
	return &idx
}

func (s *Store) saveScanIndexLocked(sessionKey string, idx *scanIndex) {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return
	}
	dir := s.sessionDir(sessionKey)
	// Scan-only sessions (no committed version yet) have no directory yet.
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	path := filepath.Join(dir, "scan-index.json")
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// ExecPreScan refreshes the session's workspace index before an exec run
// and marks the turn exec-used. Failures degrade to fewer cached files —
// the scan must never block the command it wraps.
func (s *Store) ExecPreScan(tc *TurnCapture) {
	if tc == nil {
		return
	}
	s.MarkExecUsed(tc)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := loadScanIndex(s.sessionDir(tc.SessionKey))
	now := time.Now().UnixNano()

	s.evictScanCacheLocked(tc.SessionKey, idx)

	candidates := s.walkWorkspace(tc.Workspace)
	seen := make(map[string]struct{}, len(candidates))
	for _, cand := range candidates {
		seen[cand.key] = struct{}{}
		entry, known := idx.Entries[cand.key]
		if known && entry.Cached && entry.MtimeNS == cand.mtimeNS && entry.Size == cand.size {
			entry.LastSeen = now
			idx.Entries[cand.key] = entry
			continue // unchanged and cached: steady state, no read
		}

		updated := scanEntry{Path: cand.path, Rel: cand.rel, MtimeNS: cand.mtimeNS, Size: cand.size, LastSeen: now}
		if cand.size <= s.cfg.effectiveMaxFileSize() {
			if data, err := os.ReadFile(cand.path); err == nil {
				if hash, storeErr := s.storePreImageBytes(tc.SessionKey, data); storeErr == nil {
					updated.Hash = hash
					updated.Cached = true
					idx.CacheBytes += int64(len(data))
				}
			}
		}
		idx.Entries[cand.key] = updated
	}

	// Vanished since the last scan: deleted outside any exec run (by the
	// user or a previous turn's revert) — forget them instead of letting
	// the post-scan attribute the deletion to this exec.
	for key, entry := range idx.Entries {
		if _, ok := seen[key]; !ok {
			if entry.Cached && entry.Hash != "" {
				idx.CacheBytes -= s.blobSize(tc.SessionKey, entry.Hash)
			}
			delete(idx.Entries, key)
		}
	}
	s.saveScanIndexLocked(tc.SessionKey, idx)
}

// ExecPostScan diffs the workspace against the index and records everything
// the exec run changed as pending entries of the turn.
func (s *Store) ExecPostScan(tc *TurnCapture) {
	if tc == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := loadScanIndex(s.sessionDir(tc.SessionKey))
	now := time.Now().UnixNano()

	candidates := s.walkWorkspace(tc.Workspace)
	seen := make(map[string]struct{}, len(candidates))
	for _, cand := range candidates {
		seen[cand.key] = struct{}{}
		entry, known := idx.Entries[cand.key]

		if !known {
			// New file: created by the exec run.
			s.recordExecEntry(tc, cand.path, cand.rel, OpCreated, "", false, "")
			idx.Entries[cand.key] = scanEntry{Path: cand.path, Rel: cand.rel, MtimeNS: cand.mtimeNS, Size: cand.size, LastSeen: now}
			continue
		}
		if entry.MtimeNS == cand.mtimeNS && entry.Size == cand.size {
			continue // untouched by this run
		}

		// Changed by the exec run: pre-exec content is whatever the pre-scan
		// cached; a stat-only entry degrades honestly.
		if entry.Cached && entry.Hash != "" {
			s.recordExecEntry(tc, entry.Path, entry.Rel, OpModified, entry.Hash, true, "")
		} else {
			s.recordExecEntry(tc, entry.Path, entry.Rel, OpModified, "", false,
				"pre-exec content not cached (size/budget limit)")
		}
		// Post-exec content becomes the next baseline — but the cache must
		// not claim to hold it: clearing Cached forces the next pre-scan to
		// re-read, keeping the hash ↔ content pairing honest.
		idx.Entries[cand.key] = scanEntry{Path: entry.Path, Rel: entry.Rel, MtimeNS: cand.mtimeNS, Size: cand.size, LastSeen: now}
	}

	// Present in the index, missing now: deleted by the exec run.
	for key, entry := range idx.Entries {
		if _, ok := seen[key]; ok {
			continue
		}
		if entry.Cached && entry.Hash != "" {
			s.recordExecEntry(tc, entry.Path, entry.Rel, OpDeleted, entry.Hash, true, "")
		} else {
			s.recordExecEntry(tc, entry.Path, entry.Rel, OpDeleted, "", false,
				"deleted before its content was cached")
		}
		idx.CacheBytes -= s.blobSize(tc.SessionKey, entry.Hash)
		delete(idx.Entries, key)
	}
	s.saveScanIndexLocked(tc.SessionKey, idx)
}

// recordExecEntry adds a scan-detected change to the turn capture without
// overriding an existing entry (an fs tool's earlier capture of the same
// file wins — it observed the true pre-turn state).
func (s *Store) recordExecEntry(tc *TurnCapture, absPath, relPath string, op Op, hash string, stored bool, reason string) {
	key := pathKey(absPath)
	if _, seen := tc.entries[key]; seen {
		return
	}
	entry := &pendingEntry{
		path:    filepath.Clean(absPath),
		rel:     relPath,
		op:      op,
		preHash: hash,
		stored:  stored,
		reason:  reason,
	}
	tc.entries[key] = entry
	tc.order = append(tc.order, key)
}

// evictScanCacheLocked LRU-evicts cache-only blobs when the content cache
// outgrows the disk budget. Version-referenced blobs are untouchable.
// Caller holds s.mu.
func (s *Store) evictScanCacheLocked(sessionKey string, idx *scanIndex) {
	budget := s.cfg.effectiveMaxTotalBytes()
	if idx.CacheBytes <= budget {
		return
	}
	ids, err := s.versionIDs(sessionKey)
	if err != nil {
		return
	}
	referenced, err := s.referencedBlobsLocked(sessionKey, ids)
	if err != nil {
		return
	}

	keys := make([]string, 0, len(idx.Entries))
	for key, entry := range idx.Entries {
		if entry.Cached && entry.Hash != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return idx.Entries[keys[i]].LastSeen < idx.Entries[keys[j]].LastSeen
	})
	for _, key := range keys {
		if idx.CacheBytes <= budget {
			return
		}
		entry := idx.Entries[key]
		idx.CacheBytes -= s.blobSize(sessionKey, entry.Hash)
		if _, live := referenced[entry.Hash]; !live {
			_ = os.Remove(filepath.Join(s.preDir(sessionKey), entry.Hash+".bin"))
		}
		entry.Hash = ""
		entry.Cached = false
		idx.Entries[key] = entry
	}
}

func (s *Store) blobSize(sessionKey, hash string) int64 {
	if hash == "" {
		return 0
	}
	if info, err := os.Stat(filepath.Join(s.preDir(sessionKey), hash+".bin")); err == nil {
		return info.Size()
	}
	return 0
}

// walkCandidate is one regular file observed in the workspace walk.
type walkCandidate struct {
	key     string // pathKey(abs)
	path    string // absolute, original casing
	rel     string // workspace-relative, slash-separated
	mtimeNS int64
	size    int64
}

// walkWorkspace lists the workspace's regular files within the scan caps.
// Unreadable entries are skipped, never fatal.
func (s *Store) walkWorkspace(workspace string) []walkCandidate {
	maxDepth := s.cfg.effectiveScanDepth()
	maxFiles := s.cfg.effectiveScanMaxFiles()

	out := make([]walkCandidate, 0, 64)
	count := 0
	_ = filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(workspace, path)
		if relErr != nil {
			return nil
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			depth := strings.Count(rel, string(os.PathSeparator)) + 1
			if depth >= maxDepth {
				return fs.SkipDir
			}
			if _, excluded := scanExcludedDirs[d.Name()]; excluded {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and special files: never followed
		}
		count++
		if count > maxFiles {
			return fs.SkipAll
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		out = append(out, walkCandidate{
			key:     pathKey(path),
			path:    path,
			rel:     relSlash,
			mtimeNS: info.ModTime().UnixNano(),
			size:    info.Size(),
		})
		return nil
	})
	return out
}
