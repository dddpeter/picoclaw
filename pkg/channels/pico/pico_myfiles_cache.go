package pico

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Incremental cache for the "my files" aggregation (design §11.1 + the
// efficiency follow-up): the transcript directory grows with every
// conversation (hundreds of files, hundreds of MB), so re-scanning it on
// every request is wasteful — measured 550ms per request with 288MB of
// transcripts, and it degrades linearly with usage.
//
// The cache keys each transcript by (size, mtime): a transcript that did not
// change keeps its extracted paths, so a refresh only re-reads the files
// that actually changed. Uploads are tracked the same way via the inbound
// index's mtime.
//
// Concurrency: a single-flight mutex means concurrent requests share one
// scan instead of stampeding the disk.

type transcriptFingerprint struct {
	Size int64
	Mod  time.Time
}

// fileCache caches extracted paths per transcript file.
type fileCache struct {
	mu sync.Mutex
	// pathsByTranscript holds the paths extracted from one transcript.
	pathsByTranscript map[string]map[string]struct{}
	// fingerprints remembers the (size, mtime) each transcript had when it
	// was last scanned.
	fingerprints map[string]transcriptFingerprint
	// uploadIndexMod is the inbound index mtime at the last read.
	uploadIndexMod time.Time
	// uploadIndexSize mirrors its size (mtime alone can miss same-second writes).
	uploadIndexSize int64
	// uploads caches the parsed upload entries.
	uploads []uploadIndexEntry
	// lastScan records when the full scan last completed (diagnostics).
	lastScan time.Time
	// scans counts full directory walks (diagnostics).
	scans int
}

var myFilesCache = &fileCache{
	pathsByTranscript: make(map[string]map[string]struct{}),
	fingerprints:      make(map[string]transcriptFingerprint),
}

// scanResult returns the cached view of the transcripts directory plus the
// upload entries, re-reading only what changed.
func (fc *fileCache) collect(transcriptsDir, workspace string) ([]uploadIndexEntry, map[string]struct{}) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	uploads := fc.readUploads(workspace)
	paths := fc.readTranscripts(transcriptsDir)
	fc.lastScan = time.Now()
	return uploads, paths
}

// readUploads parses the inbound index, reusing the cached copy when the
// file has not changed.
func (fc *fileCache) readUploads(workspace string) []uploadIndexEntry {
	if workspace == "" {
		return nil
	}
	path := filepath.Join(workspace, uploadInboundDir, uploadIndexName)
	info, err := os.Stat(path)
	if err != nil {
		// Index gone (fresh workspace): drop the cached copy.
		fc.uploads = nil
		fc.uploadIndexMod = time.Time{}
		fc.uploadIndexSize = 0
		return nil
	}
	if len(fc.uploads) > 0 &&
		info.Size() == fc.uploadIndexSize &&
		info.ModTime().Equal(fc.uploadIndexMod) {
		return fc.uploads
	}
	entries := readUploadIndex(workspace)
	fc.uploads = entries
	fc.uploadIndexMod = info.ModTime()
	fc.uploadIndexSize = info.Size()
	return entries
}

// readTranscripts scans the transcripts directory, re-reading only the
// files whose (size, mtime) changed since the last scan.
func (fc *fileCache) readTranscripts(dir string) map[string]struct{} {
	paths := make(map[string]struct{})
	if dir == "" {
		return paths
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return paths
	}
	fc.scans++

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		seen[full] = struct{}{}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		fingerprint := transcriptFingerprint{Size: info.Size(), Mod: info.ModTime()}
		if cached, ok := fc.fingerprints[full]; ok && cached == fingerprint {
			// Unchanged transcript: reuse its extracted paths.
			for path := range fc.pathsByTranscript[full] {
				paths[path] = struct{}{}
			}
			continue
		}
		found := make(map[string]struct{})
		scanTranscriptFile(full, found)
		fc.pathsByTranscript[full] = found
		fc.fingerprints[full] = fingerprint
		for path := range found {
			paths[path] = struct{}{}
		}
	}

	// Drop entries for transcripts that disappeared.
	for full := range fc.pathsByTranscript {
		if _, ok := seen[full]; !ok {
			delete(fc.pathsByTranscript, full)
			delete(fc.fingerprints, full)
		}
	}
	return paths
}

// invalidate drops the whole cache (used when the workspace changes).
func (fc *fileCache) invalidate() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.pathsByTranscript = make(map[string]map[string]struct{})
	fc.fingerprints = make(map[string]transcriptFingerprint)
	fc.uploads = nil
	fc.uploadIndexMod = time.Time{}
	fc.uploadIndexSize = 0
}

// stats reports cache diagnostics (scans, cached transcripts).
func (fc *fileCache) stats() (scans int, cached int, last time.Time) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.scans, len(fc.pathsByTranscript), fc.lastScan
}
