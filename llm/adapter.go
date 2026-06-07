package llm

import (
	"context"
	"errors"

	"github.com/deepact/deepact/engine"
)

type EngineClient struct {
	client Client
}

func NewEngineClient(client Client) *EngineClient {
	return &EngineClient{client: client}
}

func (c *EngineClient) Stream(ctx context.Context, req engine.ModelRequest) (<-chan engine.ModelChunk, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("llm client is nil")
	}
	chatReq := mapToChatRequest(req)
	stream, err := c.client.Stream(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	engineStream := make(chan engine.ModelChunk, 16)
	go func() {
		defer close(engineStream)
		for chunk := range stream {
			engineChunk := engine.ModelChunk{
				Delta:          chunk.Delta,
				ReasoningDelta: chunk.ReasoningDelta,
				FinishReason:   chunk.FinishReason,
				Err:            chunk.Err,
			}
			if len(chunk.ToolCalls) > 0 {
				engineChunk.ToolCalls = make([]engine.ModelToolCall, 0, len(chunk.ToolCalls))
				for _, call := range chunk.ToolCalls {
					engineChunk.ToolCalls = append(engineChunk.ToolCalls, engine.ModelToolCall{
						ID:   call.ID,
						Type: call.Type,
						Function: engine.ModelFunctionCall{
							Name:      call.Function.Name,
							Arguments: call.Function.Arguments,
						},
					})
				}
			}
			if chunk.Usage != nil {
				engineChunk.Usage = &engine.ModelUsage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
					CacheHitTokens:   chunk.Usage.PromptCacheHitTokens,
				}
			}
			engineStream <- engineChunk
		}
	}()
	return engineStream, nil
}

func (c *EngineClient) Complete(ctx context.Context, req engine.ModelRequest) (*engine.ModelResponse, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("llm client is nil")
	}
	chatReq := mapToChatRequest(req)
	resp, err := c.client.Complete(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	return mapToModelResponse(resp), nil
}

func mapToChatRequest(req engine.ModelRequest) ChatRequest {
	messages := make([]Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		mapped := Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ToolCallID:       msg.ToolCallID,
			ReasoningContent: msg.ReasoningContent,
		}
		if len(msg.ToolCalls) > 0 {
			mapped.ToolCalls = make([]ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				mapped.ToolCalls = append(mapped.ToolCalls, ToolCall{
					ID:   call.ID,
					Type: call.Type,
					Function: FunctionCall{
						Name:      call.Function.Name,
						Arguments: call.Function.Arguments,
					},
				})
			}
		}
		messages = append(messages, mapped)
	}

	tools := make([]ToolDef, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, ToolDef{
			Type: tool.Type,
			Function: ToolFunction{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		})
	}

	return ChatRequest{
		Model:           req.Model,
		Messages:        messages,
		Tools:           tools,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: req.ReasoningEffort,
		JsonMode:        req.JsonMode,
		ThinkingEnabled: req.ThinkingEnabled,
	}
}

func mapToModelResponse(resp *ChatResponse) *engine.ModelResponse {
	if resp == nil {
		return nil
	}
	modelMsg := engine.ModelMessage{
		Role:             resp.Message.Role,
		Content:          resp.Message.Content,
		ToolCallID:       resp.Message.ToolCallID,
		ReasoningContent: resp.Message.ReasoningContent,
	}
	if len(resp.Message.ToolCalls) > 0 {
		modelMsg.ToolCalls = make([]engine.ModelToolCall, 0, len(resp.Message.ToolCalls))
		for _, call := range resp.Message.ToolCalls {
			modelMsg.ToolCalls = append(modelMsg.ToolCalls, engine.ModelToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: engine.ModelFunctionCall{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
	}
	return &engine.ModelResponse{
		ID:           resp.ID,
		Model:        resp.Model,
		Message:      modelMsg,
		FinishReason: resp.FinishReason,
		Usage: engine.ModelUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CacheHitTokens:   resp.Usage.PromptCacheHitTokens,
		},
	}
}
