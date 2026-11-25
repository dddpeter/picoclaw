package pico

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/h2non/filetype"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// uploadInboundDir is the workspace-relative directory where web uploads land.
const uploadInboundDir = "inbound"

// uploadIndexName is the append-only JSONL index recording uploads so the
// file manager can distinguish user uploads from agent-generated files.
const uploadIndexName = ".index.jsonl"

// uploadOptions carries the workspace-rooted upload configuration injected
// by the channel factory (gateway boot). Empty workspace disables uploads.
type uploadOptions struct {
	workspace       string
	maxUploadBytes  int64
	fileAccessRoots []string
}

type uploadState struct {
	mu      sync.RWMutex
	options uploadOptions
}

func (u *uploadState) set(workspace string, maxBytes int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.options = uploadOptions{workspace: workspace, maxUploadBytes: maxBytes}
}

func (u *uploadState) setRoots(roots []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.options.fileAccessRoots = append([]string(nil), roots...)
}

func (u *uploadState) get() uploadOptions {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.options
}

// SetUploadOptions wires the workspace root and size cap for /pico/upload.
// Called by the channel factory after the gateway resolves the config; the
// pico channel itself only ever sees PicoSettings (no workspace inside).
func (c *PicoChannel) SetUploadOptions(workspace string, maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = int64(config.DefaultMaxMediaSize)
	}
	c.uploads.set(workspace, maxBytes)
}

// SetFileAccessRoots wires the web file-endpoint allowlist extensions
// (config web.file_access_roots — design §5.2).
func (c *PicoChannel) SetFileAccessRoots(roots []string) {
	c.uploads.setRoots(roots)
}

func (c *PicoChannel) uploadOptions() uploadOptions {
	return c.uploads.get()
}

// uploadIndexEntry is one line of inbound/.index.jsonl. Best-effort metadata
// only — the filesystem remains the source of truth (design §11.3).
type uploadIndexEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`        // original (sanitized) filename
	StoredName  string `json:"stored_name"` // on-disk name with id prefix
	Path        string `json:"path"`        // workspace-relative path
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Mtime       string `json:"mtime"`
	Session     string `json:"session,omitempty"`
}

// sanitizeUploadFilename strips path components and control characters so a
// hostile multipart filename cannot escape the target directory or corrupt
// the JSONL index.
func sanitizeUploadFilename(name string) string {
	// path.Base (slash-only), not filepath.Base: on Windows the latter eats
	// backslashes, making the sanitized name OS-dependent. The contract is
	// "keep the whole string, strip separators" regardless of server OS.
	name = path.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == ':' || r == '\\' || r == '/' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "upload.bin"
	}
	if len(out) > 200 {
		ext := filepath.Ext(out)
		out = out[:200-len(ext)] + ext
	}
	return out
}

// detectUploadMediaType prefers magic bytes, then extension, mirroring the
// send_file tool's detection (kept local: importing fstools would pull the
// whole tools package into the channel).
func detectUploadMediaType(path string) string {
	if kind, err := filetype.MatchFile(path); err == nil && kind != filetype.Unknown {
		return kind.MIME.Value
	}
	if ext := filepath.Ext(path); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

// writeUploadIndex appends one JSONL line. Failure is logged and swallowed —
// the index is metadata, never a gate for upload success (design §4.1 step 7).
func writeUploadIndex(workspace string, entry uploadIndexEntry) {
	indexDir := filepath.Join(workspace, uploadInboundDir)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		logger.WarnCF("pico", "Upload index mkdir failed", map[string]any{"error": err.Error()})
		return
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(indexDir, uploadIndexName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logger.WarnCF("pico", "Upload index open failed", map[string]any{"error": err.Error()})
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		logger.WarnCF("pico", "Upload index append failed", map[string]any{"error": err.Error()})
	}
}

// handleUpload implements POST /pico/upload (design §4.1):
// multipart "file" (+ optional "filename"), lands under
// <workspace>/inbound/<yyyymmdd>/<id>_<name>, responds with the
// workspace-relative path for the client to reference in message.send.
func (c *PicoChannel) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	opts := c.uploadOptions()
	if opts.workspace == "" {
		http.Error(w, "upload not configured", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, opts.maxUploadBytes)
	if err := r.ParseMultipartForm(opts.maxUploadBytes); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(
				w,
				fmt.Sprintf("upload too large (max %d bytes)", opts.maxUploadBytes),
				http.StatusRequestEntityTooLarge,
			)
			return
		}
		http.Error(w, "malformed multipart upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing multipart field \"file\"", http.StatusBadRequest)
		return
	}
	defer file.Close()

	name := sanitizeUploadFilename(r.FormValue("filename"))
	if name == "upload.bin" && header.Filename != "" {
		name = sanitizeUploadFilename(header.Filename)
	}

	dayDir := time.Now().Format("20060102")
	id := uuid.NewString()[:8]
	storedName := id + "_" + name
	targetDir := filepath.Join(opts.workspace, uploadInboundDir, dayDir)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		http.Error(w, "failed to create upload directory", http.StatusInternalServerError)
		return
	}
	target := filepath.Join(targetDir, storedName)

	// Write to a temp sibling then rename so a concurrent preview never sees
	// a half-written file (Windows-safe rename per pkg/fileutil conventions).
	tmp, err := os.CreateTemp(targetDir, ".upload-*")
	if err != nil {
		http.Error(w, "failed to stage upload", http.StatusInternalServerError)
		return
	}
	size, copyErr := copyAndClose(tmp, file)
	if copyErr == nil {
		copyErr = os.Rename(tmp.Name(), target)
	}
	if copyErr != nil {
		_ = os.Remove(tmp.Name())
		http.Error(w, "failed to store upload", http.StatusInternalServerError)
		return
	}

	contentType := detectUploadMediaType(target)
	relPath := filepath.ToSlash(filepath.Join(uploadInboundDir, dayDir, storedName))
	writeUploadIndex(opts.workspace, uploadIndexEntry{
		ID:          id,
		Name:        name,
		StoredName:  storedName,
		Path:        relPath,
		Size:        size,
		ContentType: contentType,
		Mtime:       time.Now().UTC().Format(time.RFC3339),
		Session:     strings.TrimSpace(r.FormValue("session")),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"path":         relPath,
		"filename":     name,
		"content_type": contentType,
		"size":         size,
	})
}

func copyAndClose(dst *os.File, src multipartFile) (int64, error) {
	defer dst.Close()
	size, err := io.Copy(dst, src)
	if err != nil {
		return size, err
	}
	return size, dst.Sync()
}

// multipartFile is the readable side of a multipart part (satisfied by
// *multipart.FileHeader.Open's reader and *os.File alike).
type multipartFile interface {
	Read(p []byte) (int, error)
}

// inboundFilePayload mirrors the client-supplied file reference in a
// message.send payload (design §4.4).
type inboundFilePayload struct {
	Path        string `json:"path"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// parseInboundFiles validates payload["files"]: every path must be
// workspace-relative, clean to inside inbound/, and resolve inside the
// workspace (no ../ escapes, no absolute paths, no symlinks out). Returns
// the bus payload plus the [file:<path>] tags to embed in content.
func (c *PicoChannel) parseInboundFiles(
	payload map[string]any,
	workspace string,
) ([]inboundFilePayload, []string, error) {
	raw, ok := payload["files"]
	if !ok || raw == nil {
		return nil, nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("files must be an array")
	}
	if len(items) == 0 {
		return nil, nil, nil
	}
	if workspace == "" {
		return nil, nil, fmt.Errorf("upload not configured")
	}

	var files []inboundFilePayload
	var tags []string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("files entries must be objects")
		}
		path, _ := m["path"].(string)
		filename, _ := m["filename"].(string)
		contentType, _ := m["content_type"].(string)

		rel, err := validateInboundFilePath(path, workspace)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, inboundFilePayload{Path: rel, Filename: filename, ContentType: contentType})

		tag := "[file:" + rel + "]"
		if strings.HasPrefix(strings.ToLower(contentType), "image/") {
			tag = "[image:" + rel + "]"
		}
		tags = append(tags, tag)
	}
	return files, tags, nil
}

// validateInboundFilePath enforces the inbound/ prefix contract: relative,
// clean, inside <workspace>/inbound, and not escaping via symlink.
func validateInboundFilePath(path, workspace string) (string, error) {
	raw := filepath.ToSlash(strings.TrimSpace(path))
	if raw == "" {
		return "", fmt.Errorf("file path is empty")
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("file path must be workspace-relative")
	}
	rel := filepath.Clean(raw)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file path escapes workspace")
	}
	if !strings.HasPrefix(rel, uploadInboundDir+string(filepath.Separator)) {
		return "", fmt.Errorf("file path must be under %s/", uploadInboundDir)
	}
	abs := filepath.Join(workspace, rel)
	inboundRoot, err := filepath.Abs(filepath.Join(workspace, uploadInboundDir))
	if err != nil {
		return "", fmt.Errorf("failed to resolve inbound root")
	}
	// Resolve symlinks of the path itself, or of its nearest existing
	// ancestor when the final file does not exist yet. The ancestor is safe
	// inside the inbound root, or at/above the workspace (the referenced
	// tree does not exist yet — nothing to verify); anything else — a
	// symlink landing elsewhere in the workspace or outside it — escapes.
	if resolved, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = resolved
	} else {
		if ancestor, ancErr := resolveExistingAncestorPath(filepath.Dir(abs)); ancErr == nil {
			if !withinDir(ancestor, inboundRoot) && !withinDir(workspace, ancestor) {
				return "", fmt.Errorf("file path resolves outside %s/", uploadInboundDir)
			}
		}
	}
	if !withinDir(abs, inboundRoot) {
		return "", fmt.Errorf("file path resolves outside %s/", uploadInboundDir)
	}
	return filepath.ToSlash(rel), nil
}

// resolveExistingAncestorPath walks up until EvalSymlinks succeeds, then
// returns that ancestor with the remaining unresolved tail appended.
func resolveExistingAncestorPath(dir string) (string, error) {
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return resolved, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no existing ancestor")
		}
		dir = parent
	}
}

func withinDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
