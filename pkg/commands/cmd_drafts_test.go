package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/evolution"
)

func draftsTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := &Runtime{
		SkillDrafts: func() []evolution.SkillDraft {
			return []evolution.SkillDraft{{
				ID:              "draft-1",
				TargetSkillName: "deploy-dance",
				DraftType:       evolution.DraftTypeWorkflow,
				ChangeKind:      evolution.ChangeKindCreate,
				HumanSummary:    "Repeat deploy dance",
				BodyOrPatch:     "# Deploy dance\n\nStep one",
				Status:          evolution.DraftStatusCandidate,
			}}
		},
		DraftByID: func(id string) (evolution.SkillDraft, bool) {
			if id == "draft-1" {
				return evolution.SkillDraft{
					ID:              id,
					TargetSkillName: "deploy-dance",
					DraftType:       evolution.DraftTypeWorkflow,
					ChangeKind:      evolution.ChangeKindCreate,
					HumanSummary:    "Repeat deploy dance",
					BodyOrPatch:     strings.Repeat("x", 3000),
					Status:          evolution.DraftStatusCandidate,
				}, true
			}
			return evolution.SkillDraft{}, false
		},
		DecideDraft: func(ctx context.Context, id string, accept bool) (evolution.SkillDraft, error) {
			status := evolution.DraftStatusRejected
			if accept {
				status = evolution.DraftStatusAccepted
			}
			return evolution.SkillDraft{ID: id, TargetSkillName: "deploy-dance", Status: status}, nil
		},
	}
	return rt
}

func TestDraftsCommand_SubCommands(t *testing.T) {
	def := draftsCommand()
	if def.Name != "drafts" || len(def.SubCommands) != 4 {
		t.Fatalf("unexpected definition: %+v", def)
	}
	want := map[string]bool{"list": false, "preview": false, "accept": false, "reject": false}
	for _, sc := range def.SubCommands {
		want[sc.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing subcommand %q", name)
		}
	}
}

func TestDraftsListHandler(t *testing.T) {
	rt := draftsTestRuntime(t)
	var replies []string
	req := Request{Channel: "cli", ChatID: "c1", Text: "/drafts list", Reply: func(s string) error {
		replies = append(replies, s)
		return nil
	}}
	if err := draftsListHandler(context.Background(), req, rt); err != nil {
		t.Fatalf("list handler: %v", err)
	}
	out := strings.Join(replies, "")
	if !strings.Contains(out, "draft-1") || !strings.Contains(out, "deploy-dance") {
		t.Fatalf("list output missing draft info: %q", out)
	}
}

func TestDraftsPreviewHandlerTruncatesBody(t *testing.T) {
	rt := draftsTestRuntime(t)
	var captured string
	req := Request{Channel: "cli", ChatID: "c1", Text: "/drafts preview draft-1", Reply: func(s string) error {
		captured = s
		return nil
	}}
	if err := draftsPreviewHandler(context.Background(), req, rt); err != nil {
		t.Fatalf("preview handler: %v", err)
	}
	if !strings.Contains(captured, "(truncated") {
		t.Fatalf("oversized body not truncated: %q", captured[:200])
	}
}

func TestDraftsAcceptRejectHandlers(t *testing.T) {
	rt := draftsTestRuntime(t)
	for _, tc := range []struct {
		text string
		want string
	}{{"/drafts accept draft-1", "applied"}, {"/drafts reject draft-1", "rejected"}} {
		var collected []string
		req := Request{Channel: "cli", ChatID: "c1", Text: tc.text, Reply: func(s string) error {
			collected = append(collected, s)
			return nil
		}}
		var err error
		if strings.Contains(tc.text, "accept") {
			err = draftsAcceptHandler(context.Background(), req, rt)
		} else {
			err = draftsRejectHandler(context.Background(), req, rt)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.text, err)
		}
		if !strings.Contains(strings.Join(collected, ""), tc.want) {
			t.Fatalf("%s reply missing %q: %v", tc.text, tc.want, collected)
		}
	}
}

func TestDraftsHandlers_UnavailableWithoutRuntime(t *testing.T) {
	for _, h := range []Handler{draftsListHandler, draftsPreviewHandler, draftsAcceptHandler, draftsRejectHandler} {
		var got string
		req := Request{Channel: "cli", ChatID: "c1", Text: "/drafts", Reply: func(s string) error {
			got = s
			return nil
		}}
		if err := h(context.Background(), req, nil); err != nil {
			t.Fatalf("handler: %v", err)
		}
		if got != unavailableMsg {
			t.Fatalf("expected unavailableMsg, got %q", got)
		}
	}
}
