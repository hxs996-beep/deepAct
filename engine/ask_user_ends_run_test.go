package engine

import (
	"context"
	"testing"
)

// 有 options 的 ask_user：模型调用 ask_user（声明互斥候选）后应立即结束 Run，
// CompletionSummary 携带问题前的报告文本，不再触发额外模型调用。
func TestAskUser_EndsRunWithOptions(t *testing.T) {
	reportText := "缓存方案需要你决定。\n\n方案A：改为前缀匹配。\n方案B：改引擎 Summary 源。"

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
				{ID: "c3", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"缓存方案选哪个？","options":["改为前缀匹配","改引擎 Summary 源"]}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, editChunks, reportChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
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
	// 2) Summary 必须是问题前的报告文本，而不是后续兜底 turn 的文本
	if resp.Summary != reportText {
		t.Errorf("resp.Summary = %q, want %q", resp.Summary, reportText)
	}
	// 3) Run 应在 ask_user turn 结束，不再额外调用模型
	if model.callIdx != 3 {
		t.Errorf("model.callIdx = %d, want 3", model.callIdx)
	}
	// 4) 有 options 走 Done 路径，不应标记 Blocked
	if resp.Blocked {
		t.Errorf("expected non-Blocked response, got BlockedBy=%q", resp.BlockedBy)
	}
}

// 无 options 的 ask_user：模型调用 ask_user（开放式问题）后应立即结束 Run，
// 通过 awaiting_user Blocked 分支呈现问题，用户自由输入。
func TestAskUser_NoOptions_EndsRunBlockedAwaitingUser(t *testing.T) {
	question := "数据库连接字符串是什么？"

	grepChunks := []ModelChunk{
		{Delta: "搜索配置",
			ToolCalls: []ModelToolCall{
				{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "grep", Arguments: `{"pattern":"dsn","path":"."}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	askChunks := []ModelChunk{
		{Delta: "没有找到 DSN 配置，我需要你提供。",
			ToolCalls: []ModelToolCall{
				{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"数据库连接字符串是什么？"}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, askChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		readLoop:  NewReadLoopState(),
	}

	resp, err := e.Run(context.Background(), "配置数据库连接")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !resp.Blocked {
		t.Fatal("expected Blocked response for no-options ask_user")
	}
	if resp.BlockedBy != "awaiting_user" {
		t.Errorf("BlockedBy = %q, want awaiting_user", resp.BlockedBy)
	}
	if len(resp.Questions) != 1 || resp.Questions[0] != question {
		t.Errorf("Questions = %v, want [%s]", resp.Questions, question)
	}
	if len(resp.Options) != 0 {
		t.Errorf("expected no options, got %v", resp.Options)
	}
	if model.callIdx != 2 {
		t.Errorf("model.callIdx = %d, want 2", model.callIdx)
	}
}
