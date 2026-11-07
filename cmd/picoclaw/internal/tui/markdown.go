package tui

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// markdownRenderer renders final answer frames with glamour (dark adaptive
// style, width-aware) and caches results. Streaming frames stay plain dim
// text — the render happens once when Streaming flips to false (design §5.1).
type markdownRenderer struct {
	mu      sync.Mutex
	width   int
	tr      *glamour.TermRenderer
	failed  bool
	cache   map[string]string // content -> rendered (bounded by usage)
	order   []string          // insertion order for eviction
	maxKeys int
}

const markdownCacheLimit = 64

func newMarkdownRenderer() *markdownRenderer {
	return &markdownRenderer{maxKeys: markdownCacheLimit}
}

// renderMarkdown renders md at the given width; on any glamour failure it
// degrades to the original text (design §5.1: never block the timeline).
func (m *markdownRenderer) render(md string, width int) string {
	if strings.TrimSpace(md) == "" {
		return md
	}
	if width < 20 {
		width = 20
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tr == nil || m.width != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width),
			glamour.WithEmoji(),
		)
		if err != nil {
			m.failed = true
			return md
		}
		m.tr = r
		m.width = width
		m.cache = make(map[string]string)
		m.order = nil
	}
	if m.failed {
		return md
	}
	if out, ok := m.cache[md]; ok {
		return out
	}
	out, err := m.tr.Render(md)
	if err != nil {
		return md
	}
	// glamour pads lines to the wrap width with trailing spaces; strip them
	// per line so lipgloss width math and terminal selection stay clean.
	out = stripTrailingSpaces(out)
	if strings.TrimSpace(out) == "" {
		out = md
	}
	m.cache[md] = out
	m.order = append(m.order, md)
	for len(m.order) > m.maxKeys {
		delete(m.cache, m.order[0])
		m.order = m.order[1:]
	}
	return out
}

// reset drops the cache (width change handled internally; :raw toggles don't
// need it because raw rendering bypasses this type entirely).
func (m *markdownRenderer) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache = make(map[string]string)
	m.order = nil
}

// stripTrailingSpaces removes trailing whitespace from every line.
func stripTrailingSpaces(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// containsANSI reports whether s already carries styling escapes (used to
// avoid double-wrapping rendered markdown in lipgloss Width constraints,
// which would re-wrap hard tabs and long code lines).
func containsANSI(s string) bool { return strings.Contains(s, "\x1b[") }

// padBlock pads every line of s with the given background style to full
// width — used for the picker/overlay rows (pi select-list style).
func padBlockLines(s string, width int, st lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if pad := width - lipgloss.Width(ln); pad > 0 {
			ln += strings.Repeat(" ", pad)
		}
		lines[i] = st.Render(ln)
	}
	return strings.Join(lines, "\n")
}
