package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- StopHook AwaitUser（提问检测）测试 ---

// TestStalledNarrationHook_AwaitUserWhenQuestion verifies that a reply asking
// the user a question returns AwaitUser=true (engine must stop and wait) and
// does NOT block with a nudge.
func TestStalledNarrationHook_AwaitUserWhenQuestion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Verdict:    &stubVerdictJudge{verdict: VerdictQuestion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "方案1、2、3 你选哪个？",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if !result.AwaitUser {
		t.Errorf("expected AwaitUser=true for question, got AwaitUser=%v Block=%v", result.AwaitUser, result.Block)
	}
	if result.Block {
		t.Errorf("expected Block=false for question (should not nudge), got Block=true")
	}
}

// TestStalledNarrationHook_IntermediateWithVerdict_StillBlocks verifies that
// when the VerdictJudge says intermediate, the hook still blocks with a nudge.
func TestStalledNarrationHook_IntermediateWithVerdict_StillBlocks(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Verdict:    &stubVerdictJudge{verdict: VerdictIntermediate},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "上述修改已写入。下面运行测试。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true for intermediate narration, got Block=%v AwaitUser=%v", result.Block, result.AwaitUser)
	}
	if result.AwaitUser {
		t.Errorf("expected AwaitUser=false for intermediate narration, got true")
	}
}

// TestStalledNarrationHook_ConclusionWithVerdict_AllowsExit verifies that a
// conclusion verdict allows exit (no block, no await).
func TestStalledNarrationHook_ConclusionWithVerdict_AllowsExit(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Verdict:    &stubVerdictJudge{verdict: VerdictConclusion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "任务已完成，测试全部通过。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block || result.AwaitUser {
		t.Errorf("expected Block=false AwaitUser=false for conclusion, got Block=%v AwaitUser=%v", result.Block, result.AwaitUser)
	}
}

// TestStalledNarrationHook_VerdictError_FallsBackToBinary verifies that a
// VerdictJudge error falls back to the binary ConclusionJudge (existing
// behavior) instead of stalling or awaiting.
func TestStalledNarrationHook_VerdictError_FallsBackToBinary(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Verdict:    &stubVerdictJudge{verdict: VerdictIntermediate, err: errBoom},
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "some text",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.AwaitUser {
		t.Errorf("expected AwaitUser=false when verdict judge errors")
	}
	if result.Block {
		t.Errorf("expected Block=false when binary classifier says conclusion, got Block=true")
	}
}

// TestZeroToolCallHook_AwaitUserWhenQuestion verifies that ZeroToolCallHook
// also stops for a question even when no tools were called this run.
func TestZeroToolCallHook_AwaitUserWhenQuestion(t *testing.T) {
	hook := &ZeroToolCallHook{
		MaxRetries: 3,
		Verdict:    &stubVerdictJudge{verdict: VerdictQuestion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "请问你想要方案 A 还是方案 B？",
		IsChinese:          true,
	})
	if !result.AwaitUser {
		t.Errorf("expected AwaitUser=true for question, got AwaitUser=%v Block=%v", result.AwaitUser, result.Block)
	}
	if result.Block {
		t.Errorf("expected Block=false for question, got Block=true")
	}
}

// TestZeroToolCallHook_IntermediateWithoutVerdict_StillNudges verifies the
// existing zero-tool-call nudge behavior is preserved when Verdict is nil.
func TestZeroToolCallHook_IntermediateWithoutVerdict_StillNudges(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "让我先读取代码",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true for zero-tool-call narration (existing behavior), got Block=%v", result.Block)
	}
	if result.AwaitUser {
		t.Errorf("expected AwaitUser=false when Verdict is nil, got true")
	}
}

// TestZeroToolCallHook_ConclusionWithVerdict_AllowsExit verifies that a
// conclusion verdict with zero tool calls (pure-reasoning answer) allows
// exit instead of being nudged into acting.
func TestZeroToolCallHook_ConclusionWithVerdict_AllowsExit(t *testing.T) {
	hook := &ZeroToolCallHook{
		MaxRetries: 3,
		Verdict:    &stubVerdictJudge{verdict: VerdictConclusion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "这是完整的分析结论，无需进一步操作。",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false for conclusion with zero tool calls, got Block=%v", result.Block)
	}
	if result.AwaitUser {
		t.Errorf("expected AwaitUser=false, got true")
	}
}

// TestZeroToolCallHook_AnalysisMode_AllowsExit verifies that in AnalysisMode
// a zero-tool-call text reply is the report itself and must exit without
// nudging — even when no Verdict is wired.
func TestZeroToolCallHook_AnalysisMode_AllowsExit(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "分析报告正文",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if result.Block {
		t.Errorf("expected Block=false in AnalysisMode with zero tool calls, got Block=%v", result.Block)
	}
	if result.AwaitUser {
		t.Errorf("expected AwaitUser=false, got true")
	}
}

// TestZeroToolCallHook_AnalysisModeIntermediateWithVerdict_AllowsExit verifies
// that an intermediate verdict in AnalysisMode also allows exit (mirrors
// StalledNarrationHook's intermediate+AnalysisMode branch).
func TestZeroToolCallHook_AnalysisModeIntermediateWithVerdict_AllowsExit(t *testing.T) {
	hook := &ZeroToolCallHook{
		MaxRetries: 3,
		Verdict:    &stubVerdictJudge{verdict: VerdictIntermediate},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "分析报告正文",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if result.Block {
		t.Errorf("expected Block=false for intermediate in AnalysisMode, got Block=%v", result.Block)
	}
}

// TestZeroToolCallHook_AnalysisModeQuestion_AwaitsUser verifies the red line:
// even in AnalysisMode, a question to the user returns AwaitUser=true — the
// model must never decide on the user's behalf, and must not be nudged.
func TestZeroToolCallHook_AnalysisModeQuestion_AwaitsUser(t *testing.T) {
	hook := &ZeroToolCallHook{
		MaxRetries: 3,
		Verdict:    &stubVerdictJudge{verdict: VerdictQuestion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "需要确认：方案 A 还是方案 B？",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if !result.AwaitUser {
		t.Errorf("expected AwaitUser=true for question even in AnalysisMode, got AwaitUser=%v Block=%v", result.AwaitUser, result.Block)
	}
	if result.Block {
		t.Errorf("expected Block=false for question, got true")
	}
}

// TestExecuteTurn_Question_ReturnsBlockedAwaitingUser verifies end-to-end:
// a text-only question reply (runToolCallCount=0 → ZeroToolCallHook) makes
// executeTurn return Blocked=true with BlockedBy="awaiting_user" and the
// question in Questions — and does NOT inject a nudge into history.
func TestExecuteTurn_Question_ReturnsBlockedAwaitingUser(t *testing.T) {
	questionText := "这里有三种修复方案：方案1、方案2、方案3。你选哪个？"
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: questionText, FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history:   []Message{{Role: "user", Content: "修复 bug"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		isChinese: true,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("expected Blocked=true for question, got Blocked=%v Done=%v", result.Blocked, result.Done)
	}
	if result.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", result.BlockedBy)
	}
	if len(result.Questions) != 1 || !strings.Contains(result.Questions[0], "方案") {
		t.Errorf("expected question in Questions, got %+v", result.Questions)
	}
	// No nudge should be injected into history: only the original user
	// message plus the assistant question text (appended by executeTurn
	// before stop hooks ran). A nudge would appear as an extra user message.
	if len(e.history) != 2 {
		t.Errorf("expected history unchanged (user + assistant, no nudge), got %d messages: %+v", len(e.history), e.history)
	} else if e.history[0].Role != "user" || e.history[1].Role != "assistant" {
		t.Errorf("expected [user, assistant], got roles %q, %q", e.history[0].Role, e.history[1].Role)
	}
}

// TestExecuteTurn_QuestionAfterToolCalls_ReturnsBlockedAwaitingUser verifies
// the same end-to-end behavior on the StalledNarrationHook path
// (runToolCallCount > 0).
func TestExecuteTurn_QuestionAfterToolCalls_ReturnsBlockedAwaitingUser(t *testing.T) {
	questionText := "是否继续深入排查，还是先总结当前发现？"
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: questionText, FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 3, Goal: "分析 bug"},
		history:          []Message{{Role: "user", Content: "分析 bug"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		isChinese:        true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("expected Blocked=true for question after tool calls, got Blocked=%v Done=%v", result.Blocked, result.Done)
	}
	if result.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", result.BlockedBy)
	}
	if len(result.Questions) != 1 || !strings.Contains(result.Questions[0], "继续") {
		t.Errorf("expected question in Questions, got %+v", result.Questions)
	}
}

// TestRun_Question_ReturnsBlockedAwaitingUserWithEmptySummary verifies the
// full Run() loop: a question reply makes Run return Blocked=true with
// BlockedBy="awaiting_user", Summary empty (the question is presented via
// Questions only, not duplicated), and the question text in Questions.
func TestRun_Question_ReturnsBlockedAwaitingUserWithEmptySummary(t *testing.T) {
	questionText := "这里有三种修复方案：方案1、方案2、方案3。你选哪个？"
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: questionText, FinishReason: "stop"},
		}},
		tools:            stubToolExecutor{},
		context:          &stubContextBuilder{},
		state:            &TaskState{TaskID: "test", ConfirmedScope: true, Goal: "修复 bug"},
		history:          []Message{{Role: "user", Content: "修复 bug", Timestamp: time.Now()}},
		config:           EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:           &GuardSystem{scope: NewScopeGuard(true), loop: NewLoopGuard("", 6)},
		readLoop:         NewReadLoopState(),
		errorLoop:        NewErrorLoopState(0),
		activatedSkills:  make(map[string]bool),
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		isChinese:        true,
		runToolCallCount: 0,
	}

	resp, err := e.Run(context.Background(), "修复 bug")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Run returned nil response")
	}
	if !resp.Blocked {
		t.Fatalf("expected Blocked=true, got Blocked=%v BlockedBy=%q Summary=%q", resp.Blocked, resp.BlockedBy, resp.Summary)
	}
	if resp.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", resp.BlockedBy)
	}
	if resp.Summary != "" {
		t.Errorf("expected empty Summary for awaiting_user (question lives in Questions), got %q", resp.Summary)
	}
	if len(resp.Questions) != 1 || !strings.Contains(resp.Questions[0], "方案") {
		t.Errorf("expected question in Questions, got %+v", resp.Questions)
	}
}

// TestExecuteTurn_QuestionWithEditCall_BlocksAwaitingUser reproduces the
// "自问自答" bug: the model emits a question to the user AND destructive
// edit/write calls in the same reply. The edit must NOT execute — the
// question is presented and the engine waits for the user. The model must
// never decide on the user's behalf.
func TestExecuteTurn_QuestionWithEditCall_BlocksAwaitingUser(t *testing.T) {
	questionText := "需要你确认的 3 个点：1. hideDownload=false 的语义？2. 覆盖逻辑作用范围？3. 新接口命名？确认后我就开始写代码。"
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        questionText,
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"a.go","old_string":"x","new_string":"y"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "修复 bug", ConfirmedScope: true},
		history:      []Message{{Role: "user", Content: "修复 bug"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		verdictJudge: &stubVerdictJudge{verdict: VerdictQuestion},
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("expected Blocked=true for question+edit, got Blocked=%v BlockedBy=%q", result.Blocked, result.BlockedBy)
	}
	if result.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", result.BlockedBy)
	}
	if len(result.Questions) != 1 || !strings.Contains(result.Questions[0], "需要你确认") {
		t.Errorf("expected question in Questions, got %+v", result.Questions)
	}
	// The edit must NOT have been executed as a real tool result: history
	// must contain a "Blocked" tool message (closing the API contract),
	// not an executed outcome.
	foundBlocked := false
	for _, m := range e.history {
		if m.Role == "tool" && strings.Contains(m.Content, "Blocked") {
			foundBlocked = true
			break
		}
	}
	if !foundBlocked {
		t.Errorf("expected a Blocked tool message in history, got %+v", e.history)
	}
}

// TestExecuteTurn_QuestionWithReadCall_NotBlocked verifies that read-only
// tool calls alongside a question are NOT blocked: the model may continue
// investigating while asking the user. (Per review: tool calls imply the
// model is still working — only destructive edits must wait.)
func TestExecuteTurn_QuestionWithReadCall_NotBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "需要确认：方案 A 还是方案 B？",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "read",
					Arguments: `{"path":"a.go"}`,
				},
			}},
		}}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 0, Goal: "修复 bug"},
		history:   []Message{{Role: "user", Content: "修复 bug"}},
		config:    EngineConfig{ModelName: "test"},
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks: []StopHook{&StalledNarrationHook{MaxRetries: 4, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		isChinese: true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Errorf("read-only call alongside question should NOT be blocked, got BlockedBy=%q msg=%q", result.BlockedBy, result.Questions)
	}
}

// TestStalledNarrationHook_AnalysisModeQuestion_AwaitsUser verifies that even
// in AnalysisMode (text-only output is the report), a question to the user
// still returns AwaitUser=true — the model must never decide on the user's
// behalf, and must not be nudged to continue acting.
func TestStalledNarrationHook_AnalysisModeQuestion_AwaitsUser(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Verdict:    &stubVerdictJudge{verdict: VerdictQuestion},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "需要你确认的 3 个点：方案 A 还是方案 B？",
		Goal:               "分析 bug",
		IsChinese:          true,
		AnalysisMode:       true,
	})
	if !result.AwaitUser {
		t.Errorf("expected AwaitUser=true for question in analysis mode, got AwaitUser=%v Block=%v", result.AwaitUser, result.Block)
	}
}

// TestExecuteTurn_WeakQuestionWithWriteCall_BlocksAwaitingUser reproduces the
// 自问自答 bug from the wild: the model emits a weak/conditional proposal
// ("需要我把它保存成文件…也可以说一声") AND a write call in the same reply.
// The guard is fail-closed: any non-conclusion verdict (question, weak
// proposal, intermediate, classifier error) blocks the write and waits for
// the user — the model must never decide on the user's behalf.
func TestExecuteTurn_WeakQuestionWithWriteCall_BlocksAwaitingUser(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "需要我把它保存成文件（比如 docs/h3-metal.md）也可以说一声。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "write",
					Arguments: `{"path":"docs/h3-metal.md","content":"# h3-metal 速查"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "生成 md 文档", ConfirmedScope: true},
		history:      []Message{{Role: "user", Content: "生成 md 文档"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		verdictJudge: &stubVerdictJudge{verdict: VerdictIntermediate},
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("expected Blocked=true for weak proposal + write, got Blocked=%v BlockedBy=%q", result.Blocked, result.BlockedBy)
	}
	if result.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", result.BlockedBy)
	}
	// The write must NOT have been executed: history must contain a Blocked
	// tool message, not an executed outcome.
	foundBlocked := false
	for _, m := range e.history {
		if m.Role == "tool" && strings.Contains(m.Content, "Blocked") {
			foundBlocked = true
			break
		}
	}
	if !foundBlocked {
		t.Errorf("expected a Blocked tool message in history, got %+v", e.history)
	}
}

// TestExecuteTurn_WeakQuestionWithWriteCall_ClassifierError_Blocks verifies the
// fail-closed path when the verdict classifier errors: a write alongside any
// non-conclusion text must still be blocked, not silently executed.
func TestExecuteTurn_WeakQuestionWithWriteCall_ClassifierError_Blocks(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "需要我把它保存成文件（比如 docs/h3-metal.md）也可以说一声。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "write",
					Arguments: `{"path":"docs/h3-metal.md","content":"# h3-metal 速查"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "生成 md 文档", ConfirmedScope: true},
		history:      []Message{{Role: "user", Content: "生成 md 文档"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		verdictJudge: &stubVerdictJudge{verdict: VerdictIntermediate, err: errBoom},
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("expected Blocked=true on classifier error with write, got Blocked=%v BlockedBy=%q", result.Blocked, result.BlockedBy)
	}
	if result.BlockedBy != "awaiting_user" {
		t.Errorf("expected BlockedBy='awaiting_user', got %q", result.BlockedBy)
	}
}

// TestExecuteTurn_ConclusionWithWriteCall_NotBlocked verifies the allowed path
// under fail-closed: a write call carrying text that clearly classifies as a
// final conclusion is NOT blocked (normal edit flow proceeds).
func TestExecuteTurn_ConclusionWithWriteCall_NotBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "文档已生成完毕。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "write",
					Arguments: `{"path":"docs/h3-metal.md","content":"# h3-metal 速查"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "生成 md 文档", ConfirmedScope: true},
		history:      []Message{{Role: "user", Content: "生成 md 文档"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictConclusion}}},
		verdictJudge: &stubVerdictJudge{verdict: VerdictConclusion},
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Fatalf("expected NOT blocked for conclusion + write, got BlockedBy=%q", result.BlockedBy)
	}
}

// TestExecuteTurn_UserConfirmedAnalysisReport_WriteWithIntermediateText_NotBlocked
// 复现死循环的放行侧：用户已经确认（AnalysisReportConfirmed=true），模型带中间
// 叙述文本提交 edit。fail-closed 守卫必须放行 —— 用户已明确同意执行，语义分类
// 不再适用，否则"确认→又被拦→再确认"无限循环。
func TestExecuteTurn_UserConfirmedAnalysisReport_WriteWithIntermediateText_NotBlocked(t *testing.T) {
	judge := &stubVerdictJudge{verdict: VerdictIntermediate}
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "按方案修改 ui/model.go 的 renderBody，移除主 agent narration 的实时逐字渲染。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"ui/model.go","old":"a","new":"b"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "修改 renderBody", ConfirmedScope: true, AnalysisReportConfirmed: true},
		history:      []Message{{Role: "user", Content: "修改 renderBody"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		verdictJudge: judge,
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Fatalf("user-confirmed edit must NOT be blocked by self-answering guard, got Blocked=%v BlockedBy=%q", result.Blocked, result.BlockedBy)
	}
	if judge.called {
		t.Errorf("self-answering guard must NOT classify after user confirmation (verdict judge should be skipped), got called=true")
	}
}

// TestExecuteTurn_SelfAnsweringGuardBlock_SetsPendingAnalysisNudge verifies that
// the self-answering guard, when it blocks an edit/write call, records
// pendingAnalysisNudge=true so the user's subsequent confirmation ("ok")
// can set AnalysisReportConfirmed and let the next edit through. Without this,
// the "确认→又被拦→再确认" loop never terminates.
func TestExecuteTurn_SelfAnsweringGuardBlock_SetsPendingAnalysisNudge(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "需要我把它保存成文件（比如 docs/h3-metal.md）也可以说一声。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "write",
					Arguments: `{"path":"docs/h3-metal.md","content":"# h3-metal 速查"}`,
				},
			}},
		}}},
		context:      &stubContextBuilder{},
		tools:        stubToolExecutor{},
		state:        &TaskState{TurnNumber: 0, Goal: "生成 md 文档", ConfirmedScope: true},
		history:      []Message{{Role: "user", Content: "生成 md 文档"}},
		config:       EngineConfig{ModelName: "test"},
		guards:       &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		stopHooks:    []StopHook{&ZeroToolCallHook{MaxRetries: 5, Verdict: &stubVerdictJudge{verdict: VerdictQuestion}}},
		verdictJudge: &stubVerdictJudge{verdict: VerdictIntermediate},
		isChinese:    true,
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked || result.BlockedBy != "awaiting_user" {
		t.Fatalf("expected blocked awaiting_user, got Blocked=%v BlockedBy=%q", result.Blocked, result.BlockedBy)
	}
	if !e.pendingAnalysisNudge {
		t.Errorf("expected pendingAnalysisNudge=true after self-answering guard blocks (so user confirmation can release the next edit)")
	}
}

// seqVerdictJudge returns scripted verdicts in order (per Classify call).
type seqVerdictJudge struct {
	verdicts []TextVerdict
	idx      int
}

func (s *seqVerdictJudge) Classify(_ context.Context, _ ConclusionCheck) (TextVerdict, error) {
	v := s.verdicts[s.idx%len(s.verdicts)]
	s.idx++
	return v, nil
}

// TestRun_UserConfirmation_BreaksSelfAnsweringLoop reproduces the exact
// dead-loop from the wild: turn 1 the model asks a question ("要我按此实施吗？")
// alongside a write call → blocked awaiting_user. The user confirms ("ok").
// Turn 2 the model retries the write with intermediate narration text. After
// confirmation, the edit MUST go through — not be re-blocked forever.
func TestRun_UserConfirmation_BreaksSelfAnsweringLoop(t *testing.T) {
	model := &multiTurnModel{turns: [][]ModelChunk{
		{{ // turn 1: weak question + write → self-answering guard blocks
			Delta:        "确认后我才执行改动。要我按此实施吗？",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:       "c1",
				Type:     "function",
				Function: ModelFunctionCall{Name: "write", Arguments: `{"path":"ui/model.go","content":"x"}`},
			}},
		}},
		{{ // turn 2: intermediate narration + write (user already confirmed)
			Delta:        "按方案修改 ui/model.go 的 renderBody，移除主 agent narration 的实时逐字渲染。",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:       "c2",
				Type:     "function",
				Function: ModelFunctionCall{Name: "write", Arguments: `{"path":"ui/model.go","content":"y"}`},
			}},
		}},
		{{Delta: "已完成修改。", FinishReason: "stop"}}, // turn 3: text-only → stop-hook path
	}}
	judge := &seqVerdictJudge{verdicts: []TextVerdict{VerdictQuestion, VerdictIntermediate, VerdictConclusion}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true, Goal: "修改 renderBody"},
		history:   []Message{{Role: "user", Content: "修改 renderBody", Timestamp: time.Now()}},
		config:    EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:    &GuardSystem{scope: NewScopeGuard(true), loop: NewLoopGuard("", 6)},
		readLoop:  NewReadLoopState(),
		errorLoop: NewErrorLoopState(0),
		stopHooks: []StopHook{
			&ZeroToolCallHook{MaxRetries: 5, Verdict: judge},
			&StalledNarrationHook{MaxRetries: 4, Verdict: judge},
		},
		verdictJudge: judge,
		isChinese:    true,
	}

	resp1, err := e.Run(context.Background(), "修改 renderBody")
	if err != nil {
		t.Fatalf("Run 1 error: %v", err)
	}
	if !resp1.Blocked || resp1.BlockedBy != "awaiting_user" {
		t.Fatalf("expected turn 1 blocked awaiting_user, got Blocked=%v BlockedBy=%q", resp1.Blocked, resp1.BlockedBy)
	}

	resp2, err := e.Run(context.Background(), "ok")
	if err != nil {
		t.Fatalf("Run 2 error: %v", err)
	}
	if resp2.Blocked {
		t.Fatalf("user-confirmed edit must NOT be re-blocked (dead-loop), got BlockedBy=%q Summary=%q", resp2.BlockedBy, resp2.Summary)
	}
}
