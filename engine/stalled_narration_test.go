package engine

import (
	"context"
	"strings"
	"testing"
)

// reportedStallExamples are the exact mid-task narrations the user reported:
// the agent had already made tool calls, then narrated the next step as text.
var reportedStallExamples = []string{
	"查看 buildResult 如何从子代理内容提取 Summary",
	"继续验证截断路径。检查子代理的 stream_delta 是否在辩论期间显示到 UI，以及是否有其他截断点。",
	"查看 finishStreaming 逻辑，确认用户最终看到的是流式内容还是裁决摘要（被截断的）。",
}

func TestStalledNarrationHook_BlocksReportedMidTaskExamples(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	for _, c := range reportedStallExamples {
		result := hook.Check(context.Background(), StopHookContext{
			RunToolCallCount:   3,
			StopHookRetryCount: 0,
			LastContent:        c,
			IsChinese:          true,
		})
		if !result.Block {
			t.Errorf("expected Block=true for mid-task narration: %q", c)
		}
		if result.Reason != "stalled_narration" {
			t.Errorf("expected Reason='stalled_narration', got %q for %q", result.Reason, c)
		}
		if result.Message == "" {
			t.Errorf("expected non-empty nudge Message for %q", c)
		}
	}
}

func TestStalledNarrationHook_PassesGenuineConclusion(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	conclusions := []string{
		"任务已完成，所有文件已修改。",
		"综上，根因是 turn.go:235 把无工具调用的文本当成了最终回复。",
		"根本原因在于 isIntermediateText 的标点规则过严，建议改用 stop hook。",
		"修复完成，测试全部通过。",
		"文件 a.go 第 42 行有一个空指针 bug。",
	}
	for _, c := range conclusions {
		result := hook.Check(context.Background(), StopHookContext{
			RunToolCallCount:   3,
			StopHookRetryCount: 0,
			LastContent:        c,
			IsChinese:          true,
		})
		if result.Block {
			t.Errorf("expected Block=false for genuine conclusion, got block for: %q", c)
		}
	}
}

func TestStalledNarrationHook_PassesWhenZeroToolCalls(t *testing.T) {
	// RunToolCallCount == 0 is ZeroToolCallHook's responsibility; this hook
	// must stay out so the two compose cleanly (mutually exclusive per turn).
	hook := &StalledNarrationHook{MaxRetries: 2}
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

func TestStalledNarrationHook_PassesAfterMaxRetries(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when retryCount>=maxRetries (bounded safety net)")
	}
}

func TestStalledNarrationHook_DefaultMaxRetries(t *testing.T) {
	hook := &StalledNarrationHook{} // MaxRetries=0 → default 2
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when retryCount=1 < default maxRetries=2")
	}
}

func TestStalledNarrationHook_EnglishNarration(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "Let me check the finishStreaming logic to confirm truncation.",
		IsChinese:          false,
	})
	if !result.Block {
		t.Errorf("expected Block=true for English mid-task narration")
	}
	if result.Message == "" {
		t.Errorf("expected non-empty English nudge message")
	}
	if strings.ContainsAny(result.Message, "你请查看逻辑") {
		t.Errorf("expected English message, got Chinese: %q", result.Message)
	}
}

func TestStalledNarrationHook_EnglishConclusion(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "In summary, the root cause is the premature Done=true at turn.go:235.",
		IsChinese:          false,
	})
	if result.Block {
		t.Errorf("expected Block=false for English conclusion")
	}
}

func TestLooksLikeNextStepNarration(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"zh look at", "查看 buildResult 如何从子代理内容提取 Summary", true},
		{"zh continue+verify", "继续验证截断路径。检查子代理的 stream_delta 是否显示到 UI。", true},
		{"zh look+confirm", "查看 finishStreaming 逻辑，确认用户看到的是流式内容还是裁决摘要。", true},
		{"zh let me", "让我先读取 turn.go 的相关部分。", true},
		{"zh next step numbered", "1. 查看 loop.go 的退出条件", true},
		{"zh conclusion done", "任务已完成，所有文件已修改。", false},
		{"zh conclusion rootcause", "综上，根因是 turn.go:235。", false},
		{"zh conclusion suggest", "根本原因在于标点规则，建议改用 stop hook。", false},
		{"zh plain finding", "文件 a.go 第 42 行有一个空指针 bug。", false},
		{"en let me", "Let me check the finishStreaming logic.", true},
		{"en next", "Next, I'll verify the truncation path.", true},
		{"en conclusion", "In summary, the root cause is X.", false},
		{"empty", "", false},
		{"ellipsis", "...", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeNextStepNarration(tt.text); got != tt.want {
				t.Errorf("looksLikeNextStepNarration(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// TestExecuteTurn_StalledNarrationAfterToolCalls_Nudges is the end-to-end
// regression for the reported bug: model already called tools this Run
// (runToolCallCount>0), then emits a text-only next-step narration. With the
// StalledNarrationHook registered, the loop must NOT terminate — it injects a
// nudge and continues.
func TestExecuteTurn_StalledNarrationAfterToolCalls_Nudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 finishStreaming 逻辑，确认用户看到的是流式内容还是被截断的。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3},
		history:          []Message{{Role: "user", Content: "分析截断问题"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 2}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (mid-task narration → stalled-narration hook should nudge), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
}

// TestExecuteTurn_ConclusionAfterToolCalls_StillDone verifies the new hook does
// NOT pester a genuine conclusion in the full production hook set.
func TestExecuteTurn_ConclusionAfterToolCalls_StillDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3},
		history:          []Message{{Role: "user", Content: "执行方案"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 2}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion, got Done=false (hook must not pester)")
	}
}

// TestExecuteTurn_StopHookExhausted_ReturnsBlocked verifies that when stop
// hooks have been retrying (retryCount > 0) and MaxRetries is exhausted (hook
// no longer blocks), the turn returns Blocked=true with a diagnostic message
// instead of Done=true, so the user is not misled into thinking the agent
// completed the task.
func TestExecuteTurn_StopHookExhausted_ReturnsBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 finishStreaming 逻辑，确认用户看到的是流式内容。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3},
		history:            []Message{{Role: "user", Content: "分析截断问题"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 2}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 2, // MaxRetries exhausted for StalledNarrationHook
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false when stop hook exhausted, got Done=true (must not mistake narration for completion)")
	}
	if !result.Blocked {
		t.Errorf("expected Blocked=true when stop hook exhausted")
	}
	if result.BlockedBy != "stalled_narration_exhausted" {
		t.Errorf("expected BlockedBy='stalled_narration_exhausted', got %q", result.BlockedBy)
	}
	if len(result.Questions) == 0 {
		t.Errorf("expected non-empty diagnostic Questions")
	}
}

// TestStalledNarrationHook_RetryNudgeReferencesLastContent verifies that on
// retry (StopHookRetryCount > 0), the nudge message includes a snippet of the
// model's own words, making the nudge concrete rather than generic.
func TestStalledNarrationHook_RetryNudgeReferencesLastContent(t *testing.T) {
	hook := &StalledNarrationHook{MaxRetries: 2}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1, // retry -> nudge should reference LastContent
		LastContent:        "查看 buildResult 如何从子代理内容提取 Summary",
		IsChinese:          true,
	})
	if !result.Block {
		t.Fatalf("expected Block=true")
	}
	if !strings.Contains(result.Message, "buildResult") {
		t.Errorf("expected retry nudge to reference LastContent 'buildResult', got: %q", result.Message)
	}
}

// TestZeroToolCallHook_RetryNudgeReferencesLastContent verifies that on
// retry, ZeroToolCallHook's nudge also includes the model's own words.
func TestZeroToolCallHook_RetryNudgeReferencesLastContent(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 1,
		LastContent:        "Let me check the finishStreaming logic.",
		IsChinese:          false,
	})
	if !result.Block {
		t.Fatalf("expected Block=true")
	}
	if !strings.Contains(result.Message, "finishStreaming") {
		t.Errorf("expected retry nudge to reference LastContent 'finishStreaming', got: %q", result.Message)
	}
}

// TestExecuteTurn_ConclusionAfterNudge_ReturnsDone verifies the fix for a
// false positive: when stop hooks previously blocked (retryCount > 0) but
// the model now produces a genuine conclusion (not narration), the hook
// doesn't block and doesn't signal Exhausted - the turn must return Done=true,
// NOT Blocked=true. Previously, retryCount > 0 alone triggered Blocked.
func TestExecuteTurn_ConclusionAfterNudge_ReturnsDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3},
		history:            []Message{{Role: "user", Content: "执行方案"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 5}, &StalledNarrationHook{MaxRetries: 4}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 1, // previously nudged, but MaxRetries NOT exhausted (1 < 4)
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Errorf("expected Blocked=false when model produces conclusion after nudge (retryCount=1 < MaxRetries=4), got Blocked=true BlockedBy=%q", result.BlockedBy)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion after nudge, got Done=false")
	}
}
