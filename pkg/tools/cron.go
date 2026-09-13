package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// JobExecutor is the interface for executing cron jobs through the agent
type JobExecutor interface {
	ProcessDirectWithChannel(ctx context.Context, content, sessionKey, channel, chatID string) (string, error)
	// PublishResponseIfNeeded sends response to the outbound bus only when the
	// agent did not already deliver content through the message tool in this round.
	PublishResponseIfNeeded(ctx context.Context, channel, chatID, sessionKey, response string)
}

// CronTool provides scheduling capabilities for the agent
type CronTool struct {
	cronService           *cron.CronService
	suggestions           *cron.SuggestionManager
	executor              JobExecutor
	msgBus                *bus.MessageBus
	execTool              *ExecTool
	allowCommand          bool
	execEnabled           bool
	commandAllowedRemotes []string
}

// NewCronTool creates a new CronTool
// execTimeout: 0 means no timeout, >0 sets the timeout duration
func NewCronTool(
	cronService *cron.CronService, executor JobExecutor, msgBus *bus.MessageBus, workspace string, restrict bool,
	execTimeout time.Duration, config *config.Config,
) (*CronTool, error) {
	allowCommand := true
	execEnabled := true
	var commandAllowedRemotes []string
	if config != nil {
		allowCommand = config.Tools.Cron.AllowCommand
		execEnabled = config.Tools.Exec.Enabled
		commandAllowedRemotes = config.Tools.Cron.CommandAllowedRemotes
	}

	var execTool *ExecTool
	if execEnabled {
		var err error
		execTool, err = NewExecToolWithConfig(workspace, restrict, config)
		if err != nil {
			return nil, fmt.Errorf("unable to configure exec tool: %w", err)
		}
	}

	if execTool != nil {
		execTool.SetTimeout(execTimeout)
	}
	return &CronTool{
		cronService:           cronService,
		suggestions:           cron.NewSuggestionManager(cronService.StorePath()),
		executor:              executor,
		msgBus:                msgBus,
		execTool:              execTool,
		allowCommand:          allowCommand,
		execEnabled:           execEnabled,
		commandAllowedRemotes: commandAllowedRemotes,
	}, nil
}

// Service exposes the underlying cron service (used by the /cron command wiring).
func (t *CronTool) Service() *cron.CronService {
	if t == nil {
		return nil
	}
	return t.cronService
}

// Suggestions exposes the automation-suggestion store (used by /cron wiring).
func (t *CronTool) Suggestions() *cron.SuggestionManager {
	if t == nil {
		return nil
	}
	return t.suggestions
}

// Name returns the tool name
func (t *CronTool) Name() string {
	return "cron"
}

// Description returns the tool description
func (t *CronTool) Description() string {
	return `Schedule, inspect, and update reminders, tasks, or system commands. 
IMPORTANT: When user asks to be reminded or scheduled, you MUST call this tool. 
Use 'at_seconds' for one-time reminders (e.g., 'remind me in 10 minutes' → at_seconds=600). 
Use 'every_seconds' ONLY for recurring tasks (e.g., 'every 2 hours' → every_seconds=7200). 
Use 'cron_expr' for complex recurring schedules (must be a valid 5-field expression). 
Use 'blueprint' to create preset automations without writing cron expressions. 
Use 'command' or 'script' to execute shell commands (script's output {"wakeAgent": false} skips the run). 
Use 'suggestions' to review proposed automations and accept_suggestion/dismiss_suggestion to decide them.`
}

// Parameters returns the tool parameters schema
//
//nolint:dupl // Tool parameter schemas intentionally use similar JSON-schema map literals.
func (t *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"add", "list", "get", "update", "remove", "enable", "disable", "blueprints", "suggestions", "accept_suggestion", "dismiss_suggestion"},
				"description": "Action to perform. Use 'get' before editing and 'update' to change existing jobs without losing their payload. Remote channels can only list/get/update jobs for the current channel/chat_id. 'blueprints' lists preset automations; 'suggestions' lists proposed automations waiting for a decision.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Optional job display name for update or add.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "The reminder/task message to display when triggered. If 'command' is used, this describes what the command does.",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Optional: Shell command to execute directly (e.g., 'df -h'). If set, the agent will run this command and report output instead of just showing the message. For update, omit to preserve the command or pass an empty string to clear it.",
			},
			"command_confirm": map[string]any{
				"type":        "boolean",
				"description": "Optional explicit confirmation flag for scheduling a shell command (command or script). Command execution must also be enabled via tools.cron.allow_command.",
			},
			"script": map[string]any{
				"type":        "string",
				"description": "Optional: pre-run shell script executed BEFORE the agent turn (data collection / condition check). Its stdout is injected into the prompt as context. If the LAST non-empty stdout line is JSON {\"wakeAgent\": false}, the run is skipped entirely: no agent turn, no command execution, no delivery. Requires command execution to be allowed.",
			},
			"at_seconds": map[string]any{
				"type":        "integer",
				"description": "One-time reminder: seconds from now when to trigger (e.g., 600 for 10 minutes later). Use this for one-time reminders like 'remind me in 10 minutes'.",
			},
			"every_seconds": map[string]any{
				"type":        "integer",
				"description": "Recurring interval in seconds (e.g., 3600 for every hour). Use this ONLY for recurring tasks like 'every 2 hours' or 'daily reminder'.",
			},
			"cron_expr": map[string]any{
				"type":        "string",
				"description": "Cron expression for complex recurring schedules (e.g., '0 9 * * *' for daily at 9am). Use this for complex recurring schedules. Must be a valid 5-field expression.",
			},
			"blueprint": map[string]any{
				"type":        "string",
				"description": "Optional: name of a built-in automation blueprint (see action=blueprints). Fills the schedule from the blueprint so no cron expression is needed.",
			},
			"blueprint_values": map[string]any{
				"type":        "object",
				"description": "Slot values for the chosen blueprint (e.g. {\"time\": \"09:30\", \"text\": \"summarize yesterday's messages\"}). Required slots are listed in the blueprint catalog.",
			},
			"suggestion_id": map[string]any{
				"type":        "string",
				"description": "Suggestion ID (for accept_suggestion/dismiss_suggestion).",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID (for get/update/remove/enable/disable)",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the tool with the given arguments
func (t *CronTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, ok := args["action"].(string)
	if !ok {
		return ErrorResult("action is required")
	}

	switch action {
	case "add":
		return t.addJob(ctx, args)
	case "list":
		return t.listJobs(ctx)
	case "get":
		return t.getJob(ctx, args)
	case "update":
		return t.updateJob(ctx, args)
	case "remove":
		return t.removeJob(ctx, args)
	case "enable":
		return t.enableJob(ctx, args, true)
	case "disable":
		return t.enableJob(ctx, args, false)
	case "blueprints":
		return t.listBlueprints()
	case "suggestions":
		return t.listSuggestions()
	case "accept_suggestion":
		return t.decideSuggestion(ctx, args, true)
	case "dismiss_suggestion":
		return t.decideSuggestion(ctx, args, false)
	default:
		return ErrorResult(fmt.Sprintf("unknown action: %s", action))
	}
}

func (t *CronTool) addJob(ctx context.Context, args map[string]any) *ToolResult {
	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)

	if channel == "" || chatID == "" {
		return ErrorResult("no session context (channel/chat_id not set). Use this tool in an active conversation.")
	}

	message, _ := args["message"].(string)

	var schedule cron.CronSchedule

	// Check for blueprint, at_seconds (one-time), every_seconds (recurring), or cron_expr
	atSeconds, hasAt := args["at_seconds"].(float64)
	everySeconds, hasEvery := args["every_seconds"].(float64)
	cronExpr, hasCron := args["cron_expr"].(string)
	blueprintName, hasBlueprint := args["blueprint"].(string)
	hasBlueprint = hasBlueprint && strings.TrimSpace(blueprintName) != ""

	// Fix: type assertions return true for zero values, need additional validity checks
	// This prevents LLMs that fill unused optional parameters with defaults (0) from triggering wrong type
	hasAt = hasAt && atSeconds > 0
	hasEvery = hasEvery && everySeconds > 0
	hasCron = hasCron && cronExpr != ""

	// Priority: blueprint > at_seconds > every_seconds > cron_expr
	var blueprintMessage string
	if hasBlueprint {
		bp, ok := cron.GetBlueprint(blueprintName)
		if !ok {
			return ErrorResult(fmt.Sprintf("unknown blueprint %q; use action=blueprints to list the catalog", blueprintName))
		}
		values := map[string]string{}
		if raw, ok := args["blueprint_values"].(map[string]any); ok {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					values[k] = s
				}
			}
		}
		sched, msg, err := cron.FillBlueprint(bp, values)
		if err != nil {
			return ErrorResult(fmt.Sprintf("blueprint %q: %v", blueprintName, err))
		}
		schedule = sched
		blueprintMessage = msg
	} else if hasAt {
		atMS := time.Now().UnixMilli() + int64(atSeconds)*1000
		schedule = cron.CronSchedule{
			Kind: "at",
			AtMS: &atMS,
		}
	} else if hasEvery {
		everyMS := int64(everySeconds) * 1000
		schedule = cron.CronSchedule{
			Kind:    "every",
			EveryMS: &everyMS,
		}
	} else if hasCron {
		schedule = cron.CronSchedule{
			Kind: "cron",
			Expr: cronExpr,
		}
	} else {
		return ErrorResult("one of blueprint, at_seconds, every_seconds, or cron_expr is required")
	}

	// Blueprint jobs get their message from the template unless an explicit
	// message overrides it. Non-blueprint jobs still require one.
	if hasBlueprint && strings.TrimSpace(message) == "" {
		message = blueprintMessage
	}
	if strings.TrimSpace(message) == "" {
		return ErrorResult("message is required for add")
	}

	// GHSA-pv8c-p6jf-3fpp: command scheduling requires internal channel. When
	// allow_command is disabled, explicit confirmation is required as an override.
	// Non-command reminders remain open to all channels.
	command, _ := args["command"].(string)
	commandConfirm, _ := args["command_confirm"].(bool)
	script, _ := args["script"].(string)
	if command != "" || script != "" {
		if !t.execEnabled {
			return ErrorResult("command execution is disabled")
		}
		if !constants.IsInternalChannel(channel) && !isCommandAllowedRemote(channel, chatID, t.commandAllowedRemotes) {
			return ErrorResult(
				"scheduling command execution is restricted to internal channels or configured remote channels",
			)
		}
		if !t.allowCommand && !commandConfirm {
			return ErrorResult("command_confirm=true is required when allow_command is disabled")
		}
	}

	// Truncate message for job name (max 30 chars)
	messagePreview := utils.Truncate(message, 30)

	job, err := t.cronService.AddJob(
		messagePreview,
		schedule,
		message,
		channel,
		chatID,
	)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Error adding job: %v", err))
	}

	// Apply optional payload fields and persist in a single UpdateJob call
	needsUpdate := false
	if command != "" {
		job.Payload.Command = command
		needsUpdate = true
	}
	if script != "" {
		job.Payload.Script = script
		needsUpdate = true
	}
	if needsUpdate {
		t.cronService.UpdateJob(job)
	}

	return SilentResult(fmt.Sprintf("Cron job added: %s (id: %s)", job.Name, job.ID))
}

func (t *CronTool) listJobs(ctx context.Context) *ToolResult {
	jobs := t.cronService.ListJobs(false)

	var accessibleJobs []cron.CronJob
	for _, job := range jobs {
		if t.canAccessJob(ctx, &job) {
			accessibleJobs = append(accessibleJobs, job)
		}
	}
	jobs = accessibleJobs

	if len(jobs) == 0 {
		return SilentResult("No scheduled jobs")
	}

	var result strings.Builder
	result.WriteString("Scheduled jobs:\n")
	for _, j := range jobs {
		var scheduleInfo string
		if j.Schedule.Kind == "every" && j.Schedule.EveryMS != nil {
			scheduleInfo = fmt.Sprintf("every %ds", *j.Schedule.EveryMS/1000)
		} else if j.Schedule.Kind == "cron" {
			scheduleInfo = j.Schedule.Expr
		} else if j.Schedule.Kind == "at" {
			scheduleInfo = "one-time"
		} else {
			scheduleInfo = "unknown"
		}
		result.WriteString(fmt.Sprintf("- %s (id: %s, %s)\n", j.Name, j.ID, scheduleInfo))
	}

	return SilentResult(result.String())
}

func (t *CronTool) getJob(ctx context.Context, args map[string]any) *ToolResult {
	jobID, errResult := requiredCronJobID(args, "get")
	if errResult != nil {
		return errResult
	}

	job, ok := t.cronService.GetJob(jobID)
	if !ok {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}
	if !t.canAccessJob(ctx, job) {
		return ErrorResult(fmt.Sprintf("Job %s is not accessible from this channel", jobID))
	}

	return SilentResult(formatCronJobJSON(job))
}

func (t *CronTool) updateJob(ctx context.Context, args map[string]any) *ToolResult {
	jobID, errResult := requiredCronJobID(args, "update")
	if errResult != nil {
		return errResult
	}

	job, ok := t.cronService.GetJob(jobID)
	if !ok {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}
	if !t.canAccessJob(ctx, job) {
		return ErrorResult(fmt.Sprintf("Job %s is not accessible from this channel", jobID))
	}

	patches := 0

	name, namePresent, nameErr := optionalNonEmptyString(args, "name")
	if nameErr != nil {
		return nameErr
	}
	if namePresent {
		job.Name = name
		patches++
	}

	message, messagePresent, messageErr := optionalNonEmptyString(args, "message")
	if messageErr != nil {
		return messageErr
	}
	if messagePresent {
		job.Payload.Message = message
		patches++
	}

	schedule, hasSchedule, errResult := schedulePatch(args)
	if errResult != nil {
		return errResult
	}
	if hasSchedule {
		job.Schedule = schedule
		job.DeleteAfterRun = schedule.Kind == "at"
		patches++
	}

	command, commandPresent, errResult := optionalString(args, "command")
	if errResult != nil {
		return errResult
	}
	if commandPresent {
		if errResult := t.validateCommandMutation(ctx, args); errResult != nil {
			return errResult
		}
		job.Payload.Command = command
		patches++
	}

	script, scriptPresent, errResult := optionalString(args, "script")
	if errResult != nil {
		return errResult
	}
	if scriptPresent {
		if errResult := t.validateCommandMutation(ctx, args); errResult != nil {
			return errResult
		}
		job.Payload.Script = script
		patches++
	}

	if patches == 0 {
		return ErrorResult("at least one update field is required")
	}

	if err := t.cronService.UpdateJob(job); err != nil {
		return ErrorResult(fmt.Sprintf("Error updating job: %v", err))
	}

	updated, _ := t.cronService.GetJob(jobID)
	return SilentResult(fmt.Sprintf("Cron job updated:\n%s", formatCronJobJSON(updated)))
}

func (t *CronTool) removeJob(ctx context.Context, args map[string]any) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for remove")
	}

	job, ok := t.cronService.GetJob(jobID)
	if !ok {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}
	if !t.canAccessJob(ctx, job) {
		return ErrorResult(fmt.Sprintf("Job %s is not accessible from this channel", jobID))
	}

	if t.cronService.RemoveJob(jobID) {
		return SilentResult(fmt.Sprintf("Cron job removed: %s", jobID))
	}
	return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
}

func requiredCronJobID(args map[string]any, action string) (string, *ToolResult) {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return "", ErrorResult(fmt.Sprintf("job_id is required for %s", action))
	}
	return jobID, nil
}

func optionalNonEmptyString(args map[string]any, key string) (string, bool, *ToolResult) {
	_, present := args[key]
	if !present {
		return "", false, nil
	}
	text, _, errResult := optionalString(args, key)
	if errResult != nil {
		return "", false, errResult
	}
	if strings.TrimSpace(text) == "" {
		return "", false, ErrorResult(fmt.Sprintf("%s cannot be empty", key))
	}
	return text, true, nil
}

func optionalString(args map[string]any, key string) (string, bool, *ToolResult) {
	value, present := args[key]
	if !present {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", false, ErrorResult(fmt.Sprintf("%s must be a string", key))
	}
	return text, true, nil
}

func schedulePatch(args map[string]any) (cron.CronSchedule, bool, *ToolResult) {
	var schedule cron.CronSchedule
	patches := 0

	if _, present := args["at_seconds"]; present {
		seconds, errResult := positiveSeconds(args, "at_seconds")
		if errResult != nil {
			return cron.CronSchedule{}, false, errResult
		}
		atMS := time.Now().UnixMilli() + seconds*1000
		schedule = cron.CronSchedule{Kind: "at", AtMS: &atMS}
		patches++
	}

	if _, present := args["every_seconds"]; present {
		seconds, errResult := positiveSeconds(args, "every_seconds")
		if errResult != nil {
			return cron.CronSchedule{}, false, errResult
		}
		everyMS := seconds * 1000
		schedule = cron.CronSchedule{Kind: "every", EveryMS: &everyMS}
		patches++
	}

	if _, present := args["cron_expr"]; present {
		cronExpr, ok := args["cron_expr"].(string)
		if !ok {
			return cron.CronSchedule{}, false, ErrorResult("cron_expr must be a string")
		}
		if strings.TrimSpace(cronExpr) == "" {
			return cron.CronSchedule{}, false, ErrorResult("cron_expr cannot be empty")
		}
		schedule = cron.CronSchedule{Kind: "cron", Expr: cronExpr}
		patches++
	}

	if patches > 1 {
		return cron.CronSchedule{}, false, ErrorResult("only one of at_seconds, every_seconds, or cron_expr can be set")
	}
	return schedule, patches == 1, nil
}

func positiveSeconds(args map[string]any, key string) (int64, *ToolResult) {
	value := args[key]
	var seconds int64
	switch v := value.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, ErrorResult(fmt.Sprintf("%s must be a positive integer", key))
		}
		seconds = int64(v)
	case int:
		seconds = int64(v)
	case int64:
		seconds = v
	default:
		return 0, ErrorResult(fmt.Sprintf("%s must be a positive integer", key))
	}
	if seconds <= 0 {
		return 0, ErrorResult(fmt.Sprintf("%s must be a positive integer", key))
	}
	return seconds, nil
}

func (t *CronTool) validateCommandMutation(ctx context.Context, args map[string]any) *ToolResult {
	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)
	if !t.execEnabled {
		return ErrorResult("command execution is disabled")
	}
	if !constants.IsInternalChannel(channel) && !isCommandAllowedRemote(channel, chatID, t.commandAllowedRemotes) {
		return ErrorResult(
			"updating command execution is restricted to internal channels or configured remote channels",
		)
	}
	commandConfirm, _ := args["command_confirm"].(bool)
	if !t.allowCommand && !commandConfirm {
		return ErrorResult("command_confirm=true is required when allow_command is disabled")
	}
	return nil
}

func isCommandAllowedRemote(channel, chatID string, allowed []string) bool {
	if channel == "" {
		return false
	}

	target := channel
	if chatID != "" {
		target = channel + ":" + chatID
	}

	for _, entry := range allowed {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" || entry == channel || entry == target {
			return true
		}
	}

	return false
}

func (t *CronTool) canAccessJob(ctx context.Context, job *cron.CronJob) bool {
	channel := ToolChannel(ctx)
	if constants.IsInternalChannel(channel) {
		return true
	}

	chatID := ToolChatID(ctx)
	if channel == "" || chatID == "" {
		return false
	}
	if job.Payload.Channel != channel || job.Payload.To != chatID {
		return false
	}
	if job.Payload.Command != "" {
		return isCommandAllowedRemote(channel, chatID, t.commandAllowedRemotes)
	}
	return true
}

func formatCronJobJSON(job *cron.CronJob) string {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Sprintf("%+v", *job)
	}
	return string(data)
}

func (t *CronTool) enableJob(ctx context.Context, args map[string]any, enable bool) *ToolResult {
	jobID, ok := args["job_id"].(string)
	if !ok || jobID == "" {
		return ErrorResult("job_id is required for enable/disable")
	}

	job, ok := t.cronService.GetJob(jobID)
	if !ok {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}
	if !t.canAccessJob(ctx, job) {
		return ErrorResult(fmt.Sprintf("Job %s is not accessible from this channel", jobID))
	}

	updatedJob := t.cronService.EnableJob(jobID, enable)
	if updatedJob == nil {
		return ErrorResult(fmt.Sprintf("Job %s not found", jobID))
	}

	status := "enabled"
	if !enable {
		status = "disabled"
	}
	return SilentResult(fmt.Sprintf("Cron job '%s' %s", updatedJob.Name, status))
}

// listBlueprints returns the built-in automation catalog.
func (t *CronTool) listBlueprints() *ToolResult {
	catalog := cron.BlueprintCatalog()
	if len(catalog) == 0 {
		return SilentResult("No blueprints available")
	}
	var b strings.Builder
	b.WriteString("Available automation blueprints (fill slots, no cron expression needed):\n")
	for _, bp := range catalog {
		b.WriteString(fmt.Sprintf("- %s: %s\n", bp.Name, bp.Description))
		b.WriteString("  slots: ")
		parts := make([]string, 0, len(bp.Slots))
		for _, slot := range bp.Slots {
			s := fmt.Sprintf("%s (%s)", slot.Name, slot.Type)
			if slot.Required {
				s += " required"
			} else if slot.Default != "" {
				s += fmt.Sprintf(" default=%s", slot.Default)
			}
			parts = append(parts, s)
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n")
	}
	return SilentResult(b.String())
}

// listSuggestions shows pending automation proposals.
func (t *CronTool) listSuggestions() *ToolResult {
	if t.suggestions == nil {
		return ErrorResult("suggestions unavailable")
	}
	pending := t.suggestions.List(cron.SuggestionStatusPending)
	if len(pending) == 0 {
		return SilentResult("No pending automation suggestions")
	}
	var b strings.Builder
	b.WriteString("Pending automation suggestions (accept_suggestion / dismiss_suggestion with the id):\n")
	for _, s := range pending {
		scheduleDesc := s.Schedule.Kind
		switch s.Schedule.Kind {
		case "cron":
			scheduleDesc = s.Schedule.Expr
		case "every":
			if s.Schedule.EveryMS != nil {
				scheduleDesc = fmt.Sprintf("every %ds", *s.Schedule.EveryMS/1000)
			}
		}
		b.WriteString(fmt.Sprintf("- %s (id: %s, %s)\n", s.Name, s.ID, scheduleDesc))
		if s.Rationale != "" {
			b.WriteString(fmt.Sprintf("  why: %s\n", s.Rationale))
		}
	}
	return SilentResult(b.String())
}

// decideSuggestion accepts (creates the real job, bound to the accepting
// channel) or dismisses (latched, never re-offered) a proposal.
func (t *CronTool) decideSuggestion(ctx context.Context, args map[string]any, accept bool) *ToolResult {
	if t.suggestions == nil {
		return ErrorResult("suggestions unavailable")
	}
	suggestionID, _ := args["suggestion_id"].(string)
	if strings.TrimSpace(suggestionID) == "" {
		return ErrorResult("suggestion_id is required")
	}

	if !accept {
		if err := t.DismissSuggestion(suggestionID); err != nil {
			return ErrorResult(fmt.Sprintf("dismiss failed: %v", err))
		}
		return SilentResult(fmt.Sprintf("Suggestion dismissed (will not be offered again)"))
	}

	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)
	jobID, err := t.AcceptSuggestion(channel, chatID, suggestionID)
	if err != nil {
		return ErrorResult(fmt.Sprintf("accept failed: %v", err))
	}
	return SilentResult(fmt.Sprintf("Suggestion accepted: cron job created (id: %s)", jobID))
}

// PendingSuggestions lists proposals waiting for a decision.
func (t *CronTool) PendingSuggestions() []cron.Suggestion {
	if t == nil || t.suggestions == nil {
		return nil
	}
	return t.suggestions.List(cron.SuggestionStatusPending)
}

// AcceptSuggestion materializes a proposal into a real job bound to the
// accepting channel. Consent happens here: only an explicit accept creates
// the job.
func (t *CronTool) AcceptSuggestion(channel, chatID, suggestionID string) (string, error) {
	if t == nil || t.suggestions == nil {
		return "", fmt.Errorf("suggestions unavailable")
	}
	var target *cron.Suggestion
	for _, s := range t.suggestions.List(cron.SuggestionStatusPending) {
		if s.ID == suggestionID {
			target = &s
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("pending suggestion %s not found", suggestionID)
	}

	job, err := t.cronService.AddJob(target.Name, target.Schedule, target.Message, channel, chatID)
	if err != nil {
		return "", err
	}
	if _, err := t.suggestions.UpdateStatus(suggestionID, cron.SuggestionStatusAccepted, job.ID); err != nil {
		return job.ID, fmt.Errorf("job %s created but suggestion state update failed: %w", job.ID, err)
	}
	return job.ID, nil
}

// DismissSuggestion latches a proposal so it is never re-offered.
func (t *CronTool) DismissSuggestion(suggestionID string) error {
	if t == nil || t.suggestions == nil {
		return fmt.Errorf("suggestions unavailable")
	}
	_, err := t.suggestions.UpdateStatus(suggestionID, cron.SuggestionStatusDismissed, "")
	return err
}

// ExecuteJob executes a cron job through the agent
func (t *CronTool) ExecuteJob(ctx context.Context, job *cron.CronJob) string {
	// Get channel/chatID from job payload
	channel := job.Payload.Channel
	chatID := job.Payload.To

	// Default values if not set
	if channel == "" {
		channel = "cli"
	}
	if chatID == "" {
		chatID = "direct"
	}

	// Pre-run script (wake gate): collect data and optionally skip the whole
	// run. The LAST non-empty stdout line being JSON {"wakeAgent": false}
	// skips everything — no command execution, no agent turn, no delivery.
	scriptContext := ""
	if job.Payload.Script != "" {
		output, wake, err := t.runWakeGateScript(ctx, job.Payload.Script)
		if err != nil {
			return fmt.Sprintf("Error: pre-run script failed: %v", err)
		}
		if !wake {
			return "skipped by wake gate"
		}
		scriptContext = output
	}

	// Execute command if present
	if job.Payload.Command != "" {
		if !t.execEnabled || t.execTool == nil {
			output := "Error executing scheduled command: command execution is disabled"
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
				Context: bus.NewOutboundContext(channel, chatID, ""),
				Content: output,
			})
			return "ok"
		}

		args := map[string]any{
			"action":    "run",
			"command":   job.Payload.Command,
			"__channel": channel,
			"__chat_id": chatID,
		}

		result := t.execTool.Execute(ctx, args)
		var output string
		if result.IsError {
			output = fmt.Sprintf("Error executing scheduled command: %s", result.ForLLM)
		} else {
			output = fmt.Sprintf("Scheduled command '%s' executed:\n%s", job.Payload.Command, result.ForLLM)
		}

		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		t.msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
			Context: bus.NewOutboundContext(channel, chatID, ""),
			Content: output,
		})
		return "ok"
	}

	sessionKey := fmt.Sprintf("agent:cron-%s-%s", job.ID, uuid.New().String())

	// Prepend the pre-run script output as a context block so the agent turn
	// starts from fresh data instead of re-collecting it.
	message := job.Payload.Message
	if scriptContext != "" {
		message = fmt.Sprintf("## Pre-run script output\n\n```\n%s\n```\n\n%s", scriptContext, message)
	}

	// Call agent with the job message
	response, err := t.executor.ProcessDirectWithChannel(
		ctx,
		message,
		sessionKey,
		channel,
		chatID,
	)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}

	if response != "" {
		t.executor.PublishResponseIfNeeded(ctx, channel, chatID, sessionKey, response)
	}
	return "ok"
}

// wakeGateOutputLimit caps how much script stdout is carried into the prompt.
const wakeGateOutputLimit = 8 * 1024

// runWakeGateScript executes the pre-run script through the exec tool and
// interprets the wake gate. It returns the (capped) stdout to inject as
// context and whether the agent turn should run at all.
func (t *CronTool) runWakeGateScript(ctx context.Context, script string) (string, bool, error) {
	if !t.execEnabled || t.execTool == nil {
		return "", false, fmt.Errorf("script requires command execution to be enabled (tools.exec.enabled)")
	}

	result := t.execTool.Execute(ctx, map[string]any{
		"action":  "run",
		"command": script,
	})
	if result.IsError {
		return "", false, fmt.Errorf("%s", result.ForLLM)
	}

	output := result.ForLLM
	if len(output) > wakeGateOutputLimit {
		output = output[:wakeGateOutputLimit] + "\n... (truncated)"
	}
	return output, parseWakeGate(result.ForLLM), nil
}

// parseWakeGate inspects the LAST non-empty line of script output. JSON
// {"wakeAgent": false} means skip; anything else (non-JSON, missing flag,
// gate absent, true) means wake the agent normally.
func parseWakeGate(output string) bool {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var gate struct {
			WakeAgent *bool `json:"wakeAgent"`
		}
		if err := json.Unmarshal([]byte(line), &gate); err != nil {
			return true
		}
		if gate.WakeAgent == nil {
			return true
		}
		return *gate.WakeAgent
	}
	return true
}
