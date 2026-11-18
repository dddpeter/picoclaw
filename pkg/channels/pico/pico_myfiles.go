package pico

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// "My files" aggregation (design §11.1): the file manager lists the files
// THIS user uploaded or the agent generated — across all sessions — instead
// of the whole workspace tree (which is mostly noise: cron jobs, plans,
// backups).
//
// Two sources, both durable:
//   - uploads: <workspace>/inbound/.index.jsonl (written by handleUpload)
//   - generated: paths recovered from persisted session transcripts
//     (tool-call arguments + inline [file:…] tags + exec "Full output:"
//     pointers) — the same evidence the session-scoped panel uses, but
//     collected across every session.
//
// Performance: the sessions directory grows with every conversation (hundreds
// of files, hundreds of MB). Reading it through the session store would
// unmarshal every message of every session, so the scan below streams the
// raw JSONL lines and extracts only what is needed.

// MyFile is one entry of the aggregated list.
type MyFile struct {
	Path        string `json:"path"`
	Filename    string `json:"filename"`
	Origin      string `json:"origin"` // "upload" | "generated"
	Session     string `json:"session,omitempty"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Mtime       string `json:"mtime,omitempty"`
}

// SessionFileScanner returns the persisted transcript directory to scan.
// The gateway injects it (the channel has no access to the agent's session
// store on its own).
type SessionFileScanner func() string

// SetSessionFileScanner wires the cross-session transcript source.
func (c *PicoChannel) SetSessionFileScanner(scan SessionFileScanner) {
	c.fileScannerMu.Lock()
	defer c.fileScannerMu.Unlock()
	c.fileScanner = scan
}

// toolArgsPathKeys are the argument names that carry a file path.
var toolArgsPathKeys = []string{
	"path", "file_path", "filepath", "dest", "target_path", "output_path",
}

// fileWriteTools produce files the agent generated.
var fileWriteTools = map[string]bool{
	"write_file": true, "edit_file": true, "append_file": true,
	"write": true, "edit": true, "send_file": true, "exec": true, "bash": true,
}

var (
	inlineFileTagRe = regexp.MustCompile(`\[(file|image|audio|video):([^\]\s]+)\]`)
	// "Full output: <path>" sits inside a JSON string, so the path may end
	// with an escaped quote before the string closes — stop at whitespace
	// and strip trailing JSON punctuation.
	fullOutputRe = regexp.MustCompile(`Full output:\s*([^\s"\\]+)`)
	toolNameRe   = regexp.MustCompile(`"name"\s*:\s*"([a-z_]+)"`)
	// Tool arguments inside a transcript line are themselves JSON-encoded,
	// so the path key and its value appear escaped (\"path\":\"...\").
	// The value must stop at the closing quote — including its escaped form
	// — otherwise the following JSON fields leak into the captured path.
	toolArgsPathRe = regexp.MustCompile(`(?:\\?)"(?:` + strings.Join(toolArgsPathKeys, "|") + `)(?:\\?)"\s*:\s*(?:\\?)"((?:[^"\\\n]|\\.)*?)(?:\\?)"`)
)

// collectMyFiles aggregates uploads and generated files.
func (c *PicoChannel) collectMyFiles() []MyFile {
	opts := c.uploadOptions()
	byPath := make(map[string]MyFile)

	c.fileScannerMu.RLock()
	scanner := c.fileScanner
	c.fileScannerMu.RUnlock()

	transcriptsDir := ""
	if scanner != nil {
		transcriptsDir = scanner()
	}
	// Incremental: only transcripts whose (size, mtime) changed are re-read.
	uploads, generatedPaths := myFilesCache.collect(transcriptsDir, opts.workspace)

	// 1) uploads (index written at upload time)
	for _, entry := range uploads {
		if _, exists := byPath[entry.Path]; exists {
			continue
		}
		byPath[entry.Path] = MyFile{
			Path:        entry.Path,
			Filename:    entry.Name,
			Origin:      "upload",
			Session:     entry.Session,
			Size:        entry.Size,
			ContentType: entry.ContentType,
			Mtime:       entry.Mtime,
		}
	}

	// 2) generated files recovered from persisted transcripts
	for path := range generatedPaths {
		if _, exists := byPath[path]; exists {
			continue
		}
		// Only files the web preview can actually open: paths outside the
		// workspace are rejected by the allowlist, and elided paths
		// (".../config.yaml") point at nothing on disk.
		rel, ok := normalizeToWorkspacePath(opts.workspace, path)
		if !ok {
			continue
		}
		byPath[rel] = MyFile{
			Path:     rel,
			Filename: filepath.Base(rel),
			Origin:   "generated",
		}
	}

	files := make([]MyFile, 0, len(byPath))
	for _, file := range byPath {
		// Enrich with on-disk facts (size/mtime) when the file still exists.
		if abs, err := safeJoinWorkspace(opts.workspace, file.Path); err == nil {
			if info, err := os.Stat(abs); err == nil {
				file.Size = info.Size()
				file.Mtime = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Mtime != files[j].Mtime {
			return files[i].Mtime > files[j].Mtime
		}
		return files[i].Path < files[j].Path
	})
	return files
}

// readUploadIndex parses inbound/.index.jsonl. Corrupt lines are skipped so
// one bad record never hides the whole list (design §11.3).
func readUploadIndex(workspace string) []uploadIndexEntry {
	if workspace == "" {
		return nil
	}
	path := filepath.Join(workspace, uploadInboundDir, uploadIndexName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []uploadIndexEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry uploadIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Path == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func scanTranscriptFile(path string, found map[string]struct{}) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		collectPathsFromLine(line, found)
	}
}

func collectPathsFromLine(line []byte, found map[string]struct{}) {
	text := string(line)

	// inline [file:…] tags and exec "Full output:" pointers
	for _, match := range inlineFileTagRe.FindAllStringSubmatch(text, -1) {
		addGeneratedPath(match[2], found)
	}
	for _, match := range fullOutputRe.FindAllStringSubmatch(text, -1) {
		addGeneratedPath(match[1], found)
	}

	// tool-call arguments: only for file-writing tools
	if !strings.Contains(text, `"tool_calls"`) {
		return
	}
	for _, toolName := range toolNameRe.FindAllStringSubmatch(text, -1) {
		if !fileWriteTools[toolName[1]] {
			continue
		}
		for _, match := range toolArgsPathRe.FindAllStringSubmatch(text, -1) {
			addGeneratedPath(unescapeJSONString(match[1]), found)
		}
	}
}

func addGeneratedPath(path string, found map[string]struct{}) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if !looksLikeFilePath(path) {
		return
	}
	found[path] = struct{}{}
}

// looksLikeFilePath keeps version-like tokens and prose out of the list.
func looksLikeFilePath(path string) bool {
	cleaned := strings.TrimRight(path, ".,;:)]}\"'")
	base := filepath.Base(filepath.ToSlash(cleaned))
	if base == "" || base == "." || base == ".." {
		return false
	}
	if !strings.Contains(base, ".") {
		return false
	}
	// Version-like tokens (1.0.2, v1.0.2, 2024.11.1) are not files: they
	// carry no alphabetic extension part.
	if isVersionLike(base) {
		return false
	}
	return true
}

// isVersionLike reports whether a name looks like a version number rather
// than a file name (e.g. "v1.0.2", "1.0.2", "2024.11").
func isVersionLike(base string) bool {
	trimmed := strings.TrimPrefix(base, "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// unescapeJSONString decodes the common escapes in a JSON string body.
func unescapeJSONString(raw string) string {
	if !strings.Contains(raw, `\`) {
		return raw
	}
	var out string
	if err := json.Unmarshal([]byte(`"`+raw+`"`), &out); err == nil {
		return out
	}
	return raw
}

// safeJoinWorkspace joins a relative path under the workspace, refusing
// anything that escapes it.
func safeJoinWorkspace(workspace, rel string) (string, error) {
	if workspace == "" {
		return "", os.ErrInvalid
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", os.ErrInvalid
	}
	return filepath.Join(workspace, cleaned), nil
}

// normalizeToWorkspacePath maps a path recorded by the agent onto a
// workspace-relative path. It rejects paths outside the workspace (the web
// allowlist would refuse them anyway, so listing them would only produce
// dead entries) and elided paths that name no file on disk.
func normalizeToWorkspacePath(workspace, path string) (string, bool) {
	if workspace == "" {
		return "", false
	}
	raw := strings.TrimSpace(path)
	if raw == "" || strings.Contains(raw, "...") {
		return "", false
	}
	fromSlash := filepath.FromSlash(raw)
	// Windows: "/x" (rooted, drive-less) is absolute on the process drive even
	// though filepath.IsAbs says false — treating it as relative would list
	// workspace-external paths as if they lived in the workspace.
	if filepath.IsAbs(fromSlash) || strings.HasPrefix(filepath.ToSlash(fromSlash), "/") {
		absWorkspace, err := filepath.Abs(workspace)
		if err != nil {
			return "", false
		}
		absPath, err := filepath.Abs(filepath.FromSlash(raw))
		if err != nil {
			return "", false
		}
		rel, err := filepath.Rel(absWorkspace, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(rel), true
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

// unusedProvidersRef keeps the providers import meaningful for future
// callers that pass structured histories instead of raw transcript lines.
var _ = providers.Message{}
