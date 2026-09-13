package evolution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// CronSuggester turns recurring success patterns into ready-to-run cron job
// proposals. Consent-first: suggestions land in the suggestion store for the
// user to accept or dismiss — nothing here creates a real job.
type CronSuggester interface {
	SuggestCronJobs(ctx context.Context, workspace string, patterns []LearningRecord) error
}

// SuggestionAdder is the store contract the suggester writes proposals to
// (satisfied by *cron.SuggestionManager); an interface keeps the LLM
// suggester testable without touching the filesystem.
type SuggestionAdder interface {
	Add(s cron.Suggestion) (cron.Suggestion, bool, error)
}

type LLMCronSuggester struct {
	provider providers.LLMProvider
	model    string
	// storeFor resolves the per-workspace suggestion store lazily.
	storeFor func(workspace string) SuggestionAdder
	// minEventCount gates which patterns are worth proposing on.
	minEventCount int
	// minSuccessRate is the success ratio a pattern must show.
	minSuccessRate float64
}

// NewLLMCronSuggester creates a suggester. storeFor receives the workspace and
// must return a suggestion store for it (may return nil to disable).
func NewLLMCronSuggester(provider providers.LLMProvider, model string, storeFor func(workspace string) SuggestionAdder, minEventCount int, minSuccessRate float64) *LLMCronSuggester {
	if minEventCount < 1 {
		minEventCount = 2
	}
	if minSuccessRate <= 0 || minSuccessRate > 1 {
		minSuccessRate = 0.7
	}
	return &LLMCronSuggester{
		provider:       provider,
		model:          strings.TrimSpace(model),
		storeFor:       storeFor,
		minEventCount:  minEventCount,
		minSuccessRate: minSuccessRate,
	}
}

type llmSuggestion struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"` // "cron" | "every"
	CronExpr     string `json:"cron_expr,omitempty"`
	EverySeconds int64  `json:"every_seconds,omitempty"`
	Message      string `json:"message"`
	Rationale    string `json:"rationale"`
}

func (s *LLMCronSuggester) SuggestCronJobs(ctx context.Context, workspace string, patterns []LearningRecord) error {
	if s == nil || s.provider == nil || s.storeFor == nil {
		return nil
	}
	store := s.storeFor(workspace)
	if store == nil {
		return nil
	}

	qualified := filterSuggestablePatterns(patterns, s.minEventCount, s.minSuccessRate)
	if len(qualified) == 0 {
		return nil
	}

	for _, pattern := range qualified {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.suggestOne(ctx, store, pattern); err != nil {
			// One bad proposal must not starve the others.
			logger.WarnCF("evolution", "Failed to generate automation suggestion", map[string]any{
				"workspace": workspace,
				"pattern":   summarizePatternRecord(pattern),
				"error":     err.Error(),
			})
		}
	}
	return nil
}

func (s *LLMCronSuggester) suggestOne(ctx context.Context, store SuggestionAdder, pattern LearningRecord) error {
	model := s.model
	if model == "" {
		model = strings.TrimSpace(s.provider.GetDefaultModel())
	}
	if model == "" {
		return fmt.Errorf("no model available for suggestion generation")
	}

	callCtx, cancel := withLLMCallTimeout(ctx, llmSuggestionTimeout)
	defer cancel()
	resp, err := s.provider.Chat(callCtx, []providers.Message{
		{
			Role:    "system",
			Content: "Return exactly one JSON object proposing a scheduled automation, or {\"skip\": true} if none makes sense. Do not use markdown fences.",
		},
		{
			Role:    "user",
			Content: buildSuggestionPrompt(pattern),
		},
	}, nil, model, map[string]any{"temperature": 0.2})
	if err != nil {
		return fmt.Errorf("llm call: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return fmt.Errorf("empty llm response")
	}

	var proposal struct {
		Skip bool `json:"skip"`
		llmSuggestion
	}
	content := extractJSONLoose(resp.Content)
	if err := json.Unmarshal([]byte(content), &proposal); err != nil {
		return fmt.Errorf("parse proposal: %w", err)
	}
	if proposal.Skip {
		return nil
	}

	suggestion, err := suggestionFromProposal(proposal.llmSuggestion, pattern)
	if err != nil {
		return err
	}

	_, added, err := store.Add(suggestion)
	if err != nil {
		return err
	}
	if added {
		logger.InfoCF("evolution", "Proposed cron automation", map[string]any{
			"suggestion": suggestion.Name,
			"schedule":   suggestion.Schedule.Kind,
			"pattern":    summarizePatternRecord(pattern),
		})
	}
	return nil
}

// filterSuggestablePatterns keeps patterns with enough repetition and enough
// success to be worth automating.
func filterSuggestablePatterns(patterns []LearningRecord, minCount int, minRate float64) []LearningRecord {
	out := make([]LearningRecord, 0, len(patterns))
	for _, p := range patterns {
		if p.EventCount < minCount {
			continue
		}
		if p.SuccessRate > 0 && p.SuccessRate < minRate {
			continue
		}
		if strings.TrimSpace(p.Summary) == "" && len(p.WinningPath) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

func suggestionFromProposal(p llmSuggestion, pattern LearningRecord) (cron.Suggestion, error) {
	var schedule cron.CronSchedule
	switch strings.ToLower(strings.TrimSpace(p.Kind)) {
	case "cron":
		schedule = cron.CronSchedule{Kind: "cron", Expr: strings.TrimSpace(p.CronExpr)}
	case "every":
		if p.EverySeconds <= 0 {
			return cron.Suggestion{}, fmt.Errorf("every_seconds must be positive")
		}
		// Sub-hour repetition is almost never what a pattern-based
		// automation means; keep proposals sane.
		if p.EverySeconds < 3600 {
			p.EverySeconds = 3600
		}
		everyMS := p.EverySeconds * 1000
		schedule = cron.CronSchedule{Kind: "every", EveryMS: &everyMS}
	default:
		return cron.Suggestion{}, fmt.Errorf("unknown schedule kind %q", p.Kind)
	}

	message := strings.TrimSpace(p.Message)
	if message == "" {
		return cron.Suggestion{}, fmt.Errorf("proposal message is empty")
	}
	if len(message) > 2000 {
		message = message[:2000]
	}

	return cron.Suggestion{
		DedupKey:  patternDedupKey(pattern),
		Name:      truncateSuggestionName(p.Name, pattern),
		Message:   message,
		Schedule:  schedule,
		Rationale: strings.TrimSpace(p.Rationale),
		Source:    "evolution",
	}, nil
}

// patternDedupKey derives a stable latch key from the pattern so repeated
// cold-path runs never re-offer the same automation after a dismissal.
func patternDedupKey(pattern LearningRecord) string {
	if id := strings.TrimSpace(pattern.ID); id != "" {
		return "evolution:pattern:" + id
	}
	sum := sha256.Sum256([]byte(pattern.Summary))
	return "evolution:summary:" + hex.EncodeToString(sum[:8])
}

func truncateSuggestionName(name string, pattern LearningRecord) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(pattern.Summary)
	}
	if name == "" {
		name = "recurring task"
	}
	runes := []rune(name)
	if len(runes) > 40 {
		name = string(runes[:40])
	}
	return name
}

// extractJSONLoose strips markdown fences the model may add despite instructions.
func extractJSONLoose(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func buildSuggestionPrompt(pattern LearningRecord) string {
	var b strings.Builder
	b.WriteString(`A user keeps successfully repeating the task pattern below. Propose ONE scheduled automation (cron job) that would run this task for them unattended.

Rules:
- The proposal must be genuinely recurring and self-contained: the job prompt must be executable WITHOUT the original conversation context.
- Prefer conservative schedules (daily/weekly). Never schedule more often than every hour.
- If the task cannot run unattended, return {"skip": true} instead.

Return JSON with fields:
{"name": "...", "kind": "cron"|"every", "cron_expr": "M H * * *", "every_seconds": N, "message": "<self-contained job prompt>", "rationale": "<why this schedule>"}

Task pattern:`)
	b.WriteString("\n")
	if s := strings.TrimSpace(pattern.Summary); s != "" {
		b.WriteString("Summary: " + s + "\n")
	}
	if g := strings.TrimSpace(pattern.UserGoal); g != "" {
		b.WriteString("User goal: " + g + "\n")
	}
	if len(pattern.WinningPath) > 0 {
		b.WriteString("Typical steps: " + strings.Join(pattern.WinningPath, " -> ") + "\n")
	}
	if s := strings.TrimSpace(pattern.FinalOutput); s != "" {
		output := s
		if len(output) > 400 {
			output = output[:400]
		}
		b.WriteString("Last result excerpt: " + output + "\n")
	}
	fmt.Fprintf(&b, "Observed occurrences: %d (success rate %.0f%%)\n", pattern.EventCount, pattern.SuccessRate*100)
	return b.String()
}
