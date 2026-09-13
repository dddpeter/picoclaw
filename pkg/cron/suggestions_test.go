package cron

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- SuggestionManager ---

func newTestSuggestionManager(t *testing.T) *SuggestionManager {
	t.Helper()
	return NewSuggestionManager(filepath.Join(t.TempDir(), "jobs.json"))
}

func testSuggestion(name, dedup string) Suggestion {
	every := int64(86400000)
	return Suggestion{
		DedupKey: dedup,
		Name:     name,
		Message:  "run " + name,
		Schedule: CronSchedule{Kind: "every", EveryMS: &every},
		Source:   "evolution",
	}
}

func TestSuggestionManager_AddListDedup(t *testing.T) {
	m := newTestSuggestionManager(t)

	added, ok, err := m.Add(testSuggestion("nightly backup", "evolution:pattern:p1"))
	if err != nil || !ok {
		t.Fatalf("Add failed: ok=%v err=%v", ok, err)
	}
	if added.Status != SuggestionStatusPending || added.ID == "" {
		t.Fatalf("unexpected stored suggestion %+v", added)
	}

	// Same dedup key: latched, no duplicate.
	existing, ok, err := m.Add(testSuggestion("nightly backup v2", "evolution:pattern:p1"))
	if err != nil {
		t.Fatalf("dedup Add failed: %v", err)
	}
	if ok {
		t.Fatalf("dedup key must not create a second entry")
	}
	if existing.ID != added.ID {
		t.Fatalf("dedup must return the original suggestion")
	}

	// Dismissed proposals are latched forever.
	if _, err := m.UpdateStatus(added.ID, SuggestionStatusDismissed, ""); err != nil {
		t.Fatalf("dismiss failed: %v", err)
	}
	if _, ok, err := m.Add(testSuggestion("nightly backup v3", "evolution:pattern:p1")); err != nil || ok {
		t.Fatalf("dismissed dedup key must be latched: ok=%v err=%v", ok, err)
	}

	if got := len(m.List(SuggestionStatusPending)); got != 0 {
		t.Fatalf("pending list should be empty after dismissal, got %d", got)
	}
}

func TestSuggestionManager_PendingCap(t *testing.T) {
	m := newTestSuggestionManager(t)
	for i := 0; i < MaxPendingSuggestions; i++ {
		if _, ok, err := m.Add(testSuggestion(fmt.Sprintf("job-%d", i), fmt.Sprintf("k%d", i))); err != nil || !ok {
			t.Fatalf("Add %d failed: ok=%v err=%v", i, ok, err)
		}
	}
	// Cap reached: new proposal dropped.
	_, ok, err := m.Add(testSuggestion("overflow", "k-overflow"))
	if err != nil {
		t.Fatalf("overflow Add failed: %v", err)
	}
	if ok {
		t.Fatalf("overflow proposal must be dropped at cap %d", MaxPendingSuggestions)
	}

	// Dismissing one frees a slot.
	pending := m.List(SuggestionStatusPending)
	if _, err := m.UpdateStatus(pending[0].ID, SuggestionStatusDismissed, ""); err != nil {
		t.Fatalf("dismiss failed: %v", err)
	}
	if _, ok, err := m.Add(testSuggestion("overflow", "k-overflow")); err != nil || !ok {
		t.Fatalf("proposal after dismissal failed: ok=%v err=%v", ok, err)
	}
}

func TestSuggestionManager_AcceptAndPersistence(t *testing.T) {
	m := newTestSuggestionManager(t)
	s, _, err := m.Add(testSuggestion("daily report", "k1"))
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if _, err := m.UpdateStatus(s.ID, SuggestionStatusAccepted, "job-123"); err != nil {
		t.Fatalf("accept failed: %v", err)
	}

	// State survives a reload from disk.
	m2 := NewSuggestionManager(m.path)
	all := m2.List("")
	if len(all) != 1 || all[0].Status != SuggestionStatusAccepted || all[0].JobID != "job-123" {
		t.Fatalf("suggestion state not persisted: %+v", all)
	}
}

func TestSuggestionManager_RejectsInvalid(t *testing.T) {
	m := newTestSuggestionManager(t)

	bad := testSuggestion("x", "kx")
	bad.Source = "aliens"
	if _, _, err := m.Add(bad); err == nil {
		t.Fatalf("unknown source must be rejected")
	}

	bad = testSuggestion("x", "kx")
	bad.Schedule = CronSchedule{Kind: "cron", Expr: "45 16"} // truncated expr
	if _, _, err := m.Add(bad); err == nil {
		t.Fatalf("invalid schedule must be rejected")
	}

	bad = testSuggestion("", "kx")
	if _, _, err := m.Add(bad); err == nil {
		t.Fatalf("empty name must be rejected")
	}
}

// --- Blueprints ---

func TestFillBlueprint_DailyReport(t *testing.T) {
	bp, ok := GetBlueprint("daily_report")
	if !ok {
		t.Fatalf("daily_report blueprint missing from catalog")
	}

	schedule, message, err := FillBlueprint(bp, map[string]string{
		"time": "9:30",
		"text": "summarize yesterday",
	})
	if err != nil {
		t.Fatalf("FillBlueprint failed: %v", err)
	}
	if schedule.Kind != "cron" || schedule.Expr != "30 9 * * *" {
		t.Fatalf("unexpected schedule %+v", schedule)
	}
	if message == "" || !strings.Contains(message, "summarize yesterday") {
		t.Fatalf("unexpected message %q", message)
	}
}

func TestFillBlueprint_WeeklySummaryPreset(t *testing.T) {
	bp, ok := GetBlueprint("weekly_summary")
	if !ok {
		t.Fatalf("weekly_summary blueprint missing")
	}
	schedule, _, err := FillBlueprint(bp, map[string]string{
		"time":     "08:00",
		"weekdays": "weekdays",
		"text":     "weekly audit",
	})
	if err != nil {
		t.Fatalf("FillBlueprint failed: %v", err)
	}
	if schedule.Expr != "0 8 * * 1-5" {
		t.Fatalf("unexpected expr %q", schedule.Expr)
	}
}

func TestFillBlueprint_IntervalCheck(t *testing.T) {
	bp, ok := GetBlueprint("interval_check")
	if !ok {
		t.Fatalf("interval_check blueprint missing")
	}
	schedule, _, err := FillBlueprint(bp, map[string]string{
		"hours": "6",
		"text":  "check disk",
	})
	if err != nil {
		t.Fatalf("FillBlueprint failed: %v", err)
	}
	if schedule.Kind != "every" || schedule.EveryMS == nil || *schedule.EveryMS != 6*3600*1000 {
		t.Fatalf("unexpected schedule %+v", schedule)
	}
}

func TestFillBlueprint_Rejects(t *testing.T) {
	bp, _ := GetBlueprint("daily_report")

	// Missing required slot.
	if _, _, err := FillBlueprint(bp, map[string]string{"time": "09:00"}); err == nil {
		t.Fatalf("missing required slot must be rejected")
	}
	// Malformed time.
	if _, _, err := FillBlueprint(bp, map[string]string{"time": "25:99", "text": "x"}); err == nil {
		t.Fatalf("malformed time must be rejected")
	}
	// Unknown slot.
	if _, _, err := FillBlueprint(bp, map[string]string{"time": "09:00", "text": "x", "nope": "y"}); err == nil {
		t.Fatalf("unknown slot must be rejected")
	}
	// Unknown blueprint name.
	if _, ok := GetBlueprint("nope"); ok {
		t.Fatalf("unknown blueprint must not resolve")
	}
}

func TestBlueprints_AllCatalogEntriesAreValid(t *testing.T) {
	for _, bp := range BlueprintCatalog() {
		if bp.Name == "" || bp.Description == "" {
			t.Fatalf("catalog entry missing name/description: %+v", bp)
		}
		if bp.ScheduleKind != "cron" && bp.ScheduleKind != "every" {
			t.Fatalf("blueprint %s: bad schedule kind %q", bp.Name, bp.ScheduleKind)
		}
		if len(bp.Slots) == 0 {
			t.Fatalf("blueprint %s has no slots", bp.Name)
		}
	}
}

// --- SuggestionsPath ---

func TestSuggestionsPath(t *testing.T) {
	got := SuggestionsPath(filepath.Join("ws", "cron", "jobs.json"))
	if want := filepath.Join("ws", "cron", "suggestions.json"); got != want {
		t.Fatalf("SuggestionsPath = %q, want %q", got, want)
	}
}

// Guard: suggestions must not leak into the jobs store file reads.
func TestSuggestionManager_MissingFileStartsEmpty(t *testing.T) {
	m := NewSuggestionManager(filepath.Join(t.TempDir(), "jobs.json"))
	if got := m.List(""); len(got) != 0 {
		t.Fatalf("expected empty list, got %+v", got)
	}
	if _, err := os.Stat(m.Path()); !os.IsNotExist(err) {
		t.Fatalf("store file must not be created on read, stat err = %v", err)
	}
}
