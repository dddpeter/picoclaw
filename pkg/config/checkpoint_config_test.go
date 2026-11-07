package config

import (
	"encoding/json"
	"testing"
)

// TestCheckpointDisabledByDefault pins the workspace-checkpoint default
// (trae borrowing 增量清单 §2.1): the feature ships off, and only the
// exec-coverage sub-switch defaults on so enabling the feature covers
// shell-made changes without extra configuration. A plain bool is
// deliberate — default-off has no legacy behavior to preserve, so the
// *bool nil=on convention (loop_detection/streaming) does not apply.
func TestCheckpointDisabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Tools.Checkpoint.Enabled {
		t.Fatal("tools.checkpoint.enabled must default to false")
	}
	if !cfg.Tools.Checkpoint.TrackExec {
		t.Fatal("tools.checkpoint.track_exec must default to true (exec coverage follows the feature switch)")
	}

	// The real load path overlays user JSON on DefaultConfig, so an absent
	// block keeps every default (enabled off, track_exec on).
	cfg = DefaultConfig()
	if err := json.Unmarshal([]byte(`{"tools": {}}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Tools.Checkpoint.Enabled {
		t.Fatal("absent checkpoint block must stay disabled")
	}
	if !cfg.Tools.Checkpoint.TrackExec {
		t.Fatal("absent checkpoint block must keep track_exec on")
	}

	// Explicit false wins over the default.
	cfg = DefaultConfig()
	if err := json.Unmarshal([]byte(`{"tools": {"checkpoint": {"enabled": true}}}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Tools.Checkpoint.Enabled {
		t.Fatal("explicit enabled=true must survive overlay")
	}
}

func TestCheckpointConfigEffectiveDefaults(t *testing.T) {
	var c CheckpointConfig
	if got := c.EffectiveMaxVersions(); got != 20 {
		t.Fatalf("EffectiveMaxVersions() = %d, want 20", got)
	}
	if got := c.EffectiveMaxTotalBytes(); got != 128<<20 {
		t.Fatalf("EffectiveMaxTotalBytes() = %d, want 128MB", got)
	}
	if got := c.EffectiveMaxFileSize(); got != 4<<20 {
		t.Fatalf("EffectiveMaxFileSize() = %d, want 4MB", got)
	}
	if got := c.EffectiveScanDepth(); got != 24 {
		t.Fatalf("EffectiveScanDepth() = %d, want 24", got)
	}
	if got := c.EffectiveScanMaxFiles(); got != 20000 {
		t.Fatalf("EffectiveScanMaxFiles() = %d, want 20000", got)
	}

	c = CheckpointConfig{MaxVersions: 3, MaxTotalBytes: 10, MaxFileSize: 20, ScanDepth: 5, ScanMaxFiles: 6}
	if c.EffectiveMaxVersions() != 3 || c.EffectiveMaxTotalBytes() != 10 || c.EffectiveMaxFileSize() != 20 || c.EffectiveScanDepth() != 5 || c.EffectiveScanMaxFiles() != 6 {
		t.Fatal("explicit values must be preserved")
	}
}
