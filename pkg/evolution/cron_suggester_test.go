package evolution

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// --- filterSuggestablePatterns ---

func patternWithCount(id string, count int, rate float64) LearningRecord {
	return LearningRecord{
		ID:          id,
		Kind:        RecordKindPattern,
		Summary:     "summarize channel history",
		EventCount:  count,
		SuccessRate: rate,
	}
}

func TestFilterSuggestablePatterns(t *testing.T) {
	blank := patternWithCount("no-summary", 5, 1.0)
	blank.Summary = ""
	patterns := []LearningRecord{
		patternWithCount("low-count", 1, 1.0),
		patternWithCount("low-rate", 5, 0.3),
		blank,
		patternWithCount("good", 3, 0.9),
	}
	got := filterSuggestablePatterns(patterns, 2, 0.7)
	if len(got) != 1 || got[0].ID != "good" {
		t.Fatalf("filter returned %+v, want only [good]", got)
	}
}

// --- LLMCronSuggester with a fake provider and in-memory store ---

type fakeSuggestionProvider struct {
	content string
	err     error
	calls   int
}

func (f *fakeSuggestionProvider) Chat(_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, _ map[string]any) (*providers.LLMResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &providers.LLMResponse{Content: f.content}, nil
}

func (f *fakeSuggestionProvider) GetDefaultModel() string { return "fake-model" }

type memorySuggestionStore struct {
	added  []cron.Suggestion
	latch  map[string]bool
	failOn map[string]bool
}

func newMemorySuggestionStore() *memorySuggestionStore {
	return &memorySuggestionStore{latch: map[string]bool{}, failOn: map[string]bool{}}
}

func (m *memorySuggestionStore) Add(s cron.Suggestion) (cron.Suggestion, bool, error) {
	if m.failOn[s.DedupKey] {
		return cron.Suggestion{}, false, fmt.Errorf("injected failure")
	}
	if m.latch[s.DedupKey] {
		return s, false, nil
	}
	m.latch[s.DedupKey] = true
	s.ID = fmt.Sprintf("sug_%d", len(m.added))
	s.Status = cron.SuggestionStatusPending
	m.added = append(m.added, s)
	return s, true, nil
}

func TestLLMCronSuggester_ProposesForQualifiedPatterns(t *testing.T) {
	provider := &fakeSuggestionProvider{content: `{
		"name": "nightly channel summary",
		"kind": "cron",
		"cron_expr": "0 21 * * *",
		"message": "Summarize today's channel history.",
		"rationale": "daily habit, cheap at night"
	}`}
	store := newMemorySuggestionStore()
	suggester := NewLLMCronSuggester(provider, "fake-model", func(string) SuggestionAdder { return store }, 2, 0.7)

	patterns := []LearningRecord{patternWithCount("p1", 3, 0.9)}
	if err := suggester.SuggestCronJobs(context.Background(), "/ws", patterns); err != nil {
		t.Fatalf("SuggestCronJobs failed: %v", err)
	}
	if len(store.added) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(store.added))
	}
	s := store.added[0]
	if s.Schedule.Kind != "cron" || s.Schedule.Expr != "0 21 * * *" {
		t.Fatalf("unexpected schedule %+v", s.Schedule)
	}
	if s.Source != "evolution" || s.DedupKey != "evolution:pattern:p1" {
		t.Fatalf("unexpected suggestion %+v", s)
	}
}

func TestLLMCronSuggester_SkipsWhenModelSaysSkip(t *testing.T) {
	provider := &fakeSuggestionProvider{content: `{"skip": true}`}
	store := newMemorySuggestionStore()
	suggester := NewLLMCronSuggester(provider, "fake-model", func(string) SuggestionAdder { return store }, 2, 0.7)

	if err := suggester.SuggestCronJobs(context.Background(), "/ws", []LearningRecord{patternWithCount("p1", 5, 1.0)}); err != nil {
		t.Fatalf("SuggestCronJobs failed: %v", err)
	}
	if len(store.added) != 0 {
		t.Fatalf("skip proposal must not create a suggestion")
	}
}

func TestLLMCronSuggester_EverySecondsClampedToHourlyFloor(t *testing.T) {
	provider := &fakeSuggestionProvider{content: `{"name":"x","kind":"every","every_seconds":60,"message":"m"}`}
	store := newMemorySuggestionStore()
	suggester := NewLLMCronSuggester(provider, "fake-model", func(string) SuggestionAdder { return store }, 2, 0.7)

	if err := suggester.SuggestCronJobs(context.Background(), "/ws", []LearningRecord{patternWithCount("p1", 5, 1.0)}); err != nil {
		t.Fatalf("SuggestCronJobs failed: %v", err)
	}
	if len(store.added) != 1 || store.added[0].Schedule.EveryMS == nil || *store.added[0].Schedule.EveryMS < 3600*1000 {
		t.Fatalf("sub-hour proposal must be clamped to >= 1h, got %+v", store.added)
	}
}

func TestLLMCronSuggester_OneFailureDoesNotStarveOthers(t *testing.T) {
	provider := &fakeSuggestionProvider{content: `{"name":"x","kind":"cron","cron_expr":"0 9 * * *","message":"m"}`}
	store := newMemorySuggestionStore()
	store.failOn["evolution:pattern:bad"] = true
	suggester := NewLLMCronSuggester(provider, "fake-model", func(string) SuggestionAdder { return store }, 2, 0.7)

	patterns := []LearningRecord{
		patternWithCount("bad", 5, 1.0),
		patternWithCount("good", 5, 1.0),
	}
	if err := suggester.SuggestCronJobs(context.Background(), "/ws", patterns); err != nil {
		t.Fatalf("SuggestCronJobs failed: %v", err)
	}
	if len(store.added) != 1 || store.added[0].DedupKey != "evolution:pattern:good" {
		t.Fatalf("expected only the good pattern to produce a suggestion, got %+v", store.added)
	}
}

func TestLLMCronSuggester_DisabledWithoutProviderOrStore(t *testing.T) {
	store := newMemorySuggestionStore()
	suggester := NewLLMCronSuggester(nil, "", func(string) SuggestionAdder { return store }, 0, 0)
	if err := suggester.SuggestCronJobs(context.Background(), "/ws", []LearningRecord{patternWithCount("p1", 5, 1.0)}); err != nil {
		t.Fatalf("nil provider must be a no-op, got %v", err)
	}
	if len(store.added) != 0 {
		t.Fatalf("nil provider must not add suggestions")
	}
}

// --- Runtime wiring ---

func TestRuntime_CronSuggesterRunsOnColdPath(t *testing.T) {
	dir := t.TempDir()
	cfg := config.EvolutionConfig{
		Enabled:      true,
		Mode:         "draft",
		MinTaskCount: 2,
	}
	store := newTestStore(t, dir)

	calls := 0
	suggester := fakeCronSuggester{fn: func(_ context.Context, ws string, patterns []LearningRecord) error {
		calls++
		return nil
	}}

	rt, err := NewRuntime(RuntimeOptions{
		Config:        cfg,
		Store:         store,
		CronSuggester: suggester,
	})
	if err != nil {
		t.Fatalf("NewRuntime failed: %v", err)
	}

	// Prime pattern records so the cold path has something to suggest on.
	pattern := patternWithCount("pat1", 5, 1.0)
	pattern.WorkspaceID = dir
	if err := store.MergePatternRecords([]LearningRecord{pattern}); err != nil {
		t.Fatalf("MergePatternRecords failed: %v", err)
	}

	if err := rt.RunColdPathOnce(context.Background(), dir); err != nil {
		t.Fatalf("RunColdPathOnce failed: %v", err)
	}
	if calls == 0 {
		t.Fatalf("cron suggester was not consulted during cold path")
	}
}

func TestRuntime_CronSuggesterSkippedInObserveMode(t *testing.T) {
	dir := t.TempDir()
	cfg := config.EvolutionConfig{Enabled: true, Mode: "observe"}
	store := newTestStore(t, dir)

	calls := 0
	suggester := fakeCronSuggester{fn: func(_ context.Context, ws string, patterns []LearningRecord) error {
		calls++
		return nil
	}}

	rt, err := NewRuntime(RuntimeOptions{Config: cfg, Store: store, CronSuggester: suggester})
	if err != nil {
		t.Fatalf("NewRuntime failed: %v", err)
	}
	if err := rt.RunColdPathOnce(context.Background(), dir); err != nil {
		t.Fatalf("RunColdPathOnce failed: %v", err)
	}
	if calls != 0 {
		t.Fatalf("observe mode must not consult the cron suggester")
	}
}

type fakeCronSuggester struct {
	fn func(ctx context.Context, workspace string, patterns []LearningRecord) error
}

func (f fakeCronSuggester) SuggestCronJobs(ctx context.Context, workspace string, patterns []LearningRecord) error {
	return f.fn(ctx, workspace, patterns)
}

func newTestStore(t *testing.T, workspace string) *Store {
	t.Helper()
	return NewStore(NewPaths(workspace, filepath.Join(workspace, "state", "evolution")))
}
