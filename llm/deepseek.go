package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	dlog "github.com/deepact/deepact/internal/log"
)

var debugLog = dlog.New("[llm] ")

const (
	// DefaultDeepSeekEndpoint is the DeepSeek official API base URL (no path).
	// Users may override it via [model].base_url in config (e.g. to point at the
	// Volcano Engine Ark coding plan: https://ark.cn-beijing.volces.com/api/coding/v3).
	// The "/chat/completions" path is appended by chatCompletionsURL at request time.
	DefaultDeepSeekEndpoint = "https://api.deepseek.com"
	DefaultOpenRouterURL    = "https://openrouter.ai/api/v1"

	// DefaultIdleTimeout caps how long a streaming response may stay silent
	// (no SSE data line) before the read is aborted as a stalled connection.
	// Generous enough to never trip on a slow-but-alive model (reasoning
	// pauses, long ttft after headers), short enough that a truly hung
	// connection fails in ~1 minute instead of blocking forever. Override per
	// client via SetIdleTimeout; 0 disables.
	DefaultIdleTimeout = 60 * time.Second
	// DefaultResponseHeaderTimeout caps how long the client waits for the
	// response headers (HTTP status line + headers) after sending the request.
	// This is the TTFT-before-any-bytes phase; a server that accepts the TCP
	// connection but never responds with headers hangs here indefinitely
	// without it. Streaming bodies are NOT bounded by this (the idleTimeout
	// watchdog handles mid-stream stalls).
	DefaultResponseHeaderTimeout = 120 * time.Second
	// DefaultDialTimeout caps the TCP connection establishment phase. Without
	// it, a server that is reachable but never completes the handshake (or a
	// black-holed route) hangs the very first byte indefinitely. Generous
	// enough for slow mobile/satellite links, short enough to fail fast.
	DefaultDialTimeout = 30 * time.Second
)

// chatCompletionsURL normalizes an API base URL into a full chat completions
// POST endpoint. Users configure a base such as "https://api.deepseek.com" or
// "https://ark.cn-beijing.volces.com/api/coding/v3"; the "/chat/completions"
// path is appended here, preserving any query string (e.g. "?sub=1" used for
// sub-agent prefix-cache isolation). Idempotent: a base already ending in
// "/chat/completions" is returned unchanged.
func chatCompletionsURL(base string) string {
	if base == "" {
		return ""
	}
	path, query, _ := strings.Cut(base, "?")
	if strings.HasSuffix(path, "/chat/completions") {
		return base
	}
	path = strings.TrimSuffix(path, "/") + "/chat/completions"
	if query != "" {
		return path + "?" + query
	}
	return path
}

var errSSEDone = errors.New("sse done")

type DeepSeekClient struct {
	apiKey       string
	endpoint     string
	http         *http.Client
	limiter      *AdaptiveLimiter
	retry        RetryPolicy
	estimator    *TokenEstimator
	reasoningMgr *ReasoningEchoManager
	// idleTimeout is the max time allowed between two SSE data lines during a
	// streaming response. Streaming LLM output can legitimately pause between
	// tokens, but a connection that goes silent for this long is almost
	// certainly stalled (server hung, connection half-open). Tripping it
	// returns ErrTimeout so the caller can retry instead of waiting forever.
	// 0 disables the watchdog (legacy behavior). See streamOnce/parseSSE.
	idleTimeout time.Duration
}

// Fork creates a new DeepSeekClient sharing the same HTTP client, limiter, retry policy,
// and token estimator, but with an independent ReasoningEchoManager.
// This prevents reasoning_content cross-contamination between nested agent calls
// (e.g. main agent → sub-agent reasoning leaking into the wrong context).
func (c *DeepSeekClient) Fork() *DeepSeekClient {
	return &DeepSeekClient{
		apiKey:       c.apiKey,
		endpoint:     c.endpoint,
		http:         c.http,
		limiter:      c.limiter,
		retry:        c.retry,
		estimator:    c.estimator,
		reasoningMgr: NewReasoningEchoManager(), // fresh, independent manager
		idleTimeout:  c.idleTimeout,
	}
}

// ForkWithEndpoint creates a new DeepSeekClient sharing the same HTTP client, limiter,
// retry policy, and token estimator, but with an independent ReasoningEchoManager AND
// a different API endpoint. Used by sub-agents to isolate their prefix cache partition
// from the main agent's, preventing sub-agent calls from polluting the main agent's
// DeepSeek server-side prefix cache.
func (c *DeepSeekClient) ForkWithEndpoint(endpoint string) *DeepSeekClient {
	return &DeepSeekClient{
		apiKey:       c.apiKey,
		endpoint:     chatCompletionsURL(endpoint),
		http:         c.http,
		limiter:      c.limiter,
		retry:        c.retry,
		estimator:    c.estimator,
		reasoningMgr: NewReasoningEchoManager(), // fresh, independent manager
		idleTimeout:  c.idleTimeout,
	}
}

func NewDeepSeekClient(apiKey string, httpClient *http.Client, limiter *AdaptiveLimiter, retry RetryPolicy, estimator *TokenEstimator) *DeepSeekClient {
	return NewDeepSeekClientWithEndpoint(DefaultDeepSeekEndpoint, apiKey, httpClient, limiter, retry, estimator)
}

// NewDeepSeekClientWithEndpoint creates a DeepSeekClient with a custom endpoint URL.
// For OpenRouter, pass "https://openrouter.ai/api/v1" as baseURL — it replaces the
// default DeepSeek endpoint and configures OpenRouter-specific headers.
func NewDeepSeekClientWithEndpoint(baseURL, apiKey string, httpClient *http.Client, limiter *AdaptiveLimiter, retry RetryPolicy, estimator *TokenEstimator) *DeepSeekClient {
	if httpClient == nil {
		// No whole-request Timeout on the HTTP client — streaming LLM responses
		// can legitimately take minutes (thinking + generation), and a hard
		// http.Client.Timeout would abort healthy long streams. Instead, two
		// narrower guards bound stalls without killing slow-but-alive streams:
		//   1. ResponseHeaderTimeout — headers must arrive within this after
		//      the request is sent (covers a server that accepts the TCP
		//      connection but never responds).
		//   2. idleTimeout (per-client, enforced in parseSSE) — no SSE data line
		//      may be silent for longer than this mid-stream.
		// Context cancellation from the caller (user cancel, turn boundary)
		// remains the primary control.
		httpClient = &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				// DialContext bounds TCP connection establishment + TLS
				// handshake. A black-holed route or a server that never
				// completes the handshake would otherwise hang the request
				// before any timeout can fire.
				DialContext: (&net.Dialer{
					Timeout:   DefaultDialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   DefaultDialTimeout,
				ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
				// IdleConnTimeout recycles pooled connections that have gone
				// stale (server closed without notice) so a reused half-open
				// connection doesn't silently hang the next request.
				IdleConnTimeout: 90 * time.Second,
			},
		}
	}
	if limiter == nil {
		limiter = NewAdaptiveLimiter(5, 10, 1, 10, 5)
	}
	if retry.MaxRetries == 0 {
		retry = DefaultRetryPolicy()
	}
	if estimator == nil {
		estimator = NewTokenEstimator()
	}
	endpoint := chatCompletionsURL(baseURL)
	return &DeepSeekClient{
		apiKey:       apiKey,
		endpoint:     endpoint,
		http:         httpClient,
		limiter:      limiter,
		retry:        retry,
		estimator:    estimator,
		reasoningMgr: NewReasoningEchoManager(),
		idleTimeout:  DefaultIdleTimeout,
	}
}

// SetIdleTimeout overrides the per-client SSE idle timeout. Pass 0 to disable
// the watchdog entirely (restores legacy block-forever behavior). Must be set
// before the first request; changes do not affect in-flight streams.
func (c *DeepSeekClient) SetIdleTimeout(d time.Duration) {
	if c == nil {
		return
	}
	c.idleTimeout = d
}

// isOpenRouterKey returns true if the API key is an OpenRouter key (starts with "sk-or-v1-")
func isOpenRouterKey(apiKey string) bool {
	return strings.HasPrefix(apiKey, "sk-or-v1-")
}

// DetectBaseURL picks the API base URL based on the key prefix and configured BaseURL.
// If a baseURL is explicitly configured, it takes priority over key-based detection.
func DetectBaseURL(configuredBaseURL, apiKey string) string {
	if configuredBaseURL != "" {
		return configuredBaseURL
	}
	if isOpenRouterKey(apiKey) {
		return DefaultOpenRouterURL
	}
	return DefaultDeepSeekEndpoint
}

// SubAgentEndpoint returns an API base URL for sub-agents that differs from the main
// agent's endpoint. This creates a separate prefix cache partition on DeepSeek's server,
// preventing sub-agent calls from polluting the main agent's cached prefix.
// The derivation appends a harmless query parameter that changes the cache key without
// affecting request routing or behavior.
func SubAgentEndpoint(configuredBaseURL, apiKey string) string {
	mainEndpoint := DetectBaseURL(configuredBaseURL, apiKey)
	if strings.Contains(mainEndpoint, "?") {
		return mainEndpoint + "&sub=1"
	}
	return mainEndpoint + "?sub=1"
}

func (c *DeepSeekClient) Stream(ctx context.Context, req ChatRequest) (<-chan Chunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("stream: %w", classifyContextError(err))
	}
	acqStart := time.Now()
	if err := c.limiter.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("acquire limiter: %w", err)
	}
	if d := time.Since(acqStart); d > 50*time.Millisecond {
		debugLog.Printf("limiter acquire blocked for %s (slots=%d)", d, c.limiter.Slots())
	}
	req.Messages = c.reasoningMgr.ApplyEcho(req.Messages)
	ch := make(chan Chunk, 16)
	go func() {
		defer c.limiter.Release()
		defer close(ch)
		if err := c.streamWithRetry(ctx, req, ch); err != nil {
			ch <- Chunk{Err: err}
		}
	}()
	return ch, nil
}

func (c *DeepSeekClient) Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	stream, err := c.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	assembler := newStreamAssembler()
	for chunk := range stream {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		assembler.apply(chunk)
	}
	resp := assembler.buildResponse(req.Model)
	if resp == nil {
		return nil, fmt.Errorf("complete: %w", ErrInvalidResponse)
	}
	return resp, nil
}

func (c *DeepSeekClient) streamWithRetry(ctx context.Context, req ChatRequest, ch chan<- Chunk) error {
	var retryLog []string
	for attempt := 0; attempt <= c.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			backoffStart := time.Now()
			if err := c.retry.Sleep(ctx, attempt); err != nil {
				return fmt.Errorf("backoff: %w", classifyContextError(err))
			}
			backoffDur := time.Since(backoffStart)
			debugLog.Printf("retry attempt=%d after backoff=%s", attempt, backoffDur)
			retryLog = append(retryLog, fmt.Sprintf("retry %d/%d (waited %s)", attempt, c.retry.MaxRetries, backoffDur.Round(time.Millisecond)))
			// Send retry progress to the caller so the UI can show it in real time.
			select {
			case ch <- Chunk{RetryProgress: fmt.Sprintf("Retrying %d/%d after %s...", attempt, c.retry.MaxRetries, backoffDur.Round(time.Millisecond))}:
			default:
			}
		}
		status, err := c.streamOnce(ctx, req, ch)
		if err == nil {
			if attempt > 0 {
				debugLog.Printf("stream succeeded on attempt=%d", attempt)
			}
			return nil
		}
		debugLog.Printf("streamOnce failed attempt=%d status=%d err=%v", attempt, status, err)
		retryLog = append(retryLog, fmt.Sprintf("attempt %d: %v", attempt+1, err))
		// Context errors are not retryable — the caller cancelled or deadline passed
		if errors.Is(err, ErrContextCanceled) || errors.Is(err, ErrTimeout) {
			return err
		}
		if errors.Is(err, ErrRateLimit) {
			c.limiter.Record429()
		}
		if !c.retry.ShouldRetry(status) || attempt == c.retry.MaxRetries {
			if len(retryLog) > 1 {
				return fmt.Errorf("%w\n\n%s", err, strings.Join(retryLog, "\n"))
			}
			return err
		}
	}
	return ErrInvalidResponse
}

func (c *DeepSeekClient) streamOnce(ctx context.Context, req ChatRequest, ch chan<- Chunk) (status int, err error) {
	start := time.Now()
	var headersAt time.Time
	var firstTokenAt time.Time
	var chunkCount int
	var bodyLen int
	var doneUsage *Usage
	defer func() {
		total := time.Since(start)
		tHeaders := time.Duration(0)
		ttft := time.Duration(0)
		tStream := time.Duration(0)
		if !headersAt.IsZero() {
			tHeaders = headersAt.Sub(start)
		}
		if !firstTokenAt.IsZero() {
			ttft = firstTokenAt.Sub(start)
			tStream = time.Since(firstTokenAt)
		}
		usageStr := "n/a"
		if doneUsage != nil {
			usageStr = fmt.Sprintf("prompt=%d completion=%d cache_hit=%d cache_miss=%d",
				doneUsage.PromptTokens, doneUsage.CompletionTokens,
				doneUsage.PromptCacheHitTokens, doneUsage.PromptCacheMissTokens)
		}
		debugLog.Printf("streamOnce: model=%s msgs=%d body_bytes=%d status=%d chunks=%d total=%s t_headers=%s ttft=%s t_stream=%s usage=%s err=%v",
			req.Model, len(req.Messages), bodyLen, status, chunkCount, total, tHeaders, ttft, tStream, usageStr, err)
	}()

	body, err := c.buildRequestBody(req)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	bodyLen = len(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	// OpenRouter-specific headers (recommended for identification/tracking)
	if strings.Contains(c.endpoint, "openrouter.ai") {
		httpReq.Header.Set("HTTP-Referer", "https://github.com/deepact/deepact")
		httpReq.Header.Set("X-Title", "DeepAct")
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("send request: %w", classifyContextError(err))
	}
	headersAt = time.Now()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		debugLog.Printf("API returned %d, body=%q (len=%d)", resp.StatusCode, string(body), len(body))
		// hex dump the body to catch non-printable characters
		debugLog.Printf("API body hex: %x", body)
		return resp.StatusCode, fmt.Errorf("status %d: %s: %w", resp.StatusCode, string(body), classifyStatusError(resp.StatusCode))
	}
	reader := bufio.NewReader(resp.Body)
	assembler := newStreamAssembler()
	if err := parseSSE(reader, c.idleTimeout, func(payload string) error {
		if payload == "[DONE]" {
			if usage := assembler.usage(); usage != nil {
				doneUsage = usage
				c.estimator.Calibrate(assembler.promptText(req), *usage)
			}
			c.limiter.RecordSuccess()
			return errSSEDone
		}
		var delta streamResponse
		if err := json.Unmarshal([]byte(payload), &delta); err != nil {
			return fmt.Errorf("decode stream: %w", ErrInvalidResponse)
		}
		chunk, err := assembler.consume(delta)
		if err != nil {
			return err
		}
		if chunk != nil {
			chunkCount++
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
			}
			ch <- *chunk
		}
		return nil
	}); err != nil {
		if errors.Is(err, errSSEDone) {
			// Observe the assembled message for reasoning echo on next turn
			if c.reasoningMgr != nil {
				if obsMsg := assembler.observeMessage(); obsMsg != nil {
					c.reasoningMgr.ObserveAssistant(*obsMsg)
				}
			}
			return http.StatusOK, nil
		}
		if errors.Is(err, io.EOF) {
			return http.StatusOK, fmt.Errorf("stream closed: %w", ErrInvalidResponse)
		}
		// Idle timeout (stream went silent) — report status 0 so the retry
		// layer treats it as a transient network error and re-attempts, rather
		// than surfacing a hang to the user. Distinguished from ErrTimeout
		// (a hard deadline, not retried) by the ErrStreamIdle sentinel.
		if errors.Is(err, ErrStreamIdle) {
			return 0, fmt.Errorf("stream idle: %w", ErrStreamIdle)
		}
		return http.StatusOK, err
	}
	return http.StatusOK, nil
}

// validateReasoningEcho is a pre-flight check that runs after ApplyEcho.
// It only scans the LAST assistant message with tool_calls for missing
// ReasoningContent — never modifies history messages, preserving their
// stable JSON serialization for prefix cache hits.
// If missing, it attempts recovery using the manager's lastReasoning
// or a fallback placeholder ".." to prevent a 400 from DeepSeek API.
func (c *DeepSeekClient) validateReasoningEcho(msgs []Message) []Message {
	var fixed bool
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			if msgs[i].ReasoningContent != "" {
				break // found last assistant with tool_calls AND reasoning OK
			}
			// Try manager's lastReasoning first
			if c.reasoningMgr != nil {
				if pending, ok := c.reasoningMgr.PendingEcho(); ok {
					debugLog.Printf("pre-flight fix: filling reasoning_content (from manager) at msgs[%d]", i)
					msgs[i].ReasoningContent = pending
					fixed = true
					break
				}
			}
			// Fallback: use ".." placeholder to prevent 400
			debugLog.Printf("pre-flight fix: filling reasoning_content (placeholder) at msgs[%d]", i)
			msgs[i].ReasoningContent = ".."
			fixed = true
			break
		}
	}
	if fixed {
		debugLog.Println("pre-flight: reasoning_content fixed")
	}
	return msgs
}

// validateAssistantContent ensures all assistant messages have at least content or tool_calls.
// DeepSeek/OpenAI API rejects assistant messages with neither field set (400 error).
// This handles cases where the model returned only reasoning_content with no visible output.
func (c *DeepSeekClient) validateAssistantContent(msgs []Message) []Message {
	for i := range msgs {
		if msgs[i].Role != "assistant" {
			continue
		}
		if msgs[i].Content != "" || len(msgs[i].ToolCalls) > 0 {
			continue
		}
		msgs[i].Content = ".."
		debugLog.Printf("pre-flight fix: filling empty assistant content at msgs[%d]", i)
	}
	return msgs
}

// validateToolCallResponses ensures every tool_call_id emitted by an assistant
// message is answered by a subsequent tool message. DeepSeek/OpenAI APIs reject
// requests where an assistant message with tool_calls is not followed by tool
// messages responding to each tool_call_id (400 "insufficient tool messages
// following tool_calls message").
//
// This is a defensive backfill: it scans the message list and, for any assistant
// message whose tool_call_ids are not all answered before the next assistant or
// user message, inserts placeholder tool messages with the missing ids. This
// guarantees the request is always schema-valid regardless of how upstream code
// assembled the history (e.g. read-only calls skipped during plan replay).
func validateToolCallResponses(msgs []Message) []Message {
	// First pass: collect the set of tool_call_ids that already have a response.
	answered := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}

	// Second pass: walk forward and backfill unanswered ids in place. We must
	// insert placeholders immediately after the assistant message that emitted
	// them (and after any tool messages that already follow it), to preserve the
	// required ordering.
	result := make([]Message, 0, len(msgs)+4)
	for i := 0; i < len(msgs); i++ {
		result = append(result, msgs[i])
		if msgs[i].Role != "assistant" || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		// Collect this assistant message's tool_call_ids.
		var ids []string
		for _, tc := range msgs[i].ToolCalls {
			if tc.ID != "" {
				ids = append(ids, tc.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		// Carry forward any tool messages that already follow this assistant msg
		// (they answer some of the ids). We append them as-is, then backfill the
		// rest. Stop at the next non-tool message.
		for i+1 < len(msgs) && msgs[i+1].Role == "tool" {
			result = append(result, msgs[i+1])
			i++
		}
		// Backfill any ids that still have no response.
		var missing []string
		for _, id := range ids {
			if !answered[id] {
				missing = append(missing, id)
			}
		}
		for _, id := range missing {
			debugLog.Printf("pre-flight fix: backfilling missing tool response for tool_call_id=%s", id)
			result = append(result, Message{
				Role:       "tool",
				ToolCallID: id,
				Content:    "Skipped: no tool result was produced for this call.",
			})
			answered[id] = true
		}
	}
	return result
}

func (c *DeepSeekClient) buildRequestBody(req ChatRequest) ([]byte, error) {
	// Pre-send validation: ensure assistant messages with tool_calls have reasoning_content.
	// Uses ReasoningEchoManager for stateful echo instead of scanning the message list.
	if c.reasoningMgr != nil {
		req.Messages = c.reasoningMgr.ApplyEcho(req.Messages)
	}

	// Pre-flight check: after ApplyEcho, verify no assistant+tool_calls message is still
	// missing reasoning_content. If found, auto-fix before sending to prevent 400.
	req.Messages = c.validateReasoningEcho(req.Messages)

	// Pre-flight check: ensure all assistant messages have at least content or tool_calls.
	// The API requires one of these to be set; messages with only reasoning_content are invalid.
	req.Messages = c.validateAssistantContent(req.Messages)

	// Pre-flight check: every tool_call_id in an assistant message must be answered by
	// a following tool message. If any call went unanswered (e.g. a read-only call was
	// skipped during plan-confirmation replay), DeepSeek rejects the request with
	// 400 "insufficient tool messages following tool_calls message". We backfill
	// placeholder tool messages for any orphaned ids so the request is always valid.
	req.Messages = validateToolCallResponses(req.Messages)

	payload := requestBody{
		Model:           req.Model,
		Messages:        req.Messages,
		Tools:           req.Tools,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: req.ReasoningEffort,
		Stream:          true,
	}
	// When tools are present, request parallel tool calls so the model can emit
	// multiple tool_use blocks (e.g. several reads) in a single response. Without
	// this, DeepSeek defaults to one tool call per turn, which turns reading N
	// files into N rapid sequential requests and triggers rate limits.
	if len(req.Tools) > 0 {
		t := true
		payload.ParallelToolCalls = &t
	}
	if req.JsonMode {
		payload.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	if req.ThinkingEnabled {
		payload.ExtraBody = &extraBody{Thinking: &thinkingBody{Type: "enabled"}}
	}
	return json.Marshal(payload)
}

func classifyStatusError(status int) error {
	if status == http.StatusTooManyRequests {
		return ErrRateLimit
	}
	if status >= 500 && status <= 599 {
		return fmt.Errorf("server error (%d): %w", status, ErrInvalidResponse)
	}
	return fmt.Errorf("unexpected status %d: %w", status, ErrInvalidResponse)
}

func classifyContextError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return ErrContextCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return err
}

type requestBody struct {
	Model              string          `json:"model"`
	Messages           []Message       `json:"messages"`
	Tools              []ToolDef       `json:"tools,omitempty"`
	Temperature        float64         `json:"temperature,omitempty"`
	MaxTokens          int             `json:"max_tokens,omitempty"`
	ReasoningEffort    string          `json:"reasoning_effort,omitempty"`
	Stream             bool            `json:"stream"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	ResponseFormat     *responseFormat `json:"response_format,omitempty"`
	ExtraBody          *extraBody      `json:"extra_body,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type extraBody struct {
	Thinking *thinkingBody `json:"thinking,omitempty"`
}

type thinkingBody struct {
	Type string `json:"type"`
}

type streamResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *Usage         `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type streamDelta struct {
	Role             string           `json:"role"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content"`
	ToolCalls        []streamToolCall `json:"tool_calls"`
}

type streamToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function streamFunction `json:"function"`
}

type streamFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type streamAssembler struct {
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    []ToolCall
	finishReason string
	usageState   *Usage
}

func newStreamAssembler() *streamAssembler {
	return &streamAssembler{}
}

func (s *streamAssembler) consume(resp streamResponse) (*Chunk, error) {
	if resp.Usage != nil {
		s.usageState = resp.Usage
	}
	if len(resp.Choices) == 0 {
		if resp.Usage != nil {
			return &Chunk{Usage: resp.Usage}, nil
		}
		return nil, nil
	}
	var chunk Chunk
	for _, choice := range resp.Choices {
		if choice.Delta.Content != "" {
			s.content.WriteString(choice.Delta.Content)
			chunk.Delta += choice.Delta.Content
		}
		if choice.Delta.ReasoningContent != "" {
			s.reasoning.WriteString(choice.Delta.ReasoningContent)
			chunk.ReasoningDelta += choice.Delta.ReasoningContent
		}
		if len(choice.Delta.ToolCalls) > 0 {
			chunk.ToolCalls = s.mergeToolCalls(choice.Delta.ToolCalls)
		}
		if choice.FinishReason != "" {
			s.finishReason = choice.FinishReason
			chunk.FinishReason = choice.FinishReason
		}
	}
	if resp.Usage != nil {
		chunk.Usage = resp.Usage
	}
	if chunk.Delta == "" && chunk.ReasoningDelta == "" && len(chunk.ToolCalls) == 0 && chunk.FinishReason == "" && chunk.Usage == nil {
		return nil, nil
	}
	return &chunk, nil
}

func (s *streamAssembler) mergeToolCalls(deltas []streamToolCall) []ToolCall {
	for _, delta := range deltas {
		if delta.Index < 0 {
			continue
		}
		for len(s.toolCalls) <= delta.Index {
			s.toolCalls = append(s.toolCalls, ToolCall{})
		}
		call := &s.toolCalls[delta.Index]
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Type != "" {
			call.Type = delta.Type
		}
		if delta.Function.Name != "" {
			call.Function.Name = delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			call.Function.Arguments += delta.Function.Arguments
		}
	}
	copyCalls := make([]ToolCall, len(s.toolCalls))
	copy(copyCalls, s.toolCalls)
	return copyCalls
}

func (s *streamAssembler) apply(chunk Chunk) {
	if chunk.Delta != "" {
		s.content.WriteString(chunk.Delta)
	}
	if chunk.ReasoningDelta != "" {
		s.reasoning.WriteString(chunk.ReasoningDelta)
	}
	if chunk.FinishReason != "" {
		s.finishReason = chunk.FinishReason
	}
	if chunk.Usage != nil {
		s.usageState = chunk.Usage
	}
	if len(chunk.ToolCalls) > 0 {
		s.toolCalls = chunk.ToolCalls
	}
}

func (s *streamAssembler) usage() *Usage {
	return s.usageState
}

func (s *streamAssembler) buildResponse(model string) *ChatResponse {
	if s.content.Len() == 0 && s.reasoning.Len() == 0 && len(s.toolCalls) == 0 {
		return nil
	}
	msg := Message{
		Role:             "assistant",
		Content:          s.content.String(),
		ReasoningContent: s.reasoning.String(),
		ToolCalls:        s.toolCalls,
	}
	usage := Usage{}
	if s.usageState != nil {
		usage = *s.usageState
	}
	return &ChatResponse{
		Model:        model,
		Message:      msg,
		FinishReason: s.finishReason,
		Usage:        usage,
	}
}

func (s *streamAssembler) promptText(req ChatRequest) string {
	var builder strings.Builder
	for _, msg := range req.Messages {
		builder.WriteString(msg.Content)
		builder.WriteString(msg.ReasoningContent)
	}
	return builder.String()
}

// observeMessage returns a Message with the assembled reasoning and tool calls.
// Used by ReasoningEchoManager.ObserveAssistant after a successful stream.
// Returns nil if there's nothing meaningful to observe (no reasoning, no tool calls).
func (s *streamAssembler) observeMessage() *Message {
	if s.reasoning.Len() == 0 && len(s.toolCalls) == 0 {
		return nil
	}
	return &Message{
		Role:             "assistant",
		ReasoningContent: s.reasoning.String(),
		ToolCalls:        s.toolCalls,
	}
}

// parseSSE reads an SSE stream line by line, invoking handle for each
// "data: " payload. idleTimeout bounds how long a read may block without
// producing a line: if no line arrives within idleTimeout, the read is
// aborted (ErrTimeout) so a stalled connection fails fast instead of hanging
// forever. Pass 0 to disable the watchdog. The timeout is reset on every
// successful read, so it measures inter-line silence, not total stream time —
// a slow-but-alive stream that emits a line every few seconds never trips it.
func parseSSE(reader *bufio.Reader, idleTimeout time.Duration, handle func(payload string) error) error {
	if idleTimeout <= 0 {
		return parseSSEUnbounded(reader, handle)
	}
	for {
		line, err := readLineWithIdleTimeout(reader, idleTimeout)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if err := handle(payload); err != nil {
			return err
		}
	}
}

// parseSSEUnbounded is the legacy no-timeout path (idleTimeout disabled).
func parseSSEUnbounded(reader *bufio.Reader, handle func(payload string) error) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if err := handle(payload); err != nil {
			return err
		}
	}
}

// readLineWithIdleTimeout reads one line, aborting with ErrTimeout if no data
// arrives within idleTimeout. Implementation: a watchdog goroutine sleeps for
// idleTimeout; each call starts a fresh watchdog and the previous one is
// cancelled via its own context when this function returns (success or EOF).
// Because reader.ReadString blocks in a syscall, we cannot interrupt it
// directly — instead we run the read in a goroutine and select between the
// read result and the watchdog timer, cancelling the read's context on
// timeout. The spawned reader goroutine leaks until the underlying connection
// eventually unblocks (caller closes resp.Body on return), which is acceptable
// for a stalled connection that we are abandoning anyway.
func readLineWithIdleTimeout(reader *bufio.Reader, idleTimeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		resCh <- result{line, err}
	}()
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	select {
	case r := <-resCh:
		return r.line, r.err
	case <-timer.C:
		return "", ErrStreamIdle
	}
}
