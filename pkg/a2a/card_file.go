package a2a

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// CardFileName is the agent-card spec file, stored next to config.json.
// The gateway generates it once from the workspace persona when the A2A
// endpoint is first enabled; afterwards the file is the source of truth
// (user-editable, hot-reloaded by mtime, never auto-regenerated).
const CardFileName = "agent-card.json"

// CardSpec is the user-editable card identity. Only name/description/url —
// the protocol-level fields (protocolVersion, capabilities, securitySchemes,
// default modes) stay code-derived so the wire contract can't be broken by
// a typo in the file.
type CardSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// URL overrides the advertised base URL. Empty = the gateway's listen
	// address (fine for loopback discovery; set a reachable URL when the
	// gateway is behind NAT/proxy — "http://0.0.0.0:18790" is not dialable).
	URL string `json:"url,omitempty"`
}

// LoadCardSpec reads the card spec file. ok=false when the file is absent
// (not an error); a malformed file IS an error — the caller falls back to
// the derived default rather than serving garbage.
func LoadCardSpec(path string) (spec *CardSpec, ok bool, err error) {
	data, rerr := os.ReadFile(path)
	if os.IsNotExist(rerr) {
		return nil, false, nil
	}
	if rerr != nil {
		return nil, false, fmt.Errorf("read card spec: %w", rerr)
	}
	var s CardSpec
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, false, fmt.Errorf("parse card spec %s: %w", path, err)
	}
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.URL = strings.TrimSpace(s.URL)
	return &s, true, nil
}

// SaveCardSpec writes the spec atomically (0644: the card is a public
// discovery document by design — persona-derived, never memory content).
func SaveCardSpec(path string, spec CardSpec) error {
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// EnsureCardFile generates the card spec file from the workspace persona
// when it does not exist yet. An existing file is never touched (user
// edits win). Write errors are logged and returned, but are non-fatal:
// the serving side falls back to the generic default card.
func EnsureCardFile(path, workspace string) error {
	if _, exists, err := LoadCardSpec(path); err != nil {
		return err // malformed existing file: report, don't overwrite
	} else if exists {
		return nil
	}
	spec := DeriveDefaultCardSpec(workspace)
	if err := SaveCardSpec(path, spec); err != nil {
		return fmt.Errorf("write default card spec: %w", err)
	}
	logger.InfoCF("a2a", "agent-card.json generated from workspace persona", map[string]any{
		"path":      path,
		"name":      spec.Name,
		"workspace": workspace,
	})
	return nil
}

// DeriveDefaultCardSpec builds the card identity from the workspace
// persona: AGENT.md frontmatter (name, description) → SOUL.md first
// meaningful line → generic fallback. Long-term memory (memory/MEMORY.md)
// is deliberately NOT consulted: the card is an unauthenticated discovery
// document and persona text is public-by-design while memory is not.
func DeriveDefaultCardSpec(workspace string) CardSpec {
	name, description := readPersonaIdentity(workspace)
	if name == "" {
		name = "PicoClaw"
	}
	if description == "" {
		description = "PicoClaw personal AI agent"
	}
	return CardSpec{Name: name, Description: description}
}

// cardDescriptionMaxRunes caps the SOUL.md-derived description so a rambling
// soul file can't bloat the discovery document.
const cardDescriptionMaxRunes = 200

// readPersonaIdentity returns (name, description) from the workspace
// persona files, both possibly empty.
func readPersonaIdentity(workspace string) (name, description string) {
	if strings.TrimSpace(workspace) == "" {
		return "", ""
	}
	name, description = readAgentFrontmatter(filepath.Join(workspace, "AGENT.md"))
	if description == "" {
		description = firstSoulLine(filepath.Join(workspace, "SOUL.md"))
	}
	return name, description
}

// readAgentFrontmatter parses the YAML frontmatter of AGENT.md for
// name/description only. It mirrors pkg/agent's splitAgentFrontmatter in
// spirit but is deliberately local: pkg/a2a must not depend on the agent
// package (the agent loop already depends on this package's channel names).
func readAgentFrontmatter(path string) (name, description string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	content := string(parser.NormalizeNewlines(data))
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", "" // body-only AGENT.md: no machine-readable identity
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", ""
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		logger.WarnCF("a2a", "AGENT.md frontmatter unparsable for card derivation", map[string]any{
			"path":  path,
			"error": err.Error(),
		})
		return "", ""
	}
	return strings.TrimSpace(fm.Name), strings.TrimSpace(fm.Description)
}

// firstSoulLine returns the first meaningful line of SOUL.md (skipping
// headings and blank lines), truncated. A markdown heading is identity
// structure, not identity content — skip it.
func firstSoulLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(parser.NormalizeNewlines(data))
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return truncateRunes(line, cardDescriptionMaxRunes)
	}
	return ""
}

func truncateRunes(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

// ReloadableCard serves the agent card from the spec file, re-reading it
// when the file's mtime changes (edit → next request serves the new
// content, no restart). When the file is missing or unreadable it serves
// the last known card — or the generic default before the first successful
// read — so a botched edit can never take discovery down.
type ReloadableCard struct {
	path     string
	baseURL  string
	withAuth bool

	mu      sync.Mutex
	card    AgentCard // last successfully built card
	specMod time.Time // mtime the cached card was built from
	loaded  bool      // whether a successful build ever happened
}

// NewReloadableCard builds the card source. baseURL backs the URL field
// when the spec file doesn't override it.
func NewReloadableCard(path, baseURL string, withAuth bool) *ReloadableCard {
	return &ReloadableCard{path: path, baseURL: baseURL, withAuth: withAuth}
}

// Card returns the current card, refreshing from disk when the spec file
// changed since the last build.
func (rc *ReloadableCard) Card() AgentCard {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	mod, err := fileModTime(rc.path)
	if err == nil && rc.loaded && mod.Equal(rc.specMod) {
		return rc.card // unchanged (or unreadable since — keep last known)
	}
	spec, ok, err := LoadCardSpec(rc.path)
	if err != nil {
		logger.WarnCF("a2a", "card spec unreadable; serving last known card", map[string]any{
			"path":  rc.path,
			"error": err.Error(),
		})
		if rc.loaded {
			return rc.card
		}
		spec = nil // fall through to generic default
	}
	if !ok && rc.loaded {
		// File existed at startup and was deleted: keep serving the last
		// known card rather than silently regressing to the generic one.
		return rc.card
	}
	if spec == nil || !ok {
		spec = &CardSpec{Name: "PicoClaw", Description: "PicoClaw personal AI agent"}
	}
	rc.card = newCardFromSpec(*spec, rc.baseURL, rc.withAuth)
	rc.specMod = mod
	rc.loaded = true
	return rc.card
}

// Handler serves GET /.well-known/agent-card.json from the reloadable source.
func (rc *ReloadableCard) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rc.Card())
	}
}

// fileModTime stats the file's mtime; a missing/unreadable file returns
// the zero time without an error distinction the caller can't act on —
// the Card() load path handles absence separately.
func fileModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
