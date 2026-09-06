package commands

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ActiveTurnStatus describes one in-flight turn for /status output.
type ActiveTurnStatus struct {
	SessionKey string
	AgentID    string
	Channel    string
	Task       string
	Phase      string
	Iteration  int
	RunningFor time.Duration
}

// StatusOverview is a snapshot of agent runtime status, assembled by the
// agent loop so command handlers never depend on agent internals.
type StatusOverview struct {
	Version     string
	Model       string
	Provider    string
	Channels    []string
	Uptime      time.Duration
	ActiveTurns []ActiveTurnStatus
}

func statusCommand() Definition {
	return Definition{
		Name:        "status",
		Description: "Show agent status: model, channels, active tasks and uptime",
		Usage:       "/status",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetStatusOverview == nil {
				return req.Reply(unavailableMsg)
			}
			overview := rt.GetStatusOverview()
			if overview == nil {
				return req.Reply("Status unavailable.")
			}
			var stats *ContextStats
			if rt.GetContextStats != nil {
				stats = rt.GetContextStats()
			}
			return req.Reply(formatStatusOverview(overview, stats))
		},
	}
}

func formatStatusOverview(o *StatusOverview, stats *ContextStats) string {
	var b strings.Builder
	b.WriteString("📊 Status\n")
	if o.Version != "" {
		b.WriteString("Version: " + o.Version + "\n")
	}
	if o.Model != "" {
		provider := o.Provider
		if provider == "" {
			provider = "unknown"
		}
		b.WriteString(fmt.Sprintf("Model: %s (%s)\n", o.Model, provider))
	}
	if len(o.Channels) > 0 {
		b.WriteString("Channels: " + strings.Join(o.Channels, ", ") + "\n")
	}
	if o.Uptime > 0 {
		b.WriteString("Uptime: " + formatStatusDuration(o.Uptime) + "\n")
	}
	if stats != nil && stats.TotalTokens > 0 {
		b.WriteString(fmt.Sprintf("Context: %d%% used (~%s/%s tokens)\n",
			stats.UsedPercent, formatStatusTokens(stats.UsedTokens), formatStatusTokens(stats.TotalTokens)))
	}
	if len(o.ActiveTurns) == 0 {
		b.WriteString("Active tasks: none")
		return b.String()
	}
	b.WriteString(fmt.Sprintf("Active tasks (%d):\n", len(o.ActiveTurns)))
	for _, t := range o.ActiveTurns {
		task := t.Task
		if task == "" {
			task = "(no description)"
		}
		if len(task) > 60 {
			task = task[:60] + "…"
		}
		line := fmt.Sprintf(" • [%s] %s — %s, iteration %d",
			t.Channel, task, formatStatusDuration(t.RunningFor), t.Iteration)
		if t.Phase != "" {
			line += " (" + t.Phase + ")"
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatStatusDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func formatStatusTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
