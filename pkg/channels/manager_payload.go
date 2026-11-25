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

// typingEntry wraps a typing stop function with a creation timestamp for TTL eviction.
type typingEntry struct {
	stop      func()
	createdAt time.Time
}

// reactionEntry wraps a reaction undo function with a creation timestamp for TTL eviction.
type reactionEntry struct {
	undo      func()
	createdAt time.Time
}

// placeholderEntry wraps a placeholder ID with a creation timestamp for TTL eviction.
type placeholderEntry struct {
	id        string
	createdAt time.Time
}

// channelRateConfig maps channel name to per-second rate limit.
var channelRateConfig = map[string]float64{
	"telegram": 20,
	"discord":  1,
	"slack":    1,
	"matrix":   2,
	"line":     10,
	"qq":       5,
	"irc":      2,
}

type asyncTask struct {
	cancel context.CancelFunc
}

func outboundMessageChannel(msg bus.OutboundMessage) string {
	return msg.Context.Channel
}

func outboundMessageChatID(msg bus.OutboundMessage) string {
	return msg.ChatID
}

func outboundMessageIsToolFeedback(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), "tool_feedback")
}

func outboundMessageIsToolCalls(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), "tool_calls")
}

func outboundMessageHasAuxiliaryKind(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.TrimSpace(msg.Context.Raw["message_kind"]) != ""
}

func outboundMessageIsFinal(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["outbound_kind"]), "final")
}

func outboundMessageBypassesPlaceholderEdit(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	kind := strings.TrimSpace(msg.Context.Raw["message_kind"])
	return strings.EqualFold(kind, "thought") || strings.EqualFold(kind, "tool_calls")
}

func outboundMessageEditPayload(msg bus.OutboundMessage, content string) map[string]any {
	payload := map[string]any{
		"content": content,
	}
	if len(msg.Context.Raw) == 0 {
		return payload
	}
	if modelName := strings.TrimSpace(msg.Context.Raw["model_name"]); modelName != "" {
		payload["model_name"] = modelName
	}
	return payload
}

func outboundMediaChannel(msg bus.OutboundMediaMessage) string {
	return msg.Context.Channel
}

func outboundMediaChatID(msg bus.OutboundMediaMessage) string {
	return msg.ChatID
}

func streamSuppressionKey(channel, chatID, sessionKey string) string {
	key := channel + ":" + chatID
	if strings.TrimSpace(sessionKey) == "" {
		return key
	}
	return key + ":" + sessionKey
}
