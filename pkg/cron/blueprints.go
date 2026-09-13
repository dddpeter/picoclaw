package cron

import (
	"fmt"
	"strconv"
	"strings"
)

// Automation blueprints — one preset per common automation (daily report,
// hourly check, weekly summary...). The recurrence is fixed by the blueprint;
// users (or the model, via the cron tool) only fill human slots like time and
// weekdays, so nobody ever types a raw cron expression — which also makes the
// "残缺表达式静默永不匹配" failure mode unreachable for blueprint jobs.
// Slot schema borrowed from hermes-agent's blueprint catalog (docs/design/
// hermes-borrowing-analysis.zh.md); scoped to what picoclaw can deliver.

// Slot types the fill validator understands.
const (
	SlotTypeTime     = "time"     // "HH:MM" 24h
	SlotTypeWeekdays = "weekdays" // preset name or cron day-of-week list
	SlotTypeText     = "text"     // free-form, non-empty
)

type BlueprintSlot struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"` // preset choices for weekdays/text enums
	Description string   `json:"description,omitempty"`
}

type Blueprint struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	ScheduleTemplate string          `json:"schedule_template"` // "M H * * *" with {H}/{M} placeholders, or "every:{seconds}"
	ScheduleKind     string          `json:"schedule_kind"`     // "cron" | "every"
	MessageTemplate  string          `json:"message_template"`  // {slot} placeholders
	Slots            []BlueprintSlot `json:"slots"`
}

// WeekdayPresets maps friendly names to cron day-of-week fields.
var WeekdayPresets = map[string]string{
	"everyday": "*",
	"daily":    "*",
	"weekdays": "1-5",
	"workdays": "1-5",
	"weekends": "0,6",
}

var blueprintCatalog = []Blueprint{
	{
		Name:             "daily_report",
		Description:      "Run a check or report once a day at a fixed time and deliver the result.",
		ScheduleKind:     "cron",
		ScheduleTemplate: "{M} {H} * * *",
		MessageTemplate:  "Every day at {time}: {text}",
		Slots: []BlueprintSlot{
			{Name: "time", Label: "Time of day", Type: SlotTypeTime, Required: true, Description: "24h HH:MM"},
			{Name: "text", Label: "What to do", Type: SlotTypeText, Required: true, Description: "the task or report to produce"},
		},
	},
	{
		Name:             "weekday_report",
		Description:      "Like daily_report but only Monday to Friday.",
		ScheduleKind:     "cron",
		ScheduleTemplate: "{M} {H} * * 1-5",
		MessageTemplate:  "Every weekday at {time}: {text}",
		Slots: []BlueprintSlot{
			{Name: "time", Label: "Time of day", Type: SlotTypeTime, Required: true, Description: "24h HH:MM"},
			{Name: "text", Label: "What to do", Type: SlotTypeText, Required: true},
		},
	},
	{
		Name:             "weekly_summary",
		Description:      "Weekly summary or audit on a chosen day of the week.",
		ScheduleKind:     "cron",
		ScheduleTemplate: "{M} {H} * * {W}",
		MessageTemplate:  "Every {weekdays} at {time}: {text}",
		Slots: []BlueprintSlot{
			{Name: "time", Label: "Time of day", Type: SlotTypeTime, Required: true},
			{Name: "weekdays", Label: "Day of week", Type: SlotTypeWeekdays, Required: false, Default: "monday", Options: []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "weekdays", "weekends", "everyday"}},
			{Name: "text", Label: "What to do", Type: SlotTypeText, Required: true},
		},
	},
	{
		Name:             "interval_check",
		Description:      "Repeat a check every N hours around the clock.",
		ScheduleKind:     "every",
		ScheduleTemplate: "every:{hours}",
		MessageTemplate:  "Every {hours} hours: {text}",
		Slots: []BlueprintSlot{
			{Name: "hours", Label: "Interval hours", Type: SlotTypeText, Required: true, Description: "positive integer"},
			{Name: "text", Label: "What to check", Type: SlotTypeText, Required: true},
		},
	},
	{
		Name:             "heartbeat_patrol",
		Description:      "Cheap wake-gate patrol: run a script that decides whether the agent needs to wake at all.",
		ScheduleKind:     "cron",
		ScheduleTemplate: "{M} {H} * * *",
		MessageTemplate:  "Patrol check at {time}. Review the pre-run script output; if nothing needs attention, reply with only HEARTBEAT_OK. Task: {text}",
		Slots: []BlueprintSlot{
			{Name: "time", Label: "Time of day", Type: SlotTypeTime, Required: true},
			{Name: "text", Label: "What to patrol", Type: SlotTypeText, Required: true},
		},
	},
}

// BlueprintCatalog returns the built-in blueprint list.
func BlueprintCatalog() []Blueprint {
	out := make([]Blueprint, len(blueprintCatalog))
	copy(out, blueprintCatalog)
	return out
}

// GetBlueprint looks a blueprint up by name (case-insensitive).
func GetBlueprint(name string) (Blueprint, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, bp := range blueprintCatalog {
		if bp.Name == name {
			return bp, true
		}
	}
	return Blueprint{}, false
}

// FillBlueprint validates slot values and materializes a ready-to-run
// schedule + prompt message. Unknown slots and missing required slots are
// rejected with actionable error messages.
func FillBlueprint(bp Blueprint, values map[string]string) (CronSchedule, string, error) {
	filled := make(map[string]string, len(values))
	normalized := make(map[string]string, len(values))

	for _, slot := range bp.Slots {
		raw := strings.TrimSpace(values[slot.Name])
		if raw == "" {
			raw = slot.Default
		}
		if raw == "" {
			if slot.Required {
				return CronSchedule{}, "", fmt.Errorf("blueprint %q requires slot %q (%s)", bp.Name, slot.Name, slot.Label)
			}
			continue
		}
		switch slot.Type {
		case SlotTypeTime:
			h, m, err := parseSlotTime(raw)
			if err != nil {
				return CronSchedule{}, "", fmt.Errorf("slot %q: %w", slot.Name, err)
			}
			normalized["H"], normalized["M"] = strconv.Itoa(h), strconv.Itoa(m)
			filled[slot.Name] = fmt.Sprintf("%02d:%02d", h, m)
		case SlotTypeWeekdays:
			field, err := parseSlotWeekday(raw)
			if err != nil {
				return CronSchedule{}, "", fmt.Errorf("slot %q: %w", slot.Name, err)
			}
			normalized["W"] = field
			filled[slot.Name] = strings.ToLower(raw)
		default: // SlotTypeText
			filled[slot.Name] = raw
		}
	}

	for name := range values {
		known := false
		for _, slot := range bp.Slots {
			if slot.Name == name {
				known = true
				break
			}
		}
		if !known {
			return CronSchedule{}, "", fmt.Errorf("unknown slot %q for blueprint %q (valid: %s)",
				name, bp.Name, slotNames(bp.Slots))
		}
	}

	expand := func(tpl string) string {
		out := tpl
		for k, v := range normalized {
			out = strings.ReplaceAll(out, "{"+k+"}", v)
		}
		for k, v := range filled {
			out = strings.ReplaceAll(out, "{"+k+"}", v)
		}
		return out
	}

	var schedule CronSchedule
	switch bp.ScheduleKind {
	case "every":
		raw := strings.TrimSpace(values["hours"])
		if raw == "" {
			raw = "24"
		}
		hours, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || hours <= 0 {
			return CronSchedule{}, "", fmt.Errorf("slot %q: hours must be a positive integer, got %q", "hours", raw)
		}
		everyMS := hours * 3600 * 1000
		schedule = CronSchedule{Kind: "every", EveryMS: &everyMS}
	case "cron":
		expr := expand(bp.ScheduleTemplate)
		schedule = CronSchedule{Kind: "cron", Expr: expr}
	default:
		return CronSchedule{}, "", fmt.Errorf("blueprint %q has unsupported schedule kind %q", bp.Name, bp.ScheduleKind)
	}

	if err := ValidateSchedule(schedule); err != nil {
		// Should be unreachable for a well-formed catalog entry; guards
		// against future template edits producing broken expressions.
		return CronSchedule{}, "", fmt.Errorf("blueprint %q produced invalid schedule: %w", bp.Name, err)
	}

	return schedule, expand(bp.MessageTemplate), nil
}

// parseSlotTime accepts "HH:MM" (24h) and returns (hour, minute).
func parseSlotTime(raw string) (int, int, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time must be HH:MM (24h), got %q", raw)
	}
	h, errH := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, errM := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time must be HH:MM (24h), got %q", raw)
	}
	return h, m, nil
}

// parseSlotWeekday accepts preset names ("monday", "weekdays", ...) or a raw
// cron day-of-week list ("1-5", "0,6", "*").
func parseSlotWeekday(raw string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if field, ok := WeekdayPresets[lower]; ok {
		return field, nil
	}
	if strings.EqualFold(raw, "monday") {
		return "1", nil
	}
	// Raw cron day-of-week: digits, commas, ranges, *, / — validated by use.
	for _, r := range raw {
		if !(r >= '0' && r <= '9') && r != ',' && r != '-' && r != '*' && r != '/' && r != ' ' {
			return "", fmt.Errorf("unknown weekday %q (use a name like monday/weekdays/everyday, or a cron day list like 1-5)", raw)
		}
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("weekday cannot be empty")
	}
	return strings.TrimSpace(raw), nil
}

func slotNames(slots []BlueprintSlot) string {
	names := make([]string, 0, len(slots))
	for _, s := range slots {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
