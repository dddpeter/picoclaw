// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
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
