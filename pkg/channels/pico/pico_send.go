package pico

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Send implements Channel — sends a message to the appropriate WebSocket connection.
func (c *PicoChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	isThought := outboundMessageIsThought(msg)
	isToolFeedback := outboundMessageIsToolFeedback(msg)
	isToolCalls := outboundMessageIsToolCalls(msg)
	isProgressNote := outboundMessageIsProgressNote(msg)
	if isToolFeedback {
		if msgID, handled, err := c.progress.Update(ctx, msg.ChatID, msg.Content); handled {
			if err != nil {
				return nil, err
			}
			return []string{msgID}, nil
		}
	}
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(msg.ChatID)
	// NOTE: an A2UI card message counts as "finalizes tracked tool feedback"
	// (it is a plain message), so a live tracked feedback would swallow it
	// here — FinalizeToolFeedbackMessage edits the feedback message with
	// content only and returns before the a2ui block below. Unreachable
	// today: the agent never publishes pre-execution tool feedback on the
	// pico channel (publishToolFeedback guards `ts.channel != "pico"`), so
	// hasTrackedMsg is always false for pico sends; the tracker exists only
	// for the shared animator interfaces.
	if outboundMessageFinalizesTrackedToolFeedback(msg) {
		if msgIDs, handled := c.FinalizeToolFeedbackMessage(ctx, msg); handled {
			return msgIDs, nil
		}
	}

	content := msg.Content
	if isToolFeedback {
		content = channels.InitialAnimatedToolFeedbackContent(msg.Content)
	}
	msgID := uuid.New().String()

	payload := map[string]any{
		PayloadKeyContent: content,
		"message_id":      msgID,
	}
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		payload[PayloadKeyModelName] = modelName
	}
	switch {
	case isThought:
		payload[PayloadKeyKind] = MessageKindThought

		// This field is kept solely for compatibility with legacy pico clients that
		// do not yet support the newer "kind" field.
		// DO NOT use it for any purpose other than legacy client compatibility.
		payload[PayloadKeyThought] = true

	case isToolCalls:
		payload[PayloadKeyKind] = MessageKindToolCalls
		if toolCalls, ok := picoToolCallsPayload(msg); ok {
			payload[PayloadKeyToolCalls] = toolCalls
		}

	case isProgressNote:
		// Non-final progress beat: mark it so clients (web UI) do not
		// mistake it for the turn's final answer and clear their
		// generating state early.
		payload[PayloadKeyKind] = MessageKindProgressNote
	}
	if msg.Approval != nil {
		payload[PayloadKeyApproval] = map[string]any{
			"tool":       msg.Approval.Tool,
			"preview":    msg.Approval.Preview,
			"timeout_ms": msg.Approval.TimeoutMs,
		}
	}
	if msg.Ask != nil {
		questions := make([]any, 0, len(msg.Ask.Questions))
		for _, q := range msg.Ask.Questions {
			opts := make([]any, 0, len(q.Options))
			for _, o := range q.Options {
				opts = append(opts, map[string]any{"label": o.Label, "description": o.Description})
			}
			questions = append(questions, map[string]any{
				"question":     q.Question,
				"header":       q.Header,
				"options":      opts,
				"multi_select": q.MultiSelect,
			})
		}
		payload[PayloadKeyAsk] = map[string]any{
			"questions":  questions,
			"timeout_ms": msg.Ask.TimeoutMs,
		}
	}
	var uiFrames []any
	uiSurfaceID := ""
	if msg.UI != nil {
		// A2UI pass-through (A2UI web render): the payload map is
		// map[string]any, so decode the raw message array into a generic
		// value first. The Go side never interprets the structure; the
		// frontend zod layer absorbs schema evolution.
		if err := json.Unmarshal(msg.UI.Messages, &uiFrames); err == nil {
			payload[PayloadKeyA2UI] = uiFrames
			uiSurfaceID = strings.TrimSpace(msg.UI.SurfaceID)
		}
	}
	setContextUsagePayload(payload, msg.ContextUsage)
	c.rememberUsage(msg.ChatID, msg.ContextUsage)

	// A2UI streamed update (phase 2, task 5): an update-only batch (no
	// createSurface) targets a card this channel already broadcast — merge
	// its frames into the registered card's accumulated frames and
	// hot-update that exact message via message.update (the frontend swaps
	// the card data in place) instead of orphaning a second card.
	if uiSurfaceID != "" && len(uiFrames) > 0 && !batchHasCreateSurface(uiFrames) {
		if state, ok := c.a2uiSurfaces.lookup(msg.ChatID, uiSurfaceID); ok {
			merged := mergeA2UIFrames(state.frames, uiFrames)
			// The card caption stays the create call's content (matches
			// the history fold pinning the caption on the establishing
			// call); editMessagePayload would otherwise blank it.
			updatePayload := map[string]any{
				PayloadKeyContent: state.content,
				PayloadKeyA2UI:    merged,
			}
			if err := c.editMessagePayload(ctx, msg.ChatID, state.messageID, updatePayload, nil); err != nil &&
				!errors.Is(err, errFrameRetainedNoDelivery) {
				// Real failure — the frame never entered the session seq
				// ring, so no client will ever receive it. Keep the
				// pre-merge registry frames so the delta stays retryable
				// instead of being absorbed invisibly.
				return nil, err
			}
			// The frame is in the seq ring regardless of live delivery: a
			// zero-connection send is replayed on reconnect (trae borrowing
			// §一), so commit the merged frames to keep the registry and
			// the replayed state in agreement.
			c.a2uiSurfaces.record(msg.ChatID, uiSurfaceID, state.messageID, state.content, merged)
			return []string{state.messageID}, nil
		}
		// Unknown surfaceId (registry miss — e.g. after a gateway restart
		// cleared it): an orphan update-only batch can NOT become a card.
		// The batch carries no createSurface frame, so the frontend A2UI
		// message processor rejects every frame with "Surface not found"
		// and renders a permanent error placeholder — and registering it
		// would only chain further updates into that dead card. Align with
		// the history-fold orphan semantics instead: publish the fallback
		// text only (no a2ui key) and leave the surface unregistered, so a
		// later create batch starts a clean card.
		delete(payload, PayloadKeyA2UI)
	}

	outMsg := newMessage(TypeMessageCreate, payload)

	if err := c.broadcastToSession(msg.ChatID, outMsg); err != nil {
		retained := errors.Is(err, errFrameRetainedNoDelivery)
		if !retained || uiSurfaceID == "" {
			// Real failure (frame never entered the seq ring), or a
			// non-A2UI message: surface the error unchanged.
			return nil, err
		}
		// Ring-retained A2UI create: a reconnecting client replays the
		// frame, so registration must commit to stay consistent with the
		// replayed state — and Send reports success, because the publish
		// did land (in the ring); reporting failure would make render_ui
		// re-publish and duplicate the card.
	}

	// Register the surface once the create frame is committed to the seq
	// ring (delivered live or retained for replay) so a never-sent frame
	// never leaves an entry pointing at a message nobody received. Orphan
	// update-only batches dropped the a2ui key above — they publish
	// fallback text only and stay unregistered.
	if uiSurfaceID != "" && len(uiFrames) > 0 && payload[PayloadKeyA2UI] != nil {
		c.a2uiSurfaces.record(msg.ChatID, uiSurfaceID, msgID, content, uiFrames)
	}
	if isToolFeedback {
		c.RecordToolFeedbackMessage(msg.ChatID, msgID, msg.Content)
	} else if hasTrackedMsg && outboundMessageFinalizesTrackedToolFeedback(msg) {
		c.dismissTrackedToolFeedbackMessage(ctx, msg.ChatID, trackedMsgID)
	}
	return []string{msgID}, nil
}

// EditMessage implements channels.MessageEditor.
func (c *PicoChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	return c.editMessage(ctx, chatID, messageID, content, nil)
}

func (c *PicoChannel) EditMessageWithPayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, payload, nil)
}

// DeleteMessage implements channels.MessageDeleter.
func (c *PicoChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	outMsg := newMessage(TypeMessageDelete, map[string]any{
		"message_id": messageID,
	})
	return c.broadcastToSession(chatID, outMsg)
}

// BroadcastRevert reports a completed /revert workspace restore to every
// connection of the session (trae borrowing 增量清单 §2.1). Session-level
// broadcast: the frame carries a seq and enters the replay ring, so a client
// that refreshes after the revert still learns the workspace changed.
func (c *PicoChannel) BroadcastRevert(chatID string, payload map[string]any) error {
	outMsg := newMessage(TypeRevertStatus, payload)
	return c.broadcastToSession(chatID, outMsg)
}

func (c *PicoChannel) currentToolFeedbackMessage(chatID string) (string, bool) {
	if c.progress == nil {
		return "", false
	}
	return c.progress.Current(chatID)
}

func (c *PicoChannel) takeToolFeedbackMessage(chatID string) (string, string, bool) {
	if c.progress == nil {
		return "", "", false
	}
	return c.progress.Take(chatID)
}

func (c *PicoChannel) RecordToolFeedbackMessage(chatID, messageID, content string) {
	if c.progress == nil {
		return
	}
	c.progress.Record(chatID, messageID, content)
}

func (c *PicoChannel) ClearToolFeedbackMessage(chatID string) {
	if c.progress == nil {
		return
	}
	c.progress.Clear(chatID)
}

func (c *PicoChannel) DismissToolFeedbackMessage(ctx context.Context, chatID string) {
	msgID, ok := c.currentToolFeedbackMessage(chatID)
	if !ok {
		return
	}
	c.dismissTrackedToolFeedbackMessage(ctx, chatID, msgID)
}

func (c *PicoChannel) dismissTrackedToolFeedbackMessage(ctx context.Context, chatID, messageID string) {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(messageID) == "" {
		return
	}
	c.ClearToolFeedbackMessage(chatID)
	deleteFn := c.deleteMessageFn
	if deleteFn == nil {
		deleteFn = c.DeleteMessage
	}
	_ = deleteFn(ctx, chatID, messageID)
}

func (c *PicoChannel) finalizeTrackedToolFeedbackMessage(
	ctx context.Context,
	chatID string,
	content string,
	editFn func(context.Context, string, string, map[string]any, *bus.ContextUsage) error,
	payload map[string]any,
	contextUsage *bus.ContextUsage,
) ([]string, bool) {
	msgID, baseContent, ok := c.takeToolFeedbackMessage(chatID)
	if !ok || editFn == nil {
		return nil, false
	}
	if payload == nil {
		payload = map[string]any{
			PayloadKeyContent: content,
		}
	}
	if _, ok := payload[PayloadKeyContent]; !ok {
		payload[PayloadKeyContent] = content
	}
	if err := editFn(ctx, chatID, msgID, payload, contextUsage); err != nil {
		c.RecordToolFeedbackMessage(chatID, msgID, baseContent)
		return nil, false
	}
	return []string{msgID}, true
}

func (c *PicoChannel) FinalizeToolFeedbackMessage(ctx context.Context, msg bus.OutboundMessage) ([]string, bool) {
	if !outboundMessageFinalizesTrackedToolFeedback(msg) {
		return nil, false
	}
	payload := map[string]any{
		PayloadKeyContent: msg.Content,
	}
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		payload[PayloadKeyModelName] = modelName
	}
	return c.finalizeTrackedToolFeedbackMessage(
		ctx,
		msg.ChatID,
		msg.Content,
		c.editMessagePayload,
		payload,
		msg.ContextUsage,
	)
}

// StartTyping implements channels.TypingCapable.
func (c *PicoChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	startMsg := newMessage(TypeTypingStart, nil)
	if err := c.broadcastToSession(chatID, startMsg); err != nil {
		return func() {}, err
	}
	return func() {
		stopMsg := newMessage(TypeTypingStop, nil)
		c.broadcastToSession(chatID, stopMsg)
	}, nil
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message via the Pico Protocol that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *PicoChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	if !c.bc.Placeholder.Enabled {
		return "", nil
	}

	text := c.bc.Placeholder.GetRandomText()

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		PayloadKeyContent:     text,
		PayloadKeyPlaceholder: true,
		"message_id":          msgID,
	})

	if err := c.broadcastToSession(chatID, outMsg); err != nil {
		return "", err
	}

	return msgID, nil
}

// BeginStream implements channels.StreamingCapable for Pico WebUI.
func (c *PicoChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if c == nil || c.config == nil || !c.config.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	streamCfg := c.config.Streaming.WithDefaults(0, 1)
	return &picoStreamer{
		channel:          c,
		chatID:           chatID,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}

// SendMedia implements channels.MediaSender for the Pico web UI.
// Media is delivered as a normal assistant message carrying structured
// attachments plus an authenticated same-origin download URL.
func (c *PicoChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(msg.ChatID)

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	attachments := make([]map[string]any, 0, len(msg.Parts))
	caption := ""

	for _, part := range msg.Parts {
		localPath, meta, err := store.ResolveWithMeta(part.Ref)
		if err != nil {
			logger.ErrorCF("pico", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		filename := strings.TrimSpace(part.Filename)
		if filename == "" {
			filename = strings.TrimSpace(meta.Filename)
		}
		if filename == "" {
			filename = filepath.Base(localPath)
		}

		contentType := strings.TrimSpace(part.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(meta.ContentType)
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		attachmentType := strings.TrimSpace(part.Type)
		if attachmentType == "" {
			attachmentType = picoInferAttachmentType(filename, contentType)
		}

		attachmentURL, err := picoDownloadURLForRef(part.Ref)
		if err != nil {
			logger.ErrorCF("pico", "Failed to build media download URL", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		attachments = append(attachments, map[string]any{
			"type":         attachmentType,
			"url":          attachmentURL,
			"filename":     filename,
			"content_type": contentType,
			"path":         localPath,
		})

		if caption == "" && strings.TrimSpace(part.Caption) != "" {
			caption = strings.TrimSpace(part.Caption)
		}
	}

	if len(attachments) == 0 {
		return nil, fmt.Errorf("no deliverable media parts: %w", channels.ErrSendFailed)
	}

	msgID := uuid.New().String()
	outMsg := newMessage(TypeMessageCreate, map[string]any{
		PayloadKeyContent: caption,
		"attachments":     attachments,
		"message_id":      msgID,
	})
	if modelName := strings.TrimSpace(msg.Context.Raw[PayloadKeyModelName]); modelName != "" {
		outMsg.Payload[PayloadKeyModelName] = modelName
	}

	if err := c.broadcastToSession(msg.ChatID, outMsg); err != nil {
		return nil, err
	}
	if hasTrackedMsg {
		c.dismissTrackedToolFeedbackMessage(ctx, msg.ChatID, trackedMsgID)
	}

	return []string{msgID}, nil
}

func (c *PicoChannel) editMessage(
	ctx context.Context,
	chatID string,
	messageID string,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	return c.editMessagePayload(ctx, chatID, messageID, map[string]any{
		PayloadKeyContent: content,
	}, contextUsage)
}

func (c *PicoChannel) editMessagePayload(
	ctx context.Context,
	chatID string,
	messageID string,
	payload map[string]any,
	contextUsage *bus.ContextUsage,
) error {
	if payload == nil {
		payload = map[string]any{}
	}
	normalized := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		normalized[key] = value
	}
	if _, ok := normalized[PayloadKeyContent]; !ok {
		normalized[PayloadKeyContent] = ""
	}
	normalized["message_id"] = messageID
	setContextUsagePayload(normalized, contextUsage)
	c.rememberUsage(chatID, contextUsage)
	outMsg := newMessage(TypeMessageUpdate, normalized)
	return c.broadcastToSession(chatID, outMsg)
}
