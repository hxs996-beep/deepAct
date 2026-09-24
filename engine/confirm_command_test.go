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

// /confirm 1 选择 ask_user 声明的第一个选项，确认执行并注入选项描述。
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
	if !strings.Contains(last, "用 Redis 缓存") {
		t.Errorf("history should mention 用 Redis 缓存, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after selecting the option, got %+v", e.pendingAskUser)
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
		state:     &TaskState{TaskID: "test"},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{scope: NewScopeGuard()},
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
	tools := &recordingToolExecutor{}
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
		tools:            tools,
		state:            &TaskState{TurnNumber: 0},
		history:          []Message{{Role: "user", Content: "改"}},
		config:           EngineConfig{ModelName: "test-model"},
		guards:           &GuardSystem{scope: NewScopeGuard()},
		runToolCallCount: 2, // 已做过搜索
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if len(tools.executed) == 0 {
		t.Error("expected edit to execute without analysis-gate blocking (no tool ran)")
	}
}

// 危险命令确认走确定性 /confirm N 通道：/confirm 1 确认并注入 re-issue hint。
func TestHandleConfirmCommand_Dangerous_Confirm(t *testing.T) {
	guard := NewScopeGuard()
	e := &Engine{
		state:     &TaskState{PendingDangerousCmd: "rm -rf /tmp/folder"},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
		guards:    &GuardSystem{scope: guard},
	}

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if !guard.dangerousConfirmed["rm -rf /tmp/folder"] {
		t.Error("dangerous command should be marked confirmed in scope guard")
	}
	if e.state.PendingDangerousCmd != "" {
		t.Errorf("PendingDangerousCmd should be cleared, got %q", e.state.PendingDangerousCmd)
	}
	joined := ""
	for _, m := range e.history {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "重新执行之前被阻断的命令") {
		t.Errorf("confirm path should inject re-issue hint, got %q", joined)
	}
}

// 危险命令取消走 /confirm 2：清 pending，不标记 confirmed，命令下次仍被拦截。
func TestHandleConfirmCommand_Dangerous_Cancel(t *testing.T) {
	guard := NewScopeGuard()
	e := &Engine{
		state:     &TaskState{PendingDangerousCmd: "rm -rf /tmp/folder"},
		history:   []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
		guards:    &GuardSystem{scope: guard},
	}

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if guard.dangerousConfirmed["rm -rf /tmp/folder"] {
		t.Error("cancelled command must NOT be marked confirmed")
	}
	if e.state.PendingDangerousCmd != "" {
		t.Errorf("PendingDangerousCmd should be cleared, got %q", e.state.PendingDangerousCmd)
	}
	// 取消路径不注入 re-issue hint。
	joined := ""
	for _, m := range e.history {
		joined += m.Content + "\n"
	}
	if strings.Contains(joined, "重新执行之前被阻断的命令") {
		t.Errorf("cancel path should NOT inject re-issue hint, got %q", joined)
	}
}
