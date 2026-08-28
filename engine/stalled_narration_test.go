package engine

import (
	"context"
	"testing"
)

// executeTurn 集成测试：无 stop hook 注册时（dsh 结构化完成），纯文本回复
// 直接结束（Done=true），不 nudge。
func TestExecuteTurn_NoStopHooks_TextOnlyDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "让我先看看代码。", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "分析代码"},
		history:   []Message{{Role: "user", Content: "分析代码"}},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true (no stop hooks → text-only reply ends the turn), got Done=false")
	}
	if len(e.pendingPinnedMessages) != 0 {
		t.Errorf("expected no pinned nudge, got %q", e.pendingPinnedMessages)
	}
	if result.FinishReason != "stop" {
		t.Errorf("expected FinishReason='stop', got %q", result.FinishReason)
	}
}

// executeTurn 集成测试：task_complete 工具调用 -> Done + CompletionSummary
func TestExecuteTurn_TaskComplete_Intercepted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "分析完成。", ToolCalls: []ModelToolCall{
				{ID: "call_1", Type: "function", Function: ModelFunctionCall{
					Name:      "task_complete",
					Arguments: `{"summary":"这是最终结论。"}`,
				}},
			}, FinishReason: "tool_calls"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 3, Goal: "分析代码"},
		history: []Message{{Role: "user", Content: "分析代码"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Fatalf("expected Done=true for task_complete call, got Done=false")
	}
	if result.CompletionSummary != "这是最终结论。" {
		t.Errorf("expected CompletionSummary='这是最终结论。', got %q", result.CompletionSummary)
	}
	// Verify tool message was added for API contract
	foundToolMsg := false
	for _, msg := range e.history {
		if msg.Role == "tool" && msg.ToolCallID == "call_1" {
			foundToolMsg = true
			break
		}
	}
	if !foundToolMsg {
		t.Error("expected tool message for task_complete call in history")
	}
}

// task_complete alongside other tools: completion takes priority, other
// calls get placeholder tool messages for API contract.
func TestExecuteTurn_TaskComplete_WithOtherTools(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "完成。", ToolCalls: []ModelToolCall{
				{ID: "call_1", Type: "function", Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"x.go","old_string":"a","new_string":"b"}`,
				}},
				{ID: "call_2", Type: "function", Function: ModelFunctionCall{
					Name:      "task_complete",
					Arguments: `{"summary":"done"}`,
				}},
			}, FinishReason: "tool_calls"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history: []Message{{Role: "user", Content: "修复 bug"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Fatalf("expected Done=true when task_complete is called alongside other tools")
	}
	if result.CompletionSummary != "done" {
		t.Errorf("expected CompletionSummary='done', got %q", result.CompletionSummary)
	}
	// Both tool calls should have tool messages (API contract)
	toolMsgs := 0
	for _, msg := range e.history {
		if msg.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Errorf("expected 2 tool messages (1 for edit + 1 for task_complete), got %d", toolMsgs)
	}
}
