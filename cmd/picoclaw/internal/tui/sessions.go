package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
)

// SessionEntry is one row of the session picker overlay.
type SessionEntry struct {
	Key       string
	Channel   string // pico / feishu / telegram / …（scope 或 key 前缀推导）
	PicoID    string // 非空 = 可切换的 pico 会话
	Title     string
	UpdatedAt time.Time
	Count     int
}

// maxSessionEntries bounds the picker list (newest first).
const maxSessionEntries = 100

// ListSessions scans <workspace>/sessions/*.meta.json and returns entries
// newest-first. Best-effort: unreadable directory yields nil. Read-only.
func ListSessions(cfg *config.Config) []SessionEntry {
	dir, err := sessionsDirFromConfig(cfg)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]SessionEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		var meta memory.SessionMeta
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		key := meta.Key
		if key == "" {
			key = strings.TrimSuffix(entry.Name(), ".meta.json")
		}
		se := SessionEntry{
			Key:       key,
			Title:     strings.TrimSpace(meta.Title),
			UpdatedAt: meta.UpdatedAt,
			Count:     meta.Count,
		}
		if picoID, ok := picoSessionIDFromMeta(&meta, key); ok {
			se.PicoID = picoID
			se.Channel = "pico"
		} else {
			se.Channel = channelFromMeta(&meta, key)
		}
		out = append(out, se)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > maxSessionEntries {
		out = out[:maxSessionEntries]
	}
	return out
}

// channelFromMeta derives a display channel label: scope.channel first,
// then the "agent:<agentID>:<channel>:…" key shape (pkg/session/key.go).
func channelFromMeta(meta *memory.SessionMeta, key string) string {
	if len(meta.Scope) > 0 {
		var scope struct {
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(meta.Scope, &scope); err == nil {
			if ch := strings.TrimSpace(scope.Channel); ch != "" {
				return ch
			}
		}
	}
	parts := strings.Split(key, ":")
	if len(parts) >= 3 && parts[0] == "agent" {
		if ch := strings.TrimSpace(parts[2]); ch != "" {
			return ch
		}
	}
	return "unknown"
}

// searchToken is the fuzzy target text for one entry.
func (s SessionEntry) searchToken() string {
	return s.Channel + " " + s.Title + " " + s.Key
}

// relativeTime renders a coarse "3 分钟前" style label (zh, dim-friendly).
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " 分钟前"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " 小时前"
	case d < 30*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + " 天前"
	default:
		return t.Format("2006-01-02")
	}
}
