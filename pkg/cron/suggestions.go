package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// Automation suggestions — proposed cron jobs the user accepts with one
// command (or one agent action). Borrowed from hermes-agent's consent-first
// design: nothing here ever auto-creates a real job; acceptance is always an
// explicit user/model decision. Producers (e.g. the evolution learning loop)
// only ever propose; dismissed proposals are latched by dedup key so they are
// never re-offered.

const (
	SuggestionStatusPending   = "pending"
	SuggestionStatusAccepted  = "accepted"
	SuggestionStatusDismissed = "dismissed"

	// MaxPendingSuggestions caps the pending list so it never becomes a nag
	// wall; when full, new proposals are dropped.
	MaxPendingSuggestions = 5
	// MaxTotalSuggestions bounds the file: oldest non-pending entries are
	// evicted first; if everything is pending, the oldest pending entry goes.
	MaxTotalSuggestions = 100
)

// ValidSuggestionSources enumerates who may propose. The evolution learning
// loop proposes under "evolution"; users/models may add under "manual".
var ValidSuggestionSources = map[string]bool{
	"evolution": true,
	"manual":    true,
}

type Suggestion struct {
	ID          string       `json:"id"`
	DedupKey    string       `json:"dedup_key"`
	Name        string       `json:"name"`
	Message     string       `json:"message"`
	Schedule    CronSchedule `json:"schedule"`
	Rationale   string       `json:"rationale,omitempty"`
	Source      string       `json:"source"`
	Status      string       `json:"status"`
	JobID       string       `json:"job_id,omitempty"`
	CreatedAtMS int64        `json:"created_at_ms"`
	UpdatedAtMS int64        `json:"updated_at_ms"`
}

type suggestionStoreFile struct {
	Version     int          `json:"version"`
	Suggestions []Suggestion `json:"suggestions"`
}

// SuggestionManager persists suggestions next to the cron job store
// (workspace/cron/suggestions.json). Safe for concurrent use; file writes are
// atomic with fsync (same utility as jobs.json).
type SuggestionManager struct {
	path string
	mu   sync.Mutex
}

// SuggestionsPath derives the suggestion store path from a jobs.json path so
// both files always live side by side.
func SuggestionsPath(jobsStorePath string) string {
	return filepath.Join(filepath.Dir(jobsStorePath), "suggestions.json")
}

func NewSuggestionManager(jobsStorePath string) *SuggestionManager {
	return &SuggestionManager{path: SuggestionsPath(jobsStorePath)}
}

func (m *SuggestionManager) Path() string { return m.path }

func (m *SuggestionManager) load() (*suggestionStoreFile, error) {
	store := &suggestionStoreFile{Version: 1, Suggestions: []Suggestion{}}
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, store); err != nil {
		return nil, err
	}
	if store.Suggestions == nil {
		store.Suggestions = []Suggestion{}
	}
	return store, nil
}

func (m *SuggestionManager) save(store *suggestionStoreFile) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(m.path, data, 0o600)
}

// List returns suggestions, optionally filtered by status (nil = all).
func (m *SuggestionManager) List(status string) []Suggestion {
	m.mu.Lock()
	defer m.mu.Unlock()

	store, err := m.load()
	if err != nil {
		return []Suggestion{}
	}
	if status == "" {
		return store.Suggestions
	}
	var out []Suggestion
	for _, s := range store.Suggestions {
		if s.Status == status {
			out = append(out, s)
		}
	}
	return out
}

// Add proposes a new automation. Dedup: any existing entry (pending, accepted,
// or dismissed) with the same DedupKey short-circuits — dismissed proposals
// are latched forever, pending/accepted ones are not duplicated. When the
// pending cap is reached the proposal is dropped (reported via the returned
// bool). ValidateSchedule errors and unknown sources are rejected.
func (m *SuggestionManager) Add(s Suggestion) (Suggestion, bool, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Message = strings.TrimSpace(s.Message)
	s.DedupKey = strings.TrimSpace(s.DedupKey)
	if s.Name == "" || s.Message == "" || s.DedupKey == "" {
		return Suggestion{}, false, fmt.Errorf("suggestion requires name, message, and dedup_key")
	}
	if !ValidSuggestionSources[s.Source] {
		return Suggestion{}, false, fmt.Errorf("unknown suggestion source %q (valid: evolution, manual)", s.Source)
	}
	if err := ValidateSchedule(s.Schedule); err != nil {
		return Suggestion{}, false, fmt.Errorf("invalid suggestion schedule: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	store, err := m.load()
	if err != nil {
		return Suggestion{}, false, err
	}
	for _, existing := range store.Suggestions {
		if existing.DedupKey == s.DedupKey {
			return existing, false, nil
		}
	}

	pending := 0
	for _, existing := range store.Suggestions {
		if existing.Status == SuggestionStatusPending {
			pending++
		}
	}
	if pending >= MaxPendingSuggestions {
		return Suggestion{}, false, nil
	}

	now := time.Now().UnixMilli()
	s.ID = generateSuggestionID()
	s.Status = SuggestionStatusPending
	s.CreatedAtMS = now
	s.UpdatedAtMS = now
	store.Suggestions = append(store.Suggestions, s)
	m.trim(store)
	if err := m.save(store); err != nil {
		return Suggestion{}, false, err
	}
	return s, true, nil
}

// UpdateStatus moves a suggestion to accepted (with the created job id) or
// dismissed. Idempotent for repeated transitions to the same status.
func (m *SuggestionManager) UpdateStatus(id, status, jobID string) (Suggestion, error) {
	if status != SuggestionStatusAccepted && status != SuggestionStatusDismissed {
		return Suggestion{}, fmt.Errorf("invalid suggestion status %q", status)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	store, err := m.load()
	if err != nil {
		return Suggestion{}, err
	}
	for i := range store.Suggestions {
		if store.Suggestions[i].ID != id {
			continue
		}
		store.Suggestions[i].Status = status
		store.Suggestions[i].JobID = jobID
		store.Suggestions[i].UpdatedAtMS = time.Now().UnixMilli()
		if err := m.save(store); err != nil {
			return Suggestion{}, err
		}
		return store.Suggestions[i], nil
	}
	return Suggestion{}, fmt.Errorf("suggestion %s not found", id)
}

// trim evicts entries past the total cap: oldest non-pending first, then the
// oldest pending. Suggestions are appended chronologically, so index order is
// age order. Caller holds the lock.
func (m *SuggestionManager) trim(store *suggestionStoreFile) {
	for len(store.Suggestions) > MaxTotalSuggestions {
		idx := -1
		for i, s := range store.Suggestions {
			if s.Status != SuggestionStatusPending {
				idx = i
				break
			}
		}
		if idx == -1 {
			idx = 0
		}
		store.Suggestions = append(store.Suggestions[:idx], store.Suggestions[idx+1:]...)
	}
}

func generateSuggestionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return "sug_" + hex.EncodeToString(b)
}
