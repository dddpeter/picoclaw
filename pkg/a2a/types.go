// Package a2a serves a minimal A2A v1.0 endpoint (agent card discovery +
// synchronous message/send) on the gateway's shared HTTP mux. Zero new
// dependencies: JSON-RPC 2.0 is hand-rolled with stdlib, exactly two
// methods are worth having at MVP.
//
// Wire format (A2A v1.0 = HTTP + JSON-RPC 2.0):
//
//	GET  /.well-known/agent-card.json   → AgentCard
//	POST <base-url>                     → {"jsonrpc":"2.0","id":…,"method":"message/send","params":{"message":{…}}}
//
// The acceptance oracle is the GroupBridge client
// (tools/groupbridge/internal/agents/a2a.go): if it can drive this server
// unmodified, the implementation is spec-compatible.
package a2a

import "encoding/json"

// JSONRPCRequest is the JSON-RPC 2.0 request envelope.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"` // always "2.0"
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// JSONRPCResponse is the JSON-RPC 2.0 response envelope. Exactly one of
// Result / Error is set on the wire (omitempty on both).
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError carries a JSON-RPC error object.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes used by this server.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000 // server-defined range start: agent loop failures
)

// A2AMessage is an A2A message (user or agent side).
type A2AMessage struct {
	Role      string    `json:"role"` // "user" | "agent"
	Parts     []A2APart `json:"parts"`
	MessageID string    `json:"messageId"`
	ContextID string    `json:"contextId,omitempty"`
}

// A2APart is one content part; MVP carries text only.
type A2APart struct {
	Kind string `json:"kind"` // MVP: "text"
	Text string `json:"text"`
}

// Text extracts the concatenated text of all text parts.
func (m *A2AMessage) Text() string {
	out := ""
	for i, p := range m.Parts {
		if p.Kind == "text" || p.Kind == "" {
			if i > 0 && out != "" {
				out += "\n"
			}
			out += p.Text
		}
	}
	return out
}

// MessageSendParams is the params object of message/send.
type MessageSendParams struct {
	Message *A2AMessage `json:"message"`
}

// AgentCardCapabilities advertises what this server supports.
type AgentCardCapabilities struct {
	Streaming         bool `json:"streaming"`         // false: sync MVP
	PushNotifications bool `json:"pushNotifications"` // false: no tasks/push yet
}

// AgentCardSecurityScheme names the supported auth scheme.
type AgentCardSecurityScheme struct {
	Type string `json:"type"` // "http" (bearer)
}

// AgentCard is the discovery document served at /.well-known/agent-card.json.
type AgentCard struct {
	Name               string                `json:"name"`
	Description        string                `json:"description"`
	ProtocolVersion    string                `json:"protocolVersion"`
	URL                string                `json:"url"`
	Capabilities       AgentCardCapabilities `json:"capabilities"`
	SecuritySchemes    []string              `json:"securitySchemes,omitempty"` // e.g. ["bearer"]
	DefaultInputModes  []string              `json:"defaultInputModes"`
	DefaultOutputModes []string              `json:"defaultOutputModes"`
}

// NewAgentCard builds the standard card for this gateway.
func NewAgentCard(name, description, baseURL string, withAuth bool) AgentCard {
	card := AgentCard{
		Name:            name,
		Description:     description,
		ProtocolVersion: "1.0",
		URL:             baseURL,
		Capabilities: AgentCardCapabilities{
			Streaming:         false, // message/stream endpoint exists but the pipeline→registry frame path is not wired end-to-end yet (see stream.go known issue)
			PushNotifications: false, // no tasks/push yet
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}
	if withAuth {
		card.SecuritySchemes = []string{"bearer"}
	}
	return card
}
