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

func TestStalledNarrationHook_ConservativeBlockOnClassifierError(t *testing.T) {
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
	if !result.Block {
		t.Errorf("expected Block=true (conservative) when classifier errors")
	}
	if result.Reason != "classifier_error" {
		t.Errorf("expected Reason='classifier_error', got %q", result.Reason)
	}
}

func TestStalledNarrationHook_NilClassifierSkipsWithoutCrash(t *testing.T) {
	// A StalledNarrationHook registered without a Classifier (e.g. a caller
	// forgot to wire NewConclusionClassifier) must not panic on a text-only
	// turn after tool calls. It skips the check - returning an empty result so
	// the turn terminates normally - rather than dereferencing the nil
	// ConclusionJudge.
	// Note: LastContent must NOT trigger the heuristic pre-check, otherwise
	// the nil-classifier path is never reached.
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

func TestStalledNarrationHook_HeuristicBlocksBeforeClassifier(t *testing.T) {
	// Even when the classifier would return a false positive (conclusion: true),
	// the heuristic pre-check should block first WITHOUT calling the classifier.
	// This is the core fix for the "LLM stops mid-execution" bug.
	classifier := &stubClassifier{conclusion: true}
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: classifier}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "搜索结果显示项目中确实存在大量关键词匹配业务逻辑的地方。主要集中在 engine/loop.go。让我精读这些关键区域。",
		Goal:               "查找关键字匹配",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true from heuristic pre-check")
	}
	if result.Reason != "heuristic_next_step" {
		t.Errorf("expected Reason='heuristic_next_step', got %q", result.Reason)
	}
	if classifier.called {
		t.Errorf("classifier should NOT be called when heuristic blocks")
	}
}

func TestStalledNarrationHook_HeuristicSkippedForConclusion(t *testing.T) {
	// When the text is a genuine conclusion (no trailing next-step intent),
	// the heuristic should NOT block, and the classifier should be called.
	classifier := &stubClassifier{conclusion: true}
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: classifier}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "任务已完成，所有文件已修改。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false for genuine conclusion (no next-step intent)")
	}
	if !classifier.called {
		t.Errorf("classifier should be called when heuristic does not match")
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

// executeTurn 集成测试：classifier error -> 保守 nudge（不 Done）
func TestExecuteTurn_ClassifierError_Nudges(t *testing.T) {
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
	if result.Done {
		t.Errorf("expected Done=false (conservative nudge on classifier error), got Done=true")
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

// Regression: the flash-model classifier can false-positive on a declarative
// intermediate finding ("问题出在 X，建议 Y") - text that reads as a partial
// answer but is NOT a final conclusion. Without an explicit completion marker
// the hook must NOT trust the verdict and must block one more round
// conservatively; otherwise the loop exits on a partial answer (the
// "only an intermediate result, then output stops" bug).
func TestStalledNarrationHook_BlocksUnconfirmedConclusion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		// Classifier (wrongly) judges the partial answer as a conclusion.
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "经过分析，问题出在 loop.go 的停止逻辑，建议在 turn 处加判断。",
		Goal:               "排查停止 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Fatalf("expected Block=true for classifier-true verdict lacking a completion marker (conservative block), got Block=false")
	}
	if result.Reason != "classifier_unconfirmed" {
		t.Errorf("expected Reason='classifier_unconfirmed', got %q", result.Reason)
	}
}

// A classifier "conclusion" verdict is trusted when the text carries an
// explicit completion marker - the conservative guard must not block genuine
// completions.
func TestStalledNarrationHook_TrustsConclusionWithCompletionMarker(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "修复已完成，所有测试全部通过。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when conclusion text carries a completion marker")
	}
}

// After one conservative nudge (retry > 0), trust the classifier verdict even
// without a completion marker - give the model a second chance rather than
// looping until exhaustion on a genuine but tersely-worded conclusion.
func TestStalledNarrationHook_TrustsConclusionOnSecondChance(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1,
		LastContent:        "经过分析，问题出在 loop.go 的停止逻辑。",
		Goal:               "排查停止 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false on second chance (retry>0) even without completion marker")
	}
}

// English path: a declarative intermediate finding without a completion marker
// must also be blocked conservatively. Verifies hasCompletionMarker's
// case-insensitive English matching and that the guard is language-agnostic.
func TestStalledNarrationHook_BlocksUnconfirmedConclusionEnglish(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "The issue appears to be in loop.go's stop logic, suggesting a fix in turn.go.",
		Goal:               "investigate stop bug",
		IsChinese:          false,
	})
	if !result.Block {
		t.Fatalf("expected Block=true for unconfirmed English conclusion without completion marker")
	}
	if result.Reason != "classifier_unconfirmed" {
		t.Errorf("expected Reason='classifier_unconfirmed', got %q", result.Reason)
	}
}

// hasFutureIntent: hard signal for intermediate state. Text containing a
// future-intent marker is mid-task regardless of what the classifier says.
func TestHasFutureIntent(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"需要 zh", "综上，需要在 turn.go 加入校验。", true},
		{"接下来 zh", "分析完成。接下来查看 loop.go。", true},
		{"下一步 zh", "下一步运行测试。", true},
		{"准备 zh", "准备运行测试。", true},
		{"尚未 zh", "尚未确认结果。", true},
		{"need to en", "I need to check the file.", true},
		{"going to en", "I'm going to read it.", true},
		{"completion no future", "修复已完成，测试全部通过。", false},
		{"declarative no future", "问题出在 loop.go 的停止逻辑。", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasFutureIntent(tt.text); got != tt.want {
				t.Errorf("hasFutureIntent(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// Weak analytical markers (综上/总结/总的来说/结论是) must NOT count as
// completion - they appear in intermediate findings ("综上，需要...") and caused
// false positives that let mid-task text exit the loop.
func TestHasCompletionMarker_RejectsWeakMarkers(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"综上 not completion", "综上，需要在 turn.go 加入校验。", false},
		{"总结 not completion", "总结一下，接下来查看 X。", false},
		{"总的来说 not completion", "总的来说，还需进一步分析。", false},
		{"强完成 已完成", "修复已完成，测试全部通过。", true},
		{"强完成 全部通过", "所有测试全部通过。", true},
		{"强完成 已修复", "问题已修复。", true},
		{"done en", "Done, all tests pass.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasCompletionMarker(tt.text); got != tt.want {
				t.Errorf("hasCompletionMarker(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// StalledNarrationHook: future-intent text is blocked hard, before the
// classifier is even called - the "综上，需要..." case that previously slipped
// through heuristic and classifier. Classifier is NOT consulted.
func TestStalledNarrationHook_BlocksFutureIntent(t *testing.T) {
	classifier := &stubClassifier{conclusion: true} // would false-positive
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: classifier}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "综上，停止逻辑的问题在于 turn.go 缺少校验，需要加入保守判断。",
		Goal:               "排查停止 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Fatalf("expected Block=true for future-intent text, got Block=false")
	}
	if result.Reason != "future_intent" {
		t.Errorf("expected Reason='future_intent', got %q", result.Reason)
	}
	if classifier.called {
		t.Errorf("classifier should NOT be called when future_intent hard guard blocks")
	}
}

// classifier error + strong completion marker -> allow exit. When the
// classifier is unavailable (network/parse failure), a genuine conclusion
// with a strong completion marker must not be trapped in a nudge loop.
func TestStalledNarrationHook_ClassifierError_AllowsCompletionMarker(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{err: errBoom},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "修复已完成，测试全部通过。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when classifier errors but text has strong completion marker, got Block=true reason=%q", result.Reason)
	}
}
