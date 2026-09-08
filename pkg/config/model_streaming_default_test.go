package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fork's default-on convention for model streaming: omitted (nil) counts
// as enabled, explicit false opts out. Round-trip: nil stays omitted, false
// must survive a save (it is the only way to opt out).
func TestModelStreamingConfigDefaultOn(t *testing.T) {
	if !(ModelStreamingConfig{}).EffectiveEnabled() {
		t.Fatal("omitted (nil) streaming must default to enabled")
	}
	if !(ModelStreamingConfig{Enabled: boolPtr(true)}).EffectiveEnabled() {
		t.Fatal("explicit true must be enabled")
	}
	if (ModelStreamingConfig{Enabled: boolPtr(false)}).EffectiveEnabled() {
		t.Fatal("explicit false must disable streaming")
	}
	if (ModelStreamingConfig{}).IsZero() != true {
		t.Fatal("nil streaming should marshal away (IsZero)")
	}
	if (ModelStreamingConfig{Enabled: boolPtr(false)}).IsZero() {
		t.Fatal("explicit false must NOT marshal away — it would flip back to default-on on reload")
	}

	// Marshal round-trip on a full ModelConfig
	m := &ModelConfig{ModelName: "m", Provider: "openai", Model: "m"}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "streaming") {
		t.Fatalf("nil streaming should be omitted, got %s", out)
	}

	m.Streaming = ModelStreamingConfig{Enabled: boolPtr(false)}
	out, err = json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `"enabled":false`) {
		t.Fatalf("explicit false must round-trip, got %s", out)
	}

	var back ModelConfig
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Streaming.EffectiveEnabled() {
		t.Fatal("explicit false must stay disabled after round-trip")
	}
}
