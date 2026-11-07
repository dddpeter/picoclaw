package tui

import (
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/commands"
)

// completionSuggestion is one row of the completion popup.
type completionSuggestion struct {
	// Replace is the text substituted for the current command token when the
	// suggestion is accepted, e.g. "/switch model" or ":copy".
	Replace string
	Label   string // display name (bold)
	Hint    string // display description / args (dim)
}

// completionState is the terminal-independent completion engine: given the
// composer text it produces the filtered suggestion list and tracks the
// selected row. Rendering and key routing live in app.go.
type completionState struct {
	kind      int // 0 = inactive, '/' = server commands, ':' = local commands
	query     string
	items     []completionSuggestion
	filtered  []int // indices into items
	selected  int
	dismissed bool // Esc closed it; re-arm only after the prefix goes away
}

const completionTrigger = '/'
const localTrigger = ':'

// maxCompletionRows bounds the popup height.
const maxCompletionRows = 8

var (
	serverCommands     []commands.Definition
	serverCommandsOnce sync.Once
)

// loadServerCommands builds the "/" suggestion catalog from the server-side
// builtin definitions. Built once per process.
func loadServerCommands() []commands.Definition {
	serverCommandsOnce.Do(func() {
		serverCommands = commands.BuiltinDefinitions()
	})
	return serverCommands
}

// localCommand describes one ":" command for completion and the help sheet.
type localCommand struct {
	Name  string
	Desc  string
	Usage string
}

var localCommands = []localCommand{
	{Name: "help", Desc: "键位与命令帮助浮层", Usage: ":help"},
	{Name: "q", Desc: "退出", Usage: ":q"},
	{Name: "quit", Desc: "退出", Usage: ":quit"},
	{Name: "stop", Desc: "停止当前回合", Usage: ":stop"},
	{Name: "new", Desc: "开新会话", Usage: ":new"},
	{Name: "clear", Desc: "清空本地时间线", Usage: ":clear"},
	{Name: "ls", Desc: "打开会话选择浮层", Usage: ":ls"},
	{Name: "sessions", Desc: "打开会话选择浮层", Usage: ":sessions"},
	{Name: "model", Desc: "查看/切换模型（无参开浮层）", Usage: ":model [名]"},
	{Name: "status", Desc: "运行状态总览", Usage: ":status"},
	{Name: "context", Desc: "上下文构成详情", Usage: ":context"},
	{Name: "title", Desc: "设置会话标题", Usage: ":title <文本>"},
	{Name: "reload", Desc: "重载配置", Usage: ":reload"},
	{Name: "usage", Desc: "token 用量浮层", Usage: ":usage"},
	{Name: "copy", Desc: "复制第 n 条答案（默认 1）", Usage: ":copy [n]"},
	{Name: "raw", Desc: "切换答案 markdown/原文渲染", Usage: ":raw"},
}

// updateCompletion recomputes the completion state from the composer text.
// The popup only triggers when the first non-space character is '/' or ':'
// (so chat prose containing "/path" never pops it up).
func updateCompletion(cs *completionState, text string) {
	trimmed := strings.TrimLeft(text, " ")
	if trimmed == "" || !strings.HasPrefix(trimmed, string(completionTrigger)) && !strings.HasPrefix(trimmed, string(localTrigger)) {
		cs.reset()
		return
	}
	trigger := rune(trimmed[0])
	rest := trimmed[1:]
	// Only complete the command token: anything after the first space means
	// the user moved on to arguments.
	if strings.ContainsRune(rest, ' ') {
		cs.reset()
		return
	}
	if cs.dismissed && cs.kind == int(trigger) {
		// Esc dismissed this prefix mode; stay quiet until the text leaves it.
		cs.query = rest
		return
	}
	cs.kind = int(trigger)
	cs.query = rest
	cs.rebuild(trigger, rest)
}

func (cs *completionState) reset() {
	cs.kind = 0
	cs.query = ""
	cs.filtered = nil
	cs.selected = 0
	cs.dismissed = false
}

func (cs *completionState) rebuild(trigger rune, query string) {
	var items []completionSuggestion
	if trigger == completionTrigger {
		items = serverCompletionItems()
	} else {
		items = localCompletionItems()
	}
	cs.items = items

	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Label + " " + it.Hint
	}
	matches := fuzzyFilter(query, texts, maxCompletionRows)
	cs.filtered = make([]int, 0, len(matches))
	for _, m := range matches {
		cs.filtered = append(cs.filtered, m.Index)
	}
	if cs.selected >= len(cs.filtered) {
		cs.selected = max(len(cs.filtered)-1, 0)
	}
	cs.dismissed = false
}

// serverCompletionItems builds "/" suggestions: command names + aliases and
// the second-level subcommands of builtin definitions ("/switch model").
func serverCompletionItems() []completionSuggestion {
	defs := loadServerCommands()
	items := make([]completionSuggestion, 0, len(defs)*2)
	for _, d := range defs {
		items = append(items, completionSuggestion{
			Replace: "/" + d.Name,
			Label:   "/" + d.Name,
			Hint:    d.Description,
		})
		for _, sc := range d.SubCommands {
			if sc.Name == "" {
				continue
			}
			repl := "/" + d.Name + " " + sc.Name
			hint := sc.Description
			if sc.ArgsUsage != "" {
				hint = sc.ArgsUsage + " — " + hint
			}
			items = append(items, completionSuggestion{
				Replace: repl,
				Label:   repl,
				Hint:    hint,
			})
		}
		for _, alias := range d.Aliases {
			items = append(items, completionSuggestion{
				Replace: "/" + alias,
				Label:   "/" + alias,
				Hint:    "同 /" + d.Name,
			})
		}
	}
	return items
}

func localCompletionItems() []completionSuggestion {
	items := make([]completionSuggestion, 0, len(localCommands))
	for _, lc := range localCommands {
		items = append(items, completionSuggestion{
			Replace: ":" + lc.Name,
			Label:   ":" + lc.Name,
			Hint:    lc.Desc,
		})
	}
	return items
}

// active reports whether the popup should render with at least one row.
func (cs *completionState) active() bool {
	return cs.kind != 0 && !cs.dismissed && len(cs.filtered) > 0
}

// move shifts the selection by delta (wrapping); returns true when changed.
func (cs *completionState) move(delta int) bool {
	n := len(cs.filtered)
	if n == 0 {
		return false
	}
	next := (cs.selected + delta + n) % n
	if next == cs.selected {
		return false
	}
	cs.selected = next
	return true
}

// accept returns the replacement text for the current selection.
func (cs *completionState) accept() string {
	if !cs.active() {
		return ""
	}
	return cs.items[cs.filtered[cs.selected]].Replace
}

// visibleRows returns the popup rows (label/hint pairs) for rendering.
func (cs *completionState) visibleRows() []completionSuggestion {
	out := make([]completionSuggestion, 0, len(cs.filtered))
	for _, idx := range cs.filtered {
		out = append(out, cs.items[idx])
	}
	return out
}

// sortedServerCommandHelp returns builtin commands sorted by name for the
// help overlay.
func sortedServerCommandHelp() []commands.Definition {
	defs := loadServerCommands()
	out := append([]commands.Definition(nil), defs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
