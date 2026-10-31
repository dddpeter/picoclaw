package evolution

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// m-5: with a shared state_dir the drafts file spans workspaces; the /drafts
// review surface must only expose the calling workspace's drafts, and an
// already-accepted draft must not be rejectable (its skill is live).
func TestDraftReviewScopesToWorkspaceAndProtectsAccepted(t *testing.T) {
	shared := t.TempDir()
	store := NewStore(NewPaths("ws-a", shared))
	if err := store.SaveDrafts([]SkillDraft{
		{ID: "d-a", WorkspaceID: "ws-a", Status: DraftStatusCandidate, TargetSkillName: "skill-a"},
		{ID: "d-b", WorkspaceID: "ws-b", Status: DraftStatusCandidate, TargetSkillName: "skill-b"},
		{ID: "d-acc", WorkspaceID: "ws-a", Status: DraftStatusAccepted, TargetSkillName: "skill-live"},
	}); err != nil {
		t.Fatalf("SaveDrafts: %v", err)
	}

	rt, err := NewRuntime(RuntimeOptions{Config: config.EvolutionConfig{StateDir: shared}})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	got := rt.CandidateDraftsForReview("ws-a")
	if len(got) != 1 || got[0].ID != "d-a" {
		t.Fatalf("ws-a candidates = %+v, want only d-a", got)
	}

	if _, ok := rt.DraftByID("ws-b", "d-a"); ok {
		t.Fatal("ws-b must not preview ws-a's draft")
	}
	if _, ok := rt.DraftByID("ws-a", "d-a"); !ok {
		t.Fatal("ws-a must preview its own draft")
	}

	if _, err := rt.DecideDraft(context.Background(), "ws-b", "d-a", true); err == nil {
		t.Fatal("ws-b must not accept ws-a's draft")
	}

	if _, err := rt.DecideDraft(context.Background(), "ws-a", "d-acc", false); err == nil {
		t.Fatal("rejecting an accepted draft must fail — its skill is already live")
	}

	// Rejecting a candidate still works (workspace-scoped).
	rejected, err := rt.DecideDraft(context.Background(), "ws-a", "d-a", false)
	if err != nil || rejected.Status != DraftStatusRejected {
		t.Fatalf("reject candidate: status=%v err=%v", rejected.Status, err)
	}
}
