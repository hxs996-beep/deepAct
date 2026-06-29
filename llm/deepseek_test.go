package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeepSeekClient_Stream_BasicContent(t *testing.T) {
	ssePayload := `data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}

data: {"id":"cmpl-1","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"cmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}

data: [DONE]

`
	srv := newTestServer(t, http.StatusOK, ssePayload)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	stream, err := client.Stream(context.Background(), ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var content strings.Builder
	var gotUsage bool
	var finishReason string
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		content.WriteString(chunk.Delta)
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
		if chunk.Usage != nil {
			gotUsage = true
			if chunk.Usage.PromptTokens != 10 {
				t.Errorf("prompt tokens = %d, want 10", chunk.Usage.PromptTokens)
			}
			if chunk.Usage.CompletionTokens != 2 {
				t.Errorf("completion tokens = %d, want 2", chunk.Usage.CompletionTokens)
			}
		}
	}

	if content.String() != "Hello world" {
		t.Errorf("content = %q, want %q", content.String(), "Hello world")
	}
	if finishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", finishReason, "stop")
	}
	if !gotUsage {
		t.Error("expected usage in stream, got none")
	}
}

func TestDeepSeekClient_Stream_ToolCalls(t *testing.T) {
	ssePayload := `data: {"id":"cmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"grep","arguments":""}}]},"finish_reason":null}]}

data: {"id":"cmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\":"}}]},"finish_reason":null}]}

data: {"id":"cmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hello\"}"}}]},"finish_reason":null}]}

data: {"id":"cmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":15,"total_tokens":35}}

data: [DONE]

`
	srv := newTestServer(t, http.StatusOK, ssePayload)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	stream, err := client.Stream(context.Background(), ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "search for hello"}},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var lastToolCalls []ToolCall
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if len(chunk.ToolCalls) > 0 {
			lastToolCalls = chunk.ToolCalls
		}
	}

	if len(lastToolCalls) != 1 {
		t.Fatalf("tool_calls count = %d, want 1", len(lastToolCalls))
	}
	tc := lastToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("tool call id = %q, want %q", tc.ID, "call_abc")
	}
	if tc.Function.Name != "grep" {
		t.Errorf("tool call name = %q, want %q", tc.Function.Name, "grep")
	}
	wantArgs := `{"pattern":"hello"}`
	if tc.Function.Arguments != wantArgs {
		t.Errorf("tool call args = %q, want %q", tc.Function.Arguments, wantArgs)
	}
}

func TestDeepSeekClient_Stream_ReasoningContent(t *testing.T) {
	ssePayload := `data: {"id":"cmpl-3","choices":[{"index":0,"delta":{"reasoning_content":"Let me think..."},"finish_reason":null}]}

data: {"id":"cmpl-3","choices":[{"index":0,"delta":{"content":"The answer is 42"},"finish_reason":null}]}

data: {"id":"cmpl-3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":8,"total_tokens":13}}

data: [DONE]

`
	srv := newTestServer(t, http.StatusOK, ssePayload)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	stream, err := client.Stream(context.Background(), ChatRequest{
		Model:           "deepseek-v4-pro",
		Messages:        []Message{{Role: "user", Content: "what is the meaning?"}},
		ThinkingEnabled: true,
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var reasoning, content strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		reasoning.WriteString(chunk.ReasoningDelta)
		content.WriteString(chunk.Delta)
	}

	if reasoning.String() != "Let me think..." {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "Let me think...")
	}
	if content.String() != "The answer is 42" {
		t.Errorf("content = %q, want %q", content.String(), "The answer is 42")
	}
}

func TestDeepSeekClient_Complete(t *testing.T) {
	ssePayload := `data: {"id":"cmpl-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Done"},"finish_reason":null}]}

data: {"id":"cmpl-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}

data: [DONE]

`
	srv := newTestServer(t, http.StatusOK, ssePayload)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	resp, err := client.Complete(context.Background(), ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "ok"}},
	})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if resp.Message.Content != "Done" {
		t.Errorf("content = %q, want %q", resp.Message.Content, "Done")
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage.TotalTokens != 4 {
		t.Errorf("total_tokens = %d, want 4", resp.Usage.TotalTokens)
	}
}

func TestDeepSeekClient_Stream_429Retry(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		payload := `data: {"id":"cmpl-r","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}

data: [DONE]

`
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	client := newTestClientWithRetry(t, srv.URL, RetryPolicy{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   50 * time.Millisecond,
		Factor:     2,
		Jitter:     0.1,
	})

	stream, err := client.Stream(context.Background(), ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "retry test"}},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	var content strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		content.WriteString(chunk.Delta)
	}
	if content.String() != "ok" {
		t.Errorf("content = %q, want %q", content.String(), "ok")
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3", attempts.Load())
	}
}

func TestDeepSeekClient_Stream_ContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	stream, err := client.Stream(ctx, ChatRequest{
		Model:    "deepseek-v4-flash",
		Messages: []Message{{Role: "user", Content: "timeout test"}},
	})
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	for chunk := range stream {
		if chunk.Err != nil {
			return
		}
	}
	t.Error("expected error from canceled context, got clean close")
}

func TestParseSSE(t *testing.T) {
	input := "data: {\"x\":1}\n\ndata: hello\n\n: keep-alive\n\ndata: [DONE]\n\n"
	reader := bufio.NewReader(strings.NewReader(input))

	var payloads []string
	err := parseSSE(reader, 0, func(payload string) error {
		payloads = append(payloads, payload)
		if payload == "[DONE]" {
			return errSSEDone
		}
		return nil
	})
	if err != errSSEDone {
		t.Fatalf("parseSSE error = %v, want errSSEDone", err)
	}
	if len(payloads) != 3 {
		t.Fatalf("payload count = %d, want 3", len(payloads))
	}
	if payloads[0] != `{"x":1}` {
		t.Errorf("payload[0] = %q, want %q", payloads[0], `{"x":1}`)
	}
	if payloads[2] != "[DONE]" {
		t.Errorf("payload[2] = %q, want %q", payloads[2], "[DONE]")
	}
}

// blockingReader never produces data and never returns EOF — it models a
// stalled connection where the server accepted the request but sends nothing.
type blockingReader struct{}

func (blockingReader) Read(p []byte) (int, error) {
	// Block forever; the test relies on parseSSE's idle watchdog to abort.
	select {}
}

// TestParseSSE_IdleTimeout verifies a stalled stream (no data line arrives)
// is aborted with ErrStreamIdle within ~idleTimeout, instead of blocking
// forever. This is the regression guard for the "hangs indefinitely when the
// API connection goes silent" bug.
func TestParseSSE_IdleTimeout(t *testing.T) {
	reader := bufio.NewReader(blockingReader{})
	start := time.Now()
	err := parseSSE(reader, 50*time.Millisecond, func(payload string) error {
		t.Fatalf("handler should not be called on a stalled stream")
		return nil
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("parseSSE error = %v, want ErrStreamIdle", err)
	}
	// Should trip shortly after the idle timeout, not hang.
	if elapsed > 2*time.Second {
		t.Fatalf("idle timeout took %s, want < 2s", elapsed)
	}
}

// TestParseSSE_IdleTimeoutDisabled verifies that idleTimeout=0 keeps the
// legacy unbounded behavior (no watchdog). We feed a normal stream and confirm
// it completes without timing out.
func TestParseSSE_IdleTimeoutDisabled(t *testing.T) {
	input := "data: {\"x\":1}\n\ndata: [DONE]\n\n"
	reader := bufio.NewReader(strings.NewReader(input))
	var n int
	err := parseSSE(reader, 0, func(payload string) error {
		n++
		if payload == "[DONE]" {
			return errSSEDone
		}
		return nil
	})
	if err != errSSEDone {
		t.Fatalf("parseSSE error = %v, want errSSEDone", err)
	}
	if n != 2 {
		t.Fatalf("payload count = %d, want 2", n)
	}
}

// TestParseSSE_IdleTimeoutResetsOnData verifies the watchdog resets on each
// successful read — a slow-but-alive stream (a line every < idleTimeout) must
// not trip the timeout even if its total duration exceeds idleTimeout.
func TestParseSSE_IdleTimeoutResetsOnData(t *testing.T) {
	// Emit 3 SSE events spaced 30ms apart, with an 80ms idle timeout. Each
	// gap (30ms) is under the timeout, but the total (~90ms) exceeds it —
	// a non-resetting watchdog would falsely trip. The watchdog must reset
	// per line and let the stream complete.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		frames := []string{"data: a\n\n", "data: b\n\n", "data: [DONE]\n\n"}
		for _, f := range frames {
			time.Sleep(30 * time.Millisecond)
			pw.Write([]byte(f))
		}
	}()
	reader := bufio.NewReader(pr)
	var n int
	err := parseSSE(reader, 80*time.Millisecond, func(payload string) error {
		n++
		if payload == "[DONE]" {
			return errSSEDone
		}
		return nil
	})
	if err != errSSEDone {
		t.Fatalf("parseSSE error = %v, want errSSEDone (stream was alive)", err)
	}
	if n != 3 {
		t.Fatalf("payload count = %d, want 3", n)
	}
}

func TestStreamAssembler_ToolCallMerge(t *testing.T) {
	asm := newStreamAssembler()

	chunk1 := streamResponse{
		Choices: []streamChoice{{
			Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index:    0,
					ID:       "call_1",
					Type:     "function",
					Function: streamFunction{Name: "bash", Arguments: ""},
				}},
			},
		}},
	}
	chunk2 := streamResponse{
		Choices: []streamChoice{{
			Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index:    0,
					Function: streamFunction{Arguments: `{"cmd":`},
				}},
			},
		}},
	}
	chunk3 := streamResponse{
		Choices: []streamChoice{{
			Delta: streamDelta{
				ToolCalls: []streamToolCall{{
					Index:    0,
					Function: streamFunction{Arguments: `"ls"}`},
				}},
			},
		}},
	}

	asm.consume(chunk1)
	asm.consume(chunk2)
	result, _ := asm.consume(chunk3)

	if result == nil {
		t.Fatal("expected chunk, got nil")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("id = %q, want %q", tc.ID, "call_1")
	}
	if tc.Function.Name != "bash" {
		t.Errorf("name = %q, want %q", tc.Function.Name, "bash")
	}
	wantArgs := `{"cmd":"ls"}`
	if tc.Function.Arguments != wantArgs {
		t.Errorf("args = %q, want %q", tc.Function.Arguments, wantArgs)
	}
}

func TestBuildRequestBody_ThinkingMode(t *testing.T) {
	client := &DeepSeekClient{}
	body, err := client.buildRequestBody(ChatRequest{
		Model:           "deepseek-v4-pro",
		Messages:        []Message{{Role: "user", Content: "think"}},
		ThinkingEnabled: true,
		ReasoningEffort: "high",
		JsonMode:        true,
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if parsed["stream"] != true {
		t.Error("expected stream=true")
	}
	if parsed["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", parsed["reasoning_effort"])
	}

	eb, ok := parsed["extra_body"].(map[string]interface{})
	if !ok {
		t.Fatal("extra_body missing or not object")
	}
	thinking, ok := eb["thinking"].(map[string]interface{})
	if !ok {
		t.Fatal("thinking missing or not object")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}

	rf, ok := parsed["response_format"].(map[string]interface{})
	if !ok {
		t.Fatal("response_format missing")
	}
	if rf["type"] != "json_object" {
		t.Errorf("response_format.type = %v, want json_object", rf["type"])
	}
}

func TestValidateToolCallResponses_BackfillsOrphanedIDs(t *testing.T) {
	// assistant emitted two tool_call_ids but only one has a tool response.
	// The missing one must be backfilled with a placeholder tool message.
	msgs := []Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "call_a", Type: "function", Function: FunctionCall{Name: "read"}},
			{ID: "call_b", Type: "function", Function: FunctionCall{Name: "edit"}},
		}},
		{Role: "tool", ToolCallID: "call_b", Content: "edited"},
	}
	out := validateToolCallResponses(msgs)

	// Expect: user, assistant, tool(call_b), tool(call_a backfilled)
	if len(out) != 4 {
		t.Fatalf("len(out) = %d, want 4: %+v", len(out), out)
	}
	if out[2].ToolCallID != "call_b" {
		t.Errorf("out[2].ToolCallID = %q, want call_b", out[2].ToolCallID)
	}
	if out[3].Role != "tool" || out[3].ToolCallID != "call_a" {
		t.Errorf("out[3] = %+v, want backfilled tool for call_a", out[3])
	}
}

func TestValidateToolCallResponses_AllAnswered(t *testing.T) {
	// Both ids answered — no backfill, no duplication.
	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: "tool", ToolCallID: "a", Content: "ra"},
		{Role: "tool", ToolCallID: "b", Content: "rb"},
	}
	out := validateToolCallResponses(msgs)
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3 (no backfill)", len(out))
	}
}

func TestValidateToolCallResponses_NoToolCalls(t *testing.T) {
	// Plain messages with no tool_calls pass through unchanged.
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	out := validateToolCallResponses(msgs)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
}

func TestBuildRequestBody_BackfillsOrphanedToolCallID(t *testing.T) {
	// End-to-end: buildRequestBody must produce a valid message sequence even
	// when an assistant tool_call has no matching tool response.
	client := &DeepSeekClient{}
	body, err := client.buildRequestBody(ChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []Message{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "orphan", Type: "function", Function: FunctionCall{Name: "read"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	var parsed struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	// Expect: user, assistant(tool_calls), tool(orphan)
	if len(parsed.Messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3", len(parsed.Messages))
	}
	last := parsed.Messages[2]
	if last.Role != "tool" || last.ToolCallID != "orphan" {
		t.Errorf("last message = %+v, want backfilled tool for orphan", last)
	}
}

func newTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func newTestClient(t *testing.T, baseURL string) *DeepSeekClient {
	t.Helper()
	c := NewDeepSeekClient(
		"test-key",
		&http.Client{Timeout: 5 * time.Second},
		NewAdaptiveLimiter(5, 10, 1, 100, 10),
		DefaultRetryPolicy(),
		NewTokenEstimator(),
	)
	overrideEndpoint(c, baseURL)
	return c
}

func newTestClientWithRetry(t *testing.T, baseURL string, retry RetryPolicy) *DeepSeekClient {
	t.Helper()
	c := NewDeepSeekClient(
		"test-key",
		&http.Client{Timeout: 5 * time.Second},
		NewAdaptiveLimiter(5, 10, 1, 100, 10),
		retry,
		NewTokenEstimator(),
	)
	overrideEndpoint(c, baseURL)
	return c
}

func overrideEndpoint(c *DeepSeekClient, url string) {
	c.endpoint = url
}

func TestChatCompletionsURL(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{"empty", "", ""},
		{"deepseek base", "https://api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		{"deepseek base trailing slash", "https://api.deepseek.com/", "https://api.deepseek.com/chat/completions"},
		{"volcano coding v3", "https://ark.cn-beijing.volces.com/api/coding/v3", "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions"},
		{"openrouter", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1/chat/completions"},
		{"already full idempotent", "https://api.deepseek.com/chat/completions", "https://api.deepseek.com/chat/completions"},
		{"sub-agent query preserved", "https://api.deepseek.com?sub=1", "https://api.deepseek.com/chat/completions?sub=1"},
		{"volcano sub-agent query", "https://ark.cn-beijing.volces.com/api/coding/v3?sub=1", "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions?sub=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatCompletionsURL(tt.base); got != tt.want {
				t.Errorf("chatCompletionsURL(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestValidateReasoningEcho_FillsMissing(t *testing.T) {
	client := NewDeepSeekClient("test-key", nil, nil, DefaultRetryPolicy(), nil)

	// Simulate that manager observed a reasoning
	client.reasoningMgr.ObserveAssistant(Message{
		Role:             "assistant",
		ReasoningContent: "deep reasoning here",
		ToolCalls:        []ToolCall{{ID: "call_1", Function: FunctionCall{Name: "bash"}}},
	})

	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_2"}}}, // missing reasoning
	}

	got := client.validateReasoningEcho(msgs)
	if got[1].ReasoningContent != "deep reasoning here" {
		t.Errorf("msgs[1].ReasoningContent = %q, want %q", got[1].ReasoningContent, "deep reasoning here")
	}
}

func TestValidateReasoningEcho_FallbackPlaceholder(t *testing.T) {
	client := NewDeepSeekClient("test-key", nil, nil, DefaultRetryPolicy(), nil)
	// manager never observed anything — no pending echo

	msgs := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1"}}, ReasoningContent: ""},
	}

	got := client.validateReasoningEcho(msgs)
	if got[0].ReasoningContent != ".." {
		t.Errorf("msgs[0].ReasoningContent = %q, want placeholder %q", got[0].ReasoningContent, "..")
	}
}

func TestValidateReasoningEcho_SkipsClean(t *testing.T) {
	client := NewDeepSeekClient("test-key", nil, nil, DefaultRetryPolicy(), nil)

	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ToolCalls: []ToolCall{{ID: "c1"}}, ReasoningContent: "ok"},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}

	got := client.validateReasoningEcho(msgs)
	if got[1].ReasoningContent != "ok" {
		t.Errorf("clean message corrupted: got %q", got[1].ReasoningContent)
	}
	if got[2].ReasoningContent != "" {
		t.Errorf("tool message should not be touched: got %q", got[2].ReasoningContent)
	}
}
