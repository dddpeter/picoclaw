package memory

import (
	"encoding/json"
	"testing"
)

// TestAskMarkerRoundtrip pins the ask marker lifecycle (fork, octop
// borrowing §三): write → read → clear, corrupt self-heal.
func TestAskMarkerRoundtrip(t *testing.T) {
	s := newTestStore(t)
	key := "agent_default:test:ask"

	if m, err := s.ReadAskMarker(key); err != nil || m != nil {
		t.Fatalf("fresh read = (%v, %v), want (nil, nil)", m, err)
	}

	questions, _ := json.Marshal([]map[string]any{
		{"question": "部署到哪个环境？", "header": "环境",
			"options": []map[string]any{{"label": "staging"}, {"label": "production"}}},
	})
	if err := s.WriteAskMarker(AskMarker{
		SessionKey: key,
		AgentID:    "main",
		Questions:  questions,
		TimeoutMs:  600000,
	}); err != nil {
		t.Fatalf("WriteAskMarker: %v", err)
	}

	m, err := s.ReadAskMarker(key)
	if err != nil || m == nil {
		t.Fatalf("ReadAskMarker = (%v, %v)", m, err)
	}
	if m.SessionKey != key || m.AgentID != "main" || m.TimeoutMs != 600000 {
		t.Fatalf("marker = %+v", m)
	}
	if m.StartedAt == 0 {
		t.Fatal("StartedAt must be stamped when unset")
	}
	var qs []map[string]any
	if err := json.Unmarshal(m.Questions, &qs); err != nil || len(qs) != 1 || qs[0]["header"] != "环境" {
		t.Fatalf("questions roundtrip failed: %v %v", string(m.Questions), err)
	}

	if err := s.ClearAskMarker(key); err != nil {
		t.Fatalf("ClearAskMarker: %v", err)
	}
	if m, _ := s.ReadAskMarker(key); m != nil {
		t.Fatal("cleared marker must read nil")
	}
	// Idempotent clear.
	if err := s.ClearAskMarker(key); err != nil {
		t.Fatalf("idempotent clear: %v", err)
	}
}
