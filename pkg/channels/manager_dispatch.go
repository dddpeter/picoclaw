// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// GetStreamer implements bus.StreamDelegate.
// It checks if the named channel supports streaming and returns a Streamer.
func (m *Manager) GetStreamer(ctx context.Context, channelName, chatID, sessionKey string) (bus.Streamer, bool) {
	m.mu.RLock()
	ch, exists := m.channels[channelName]
	m.mu.RUnlock()

	if !exists {
		return nil, false
	}

	sc, ok := ch.(StreamingCapable)
	if !ok {
		return nil, false
	}

	// Session-scoped begin when the channel supports it (B2-2): keeps
	// concurrent turns from different sessions in one chat on distinct
	// streaming surfaces. Falls back to the legacy unscoped BeginStream.
	beginStream := func(beginCtx context.Context) (bus.Streamer, error) {
		if sb, ok := ch.(SessionScopedBeginStreamer); ok {
			return sb.BeginStreamForSession(beginCtx, chatID, sessionKey)
		}
		return sc.BeginStream(beginCtx, chatID)
	}
	streamer, err := beginStream(ctx)
	if err != nil {
		logger.DebugCF("channels", "Streaming unavailable, falling back to placeholder", map[string]any{
			"channel": channelName,
			"error":   err.Error(),
		})
		return nil, false
	}

	// Mark streamActive on Finalize so preSend knows to clean up the placeholder
	// and late auxiliary messages cannot leak after streaming produced a final.
	streamKey := streamSuppressionKey(channelName, chatID, sessionKey)
	placeholderKey := channelName + ":" + chatID
	clearMarker := func() {
		m.streamActive.Delete(streamKey)
	}
	onFinalize := func(finalizeCtx context.Context, finalContent string, canceled bool) {
		if m.toolFeedbackSeparateMessagesEnabled() {
			clearTrackedToolFeedbackMessage(
				ch,
				chatID,
				&bus.InboundContext{
					Channel: channelName,
					ChatID:  chatID,
				},
			)
		} else {
			dismissTrackedToolFeedbackMessage(
				finalizeCtx,
				ch,
				chatID,
				&bus.InboundContext{
					Channel: channelName,
					ChatID:  chatID,
				},
			)
		}
		if v, loaded := m.placeholders.LoadAndDelete(placeholderKey); loaded {
			if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
				if deleter, ok := ch.(MessageDeleter); ok {
					deleter.DeleteMessage(finalizeCtx, chatID, entry.id) // best effort
				} else if editor, ok := ch.(MessageEditor); ok {
					editor.EditMessage(finalizeCtx, chatID, entry.id, finalContent) // best effort fallback
				}
			}
		}
		// A canceled stream must not arm the suppression marker: the normal
		// outbound that delivers a fallback (non-streamed) response still has
		// to go through. A genuine Finalize arms the marker even with empty
		// content — display-side filters (e.g. tool-call parrot stripping)
		// can empty the final answer, and an unarmed marker let the raw
		// response escape as a duplicate plain message (observed 2026-10-05,
		// feishu: the parrot dump arrived as a second card next to the sealed
		// streaming card). Cancellation is therefore an explicit flag, not
		// inferred from empty content.
		if canceled {
			return
		}
		m.streamActive.Store(streamKey, true)
		m.streamAuxiliaryTombstones.Store(streamKey, time.Now())
	}

	if m.config != nil && m.config.Agents.Defaults.SplitOnMarker {
		return &splitMarkerStreamer{
			current:     streamer,
			reasoning:   reasoningStreamerFrom(streamer),
			begin:       beginStream,
			onFinalize:  onFinalize,
			clearMarker: clearMarker,
		}, true
	}

	return &finalizeHookStreamer{
		Streamer:    streamer,
		clearMarker: clearMarker,
		onFinalize:  onFinalize,
	}, true
}

func reasoningStreamerFrom(streamer bus.Streamer) bus.ReasoningStreamer {
	if reasoningStreamer, ok := streamer.(bus.ReasoningStreamer); ok {
		return reasoningStreamer
	}
	return nil
}

type modelNameStreamer interface {
	SetModelName(modelName string)
}

func setStreamerModelName(streamer any, modelName string) {
	setter, ok := streamer.(modelNameStreamer)
	if !ok {
		return
	}
	setter.SetModelName(modelName)
}

type turnUsageStreamer interface {
	SetTurnUsage(usage providers.UsageInfo)
}

// setStreamerTurnUsage forwards real per-turn token usage to a streamer that
// supports it, transparently unwrapping the manager's streamer wrappers.
func setStreamerTurnUsage(streamer any, usage providers.UsageInfo) {
	setter, ok := streamer.(turnUsageStreamer)
	if !ok {
		return
	}
	setter.SetTurnUsage(usage)
}

// splitMarkerStreamer turns accumulated streaming text containing
// MessageSplitMarker into separate channel stream messages.
type splitMarkerStreamer struct {
	mu             sync.Mutex
	current        bus.Streamer
	reasoning      bus.ReasoningStreamer
	begin          func(context.Context) (bus.Streamer, error)
	completedParts int
	finalized      bool
	onFinalize     func(context.Context, string, bool)
	clearMarker    func()
	modelName      string
	// turnUsage is the last-seen per-turn usage snapshot, replayed onto each
	// freshly begun inner streamer so split messages keep the footer stats.
	turnUsage providers.UsageInfo
}

func (s *splitMarkerStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, content)
}

func (s *splitMarkerStreamer) Finalize(ctx context.Context, content string) error {
	return s.FinalizeWithContext(ctx, content, nil)
}

func (s *splitMarkerStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.finalizeLocked(ctx, content, usage); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content, false)
	return nil
}

func (s *splitMarkerStreamer) UpdateReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reasoning == nil {
		return nil
	}
	setStreamerModelName(s.reasoning, s.modelName)
	return s.reasoning.UpdateReasoning(ctx, content)
}

func (s *splitMarkerStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reasoning == nil {
		return nil
	}
	setStreamerModelName(s.reasoning, s.modelName)
	return s.reasoning.FinalizeReasoning(ctx, content)
}

func (s *splitMarkerStreamer) AppendToolStep(ctx context.Context, step bus.ToolStep) error {
	s.mu.Lock()
	current := s.current
	s.mu.Unlock()
	if streamer, ok := current.(bus.ToolStepStreamer); ok {
		return streamer.AppendToolStep(ctx, step)
	}
	return nil
}

func (s *splitMarkerStreamer) SetModelName(modelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelName = strings.TrimSpace(modelName)
	setStreamerModelName(s.current, s.modelName)
	setStreamerModelName(s.reasoning, s.modelName)
}

func (s *splitMarkerStreamer) SetTurnUsage(usage providers.UsageInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnUsage = usage
	setStreamerTurnUsage(s.current, s.turnUsage)
}

func (s *splitMarkerStreamer) Cancel(ctx context.Context) {
	s.CancelWithReason(ctx, "")
}

func (s *splitMarkerStreamer) CancelWithReason(ctx context.Context, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		cancelStreamerWithReason(s.current, ctx, reason)
	}
	s.runFinalizeHook(ctx, "", true)
}

// cancelStreamerWithReason forwards a cancellation cause to streamers that
// understand it and falls back to a plain Cancel otherwise.
func cancelStreamerWithReason(streamer bus.Streamer, ctx context.Context, reason string) {
	if cr, ok := streamer.(bus.CancelReasonStreamer); ok && reason != "" {
		cr.CancelWithReason(ctx, reason)
		return
	}
	streamer.Cancel(ctx)
}

func (s *splitMarkerStreamer) ClearFinalizedStreamMarker() {
	if s.clearMarker != nil {
		s.clearMarker()
	}
}

func (s *splitMarkerStreamer) updateLocked(ctx context.Context, content string) error {
	// Fast path: without the marker, Split would scan the whole accumulated
	// content and allocate for nothing on every streamed chunk.
	var parts []string
	if strings.Contains(content, MessageSplitMarker) {
		parts = strings.Split(content, MessageSplitMarker)
	} else {
		parts = []string{content}
	}
	completedLimit := len(parts) - 1
	if err := s.finalizeCompletedPartsLocked(ctx, parts, completedLimit, nil); err != nil {
		return err
	}
	active := strings.TrimSpace(parts[len(parts)-1])
	if active == "" {
		return nil
	}
	if err := s.ensureCurrentLocked(ctx); err != nil {
		return err
	}
	return s.current.Update(ctx, active)
}

func (s *splitMarkerStreamer) finalizeLocked(ctx context.Context, content string, usage *bus.ContextUsage) error {
	parts := strings.Split(content, MessageSplitMarker)
	return s.finalizeCompletedPartsLocked(ctx, parts, len(parts), usage)
}

func (s *splitMarkerStreamer) finalizeCompletedPartsLocked(
	ctx context.Context,
	parts []string,
	limit int,
	usage *bus.ContextUsage,
) error {
	for s.completedParts < limit {
		content := strings.TrimSpace(parts[s.completedParts])
		isLast := s.completedParts == limit-1
		if content != "" {
			if err := s.ensureCurrentLocked(ctx); err != nil {
				return err
			}
			if isLast && usage != nil {
				if contextStreamer, ok := s.current.(bus.ContextUsageStreamer); ok {
					if err := contextStreamer.FinalizeWithContext(ctx, content, usage); err != nil {
						return err
					}
				} else if err := s.current.Finalize(ctx, content); err != nil {
					return err
				}
			} else if err := s.current.Finalize(ctx, content); err != nil {
				return err
			}
			s.current = nil
		}
		s.completedParts++
	}
	return nil
}

func (s *splitMarkerStreamer) ensureCurrentLocked(ctx context.Context) error {
	if s.current != nil {
		return nil
	}
	if s.begin == nil {
		return fmt.Errorf("streamer is not initialized")
	}
	streamer, err := s.begin(ctx)
	if err != nil {
		return err
	}
	s.current = streamer
	setStreamerModelName(s.current, s.modelName)
	setStreamerTurnUsage(s.current, s.turnUsage)
	return nil
}

func (s *splitMarkerStreamer) runFinalizeHook(ctx context.Context, content string, canceled bool) {
	if s.finalized {
		return
	}
	s.finalized = true
	if s.onFinalize != nil {
		s.onFinalize(ctx, content, canceled)
	}
}

func (m *Manager) streamAuxiliaryTombstoneActive(key string) bool {
	v, ok := m.streamAuxiliaryTombstones.Load(key)
	if !ok {
		return false
	}
	createdAt, ok := v.(time.Time)
	if !ok || time.Since(createdAt) > streamAuxiliaryTombstoneTTL {
		m.streamAuxiliaryTombstones.Delete(key)
		return false
	}
	return true
}

func (m *Manager) streamActiveForChat(channel, chatID string) bool {
	chatKey := streamSuppressionKey(channel, chatID, "")
	found := false
	m.streamActive.Range(func(key, _ any) bool {
		keyString, ok := key.(string)
		if !ok {
			return true
		}
		if keyString == chatKey || strings.HasPrefix(keyString, chatKey+":") {
			found = true
			return false
		}
		return true
	})
	return found
}

// finalizeHookStreamer wraps a Streamer to run a hook on Finalize.
type finalizeHookStreamer struct {
	Streamer
	onFinalize  func(context.Context, string, bool)
	clearMarker func()
}

func (s *finalizeHookStreamer) Finalize(ctx context.Context, content string) error {
	if err := s.Streamer.Finalize(ctx, content); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content, false)
	return nil
}

func (s *finalizeHookStreamer) FinalizeWithContext(ctx context.Context, content string, usage *bus.ContextUsage) error {
	if streamer, ok := s.Streamer.(bus.ContextUsageStreamer); ok {
		if err := streamer.FinalizeWithContext(ctx, content, usage); err != nil {
			return err
		}
	} else if err := s.Streamer.Finalize(ctx, content); err != nil {
		return err
	}
	s.runFinalizeHook(ctx, content, false)
	return nil
}

func (s *finalizeHookStreamer) UpdateReasoning(ctx context.Context, content string) error {
	if streamer, ok := s.Streamer.(bus.ReasoningStreamer); ok {
		return streamer.UpdateReasoning(ctx, content)
	}
	return nil
}

func (s *finalizeHookStreamer) FinalizeReasoning(ctx context.Context, content string) error {
	if streamer, ok := s.Streamer.(bus.ReasoningStreamer); ok {
		return streamer.FinalizeReasoning(ctx, content)
	}
	return nil
}

func (s *finalizeHookStreamer) AppendToolStep(ctx context.Context, step bus.ToolStep) error {
	if streamer, ok := s.Streamer.(bus.ToolStepStreamer); ok {
		return streamer.AppendToolStep(ctx, step)
	}
	return nil
}

func (s *finalizeHookStreamer) SetModelName(modelName string) {
	setStreamerModelName(s.Streamer, strings.TrimSpace(modelName))
}

func (s *finalizeHookStreamer) SetTurnUsage(usage providers.UsageInfo) {
	setStreamerTurnUsage(s.Streamer, usage)
}

// Cancel seals the card and still runs the cleanup hook so interrupted turns
// dismiss leftover tool_feedback messages instead of leaking them in chat.
func (s *finalizeHookStreamer) Cancel(ctx context.Context) {
	s.CancelWithReason(ctx, "")
}

func (s *finalizeHookStreamer) CancelWithReason(ctx context.Context, reason string) {
	cancelStreamerWithReason(s.Streamer, ctx, reason)
	s.runFinalizeHook(ctx, "", true)
}

func (s *finalizeHookStreamer) runFinalizeHook(ctx context.Context, content string, canceled bool) {
	if s.onFinalize != nil {
		s.onFinalize(ctx, content, canceled)
	}
}

func (s *finalizeHookStreamer) ClearFinalizedStreamMarker() {
	if s.clearMarker != nil {
		s.clearMarker()
	}
}

// newChannelWorker creates a channelWorker with a rate limiter configured
// for the given channel type. channelType is used for rate limit lookup.
func newChannelWorker(name string, ch Channel, channelType string) *channelWorker {
	rateVal := float64(defaultRateLimit)
	if r, ok := channelRateConfig[channelType]; ok {
		rateVal = r
	}
	burst := int(math.Max(1, math.Ceil(rateVal/2)))

	return &channelWorker{
		ch:         ch,
		queue:      make(chan bus.OutboundMessage, defaultChannelQueueSize),
		mediaQueue: make(chan bus.OutboundMediaMessage, defaultChannelQueueSize),
		done:       make(chan struct{}),
		mediaDone:  make(chan struct{}),
		limiter:    rate.NewLimiter(rate.Limit(rateVal), burst),
	}
}

// runWorker processes outbound messages for a single channel.
// Message processing follows this order:
//  1. SplitByMarker (if enabled in config) - LLM semantic marker-based splitting
//  2. SplitMessage - channel-specific length-based splitting (MaxMessageLength)
func (m *Manager) runWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.done)
	for {
		select {
		case msg, ok := <-w.queue:
			if !ok {
				return
			}
			maxLen := 0
			if mlp, ok := w.ch.(MessageLengthProvider); ok {
				maxLen = mlp.MaxMessageLength()
			}

			// Collect all message chunks to send
			var chunks []string

			// Step 1: Try marker-based splitting if enabled.
			// Tool feedback must stay a single message, so it skips marker splitting.
			// Stream-final duplicate responses must also stay intact so preSend can
			// consume the whole final message before any marker chunk leaks.
			if m.finalizedStreamActiveForMessage(name, msg) {
				chunks = []string{msg.Content}
			} else if m.config != nil && m.config.Agents.Defaults.SplitOnMarker && !outboundMessageIsToolFeedback(msg) {
				if markerChunks := SplitByMarker(msg.Content); len(markerChunks) > 1 {
					for _, chunk := range markerChunks {
						chunkMsg := msg
						chunkMsg.Content = chunk
						chunks = append(chunks, splitOutboundMessageContent(chunkMsg, maxLen)...)
					}
				}
			}

			// Step 2: Fallback to length-based splitting if no chunks from marker
			if len(chunks) == 0 {
				chunks = splitOutboundMessageContent(msg, maxLen)
			}

			// Step 3: Send all chunks
			for _, chunk := range chunks {
				chunkMsg := msg
				chunkMsg.Content = chunk
				m.sendWithRetry(ctx, name, w, chunkMsg)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) finalizedStreamActiveForMessage(channelName string, msg bus.OutboundMessage) bool {
	if m == nil || !outboundMessageIsFinal(msg) {
		return false
	}
	chatID := outboundMessageChatID(msg)
	if strings.TrimSpace(channelName) == "" || strings.TrimSpace(chatID) == "" {
		return false
	}
	_, active := m.streamActive.Load(streamSuppressionKey(channelName, chatID, msg.SessionKey))
	return active
}

// splitOutboundMessageContent splits regular outbound content by maxLen, but
// keeps tool feedback in a single message by truncating the explanation body.
func splitOutboundMessageContent(msg bus.OutboundMessage, maxLen int) []string {
	if maxLen > 0 {
		if outboundMessageIsToolFeedback(msg) {
			animationSafeLen := maxLen - MaxToolFeedbackAnimationFrameLength()
			if animationSafeLen <= 0 {
				animationSafeLen = maxLen
			}
			if len([]rune(msg.Content)) > animationSafeLen {
				return []string{utils.FitToolFeedbackMessage(msg.Content, animationSafeLen)}
			}
			return []string{msg.Content}
		}
		if len([]rune(msg.Content)) > maxLen {
			return SplitMessage(msg.Content, maxLen)
		}
	}
	return []string{msg.Content}
}

// sendWithRetry sends a message through the channel with rate limiting and
// retry logic. It classifies errors to determine the retry strategy:
//   - ErrNotRunning / ErrSendFailed: permanent, no retry
//   - ErrRateLimit: fixed delay retry
//   - ErrTemporary / unknown: exponential backoff retry
func (m *Manager) sendWithRetry(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMessage,
) ([]string, bool) {
	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		// ctx canceled, shutting down
		m.publishChannelEvent(
			runtimeevents.KindChannelRateLimited,
			name,
			scopeFromOutboundContext(msg.Context),
			runtimeevents.SeverityWarn,
			ChannelOutboundPayload{
				ContentLen:       len([]rune(msg.Content)),
				ReplyToMessageID: msg.ReplyToMessageID,
				Error:            err.Error(),
			},
		)
		return nil, false
	}

	// Pre-send: stop typing and try to edit placeholder
	if msgIDs, handled := m.preSend(ctx, name, msg, w.ch); handled {
		m.publishOutboundSent(name, msg, msgIDs)
		return msgIDs, true
	}

	var lastErr error
	var msgIDs []string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		msgIDs, lastErr = w.ch.Send(ctx, msg)
		if lastErr == nil {
			m.publishOutboundSent(name, msg, msgIDs)
			return msgIDs, true
		}

		// Permanent failures — don't retry
		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}

		// Last attempt exhausted — don't sleep
		if attempt == maxRetries {
			break
		}

		// Rate limit error — fixed delay
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return nil, false
			}
		}

		// ErrTemporary or unknown error — exponential backoff
		backoff := min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, false
		}
	}

	// All retries exhausted or permanent failure
	logger.ErrorCF("channels", "Send failed", map[string]any{
		"channel": name,
		"chat_id": outboundMessageChatID(msg),
		"error":   lastErr.Error(),
		"retries": maxRetries,
	})
	m.publishOutboundFailed(name, msg, lastErr, false)

	return nil, false
}

// Outbound dispatch wait bounds for the full-queue policy (vars so tests
// can shrink them). The single outbound dispatcher serves every channel:
// an unbounded block on one wedged channel's queue would stall dispatch
// for all channels and, cascading through the bus (blocking publish),
// freeze the agent turn goroutines themselves — the gateway-wide
// head-of-line blocking this policy replaces.
var (
	// secondary (tool_feedback): display bubble only — the final message
	// dismisses tracked feedback regardless, so losing one under pressure
	// costs a stale bubble, not information.
	outboundDropWaitSecondary = 250 * time.Millisecond
	// critical (final answers, tool_calls, approval/ask cards, plain
	// replies): bounded wait, then drop with an ERROR log and a runtime
	// event — sustained wedge on one channel degrades to loud message loss
	// instead of a gateway-wide stall.
	outboundDropWaitCritical = 2 * time.Second
)

// outboundMessageIsDroppableBeat reports whether the message is pure
// narration (progress heartbeat / thought) whose next beat always carries
// fresher state — safe to drop at once when the queue is full.
func outboundMessageIsDroppableBeat(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	kind := strings.TrimSpace(msg.Context.Raw["message_kind"])
	return strings.EqualFold(kind, "progress_note") || strings.EqualFold(kind, "thought")
}

// enqueueOutboundForDispatch enqueues one outbound message onto a channel
// worker's queue with the tiered full-queue policy above. Returns false
// only when the dispatcher should shut down (context canceled).
func (m *Manager) enqueueOutboundForDispatch(
	ctx context.Context,
	w *channelWorker,
	msg bus.OutboundMessage,
) bool {
	channel := outboundMessageChannel(msg)
	// Fast path: space available.
	select {
	case w.queue <- msg:
		m.publishOutboundQueued(channel, msg)
		return true
	default:
	}

	if outboundMessageIsDroppableBeat(msg) {
		logger.DebugCF("channels", "Outbound beat dropped on full channel queue", map[string]any{
			"channel": channel,
			"chat_id": outboundMessageChatID(msg),
			"queued":  len(w.queue),
		})
		return true
	}

	wait := outboundDropWaitCritical
	severity := "error"
	if outboundMessageIsToolFeedback(msg) {
		wait = outboundDropWaitSecondary
		severity = "warn"
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case w.queue <- msg:
		m.publishOutboundQueued(channel, msg)
		return true
	case <-timer.C:
		err := fmt.Errorf("outbound channel queue full after %s wait (queued=%d)", wait, len(w.queue))
		logAttrs := map[string]any{
			"channel": channel,
			"chat_id": outboundMessageChatID(msg),
			"kind":    strings.TrimSpace(msg.Context.Raw["message_kind"]),
			"queued":  len(w.queue),
			"wait":    wait.String(),
			"error":   err.Error(),
		}
		if severity == "warn" {
			logger.WarnCF("channels", "Outbound tool feedback dropped on full channel queue", logAttrs)
		} else {
			logger.ErrorCF("channels", "Outbound message dropped on full channel queue", logAttrs)
			m.publishOutboundFailed(channel, msg, err, false)
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func dispatchLoop[M any](
	ctx context.Context,
	m *Manager,
	ch <-chan M,
	getChannel func(M) string,
	enqueue func(context.Context, *channelWorker, M) bool,
	startMsg, stopMsg, unknownMsg, noWorkerMsg string,
) {
	logger.InfoC("channels", startMsg)

	for {
		select {
		case <-ctx.Done():
			logger.InfoC("channels", stopMsg)
			return

		case msg, ok := <-ch:
			if !ok {
				logger.InfoC("channels", stopMsg)
				return
			}

			channel := getChannel(msg)

			// Silently skip internal channels
			if constants.IsInternalChannel(channel) {
				continue
			}

			m.mu.RLock()
			_, exists := m.channels[channel]
			w, wExists := m.workers[channel]
			m.mu.RUnlock()

			if !exists {
				logger.WarnCF("channels", unknownMsg, map[string]any{"channel": channel})
				continue
			}

			if wExists && w != nil {
				if !enqueue(ctx, w, msg) {
					return
				}
			} else if exists {
				logger.WarnCF("channels", noWorkerMsg, map[string]any{"channel": channel})
			}
		}
	}
}

func (m *Manager) dispatchOutbound(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.OutboundChan(),
		func(msg bus.OutboundMessage) string { return outboundMessageChannel(msg) },
		m.enqueueOutboundForDispatch,
		"Outbound dispatcher started",
		"Outbound dispatcher stopped",
		"Unknown channel for outbound message",
		"Channel has no active worker, skipping message",
	)
}

func (m *Manager) dispatchOutboundMedia(ctx context.Context) {
	dispatchLoop(
		ctx, m,
		m.bus.OutboundMediaChan(),
		func(msg bus.OutboundMediaMessage) string { return outboundMediaChannel(msg) },
		func(ctx context.Context, w *channelWorker, msg bus.OutboundMediaMessage) bool {
			select {
			case w.mediaQueue <- msg:
				m.publishOutboundMediaQueued(outboundMediaChannel(msg), msg)
				return true
			case <-ctx.Done():
				return false
			}
		},
		"Outbound media dispatcher started",
		"Outbound media dispatcher stopped",
		"Unknown channel for outbound media message",
		"Channel has no active worker, skipping media message",
	)
}

// runMediaWorker processes outbound media messages for a single channel.
func (m *Manager) runMediaWorker(ctx context.Context, name string, w *channelWorker) {
	defer close(w.mediaDone)
	for {
		select {
		case msg, ok := <-w.mediaQueue:
			if !ok {
				return
			}
			_, _ = m.sendMediaWithRetry(ctx, name, w, msg)
		case <-ctx.Done():
			return
		}
	}
}

// sendMediaWithRetry sends a media message through the channel with rate limiting and
// retry logic. It returns the message IDs and nil on success, or nil and the last error
// after retries, including when the channel does not support MediaSender.
func (m *Manager) sendMediaWithRetry(
	ctx context.Context,
	name string,
	w *channelWorker,
	msg bus.OutboundMediaMessage,
) ([]string, error) {
	ms, ok := w.ch.(MediaSender)
	if !ok {
		err := fmt.Errorf("channel %q does not support media sending", name)
		logger.WarnCF("channels", "Channel does not support MediaSender", map[string]any{
			"channel": name,
			"error":   err.Error(),
		})
		return nil, err
	}

	// Rate limit: wait for token
	if err := w.limiter.Wait(ctx); err != nil {
		m.publishChannelEvent(
			runtimeevents.KindChannelRateLimited,
			name,
			scopeFromOutboundContext(msg.Context),
			runtimeevents.SeverityWarn,
			ChannelOutboundPayload{
				Media: true,
				Error: err.Error(),
			},
		)
		return nil, err
	}

	// Pre-send: stop typing and clean up any placeholder before sending media.
	m.preSendMedia(ctx, name, msg, w.ch)

	var lastErr error
	var msgIDs []string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		msgIDs, lastErr = ms.SendMedia(ctx, msg)
		if lastErr == nil {
			m.publishOutboundMediaSent(name, msg, msgIDs)
			return msgIDs, nil
		}

		// Permanent failures — don't retry
		if errors.Is(lastErr, ErrNotRunning) || errors.Is(lastErr, ErrSendFailed) {
			break
		}

		// Last attempt exhausted — don't sleep
		if attempt == maxRetries {
			break
		}

		// Rate limit error — fixed delay
		if errors.Is(lastErr, ErrRateLimit) {
			select {
			case <-time.After(rateLimitDelay):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		// ErrTemporary or unknown error — exponential backoff
		backoff := min(time.Duration(float64(baseBackoff)*math.Pow(2, float64(attempt))), maxBackoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// All retries exhausted or permanent failure
	logger.ErrorCF("channels", "SendMedia failed", map[string]any{
		"channel": name,
		"chat_id": outboundMediaChatID(msg),
		"error":   lastErr.Error(),
		"retries": maxRetries,
	})
	m.publishOutboundMediaFailed(name, msg, lastErr)
	return nil, lastErr
}

// runTTLJanitor periodically scans the typingStops, placeholders, and stream
// tombstone maps and evicts entries that have exceeded their TTL. This prevents
// memory accumulation when outbound paths fail to trigger preSend (e.g. LLM errors).
func (m *Manager) runTTLJanitor(ctx context.Context) {
	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.typingStops.Range(func(key, value any) bool {
				if entry, ok := value.(typingEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.typingStops.LoadAndDelete(key); loaded {
							entry.stop() // idempotent, safe
						}
					}
				}
				return true
			})
			m.reactionUndos.Range(func(key, value any) bool {
				if entry, ok := value.(reactionEntry); ok {
					if now.Sub(entry.createdAt) > typingStopTTL {
						if _, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
							entry.undo() // idempotent, safe
						}
					}
				}
				return true
			})
			m.placeholders.Range(func(key, value any) bool {
				if entry, ok := value.(placeholderEntry); ok {
					if now.Sub(entry.createdAt) > placeholderTTL {
						m.placeholders.Delete(key)
					}
				}
				return true
			})
			m.streamAuxiliaryTombstones.Range(func(key, value any) bool {
				if createdAt, ok := value.(time.Time); !ok || now.Sub(createdAt) > streamAuxiliaryTombstoneTTL {
					m.streamAuxiliaryTombstones.Delete(key)
				}
				return true
			})
		}
	}
}
