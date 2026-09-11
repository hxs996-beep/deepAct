package engine

import (
	"context"
	"strings"
	"testing"
)

func TestParseConfirmCommand(t *testing.T) {
	cases := []struct {
		msg string
		n   int
		ok  bool
	}{
		{"/confirm 1", 1, true},
		{"/confirm 2", 2, true},
		{"/confirm 3", 3, true},
		{"/confirm", 0, false},   // 无编号不命中
		{"/confirm 0", 0, false}, // 编号从 1 起
		{"确认", 0, false},         // 非 /confirm 前缀
		{"请按方案A执行", 0, false},
	}
	for _, c := range cases {
		n, ok := parseConfirmCommand(c.msg)
		if n != c.n || ok != c.ok {
			t.Errorf("parseConfirmCommand(%q) = (%d,%v), want (%d,%v)", c.msg, n, ok, c.n, c.ok)
		}
	}
}

// 无待决问题（pendingAskUser == nil）时，/confirm N 静默消费，不改写 history。
// "按报告执行"固定确认语义已随 analysis gate 移除。
func TestHandleConfirmCommand_NoPending_Noop(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: []Message{{Role: "user", Content: "/confirm 1"}},
	}

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if last != "/confirm 1" {
		t.Errorf("history should be unchanged with no pending ask_user, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
	}
}

// /confirm 1 选择 ask_user 声明的方案A，确认执行并注入方案描述。
func TestHandleConfirmCommand_WithOptions_FirstPlanInjected(t *testing.T) {
	e := &Engine{
		state:     &TaskState{},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案A: 用 Redis 缓存") {
		t.Errorf("history should mention 方案A: 用 Redis 缓存, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after selecting 方案A, got %+v", e.pendingAskUser)
	}
}

// 模型调用 ask_user（有 options）后 Run 结束挂载选项——无 gate 参与。
func TestConfirmOptions_AskUserWithOptions_Mounted(t *testing.T) {
	askChunks := []ModelChunk{{
		Delta: "缓存方案需要你决定。",
		ToolCalls: []ModelToolCall{
			{ID: "call_ask", Type: "function", Function: ModelFunctionCall{
				Name:      AskUserToolName,
				Arguments: `{"question":"缓存方案选哪个？","options":["用 Redis 缓存","改用 MySQL"]}`,
			}},
		},
		FinishReason: "tool_calls",
		Usage:        &ModelUsage{},
	}}
	model := &multiTurnModel{turns: [][]ModelChunk{askChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
		readLoop:  NewLoopTracker(3, 4, false),
	}

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 3 {
		t.Fatalf("expected 3 options, got %d: %v", len(resp.Options), resp.Options)
	}
	if !strings.Contains(resp.Options[len(resp.Options)-1], "意见") {
		t.Errorf("last option should be the free-input item, got %q", resp.Options[len(resp.Options)-1])
	}
}

// 无 ask_user、无待决方案时正常结束不携带确认选项。
func TestConfirmOptions_NotReturnedWithoutAskUser(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成。", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "修改代码"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 0 {
		t.Errorf("expected no options without gate interception, got %v", resp.Options)
	}
}

// 回归测试：移除 analysis gate 后，搜索过代码（runToolCallCount > 0）再直接
// 提交 edit 不再被拦截——模型自主决定是否用 ask_user 确认。
func TestExecuteTurn_EditAfterSearch_NotBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "修改代码",
			ToolCalls: []ModelToolCall{
				{ID: "call_edit", Type: "function", Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`,
				}},
			},
			FinishReason: "tool_calls",
		}}},
		context:          &stubContextBuilder{},
		tools:            &recordingToolExecutor{},
		state:            &TaskState{TurnNumber: 0},
		history:          []Message{{Role: "user", Content: "改"}},
		config:           EngineConfig{ModelName: "test-model"},
		guards:           &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(true)},
		runToolCallCount: 2, // 已做过搜索
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.LastOp == "" {
		t.Error("expected edit to execute without analysis-gate blocking (LastOp empty means no operation ran)")
	}
}
