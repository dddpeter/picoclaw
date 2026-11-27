package a2a

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestAgentCard pins the Agent Card shape demanded by A2A v1.0 and the
// GroupBridge client (docs/design/a2a/2026-11-27-a2a-server-plan.md 任务 1).
func TestAgentCard(t *testing.T) {
	card := NewAgentCard("虾酱", "PicoClaw 虾酱", "http://127.0.0.1:18790", true)

	if card.ProtocolVersion != "1.0" {
		t.Errorf("protocolVersion = %q, want 1.0", card.ProtocolVersion)
	}
	if card.Name != "虾酱" {
		t.Errorf("name = %q, want 虾酱", card.Name)
	}
	if card.Description != "PicoClaw 虾酱" {
		t.Errorf("description = %q, want PicoClaw 虾酱", card.Description)
	}
	if card.URL != "http://127.0.0.1:18790" {
		t.Errorf("url = %q, want http://127.0.0.1:18790", card.URL)
	}
	if card.Capabilities.Streaming {
		t.Error("capabilities.streaming = true, want false (sync MVP)")
	}
	if card.Capabilities.PushNotifications {
		t.Error("capabilities.pushNotifications = true, want false")
	}
	found := false
	for _, scheme := range card.SecuritySchemes {
		if scheme == "bearer" {
			found = true
		}
	}
	if !found {
		t.Errorf("securitySchemes = %v, want to contain bearer", card.SecuritySchemes)
	}
	if len(card.DefaultInputModes) != 1 || card.DefaultInputModes[0] != "text" {
		t.Errorf("defaultInputModes = %v, want [text]", card.DefaultInputModes)
	}
	if len(card.DefaultOutputModes) != 1 || card.DefaultOutputModes[0] != "text" {
		t.Errorf("defaultOutputModes = %v, want [text]", card.DefaultOutputModes)
	}

	// The card must serialize cleanly (GroupBridge will json-decode it).
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("card is not a valid JSON object: %v", err)
	}
}

// TestCardHandler pins the discovery endpoint: GET /.well-known/agent-card.json.
func TestCardHandler(t *testing.T) {
	h := CardHandler(NewAgentCard("虾酱", "PicoClaw 虾酱", "http://127.0.0.1:18790", true))

	req := httptest.NewRequest("GET", "/.well-known/agent-card.json", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" || !jsonCompatible(ct) {
		t.Errorf("content-type = %q, want json", ct)
	}
	var card AgentCard
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatalf("body does not decode to AgentCard: %v", err)
	}
	if card.ProtocolVersion != "1.0" || card.Name != "虾酱" {
		t.Errorf("decoded card mismatch: %+v", card)
	}

	// Only GET is meaningful; other methods get 405.
	req2 := httptest.NewRequest("POST", "/.well-known/agent-card.json", nil)
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != 405 {
		t.Errorf("POST to card endpoint status = %d, want 405", rec2.Code)
	}
}

func jsonCompatible(ct string) bool {
	return len(ct) >= 16 && ct[:16] == "application/json"
}
