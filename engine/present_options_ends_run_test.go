package engine

import (
	"context"
	"testing"
)

// 方案B目标行为：模型调用 present_options（声明互斥方案）后应立即结束 Run，
// CompletionSummary 携带报告全文，不再触发额外模型调用。
//
// 真实方案场景 turn 序列：
//   turn1: grep（有工具执行，累加 runToolCallCount）
//   turn2: edit → 被分析 gate 拦截（nudgeCount=1）
//   turn3: 报告全文 + present_options → B 实施后 Run 应在此结束
//
// 当前实现（未 B）：present_options 被拦截后 turn 返回 Done:false → Run 循环
// 继续调用模型 → 兜底 turn4 "done" → resp.Summary 被污染为后续 turn 文本，
// 且多消耗一次模型调用。
func TestPresentOptions_EndsRunWithReportSummary(t *testing.T) {
	reportText := "完整报告：根因已定位。\n\n方案A：改为前缀匹配。\n方案B：改引擎 Summary 源。"

	grepChunks := []ModelChunk{
		{Delta: "搜索代码",
			ToolCalls: []ModelToolCall{
				{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "grep", Arguments: `{"pattern":"foo","path":"."}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	editChunks := []ModelChunk{
		{Delta: "修改代码",
			ToolCalls: []ModelToolCall{
				{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: "edit", Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	reportChunks := []ModelChunk{
		{Delta: reportText,
			ToolCalls: []ModelToolCall{
				{ID: "c3", Type: "function", Function: ModelFunctionCall{Name: PresentOptionsToolName, Arguments: `{"options":["改为前缀匹配","改引擎 Summary 源"]}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, editChunks, reportChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true, AnalysisMode: true},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		readLoop:  NewReadLoopState(),
	}

	resp, err := e.Run(context.Background(), "优化方案显示")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// 1) Options 必须被挂载（方案A/B + 意见）
	if len(resp.Options) != 3 {
		t.Fatalf("expected 3 options, got %v", resp.Options)
	}
	// 2) Summary 必须是报告全文，而不是后续兜底 turn 的文本
	if resp.Summary != reportText {
		t.Errorf("resp.Summary = %q, want 报告全文 %q（Run 未在 present_options turn 结束）", resp.Summary, reportText)
	}
	// 3) Run 应在 present_options turn 结束，不再额外调用模型
	if model.callIdx != 3 {
		t.Errorf("model.callIdx = %d, want 3（Run 应在 present_options turn 结束，当前多调用了 %d 次模型）", model.callIdx, model.callIdx)
	}
}
