package llm

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalUsage_RelayCachedTokensFallback(t *testing.T) {
	// A relay (中转站) returns OpenAI-compatible usage: prompt_tokens_details.cached_tokens,
	// NOT DeepSeek's native prompt_cache_hit_tokens.
	data := []byte(`{
		"prompt_tokens": 1000,
		"completion_tokens": 100,
		"total_tokens": 1100,
		"prompt_tokens_details": {"cached_tokens": 700}
	}`)
	var u Usage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.PromptCacheHitTokens != 700 {
		t.Errorf("PromptCacheHitTokens = %d, want 700 (from cached_tokens fallback)", u.PromptCacheHitTokens)
	}
	if u.PromptCacheMissTokens != 300 {
		t.Errorf("PromptCacheMissTokens = %d, want 300 (prompt - cache_hit)", u.PromptCacheMissTokens)
	}
}

func TestUnmarshalUsage_DeepSeekNative(t *testing.T) {
	// DeepSeek's official API reports the native fields; the fallback must not
	// override a real (possibly zero) cache-hit count.
	data := []byte(`{
		"prompt_tokens": 800,
		"completion_tokens": 50,
		"total_tokens": 850,
		"prompt_cache_hit_tokens": 500,
		"prompt_cache_miss_tokens": 300
	}`)
	var u Usage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.PromptCacheHitTokens != 500 {
		t.Errorf("PromptCacheHitTokens = %d, want 500 (native field wins)", u.PromptCacheHitTokens)
	}
	if u.PromptCacheMissTokens != 300 {
		t.Errorf("PromptCacheMissTokens = %d, want 300", u.PromptCacheMissTokens)
	}
}

func TestUnmarshalUsage_BothFields_RelayWinsNativeZero(t *testing.T) {
	// Some relays echo both; when the native hit field is 0, the detailed value wins.
	data := []byte(`{
		"prompt_tokens": 640,
		"prompt_cache_hit_tokens": 0,
		"prompt_tokens_details": {"cached_tokens": 512}
	}`)
	var u Usage
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.PromptCacheHitTokens != 512 {
		t.Errorf("PromptCacheHitTokens = %d, want 512 (detail win when native is 0)", u.PromptCacheHitTokens)
	}
}
