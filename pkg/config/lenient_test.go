package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfigLenient_UnknownFieldsToleratedWithWarnings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 3,
		"tools": {
			"exec": {
				"timeout_seconds": 600,
				"approval_patterns": ["nope"]
			}
		},
		"totally_unknown_section": {"x": 1}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, warnings, err := LoadConfigLenient(configPath)
	if err != nil {
		t.Fatalf("LoadConfigLenient() should tolerate unknown fields, got error: %v", err)
	}
	assert.Equal(t, 600, cfg.Tools.Exec.TimeoutSeconds)
	assert.Contains(t, warnings, "tools.exec.approval_patterns")
	assert.Contains(t, warnings, "totally_unknown_section")
}

func TestLoadConfigLenient_SyntaxErrorFailsHard(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	// Truncated JSON mimics the 09-07 corruption (cut mid-token).
	raw := `{
		"version": 3,
		"mcp": { "servers": { "serve"`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, warnings, err := LoadConfigLenient(configPath)
	assert.Nil(t, cfg)
	assert.Nil(t, warnings)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config.json")
}

func TestLoadConfig_StrictModeStillRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 3,
		"tools": {"exec": {"approval_patterns": ["nope"]}}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfig(configPath)
	assert.Nil(t, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tools.exec.approval_patterns")
}

func TestLoadConfigLenient_NoUnknownFieldsNoWarnings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{"version": 3}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, warnings, err := LoadConfigLenient(configPath)
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Empty(t, warnings)
}

func TestDecodeJSONLenient_UnknownFieldsAsSortedWarnings(t *testing.T) {
	var cfg struct {
		Known string `json:"known"`
	}
	warnings, err := decodeJSONLenient(
		[]byte(`{"known":"a","zz_unknown":1,"nested":{"deep_unknown":2}}`),
		&cfg,
		"test.json",
	)
	assert.NoError(t, err)
	assert.Equal(t, "a", cfg.Known)
	// Note: nested objects with unknown fields are reported at the parent
	// path ("nested"), matching collectUnknownJSONFields' existing behavior
	// in strict mode errors.
	assert.Equal(t, []string{"nested", "zz_unknown"}, warnings)
}

func TestDecodeJSONLenient_SyntaxAndTypeErrorsStillFail(t *testing.T) {
	var cfg struct {
		Known int `json:"known"`
	}
	if _, err := decodeJSONLenient([]byte(`{"known":`), &cfg, "test.json"); err == nil {
		t.Fatal("syntax error should fail")
	}
	if _, err := decodeJSONLenient([]byte(`{"known":"not-an-int"}`), &cfg, "test.json"); err == nil {
		t.Fatal("type error on known field should fail")
	}
}
