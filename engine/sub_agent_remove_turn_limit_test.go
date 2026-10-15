package engine

import (
	"context"
	"testing"
)

// readStubExecutor registers only the read tool: sub-agents are read-only, so
// a universe member must back the script's calls or the execution gate would
// refuse them. stubToolExecutor (Specs() == nil) cannot serve this test.
type readStubExecutor struct{}

func (readStubExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func (readStubExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: "read"}}}
}

// TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes 锁定“代码里不再有任何轮次
// 上限”：子代理可跑超过 99 轮工具调用，最终经 submit_result 正常完成
// （FinishReason=completed），绝不会因轮数被截断。收敛由 token 预算 + 三击守卫 +
// 结构化完成契约负责，不由写死的轮数负责。
// 历史：曾默认回退到 99 轮 → 第 100 次 submit_result 永不触达。
func TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes(t *testing.T) {
	const toolRounds = 100 // > 旧默认上限 99
	responses := make([]ModelResponse, 0, toolRounds+1)
	for i := 0; i < toolRounds; i++ {
		responses = append(responses, ModelResponse{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID:       "c",
				Type:     "function",
				Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`},
			}}},
			FinishReason: "tool_calls",
		})
	}
	responses = append(responses, ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID:       "submit",
			Type:     "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"调研完成"}`},
		}}},
		FinishReason: "tool_calls",
	})
	model := &stubSeqModel{responses: responses}
	runner := &SubAgentRunner{model: model, tools: readStubExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:            AgentSub,
		Goal:             "g",
		StructuredResult: true, // 与生产 generic sub 一致：结构化 run，submit_result 唯一完成路径
		// 无轮次上限（字段已从代码中删除）：100 轮之后仍能 submit_result
		// 正常完成，这就是本次改动的核心断言。
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls <= toolRounds {
		t.Errorf("expected sub-agent to run past the old 99-turn cap, got %d calls", model.calls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q (normal completion), got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if result.Summary != "调研完成" {
		t.Errorf("expected summary from submit_result, got %q", result.Summary)
	}
}
