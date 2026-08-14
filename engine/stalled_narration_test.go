package engine

import (
	"context"
	"strings"
	"testing"
)

// stubClassifier 是 ConclusionJudge 的可控 stub。
type stubClassifier struct {
	conclusion bool
	err        error
	called     bool
	lastCheck  ConclusionCheck
}

func (s *stubClassifier) IsConclusion(_ context.Context, check ConclusionCheck) (bool, error) {
	s.called = true
	s.lastCheck = check
	return s.conclusion, s.err
}

// stubVerdictJudge 是 VerdictJudge 的可控 stub。
type stubVerdictJudge struct {
	verdict TextVerdict
	err     error
	called  bool
	last    ConclusionCheck
}

func (s *stubVerdictJudge) Classify(_ context.Context, check ConclusionCheck) (TextVerdict, error) {
	s.called = true
	s.last = check
	return s.verdict, s.err
}

func TestStalledNarrationHook_BlocksWhenNotConclusion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "上述修改已写入 turn.go，但是否生效仍不明确。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when classifier says not conclusion")
	}
	if result.Reason != "stalled_narration" {
		t.Errorf("expected Reason='stalled_narration', got %q", result.Reason)
	}
	if result.Message == "" {
		t.Errorf("expected non-empty nudge Message")
	}
}

func TestStalledNarrationHook_PassesWhenConclusion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "任务已完成，测试全部通过。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when classifier says conclusion")
	}
}

// Classifier error now allows exit instead of blocking. Blocking on
// classifier failure forces the agent to continue when it should stop,
// causing more harm than a potentially premature exit.
func TestStalledNarrationHook_ClassifierError_AllowsExit(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{err: errBoom},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "some text",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false (allow exit) when classifier errors, got Block=true reason=%q", result.Reason)
	}
}

func TestStalledNarrationHook_NilClassifierSkipsWithoutCrash(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 4} // Classifier intentionally nil
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "上述修改已写入 turn.go，但是否生效仍不明确。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false (skip) when Classifier is nil, got Block=true reason=%q", result.Reason)
	}
	if result.Exhausted {
		t.Errorf("expected Exhausted=false when Classifier is nil")
	}
}

func TestStalledNarrationHook_PassesWhenZeroToolCalls(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount==0 (delegated to ZeroToolCallHook)")
	}
}

func TestStalledNarrationHook_ExhaustedAfterMaxRetries(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 2,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if !result.Exhausted {
		t.Errorf("expected Exhausted=true when retryCount>=maxRetries")
	}
}

func TestStalledNarrationHook_RetryNudgeReferencesLastContent(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1,
		LastContent:        "查看 buildResult 如何提取 Summary",
		IsChinese:          true,
	})
	if !result.Block {
		t.Fatalf("expected Block=true")
	}
	if !strings.Contains(result.Message, "buildResult") {
		t.Errorf("expected retry nudge to reference LastContent 'buildResult', got: %q", result.Message)
	}
}

func TestStalledNarrationHook_PassesGoalAndTextToClassifier(t *testing.T) {
	sc := &stubClassifier{conclusion: true}
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: sc}
	_ = hook.Check(context.Background(), StopHookContext{
		RunToolCallCount: 3,
		LastContent:      "完成",
		Goal:             "目标X",
		IsChinese:        true,
	})
	if !sc.called {
		t.Fatalf("expected classifier to be called")
	}
	if sc.lastCheck.Goal != "目标X" || sc.lastCheck.Text != "完成" {
		t.Errorf("expected goal/text passed to classifier, got goal=%q text=%q", sc.lastCheck.Goal, sc.lastCheck.Text)
	}
}

// AnalysisMode: text-only output after tool calls is the analysis report.
// The hook allows exit immediately without calling the classifier - no
// keyword matching, no content inspection. This is the core fix for the
// "stop hook blocks analysis report" bug.
func TestStalledNarrationHook_AnalysisModeAllowsExit(t *testing.T) {
	classifier := &stubClassifier{conclusion: false} // would block normally
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: classifier}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "是否需要我按某个方向继续深入或开始实现？",
		Goal:               "分析代码流程",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if result.Block {
		t.Errorf("expected Block=false in AnalysisMode even when classifier would say not-conclusion")
	}
	if classifier.called {
		t.Errorf("classifier should NOT be called in AnalysisMode")
	}
}

// AnalysisMode takes priority over MaxRetries exhaustion: even at the retry
// limit, an analysis report is allowed to exit.
func TestStalledNarrationHook_AnalysisModeOverridesExhaustion(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2, Classifier: &stubClassifier{conclusion: false}}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 5, // well past maxRetries
		LastContent:        "分析报告正文",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if result.Block {
		t.Errorf("expected Block=false in AnalysisMode even when retries exhausted")
	}
	if result.Exhausted {
		t.Errorf("expected Exhausted=false in AnalysisMode")
	}
}

// executeTurn 集成测试：中间态叙述 -> nudge（不 Done）
func TestExecuteTurn_StalledNarrationAfterToolCalls_Nudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "上述修改已写入 turn.go。下面运行测试验证。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history:          []Message{{Role: "user", Content: "修复 bug"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: false}}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (mid-task narration -> nudge), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
}

// executeTurn 集成测试：真结论 -> Done
func TestExecuteTurn_ConclusionAfterToolCalls_StillDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3, Goal: "执行方案"},
		history:          []Message{{Role: "user", Content: "执行方案"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: true}}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion, got Done=false")
	}
}

// executeTurn 集成测试：classifier error -> 允许退出（不保守拦截）
func TestExecuteTurn_ClassifierError_AllowsExit(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "some mid text", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history:          []Message{{Role: "user", Content: "修复 bug"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{err: errBoom}}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true (allow exit on classifier error), got Done=false")
	}
}

// executeTurn 集成测试：重试上限耗尽 -> Blocked（不 Done，交回用户）
func TestExecuteTurn_StopHookExhausted_ReturnsBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 finishStreaming 逻辑，确认用户看到的是流式内容。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3, Goal: "分析截断问题"},
		history:            []Message{{Role: "user", Content: "分析截断问题"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 2, Classifier: &stubClassifier{conclusion: false}}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 2,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false when stop hook exhausted, got Done=true")
	}
	if !result.Blocked {
		t.Errorf("expected Blocked=true when stop hook exhausted")
	}
	if result.BlockedBy != "stalled_narration_exhausted" {
		t.Errorf("expected BlockedBy='stalled_narration_exhausted', got %q", result.BlockedBy)
	}
}

// executeTurn 集成测试：nudge 后模型给出真结论 -> Done（不 Blocked）
func TestExecuteTurn_ConclusionAfterNudge_ReturnsDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3, Goal: "执行方案"},
		history:            []Message{{Role: "user", Content: "执行方案"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 5}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: true}}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 1,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Errorf("expected Blocked=false when model produces conclusion after nudge, got Blocked=true BlockedBy=%q", result.BlockedBy)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion after nudge, got Done=false")
	}
}

// executeTurn 集成测试：AnalysisMode=true 时纯文本分析报告 -> Done（不拦截）
func TestExecuteTurn_AnalysisMode_TextReportDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "以下是分析报告。是否需要我按某个方向继续深入？", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "分析代码", AnalysisMode: true},
		history:   []Message{{Role: "user", Content: "分析代码"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: false}}},
		isChinese: true,
		// runToolCallCount > 0: agent has done investigation
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true in AnalysisMode for text report, got Done=false")
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
