package lsp

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FileURI converts an absolute file path to a file:// URI (percent-encoded,
// forward slashes, always the authority-less file:/// form — on Windows a
// bare "C:/..." path would render as file://C:/... with the drive letter
// parsed as the URI host, which real servers reply to in canonical form,
// breaking diagnostic matching).
func FileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// DirectoryURI is FileURI with a trailing slash, as servers expect for
// workspace roots.
func DirectoryURI(path string) string {
	uri := FileURI(path)
	if !strings.HasSuffix(uri, "/") {
		uri += "/"
	}
	return uri
}

// DocumentKey canonicalizes a document URI so diagnostics published with a
// different-but-equivalent URI encoding still match the requested document
// (e.g. file:///c%3A/dir/a.go vs file:///C:/dir/a.go — see design §2.3).
func DocumentKey(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Host != "" {
		return uri
	}
	path := u.Path
	if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.FromSlash(path)
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// LanguageID maps a file extension to an LSP language identifier. Small
// table covering the built-in catalog; unknown extensions fall back to the
// extension without the dot.
func LanguageID(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	ids := map[string]string{
		".c": "c", ".cc": "cpp", ".cpp": "cpp", ".cs": "csharp",
		".cts": "typescript", ".cxx": "cpp", ".go": "go", ".h": "c",
		".h++": "cpp", ".hh": "cpp", ".hpp": "cpp", ".hxx": "cpp",
		".java": "java", ".js": "javascript", ".jsx": "javascriptreact",
		".mjs": "javascript", ".mts": "typescript", ".py": "python",
		".pyi": "python", ".rs": "rust", ".ts": "typescript",
		".tsx": "typescriptreact", ".vue": "vue",
	}
	if id, ok := ids[ext]; ok {
		return id
	}
	return strings.TrimPrefix(ext, ".")
}

// Supports reports whether cfg handles the given file path.
func (cfg ServerConfig) Supports(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, e := range cfg.Extensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// CommandAvailable reports whether the configured command can be found on
// PATH (or is an absolute path that exists).
func (cfg ServerConfig) CommandAvailable() bool {
	_, err := exec.LookPath(cfg.Command)
	return err == nil
}
