//go:build amd64 || arm64 || riscv64 || mips64 || ppc64

// PicoClaw - Ultra-lightweight AI agent

package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Streaming throttles. The answer element uses Feishu's native typewriter
// (print_frequency_ms), so we only rate-limit our own API calls; the process
// panel rebuild is a full card update and needs a longer interval.
const (
	feishuAnswerFlushInterval = 200 * time.Millisecond
	feishuPanelFlushInterval  = 800 * time.Millisecond
)

// feishuCallTimeout bounds every CardKit call made from the streamer: these
// run while the card mutex is held or from the agent's inbound loop, so a
// hung request must never block either indefinitely (the lark client's
// default HTTP client has no timeout of its own).
const feishuCallTimeout = 10 * time.Second

// feishuCodeStreamingTimeout (200850): Feishu closed the card's streaming
// mode after a period without element updates — e.g. while a turn sat blocked
// in a human approval. Element writes are refused until the mode is reopened
// via the card settings API; full-card updates keep working.
const feishuCodeStreamingTimeout = 200850

// errFeishuStreamingEnded marks a CardKit rejection caused by the server
// closing streaming mode (200850). The card is still updatable, only element
// streaming is gone, so this must never fail the agent's LLM call.
var errFeishuStreamingEnded = errors.New("feishu card streaming mode ended")

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

	// eventSeq orders panel events (rounds, tool steps) by arrival so the
	// panel can interleave them chronologically. Distinct from seq, which is
	// the CardKit API sequence.
	eventSeq int

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

	// streamingLost records that Feishu closed the card's streaming mode
	// (200850) and reopening failed: the answer is then delivered through
	// throttled full-card refreshes instead of the typewriter element.
	streamingLost bool

	// API seams bound at construction (tests substitute fakes). All CardKit
	// calls from streamer methods must go through these, never s.ch directly.
	streamContent   func(ctx context.Context, cardID, elementID, content string, sequence int) error
	updateCard      func(ctx context.Context, cardID string, card map[string]any, sequence int) error
	reopenStreaming func(ctx context.Context, cardID string, sequence int) error
	closeStreaming  func(ctx context.Context, cardID string, summary map[string]any, sequence int) error

	// spinnerKey is the uploaded amber spinner image_key for the status
	// line's custom icon (empty = standard icon). Snapshot per streamer so
	// mid-turn invalidation does not race card construction.
	spinnerKey string
}

// newFeishuCardStreamer builds a streamer bound to a delivered card, wiring
// the CardKit API seams to the channel's real implementations.
func newFeishuCardStreamer(ch *FeishuChannel, chatID, cardID, spinnerKey string) *feishuCardStreamer {
	return &feishuCardStreamer{
		ch:      ch,
		chatID:  chatID,
		cardID:  cardID,
		startAt: time.Now(),
		lastAt:  time.Now(),
		// Best-effort prewarm: first streamer uploads the embedded GIF once
		// per process; later ones reuse the cached key (passed in here).
		spinnerKey: spinnerKey,
		// CardKit sequence must be a small incrementing positive int32;
		// seeding with UnixMilli overflows the API's accepted range (code 9499).
		seq:             0,
		streamContent:   ch.cardkitStreamContent,
		updateCard:      ch.cardkitUpdateCard,
		reopenStreaming: ch.cardkitReopenStreaming,
		closeStreaming:  ch.cardkitCloseStreaming,
	}
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

	cardJSON, err := json.Marshal(buildFeishuStreamingCard(chatID))
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

	s := newFeishuCardStreamer(c, chatID, cardID, c.spinnerIconKey(ctx))
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
//
// The panel refresh runs asynchronously: this method is called from the
// agent's inbound loop, which must never wait on a card API call — a hung
// request there would freeze all message processing (including /stop).
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
	s.mu.Unlock()

	go func() {
		// Best-effort UI notice: a detached context with the standard call
		// timeout, so a stalled request can neither block the turn's
		// streaming callbacks on the card mutex for long, nor outlive it.
		s.mu.Lock()
		err := s.refreshPanelLocked(context.Background())
		s.mu.Unlock()
		s.logPanelErr("steering notice", err)
	}()
	return true
}

func (s *feishuCardStreamer) nextSeqLocked() int {
	s.seq++
	return s.seq
}

// streamerActiveLocked reports whether the streamer still has an unsealed
// card. Caller holds s.mu.
func (s *feishuCardStreamer) streamerActiveLocked() bool {
	return !s.done
}

// handleCardAction implements the CardKit v2 button callback
// (card.action.trigger, delivered over the websocket long connection). The
// streaming card's stop button routes here: it synthesizes the "/stop"
// command as a regular inbound message so the existing command pipeline runs
// unchanged — including the confirmation reply and the card sealing with the
// "用户停止" verdict. Card callbacks carry no chat context, so the button
// embeds its chat_id in the callback value at render time.
func (c *FeishuChannel) handleCardAction(_ context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	toast := func(typ, text string) *callback.CardActionTriggerResponse {
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: typ, Content: text}}
	}
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return toast("error", "无法识别的卡片操作"), nil
	}
	value := event.Event.Action.Value
	cmd, _ := value["cmd"].(string)
	chatID, _ := value["chat_id"].(string)
	operatorOpenID := ""
	if event.Event.Operator != nil {
		operatorOpenID = event.Event.Operator.OpenID
	}

	// Resolve the operator through the same sender gate as typed messages;
	// a button click must never bypass the channel allowlist.
	sender := bus.SenderInfo{
		Platform:   "feishu",
		PlatformID: operatorOpenID,
	}
	if operatorOpenID != "" {
		sender.CanonicalID = identity.BuildCanonicalID("feishu", operatorOpenID)
	}
	if !c.IsAllowedSender(sender) {
		return toast("error", "无权执行此操作"), nil
	}

	switch cmd {
	case feishuStopCmd:
		if chatID == "" {
			return toast("error", "卡片缺少会话信息"), nil
		}
		// The button only exists while a card streams, but a delayed click can
		// land after the turn sealed; refuse instead of stopping nothing.
		if !c.chatHasActiveStreamer(chatID) {
			return toast("info", "当前没有进行中的任务"), nil
		}
		inboundCtx := bus.InboundContext{
			Channel:  "feishu",
			ChatID:   chatID,
			SenderID: operatorOpenID,
		}
		// Detached context: the /stop processing is downstream of the callback
		// and must not be bound to the ws frame's lifetime.
		if err := c.HandleInboundContext(context.Background(), chatID, "/stop", nil, inboundCtx, sender); err != nil {
			logger.WarnCF("feishu", "stop button: /stop enqueue failed", map[string]any{
				"chat_id": chatID,
				"error":   err.Error(),
			})
			return toast("error", "停止指令发送失败"), nil
		}
		logger.InfoCF("feishu", "stop button clicked; /stop enqueued", map[string]any{
			"chat_id": chatID,
			"sender":  operatorOpenID,
		})
		return toast("success", "已发送停止指令"), nil
	default:
		return toast("info", "未知操作"), nil
	}
}

// chatHasActiveStreamer reports whether the chat still has an unsealed
// streaming card (i.e. a stoppable turn).
func (c *FeishuChannel) chatHasActiveStreamer(chatID string) bool {
	v, ok := c.streams.Load(chatID)
	if !ok {
		return false
	}
	s, ok := v.(*feishuCardStreamer)
	if !ok {
		return false
	}
	s.mu.Lock()
	active := s.streamerActiveLocked()
	s.mu.Unlock()
	return active
}

// Update streams accumulated answer text into the card's answer element,
// throttled to feishuAnswerFlushInterval. The answer element is owned by the
// native typewriter: this method must never trigger a full-card refresh
// (that would replace the element mid-print and drop its streaming state).
//
// If Feishu closed the card's streaming mode while the turn was blocked (a
// human approval wait, a long tool run), element writes fail with 200850:
// reopen once and retry, and degrade to full-card refreshes if that fails —
// never fail the LLM call over a lost typewriter.
func (s *feishuCardStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.answer = content
	s.lastAt = time.Now()
	if s.phase != feishuPhaseAnswer {
		// The status line element is a div, which the element-content API
		// cannot write (code 300313); the phase reaches the card only through
		// panel refreshes and the final seal.
		s.setPhaseLocked(feishuPhaseAnswer)
		s.renderedPhase = feishuPhaseAnswer
	}
	if s.streamingLost {
		// Element streaming is gone server-side; the answer rides the
		// throttled full-card refreshes instead of the typewriter.
		s.panelDirty = true
		err := s.refreshPanelLocked(ctx)
		s.mu.Unlock()
		s.logPanelErr("degraded answer refresh", err)
		return nil
	}
	cardID := s.cardID
	throttled := time.Since(s.answerSentAt) < feishuAnswerFlushInterval
	if !throttled {
		s.answerSentAt = time.Now()
	}
	answerContent := sanitizeFeishuMarkdownImages(s.answer)
	seq := 0
	if !throttled {
		seq = s.nextSeqLocked()
	}
	s.mu.Unlock()

	if throttled {
		return nil
	}
	err := s.streamContent(ctx, cardID, feishuAnswerElementID, answerContent, seq)
	if err == nil || !errors.Is(err, errFeishuStreamingEnded) {
		return err
	}

	// 200850: reopen streaming mode (documented remedy: settings update with
	// streaming_mode=true), then retry the element write once.
	s.mu.Lock()
	if s.streamingLost {
		s.mu.Unlock()
		return nil
	}
	reopenSeq := s.nextSeqLocked()
	retrySeq := s.nextSeqLocked()
	s.mu.Unlock()
	if reopenErr := s.reopenStreaming(ctx, cardID, reopenSeq); reopenErr != nil {
		s.degradeStreaming(ctx, reopenErr)
		return nil
	}
	if retryErr := s.streamContent(ctx, cardID, feishuAnswerElementID, answerContent, retrySeq); retryErr != nil {
		s.degradeStreaming(ctx, retryErr)
	}
	return nil
}

// degradeStreaming marks the streamer as having lost element streaming and
// pushes one immediate full-card refresh carrying the answer so far, so the
// turn keeps rendering without the typewriter.
func (s *feishuCardStreamer) degradeStreaming(ctx context.Context, cause error) {
	s.mu.Lock()
	if s.streamingLost {
		s.mu.Unlock()
		return
	}
	s.streamingLost = true
	s.panelDirty = true
	s.panelSentAt = time.Time{} // force the next refresh through the throttle
	err := s.flushPanelLocked(ctx)
	s.mu.Unlock()
	logger.WarnCF("feishu", "streaming card lost element streaming; degrading to full-card updates", map[string]any{
		"chat_id": s.chatID,
		"card_id": s.cardID,
		"error":   cause.Error(),
	})
	s.logPanelErr("degraded full-card refresh", err)
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
		s.eventSeq++
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{
			Text:     s.state.CurReasoning,
			Duration: duration,
			Seq:      s.eventSeq,
		})
		s.state.CurReasoning = ""
		s.reasonAt = time.Time{}
	}
	err := s.refreshPanelLocked(ctx)
	s.mu.Unlock()
	s.logPanelErr("reasoning finalize", err)
	return nil
}

// AppendToolStep implements bus.ToolStepStreamer. A Running step replaces the
// live in-flight entry (rendered at the end of the panel timeline); a
// completed step clears it and joins the interleaved timeline.
func (s *feishuCardStreamer) AppendToolStep(ctx context.Context, step bus.ToolStep) error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.lastAt = time.Now()
	s.setPhaseLocked(feishuPhaseThinking)
	s.panelDirty = true
	if step.Running {
		running := step
		s.state.RunningTool = &running
	} else {
		s.state.RunningTool = nil
		s.eventSeq++
		s.state.Tools = append(s.state.Tools, step)
		s.state.ToolSeqs = append(s.state.ToolSeqs, s.eventSeq)
	}
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
	// status line — or, once element streaming is lost, to carry the answer.
	if !s.state.hasPanelContent() && !phaseChanged && !s.streamingLost {
		return nil
	}
	return s.flushPanelLocked(ctx)
}

// flushPanelLocked sends one full-card refresh unconditionally (caller holds
// s.mu and has already passed the throttle guards).
func (s *feishuCardStreamer) flushPanelLocked(ctx context.Context) error {
	spinnerKey := s.spinnerKey
	card := buildFeishuCardWithinSize(func(panelBudget int) map[string]any {
		return buildFeishuRefreshCard(&s.state, s.answer, s.phase, panelBudget, spinnerKey, s.chatID)
	})
	if s.streamingLost {
		// The card is out of streaming mode server-side; carrying the
		// streaming config would try to re-enter it on every refresh.
		degradeFeishuCardConfig(card)
	}
	s.panelSentAt = time.Now()
	s.panelDirty = false
	s.renderedPhase = s.phase
	cardID, seq := s.cardID, s.nextSeqLocked()
	err := s.updateCard(ctx, cardID, card, seq)
	if err != nil && spinnerKey != "" {
		// The refresh card is the only place the custom icon is used; a
		// rejection here most plausibly implicates it, so drop the key and
		// fall back to the standard icon for the rest of this process.
		s.ch.invalidateSpinnerIcon()
		s.spinnerKey = ""
	}
	return err
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
		s.eventSeq++
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{Text: s.state.CurReasoning, Seq: s.eventSeq})
		s.state.CurReasoning = ""
	}
	state := s.state
	answer := sanitizeFeishuMarkdownImages(s.answer)
	elapsed := time.Since(s.startAt)
	cardID := s.cardID
	seq := s.nextSeqLocked()
	seqClose := s.nextSeqLocked()
	streamingLost := s.streamingLost
	s.done = true
	s.mu.Unlock()
	defer s.ch.streams.Delete(s.chatID)

	card := buildFeishuFinalCard(&state, answer, false, elapsed, "")
	if err := s.updateCard(ctx, cardID, card, seq); err != nil {
		return err
	}
	if streamingLost {
		// Streaming mode was already closed by the server; nothing to seal.
		return nil
	}
	return s.closeStreaming(ctx, cardID, feishuCardSummary(answer), seqClose)
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
		s.eventSeq++
		s.state.Rounds = append(s.state.Rounds, feishuReasoningRound{Text: s.state.CurReasoning, Seq: s.eventSeq})
		s.state.CurReasoning = ""
	}
	state := s.state
	answer := sanitizeFeishuMarkdownImages(s.answer)
	elapsed := time.Since(s.startAt)
	cardID := s.cardID
	seq := s.nextSeqLocked()
	seqClose := s.nextSeqLocked()
	streamingLost := s.streamingLost
	if reason != "" {
		s.cancelReasn = reason
	}
	reason = s.cancelReasn
	s.aborted = true
	s.done = true
	s.mu.Unlock()
	defer s.ch.streams.Delete(s.chatID)

	card := buildFeishuFinalCard(&state, answer, true, elapsed, reason)
	if err := s.updateCard(ctx, cardID, card, seq); err != nil {
		logger.WarnCF("feishu", "streaming card cancel seal failed", map[string]any{
			"chat_id": s.chatID,
			"error":   err.Error(),
		})
		return
	}
	if streamingLost {
		return // streaming mode was already closed by the server
	}
	_ = s.closeStreaming(ctx, cardID, feishuCardSummary(answer), seqClose)
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
	ctx, cancel := context.WithTimeout(ctx, feishuCallTimeout)
	defer cancel()

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
		if resp.Code == feishuCodeStreamingTimeout {
			// Streaming mode auto-closed server-side (e.g. a long approval
			// wait with no element writes). Never ErrTemporary: failing the
			// LLM call over a lost typewriter kills the whole turn.
			return errFeishuStreamingEnded
		}
		return fmt.Errorf("feishu stream content api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}

// cardkitUpdateCard replaces the whole card content (used for panel refreshes
// and the final seal). Feishu caps the card JSON at 30KB — reject locally with
// a clear error so callers can degrade instead of hitting an opaque API error.
func (c *FeishuChannel) cardkitUpdateCard(ctx context.Context, cardID string, card map[string]any, sequence int) error {
	ctx, cancel := context.WithTimeout(ctx, feishuCallTimeout)
	defer cancel()

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
	ctx, cancel := context.WithTimeout(ctx, feishuCallTimeout)
	defer cancel()

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
		if resp.Code == feishuCodeStreamingTimeout {
			// Streaming mode already closed server-side — that is exactly
			// what this call wanted; treat as success.
			return nil
		}
		return fmt.Errorf("feishu cardkit settings api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}

// cardkitReopenStreaming re-enables streaming mode after Feishu closed it
// for inactivity (element writes fail with code 200850). The documented
// remedy is a card settings update setting streaming_mode back to true.
func (c *FeishuChannel) cardkitReopenStreaming(ctx context.Context, cardID string, sequence int) error {
	ctx, cancel := context.WithTimeout(ctx, feishuCallTimeout)
	defer cancel()

	settings := map[string]any{
		"config": map[string]any{"streaming_mode": true},
	}
	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("feishu cardkit reopen streaming: marshal: %w", err)
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
		return fmt.Errorf("feishu cardkit reopen streaming: %w", channels.ErrTemporary)
	}
	if !resp.Success() {
		c.invalidateTokenOnAuthError(resp.Code)
		return fmt.Errorf("feishu cardkit reopen streaming api error (code=%d msg=%s): %w", resp.Code, resp.Msg, channels.ErrTemporary)
	}
	return nil
}
