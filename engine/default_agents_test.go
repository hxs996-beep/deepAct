package engine

import (
	"context"
	"testing"
)

// TestGenericSubAgent_RunWithPrompt verifies that genericSubAgent forwards
// RunWithPrompt to its SubAgentRunner (fixing the silent drop of role prompts
// in production /collab and /debate) and sets StructuredResult from Spec
// before forwarding — exactly matching what Run does, so the scoped
// submit_result tool is attached to the run.
func TestGenericSubAgent_RunWithPrompt(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName,
					Arguments: `{"summary":"done"}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}

	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	agent := &genericSubAgent{runner: runner}

	result, err := agent.RunWithPrompt(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5}, "你是侦察")
	if err != nil {
		t.Fatalf("RunWithPrompt error: %v", err)
	}
	if result == nil || result.Summary != "done" {
		t.Fatalf("expected forwarded result, got %+v", result)
	}
	if model.calls != 1 {
		t.Errorf("expected 1 call (terminate on submit_result), got %d", model.calls)
	}
	// StructuredResult must be set like Run does — the request must carry
	// submit_result and the extraPrompt must be injected as a user message.
	if !toolsContain(model.lastReq.Tools, SubmitResultToolName) {
		t.Errorf("expected submit_result in request tools (StructuredResult not set), got %+v", model.lastReq.Tools)
	}
	if len(model.lastReq.Messages) < 2 || model.lastReq.Messages[1].Content != "你是侦察" {
		t.Errorf("expected extraPrompt injected as second message, got %d messages, first=%q",
			len(model.lastReq.Messages), model.lastReq.Messages[1].Content)
	}
}
