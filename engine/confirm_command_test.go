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

// /confirm 1 确定性确认：置 AnalysisReportConfirmed、清 AnalysisMode。
func TestHandleConfirmCommand_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after /confirm 1")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after /confirm 1")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "已确认") {
		t.Errorf("history user message should be rewritten as confirmation, got %q", last)
	}
}

// 多方案模式下 /confirm 1 → 选择方案A，确认执行并注入方案描述。
func TestHandleConfirmCommand_WithOptions_FirstPlanInjected(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:              []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese:            true,
		pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"},
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after selecting 方案A")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after selecting 方案A")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案A: 用 Redis 缓存") {
		t.Errorf("history should mention 方案A: 用 Redis 缓存, got %q", last)
	}
}

// detectUserIntent 对 /confirm 前缀走确定性 fast-path，不调用 intentJudge。
func TestDetectUserIntent_ConfirmCommandFastPath(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze} // 即使 judge 判 analyze 也不该被调用
	e := &Engine{state: &TaskState{Goal: "g"}, intentJudge: judge}

	if got := e.detectUserIntent(context.Background(), "/confirm 1"); got != IntentContinue {
		t.Errorf("detectUserIntent(/confirm 1) = %v, want IntentContinue", got)
	}
	if judge.called {
		t.Error("intentJudge must NOT be called for /confirm (deterministic channel)")
	}
}

// 本 Run 内门控拦截过且 agent 已输出报告时，EngineResponse 携带 4 项确认选项。
// 正确触发序列（Run 入口会重置 runToolCallCount 与 analysisNudgeCount，不能用
// 预置值）：turn1 grep 累加 runToolCallCount → turn2 edit 触发门控拦截
// （nudgeCount=1）→ turn3 文本报告 Done → Run 结束挂载选项。
// 注意：必须设置 guards/readLoop，否则 turn.go:514 的 e.guards.loop 解引用 nil。
func TestConfirmOptions_ReturnedWhenGateIntercepted(t *testing.T) {
	grepChunks := []ModelChunk{
		{
			Delta: "搜索代码",
			ToolCalls: []ModelToolCall{
				{ID: "call_read", Type: "function", Function: ModelFunctionCall{
					Name:      "grep",
					Arguments: `{"pattern":"foo","path":"."}`,
				}},
			},
			FinishReason: "tool_calls",
			Usage:        &ModelUsage{},
		},
	}
	editChunks := []ModelChunk{
		{
			Delta: "修改代码",
			ToolCalls: []ModelToolCall{
				{ID: "call_edit", Type: "function", Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`,
				}},
			},
			FinishReason: "tool_calls",
			Usage:        &ModelUsage{},
		},
	}
	reportChunks := []ModelChunk{
		{Delta: "分析完成，计划修改以下文件……", FinishReason: "stop", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, editChunks, reportChunks}}
	e := &Engine{
		model:    model,
		tools:    stubToolExecutor{},
		context:  steerContextBuilder{},
		state:    &TaskState{TaskID: "test", ConfirmedScope: true, AnalysisMode: true},
		config:   EngineConfig{ModelName: "test-model"},
		isChinese: true,
		// 必须完整初始化 guards：loop 供 turn.go:514、scope 供 turn.go:558
		// （turn1 的 grep 在分析门控之前就经过 scope.CheckTool）。
		guards:   &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		readLoop: NewReadLoopState(),
	}

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 2 {
		t.Fatalf("expected 2 options, got %d: %v", len(resp.Options), resp.Options)
	}
	if !strings.Contains(resp.Options[len(resp.Options)-1], "意见") {
		t.Errorf("last option should be the free-input item, got %q", resp.Options[len(resp.Options)-1])
	}
}

// 门控未拦截（analysisNudgeCount=0）时，正常结束不携带确认选项。
func TestConfirmOptions_NotReturnedWithoutGate(t *testing.T) {
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
