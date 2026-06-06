package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	dlog "github.com/deepact/deepact/internal/log"
)

var debugLog = dlog.New("[llm] ")

const deepSeekEndpoint = "https://api.deepseek.com/chat/completions"

var errSSEDone = errors.New("sse done")

type DeepSeekClient struct {
	apiKey       string
	endpoint     string
	http         *http.Client
	limiter      *AdaptiveLimiter
	retry        RetryPolicy
	estimator    *TokenEstimator
	reasoningMgr *ReasoningEchoManager
}

func NewDeepSeekClient(apiKey string, httpClient *http.Client, limiter *AdaptiveLimiter, retry RetryPolicy, estimator *TokenEstimator) *DeepSeekClient {
	if httpClient == nil {
		// No hard timeout on the HTTP client — streaming LLM responses can take
		// several minutes (thinking + generation). Context cancellation from the
		// caller (user cancel, turn boundary) is the correct timeout mechanism.
		httpClient = &http.Client{Timeout: 0}
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
	return &DeepSeekClient{
		apiKey:       apiKey,
		endpoint:     deepSeekEndpoint,
		http:         httpClient,
		limiter:      limiter,
		retry:        retry,
		estimator:    estimator,
		reasoningMgr: NewReasoningEchoManager(),
	}
}

func (c *DeepSeekClient) Stream(ctx context.Context, req ChatRequest) (<-chan Chunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("stream: %w", classifyContextError(err))
	}
	if err := c.limiter.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("acquire limiter: %w", err)
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
	for attempt := 0; attempt <= c.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := c.retry.Sleep(ctx, attempt); err != nil {
				return fmt.Errorf("backoff: %w", classifyContextError(err))
			}
		}
		status, err := c.streamOnce(ctx, req, ch)
		if err == nil {
			return nil
		}
		// Context errors are not retryable — the caller cancelled or deadline passed
		if errors.Is(err, ErrContextCanceled) || errors.Is(err, ErrTimeout) {
			return err
		}
		if errors.Is(err, ErrRateLimit) {
			c.limiter.Record429()
		}
		if !c.retry.ShouldRetry(status) || attempt == c.retry.MaxRetries {
			return err
		}
	}
	return ErrInvalidResponse
}

func (c *DeepSeekClient) streamOnce(ctx context.Context, req ChatRequest, ch chan<- Chunk) (int, error) {
	body, err := c.buildRequestBody(req)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("send request: %w", classifyContextError(err))
	}
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
	if err := parseSSE(reader, func(payload string) error {
		if payload == "[DONE]" {
			if usage := assembler.usage(); usage != nil {
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
		return http.StatusOK, err
	}
	return http.StatusOK, nil
}

// validateReasoningEcho is a pre-flight check that runs after ApplyEcho.
// It scans all assistant messages with tool_calls to ensure every one has
// non-empty ReasoningContent. If any are missing, it attempts recovery using
// the manager's lastReasoning or a fallback placeholder "..".
// This prevents sending messages that would trigger a 400 from DeepSeek API.
func (c *DeepSeekClient) validateReasoningEcho(msgs []Message) []Message {
	var fixed bool
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 && msgs[i].ReasoningContent == "" {
			// Try manager's lastReasoning first
			if c.reasoningMgr != nil {
				if pending, ok := c.reasoningMgr.PendingEcho(); ok {
					debugLog.Printf("pre-flight fix: filling reasoning_content (from manager) at msgs[%d]", i)
					msgs[i].ReasoningContent = pending
					fixed = true
					continue
				}
			}
			// Fallback: use ".." placeholder to prevent 400
			debugLog.Printf("pre-flight fix: filling reasoning_content (placeholder) at msgs[%d]", i)
			msgs[i].ReasoningContent = ".."
			fixed = true
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

	payload := requestBody{
		Model:           req.Model,
		Messages:        req.Messages,
		Tools:           req.Tools,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: req.ReasoningEffort,
		Stream:          true,
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
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Tools           []ToolDef       `json:"tools,omitempty"`
	Temperature     float64         `json:"temperature,omitempty"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Stream          bool            `json:"stream"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
	ExtraBody       *extraBody      `json:"extra_body,omitempty"`
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

func parseSSE(reader *bufio.Reader, handle func(payload string) error) error {
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
