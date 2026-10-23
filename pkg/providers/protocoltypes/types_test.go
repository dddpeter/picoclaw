package protocoltypes

import (
	"encoding/json"
	"testing"
)

// 缓存命中两种上游写法（OpenAI/GLM 嵌套 details、DeepSeek 平铺）必须都能
// 反序列化并经 CachedTokens() 归一；缺省时归一为 0（footer 整段省略）。
func TestUsageInfoCachedTokensParsing(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantCached int
	}{
		{
			name:       "openai nested prompt_tokens_details",
			payload:    `{"prompt_tokens":1200,"completion_tokens":340,"total_tokens":1540,"prompt_tokens_details":{"cached_tokens":900}}`,
			wantCached: 900,
		},
		{
			name:       "deepseek flat prompt_cache_hit_tokens",
			payload:    `{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":800}`,
			wantCached: 800,
		},
		{
			name:       "both spellings take the max",
			payload:    `{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":300},"prompt_cache_hit_tokens":800}`,
			wantCached: 800,
		},
		{
			name:       "no cache breakdown",
			payload:    `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`,
			wantCached: 0,
		},
		{
			name:       "nested details without cached_tokens",
			payload:    `{"prompt_tokens":100,"prompt_tokens_details":{}}`,
			wantCached: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u UsageInfo
			if err := json.Unmarshal([]byte(tc.payload), &u); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := u.CachedTokens(); got != tc.wantCached {
				t.Errorf("CachedTokens() = %d, want %d", got, tc.wantCached)
			}
		})
	}
}

func TestUsageInfoCachedTokensNilSafety(t *testing.T) {
	var u *UsageInfo
	if got := u.CachedTokens(); got != 0 {
		t.Errorf("nil UsageInfo CachedTokens() = %d, want 0", got)
	}
}
