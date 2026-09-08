// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// Two-phase session titling (borrowed from hermes-agent, see
// docs/design/hermes-borrowing-analysis.zh.md §二):
//
//   - Phase 1 (sync, at turn start): a deterministic title derived from the
//     opening user message is written before the model call — it cannot
//     fail and guarantees every session is nameable immediately.
//   - Phase 2 (async): a background light-model call upgrades derived
//     titles to a concise summary title. Source priority (user > llm >
//     derived) is enforced by the session store's CAS setter, so a manual
//     /title is never overwritten and a slow upgrade cannot clobber a
//     newer title of equal or higher rank... equal rank is replaced
//     (llm retry), which is also the desired behavior.

// Title sources; must match memory.SessionTitleSource* / the store's
// priority ranking.
const (
	titleSourceDerived = "derived"
	titleSourceLLM     = "llm"
	titleSourceUser    = "user"
)

const (
	// derivedTitleMaxRunes caps deterministic titles; the launcher list
	// renders ~60 runes, derived titles stay a bit shorter.
	derivedTitleMaxRunes = 48
	// llmTitleMaxRunes is also the answer-shaped guard: a "title" longer
	// than this is the model answering the message, not naming it — reject
	// outright rather than truncate (truncating stores half an answer).
	llmTitleMaxRunes = 32
	// titleModelMaxTokens bounds the light-model call.
	titleModelMaxTokens = 96
	titleModelTimeout  = 30 * time.Second
)

// machineTitlePrefixes are opening-message shapes that must never become
// titles: system notes, cron/heartbeat scaffolding, tool annotations.
var machineTitlePrefixes = []string{
	"[System", "[system", "(heartbeat", "[heartbeat", "[Cron", "[cron",
	"[image", "[audio", "[video", "[file", "[attachment", "[tool call",
}

// machineTitleSenders marks non-human senders whose turns should not title
// the session (async tool results, heartbeat ticks, cron jobs).
func machineTitleSender(senderID string) bool {
	s := strings.ToLower(senderID)
	return s == "heartbeat" ||
		strings.HasPrefix(s, "heartbeat:") ||
		strings.HasPrefix(s, "async:") ||
		strings.HasPrefix(s, "cron:") ||
		strings.HasPrefix(s, "system")
}

// deriveSessionTitle cleans an opening user message into a deterministic
// title: strip control/zero-width runes and wrapping quotes, collapse
// whitespace, cap on a rune boundary with an ellipsis marker.
func deriveSessionTitle(msg string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(msg) {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff:
			// control / zero-width — drop
		default:
			b.WriteRune(r)
		}
	}
	title := strings.Join(strings.Fields(b.String()), " ")
	title = strings.Trim(title, "\"'“”‘’「」『』 \t")
	if title == "" {
		return ""
	}
	if runes := []rune(title); len(runes) > derivedTitleMaxRunes {
		title = string(runes[:derivedTitleMaxRunes]) + "…"
	}
	return title
}

// isMachineTitleOpening reports whether an opening message is machine
// scaffolding rather than human text.
func isMachineTitleOpening(content string) bool {
	c := strings.TrimLeft(strings.TrimSpace(content), " \t")
	if c == "" {
		return true
	}
	for _, prefix := range machineTitlePrefixes {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// titleUpgradeTracker de-duplicates background title upgrades per process:
// at most one in-flight and one attempt per session per process lifetime
// (a failed upgrade keeps the derived title; the next process tries again).
type titleUpgradeTracker struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	tried    map[string]struct{}
}

var titleUpgrades = &titleUpgradeTracker{
	inFlight: make(map[string]struct{}),
	tried:    make(map[string]struct{}),
}

func (t *titleUpgradeTracker) begin(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, busy := t.inFlight[key]; busy {
		return false
	}
	if _, done := t.tried[key]; done {
		return false
	}
	t.inFlight[key] = struct{}{}
	return true
}

func (t *titleUpgradeTracker) finish(key string, retryable bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.inFlight, key)
	if !retryable {
		t.tried[key] = struct{}{}
	}
}

// titleCapableStore is the optional store capability backing both phases.
type titleCapableStore interface {
	SetSessionTitle(sessionKey, title, source string) bool
	GetSessionTitle(sessionKey string) (title, source string, ok bool)
}

// maybeTitleSession runs phase 1 and arms phase 2 for a turn. It is called
// at turn start, after session metadata is ensured and before the first
// model call, and must never fail the turn.
func (al *AgentLoop) maybeTitleSession(agent *AgentInstance, opts *processOptions) {
	if agent == nil || opts == nil || agent.Sessions == nil {
		return
	}
	if cfg := al.GetConfig(); cfg != nil && !cfg.Agents.Defaults.SessionTitlesEnabled() {
		return
	}
	store, ok := agent.Sessions.(titleCapableStore)
	if !ok {
		return
	}
	sessionKey := opts.Dispatch.SessionKey
	if strings.TrimSpace(sessionKey) == "" {
		return
	}

	source, _, hasTitle := store.GetSessionTitle(sessionKey)
	if hasTitle && source != titleSourceDerived {
		return // llm/user title already settled
	}

	userMsg := strings.TrimSpace(opts.UserMessage)
	if userMsg == "" {
		userMsg = strings.TrimSpace(opts.Dispatch.UserMessage)
	}

	if !hasTitle {
		// Phase 1: deterministic derived title, best effort. A concurrent
		// /title cannot be clobbered: the store's CAS keeps user titles on
		// top, and a lost derived write simply retries next turn.
		if machineTitleSender(opts.SenderID) || isMachineTitleOpening(userMsg) {
			return
		}
		if derived := deriveSessionTitle(userMsg); derived != "" {
			if store.SetSessionTitle(sessionKey, derived, titleSourceDerived) {
				source = titleSourceDerived
				hasTitle = true
				logger.DebugCF("agent", "session titled (derived)", map[string]any{
					"session_key": sessionKey,
					"title":       derived,
				})
			}
		}
	}
	if !hasTitle || source != titleSourceDerived {
		return
	}

	// Phase 2: background light-model upgrade of the derived title.
	al.upgradeSessionTitleAsync(agent, store, sessionKey, userMsg)
}

// upgradeSessionTitleAsync asks the light model for a concise title and
// stores it via the CAS setter. Fire-and-forget: a lost upgrade (process
// exit) is retried by the next process because the title stays derived.
func (al *AgentLoop) upgradeSessionTitleAsync(
	agent *AgentInstance,
	store titleCapableStore,
	sessionKey, openingMsg string,
) {
	if !titleUpgrades.begin(sessionKey) {
		return
	}
	go func() {
		title, err := al.generateSessionTitle(agent, openingMsg)
		if err != nil {
			// Retryable errors allow another attempt on a later turn.
			titleUpgrades.finish(sessionKey, true)
			logger.DebugCF("agent", "session title upgrade failed", map[string]any{
				"session_key": sessionKey,
				"error":       err.Error(),
			})
			return
		}
		titleUpgrades.finish(sessionKey, false)
		if title == "" {
			return // guard rejected the answer-shaped output; keep derived
		}
		if store.SetSessionTitle(sessionKey, title, titleSourceLLM) {
			logger.DebugCF("agent", "session titled (llm)", map[string]any{
				"session_key": sessionKey,
				"title":       title,
			})
		}
	}()
}

// generateSessionTitle calls the light model (falling back to the primary
// provider) for a short title and applies the answer-shaped guard.
func (al *AgentLoop) generateSessionTitle(agent *AgentInstance, openingMsg string) (string, error) {
	openingMsg = strings.TrimSpace(openingMsg)
	if openingMsg == "" {
		return "", nil
	}
	if len(openingMsg) > 1200 {
		openingMsg = string([]rune(openingMsg)[:1200])
	}

	provider := agent.Provider
	model := agent.Model
	if agent.LightProvider != nil {
		provider = agent.LightProvider
		model = sideQuestionModelName(agent, true)
	}
	if provider == nil {
		return "", fmt.Errorf("no provider available for title generation")
	}

	ctx, cancel := context.WithTimeout(context.Background(), titleModelTimeout)
	defer cancel()
	resp, err := provider.Chat(
		ctx,
		[]providers.Message{{Role: "user", Content: fmt.Sprintf(
			"用一句不超过 %d 个字的短语为下面的对话起一个标题，直接输出标题本身，不要引号、不要标点结尾、不要解释。\n\n对话开头：\n%s",
			llmTitleMaxRunes, openingMsg,
		)}},
		nil,
		model,
		map[string]any{
			"max_tokens":  titleModelMaxTokens,
			"temperature": 0.3,
		},
	)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("empty title response")
	}

	title := sanitizeLLMTitle(resp.Content)
	if title == "" {
		// Answer-shaped output (or empty): keep the derived title rather
		// than storing half an answer.
		return "", nil
	}
	return title, nil
}

// sanitizeLLMTitle normalizes a model-produced title and applies the
// answer-shaped guard: multi-line or oversized output is rejected, not
// truncated (a truncated answer is still an answer).
func sanitizeLLMTitle(content string) string {
	title := strings.TrimSpace(content)
	title = strings.Trim(title, "\"'“”‘’「」『』 \t\n")
	if title == "" || strings.ContainsAny(title, "\n\r") {
		return ""
	}
	title = strings.TrimSuffix(title, "。")
	title = strings.TrimSuffix(title, ".")
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if utf8.RuneCountInString(title) > llmTitleMaxRunes {
		return ""
	}
	return title
}

// setSessionTitleUser backs the /title command: manual titles outrank
// everything and are applied immediately.
func (al *AgentLoop) setSessionTitleUser(agent *AgentInstance, sessionKey, title string) bool {
	if agent == nil || agent.Sessions == nil {
		return false
	}
	store, ok := agent.Sessions.(titleCapableStore)
	if !ok {
		return false
	}
	return store.SetSessionTitle(sessionKey, title, titleSourceUser)
}

// getSessionTitleInfo backs /title without arguments.
func (al *AgentLoop) getSessionTitleInfo(agent *AgentInstance, sessionKey string) (string, string, bool) {
	if agent == nil || agent.Sessions == nil {
		return "", "", false
	}
	store, ok := agent.Sessions.(titleCapableStore)
	if !ok {
		return "", "", false
	}
	return store.GetSessionTitle(sessionKey)
}
