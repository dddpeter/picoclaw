package pico

import (
	"strings"
	"time"
)

// Protocol message types.
const (
	// TypeMessageSend is sent from client to server.
	TypeMessageSend = "message.send"
	TypeMediaSend   = "media.send"
	TypePing        = "ping"

	// TypeMessageCreate is sent from server to client.
	TypeMessageCreate = "message.create"
	TypeMessageUpdate = "message.update"
	TypeMessageDelete = "message.delete"
	TypeMediaCreate   = "media.create"
	TypeTypingStart   = "typing.start"
	TypeTypingStop    = "typing.stop"
	// TypeTurnStatus is the reconnect signal (octop borrowing §四): sent as
	// the first frame on connect so a page refresh learns whether a turn is
	// live for its session. Payload: {active: bool}.
	TypeTurnStatus = "turn.status"
	// TypeStreamCatchup marks a catch-up boundary (trae borrowing §一): sent
	// after replayed frames when the client's last_seq predates the buffer's
	// oldest retained frame. Payload: {complete: bool, dropped: int}. Clients
	// must fall back to history replay for the dropped gap instead of
	// silently assuming a contiguous stream.
	TypeStreamCatchup = "stream.catchup"
	TypeError         = "error"
	TypePong          = "pong"

	PayloadKeyContent     = "content"
	PayloadKeyThought     = "thought"
	PayloadKeyKind        = "kind"
	PayloadKeyPlaceholder = "placeholder"
	PayloadKeyToolCalls   = "tool_calls"
	PayloadKeyModelName   = "model_name"
	PayloadKeyUsage       = "usage"
	PayloadKeyApproval    = "approval"
	PayloadKeyAsk         = "ask"

	MessageKindThought   = "thought"
	MessageKindToolCalls = "tool_calls"
	// MessageKindProgressNote marks non-final progress beats (stall/heartbeat
	// notices). Clients must not treat them as the turn's final answer.
	MessageKindProgressNote = "progress_note"
)

// PicoMessage is the wire format for all Pico Protocol messages.
type PicoMessage struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Seq is the per-session monotonic outbound sequence number (trae
	// borrowing §一). Assigned on session-level broadcasts only — per-connection
	// frames (pong, error, turn.status) stay seq-less. Omitempty keeps the
	// wire format byte-compatible for legacy clients and for frames that
	// never enter the seq stream.
	Seq       uint64         `json:"seq,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// newMessage creates a PicoMessage with the given type and payload.
func newMessage(msgType string, payload map[string]any) PicoMessage {
	return PicoMessage{
		Type:      msgType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func isThoughtPayload(payload map[string]any) bool {
	kind, _ := payload[PayloadKeyKind].(string)
	if strings.EqualFold(strings.TrimSpace(kind), MessageKindThought) {
		return true
	}

	// Keep pico_client inbound-compatible with legacy servers that still send
	// the pre-kind boolean thought marker.
	thought, _ := payload[PayloadKeyThought].(bool)
	return thought
}

func newErrorWithPayload(code, message string, extra map[string]any) PicoMessage {
	payload := map[string]any{
		"code":    code,
		"message": message,
	}
	for key, value := range extra {
		payload[key] = value
	}
	return newMessage(TypeError, payload)
}

// newError creates an error PicoMessage.
func newError(code, message string) PicoMessage {
	return newErrorWithPayload(code, message, nil)
}
