package pico

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

// webFileKind buckets a file for frontend renderer dispatch (design §5.6).
// The server is authoritative: the frontend pre-judges by extension but the
// X-Picoclaw-File-Kind header decides.
type webFileKind string

const (
	kindText   webFileKind = "text"
	kindImage  webFileKind = "image"
	kindAudio  webFileKind = "audio"
	kindVideo  webFileKind = "video"
	kindBinary webFileKind = "binary"
)

// textLikeExtensions extends MIME text/* with source/config formats servers
// commonly report as application/*.
var textLikeExtensions = map[string]bool{
	".json": true, ".xml": true, ".yaml": true, ".yml": true,
	".js": true, ".mjs": true, ".ts": true, ".tsx": true, ".jsx": true,
	".py": true, ".rb": true, ".go": true, ".rs": true, ".java": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cs": true,
	".sql": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true,
	".css": true, ".scss": true, ".less": true, ".html": true, ".htm": true,
	".csv": true, ".tsv": true, ".md": true, ".markdown": true, ".rst": true,
	".toml": true, ".ini": true, ".cfg": true, ".conf": true, ".env": true,
	".log": true, ".txt": true, ".svg": true, ".proto": true, ".mod": true,
	".sum": true, ".lock": true, ".gitignore": true, ".dockerfile": true,
	".properties": true, ".gradle": true, ".kt": true, ".swift": true,
	".php": true, ".lua": true, ".pl": true, ".r": true, ".m": true,
}

// classifiesAsText reports whether the MIME type reads as textual.
func classifiesAsText(contentType string, path string) bool {
	lower := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(lower, "text/") {
		return true
	}
	switch lower {
	case "application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/yaml", "application/sql",
		"application/x-sh", "application/toml", "application/graphql":
		return true
	}
	if strings.HasPrefix(lower, "application/") && strings.HasSuffix(lower, "+json") {
		return true
	}
	// HTML documents are previewable (rendered in a sandboxed iframe), so
	// they must be classified as text even when the server reports a
	// non-text MIME type.
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".xhtml":
		return true
	}
	return textLikeExtensions[strings.ToLower(filepath.Ext(path))]
}

func classifyWebFileKind(contentType, path string) webFileKind {
	lower := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(lower, "image/"):
		return kindImage
	case strings.HasPrefix(lower, "audio/"):
		return kindAudio
	case strings.HasPrefix(lower, "video/"):
		return kindVideo
	case classifiesAsText(contentType, path):
		return kindText
	default:
		return kindBinary
	}
}

// requiresAttachmentDisposition confines content the browser must never
// render inline from a chat origin (design §5.1: SVG XSS posture).
//
// HTML is served as text (the preview renders it in a sandboxed iframe
// without same-origin, so it cannot reach our cookies or DOM) — only SVG
// stays an attachment, since an inline SVG document shares the page origin.
func requiresAttachmentDisposition(kind webFileKind, contentType, path string) bool {
	if kind == kindBinary {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(lower, "svg") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		return true
	}
	return false
}

// resolveWebFilePath applies the web allowlist (workspace ∪ configured
// roots, system-dir veto, symlink checks) and returns the absolute path.
func (c *PicoChannel) resolveWebFilePath(rawPath string) (string, error) {
	opts := c.uploadOptions()
	if opts.workspace == "" {
		return "", fmt.Errorf("web file access not configured")
	}
	return fstools.ResolveWebPath(rawPath, opts.workspace, opts.fileAccessRoots)
}

// handleFileRead implements GET /pico/file (design §5.1): stream a workspace
// file for preview with Range support; the kind header drives rendering and
// text/html/svg always download.
func (c *PicoChannel) handleFileRead(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		http.Error(w, "missing path parameter", http.StatusBadRequest)
		return
	}
	abs, err := c.resolveWebFilePath(rawPath)
	if err != nil {
		// Minimal-body 403: existence is not disclosed for unauthorized paths.
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}

	info, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	contentType := detectUploadMediaType(abs)
	kind := classifyWebFileKind(contentType, abs)
	forceDownload := r.URL.Query().Get("download") == "1"

	disposition := "inline"
	if forceDownload || requiresAttachmentDisposition(kind, contentType, abs) {
		disposition = "attachment"
	}

	name := filepath.Base(abs)
	w.Header().Set("X-Picoclaw-File-Kind", string(kind))
	w.Header().Set("X-Picoclaw-File-Name", url.PathEscape(name))
	if kind == kindText && strings.HasPrefix(strings.ToLower(contentType), "text/") &&
		!strings.Contains(strings.ToLower(contentType), "charset=") {
		contentType = contentType + "; charset=utf-8"
	}
	if cd := mime.FormatMediaType(disposition, map[string]string{"filename": name}); cd != "" {
		w.Header().Set("Content-Disposition", cd)
	}
	w.Header().Set("Content-Type", contentType)

	file, err := os.Open(abs)
	if err != nil {
		http.Error(w, "failed to open file", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// treeEntry is one row of the /pico/tree listing (design §11.2).
type treeEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // workspace-relative, slash-separated
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

const (
	maxTreeDepth   = 3
	maxTreeEntries = 2000
)

// handleTree implements GET /pico/tree (design §11.2): single-level listing
// by default, optional recursion up to depth 3 with a hard entry cap.
func (c *PicoChannel) handleTree(w http.ResponseWriter, r *http.Request) {
	if !c.IsRunning() {
		http.Error(w, "channel not running", http.StatusServiceUnavailable)
		return
	}
	if !c.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		rawPath = "."
	}
	depth := 1
	if d := r.URL.Query().Get("depth"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &depth); err != nil || depth < 1 {
			http.Error(w, "invalid depth parameter", http.StatusBadRequest)
			return
		}
		if depth > maxTreeDepth {
			depth = maxTreeDepth
		}
	}

	abs, err := c.resolveWebFilePath(rawPath)
	if err != nil {
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !info.IsDir() {
		http.Error(w, "path is not a directory", http.StatusBadRequest)
		return
	}

	opts := c.uploadOptions()
	entries, truncated := walkTreeDir(abs, depth, func(child string) (treeEntry, bool) {
		return buildTreeEntry(opts.workspace, child)
	})
	if entries == nil && !truncated {
		entries = []treeEntry{}
	}

	writeJSONResponse(w, map[string]any{
		"path":      relTreePath(opts.workspace, abs),
		"entries":   entries,
		"truncated": truncated,
	})
}

// writeJSONResponse emits a JSON body with explicit no-store caching.
func writeJSONResponse(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// buildTreeEntry converts one child path into a listing row; false means the
// entry is hidden (.dot / node_modules) and must be skipped.
func buildTreeEntry(workspace, child string) (treeEntry, bool) {
	name := filepath.Base(child)
	if isHiddenTreeEntry(name) {
		return treeEntry{}, false
	}
	info, err := os.Stat(child)
	if err != nil {
		return treeEntry{}, false
	}
	return treeEntry{
		Name:  name,
		Path:  relTreePath(workspace, child),
		IsDir: info.IsDir(),
		Size:  info.Size(),
		Mtime: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	}, true
}

func isHiddenTreeEntry(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// relTreePath renders a workspace-relative slash path; outside the workspace
// it falls back to the cleaned absolute path (configured roots).
func relTreePath(workspace, abs string) string {
	if rel, err := filepath.Rel(workspace, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(filepath.Clean(abs))
}

// walkTreeDir lists dir up to depth levels; hidden entries are skipped and
// the total is capped at maxTreeEntries with truncated=true.
func walkTreeDir(dir string, depth int, entryOf func(string) (treeEntry, bool)) ([]treeEntry, bool) {
	var out []treeEntry
	truncated := false
	var recurse func(current string, level int)
	recurse = func(current string, level int) {
		if truncated {
			return
		}
		items, err := os.ReadDir(current)
		if err != nil {
			return
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir() != items[j].IsDir() {
				return items[i].IsDir()
			}
			return items[i].Name() < items[j].Name()
		})
		for _, item := range items {
			if truncated {
				return
			}
			child := filepath.Join(current, item.Name())
			entry, ok := entryOf(child)
			if !ok {
				continue
			}
			if len(out) >= maxTreeEntries {
				truncated = true
				return
			}
			out = append(out, entry)
			if entry.IsDir && level < depth {
				recurse(child, level+1)
			}
		}
	}
	recurse(dir, 1)
	return out, truncated
}
