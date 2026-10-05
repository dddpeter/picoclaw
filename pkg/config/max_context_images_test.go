package config

import "testing"

// TestGetMaxContextImagesDefaults pins the max_context_images resolution:
// 0 = default 8, negative = unlimited passthrough, positive = the value.
func TestGetMaxContextImagesDefaults(t *testing.T) {
	if DefaultMaxContextImages != 8 {
		t.Fatalf("default constant drifted: %d", DefaultMaxContextImages)
	}
	var d AgentDefaults
	if got := d.GetMaxContextImages(); got != 8 {
		t.Fatalf("unset must resolve to 8, got %d", got)
	}
	d.MaxContextImages = -1
	if got := d.GetMaxContextImages(); got != -1 {
		t.Fatalf("negative must pass through (unlimited), got %d", got)
	}
	d.MaxContextImages = 3
	if got := d.GetMaxContextImages(); got != 3 {
		t.Fatalf("positive must pass through, got %d", got)
	}
}
