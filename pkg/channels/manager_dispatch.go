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
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

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
