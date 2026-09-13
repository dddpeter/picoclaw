package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/cron"
)

// cronCommand surfaces scheduling from chat: job listing, automation
// suggestions (accept/dismiss), and the blueprint catalog. Job creation is
// consent-first — accepting a suggestion is the only shortcut to a real job,
// and it always comes from an explicit user action.
func cronCommand() Definition {
	return Definition{
		Name:        "cron",
		Description: "List scheduled jobs, review automation suggestions, and browse blueprints",
		SubCommands: []SubCommand{
			{
				Name:        "list",
				Description: "List scheduled jobs",
				Handler:     cronListHandler,
			},
			{
				Name:        "suggest",
				Description: "Show pending automation suggestions",
				Handler:     cronSuggestHandler,
			},
			{
				Name:        "accept",
				Description: "Accept a suggestion and create the job",
				ArgsUsage:   "<suggestion-id>",
				Handler:     cronAcceptHandler,
			},
			{
				Name:        "dismiss",
				Description: "Dismiss a suggestion (never offered again)",
				ArgsUsage:   "<suggestion-id>",
				Handler:     cronDismissHandler,
			},
			{
				Name:        "blueprint",
				Description: "List automation blueprints",
				Handler:     cronBlueprintHandler,
			},
		},
	}
}

func cronListHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.CronJobs == nil {
		return req.Reply(unavailableMsg)
	}
	jobs := rt.CronJobs()
	if len(jobs) == 0 {
		return req.Reply("No scheduled jobs. Create one by asking, e.g. \"every day at 9am, summarize ...\".")
	}
	var b strings.Builder
	b.WriteString("Scheduled jobs:\n")
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		b.WriteString(fmt.Sprintf("- %s (id: %s, %s)\n", job.Name, job.ID, describeSchedule(job.Schedule)))
	}
	return req.Reply(b.String())
}

func cronSuggestHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.CronSuggestions == nil {
		return req.Reply(unavailableMsg)
	}
	suggestions := rt.CronSuggestions()
	if len(suggestions) == 0 {
		return req.Reply("No pending automation suggestions. When a repeating pattern emerges from your tasks, a proposal will appear here.")
	}
	var b strings.Builder
	b.WriteString("Automation suggestions (accept or dismiss):\n")
	for _, s := range suggestions {
		b.WriteString(fmt.Sprintf("- %s (id: %s, %s)\n", s.Name, s.ID, describeSchedule(s.Schedule)))
		if s.Rationale != "" {
			b.WriteString("  " + s.Rationale + "\n")
		}
	}
	b.WriteString("\n/cron accept <id> to create the job, /cron dismiss <id> to never see it again.")
	return req.Reply(b.String())
}

func cronAcceptHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.AcceptCronSuggestion == nil {
		return req.Reply(unavailableMsg)
	}
	id := cronCommandArg(req.Text)
	if id == "" {
		return req.Reply("Usage: /cron accept <suggestion-id>")
	}
	jobID, err := rt.AcceptCronSuggestion(req.Channel, req.ChatID, id)
	if err != nil {
		return req.Reply("Failed to accept suggestion: " + err.Error())
	}
	return req.Reply("Job created from suggestion (job id: " + jobID + ").")
}

func cronDismissHandler(_ context.Context, req Request, rt *Runtime) error {
	if rt == nil || rt.DismissCronSuggestion == nil {
		return req.Reply(unavailableMsg)
	}
	id := cronCommandArg(req.Text)
	if id == "" {
		return req.Reply("Usage: /cron dismiss <suggestion-id>")
	}
	if err := rt.DismissCronSuggestion(id); err != nil {
		return req.Reply("Failed to dismiss suggestion: " + err.Error())
	}
	return req.Reply("Suggestion dismissed. It will not be offered again.")
}

func cronBlueprintHandler(_ context.Context, req Request, rt *Runtime) error {
	_ = rt // blueprint catalog is static; rt kept for handler symmetry
	var b strings.Builder
	b.WriteString("Automation blueprints (create via the agent, e.g. \"every weekday at 9am, ...\"): \n")
	for _, bp := range cron.BlueprintCatalog() {
		b.WriteString(fmt.Sprintf("- %s: %s\n", bp.Name, bp.Description))
	}
	return req.Reply(b.String())
}

// cronCommandArg extracts the argument after "/cron <sub>".
func cronCommandArg(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func describeSchedule(s cron.CronSchedule) string {
	switch s.Kind {
	case "cron":
		return s.Expr
	case "every":
		if s.EveryMS != nil {
			return fmt.Sprintf("every %ds", *s.EveryMS/1000)
		}
	case "at":
		return "one-time"
	}
	return s.Kind
}
