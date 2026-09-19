package seahorse

import (
	"strings"
	"testing"
	"time"
)

// TestGenerateSummaryIDUniqueUnderRapidCalls locks in collision resistance:
// the ID must carry entropy beyond the wall clock, because Windows has
// ~0.5ms clock granularity and rapid successive creations previously collided
// (UNIQUE constraint failed: summaries.summary_id).
func TestGenerateSummaryIDUniqueUnderRapidCalls(t *testing.T) {
	now := time.Now()
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		id := generateSummaryID("content", now)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate summary ID %q at iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

func TestGenerateSummaryIDStableFormat(t *testing.T) {
	id := generateSummaryID("content", time.Unix(1700000000, 123456789))
	if !strings.HasPrefix(id, "sum_") {
		t.Fatalf("ID %q must keep the sum_ prefix", id)
	}
	if len(id) <= len("sum_") {
		t.Fatalf("ID %q must carry payload beyond the prefix", id)
	}
	if strings.ContainsAny(id, " /:+") {
		t.Fatalf("ID %q must be filesystem/URL friendly", id)
	}
}

func TestGenerateSummaryIDDistinctForDifferentTimes(t *testing.T) {
	a := generateSummaryID("a", time.Unix(1, 0))
	b := generateSummaryID("a", time.Unix(2, 0))
	if a == b {
		t.Fatal("distinct timestamps should yield distinct IDs")
	}
}