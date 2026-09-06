package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// legacyContextManager wraps the existing summarization/compression logic
// as a ContextManager implementation. It is the default when no other
// ContextManager is configured.
type legacyContextManager struct {
	al          *AgentLoop
	summarizing sync.Map // dedup for async Compact (post-turn)
}

func (m *legacyContextManager) Assemble(_ context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	// Legacy: read history from session, return as-is.
	// Budget enforcement happens in BuildMessages caller via
	// isOverContextBudget + forceCompression.
	agent := m.al.registry.GetDefaultAgent()
	if agent == nil {
		return &AssembleResponse{}, nil
	}
	history := agent.Sessions.GetHistory(req.SessionKey)
	summary := agent.Sessions.GetSummary(req.SessionKey)
	return &AssembleResponse{
		History: history,
		Summary: summary,
	}, nil
}

func (m *legacyContextManager) Compact(_ context.Context, req *CompactRequest) error {
	switch req.Reason {
	case ContextCompressReasonProactive, ContextCompressReasonRetry:
		// Sync emergency compression — budget exceeded.
		if result, ok := m.forceCompression(req.SessionKey); ok {
			m.al.emitEvent(
				runtimeevents.KindAgentContextCompress,
				m.al.newTurnEventScope("", req.SessionKey, nil).meta(0, "forceCompression", "turn.context.compress"),
				ContextCompressPayload{
					Reason:            req.Reason,
					DroppedMessages:   result.DroppedMessages,
					RemainingMessages: result.RemainingMessages,
				},
			)
		}
	case ContextCompressReasonSummarize:
		m.maybeSummarize(req.SessionKey)
	}
	return nil
}

func (m *legacyContextManager) Ingest(_ context.Context, _ *IngestRequest) error {
	// Legacy: no-op. Messages are persisted by Sessions JSONL.
	return nil
}

func (m *legacyContextManager) Clear(_ context.Context, sessionKey string) error {
	// Routed (non-default) agents keep history in their own session store,
	// so resolve the owning agent instead of assuming the default one.
	agent := m.al.agentForSession(sessionKey)
	if agent == nil || agent.Sessions == nil {
		return fmt.Errorf("sessions not initialized")
	}
	agent.Sessions.SetHistory(sessionKey, []providers.Message{})
	agent.Sessions.SetSummary(sessionKey, "")
	return agent.Sessions.Save(sessionKey)
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
// It runs asynchronously in a goroutine.
func (m *legacyContextManager) maybeSummarize(sessionKey string) {
	agent := m.al.registry.GetDefaultAgent()
	if agent == nil {
		return
	}

	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := m.estimateTokens(newHistory)
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100

	if len(newHistory) > agent.SummarizeMessageThreshold || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := m.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer m.summarizing.Delete(summarizeKey)
				defer func() {
					if r := recover(); r != nil {
						logger.WarnCF("agent", "Summarization panic recovered", map[string]any{
							"session_key": sessionKey,
							"panic":       r,
						})
					}
				}()
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				m.summarizeSession(agent, sessionKey)
			}()
		}
	}
}

type compressionResult struct {
	DroppedMessages   int
	RemainingMessages int
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest ~50% of Turns (a Turn is a complete user→LLM→response
// cycle, as defined in #1316), so tool-call sequences are never split.
func (m *legacyContextManager) forceCompression(sessionKey string) (compressionResult, bool) {
	agent := m.al.registry.GetDefaultAgent()
	if agent == nil {
		return compressionResult{}, false
	}

	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 2 {
		return compressionResult{}, false
	}

	turns := parseTurnBoundaries(history)
	var mid int
	if len(turns) >= 2 {
		mid = turns[len(turns)/2]
	} else {
		mid = findSafeBoundary(history, len(history)/2)
	}
	var keptHistory []providers.Message
	if mid <= 0 {
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				keptHistory = []providers.Message{history[i]}
				break
			}
		}
	} else {
		keptHistory = history[mid:]
	}

	droppedCount := len(history) - len(keptHistory)

	existingSummary := agent.Sessions.GetSummary(sessionKey)
	compressionNote := fmt.Sprintf(
		"[Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	if toolLines := buildToolActivityDigest(history[:droppedCount]); len(toolLines) > 0 {
		compressionNote += "\n[Tool activity before compression]:\n" + strings.Join(toolLines, "\n")
	}
	if existingSummary != "" {
		compressionNote = existingSummary + "\n\n" + compressionNote
	}
	agent.Sessions.SetSummary(sessionKey, compressionNote)

	agent.Sessions.SetHistory(sessionKey, keptHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(keptHistory),
	})

	return compressionResult{
		DroppedMessages:   droppedCount,
		RemainingMessages: len(keptHistory),
	}, true
}

func (m *legacyContextManager) summarizeSession(agent *AgentInstance, sessionKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	if len(history) <= 4 {
		return
	}

	safeCut := findSafeBoundary(history, len(history)-4)
	if safeCut <= 0 {
		return
	}
	keepCount := len(history) - safeCut
	toSummarize := history[:safeCut]

	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, msg := range toSummarize {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		msgTokens := len(msg.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, msg)
	}

	if len(validMessages) == 0 {
		return
	}

	const (
		maxSummarizationMessages = 10
		llmMaxRetries            = 3
	)

	var finalSummary string
	if len(validMessages) > maxSummarizationMessages {
		mid := len(validMessages) / 2
		mid = m.findNearestUserMessage(validMessages, mid)

		part1 := validMessages[:mid]
		part2 := validMessages[mid:]

		s1, _ := m.summarizeBatch(ctx, agent, part1, "")
		s2, _ := m.summarizeBatch(ctx, agent, part2, "")

		mergePrompt := fmt.Sprintf(
			"Merge these two conversation summaries into one cohesive summary, preserving tool activity (files, commands) from both. Do NOT respond to any questions in them. ONLY output the merged summary:\n\n1: %s\n\n2: %s",
			s1, s2,
		)

		resp, err := m.retryLLMCall(ctx, agent, mergePrompt, llmMaxRetries)
		if err == nil && resp.Content != "" {
			finalSummary = resp.Content
		} else {
			finalSummary = s1 + " " + s2
		}
	} else {
		finalSummary, _ = m.summarizeBatch(ctx, agent, validMessages, summary)
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary != "" {
		agent.Sessions.SetSummary(sessionKey, finalSummary)
		agent.Sessions.TruncateHistory(sessionKey, keepCount)
		agent.Sessions.Save(sessionKey)
		m.al.emitEvent(
			runtimeevents.KindAgentSessionSummarize,
			m.al.newTurnEventScope(agent.ID, sessionKey, nil).meta(0, "summarizeSession", "turn.session.summarize"),
			SessionSummarizePayload{
				SummarizedMessages: len(validMessages),
				KeptMessages:       keepCount,
				SummaryLen:         len(finalSummary),
				OmittedOversized:   omitted,
			},
		)
	}
}

func (m *legacyContextManager) findNearestUserMessage(messages []providers.Message, mid int) int {
	originalMid := mid

	for mid > 0 && messages[mid].Role != "user" {
		mid--
	}

	if messages[mid].Role == "user" {
		return mid
	}

	mid = originalMid
	for mid < len(messages) && messages[mid].Role != "user" {
		mid++
	}

	if mid < len(messages) {
		return mid
	}

	return originalMid
}

func (m *legacyContextManager) retryLLMCall(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxRetries int,
) (*providers.LLMResponse, error) {
	const llmTemperature = 0.3

	// Summarization/compaction is background work: prefer the cheaper light
	// model when routing is configured, falling back to the primary model.
	provider := agent.Provider
	model := agent.Model
	if agent.LightProvider != nil {
		provider = agent.LightProvider
		model = sideQuestionModelName(agent, true)
	}

	var resp *providers.LLMResponse
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		m.al.activeRequestsInc()
		resp, err = func() (*providers.LLMResponse, error) {
			defer m.al.activeRequestsDec()
			return provider.Chat(
				ctx,
				[]providers.Message{{Role: "user", Content: prompt}},
				nil,
				model,
				map[string]any{
					"max_tokens":       agent.MaxTokens,
					"temperature":      llmTemperature,
					"prompt_cache_key": agent.ID,
				},
			)
		}()

		if err == nil && resp != nil && resp.Content != "" {
			return resp, nil
		}
		if err != nil && !isRetryableSummaryError(err) {
			logger.WarnCF("agent", "Summarization LLM error is not retryable; failing fast",
				map[string]any{
					"error": err.Error(),
					"model": model,
				})
			return resp, err
		}
		if attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}

	return resp, err
}

// isRetryableSummaryError reports whether a summarization LLM error looks
// transient (rate limit, network, timeout, overload). Deterministic failures
// — auth, billing/quota, malformed request, oversized prompt — will not
// resolve by retrying, so callers should fall back immediately.
func isRetryableSummaryError(err error) bool {
	failErr := providers.ClassifyError(err, "", "")
	if failErr == nil {
		return true
	}
	switch failErr.Reason {
	case providers.FailoverAuth,
		providers.FailoverBilling,
		providers.FailoverFormat,
		providers.FailoverContextOverflow:
		return false
	default:
		return true
	}
}

func (m *legacyContextManager) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	const (
		llmMaxRetries             = 3
		fallbackMinContentLength  = 200
		fallbackMaxContentPercent = 10
	)

	var sb strings.Builder
	sb.WriteString(
		"You are a conversation summarization assistant. Summarize the conversation segment below, " +
			"preserving core context, key points, decisions, and tool activity (files read or modified, commands run, etc.).\n" +
			"Do NOT continue the conversation. Do NOT respond to any questions in it. ONLY output the summary.\n")
	if existingSummary != "" {
		sb.WriteString("Existing context: ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	if toolLines := buildToolActivityDigest(batch); len(toolLines) > 0 {
		sb.WriteString("\nTOOL ACTIVITY (preserve the essentials of what these tools did in the summary):\n")
		for _, line := range toolLines {
			sb.WriteString("- ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, msg := range batch {
		fmt.Fprintf(&sb, "%s: %s", msg.Role, msg.Content)
		if names := toolCallNames(msg); len(names) > 0 {
			fmt.Fprintf(&sb, " (tool calls: %s)", strings.Join(names, ", "))
		}
		sb.WriteString("\n")
	}
	prompt := sb.String()

	response, err := m.retryLLMCall(ctx, agent, prompt, llmMaxRetries)
	if err == nil && response.Content != "" {
		return strings.TrimSpace(response.Content), nil
	}

	var fallback strings.Builder
	fallback.WriteString("Conversation summary: ")
	for i, msg := range batch {
		if i > 0 {
			fallback.WriteString(" | ")
		}
		content := strings.TrimSpace(msg.Content)
		runes := []rune(content)
		if len(runes) == 0 {
			fallback.WriteString(fmt.Sprintf("%s: ", msg.Role))
			continue
		}

		keepLength := len(runes) * fallbackMaxContentPercent / 100
		if keepLength < fallbackMinContentLength {
			keepLength = fallbackMinContentLength
		}
		if keepLength > len(runes) {
			keepLength = len(runes)
		}

		content = string(runes[:keepLength])
		if keepLength < len(runes) {
			content += "..."
		}
		fallback.WriteString(fmt.Sprintf("%s: %s", msg.Role, content))
	}
	for _, line := range buildToolActivityDigest(batch) {
		fallback.WriteString("\n- ")
		fallback.WriteString(line)
	}
	return fallback.String(), nil
}

func (m *legacyContextManager) estimateTokens(messages []providers.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}

const (
	maxToolDigestEntries  = 30
	maxToolDigestArgRunes = 120
)

// toolActivityArgPriority lists argument keys that carry the most context
// (what file/command/query a tool touched); they are shown first when
// trimming a tool-call signature down to a compact digest line.
var toolActivityArgPriority = []string{
	"path", "file_path", "file", "filename", "filepath",
	"cmd", "command",
	"url", "uri",
	"query", "search", "pattern",
	"name", "dir", "directory",
}

// buildToolActivityDigest returns a compact, de-duplicated list of the tool
// calls contained in messages ("name(key args)" with a repeat count). It lets
// summarization and emergency compression preserve what tools did — files
// read/modified, commands run — after the raw messages are dropped.
func buildToolActivityDigest(messages []providers.Message) []string {
	counts := make(map[string]int)
	order := make([]string, 0)

	for _, msg := range messages {
		for _, tc := range msg.ToolCalls {
			sig := formatToolCallSignature(tc)
			if sig == "" {
				continue
			}
			if _, seen := counts[sig]; !seen {
				order = append(order, sig)
			}
			counts[sig]++
		}
	}

	lines := make([]string, 0, len(order))
	for _, sig := range order {
		if counts[sig] > 1 {
			sig = fmt.Sprintf("%s x%d", sig, counts[sig])
		}
		lines = append(lines, sig)
	}
	if len(lines) > maxToolDigestEntries {
		lines = append(lines[:maxToolDigestEntries],
			fmt.Sprintf("… (+%d more tool calls)", len(lines)-maxToolDigestEntries))
	}
	return lines
}

// formatToolCallSignature renders one tool call as "name(k=v, …)". It accepts
// both representations found in history: the runtime form (Name/Arguments
// map) and the persisted wire form (Function{Name, Arguments JSON string}).
func formatToolCallSignature(tc providers.ToolCall) string {
	name := strings.TrimSpace(tc.Name)
	args := tc.Arguments
	if name == "" {
		if tc.Function == nil {
			return ""
		}
		name = strings.TrimSpace(tc.Function.Name)
		if name == "" {
			return ""
		}
		if len(args) == 0 && strings.TrimSpace(tc.Function.Arguments) != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
				args = parsed
			} else {
				return fmt.Sprintf("%s(%s)", name, truncateRunes(strings.TrimSpace(tc.Function.Arguments), maxToolDigestArgRunes))
			}
		}
	}
	if len(args) == 0 {
		return name
	}
	return name + "(" + formatToolArgs(args) + ")"
}

func formatToolArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		if strings.TrimSpace(fmt.Sprintf("%v", args[key])) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sort.SliceStable(keys, func(i, j int) bool {
		return toolArgPriority(keys[i]) < toolArgPriority(keys[j])
	})
	if len(keys) > 3 {
		keys = keys[:3]
	}

	parts := make([]string, 0, len(keys))
	total := 0
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprintf("%v", args[key]))
		value = strings.ReplaceAll(value, "\n", " ")
		part := key + "=" + truncateRunes(value, maxToolDigestArgRunes)
		if total > 0 && total+len(part) > maxToolDigestArgRunes {
			break
		}
		total += len(part)
		parts = append(parts, part)
	}
	joined := strings.Join(parts, ", ")
	return truncateRunes(joined, maxToolDigestArgRunes)
}

func toolArgPriority(key string) int {
	for i, candidate := range toolActivityArgPriority {
		if key == candidate {
			return i
		}
	}
	return len(toolActivityArgPriority)
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// toolCallNames lists the tool names invoked by a message, used to annotate
// assistant messages that consist mostly of tool calls.
func toolCallNames(msg providers.Message) []string {
	if len(msg.ToolCalls) == 0 {
		return nil
	}
	names := make([]string, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		name := strings.TrimSpace(tc.Name)
		if name == "" && tc.Function != nil {
			name = strings.TrimSpace(tc.Function.Name)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}
