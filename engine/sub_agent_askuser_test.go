package engine

import (
	"context"
	"testing"
)

// TestSubAgentAskUser_BubblesAndEnds verifies a sub-agent calling ask_user:
// the run ends with awaiting_user, and the questions bubble into the result.
func TestSubAgentAskUser_BubblesAndEnds(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？","options":["Redis","MySQL"]}`}},
		}}},
	}}
	runner := &SubAgentRunner{
		model: model, tools: stubToolExecutor{}, modelName: "test",
		registry: NewAgentRegistry(),
	}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", result.FinishReason)
	}
	if len(result.Questions) != 1 || result.Questions[0] != "选哪个缓存？" {
		t.Errorf("Questions = %v, want [选哪个缓存？]", result.Questions)
	}
}

// TestSubAgentNestedBubble verifies a sub-agent receiving a handoff result
// carrying Questions terminates and bubbles them up.
func TestSubAgentNestedBubble(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"子任务"}`}},
		}}},
	}}
	// questionExecutor simulates the registered SubAgentTool returning a
	// handoff result that carried questions from a deeper child.
	exec := &questionExecutor{questions: []string{"数据库连接串是什么？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test", registry: NewAgentRegistry()}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", result.FinishReason)
	}
	if len(result.Questions) != 1 || result.Questions[0] != "数据库连接串是什么？" {
		t.Errorf("Questions = %v, want [数据库连接串是什么？]", result.Questions)
	}
}

// questionExecutor returns a handoff result carrying the given questions.
type questionExecutor struct {
	questions []string
}

func (q *questionExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolResult{
			ToolCallID: c.ID, ToolName: c.Name, Status: "ok",
			Digest: "child asked a question", FinishReason: HandoffReasonAwaitingUser,
			Questions: q.questions,
		})
	}
	return out
}

func (q *questionExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: HandoffToolName}}}
}
