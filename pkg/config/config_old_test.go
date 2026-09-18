package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsProvidersMapEmpty covers the "is there any usable provider config"
// predicate used before V0→V1 migration. A provider counts as non-empty when
// any of api_key / api_base / connect_mode / auth_method is present (even as
// nil — presence, not value, is what the check tests).
func TestIsProvidersMapEmpty(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]any
		want      bool
	}{
		{name: "nil map", providers: nil, want: true},
		{name: "empty map", providers: map[string]any{}, want: true},
		{
			name:      "provider value not a map",
			providers: map[string]any{"openai": "not-a-map"},
			want:      true,
		},
		{
			name: "all checked fields empty strings",
			providers: map[string]any{
				"openai": map[string]any{
					"api_key":      "",
					"api_base":     "",
					"connect_mode": "",
					"auth_method":  "",
				},
			},
			want: true,
		},
		{
			name:      "field not checked (request_timeout only)",
			providers: map[string]any{"openai": map[string]any{"request_timeout": float64(30)}},
			want:      true,
		},
		{
			name:      "api_key present",
			providers: map[string]any{"openai": map[string]any{"api_key": "sk-test"}},
			want:      false,
		},
		{
			name:      "api_base present",
			providers: map[string]any{"openai": map[string]any{"api_base": "https://x"}},
			want:      false,
		},
		{
			name:      "connect_mode present",
			providers: map[string]any{"github_copilot": map[string]any{"connect_mode": "ide"}},
			want:      false,
		},
		{
			name:      "auth_method present",
			providers: map[string]any{"antigravity": map[string]any{"auth_method": "oauth"}},
			want:      false,
		},
		{
			// Documents current semantics: a present-but-nil value still
			// counts as non-empty (nil != "" for interface values).
			name:      "api_key nil still counts as present",
			providers: map[string]any{"openai": map[string]any{"api_key": nil}},
			want:      false,
		},
		{
			name: "one non-empty provider among empties",
			providers: map[string]any{
				"empty":    map[string]any{"api_key": ""},
				"deepseek": map[string]any{"api_key": "sk-ds"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isProvidersMapEmpty(tt.providers))
		})
	}
}

// TestV0ProvidersMapToModelList covers the legacy providers→model_list
// conversion. The migration table has 25 rules; the sweep case feeds every
// primary key at once so every extractFn branch is executed, while the
// targeted cases pin down alias resolution, user-model override, ordering,
// and skip behavior.
func TestV0ProvidersMapToModelList(t *testing.T) {
	// primaries lists one json key per migration rule, in table order,
	// with the default model each rule assigns.
	primaries := []struct {
		key      string
		defModel string
	}{
		{"openai", "openai/gpt-5.4"},
		{"anthropic", "anthropic/claude-sonnet-4.6"},
		{"litellm", "litellm/auto"},
		{"openrouter", "openrouter/auto"},
		{"groq", "groq/llama-3.1-70b-versatile"},
		{"zhipu", "zhipu/glm-4"},
		{"vllm", "vllm/auto"},
		{"gemini", "gemini/gemini-pro"},
		{"nvidia", "nvidia/meta/llama-3.1-8b-instruct"},
		{"ollama", "ollama/llama3"},
		{"moonshot", "moonshot/kimi"},
		{"shengsuanyun", "shengsuanyun/auto"},
		{"deepseek", "deepseek/deepseek-chat"},
		{"cerebras", "cerebras/llama-3.3-70b"},
		{"vivgrid", "vivgrid/auto"},
		{"volcengine", "volcengine/doubao-pro"},
		{"github_copilot", "github-copilot/gpt-5.4"},
		{"antigravity", "antigravity/gemini-2.0-flash"},
		{"qwen", "qwen/qwen-max"},
		{"mistral", "mistral/mistral-small-latest"},
		{"avian", "avian/deepseek/deepseek-v3.2"},
		{"minimax", "minimax/minimax"},
		{"longcat", "longcat/LongCat-Flash-Thinking"},
		{"modelscope", "modelscope/Qwen/Qwen3-235B-A22B-Instruct-2507"},
		{"novita", "novita/auto"},
	}

	t.Run("sweep: every provider with full fields", func(t *testing.T) {
		providers := make(map[string]any, len(primaries))
		for _, p := range primaries {
			m := map[string]any{
				// numbers mirror json.Unmarshal (float64)
				"api_key":         "sk-" + p.key,
				"api_base":        "https://" + p.key + ".example.com",
				"proxy":           "http://127.0.0.1:7890",
				"request_timeout": float64(30),
				"auth_method":     "api_key",
			}
			if p.key == "openai" {
				m["web_search"] = true
			}
			if p.key == "github_copilot" {
				m["connect_mode"] = "ide"
			}
			providers[p.key] = m
		}

		got := v0ProvidersMapToModelList(providers, "", "")
		require.Len(t, got, len(primaries))

		for i, p := range primaries {
			entry, ok := got[i].(map[string]any)
			require.True(t, ok, "entry %d not a map", i)
			assert.Equal(t, p.key, entry["model_name"], "entry %d model_name", i)
			assert.Equal(t, p.defModel, entry["model"], "entry %d model", i)
			assert.Equal(t, "sk-"+p.key, entry["api_key"], "entry %d api_key", i)
		}

		// openai keeps the extra web_search flag
		openai := got[0].(map[string]any)
		assert.Equal(t, true, openai["web_search"])
		assert.Equal(t, "api_key", openai["auth_method"])
		assert.Contains(t, openai, "request_timeout")

		// github_copilot extracts connect_mode but drops proxy/timeout/auth_method
		copilot := got[16].(map[string]any)
		assert.Equal(t, "ide", copilot["connect_mode"])
		assert.NotContains(t, copilot, "proxy")
		assert.NotContains(t, copilot, "request_timeout")
		assert.NotContains(t, copilot, "auth_method")

		// antigravity only carries api_key + auth_method
		anti := got[17].(map[string]any)
		assert.Equal(t, "sk-antigravity", anti["api_key"])
		assert.Equal(t, "api_key", anti["auth_method"])
		assert.NotContains(t, anti, "api_base")
		assert.NotContains(t, anti, "proxy")
		assert.NotContains(t, anti, "request_timeout")
	})

	t.Run("empty providers yields nil", func(t *testing.T) {
		assert.Empty(t, v0ProvidersMapToModelList(map[string]any{}, "", ""))
	})

	t.Run("unknown provider skipped", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"foobar": map[string]any{"api_key": "sk-x"}}, "", "")
		assert.Empty(t, got)
	})

	t.Run("provider value not a map skipped", func(t *testing.T) {
		got := v0ProvidersMapToModelList(map[string]any{"openai": "oops"}, "", "")
		assert.Empty(t, got)
	})

	t.Run("provider with only empty fields skipped", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"openai": map[string]any{"api_key": "", "api_base": ""}}, "", "")
		assert.Empty(t, got)
	})

	t.Run("alias key resolves to primary model_name", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"claude": map[string]any{"api_key": "sk-c"}}, "", "")
		require.Len(t, got, 1)
		entry := got[0].(map[string]any)
		assert.Equal(t, "anthropic", entry["model_name"])
		assert.Equal(t, "anthropic/claude-sonnet-4.6", entry["model"])
	})

	t.Run("copilot alias keeps underscored model_name", func(t *testing.T) {
		// protocol is "github-copilot" but model_name must stay jsonKeys[0]
		got := v0ProvidersMapToModelList(
			map[string]any{"copilot": map[string]any{"api_key": "sk-co"}}, "", "")
		require.Len(t, got, 1)
		entry := got[0].(map[string]any)
		assert.Equal(t, "github_copilot", entry["model_name"])
		assert.Equal(t, "github-copilot/gpt-5.4", entry["model"])
	})

	t.Run("user model without slash gets protocol prefix", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"openai": map[string]any{"api_key": "sk-o"}},
			"openai", "gpt-4o")
		entry := got[0].(map[string]any)
		assert.Equal(t, "openai/gpt-4o", entry["model"])
	})

	t.Run("user model with slash used verbatim", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"deepseek": map[string]any{"api_key": "sk-d"}},
			"deepseek", "custom/mymodel")
		entry := got[0].(map[string]any)
		assert.Equal(t, "custom/mymodel", entry["model"])
	})

	t.Run("user model via alias gets canonical protocol prefix", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"moonshot": map[string]any{"api_key": "sk-m"}},
			"kimi", "kimi-k2")
		entry := got[0].(map[string]any)
		assert.Equal(t, "moonshot/kimi-k2", entry["model"])
	})

	t.Run("user model via copilot alias gets hyphenated protocol prefix", func(t *testing.T) {
		// github_copilot is the one rule where protocol ("github-copilot")
		// differs from jsonKeys[0]; the user-model override must use the
		// protocol, not the underscored model_name.
		got := v0ProvidersMapToModelList(
			map[string]any{"copilot": map[string]any{"api_key": "sk-co"}},
			"copilot", "gpt-4o")
		require.Len(t, got, 1)
		entry := got[0].(map[string]any)
		assert.Equal(t, "github-copilot/gpt-4o", entry["model"])
	})

	t.Run("user provider matches but model empty keeps default", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"openai": map[string]any{"api_key": "sk-o"}},
			"openai", "")
		entry := got[0].(map[string]any)
		assert.Equal(t, "openai/gpt-5.4", entry["model"])
	})

	t.Run("user provider not in table keeps default", func(t *testing.T) {
		got := v0ProvidersMapToModelList(
			map[string]any{"openai": map[string]any{"api_key": "sk-o"}},
			"unknown-prov", "whatever")
		entry := got[0].(map[string]any)
		assert.Equal(t, "openai/gpt-5.4", entry["model"])
	})

	t.Run("primary key wins over alias when both present", func(t *testing.T) {
		providers := map[string]any{
			"openai": map[string]any{"api_key": "sk-primary"},
			"gpt":    map[string]any{"api_key": "sk-alias"},
		}
		got := v0ProvidersMapToModelList(providers, "", "")
		require.Len(t, got, 1)
		entry := got[0].(map[string]any)
		assert.Equal(t, "sk-primary", entry["api_key"])
	})

	t.Run("entries follow migration table order, not map order", func(t *testing.T) {
		providers := map[string]any{
			"deepseek":  map[string]any{"api_key": "sk-ds"},
			"anthropic": map[string]any{"api_key": "sk-an"},
			"openai":    map[string]any{"api_key": "sk-oa"},
		}
		got := v0ProvidersMapToModelList(providers, "", "")
		require.Len(t, got, 3)
		assert.Equal(t, "openai", got[0].(map[string]any)["model_name"])
		assert.Equal(t, "anthropic", got[1].(map[string]any)["model_name"])
		assert.Equal(t, "deepseek", got[2].(map[string]any)["model_name"])
	})

	t.Run("web_search false excluded true included", func(t *testing.T) {
		providers := map[string]any{
			"openai": map[string]any{
				"api_key":    "sk-o",
				"web_search": false,
			},
		}
		got := v0ProvidersMapToModelList(providers, "", "")
		require.Len(t, got, 1)
		assert.NotContains(t, got[0].(map[string]any), "web_search")

		providers["openai"].(map[string]any)["web_search"] = true
		got = v0ProvidersMapToModelList(providers, "", "")
		assert.Equal(t, true, got[0].(map[string]any)["web_search"])
	})

	t.Run("request_timeout zero kept nil dropped", func(t *testing.T) {
		providers := map[string]any{
			"openai": map[string]any{
				"api_key":         "sk-o",
				"request_timeout": float64(0),
			},
		}
		got := v0ProvidersMapToModelList(providers, "", "")
		require.Len(t, got, 1)
		entry := got[0].(map[string]any)
		val, has := entry["request_timeout"]
		require.True(t, has, "zero timeout should be kept")
		assert.Equal(t, float64(0), val)

		providers["openai"].(map[string]any)["request_timeout"] = nil
		got = v0ProvidersMapToModelList(providers, "", "")
		assert.NotContains(t, got[0].(map[string]any), "request_timeout")
	})
}
