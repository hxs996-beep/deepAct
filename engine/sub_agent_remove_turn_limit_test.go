package engine

import (
	"context"
	"testing"
)

// TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes 锁定移除默认 99 轮上限：
// MaxIterations=0（默认）时子代理可跑超过 99 轮工具调用，最终经 submit_result
// 正常完成（FinishReason=completed），绝不因轮次上限被截断。
// 改动前：MaxIterations=0 回退到 99 → 第 100 次 submit_result 永不触达，
// 循环尾部以 NoResult 结束 → 本测试红灯。
func TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes(t *testing.T) {
	const toolRounds = 100 // > 旧默认上限 99
	responses := make([]ModelResponse, 0, toolRounds+1)
	for i := 0; i < toolRounds; i++ {
		responses = append(responses, ModelResponse{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID:       "c",
				Type:     "function",
				Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"echo progress"}`},
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
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:            AgentSub,
		Goal:             "g",
		StructuredResult: true, // 与生产 generic sub 一致：结构化 run，submit_result 唯一完成路径
		// MaxIterations 省略 = 0 → 无上限（本次改动的核心断言）
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
