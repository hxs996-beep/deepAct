# task_complete 工具替换关键字流程判断 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 `task_complete` 工具调用替换 `classifier.go` 的关键词启发式和 `stop_hook.go` 的双 hook 机制，让模型通过结构化工具调用显式声明完成，而非从自然语言猜测。

**Architecture:** 新增 `task_complete` 工具，模型调用它来提交最终输出。`executeTurn` 拦截该调用并设 `Done=true`。`ZeroToolCallHook` + `StalledNarrationHook` 合并为单一 `CompletionToolHook`：纯文本 = 未完成，MaxRetries 耗尽后 LLM 兜底。所有关键词函数退役。

**Tech Stack:** Go 1.24, DeepSeek API, 现有 Tool/StopHook 接口

## Global Constraints

- 不改变 `StopHook` 接口签名（`Check(ctx, StopHookContext) StopHookResult`）
- `ConclusionJudge` 接口保留，降级为 MaxRetries 兜底
- `TurnResult` 新增字段向后兼容（零值 = 现有行为）
- 系统提示词位于嵌入文件 `context/promptset/zh/system.md`，修改后需重新编译（`go:embed`）
- 测试沿用 `stubStreamModel` + `stubContextBuilder` + `stubToolExecutor` 模式（见 `turn_test.go:57-78`）

---

### Task 1: task_complete 工具定义 + TurnResult 字段

**Files:**
- Modify: `engine/agent.go:18-20`（常量）、`engine/agent.go:64-77`（params 结构体旁追加）
- Modify: `engine/turn.go:18-32`（TurnResult 结构体）、`engine/turn.go:631-636`（toolSpecsWithHandoff）
- Test: `engine/completion_tool_def_test.go`（新建）

**Interfaces:**
- Produces: `TaskCompleteToolName` 常量（string）、`TaskCompleteParams` 结构体、`taskCompleteToolSpec(zh bool) ModelTool` 函数、`TurnResult.CompletionSummary` 字段

- [ ] **Step 1: Write the failing test**

```go
// engine/completion_tool_def_test.go
package engine

import (
	"encoding/json"
	"testing"
)

func TestTaskCompleteToolSpec(t *testing.T) {
	spec := taskCompleteToolSpec(true)
	if spec.Function.Name != TaskCompleteToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, TaskCompleteToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	// Verify parameters schema has required "summary" field
	var params struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "summary" {
		t.Errorf("required = %v, want [summary]", params.Required)
	}
}

func TestTaskCompleteToolSpec_English(t *testing.T) {
	spec := taskCompleteToolSpec(false)
	if spec.Function.Name != TaskCompleteToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, TaskCompleteToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestTurnResult_CompletionSummaryField(t *testing.T) {
	tr := TurnResult{Done: true, CompletionSummary: "test summary"}
	if tr.CompletionSummary != "test summary" {
		t.Errorf("CompletionSummary = %q, want %q", tr.CompletionSummary, "test summary")
	}
	// Zero value should be empty string (backward compatible)
	var zero TurnResult
	if zero.CompletionSummary != "" {
		t.Errorf("zero-value CompletionSummary = %q, want empty", zero.CompletionSummary)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run 'TestTaskCompleteToolSpec|TestTurnResult_CompletionSummary' -v`
Expected: FAIL — `TaskCompleteToolName` undefined, `taskCompleteToolSpec` undefined, `TurnResult.CompletionSummary` undefined

- [ ] **Step 3: Write minimal implementation**

In `engine/agent.go`, add the constant alongside existing tool name constants (line 18-20):

```go
const (
	AgentSub          AgentID = "sub"
	AgentCritic       AgentID = "critic"
	AgentTeamLead     AgentID = "team-lead"

	HandoffToolName        = "handoff_to_agent"
	ActivateSkillToolName  = "activate_skill"
	TaskCompleteToolName   = "task_complete"
)
```

Add the params struct and tool spec function after `HandoffToAgentParams` (after line 77):

```go
// TaskCompleteParams is the JSON schema for the task_complete tool call.
type TaskCompleteParams struct {
	Summary string `json:"summary"`
}

// taskCompleteToolSpec returns the tool definition for signaling task completion.
// The model calls this to submit its final output to the user.
func taskCompleteToolSpec(zh bool) ModelTool {
	desc := "Submit your final conclusion or reply to the user. Call this when the user's goal is fully accomplished. This is the ONLY way to return output to the user."
	summaryDesc := "Your final conclusion, analysis result, or reply to the user"
	if zh {
		desc = "提交最终结论或回复给用户。目标全部完成后调用此工具。这是向用户返回输出的唯一方式。"
		summaryDesc = "你的最终结论、分析结果或给用户的回复"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["summary"]
			}`, summaryDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        TaskCompleteToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}
```

In `engine/turn.go`, add `CompletionSummary` to `TurnResult` (line 18-32):

```go
type TurnResult struct {
	Done         bool
	Blocked      bool
	BlockedBy    string
	Questions    []string
	FinishReason string
	LastOp       string
	LastOpError  bool
	VerifyFailedSummary string
	CompletionSummary string // task_complete 工具的 summary 参数，模型显式声明完成时设置
}
```

In `engine/turn.go`, add `task_complete` to `toolSpecsWithHandoff` (line 631-636):

```go
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, handoffToolSpec(e.isChinese))
	specs = append(specs, activateSkillToolSpec())
	specs = append(specs, taskCompleteToolSpec(e.isChinese))
	return specs
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./engine/ -run 'TestTaskCompleteToolSpec|TestTurnResult_CompletionSummary' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add engine/agent.go engine/turn.go engine/completion_tool_def_test.go
git commit -m "feat: add task_complete tool definition and TurnResult.CompletionSummary field"
```

---

### Task 2: task_complete 拦截逻辑

**Files:**
- Modify: `engine/turn.go:465-499`（拦截点 + calls 分离循环）
- Test: `engine/completion_tool_intercept_test.go`（新建）

**Interfaces:**
- Consumes: `TaskCompleteToolName`、`TaskCompleteParams`（from Task 1）、`ToolCallRequest{ID, Name, Input}`（`types.go:176`）
- Produces: `extractTaskComplete(calls []ToolCallRequest) (string, bool)` 函数；`executeTurn` 在检测到 `task_complete` 时返回 `TurnResult{Done: true, CompletionSummary: summary}`

- [ ] **Step 1: Write the failing test**

```go
// engine/completion_tool_intercept_test.go
package engine

import (
	"context"
	"testing"
)

// TestExecuteTurn_TaskComplete_ReturnsDone verifies that when the model
// calls task_complete, executeTurn returns Done=true with the summary.
func TestExecuteTurn_TaskComplete_ReturnsDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "task_complete",
					Arguments: `{"summary":"分析完成：共发现 3 处问题。"}`,
				},
			}},
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0, Goal: "分析"},
		history: []Message{{Role: "user", Content: "分析"}},
		config:  EngineConfig{ModelName: "test"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Fatal("expected Done=true when task_complete is called")
	}
	if result.CompletionSummary != "分析完成：共发现 3 处问题。" {
		t.Errorf("CompletionSummary = %q, want %q", result.CompletionSummary, "分析完成：共发现 3 处问题。")
	}
}

// TestExecuteTurn_TaskComplete_AddsToolResponse verifies that a tool
// response message is appended to history for the API contract.
func TestExecuteTurn_TaskComplete_AddsToolResponse(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "task_complete",
					Arguments: `{"summary":"done"}`,
				},
			}},
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0, Goal: "test"},
		history: []Message{{Role: "user", Content: "test"}},
		config:  EngineConfig{ModelName: "test"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	// History should contain: user, assistant(tool_calls), tool(response)
	if len(e.history) < 3 {
		t.Fatalf("expected at least 3 history messages, got %d", len(e.history))
	}
	last := e.history[len(e.history)-1]
	if last.Role != "tool" {
		t.Errorf("expected last message role=tool, got %q", last.Role)
	}
	if last.ToolCallID != "c1" {
		t.Errorf("expected ToolCallID=c1, got %q", last.ToolCallID)
	}
}

// TestExecuteTurn_RegularToolsStillWork verifies that normal tool calls
// are not affected by the task_complete interception.
func TestExecuteTurn_RegularToolsStillWork(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "读取文件",
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
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0, Goal: "test"},
		history: []Message{{Role: "user", Content: "test"}},
		config:  EngineConfig{ModelName: "test"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Error("expected Done=false for regular tool call (not task_complete)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run 'TestExecuteTurn_TaskComplete|TestExecuteTurn_RegularToolsStillWork' -v`
Expected: FAIL — task_complete not intercepted, falls through to regular tool execution ("tool not found"), Done=false

- [ ] **Step 3: Write minimal implementation**

In `engine/turn.go`, add the `extractTaskComplete` helper function (place near `processActivateSkillCalls`, e.g. after line 1716):

```go
// extractTaskComplete scans tool calls for a task_complete call and returns
// its summary argument. Returns ("", false) if not found or unmarshal fails.
func extractTaskComplete(calls []ToolCallRequest) (string, bool) {
	for _, call := range calls {
		if call.Name != TaskCompleteToolName {
			continue
		}
		var params TaskCompleteParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			return "", false
		}
		return params.Summary, true
	}
	return "", false
}
```

In `engine/turn.go`, add the interception block **after** `pendingActivateMsgs := e.processActivateSkillCalls(calls)` (line 469) and **before** `e.history = append(e.history, assistant)` (line 471):

```go
	pendingActivateMsgs := e.processActivateSkillCalls(calls)

	// Intercept task_complete: model explicitly signals completion.
	if summary, found := extractTaskComplete(calls); found {
		e.history = append(e.history, assistant)
		for _, msg := range pendingActivateMsgs {
			e.history = append(e.history, msg)
		}
		for _, call := range calls {
			if call.Name == TaskCompleteToolName {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Task complete.",
					Timestamp:  time.Now(),
				})
				break
			}
		}
		e.runToolCallCount++
		turnLog.Printf("task_complete intercepted: summary_len=%d", len(summary))
		return TurnResult{Done: true, CompletionSummary: summary, FinishReason: finish}, nil
	}

	e.history = append(e.history, assistant)
```

In `engine/turn.go`, add `TaskCompleteToolName` to the calls separation loop (around line 486-493) so it's excluded from regularCalls:

```go
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else if call.Name == ActivateSkillToolName {
			continue
		} else if call.Name == TaskCompleteToolName {
			continue // handled by interception above
		} else {
			regularCalls = append(regularCalls, call)
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./engine/ -run 'TestExecuteTurn_TaskComplete|TestExecuteTurn_RegularToolsStillWork' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add engine/turn.go engine/completion_tool_intercept_test.go
git commit -m "feat: intercept task_complete tool call in executeTurn"
```

---

### Task 3: Loop 使用 CompletionSummary 构造 EngineResponse

**Files:**
- Modify: `engine/loop.go:729-731`（Done break 处保存 summary）、`engine/loop.go:814-819`（优先使用 CompletionSummary）
- Test: `engine/completion_loop_test.go`（新建）

**Interfaces:**
- Consumes: `TurnResult.CompletionSummary`（from Task 1）
- Produces: `EngineResponse.Summary` 优先取自 `CompletionSummary`

- [ ] **Step 1: Write the failing test**

```go
// engine/completion_loop_test.go
package engine

import (
	"context"
	"testing"
)

// TestRun_TaskComplete_ReturnsSummaryAsResponse verifies that when the
// model calls task_complete, Run() returns the summary as EngineResponse.Summary.
func TestRun_TaskComplete_ReturnsSummaryAsResponse(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta:        "",
			FinishReason: "tool_calls",
			ToolCalls: []ModelToolCall{{
				ID:   "c1",
				Type: "function",
				Function: ModelFunctionCall{
					Name:      "task_complete",
					Arguments: `{"summary":"最终结论：替换方案已确定。"}`,
				},
			}},
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0, Goal: "设计替换方案"},
		history: []Message{{Role: "user", Content: "设计替换方案"}},
		config:  EngineConfig{ModelName: "test"},
		guards:  &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
	}

	resp, err := e.Run(context.Background(), "设计替换方案")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Summary != "最终结论：替换方案已确定。" {
		t.Errorf("Summary = %q, want %q", resp.Summary, "最终结论：替换方案已确定。")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run 'TestRun_TaskComplete_ReturnsSummaryAsResponse' -v`
Expected: FAIL — `EngineResponse.Summary` comes from `buildRunSummary` (walks history for assistant content), not from `CompletionSummary`. Since task_complete has empty Delta, the assistant message content is empty, so the summary won't match.

- [ ] **Step 3: Write minimal implementation**

In `engine/loop.go`, declare a variable before the for loop (before the `for {` that contains `turnResult, err := e.executeTurn(ctx)`). Find the line where `turns := 0` is declared and add alongside:

```go
	turns := 0
	var completionSummary string
```

At the `Done` break point (line 729-731), save the summary:

```go
		if turnResult.Done {
			completionSummary = turnResult.CompletionSummary
			break
		}
```

At the summary construction (line 814-819), prefer CompletionSummary:

```go
	var summary string
	if completionSummary != "" {
		summary = completionSummary
	} else {
		summary = buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh)
	}
	loopLog.Printf("Run done: turns=%d total=%s tool_calls=%d errors=%d usage prompt=%d completion=%d cache_hit=%d cache_miss=%d",
		e.state.TurnNumber, time.Since(e.runStartAt), e.runToolCallCount, e.runErrorCount,
		e.runUsageAccum.PromptTokens, e.runUsageAccum.CompletionTokens,
		e.runUsageAccum.CacheHitTokens, e.runUsageAccum.CacheMissTokens)
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./engine/ -run 'TestRun_TaskComplete_ReturnsSummaryAsResponse' -v`
Expected: PASS

- [ ] **Step 5: Run existing tests to verify no regression**

Run: `go test ./engine/ -run 'TestExecuteTurn|TestRun' -v`
Expected: All PASS (backward compatible — completionSummary is "" when not set by task_complete)

- [ ] **Step 6: Commit**

```bash
git add engine/loop.go engine/completion_loop_test.go
git commit -m "feat: use CompletionSummary for EngineResponse when task_complete is called"
```

---

### Task 4: CompletionToolHook

**Files:**
- Modify: `engine/stop_hook.go`（新增 CompletionToolHook，保留旧 hook 暂不删除）
- Test: `engine/completion_hook_test.go`（新建）

**Interfaces:**
- Consumes: `StopHook` 接口（现有）、`StopHookContext`（现有）、`StopHookResult`（现有）、`ConclusionJudge`（现有，`classifier.go:264`）
- Produces: `CompletionToolHook` 结构体，实现 `StopHook` 接口

- [ ] **Step 1: Write the failing test**

```go
// engine/completion_hook_test.go
package engine

import (
	"context"
	"testing"
)

func TestCompletionToolHook_BlocksTextOnly(t *testing.T) {
	hook := &CompletionToolHook{
		MaxRetries:  4,
		Classifier:  &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "这是分析结果。",
		Goal:               "分析",
		IsChinese:          true,
	})
	if !result.Block {
		t.Error("expected Block=true for text-only without task_complete")
	}
	if result.Message == "" {
		t.Error("expected non-empty nudge Message")
	}
	if result.Reason != "no_completion_tool" {
		t.Errorf("Reason = %q, want 'no_completion_tool'", result.Reason)
	}
}

func TestCompletionToolHook_BlocksEvenWithZeroToolCalls(t *testing.T) {
	// Unlike the old ZeroToolCallHook, CompletionToolHook handles both
	// zero-tool and tools-called cases uniformly.
	hook := &CompletionToolHook{
		MaxRetries:  4,
		Classifier:  &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "答案如下。",
		Goal:               "问答",
		IsChinese:          true,
	})
	if !result.Block {
		t.Error("expected Block=true for text-only with zero tool calls")
	}
}

func TestCompletionToolHook_LLMFallbackAllowsConclusion(t *testing.T) {
	hook := &CompletionToolHook{
		MaxRetries:  2,
		Classifier:  &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2, // >= MaxRetries -> LLM fallback
		LastContent:        "任务完成。",
		Goal:               "修复",
		IsChinese:          true,
	})
	if result.Block {
		t.Error("expected Block=false when LLM says conclusion at MaxRetries")
	}
	if !hook.Classifier.(*stubClassifier).called {
		t.Error("expected Classifier to be called at MaxRetries")
	}
}

func TestCompletionToolHook_LLMFallbackExhaustsOnNotConclusion(t *testing.T) {
	hook := &CompletionToolHook{
		MaxRetries:  2,
		Classifier:  &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2,
		LastContent:        "让我继续查看。",
		Goal:               "修复",
		IsChinese:          true,
	})
	if !result.Exhausted {
		t.Error("expected Exhausted=true when LLM says not conclusion at MaxRetries")
	}
}

func TestCompletionToolHook_LLMFallbackExhaustsOnError(t *testing.T) {
	hook := &CompletionToolHook{
		MaxRetries:  2,
		Classifier:  &stubClassifier{err: errBoom},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2,
		LastContent:        "部分结果。",
		Goal:               "修复",
		IsChinese:          true,
	})
	if !result.Exhausted {
		t.Error("expected Exhausted=true when classifier errors at MaxRetries")
	}
}

func TestCompletionToolHook_BlocksBeforeMaxRetries(t *testing.T) {
	hook := &CompletionToolHook{
		MaxRetries:  4,
		Classifier:  &stubClassifier{conclusion: true},
	}
	// retry < MaxRetries -> should block WITHOUT calling classifier
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1,
		LastContent:        "部分结果。",
		Goal:               "修复",
		IsChinese:          true,
	})
	if !result.Block {
		t.Error("expected Block=true when retry < MaxRetries")
	}
	if hook.Classifier.(*stubClassifier).called {
		t.Error("expected Classifier NOT to be called before MaxRetries")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run 'TestCompletionToolHook' -v`
Expected: FAIL — `CompletionToolHook` undefined

Note: `errBoom` is already defined in the test package (used by `stalled_narration_test.go`). If not, add `var errBoom = fmt.Errorf("boom")` to the test file.

- [ ] **Step 3: Write minimal implementation**

In `engine/stop_hook.go`, add `CompletionToolHook` after the existing `StalledNarrationHook` (do NOT remove old hooks yet):

```go
// CompletionToolHook replaces ZeroToolCallHook and StalledNarrationHook.
// With the task_complete tool, text-only responses (no tool calls) always
// mean the model has not signaled completion. The hook blocks and nudges
// the model to call task_complete or continue using tools. When MaxRetries
// is reached, the LLM ConclusionClassifier is used as a last-resort fallback
// (no keyword heuristics).
type CompletionToolHook struct {
	MaxRetries int
	Classifier ConclusionJudge
}

func (h *CompletionToolHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 4
	}

	// Before MaxRetries: block and nudge, no LLM call.
	if sc.StopHookRetryCount < maxRetries {
		return StopHookResult{
			Block:   true,
			Message: completionNudgeMsg(sc),
			Reason:  "no_completion_tool",
		}
	}

	// MaxRetries reached: LLM fallback.
	if h.Classifier == nil {
		turnLog.Printf("CompletionToolHook: nil Classifier at MaxRetries, exhausting")
		return StopHookResult{Exhausted: true}
	}
	isConclusion, err := h.Classifier.IsConclusion(ctx, ConclusionCheck{
		Goal:            sc.Goal,
		Text:            sc.LastContent,
		ToolCallSummary: sc.ToolCallSummary,
	})
	turnLog.Printf("completion hook fallback: conclusion=%v err=%v retry=%d",
		isConclusion, err, sc.StopHookRetryCount)
	if err != nil {
		return StopHookResult{Exhausted: true}
	}
	if isConclusion {
		return StopHookResult{} // allow exit — model is done but forgot task_complete
	}
	return StopHookResult{Exhausted: true}
}

// completionNudgeMsg builds the bilingual nudge for CompletionToolHook.
func completionNudgeMsg(sc StopHookContext) string {
	if sc.IsChinese {
		return "你的回复未通过 task_complete 提交。调用 task_complete 工具提交最终结论，或继续使用工具执行下一步。"
	}
	return "Your reply was not submitted via task_complete. Call the task_complete tool to submit your final conclusion, or continue using tools to proceed."
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./engine/ -run 'TestCompletionToolHook' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add engine/stop_hook.go engine/completion_hook_test.go
git commit -m "feat: add CompletionToolHook replacing keyword-based stop hooks"
```

---

### Task 5: 接线 CompletionToolHook

**Files:**
- Modify: `cmd/exec.go:27-30`
- Modify: `ui/runner.go:90-93`
- Test: 无新测试（验证编译 + 现有测试通过）

**Interfaces:**
- Consumes: `CompletionToolHook`（from Task 4）
- Produces: hook 注册从 `ZeroToolCallHook` + `StalledNarrationHook` 改为 `CompletionToolHook`

- [ ] **Step 1: Modify cmd/exec.go**

Replace lines 27-30:

```go
	agent.SetStopHooks([]engine.StopHook{
		&engine.ZeroToolCallHook{MaxRetries: 5},
		&engine.StalledNarrationHook{MaxRetries: 4, Classifier: classifier},
	})
```

With:

```go
	agent.SetStopHooks([]engine.StopHook{
		&engine.CompletionToolHook{MaxRetries: 5, Classifier: classifier},
	})
```

- [ ] **Step 2: Modify ui/runner.go**

Replace lines 90-93:

```go
		r.eng.SetStopHooks([]engine.StopHook{
			&engine.ZeroToolCallHook{MaxRetries: 5},
			&engine.StalledNarrationHook{MaxRetries: 4, Classifier: r.eng.NewConclusionClassifier()},
		})
```

With:

```go
		r.eng.SetStopHooks([]engine.StopHook{
			&engine.CompletionToolHook{MaxRetries: 5, Classifier: r.eng.NewConclusionClassifier()},
		})
```

- [ ] **Step 3: Verify compilation and existing tests**

Run: `go build ./... && go test ./engine/ -run 'TestExecuteTurn|TestCompletionToolHook|TestRun' -v`
Expected: Build PASS, tests PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/exec.go ui/runner.go
git commit -m "refactor: wire CompletionToolHook replacing ZeroToolCallHook and StalledNarrationHook"
```

---

### Task 6: 系统提示词 — 完成信号

**Files:**
- Modify: `context/promptset/zh/system.md`（"回复格式"节后追加）
- Test: 无代码测试（人工检查 + 编译验证 `go:embed`）

- [ ] **Step 1: Add "完成信号" section to system.md**

In `context/promptset/zh/system.md`, after the "回复格式" section (after line 68, before "# 工具使用策略"), add:

```markdown

# 完成信号
目标全部完成后，调用 `task_complete` 工具提交最终结论。这是向用户返回输出的唯一方式。
- 有工具调用时继续执行，不要给出纯文本结论
- 简单问答也通过 `task_complete` 返回答案
- 纯文本回复（无任何工具调用）会被拦截并要求重新提交
```

- [ ] **Step 2: Verify embed compiles**

Run: `go build ./context/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add context/promptset/zh/system.md
git commit -m "feat: add completion signal instructions to system prompt"
```

---

### Task 7: 子代理 task_complete 检测

**Files:**
- Modify: `engine/sub_agent.go:330-384`（替换 isIntermediateText + hasTrailingNextStepIntent 内联逻辑）
- Test: `engine/sub_agent_completion_test.go`（新建）

**Interfaces:**
- Consumes: `TaskCompleteToolName`、`TaskCompleteParams`（from Task 1）、`extractTaskComplete`（from Task 2）
- Produces: 子代理在模型调用 `task_complete` 时终止并返回 summary

- [ ] **Step 1: Write the failing test**

```go
// engine/sub_agent_completion_test.go
package engine

import (
	"testing"
)

// TestExtractTaskComplete_FromSubAgentCalls verifies the helper works
// with the call format used in sub_agent.go.
func TestExtractTaskComplete_FromSubAgentCalls(t *testing.T) {
	calls := []ToolCallRequest{
		{ID: "c1", Name: "read", Input: nil},
		{ID: "c2", Name: "task_complete", Input: []byte(`{"summary":"子代理完成"}`)},
	}
	summary, found := extractTaskComplete(calls)
	if !found {
		t.Fatal("expected found=true")
	}
	if summary != "子代理完成" {
		t.Errorf("summary = %q, want %q", summary, "子代理完成")
	}
}

// TestExtractTaskComplete_NotFound verifies false when no task_complete.
func TestExtractTaskComplete_NotFound(t *testing.T) {
	calls := []ToolCallRequest{
		{ID: "c1", Name: "read", Input: nil},
	}
	_, found := extractTaskComplete(calls)
	if found {
		t.Error("expected found=false when no task_complete call")
	}
}
```

- [ ] **Step 2: Run test to verify it passes** (extractTaskComplete already exists from Task 2)

Run: `go test ./engine/ -run 'TestExtractTaskComplete' -v`
Expected: PASS (function already implemented in Task 2)

- [ ] **Step 3: Modify sub_agent.go to use task_complete detection**

In `engine/sub_agent.go`, replace the `isIntermediateText` call at line 335:

```go
		// Strip intermediate thinking text from content when tool calls exist
		// AND none of them is task_complete. With task_complete, the content
		// is the model's narration alongside the completion signal — keep it.
		if len(msg.ToolCalls) > 0 {
			if _, isCompletion := extractTaskComplete(toolCallsToRequests(msg.ToolCalls)); isCompletion {
				result := r.buildResult(extractCompletionSummary(msg.ToolCalls), input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
		}
```

Replace the no-tool-calls block (lines 342-384). The new logic: no tool calls = not done (model should call task_complete). Use the classifier only as fallback after consecutiveIntermediate threshold:

```go
		// No tool calls -> model gave text-only without task_complete.
		// Nudge: the model should call task_complete to submit, or use tools.
		if len(msg.ToolCalls) == 0 {
			if input.NoNudge {
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			consecutiveIntermediate++
			if consecutiveIntermediate >= 3 {
				// Break — model keeps producing text without task_complete
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			history = append(history, Message{
				Role:    "user",
				Content: "调用 task_complete 工具提交最终结论，或继续使用工具执行下一步。",
			})
			continue
		}

		consecutiveIntermediate = 0
```

Add helper functions at the end of sub_agent.go (or near the top):

```go
// toolCallsToRequests converts ModelToolCall slice to ToolCallRequest slice
// for use with extractTaskComplete.
func toolCallsToRequests(calls []ModelToolCall) []ToolCallRequest {
	out := make([]ToolCallRequest, len(calls))
	for i, c := range calls {
		out[i] = ToolCallRequest{
			ID:    c.ID,
			Name:  c.Function.Name,
			Input: json.RawMessage(c.Function.Arguments),
		}
	}
	return out
}

// extractCompletionSummary extracts the summary from a task_complete tool call.
func extractCompletionSummary(calls []ModelToolCall) string {
	reqs := toolCallsToRequests(calls)
	if summary, found := extractTaskComplete(reqs); found {
		return summary
	}
	return ""
}
```

- [ ] **Step 4: Verify compilation**

Run: `go build ./engine/`
Expected: PASS

- [ ] **Step 5: Run sub-agent tests**

Run: `go test ./engine/ -run 'TestSubAgent|TestExtractTaskComplete' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_completion_test.go
git commit -m "refactor: sub-agent uses task_complete detection instead of keyword heuristics"
```

---

### Task 8: 移除关键词启发式 + 旧 hook

**Files:**
- Modify: `engine/classifier.go`（移除关键词函数和变量）
- Modify: `engine/stop_hook.go`（移除 ZeroToolCallHook、StalledNarrationHook、stalledNudgeMsg）
- Modify: `engine/turn.go:191`（移除 isIntermediateText 调用）
- Test: 验证编译 + 现有测试通过

**Interfaces:**
- Consumes: Tasks 4-7 已将所有调用方替换为 CompletionToolHook / task_complete 检测
- Produces: 无 — 纯删除

- [ ] **Step 1: Remove isIntermediateText call from turn.go:191**

In `engine/turn.go`, remove lines 188-193:

```go
	// Layer 3: When tool calls exist, strip intermediate thinking text from content.
	// The model sometimes outputs intent text ("Let me...", "让我...") alongside
	// DSML tool calls. This text is noise - tool results provide execution context.
	if hasValidToolCalls(toolCalls) && isIntermediateText(content) {
		content = ""
	}
```

- [ ] **Step 2: Remove keyword functions from classifier.go**

In `engine/classifier.go`, remove:
- `hasTrailingNextStepIntent` function (line 55-117)
- `lastSentence` function (line 123-139)
- `isSentDelim` function (line 144-157)
- `isIntermediateText` function (line 170-191)
- `completionMarkers` variable (line 197-208)
- `futureIntentMarkers` variable (line 213-219)
- `hasFutureIntent` function (line 227-235)
- `hasCompletionMarker` function (line 243-251)

Keep: `rememberRe`, `extractRememberMarkers`, `ConclusionCheck`, `ConclusionJudge`, `ConclusionClassifier`, `NewConclusionClassifier`, `IsConclusion`, `parseConclusionJSON`, `pickClassifierPrompt`, prompt constants, `IntentCheck`, `IntentJudge`, `IntentClassifier`, `NewIntentClassifier`, `Classify`, `parseIntentJSON`, `intentFromString`, `pickIntentPrompt`, intent prompt constants, `buildToolCallSummary`.

- [ ] **Step 3: Remove old hooks from stop_hook.go**

In `engine/stop_hook.go`, remove:
- `ZeroToolCallHook` struct and its `Check` method (line 40-67)
- `StalledNarrationHook` struct and its `Check` method (line 69-156)
- `stalledNudgeMsg` function (line 160-173)

Keep: `StopHookContext`, `StopHookResult`, `StopHook` interface, `SetStopHooks`, `NewConclusionClassifier`, `runStopHooks`, and the new `CompletionToolHook` + `completionNudgeMsg`.

- [ ] **Step 4: Verify compilation**

Run: `go build ./...`
Expected: PASS — no remaining references to removed functions

- [ ] **Step 5: Run all engine tests**

Run: `go test ./engine/ -v -count=1`
Expected: Some tests in `stalled_narration_test.go` and `classifier_test.go` will FAIL (they reference removed functions). These are rewritten in Task 9.

- [ ] **Step 6: Commit (with broken tests — will fix in Task 9)**

```bash
git add engine/classifier.go engine/stop_hook.go engine/turn.go
git commit -m "refactor: remove keyword heuristics and old stop hooks

Removed: hasFutureIntent, hasTrailingNextStepIntent, hasCompletionMarker,
isIntermediateText, completionMarkers, futureIntentMarkers, ZeroToolCallHook,
StalledNarrationHook, stalledNudgeMsg. Tests to be rewritten in next commit."
```

---

### Task 9: 重写测试

**Files:**
- Modify: `engine/stalled_narration_test.go`（改写为 CompletionToolHook 测试）
- Modify: `engine/classifier_test.go`（移除关键词函数测试）
- Test: 所有测试通过

**Interfaces:**
- Consumes: `CompletionToolHook`（from Task 4）
- Produces: 无 — 纯测试改写

- [ ] **Step 1: Rewrite stalled_narration_test.go**

Replace the entire file with tests for `CompletionToolHook`. Keep the `stubClassifier` helper (lines 9-21) since it's reused. Remove all `StalledNarrationHook` and `ZeroToolCallHook` tests. The `CompletionToolHook` tests already exist in `completion_hook_test.go` (Task 4), so this file becomes:

```go
package engine

import (
	"context"
	"testing"
)

// stubClassifier is a controllable ConclusionJudge stub.
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

var errBoom = fmt.Errorf("boom")

// CompletionToolHook tests are in completion_hook_test.go.
// This file retains the shared stubClassifier and errBoom used across
// completion-related test files.
```

Note: `errBoom` may already be defined elsewhere. If so, remove the duplicate and keep only the `stubClassifier` definition. Check with: `grep -r 'errBoom' engine/*_test.go`

- [ ] **Step 2: Rewrite classifier_test.go**

Remove tests for `isIntermediateText` and `hasTrailingNextStepIntent`. Keep tests for `extractRememberMarkers`, `parseConclusionJSON`, `parseIntentJSON` if they exist. Run `grep -n 'func Test' engine/classifier_test.go` to see what's there.

- [ ] **Step 3: Run all engine tests**

Run: `go test ./engine/ -v -count=1`
Expected: All PASS

- [ ] **Step 4: Run full test suite**

Run: `go test ./... -count=1`
Expected: All PASS

- [ ] **Step 5: Commit**

```bash
git add engine/stalled_narration_test.go engine/classifier_test.go
git commit -m "test: rewrite tests for CompletionToolHook, remove keyword heuristic tests"
```

---

## Self-Review

### 1. Spec coverage

| Spec 要求 | 实现任务 |
|-----------|----------|
| task_complete 工具定义 | Task 1 |
| TurnResult.CompletionSummary 字段 | Task 1 |
| executeTurn 拦截 task_complete | Task 2 |
| Loop 使用 CompletionSummary | Task 3 |
| CompletionToolHook（合并 ZeroToolCall + StalledNarration） | Task 4 |
| 接线 cmd/exec.go + ui/runner.go | Task 5 |
| 系统提示词"完成信号"节 | Task 6 |
| 子代理 task_complete 检测 | Task 7 |
| 移除关键词函数 + 旧 hook | Task 8 |
| 重写测试 | Task 9 |

✅ 全部覆盖。

### 2. Placeholder scan

✅ 无 TBD/TODO。所有代码步骤包含完整代码。Task 8 的删除步骤引用了精确行号和函数名。

### 3. Type consistency

- `TaskCompleteToolName` = `"task_complete"` — Task 1 定义，Task 2/7 使用 ✅
- `TaskCompleteParams{Summary string}` — Task 1 定义，Task 2 `extractTaskComplete` 使用 ✅
- `TurnResult.CompletionSummary` — Task 1 定义，Task 2 设置，Task 3 读取 ✅
- `CompletionToolHook{MaxRetries int, Classifier ConclusionJudge}` — Task 4 定义，Task 5 接线 ✅
- `extractTaskComplete(calls []ToolCallRequest) (string, bool)` — Task 2 定义，Task 7 使用 ✅
- `completionNudgeMsg(sc StopHookContext) string` — Task 4 定义 ✅
