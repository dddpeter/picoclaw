package tui

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/picoclient"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// legacyPicoSessionPrefix mirrors the launcher's discovery constant
// (web/backend/api/session.go): session keys used by older pico stores.
const legacyPicoSessionPrefix = "agent:main:pico:direct:pico:"

// maxHistoryJSONLLineSize bounds one JSONL record (same order of magnitude as
// the launcher's reader).
const maxHistoryJSONLLineSize = 8 << 20

// SessionHistory is the disk-loaded view of the current pico session.
type SessionHistory struct {
	Title string
	Items []Item
	Found bool
}

// LoadSessionHistory resolves the JSONL session file for a pico session id
// under the configured workspace and decodes it into timeline items. It
// mirrors the launcher's discovery semantics (scope-based pico sessions
// first, then legacy key/alias prefixes) but is read-only and best-effort:
// any error simply yields Found=false and the TUI stays usable.
func LoadSessionHistory(cfg *config.Config, sessionID string) SessionHistory {
	dir, err := sessionsDirFromConfig(cfg)
	if err != nil {
		return SessionHistory{}
	}
	key, meta, ok := findSessionKey(dir, sessionID)
	if !ok {
		return SessionHistory{}
	}
	msgs, err := readSessionJSONL(filepath.Join(dir, sanitizeSessionKey(key)+".jsonl"), meta.Skip)
	if err != nil {
		return SessionHistory{Title: meta.Title, Found: true}
	}
	return SessionHistory{
		Title: meta.Title,
		Items: historyToItems(msgs),
		Found: true,
	}
}

// sessionsDirFromConfig resolves <workspace>/sessions with the same rules as
// the launcher (agents.defaults.workspace, "~" expansion, default home).
func sessionsDirFromConfig(cfg *config.Config) (string, error) {
	workspace := cfg.Agents.Defaults.Workspace
	if strings.TrimSpace(workspace) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		workspace = filepath.Join(home, ".picoclaw", "workspace")
	}
	if workspace == "~" || strings.HasPrefix(workspace, "~/") || strings.HasPrefix(workspace, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		workspace = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(workspace, "~"), string(filepath.Separator)))
	}
	return filepath.Join(workspace, "sessions"), nil
}

// findSessionKey scans *.meta.json and returns the session key whose pico
// session id matches. Dedup semantics follow the launcher: first match wins.
func findSessionKey(dir, sessionID string) (string, memory.SessionMeta, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", memory.SessionMeta{}, false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var meta memory.SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		key := meta.Key
		if key == "" {
			key = strings.TrimSuffix(entry.Name(), ".meta.json")
		}
		if picoID, ok := picoSessionIDFromMeta(&meta, key); ok && picoID == sessionID {
			return key, meta, true
		}
	}
	return "", memory.SessionMeta{}, false
}

// picoSessionIDFromMeta extracts the pico session UUID from a meta entry:
// scope-based sessions first (channel=pico, values sender/chat carry
// "pico:<uuid>"), then the legacy key/alias prefix.
func picoSessionIDFromMeta(meta *memory.SessionMeta, key string) (string, bool) {
	if len(meta.Scope) > 0 {
		var scope struct {
			Channel string            `json:"channel"`
			Values  map[string]string `json:"values"`
		}
		if err := json.Unmarshal(meta.Scope, &scope); err == nil {
			if strings.EqualFold(strings.TrimSpace(scope.Channel), "pico") {
				for _, candidate := range []string{scope.Values["sender"], scope.Values["chat"]} {
					if idx := strings.Index(candidate, "pico:"); idx >= 0 {
						if id := strings.TrimSpace(candidate[idx+len("pico:"):]); id != "" {
							return id, true
						}
					}
				}
			}
		}
	}
	if id, ok := legacyPicoSessionID(key); ok {
		return id, true
	}
	for _, alias := range meta.Aliases {
		if id, ok := legacyPicoSessionID(alias); ok {
			return id, true
		}
	}
	return "", false
}

func legacyPicoSessionID(key string) (string, bool) {
	if strings.HasPrefix(key, legacyPicoSessionPrefix) {
		if id := strings.TrimPrefix(key, legacyPicoSessionPrefix); id != "" {
			return id, true
		}
	}
	return "", false
}

// sanitizeSessionKey mirrors the on-disk key sanitization (":" "/" "\" → "_").
func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

func readSessionJSONL(path string, skip int) ([]protocoltypes.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	msgs := make([]protocoltypes.Message, 0, 64)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxHistoryJSONLLineSize)
	seen := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		seen++
		if seen <= skip {
			continue
		}
		var msg protocoltypes.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, scanner.Err()
}

// historyToItems maps persisted messages onto timeline items with the same
// visual grammar as live streaming (user / thought fold / tool lines /
// tool feedback / answer). Tool-result records are skipped: the tool_calls
// plan lines already represent them.
func historyToItems(msgs []protocoltypes.Message) []Item {
	items := make([]Item, 0, len(msgs))
	for i := range msgs {
		m := &msgs[i]
		// Transient thought-only assistant records are internal runtime
		// artifacts, not displayable conversation turns.
		if messageutil.IsTransientAssistantThoughtMessage(*m) {
			continue
		}
		switch m.Role {
		case "user":
			if strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 && len(m.Media) == 0 {
				continue
			}
			item := Item{
				Kind:        ItemUser,
				Content:     m.Content,
				Attachments: historyAttachments(m),
				Timestamp:   messageTime(m),
			}
			items = append(items, item)

		case "assistant":
			if m.ToolCallID != "" {
				continue // tool result echo; the plan line represents it
			}
			if strings.TrimSpace(m.ReasoningContent) != "" {
				items = append(items, Item{
					Kind:      ItemThought,
					Content:   m.ReasoningContent,
					Timestamp: messageTime(m),
				})
			}
			for _, tc := range m.ToolCalls {
				name, args := "", ""
				if tc.Function != nil {
					name = tc.Function.Name
					args = tc.Function.Arguments
				}
				items = append(items, Item{
					Kind:      ItemToolCalls,
					ToolName:  name,
					ToolArgs:  args,
					Timestamp: messageTime(m),
				})
			}
			atts := historyAttachments(m)
			if strings.TrimSpace(m.Content) == "" {
				if len(atts) > 0 {
					items = append(items, Item{
						Kind:        ItemMedia,
						Attachments: atts,
						Timestamp:   messageTime(m),
					})
				}
				continue
			}
			kind := ItemAnswer
			if strings.HasPrefix(strings.TrimSpace(m.Content), "🔧") {
				kind = ItemToolFeedback
			}
			items = append(items, Item{
				Kind:        kind,
				Content:     m.Content,
				ModelName:   m.ModelName,
				Attachments: atts,
				Timestamp:   messageTime(m),
			})
		}
	}
	return items
}

func historyAttachments(m *protocoltypes.Message) []picoclient.Attachment {
	out := make([]picoclient.Attachment, 0, len(m.Attachments)+len(m.Media))
	for _, att := range m.Attachments {
		url := att.URL
		if url == "" {
			url = att.Ref
		}
		if url == "" {
			continue
		}
		out = append(out, picoclient.Attachment{
			Type: att.Type, URL: url, Filename: att.Filename, ContentType: att.ContentType,
		})
	}
	for _, media := range m.Media {
		if media == "" {
			continue
		}
		out = append(out, picoclient.Attachment{Type: "image", URL: media})
	}
	return out
}

func messageTime(m *protocoltypes.Message) time.Time {
	if m.CreatedAt != nil {
		return *m.CreatedAt
	}
	return time.Time{}
}
