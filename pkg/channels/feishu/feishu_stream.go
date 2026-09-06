//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

// PicoClaw - Ultra-lightweight AI agent

package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Streaming throttles. The answer element uses Feishu's native typewriter
// (print_frequency_ms), so we only rate-limit our own API calls; the process
// panel rebuild is a full card update and needs a longer interval.
const (
	feishuAnswerFlushInterval = 200 * time.Millisecond
	feishuPanelFlushInterval  = 800 * time.Millisecond
)

// feishuCardStreamer implements bus.Streamer on top of a CardKit v2 streaming
// card: one card per turn, showing a process panel (reasoning rounds + tool
// steps) above the streamed answer.
type feishuCardStreamer struct {
	ch     *FeishuChannel
	chatID string
	cardID string

	mu      sync.Mutex
	seq     int
	startAt time.Time

	state       feishuStreamState
	answer      string
	aborted     bool
	done        bool
	cancelReasn string
	reasonAt    time.Time // start of the current reasoning round
	lastAt      time.Time // last activity; guards stale reuse in BeginStream

	// phase tracks what the turn is doing right now (see feishuPhase*
	// constants) so the status line stays truthful; renderedPhase is the
	// phase already sent in the last card refresh — a change forces one
	// immediate refresh even inside the throttle window. panelDirty marks
	// panel-affecting changes (reasoning, tools, steering) awaiting a flush.
	phase         string
	renderedPhase string
	panelDirty    bool

	answerSentAt time.Time
	panelSentAt  time.Time
}

// setPhaseLocked records the current turn phase for the status line.
func (s *feishuCardStreamer) setPhaseLocked(phase string) {
	s.phase = phase
}

// feishuStreamReuseTTL bounds how long an unfinished card may be picked up
// again by a later BeginStream. Aborted turns normally cancel their streamer,
// but if that cleanup ever fails (process restart aside), a stale card must
// not swallow the next turn.
const feishuStreamReuseTTL = 2 * time.Minute

// BeginStream implements channels.StreamingCapable. The manager may call this
// once per LLM iteration within a turn; we reuse the in-flight card for the
// same chat so the whole turn stays on one card and is sealed on Finalize.
func (c *FeishuChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if v, ok := c.streams.Load(chatID); ok {
		if s, ok := v.(*feishuCardStreamer); ok {
			s.mu.Lock()
			reusable := !s.done && time.Since(s.lastAt) < feishuStreamReuseTTL
			if reusable {
				// One BeginStream per LLM call: reuse means another iteration.
				s.state.LLMCalls++
				s.lastAt = time.Now()
			}
			s.mu.Unlock()
			if reusable {
				return s, nil
			}
		}
	}

	cardJSON, err := json.Marshal(buildFeishuStreamingCard())
	if err != nil {
		return nil, fmt.Errorf("feishu stream: build card: %w", err)
	}
	createReq := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(string(cardJSON)).
			Build()).
		Build()
	createResp, err := c.client.Cardkit.V1.Card.Create(ctx, createReq)
	if err != nil || !createResp.Success() {
		code, msg := 0, ""
		if createResp != nil {
			code, msg = createResp.Code, createResp.Msg
		}
		return nil, fmt.Errorf("feishu stream: cardkit create failed (code=%d msg=%s err=%v)", code, msg, err)
	}
	if createResp.Data == nil || createResp.Data.CardId == nil {
		return nil, fmt.Errorf("feishu stream: cardkit create returned no card_id")
	}
	cardID := *createResp.Data.CardId

	// Deliver the card to the chat as a message referencing the card entity.
	msgContent, _ := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]any{"card_id": cardID},
	})
	if _, err := c.sendCard(ctx, chatID, string(msgContent)); err != nil {
		return nil, fmt.Errorf("feishu stream: send streaming card: %w", err)
	}

	s := &feishuCardStreamer{
		ch:      c,
		chatID:  chatID,
		cardID:  cardID,
		startAt: time.Now(),
		lastAt:  time.Now(),
		// CardKit sequence must be a small incrementing positive int32;
		// seeding with UnixMilli overflows the API's accepted range (code 9499).
		seq: 0,
	}
	s.state.LLMCalls = 1
	c.streams.Store(chatID, s)
	logger.DebugCF("feishu", "streaming card created", map[string]any{
		"chat_id": chatID,
		"card_id": cardID,
	})
	return s, nil
}

// NotifySteeringInChat implements channels.SteeringNotifyCapable: records a
// steering acknowledgement on the chat's active streaming card so the user
// sees their mid-turn message was heard. Returns false when no card is active.
func (c *FeishuChannel) NotifySteeringInChat(ctx context.Context, chatID, preview string) bool {
	v, ok := c.streams.Load(chatID)
	if !ok {
		return false
	}
	s, ok := v.(*feishuCardStreamer)
	if !ok {
		return false
	}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return false
	}
	s.lastAt = time.Now()
	s.panelDirty = true
	s.state.SteeringCount++
	s.state.SteeringLast = preview
	err := s.refreshPanelLocked(ctx)
	s.mu.Unlock()
	s.logPanelErr("steering notice", err)
	return true
}

func (s *feishuCardStreamer) nextSeqLocked() int {
	s.seq++
	return s.seq
}

// Update streams accumulated answer text into the card's answer element,
// throttled to feishuAnswerFlushInterval. The answer element is owned by the
// native typewriter: this method must never trigger a full-card refresh
// (that would replace the element mid-print and drop its streaming state) —
// the phase flip only updates the status line element-scoped, best effort.
func (s *feishuCardStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.answer = content
	s.lastAt = time.Now()
	statusText := ""
	if s.phase != feishuPhaseAnswer {
		s.setPhaseLocked(feishuPhaseAnswer)
		s.renderedPhase = feishuPhaseAnswer
		statusText, _ = feishuLoadingText(feishuPhaseAnswer)
	}
	cardID := s.cardID
	statusSeq := 0
	if statusText != "" {
		statusSeq = s.nextSeqLocked()
	}
	throttled := time.Since(s.answerSentAt) < feishuAnswerFlushInterval
	if !throttled {
		s.answerSentAt = time.Now()
	}
	streamContent := sanitizeFeishuMarkdownImages(s.answer)
	seq := 0
	if !throttled {
		seq = s.nextSeqLocked()
	}
	s.mu.Unlock()

	if statusText != "" {
		if err := s.ch.cardkitStreamContent(ctx, cardID, feishuLoadingElementID, statusText, statusSeq); err != nil {
			s.logPanelErr("status line", err)
		}
	}
	if throttled {
		return nil
	}
	return s.ch.cardkitStreamContent(ctx, cardID, feishuAnswerElementID, streamContent, seq)
}

// UpdateReasoning accumulates the in-progress reasoning round and refreshes
// the process panel (throttled).
func (s *feishuCardStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.lastAt = time.Now()
	s.setPhaseLocked(feishuPhaseThinking)
	s.panelDirty = true
	if s.state.CurReasoning == "" {
		s.reasonAt = time.Now()
	}
	s.state.CurReasoning = content
	err := s.refreshPanelLocked(ctx)
	s.mu.Unlock()
	s.logPanelErr("reasoning update", err)
	return nil
}

// FinalizeReasoning closes the current reasoning round into the panel history.
func (s *feishuCardStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.lastAt = time.Now()
	if text := content; text != "" {
		s.state.CurReasoning = text
	}
	s.panelDirty = true
	if s.state.CurReasoning != "" {
		duration := time.Duration(0)
		if !s.reasonAt.IsZero() {
			duration = time.Since(s.reasonAt)
		}
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{
			Text:     s.state.CurReasoning,
			Duration: duration,
		})
		s.state.CurReasoning = ""
		s.reasonAt = time.Time{}
	}
	err := s.refreshPanelLocked(ctx)
	s.mu.Unlock()
	s.logPanelErr("reasoning finalize", err)
	return nil
}

// AppendToolStep implements bus.ToolStepStreamer.
func (s *feishuCardStreamer) AppendToolStep(ctx context.Context, step bus.ToolStep) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.lastAt = time.Now()
	s.setPhaseLocked(feishuPhaseThinking)
	s.panelDirty = true
	s.state.Tools = append(s.state.Tools, step)
	err := s.refreshPanelLocked(ctx)
	s.mu.Unlock()
	s.logPanelErr("tool step", err)
	return nil
}

// logPanelErr keeps panel refresh failures non-fatal: the panel is auxiliary
// and must never take the answer stream down with it — a returned error here
// would make the agent abort the whole streaming turn.
func (s *feishuCardStreamer) logPanelErr(op string, err error) {
	if err != nil {
		logger.WarnCF("feishu", "streaming panel refresh failed (answer stream continues)", map[string]any{
			"chat_id": s.chatID,
			"op":      op,
			"error":   err.Error(),
		})
	}
}

// refreshPanelLocked rebuilds the process panel in the card via a full card
// update, throttled to feishuPanelFlushInterval. A phase change (e.g. tools
// → answer) bypasses the throttle once so the status line and panel collapse
// reach the card promptly. Caller holds s.mu; the API call is made while
// holding the lock — streamer methods never call each other, so this only
// serializes updates, it cannot deadlock.
func (s *feishuCardStreamer) refreshPanelLocked(ctx context.Context) error {
	phaseChanged := s.phase != s.renderedPhase
	if !s.panelDirty && !phaseChanged {
		return nil
	}
	if !phaseChanged && time.Since(s.panelSentAt) < feishuPanelFlushInterval {
		return nil
	}
	// Without panel content a full-card refresh is only worth it to flip the
	// status line; there is nothing else to redraw yet.
	if !s.state.hasPanelContent() && !phaseChanged {
		return nil
	}

	card := buildFeishuCardWithinSize(func(panelBudget int) map[string]any {
		return buildFeishuRefreshCard(&s.state, s.answer, s.phase, panelBudget)
	})
	s.panelSentAt = time.Now()
	s.panelDirty = false
	s.renderedPhase = s.phase
	cardID, seq := s.cardID, s.nextSeqLocked()
	return s.ch.cardkitUpdateCard(ctx, cardID, card, seq)
}

// Finalize seals the card: full answer, collapsed panel, footer statistics,
// streaming mode off.
func (s *feishuCardStreamer) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

func (s *feishuCardStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	if content != "" {
		s.answer = content
	}
	if usage != nil {
		s.state.ContextUsed = usage.UsedTokens
		s.state.ContextTotal = usage.TotalTokens
		s.state.ContextOffset = usage.HistoryTokens
	}
	// Fold any in-progress reasoning round so the sealed panel is complete.
	if s.state.CurReasoning != "" {
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{Text: s.state.CurReasoning})
		s.state.CurReasoning = ""
	}
	state := s.state
	answer := sanitizeFeishuMarkdownImages(s.answer)
	elapsed := time.Since(s.startAt)
	cardID := s.cardID
	seq := s.nextSeqLocked()
	seqClose := s.nextSeqLocked()
	s.done = true
	s.mu.Unlock()
	defer s.ch.streams.Delete(s.chatID)

	card := buildFeishuFinalCard(&state, answer, false, elapsed, "")
	if err := s.ch.cardkitUpdateCard(ctx, cardID, card, seq); err != nil {
		return err
	}
	return s.ch.cardkitCloseStreaming(ctx, cardID, feishuCardSummary(answer), seqClose)
}

// Cancel seals the card in an interrupted state (best effort).
func (s *feishuCardStreamer) Cancel(ctx context.Context) {
	s.CancelWithReason(ctx, "")
}

// CancelWithReason implements bus.CancelReasonStreamer: the cause is shown on
// the sealed card's status line so users know why the reply stopped.
func (s *feishuCardStreamer) CancelWithReason(ctx context.Context, reason string) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	// Fold the in-progress reasoning round so the panel stays consistent.
	if s.state.CurReasoning != "" {
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{Text: s.state.CurReasoning})
		s.state.CurReasoning = ""
	}
	state := s.state
	answer := sanitizeFeishuMarkdownImages(s.answer)
	elapsed := time.Since(s.startAt)
	cardID := s.cardID
	seq := s.nextSeqLocked()
	seqClose := s.nextSeqLocked()
	if reason != "" {
		s.cancelReasn = reason
	}
	reason = s.cancelReasn
	s.aborted = true
	s.done = true
	s.mu.Unlock()
	defer s.ch.streams.Delete(s.chatID)

	card := buildFeishuFinalCard(&state, answer, true, elapsed, reason)
	if err := s.ch.cardkitUpdateCard(ctx, cardID, card, seq); err != nil {
		logger.WarnCF("feishu", "streaming card cancel seal failed", map[string]any{
			"chat_id": s.chatID,
			"error":   err.Error(),
		})
		return
	}
	_ = s.ch.cardkitCloseStreaming(ctx, cardID, feishuCardSummary(answer), seqClose)
}

func (s *feishuCardStreamer) SetModelName(modelName string) {
	s.mu.Lock()
	s.state.ModelName = modelName
	s.mu.Unlock()
}

func (s *feishuCardStreamer) SetTurnUsage(inputTokens, outputTokens int) {
	s.mu.Lock()
	s.state.InputTokens = inputTokens
	s.state.OutputTokens = outputTokens
	s.mu.Unlock()
}

// --- CardKit API wrappers ---

// cardkitStreamContent pushes accumulated text into one card element with a
// monotonic sequence (Feishu requires increasing sequence numbers while the
// card is in streaming mode).
func (c *FeishuChannel) cardkitStreamContent(ctx context.Context, cardID, elementID, content string, sequence int) error {
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).
		ElementId(elementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Content(content).
			Sequence(sequence).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.CardElement.Content(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu stream content: %w", channels.ErrTemporary)
	}
	if !resp.Success() {
		c.invalidateTokenOnAuthError(resp.Code)
		if resp.Code == 300309 {
			// Card already sealed (Finalize/Cancel raced this in-flight update).
			// The sealed card carries the full answer, so this late update is
			// redundant — swallow instead of failing the whole LLM call.
			return nil
		}
		return fmt.Errorf("feishu stream content api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}

// cardkitUpdateCard replaces the whole card content (used for panel refreshes
// and the final seal). Feishu caps the card JSON at 30KB — reject locally with
// a clear error so callers can degrade instead of hitting an opaque API error.
func (c *FeishuChannel) cardkitUpdateCard(ctx context.Context, cardID string, card map[string]any, sequence int) error {
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("feishu cardkit update: marshal card: %w", err)
	}
	if len(cardJSON) > 30000 {
		return fmt.Errorf("feishu cardkit update: card json %d bytes exceeds Feishu 30KB limit", len(cardJSON))
	}
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(larkcardkit.NewCardBuilder().
				Type("card_json").
				Data(string(cardJSON)).
				Build()).
			Sequence(sequence).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu cardkit update: %w", channels.ErrTemporary)
	}
	if !resp.Success() {
		c.invalidateTokenOnAuthError(resp.Code)
		return fmt.Errorf("feishu cardkit update api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}

// cardkitCloseStreaming turns streaming mode off and sets the card summary.
func (c *FeishuChannel) cardkitCloseStreaming(ctx context.Context, cardID string, summary map[string]any, sequence int) error {
	settings := map[string]any{
		"config": map[string]any{
			"streaming_mode": false,
			"summary":        summary,
		},
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("feishu cardkit settings: marshal: %w", err)
	}
	req := larkcardkit.NewSettingsCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewSettingsCardReqBodyBuilder().
			Settings(string(settingsJSON)).
			Sequence(sequence).
			Build()).
		Build()
	resp, err := c.client.Cardkit.V1.Card.Settings(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu cardkit settings: %w", channels.ErrTemporary)
	}
	if !resp.Success() {
		c.invalidateTokenOnAuthError(resp.Code)
		return fmt.Errorf("feishu cardkit settings api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}
