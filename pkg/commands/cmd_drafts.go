package commands

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// draftsCommand closes the evolution draft-review loop (2026-10-31 review C3):
// draft mode kept producing candidate skill drafts with no user-visible entry
// point to list, preview, accept, or reject them.
func draftsCommand() Definition {
	return Definition{
		Name:        "drafts",
		Description: "Review evolution skill drafts: list candidates, preview one, accept or reject",
		SubCommands: []SubCommand{
			{
				Name:        "list",
				Description: "List candidate skill drafts awaiting review",
				Handler:     draftsListHandler,
			},
			{
				Name:        "preview",
				Description: "Show one draft's summary and body",
				ArgsUsage:   "<draft-id>",
				Handler:     draftsPreviewHandler,
			},
			{
				Name:        "accept",
				Description: "Apply a candidate draft to the skill library",
				ArgsUsage:   "<draft-id>",
				Handler:     draftsAcceptHandler,
			},
			{
				Name:        "reject",
				Description: "Reject a candidate draft (stops surfacing for review)",
				ArgsUsage:   "<draft-id>",
				Handler:     draftsRejectHandler,
			},
		},
	}
}

func draftsArg(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func draftsListHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.SkillDrafts == nil {
		return req.Reply(unavailableMsg)
	}
	drafts := rt.SkillDrafts()
	if len(drafts) == 0 {
		return req.Reply("No candidate skill drafts awaiting review.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Candidate skill drafts (%d):\n", len(drafts))
	for _, draft := range drafts {
		kind := string(draft.DraftType) + "/" + string(draft.ChangeKind)
		fmt.Fprintf(&b, "• %s — %s\n", draft.ID, draft.HumanSummary)
		fmt.Fprintf(&b, "  target: %s · %s\n", draft.TargetSkillName, kind)
	}
	b.WriteString("\nUse /drafts preview <id>, then /drafts accept <id> or /drafts reject <id>.")
	return req.Reply(b.String())
}

func draftsPreviewHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.DraftByID == nil {
		return req.Reply(unavailableMsg)
	}
	id := draftsArg(req.Text)
	if id == "" {
		return req.Reply("Usage: /drafts preview <draft-id> (ids from /drafts list)")
	}
	draft, ok := rt.DraftByID(id)
	if !ok {
		return req.Reply(fmt.Sprintf("Draft %s not found.", id))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Draft %s (%s/%s, status %s)\n", draft.ID, draft.DraftType, draft.ChangeKind, draft.Status)
	fmt.Fprintf(&b, "Target skill: %s\n", draft.TargetSkillName)
	if len(draft.MatchedSkillRefs) > 0 {
		fmt.Fprintf(&b, "Matched skills: %s\n", strings.Join(draft.MatchedSkillRefs, ", "))
	}
	if len(draft.IntendedUseCases) > 0 {
		fmt.Fprintf(&b, "Use cases: %s\n", strings.Join(draft.IntendedUseCases, "; "))
	}
	fmt.Fprintf(&b, "\n%s\n", draft.HumanSummary)
	body := draft.BodyOrPatch
	const maxBody = 2000
	if runes := utf8.RuneCountInString(body); runes > maxBody {
		body = string([]rune(body)[:maxBody]) + "\n… (truncated — accept writes the full content)"
	}
	fmt.Fprintf(&b, "\n---\n%s\n---\n", body)
	b.WriteString("\n/drafts accept <id> applies it; /drafts reject <id> discards it.")
	return req.Reply(b.String())
}

func draftsAcceptHandler(ctx context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.DecideDraft == nil {
		return req.Reply(unavailableMsg)
	}
	id := draftsArg(req.Text)
	if id == "" {
		return req.Reply("Usage: /drafts accept <draft-id>")
	}
	draft, err := rt.DecideDraft(ctx, id, true)
	if err != nil {
		return req.Reply("Failed to apply draft: " + err.Error())
	}
	return req.Reply(fmt.Sprintf(
		"Draft %s applied (status %s). Skill %q is now part of the library.",
		draft.ID, draft.Status, draft.TargetSkillName,
	))
}

func draftsRejectHandler(ctx context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.DecideDraft == nil {
		return req.Reply(unavailableMsg)
	}
	id := draftsArg(req.Text)
	if id == "" {
		return req.Reply("Usage: /drafts reject <draft-id>")
	}
	draft, err := rt.DecideDraft(ctx, id, false)
	if err != nil {
		return req.Reply("Failed to reject draft: " + err.Error())
	}
	return req.Reply(fmt.Sprintf("Draft %s rejected (status %s).", draft.ID, draft.Status))
}
