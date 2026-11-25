package pico

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

var allowedInlineImageMIMETypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/webp": {},
	"image/bmp":  {},
}

func outboundMessageIsThought(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindThought)
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
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindToolCalls)
}

// outboundMessageIsProgressNote reports whether the message is a non-final
// beat rather than the turn's final answer: an explicit progress_note mark
// (heartbeat / stall-watchdog notice) or the pico-scoped receipt marker
// (approval/ask answer receipts — the model keeps generating behind them).
// Approval/ask CARDS are deliberately NOT marked here: older pico clients
// (the TUI) render progress_note as a status-line-only beat and would drop
// the card from the timeline — the web frontend derives the non-final
// semantics from the ask/approval payload itself instead (review P1-2).
func outboundMessageIsProgressNote(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), MessageKindProgressNote) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["pico_kind"]), MessageKindProgressNote)
}

func outboundMessageFinalizesTrackedToolFeedback(msg bus.OutboundMessage) bool {
	return !outboundMessageIsToolFeedback(msg) &&
		!outboundMessageIsThought(msg) &&
		!outboundMessageIsToolCalls(msg) &&
		!outboundMessageIsProgressNote(msg)
}

func picoDownloadURLForRef(ref string) (string, error) {
	refID, err := picoMediaRefID(ref)
	if err != nil {
		return "", err
	}
	return "/pico/media/" + url.PathEscape(refID), nil
}

func picoMediaRefID(ref string) (string, error) {
	refID := strings.TrimSpace(strings.TrimPrefix(ref, "media://"))
	if refID == "" || strings.Contains(refID, "/") {
		return "", fmt.Errorf("invalid media ref %q", ref)
	}
	return refID, nil
}

func picoInferAttachmentType(filename, contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	filename = strings.ToLower(strings.TrimSpace(filename))

	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	}

	switch ext := filepath.Ext(filename); ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	default:
		return "file"
	}
}

func picoAllowsInlineDisplay(filename, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	filename = strings.ToLower(strings.TrimSpace(filename))

	if strings.Contains(contentType, "svg") || filepath.Ext(filename) == ".svg" {
		return false
	}

	return picoInferAttachmentType(filename, contentType) == "image"
}

// appendFileTags appends [file:<path>] tags to the message content. With an
// empty content the tags alone become the content, keeping the turn valid.
func appendFileTags(content string, tags []string) string {
	for _, tag := range tags {
		content = strings.TrimSpace(content + " " + tag)
	}
	return content
}

// truncate truncates a string to maxLen runes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func parseInlineImageMedia(payload map[string]any) ([]string, error) {
	if len(payload) == 0 {
		return nil, nil
	}

	media, err := parseInlineImageValues(payload["media"])
	if err != nil {
		return nil, err
	}

	attachments, err := parseInlineImageAttachments(payload["attachments"])
	if err != nil {
		return nil, err
	}
	media = append(media, attachments...)

	return media, nil
}

func parseInlineImageValues(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch values := raw.(type) {
	case []any:
		media := make([]string, 0, len(values))
		for i, item := range values {
			value, err := inlineImageValue(item)
			if err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case []string:
		media := make([]string, 0, len(values))
		for i, value := range values {
			value = strings.TrimSpace(value)
			if err := validateInlineImageDataURL(value); err != nil {
				return nil, fmt.Errorf("media[%d]: %w", i, err)
			}
			media = append(media, value)
		}
		return media, nil
	case string:
		value := strings.TrimSpace(values)
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, err
		}
		return []string{value}, nil
	default:
		return nil, fmt.Errorf("media must be a string or array of strings")
	}
}

func parseInlineImageAttachments(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}

	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("attachments must be an array")
	}

	media := make([]string, 0, len(values))
	for i, item := range values {
		attachment, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("attachments[%d]: attachment must be an object", i)
		}

		attachmentType, _ := attachment["type"].(string)
		attachmentType = strings.ToLower(strings.TrimSpace(attachmentType))
		if attachmentType != "" && attachmentType != "image" {
			continue
		}

		value, err := inlineImageValue(attachment)
		if err != nil {
			if attachmentType == "image" {
				return nil, fmt.Errorf("attachments[%d]: %w", i, err)
			}
			continue
		}
		if !strings.HasPrefix(value, "data:") {
			continue
		}
		if err := validateInlineImageDataURL(value); err != nil {
			return nil, fmt.Errorf("attachments[%d]: %w", i, err)
		}
		media = append(media, value)
	}
	return media, nil
}

func inlineImageValue(item any) (string, error) {
	switch value := item.(type) {
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("image payload is empty")
		}
		return value, nil
	case map[string]any:
		for _, key := range []string{"url", "data_url"} {
			if raw, ok := value[key].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw), nil
			}
		}
		return "", fmt.Errorf("image payload must include url or data_url")
	default:
		return "", fmt.Errorf("image payload must be a string or object")
	}
}

func validateInlineImageDataURL(mediaURL string) error {
	if mediaURL == "" {
		return fmt.Errorf("image payload is empty")
	}
	if !strings.HasPrefix(mediaURL, "data:image/") {
		return fmt.Errorf("only inline image data URLs are supported")
	}

	header, data, found := strings.Cut(mediaURL, ",")
	if !found || strings.TrimSpace(data) == "" {
		return fmt.Errorf("image data URL is malformed")
	}
	if !strings.Contains(header, ";base64") {
		return fmt.Errorf("image data URL must be base64 encoded")
	}
	mimeType, _, _ := strings.Cut(strings.TrimPrefix(header, "data:"), ";")
	if _, ok := allowedInlineImageMIMETypes[mimeType]; !ok {
		return fmt.Errorf("unsupported image format: %s", mimeType)
	}

	data = strings.TrimSpace(data)
	if base64.StdEncoding.DecodedLen(len(data)) > config.DefaultMaxMediaSize {
		return fmt.Errorf("image exceeds %d byte limit", config.DefaultMaxMediaSize)
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return fmt.Errorf("invalid base64 image data")
	}

	return nil
}

// setContextUsagePayload adds context window usage stats to a pico payload.
func setContextUsagePayload(payload map[string]any, u *bus.ContextUsage) {
	if u == nil {
		return
	}
	usage := map[string]any{
		"used_tokens":         u.UsedTokens,
		"total_tokens":        u.TotalTokens,
		"history_tokens":      u.HistoryTokens,
		"compress_at_tokens":  u.CompressAtTokens,
		"summarize_at_tokens": u.SummarizeAtTokens,
		"used_percent":        u.UsedPercent,
	}
	// Segments (octop borrowing §五): the web ring's stacked breakdown reads
	// context_usage.segments — dropping them here left the whole feature dead
	// on the wire (review P1-6).
	if len(u.Segments) > 0 {
		segments := make([]map[string]any, 0, len(u.Segments))
		for _, seg := range u.Segments {
			segments = append(segments, map[string]any{
				"key":    seg.Key,
				"tokens": seg.Tokens,
			})
		}
		usage["segments"] = segments
	}
	payload["context_usage"] = usage
}

// setTurnUsagePayload attaches real per-turn LLM token usage to the payload.
// Input and output are kept separate (billed at different rates); total is a
// convenience sum; llm_calls is the per-turn API call count for the web status
// bar; cached_tokens is the prompt-cache hit count inside input_tokens
// (omitted when the upstream reports none). Omitted entirely when all counts
// are zero.
func setTurnUsagePayload(payload map[string]any, usage providers.UsageInfo, llmCalls int) {
	if usage.PromptTokens <= 0 && usage.CompletionTokens <= 0 && llmCalls <= 0 {
		return
	}
	u := map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
		"total_tokens":  usage.PromptTokens + usage.CompletionTokens,
	}
	if cached := usage.CachedTokens(); cached > 0 {
		u["cached_tokens"] = cached
	}
	if llmCalls > 0 {
		u["llm_calls"] = llmCalls
	}
	payload[PayloadKeyUsage] = u
}

func picoToolCallsPayload(msg bus.OutboundMessage) ([]utils.VisibleToolCall, bool) {
	raw := strings.TrimSpace(msg.Context.Raw[PayloadKeyToolCalls])
	if raw == "" {
		return nil, false
	}

	var toolCalls []utils.VisibleToolCall
	if err := json.Unmarshal([]byte(raw), &toolCalls); err != nil || len(toolCalls) == 0 {
		return nil, false
	}
	return toolCalls, true
}
