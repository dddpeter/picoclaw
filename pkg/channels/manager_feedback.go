// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type toolFeedbackMessageTracker interface {
	RecordToolFeedbackMessage(chatID, messageID, content string)
	ClearToolFeedbackMessage(chatID string)
}

type toolFeedbackMessageCleaner interface {
	DismissToolFeedbackMessage(ctx context.Context, chatID string)
}

type toolFeedbackMessageTargetResolver interface {
	ToolFeedbackMessageChatID(chatID string, outboundCtx *bus.InboundContext) string
}

type toolFeedbackMessageContentPreparer interface {
	PrepareToolFeedbackMessageContent(content string) string
}

func trackedToolFeedbackMessageChatID(ch Channel, chatID string, outboundCtx *bus.InboundContext) string {
	if resolver, ok := ch.(toolFeedbackMessageTargetResolver); ok {
		if resolved := strings.TrimSpace(resolver.ToolFeedbackMessageChatID(chatID, outboundCtx)); resolved != "" {
			return resolved
		}
	}
	return strings.TrimSpace(chatID)
}

func dismissTrackedToolFeedbackMessage(
	ctx context.Context,
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
) {
	trackedChatID := trackedToolFeedbackMessageChatID(ch, chatID, outboundCtx)
	if trackedChatID == "" {
		return
	}
	if cleaner, ok := ch.(toolFeedbackMessageCleaner); ok {
		cleaner.DismissToolFeedbackMessage(ctx, trackedChatID)
		return
	}
	if tracker, ok := ch.(toolFeedbackMessageTracker); ok {
		tracker.ClearToolFeedbackMessage(trackedChatID)
	}
}

func clearTrackedToolFeedbackMessage(
	ch Channel,
	chatID string,
	outboundCtx *bus.InboundContext,
) {
	trackedChatID := trackedToolFeedbackMessageChatID(ch, chatID, outboundCtx)
	if trackedChatID == "" {
		return
	}
	if tracker, ok := ch.(toolFeedbackMessageTracker); ok {
		tracker.ClearToolFeedbackMessage(trackedChatID)
	}
}

// DismissToolFeedback clears any tracked tool feedback animation for the
// given channel/chat. This is called when a turn ends without a final
// response (e.g., ResponseHandled tools) to stop orphaned animation goroutines.
// outboundCtx carries topic/thread info for channels that use scoped tracker
// keys (e.g., Telegram forum topics); may be nil for non-topic channels.
func (m *Manager) DismissToolFeedback(
	ctx context.Context, channelName, chatID string, outboundCtx *bus.InboundContext,
) {
	ch, ok := m.GetChannel(channelName)
	if !ok {
		return
	}
	dismissTrackedToolFeedbackMessage(ctx, ch, chatID, outboundCtx)
}

func prepareToolFeedbackMessageContent(ch Channel, content string) string {
	prepared := strings.TrimSpace(content)
	if prepared == "" {
		return ""
	}
	if preparer, ok := ch.(toolFeedbackMessageContentPreparer); ok {
		if candidate := strings.TrimSpace(preparer.PrepareToolFeedbackMessageContent(prepared)); candidate != "" {
			return candidate
		}
	}
	return prepared
}

func (m *Manager) toolFeedbackSeparateMessagesEnabled() bool {
	if m == nil || m.config == nil {
		return false
	}
	return m.config.Agents.Defaults.IsToolFeedbackSeparateMessagesEnabled()
}

// RecordPlaceholder registers a placeholder message for later editing.
// Implements PlaceholderRecorder.
func (m *Manager) RecordPlaceholder(channel, chatID, placeholderID string) {
	key := channel + ":" + chatID
	m.placeholders.Store(key, placeholderEntry{id: placeholderID, createdAt: time.Now()})
}

// SendPlaceholder sends a "Thinking…" placeholder for the given channel/chatID
// and records it for later editing. Returns true if a placeholder was sent.
func (m *Manager) SendPlaceholder(ctx context.Context, channel, chatID string) bool {
	m.mu.RLock()
	ch, ok := m.channels[channel]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	pc, ok := ch.(PlaceholderCapable)
	if !ok {
		return false
	}
	phID, err := pc.SendPlaceholder(ctx, chatID)
	if err != nil || phID == "" {
		return false
	}
	m.RecordPlaceholder(channel, chatID, phID)
	return true
}

// RecordTypingStop registers a typing stop function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordTypingStop(channel, chatID string, stop func()) {
	key := channel + ":" + chatID
	entry := typingEntry{stop: stop, createdAt: time.Now()}
	if previous, loaded := m.typingStops.Swap(key, entry); loaded {
		if oldEntry, ok := previous.(typingEntry); ok && oldEntry.stop != nil {
			oldEntry.stop()
		}
	}
}

// InvokeTypingStop invokes the registered typing stop function for the given channel and chatID.
// It is safe to call even when no typing indicator is active (no-op).
// Used by the agent loop to stop typing when processing completes (success, error, or panic),
// regardless of whether an outbound message is published.
func (m *Manager) InvokeTypingStop(channel, chatID string) {
	key := channel + ":" + chatID
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop()
		}
	}
}

// RecordReactionUndo registers a reaction undo function for later invocation.
// Implements PlaceholderRecorder.
func (m *Manager) RecordReactionUndo(channel, chatID string, undo func()) {
	key := channel + ":" + chatID
	m.reactionUndos.Store(key, reactionEntry{undo: undo, createdAt: time.Now()})
}

// preSend handles typing stop, reaction undo, and placeholder editing before sending a message.
// Returns the delivered message IDs and true when delivery completed before a normal Send.
func (m *Manager) preSend(ctx context.Context, name string, msg bus.OutboundMessage, ch Channel) ([]string, bool) {
	chatID := outboundMessageChatID(msg)
	key := name + ":" + chatID
	streamKey := streamSuppressionKey(name, chatID, msg.SessionKey)

	// 1. Stop typing
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop() // idempotent, safe
		}
	}

	// 2. Undo reaction
	if v, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo() // idempotent, safe
		}
	}

	isToolFeedback := outboundMessageIsToolFeedback(msg)
	isToolCalls := outboundMessageIsToolCalls(msg)
	isAuxiliaryMessage := outboundMessageHasAuxiliaryKind(msg)
	isFinalMessage := outboundMessageIsFinal(msg)
	separateToolFeedbackMessages := m.toolFeedbackSeparateMessagesEnabled()

	// 3. If a stream already finalized this chat, stale auxiliary messages must
	// be dropped without consuming the final-response marker. Streaming
	// finalization bypasses the worker queue, so older queued feedback/thoughts
	// can arrive before the normal final outbound message that cleans up the
	// marker and placeholder.
	// Note: tool_calls messages must NOT be dropped as they represent new tool
	// invocations for the current turn that must be delivered to the UI.
	if isAuxiliaryMessage && !isToolCalls {
		if _, loaded := m.streamActive.Load(streamKey); loaded {
			return nil, true
		}
		if m.streamAuxiliaryTombstoneActive(streamKey) {
			return nil, true
		}
	}

	// 4. If a stream already finalized this turn, skip only the duplicate final
	// outbound. Earlier queued visible messages must still be delivered.
	if isFinalMessage {
		if _, loaded := m.streamActive.LoadAndDelete(streamKey); loaded {
			if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
				if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
					// Prefer deleting the placeholder (cleaner UX than editing to same content)
					if deleter, ok := ch.(MessageDeleter); ok {
						deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
					} else if editor, ok := ch.(MessageEditor); ok {
						if payloadEditor, ok := ch.(MessageEditorWithPayload); ok {
							_ = payloadEditor.EditMessageWithPayload(
								ctx,
								chatID,
								entry.id,
								outboundMessageEditPayload(msg, msg.Content),
							)
						} else {
							editor.EditMessage(ctx, chatID, entry.id, msg.Content) // fallback
						}
					}
				}
			}
			if !isToolFeedback {
				if separateToolFeedbackMessages {
					clearTrackedToolFeedbackMessage(ch, chatID, &msg.Context)
				} else {
					dismissTrackedToolFeedbackMessage(ctx, ch, chatID, &msg.Context)
				}
			}
			return nil, true
		}
	}

	if _, loaded := m.streamActive.Load(streamKey); loaded {
		return nil, false
	}
	if m.streamActiveForChat(name, chatID) {
		return nil, false
	}

	if !isAuxiliaryMessage {
		m.streamAuxiliaryTombstones.Delete(streamKey)
	}

	if separateToolFeedbackMessages {
		clearTrackedToolFeedbackMessage(ch, chatID, &msg.Context)
	}

	// 5. Try editing placeholder
	if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
		if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
			if isToolFeedback && separateToolFeedbackMessages {
				if deleter, ok := ch.(MessageDeleter); ok {
					deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
				}
				return nil, false
			}
			if outboundMessageBypassesPlaceholderEdit(msg) {
				if deleter, ok := ch.(MessageDeleter); ok {
					deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
				}
				return nil, false
			}
			if editor, ok := ch.(MessageEditor); ok {
				content := msg.Content
				trackedContent := msg.Content
				if isToolFeedback {
					trackedContent = prepareToolFeedbackMessageContent(ch, msg.Content)
					content = InitialAnimatedToolFeedbackContent(trackedContent)
				}
				err := func() error {
					if payloadEditor, ok := ch.(MessageEditorWithPayload); ok {
						return payloadEditor.EditMessageWithPayload(
							ctx,
							chatID,
							entry.id,
							outboundMessageEditPayload(msg, content),
						)
					}
					return editor.EditMessage(ctx, chatID, entry.id, content)
				}()
				if err == nil {
					trackedChatID := trackedToolFeedbackMessageChatID(ch, chatID, &msg.Context)
					if tracker, ok := ch.(toolFeedbackMessageTracker); ok && isToolFeedback {
						tracker.RecordToolFeedbackMessage(trackedChatID, entry.id, trackedContent)
					} else if !isToolFeedback {
						dismissTrackedToolFeedbackMessage(ctx, ch, chatID, &msg.Context)
					}
					return []string{entry.id}, true
				}
				// edit failed → fall through to normal Send
			}
		}
	}

	return nil, false
}

// preSendMedia handles typing stop, reaction undo, and placeholder cleanup
// before sending media attachments. Unlike preSend for text messages, media
// delivery never edits the placeholder because there is no text payload to
// replace it with; it only attempts to delete the placeholder when possible.
func (m *Manager) preSendMedia(ctx context.Context, name string, msg bus.OutboundMediaMessage, ch Channel) {
	chatID := outboundMediaChatID(msg)
	key := name + ":" + chatID
	streamKey := streamSuppressionKey(name, chatID, msg.SessionKey)

	// 1. Stop typing
	if v, loaded := m.typingStops.LoadAndDelete(key); loaded {
		if entry, ok := v.(typingEntry); ok {
			entry.stop() // idempotent, safe
		}
	}

	// 2. Undo reaction
	if v, loaded := m.reactionUndos.LoadAndDelete(key); loaded {
		if entry, ok := v.(reactionEntry); ok {
			entry.undo() // idempotent, safe
		}
	}

	// 3. Clear any finalized stream markers for this chat before media delivery.
	m.streamActive.LoadAndDelete(streamKey)
	m.streamAuxiliaryTombstones.Delete(streamKey)

	if m.toolFeedbackSeparateMessagesEnabled() {
		clearTrackedToolFeedbackMessage(ch, chatID, &msg.Context)
	}

	// 4. Delete placeholder if present.
	if v, loaded := m.placeholders.LoadAndDelete(key); loaded {
		if entry, ok := v.(placeholderEntry); ok && entry.id != "" {
			if deleter, ok := ch.(MessageDeleter); ok {
				deleter.DeleteMessage(ctx, chatID, entry.id) // best effort
			}
		}
	}
}
