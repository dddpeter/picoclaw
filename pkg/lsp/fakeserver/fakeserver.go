// Package fakeserver is a programmable minimal LSP server for tests: it
// answers initialize, serves diagnostics via pull and/or push, and can be
// launched as a helper process by re-invoking any test binary with
// -test.run=<Helper> and the env vars below.
//
// Env contract:
//
//	PICOCLAW_FAKESERVER=1        activate (helper entry points Skip without it)
//	PICOCLAW_FAKESERVER_PULL=1   advertise diagnosticProvider (pull mode)
//	PICOCLAW_FAKESERVER_PUSH=1   publish diagnostics on didOpen/didChange
//	PICOCLAW_FAKESERVER_DIAGS    JSON map[documentKey][]lsp.Diagnostic
package fakeserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Diag mirrors the LSP diagnostic shape for configuring the fake server.
// It is deliberately local to this package: importing pkg/lsp here would
// create a test import cycle.
type Diag struct {
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	} `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// DiagAt builds a single-character Diag.
func DiagAt(line, char, severity int, message string) Diag {
	var d Diag
	d.Range.Start.Line = line
	d.Range.Start.Character = char
	d.Range.End.Line = line
	d.Range.End.Character = char + 1
	d.Severity = severity
	d.Source = "fake"
	d.Message = message
	return d
}

// Serve runs the fake server loop on the given stdio until "exit".
func Serve(r io.Reader, w io.Writer) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var outMu sync.Mutex

	write := func(v any) {
		outMu.Lock()
		defer outMu.Unlock()
		body, _ := json.Marshal(v)
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	respond := func(id int64, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	notify := func(method string, params any) {
		write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}

	pull := os.Getenv("PICOCLAW_FAKESERVER_PULL") == "1"
	push := os.Getenv("PICOCLAW_FAKESERVER_PUSH") == "1"
	diags := map[string][]Diag{}
	if raw := os.Getenv("PICOCLAW_FAKESERVER_DIAGS"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &diags)
	}
	var actions []fakeAction
	if raw := os.Getenv("PICOCLAW_FAKESERVER_ACTIONS"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &actions)
	}

	for {
		length, err := readHeader(reader)
		if err != nil {
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}

		uriOf := func() string {
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			return p.TextDocument.URI
		}
		diagsOf := func(uri string) []Diag {
			return append([]Diag(nil), diags[docKey(uri)]...)
		}

		switch msg.Method {
		case "initialize":
			caps := map[string]any{}
			if pull {
				caps["diagnosticProvider"] = map[string]any{"interFileDependencies": false}
			}
			if len(actions) > 0 {
				// Actions ship without edits; the client must resolve them.
				caps["codeActionProvider"] = map[string]any{"resolveProvider": true}
			}
			respond(*msg.ID, map[string]any{"capabilities": caps})
		case "shutdown":
			if marker := os.Getenv("PICOCLAW_FAKESERVER_SHUTDOWN_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte("shutdown"), 0o644)
			}
			respond(*msg.ID, nil)
		case "exit":
			return
		case "textDocument/didOpen":
			uri := uriOf()
			if push {
				notify("textDocument/publishDiagnostics", map[string]any{
					"uri": uri, "version": 1, "diagnostics": diagsOf(uri),
				})
			}
		case "textDocument/didChange":
			uri := uriOf()
			if push {
				notify("textDocument/publishDiagnostics", map[string]any{
					"uri": uri, "version": 2, "diagnostics": diagsOf(uri),
				})
			}
		case "textDocument/diagnostic":
			uri := uriOf()
			respond(*msg.ID, map[string]any{"kind": "full", "items": diagsOf(uri)})
		case "textDocument/codeAction":
			var p struct {
				Context struct {
					Only []string `json:"only"`
				} `json:"context"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			only := ""
			if len(p.Context.Only) > 0 {
				only = p.Context.Only[0]
			}
			var out []any
			for _, a := range actions {
				if only == "" || a.Kind == only || strings.HasPrefix(a.Kind, only+".") {
					out = append(out, map[string]any{"title": a.Title, "kind": a.Kind})
				}
			}
			respond(*msg.ID, out)
		case "codeAction/resolve":
			var a struct {
				Title string `json:"title"`
				Kind  string `json:"kind"`
			}
			_ = json.Unmarshal(msg.Params, &a)
			for _, fa := range actions {
				if fa.Title == a.Title && fa.Kind == a.Kind {
					edits := make([]map[string]any, 0, len(fa.Edits))
					for _, e := range fa.Edits {
						edits = append(edits, map[string]any{
							"range": map[string]any{
								"start": map[string]any{"line": e.SL, "character": e.SC},
								"end":   map[string]any{"line": e.EL, "character": e.EC},
							},
							"newText": e.NewText,
						})
					}
					respond(*msg.ID, map[string]any{
						"title": fa.Title,
						"kind":  fa.Kind,
						"edit": map[string]any{
							"documentChanges": []any{map[string]any{
								"textDocument": map[string]any{"uri": fa.URI},
								"edits":        edits,
							}},
						},
					})
					break
				}
			}
		}
	}
}

// fakeAction configures a code action served by the fake server.
type fakeAction struct {
	Title string         `json:"title"`
	Kind  string         `json:"kind"`
	URI   string         `json:"uri"`
	Edits []fakeEditSpec `json:"edits"`
}

type fakeEditSpec struct {
	SL      int    `json:"sl"`
	SC      int    `json:"sc"`
	EL      int    `json:"el"`
	EC      int    `json:"ec"`
	NewText string `json:"newText"`
}

func readHeader(r *bufio.Reader) (int, error) {
	length := 0
	seen := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = trimEOL(line)
		if line == "" {
			if seen {
				return length, nil
			}
			continue
		}
		for i := 0; i < len(line); i++ {
			if line[i] == ':' {
				if equalFold(line[:i], "Content-Length") {
					var n int
					if _, err := fmt.Sscanf(trimSpace(line[i+1:]), "%d", &n); err == nil {
						length = n
						seen = true
					}
				}
				break
			}
		}
	}
}

func trimEOL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Env builds the helper-process env map (mirrors the contract above).
func Env(diags map[string][]Diag, pull, push bool) map[string]string {
	env := map[string]string{"PICOCLAW_FAKESERVER": "1"}
	if pull {
		env["PICOCLAW_FAKESERVER_PULL"] = "1"
	}
	if push {
		env["PICOCLAW_FAKESERVER_PUSH"] = "1"
	}
	if diags != nil {
		b, _ := json.Marshal(diags)
		env["PICOCLAW_FAKESERVER_DIAGS"] = string(b)
	}
	return env
}

// Active reports whether this process was launched as a fake server (the
// helper test uses this to decide between Skip and Serve).
func Active() bool { return os.Getenv("PICOCLAW_FAKESERVER") == "1" }

// docKey canonicalizes a file URI the same way the client does
// (DocumentKey in pkg/lsp: parsed, percent-decoded, cleaned, lowercased on
// Windows), so env-configured diagnostics match the documents the client
// opens.
func docKey(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Host != "" {
		return uri
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) > 1 && p[0] == '/' && len(p) > 2 && p[2] == ':' {
		p = p[1:]
	}
	p = filepath.FromSlash(p)
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}
