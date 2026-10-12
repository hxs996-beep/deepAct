package llm

import (
	"context"
	"encoding/json"
)

type Client interface {
	Stream(ctx context.Context, req ChatRequest) (<-chan Chunk, error)
	Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

type ChatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Tools           []ToolDef `json:"tools,omitempty"`
	Temperature     float64   `json:"temperature,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	JsonMode        bool      `json:"-"`
}

// MarshalJSON guarantees that an assistant message always carries a `content`
// field, even when it is empty.
//
// A model turn that produced only reasoning has an empty Content; with
// `json:"content,omitempty"` the field disappeared from the request entirely and
// the API rejected the whole request with
// 400 "Invalid assistant message: content or tool_calls must be set".
// Emitting `content: ""` keeps every assistant turn schema-valid (the empty
// string is accepted where an absent field is not). Non-assistant roles keep
// omitempty.
func (m Message) MarshalJSON() ([]byte, error) {
	// Alias has no methods, so marshaling it cannot recurse into this one.
	type alias Message
	if m.Role != "assistant" {
		return json.Marshal(alias(m))
	}
	// Content is declared again one level shallower, which makes it win over
	// the promoted `content,omitempty` field of the embedded alias.
	return json.Marshal(struct {
		alias
		Content string `json:"content"`
	}{alias(m), m.Content})
}

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatResponse struct {
	ID               string  `json:"id"`
	Model            string  `json:"model"`
	Message          Message `json:"message"`
	FinishReason     string  `json:"finish_reason"`
	Usage            Usage   `json:"usage"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
}

type Usage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
	// PromptTokensDetails carries the OpenAI-compatible cache breakdown used by
	// most third-party relays/proxies (中转站). DeepSeek's official API reports
	// prompt_cache_hit_tokens directly; relays usually do NOT, and instead send
	// prompt_tokens_details.cached_tokens. UnmarshalJSON normalizes so the
	// cache-hit rate shown in the UI is non-zero through a relay too.
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// PromptTokensDetails is the OpenAI-compatible usage breakdown.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// UnmarshalJSON decodes usage and then falls back to the OpenAI-compatible
// prompt_tokens_details.cached_tokens field when DeepSeek's native
// prompt_cache_hit_tokens is absent (as returned by relays/proxies).
func (u *Usage) UnmarshalJSON(data []byte) error {
	// Alias avoids recursing into UnmarshalJSON.
	type usageAlias Usage
	var a usageAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*u = Usage(a)
	if u.PromptCacheHitTokens == 0 && u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		u.PromptCacheHitTokens = u.PromptTokensDetails.CachedTokens
		if u.PromptCacheMissTokens == 0 {
			miss := u.PromptTokens - u.PromptCacheHitTokens
			if miss < 0 {
				miss = 0
			}
			u.PromptCacheMissTokens = miss
		}
	}
	return nil
}

type Chunk struct {
	Delta          string
	ReasoningDelta string
	ToolCalls      []ToolCall
	FinishReason   string
	Usage          *Usage
	Err            error
	RetryProgress  string // non-empty when a retry is about to start, e.g. "Retrying 1/3..."
}
