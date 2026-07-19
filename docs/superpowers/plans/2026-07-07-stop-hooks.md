# Stop Hooks 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用结构化的 stop hook 机制替换 `isIntermediateText` 文本模式匹配，解决 agent 输出中间叙述后循环终止的问题。

**Architecture:** 参照 Claude Code 的 stop hooks 架构——当模型输出纯文本（无 tool_call）时，运行已注册的 stop hooks。内置 `ZeroToolCallHook` 检查 `runToolCallCount == 0`（行为信号），若为零则注入 nudge 继续循环。计数器限制最多 3 次（仿 sub_agent 3-strike），之后放行。

**Tech Stack:** Go 1.24, engine 包内部改动，无新依赖

## Global Constraints

- 不修改 `isIntermediateText` 函数本身（Layer 3 仍需要它）
- 不修改 `finish == "length"` 分支（max_output_tokens 恢复是独立路径）
- 不给 `sub_agent.go` 接 stop hook 机制本身（子代理走独立的 `runLoop`，不复用 `runStopHooks`）；但子代理纯文本分支需复用 `looksLikeNextStepNarration` 做结论识别--原 3-strike 计数器在工具调用时归零，critic 裁决被 nudge 后再调工具会空转到 maxIterations（详见 spec 2026-07-08 修订）
- 不添加外部/用户可配置 hook（接口预留扩展，当前只做内置）
- nudge 文案采用 `sub_agent.go:745` 的 `getNudgeMessage` 文案

## File Structure

| 文件 | 责任 |
|---|---|
| `engine/stop_hook.go` | **新建**：StopHook 接口、StopHookContext、StopHookResult、ZeroToolCallHook、SetStopHooks、runStopHooks |
| `engine/stop_hook_test.go` | **新建**：ZeroToolCallHook 单元测试 + runStopHooks 单元测试 |
| `engine/loop.go` | **修改**：Engine 加 `stopHooks`/`stopHookActive`/`stopHookRetryCount` 字段；Run() 开头重置 |
| `engine/turn.go` | **修改**：删除 `isIntermediateText` 分支，替换为 `runStopHooks` 调用；工具调用后重置 `stopHookRetryCount` |
| `engine/turn_test.go` | **修改**：更新两个现有测试适配 stop hooks |
| `cmd/exec.go` | **修改**：注册 `ZeroToolCallHook` |
| `ui/runner.go` | **修改**：注册 `ZeroToolCallHook` |

---

### Task 1: StopHook 接口 + ZeroToolCallHook

**Files:**
- Create: `engine/stop_hook.go`
- Create: `engine/stop_hook_test.go`

**Interfaces:**
- Produces: `StopHook` 接口、`StopHookContext`/`StopHookResult` 类型、`ZeroToolCallHook` 实现

- [ ] **Step 1: Write the failing test**

Create `engine/stop_hook_test.go`:

```go
package engine

import "testing"

func TestZeroToolCallHook_BlocksWhenNoToolCalls(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when runToolCallCount=0")
	}
	if result.Reason != "zero_tool_calls" {
		t.Errorf("expected Reason='zero_tool_calls', got %q", result.Reason)
	}
	if result.Message == "" {
		t.Errorf("expected non-empty nudge Message")
	}
}

func TestZeroToolCallHook_PassesWhenToolsCalled(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   1,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount>0")
	}
}

func TestZeroToolCallHook_PassesAfterMaxRetries(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 3,
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when retryCount>=maxRetries")
	}
}

func TestZeroToolCallHook_DefaultMaxRetries(t *testing.T) {
	hook := &ZeroToolCallHook{} // MaxRetries=0 → default 3
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 2,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when retryCount=2 < default maxRetries=3")
	}
}

func TestZeroToolCallHook_EnglishMessage(t *testing.T) {
	hook := &ZeroToolCallHook{MaxRetries: 3}
	result := hook.Check(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          false,
	})
	if result.Block && result.Message == "" {
		t.Errorf("expected non-empty English nudge Message")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestZeroToolCallHook -v`
Expected: FAIL — `undefined: StopHookContext, ZeroToolCallHook`

- [ ] **Step 3: Write minimal implementation**

Create `engine/stop_hook.go`:

```go
package engine

// StopHookContext carries the context a stop hook needs to decide whether
// the model's text-only response should end the loop or be nudged to continue.
type StopHookContext struct {
	RunToolCallCount   int    // total tool calls in this Run() so far
	LastContent        string // model's last text output
	FinishReason       string // finish reason (stop/length/etc)
	StopHookActive     bool   // true if this turn was triggered by a prior hook block
	StopHookRetryCount int    // consecutive hook-triggered continuations
	IsChinese          bool   // language preference for nudge message
}

// StopHookResult is what a stop hook returns.
type StopHookResult struct {
	Block   bool   // if true, inject Message and continue the loop
	Message string // nudge message injected as a user message (when Block=true)
	Reason  string // block reason (for logging)
}

// StopHook is checked when the model outputs text without tool calls.
// A blocking hook injects a nudge message and continues the agent loop
// instead of terminating. Modeled after Claude Code's stop hooks pattern.
type StopHook interface {
	Check(ctx StopHookContext) StopHookResult
}

// ZeroToolCallHook blocks loop exit when the model has not called any tools
// this Run(). A text-only response with zero prior tool calls cannot be a
// final conclusion — the model is narrating intent without acting.
// Blocks up to MaxRetries times (default 3), then allows exit.
type ZeroToolCallHook struct {
	MaxRetries int
}

func (h *ZeroToolCallHook) Check(ctx StopHookContext) StopHookResult {
	if ctx.RunToolCallCount > 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if ctx.StopHookRetryCount >= maxRetries {
		return StopHookResult{}
	}
	msg := "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	if !ctx.IsChinese {
		msg = "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
	}
	return StopHookResult{Block: true, Message: msg, Reason: "zero_tool_calls"}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestZeroToolCallHook -v`
Expected: PASS — all 5 tests

- [ ] **Step 5: Commit**

```bash
cd /Users/admin/gitspace/deepact
git add engine/stop_hook.go engine/stop_hook_test.go
git commit -m "feat: add StopHook interface and ZeroToolCallHook"
```

---

### Task 2: Engine 集成 — 字段 + SetStopHooks + runStopHooks + 重置

**Files:**
- Modify: `engine/loop.go` (Engine struct ~line 101, Run() reset ~line 232)
- Append: `engine/stop_hook.go` (SetStopHooks + runStopHooks methods)
- Append: `engine/stop_hook_test.go` (runStopHooks tests)

**Interfaces:**
- Consumes: `StopHook` interface, `StopHookContext`, `StopHookResult` from Task 1
- Produces: `Engine.stopHooks` field, `Engine.SetStopHooks()` method, `Engine.runStopHooks()` method, Run() resets

- [ ] **Step 1: Write the failing test**

Append to `engine/stop_hook_test.go`:

```go
func TestRunStopHooks_FirstBlockingResult(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			&ZeroToolCallHook{MaxRetries: 3},
		},
	}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when runToolCallCount=0")
	}
}

func TestRunStopHooks_NoHooksRegistered(t *testing.T) {
	e := &Engine{}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount: 0,
	})
	if result.Block {
		t.Errorf("expected Block=false when no hooks registered")
	}
}

func TestRunStopHooks_HookPassesThrough(t *testing.T) {
	e := &Engine{
		stopHooks: []StopHook{
			&ZeroToolCallHook{MaxRetries: 3},
		},
	}
	result := e.runStopHooks(StopHookContext{
		RunToolCallCount: 5,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount>0")
	}
}

func TestSetStopHooks(t *testing.T) {
	e := &Engine{}
	e.SetStopHooks([]StopHook{&ZeroToolCallHook{MaxRetries: 3}})
	if len(e.stopHooks) != 1 {
		t.Errorf("expected 1 hook registered, got %d", len(e.stopHooks))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestRunStopHooks|TestSetStopHooks' -v`
Expected: FAIL — `e.stopHooks undefined` and `e.runStopHooks undefined`

- [ ] **Step 3: Add Engine fields to loop.go**

Edit `engine/loop.go` — find:

```go
	runToolCallCount int
	// runStartHistoryLen is the index in e.history where the current Run()'s
```

Replace with:

```go
	runToolCallCount int
	// stopHookActive is true when the current turn was triggered by a stop
	// hook blocking the previous turn's exit (mirrors Claude Code's stopHookActive).
	stopHookActive bool
	// stopHookRetryCount tracks consecutive stop-hook-triggered continuations.
	// Reset to 0 when tools are called or at the start of each Run().
	stopHookRetryCount int
	// stopHooks are checked when the model outputs text without tool calls.
	stopHooks []StopHook
	// runStartHistoryLen is the index in e.history where the current Run()'s
```

- [ ] **Step 4: Add Run() resets to loop.go**

Edit `engine/loop.go` — find:

```go
	e.runToolCallCount = 0
	e.runErrorCount = 0
```

Replace with:

```go
	e.runToolCallCount = 0
	e.runErrorCount = 0
	e.stopHookActive = false
	e.stopHookRetryCount = 0
```

- [ ] **Step 5: Add SetStopHooks + runStopHooks to stop_hook.go**

Append to `engine/stop_hook.go`:

```go
// SetStopHooks registers stop hooks checked when the model outputs text
// without tool calls. A blocking hook injects a nudge message and continues
// the agent loop instead of terminating.
func (e *Engine) SetStopHooks(hooks []StopHook) {
	e.stopHooks = hooks
}

// runStopHooks executes registered stop hooks and returns the first blocking
// result. If no hook blocks, returns an empty result (loop may terminate).
func (e *Engine) runStopHooks(ctx StopHookContext) StopHookResult {
	for _, hook := range e.stopHooks {
		result := hook.Check(ctx)
		if result.Block {
			return result
		}
	}
	return StopHookResult{}
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestRunStopHooks|TestSetStopHooks' -v`
Expected: PASS — all 4 tests

- [ ] **Step 7: Commit**

```bash
cd /Users/admin/gitspace/deepact
git add engine/loop.go engine/stop_hook.go engine/stop_hook_test.go
git commit -m "feat: add Engine stop hooks integration (fields, SetStopHooks, runStopHooks)"
```

---

### Task 3: 替换 turn.go 的 isIntermediateText 分支

**Files:**
- Modify: `engine/turn.go` (~line 226-243, ~line 483)
- Modify: `engine/turn_test.go` (update 2 existing tests)

**Interfaces:**
- Consumes: `Engine.runStopHooks()` from Task 2, `StopHookContext` from Task 1
- Produces: stop hooks wired into the turn execution flow

- [ ] **Step 1: Update existing tests to expect stop hook behavior**

Edit `engine/turn_test.go` — find `TestExecuteTurn_IntermediateTextWithoutToolCall_Nudges`:

```go
func TestExecuteTurn_IntermediateTextWithoutToolCall_Nudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "让我看测试基础设施...", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "执行方案"}},
		config:  EngineConfig{ModelName: "test-model"},
	}
```

Replace with:

```go
func TestExecuteTurn_ZeroToolCalls_StopHookNudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 buildResult 如何提取 Summary", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 0},
		history:   []Message{{Role: "user", Content: "执行方案"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}},
		isChinese: true,
	}
```

Then find the assertion at the end of that test:

```go
	if result.Done {
		t.Errorf("expected Done=false (intermediate text should nudge, not end), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
}
```

Replace with:

```go
	if result.Done {
		t.Errorf("expected Done=false (zero tool calls → stop hook should nudge), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
	if result.FinishReason != "stop" {
		t.Errorf("expected FinishReason='stop', got %q", result.FinishReason)
	}
}
```

Then find `TestExecuteTurn_FinalTextWithoutToolCall_Done`:

```go
func TestExecuteTurn_FinalTextWithoutToolCall_Done(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "执行方案"}},
		config:  EngineConfig{ModelName: "test-model"},
	}
```

Replace with:

```go
func TestExecuteTurn_FinalTextAfterToolCalls_Done(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 0},
		history:          []Message{{Role: "user", Content: "执行方案"}},
		config:           EngineConfig{ModelName: "test-model"},
		stopHooks:        []StopHook{&ZeroToolCallHook{MaxRetries: 3}},
		isChinese:        true,
		runToolCallCount: 2, // prior tool calls → hook won't block
	}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestExecuteTurn_ZeroToolCalls_StopHookNudges|TestExecuteTurn_FinalTextAfterToolCalls_Done' -v`
Expected: FAIL — old code still uses `isIntermediateText`; "查看 buildResult..." doesn't match patterns → Done=true (test expects Done=false)

- [ ] **Step 3: Replace isIntermediateText branch in turn.go**

Edit `engine/turn.go` — find:

```go
		// The model emitted intermediate intent text ("让我...", "Let me...")
		// without a tool call — it intended to act but stopped short of
		// invoking a tool. Nudge it to actually use a tool instead of
		// treating the bare intent text as a final reply, which would
		// prematurely end the agent loop.
		if isIntermediateText(content) {
			nudge := "继续，请直接使用工具执行下一步。"
			if !e.isChinese {
				nudge = "Continue — invoke a tool to perform the next step."
			}
			e.history = append(e.history, Message{Role: "user", Content: nudge, Timestamp: time.Now()})
			turnLog.Printf("intermediate intent text without tool call, nudging (finish=%s): %q", finish, content)
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		return TurnResult{Done: true, FinishReason: finish}, nil
```

Replace with:

```go
		// Run stop hooks — structured checks that decide whether the model's
		// text-only response should end the loop or be nudged to continue.
		// Replaces the former isIntermediateText pattern-matching approach
		// with behavioral signals (e.g. runToolCallCount).
		hookResult := e.runStopHooks(StopHookContext{
			RunToolCallCount:   e.runToolCallCount,
			LastContent:        content,
			FinishReason:       finish,
			StopHookActive:     e.stopHookActive,
			StopHookRetryCount: e.stopHookRetryCount,
			IsChinese:          e.isChinese,
		})
		if hookResult.Block {
			e.history = append(e.history, Message{
				Role: "user", Content: hookResult.Message, Timestamp: time.Now(),
			})
			e.stopHookActive = true
			e.stopHookRetryCount++
			turnLog.Printf("stop hook blocked: reason=%s retry=%d", hookResult.Reason, e.stopHookRetryCount)
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		return TurnResult{Done: true, FinishReason: finish}, nil
```

- [ ] **Step 4: Add stopHookRetryCount reset after tool calls**

Edit `engine/turn.go` — find:

```go
		e.runToolCallCount += len(regularCalls)
```

Replace with:

```go
		e.runToolCallCount += len(regularCalls)
		e.stopHookRetryCount = 0 // reset on tool calls — agent is making progress
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -run 'TestExecuteTurn_ZeroToolCalls_StopHookNudges|TestExecuteTurn_FinalTextAfterToolCalls_Done' -v`
Expected: PASS — both tests

- [ ] **Step 6: Run full engine test suite for regressions**

Run: `cd /Users/admin/gitspace/deepact && go test ./engine/ -v -count=1`
Expected: PASS — no regressions. `isIntermediateText` is still used in Layer 3 (line 191) and sub_agent.go (line 326).

- [ ] **Step 7: Commit**

```bash
cd /Users/admin/gitspace/deepact
git add engine/turn.go engine/turn_test.go
git commit -m "feat: replace isIntermediateText with stop hooks in executeTurn"
```

---

### Task 4: 注册 ZeroToolCallHook

**Files:**
- Modify: `cmd/exec.go` (line 25)
- Modify: `ui/runner.go` (line 89)

**Interfaces:**
- Consumes: `Engine.SetStopHooks()` from Task 2, `ZeroToolCallHook` from Task 1

- [ ] **Step 1: Register hook in cmd/exec.go**

Edit `cmd/exec.go` — find:

```go
	agent := engine.NewEngine(config, deps)
```

Replace with:

```go
	agent := engine.NewEngine(config, deps)
	agent.SetStopHooks([]engine.StopHook{&engine.ZeroToolCallHook{MaxRetries: 3}})
```

- [ ] **Step 2: Register hook in ui/runner.go**

Edit `ui/runner.go` — find:

```go
		r.eng = engine.NewEngine(r.Config, r.Deps)
```

Replace with:

```go
		r.eng = engine.NewEngine(r.Config, r.Deps)
		r.eng.SetStopHooks([]engine.StopHook{&engine.ZeroToolCallHook{MaxRetries: 3}})
```

- [ ] **Step 3: Verify compilation**

Run: `cd /Users/admin/gitspace/deepact && go build ./...`
Expected: no errors

- [ ] **Step 4: Run full test suite**

Run: `cd /Users/admin/gitspace/deepact && go test ./... -count=1`
Expected: PASS — all tests pass

- [ ] **Step 5: Commit**

```bash
cd /Users/admin/gitspace/deepact
git add cmd/exec.go ui/runner.go
git commit -m "feat: register ZeroToolCallHook in engine creation points"
```
