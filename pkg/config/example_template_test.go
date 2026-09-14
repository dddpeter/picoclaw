package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// exampleTemplatePath resolves config/config.example.json relative to the
// package directory (go test runs with the package dir as cwd).
const exampleTemplatePath = "../../config/config.example.json"

// TestExampleTemplateLoadsStrict pins the shipped config template against the
// strict loader — the exact path `picoclaw gateway` startup uses. A first-run
// user who copies the template must not hit
// "config.json contains unknown field(s)" (2026-09 first-install regression:
// template advertised _comment keys and singular api_key fields the structs
// never had). If this fails, the template drifted from the schema.
func TestExampleTemplateLoadsStrict(t *testing.T) {
	if _, err := os.Stat(exampleTemplatePath); err != nil {
		t.Skipf("example template not found: %v", err)
	}

	cfg, warnings, err := LoadConfigWithWarnings(exampleTemplatePath, false)
	assert.NoError(t, err, "config.example.json must pass the strict loader")
	assert.NotNil(t, cfg)
	assert.Empty(t, warnings, "shipped template must not trigger unknown-field warnings")
}

// TestCommentFieldsToleratedInStrictLoad verifies the "_comment" documentation
// convention no longer fails strict loads, while genuine typos still do.
func TestCommentFieldsToleratedInStrictLoad(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	raw := `{
		"version": 3,
		"_comment": "top-level note",
		"gateway": {
			"_comment": "log level note",
			"port": 18790
		},
		"model_list": [
			{
				"_comment": "anthropic native api note",
				"model_name": "claude",
				"provider": "anthropic",
				"model": "claude-sonnet-4-5",
				"api_keys": ["sk-ant-test"]
			}
		],
		"channel_list": {
			"wecom": {
				"_comment": "WeCom AI Bot over WebSocket.",
				"enabled": false,
				"type": "wecom"
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}

	cfg, err := LoadConfig(configPath)
	assert.NoError(t, err, "_comment keys must not fail strict loads")
	assert.NotNil(t, cfg)
	assert.Equal(t, 18790, cfg.Gateway.Port)

	// The whitelist is narrow: a real typo must still be rejected.
	typoPath := filepath.Join(dir, "typo.json")
	typo := `{"version": 3, "gateway": {"prot": 1234}}`
	if err := os.WriteFile(typoPath, []byte(typo), 0o644); err != nil {
		t.Fatalf("WriteFile(typoPath): %v", err)
	}
	_, typoWarnings, err := LoadConfigLenient(typoPath)
	assert.NoError(t, err)
	assert.Contains(t, typoWarnings, "gateway.prot")
}

// TestWebSearchLegacyAPIKeyAliasFold covers the deprecated singular api_key on
// the four multi-key web-search providers: folded into api_keys when absent,
// api_keys wins when both are set, and the alias never persists back to JSON.
func TestWebSearchLegacyAPIKeyAliasFold(t *testing.T) {
	cases := []struct {
		name   string
		json   string
		expect string
	}{
		{"brave", `"brave": {"enabled": true, "api_key": "brave-key", "max_results": 5}`, "brave-key"},
		{"tavily", `"tavily": {"enabled": true, "api_key": "tavily-key", "base_url": "https://example.invalid"}`, "tavily-key"},
		{"kagi", `"kagi": {"enabled": true, "api_key": "kagi-key"}`, "kagi-key"},
		{"perplexity", `"perplexity": {"enabled": true, "api_key": "pplx-key"}`, "pplx-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_only_alias", func(t *testing.T) {
			cfg, _, err := LoadConfigLenient(writeTempConfig(t, `{
				"version": 3,
				"tools": {"web": {%s}}
			}`, tc.json))
			if err != nil {
				t.Fatalf("LoadConfigLenient(): %v", err)
			}
			web := &cfg.Tools.Web
			var got string
			switch tc.name {
			case "brave":
				got = web.Brave.APIKey()
			case "tavily":
				got = web.Tavily.APIKey()
			case "kagi":
				got = web.Kagi.APIKey()
			case "perplexity":
				got = web.Perplexity.APIKey()
			}
			assert.Equal(t, tc.expect, got, "singular api_key must fold into api_keys")
		})
	}

	// api_keys wins when both spellings are present.
	cfg, warnings, err := LoadConfigLenient(writeTempConfig(t, `{
		"version": 3,
		"tools": {"web": {"brave": {
			"api_key": "legacy",
			"api_keys": ["canonical"]
		}}}
	}`))
	assert.NoError(t, err)
	assert.Empty(t, warnings, "api_key is a known field now and must not warn")
	assert.Equal(t, "canonical", cfg.Tools.Web.Brave.APIKey())

	// The alias is load-only: marshaling never writes "api_key" back out.
	out, err := json.Marshal(&cfg.Tools.Web)
	assert.NoError(t, err)
	assert.NotContains(t, string(out), `"api_key":`,
		"deprecated api_key alias must not persist back to JSON")
}

func writeTempConfig(t *testing.T, format string, args ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := format
	for _, a := range args {
		raw = strings.Replace(raw, "%s", a, 1)
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile(configPath): %v", err)
	}
	return path
}
