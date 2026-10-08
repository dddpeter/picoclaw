// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"testing"
)

// TestModelConfigContextWindowJSON pins the per-model context_window JSON
// tag (fork feature): model_list entries carry the token count under
// "context_window" and omit it when unset.
func TestModelConfigContextWindowJSON(t *testing.T) {
	var entry ModelConfig
	if err := json.Unmarshal([]byte(`{"model_name":"m2","model":"minimax/m2","context_window":180000}`), &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.ContextWindow != 180_000 {
		t.Fatalf("ContextWindow = %d, want 180000", entry.ContextWindow)
	}

	out, err := json.Marshal(&entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round ModelConfig
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if round.ContextWindow != 180_000 {
		t.Fatalf("round-trip ContextWindow = %d, want 180000", round.ContextWindow)
	}

	var unset ModelConfig
	if err := json.Unmarshal([]byte(`{"model_name":"m3","model":"minimax/m3"}`), &unset); err != nil {
		t.Fatalf("unmarshal unset: %v", err)
	}
	if unset.ContextWindow != 0 {
		t.Fatalf("unset ContextWindow = %d, want 0", unset.ContextWindow)
	}

	if err := (&ModelConfig{ModelName: "m", Model: "p/m", ContextWindow: -1}).Validate(); err == nil {
		t.Fatalf("Validate should reject negative context_window")
	}
}

// TestExpandMultiKeyModels_CarriesContextWindow pins that multi-key
// expansion propagates the per-model window to both the primary entry and
// the virtual per-key entries — a dropped field would silently uncapped the
// key-expanded aliases.
func TestExpandMultiKeyModels_CarriesContextWindow(t *testing.T) {
	result := expandMultiKeyModels([]*ModelConfig{
		{
			ModelName:     "m2",
			Model:         "minimax/m2",
			APIKeys:       SimpleSecureStrings("key1", "key2"),
			ContextWindow: 180_000,
		},
	})
	if len(result) != 2 {
		t.Fatalf("expected 2 expanded models, got %d", len(result))
	}
	for _, m := range result {
		if m.ContextWindow != 180_000 {
			t.Errorf("expanded entry %q ContextWindow = %d, want 180000", m.ModelName, m.ContextWindow)
		}
	}
}
