package engine

import (
	"context"
	"strings"
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

// TestSubAgentAskUser_CarriesPartialFindings: a run that stops to ask the user
// must hand the parent what it had established before asking. Nothing persists
// the run's history, and the parent re-delegates once the user answers — without
// the findings the re-delegated run restarts from zero.
func TestSubAgentAskUser_CarriesPartialFindings(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", Content: "已读 store.go：缓存层未初始化，key 前缀不一致。",
				ToolCalls: []ModelToolCall{{ID: "r1", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"store.go"}`}}}},
			FinishReason: "tool_calls",
		},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？","options":["Redis","MySQL"]}`}},
		}}},
	}}
	runner := &SubAgentRunner{model: model, tools: &recordingToolExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5, UserLanguage: "中文"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.FinishReason != HandoffReasonAwaitingUser {
		t.Fatalf("FinishReason = %q, want awaiting_user", result.FinishReason)
	}
	if !strings.Contains(result.Summary, "选哪个缓存？") {
		t.Errorf("Summary must carry the question, got %q", result.Summary)
	}
	if !strings.Contains(result.Summary, "缓存层未初始化") {
		t.Errorf("Summary must carry what the run established before asking, got %q", result.Summary)
	}
}

// TestSubAgentAskUser_NoFindingsMeansNoFallbackNoise: asking as the very first
// action establishes nothing, so the summary must be just the question — not
// summarizeHistory's "retry or narrow the task" fallback, which is wrong advice
// for a run that is waiting on the user.
func TestSubAgentAskUser_NoFindingsMeansNoFallbackNoise(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？"}`}},
		}}},
	}}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5, UserLanguage: "中文"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Summary != "问题：选哪个缓存？" {
		t.Errorf("Summary = %q, want just the question", result.Summary)
	}
	if strings.Contains(result.Summary, "请重试") || strings.Contains(result.Summary, "缩小任务范围") {
		t.Errorf("Summary must not carry summarizeHistory's retry fallback: %q", result.Summary)
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
