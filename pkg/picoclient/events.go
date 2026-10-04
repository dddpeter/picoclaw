// Package picoclient implements a Go client for the Pico Protocol WebSocket
// surface served by the picoclaw gateway at /pico/ws. It is transport-only:
// Decode is stateless, Client handles dial/auth/reconnect, and turn/session
// semantics stay with the caller (see docs/design/tui-client-design.zh.md).
package picoclient

import (
	"encoding/json"
	"strings"

	pico "github.com/sipeed/picoclaw/pkg/channels/pico"
)

// Answer message kinds carried in payload["kind"]; empty means a plain answer
// message (streaming or final).
const (
	KindThought      = pico.MessageKindThought
	KindToolCalls    = pico.MessageKindToolCalls
	KindProgressNote = pico.MessageKindProgressNote
	KindPlain        = ""
)

// ToolCall is one decoded entry of a kind=tool_calls payload.
type ToolCall struct {
	ID          string
	Name        string
	Arguments   string
	Explanation string
}

// Attachment is one decoded entry of an attachments payload.
type Attachment struct {
	Type        string
	URL         string
	Filename    string
	ContentType string
}

// ContextUsage mirrors the context_usage payload block.
type ContextUsage struct {
	UsedTokens        int
	TotalTokens       int
	HistoryTokens     int
	CompressAtTokens  int
	SummarizeAtTokens int
	UsedPercent       float64
}

// TurnUsage mirrors the per-turn usage payload block.
type TurnUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Event is a decoded inbound Pico Protocol message. Fields are populated by
// message kind; Error/RequestID only on TypeError, Code/ErrMessage describing
// the server-reported failure.
type Event struct {
	Type      string
	SessionID string
	Timestamp int64

	MessageID   string
	Content     string
	Kind        string
	Placeholder bool
	ModelName   string
	ToolCalls   []ToolCall
	Attachments []Attachment

	ContextUsage *ContextUsage
	Usage        *TurnUsage

	Code       string
	ErrMessage string
	RequestID  string
}

// IsThought reports whether the event carries a reasoning-stream message.
func (e Event) IsThought() bool { return strings.EqualFold(e.Kind, KindThought) }

// IsToolCalls reports whether the event carries the tool-call plan message.
func (e Event) IsToolCalls() bool { return strings.EqualFold(e.Kind, KindToolCalls) }

// IsProgressNote reports whether the event is a non-final progress beat.
// Clients must not treat it as the turn's final answer (fork semantics, see
// pkg/channels/pico protocol.go).
func (e Event) IsProgressNote() bool { return strings.EqualFold(e.Kind, KindProgressNote) }

// IsToolFeedback reports whether the content matches the animated tool
// feedback format emitted by the gateway (🔧 prefix, see
// channels.InitialAnimatedToolFeedbackContent).
func (e Event) IsToolFeedback() bool {
	return strings.HasPrefix(strings.TrimSpace(e.Content), "🔧")
}

// IsAnswer reports whether the event is a plain answer message (streaming or
// final): no kind, not a tool-feedback line, not a placeholder.
func (e Event) IsAnswer() bool {
	return e.Kind == KindPlain && !e.IsToolFeedback() && !e.Placeholder
}

// Decode maps a raw Pico Protocol message onto Event. Unknown payload fields
// are ignored; undecodable field types fall back to zero values so that a
// single malformed payload never panics the consumer.
func Decode(msg pico.PicoMessage) Event {
	ev := Event{
		Type:      msg.Type,
		SessionID: msg.SessionID,
		Timestamp: msg.Timestamp,
	}
	p := msg.Payload
	if p == nil {
		return ev
	}

	ev.MessageID = str(p, "message_id")
	ev.Content = str(p, pico.PayloadKeyContent)
	ev.Kind = str(p, pico.PayloadKeyKind)
	ev.Placeholder, _ = p[pico.PayloadKeyPlaceholder].(bool)
	ev.ModelName = strings.TrimSpace(str(p, pico.PayloadKeyModelName))

	// Legacy servers mark thoughts with a boolean payload field instead of kind.
	if ev.Kind == KindPlain {
		if thought, ok := p[pico.PayloadKeyThought].(bool); ok && thought {
			ev.Kind = KindThought
		}
	}

	if raw, ok := p[pico.PayloadKeyToolCalls].([]any); ok {
		ev.ToolCalls = decodeToolCalls(raw)
	}
	if raw, ok := p["attachments"].([]any); ok {
		ev.Attachments = decodeAttachments(raw)
	}
	if raw, ok := p["context_usage"].(map[string]any); ok {
		ev.ContextUsage = &ContextUsage{
			UsedTokens:        integer(raw, "used_tokens"),
			TotalTokens:       integer(raw, "total_tokens"),
			HistoryTokens:     integer(raw, "history_tokens"),
			CompressAtTokens:  integer(raw, "compress_at_tokens"),
			SummarizeAtTokens: integer(raw, "summarize_at_tokens"),
			UsedPercent:       number(raw, "used_percent"),
		}
	}
	if raw, ok := p[pico.PayloadKeyUsage].(map[string]any); ok {
		ev.Usage = &TurnUsage{
			InputTokens:  integer(raw, "input_tokens"),
			OutputTokens: integer(raw, "output_tokens"),
			TotalTokens:  integer(raw, "total_tokens"),
		}
	}

	ev.Code = str(p, "code")
	ev.ErrMessage = str(p, "message")
	ev.RequestID = str(p, "request_id")
	return ev
}

func decodeToolCalls(raw []any) []ToolCall {
	out := make([]ToolCall, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		call := ToolCall{
			ID: str(obj, "id"),
		}
		if fn, ok := obj["function"].(map[string]any); ok {
			call.Name = str(fn, "name")
			call.Arguments = str(fn, "arguments")
		}
		if extra, ok := obj["extra_content"].(map[string]any); ok {
			call.Explanation = str(extra, "tool_feedback_explanation")
		}
		if call.ID != "" || call.Name != "" || call.Arguments != "" || call.Explanation != "" {
			out = append(out, call)
		}
	}
	return out
}

func decodeAttachments(raw []any) []Attachment {
	out := make([]Attachment, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		att := Attachment{
			Type:        str(obj, "type"),
			URL:         str(obj, "url"),
			Filename:    str(obj, "filename"),
			ContentType: str(obj, "content_type"),
		}
		if att.URL != "" {
			out = append(out, att)
		}
	}
	return out
}

func str(p map[string]any, key string) string {
	s, _ := p[key].(string)
	return s
}

func integer(p map[string]any, key string) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

func number(p map[string]any, key string) float64 {
	switch v := p[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}
