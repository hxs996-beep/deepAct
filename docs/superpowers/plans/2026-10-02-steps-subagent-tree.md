# Steps 树（子代理挂到所属步骤下）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把输入框上方的 `Sub-Agents` 与 `Steps` 两个平铺面板合并成"步骤主干 + 子代理缩进挂载"的单一面板，并修掉事件层按角色名 FIFO 配对导致的错位。

**架构：** 引擎侧给 `agent_start`/`agent_done` 加稳定 `RunID`（同步 = handoff tool call id，异步 = `bg-N`），并新增显式的步骤关联字段 `Step`；UI 只按 `RunID` 配对、按 `SubAgentStatus.Step == TodoItem.ID` 分组渲染，无归属的子代理保留独立的 `Sub-Agents` 块。

**技术栈：** Go 1.2x、Bubble Tea + lipgloss（UI）、标准 `testing`（表格驱动为主）。

**规格：** `docs/superpowers/specs/2026-10-02-steps-subagent-tree-design.md`（已批准，commit `a8efd9e`）

**分支：** 当前分支 `feat/subagent-run-management`（`git worktree list` 只有一个工作区，无需新建）。

**约定：** 每个任务结束跑一次 `go build ./...`；涉及引擎行为的任务跑 `go test ./engine/...`，涉及 UI 的跑 `go test ./ui/...`。命令均在仓库根目录执行。

---

## 文件结构

| 文件 | 职责 | 本计划中的改动 |
|---|---|---|
| `engine/types.go` | 引擎公共数据形状 | `TodoItem.ID`；`ProgressEvent.RunID/Step/Status` |
| `engine/agent.go` | 代理与工具 spec | `HandoffToAgentParams.Step`；`handoffToolSpec`、`todoWriteToolSpec` 的 schema 与描述 |
| `engine/handoff.go` | 委派入口（同步/异步分派） | `dispatchAsync` 发出带 `RunID` 的 `agent_start`；`bgTask.step` |
| `engine/loop.go` | Run 主循环与后台任务表 | `bgTask.step`；`finishBackgroundJob` 的终态事件 |
| `engine/turn.go` | 单轮工具执行与拦截 | 去掉重复的异步 `agent_start`；不再把派发回执当 `agent_done`；`todo_write` id 唯一性校验 |
| `engine/agent_manage.go` | `close_agent` 生命周期 | 终态事件带 `RunID`/`Step` |
| `engine/suspend.go` | 挂起 / 恢复链路 | 再挂起事件带 `RunID`/`Step` |
| `ui/model.go` | UI 状态与渲染 | `ProgressMsg` 与 `SubAgentStatus` 字段；按 `RunID` 配对；`renderStepsPanel` 取代两个旧渲染器 |
| `ui/runner.go` | 引擎事件 → UI 消息 | 转发新字段 |
| `skill/builtin/collab/SKILL.md` | collab 编排提示 | 让模型使用 `id` / `step` |

`renderSubAgentPanel`、`renderTodoList`、`renderOverlayStatus` 三个函数被 `renderStepsPanel` 取代后删除（保留其测试覆盖，迁到新渲染器上）。

---

## 任务 1：事件通道带上稳定 `RunID` / `Step` / `Status`

**文件：**
- 修改：`engine/types.go:27-45`
- 修改：`engine/agent.go:212-236`、`engine/agent.go:325-434`

本任务是纯数据形状扩展，没有可断言的行为变化，因此没有失败测试——真正的行为由任务 2~5 覆盖。

- [ ] **步骤 1：扩展 `TodoItem` 与 `ProgressEvent`**

`engine/types.go` 中把 `TodoItem` 与 `ProgressEvent` 替换为：

```go
// TodoItem is a generic step-tracking item reported by the todo_write tool.
// It is skill-agnostic: any skill can instruct the model to report its
// step-by-step progress through this channel, and the UI renders it as a
// plain-text todo list (no skill-specific theming).
type TodoItem struct {
	// ID is an optional stable key the model assigns to the step. It exists so
	// a delegated sub-agent can name the step it executes
	// (handoff_to_agent.step) and be nested under it, instead of the UI
	// guessing from step wording. Empty = the step dispatches no work.
	ID      string `json:"id,omitempty"`
	Content string `json:"content"` // step description (plain text)
	Status  string `json:"status"`  // "pending" | "in_progress" | "completed"
}

type ProgressEvent struct {
	Type       string // "tool_start" | "tool_done" | "thinking" | "content_delta" | "reasoning_delta" | "agent_start" | "agent_done" | "usage" | "todo_update"
	Name       string
	Detail     string // brief digest for live display
	FullDetail string // full content (e.g., diff) for final rendering
	Usage      *ModelUsage
	ModelName  string // which model was used for this API call
	// Todos carries the full todo-list snapshot for Type == "todo_update".
	Todos []TodoItem
	// RunID is the stable handle of a depth-0 delegation: the handoff tool
	// call id for a synchronous run, "bg-<seq>" for an async one. agent_start
	// and its agent_done carry the SAME value, so the UI pairs them by
	// identity — concurrent same-role agents are otherwise indistinguishable,
	// and name-based pairing closes the wrong row.
	RunID string
	// Step is the TodoItem.ID this sub-agent executes; empty = not linked to
	// any step, and the UI groups it separately.
	Step string
	// Status is the outcome carried by agent_done: "ok", "error" or
	// "cancelled". Empty on every other event.
	Status string
}
```

- [ ] **步骤 2：给 `handoff_to_agent` 加 `step` 参数**

`engine/agent.go` 的 `HandoffToAgentParams` 追加字段：

```go
	// Step optionally names the todo_write step (its "id" field) this
	// sub-agent executes. It is echoed back on the sub-agent's progress events
	// so the UI can nest the run under that step. Empty = no step linkage.
	Step string `json:"step,omitempty"`
```

- [ ] **步骤 3：把 `step` 暴露进工具 schema**

在 `handoffToolSpec` 的英文/中文描述块中各加一行（紧挨 `async` 之后）：

```go
	stepDesc := `Optional id of the todo_write step this sub-agent executes (that step's "id" field). It is echoed back on the sub-agent's progress events, and the UI nests the sub-agent under that step. Omit it when this delegation does not correspond to a step.`
```

```go
		stepDesc = `（可选）本子代理执行的 todo_write 步骤 id（该步骤的 "id" 字段）。它会随进度事件回传，UI 据此把子代理嵌到该步骤下。若本次委派不对应任何步骤则省略。`
```

在 `params` 模板的 `"async"` 属性之后追加：

```go
					"step": {
						"type": "string",
						"description": %q
					}
```

并把 `fmt.Sprintf` 的参数表末尾补上 `stepDesc`：

```go
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc, expectedOutputDesc, forkTurnsDesc, asyncDesc, stepDesc)
```

- [ ] **步骤 4：把 `id` 暴露进 `todo_write` schema**

`todoWriteToolSpec` 的 `Description` 追加一句、`properties` 追加 `id`：

```go
			Description: "Report the current state of your step-by-step todo list. Call this whenever you start, complete, or change the status of a step. Pass the FULL list of steps each time (snapshot, not diff). The UI displays it as a plain-text todo list. Status must be one of: pending, in_progress, completed. Give a step an \"id\" when you delegate work for it: handoff_to_agent(step=<id>) then nests that sub-agent under this step in the UI. Ids must be unique within the list when present.",
```

```go
								"id": {
									"type": "string",
									"description": "Optional stable key for this step, unique within the list. handoff_to_agent(step=<id>) links a sub-agent to it."
								},
```

- [ ] **步骤 5：编译**

运行：`go build ./...`
预期：通过（新字段尚无生产者，无编译错误）。

- [ ] **步骤 6：Commit**

```bash
git add engine/types.go engine/agent.go
git commit -m "feat(engine): add stable RunID/Step/Status to agent progress events"
```

---

## 任务 2：异步派发的 `agent_start` 带 job id

**文件：**
- 修改：`engine/loop.go:185-210`（`bgTask` 加 `step`）
- 修改：`engine/handoff.go:264-375`（`dispatchAsync`）
- 测试：`engine/handoff_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/handoff_test.go`。用同文件的 `blockingAgent`（它带 `start`/`released`，能确认后台 goroutine 已启动）与 `NewAgentRegistry`、`e.initBackgroundTasks()`，用法照抄 `TestRunSubAgent_AsyncDispatch_RegistersAndRuns`：

```go
// TestAsyncDispatchAgentStartCarriesJobID: 异步派发的 agent_start 必须带上
// job id，UI 才能用它把后台完成事件配回同一行。
func TestAsyncDispatchAgentStartCarriesJobID(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	var eventsMu sync.Mutex
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			eventsMu.Lock()
			events = append(events, ev)
			eventsMu.Unlock()
		}},
	}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true, Step: "dir-1",
	}, 0, "中文"); err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	<-a.start

	eventsMu.Lock()
	evs := append([]ProgressEvent(nil), events...)
	eventsMu.Unlock()

	starts := 0
	for _, ev := range evs {
		if ev.Type != "agent_start" {
			continue
		}
		starts++
		if ev.RunID != "bg-1" {
			t.Errorf("agent_start RunID = %q, want bg-1", ev.RunID)
		}
		if ev.Step != "dir-1" {
			t.Errorf("agent_start Step = %q, want dir-1", ev.Step)
		}
	}
	if starts != 1 {
		t.Fatalf("agent_start count = %d, want 1", starts)
	}

	e.bgMu.Lock()
	task := e.bgTasks["bg-1"]
	e.bgMu.Unlock()
	if task == nil || task.step != "dir-1" {
		t.Errorf("bgTask bg-1 = %+v, want step dir-1", task)
	}

	e.cancelBackgroundTasks() // 取消后台 ctx，释放阻塞的 agent goroutine
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestAsyncDispatchAgentStartCarriesJobID -v`
预期：FAIL —— `agent_start RunID = ""`。

- [ ] **步骤 3：给 `bgTask` 加 `step`**

`engine/loop.go` 的 `bgTask` 结构体，在 `goal string` 之后插入：

```go
	// step is the TodoItem.ID this job executes ("" = not linked to a step).
	// Stored here so every terminal event can report the same link the
	// dispatch reported, without re-parsing the original tool call.
	step string
```

- [ ] **步骤 4：`dispatchAsync` 发事件时带上 `RunID` / `Step`**

`engine/handoff.go`，在构造 `task` 字面量时补 `step: params.Step`：

```go
	task := &bgTask{
		id:      jobID,
		agent:   AgentID(params.Agent),
		goal:    params.Goal,
		step:    params.Step,
		ctx:     bgCtx,
		cancel:  cancel,
		result:  make(chan *HandoffResult, 1),
		done:    make(chan struct{}),
		inbox:   inbox,
		startAt: time.Now(),
		state:   bgStateRunning,
	}
```

把该函数里原来那段 `agent_start` 发射替换为：

```go
	// agent_start event for UI. Emitted HERE, and not in turn.go, because only
	// this point knows the job id the UI must pair the run's outcome with —
	// turn.go cannot know it before the tool call runs, and emitting one there
	// as well would show every async delegation twice.
	if e.config.OnProgress != nil {
		name := params.Agent
		if name == "" {
			name = "sub"
		}
		e.config.OnProgress(ProgressEvent{
			Type: "agent_start", Name: name, Detail: params.Goal,
			RunID: jobID, Step: params.Step,
		})
	}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestAsyncDispatchAgentStartCarriesJobID -v`
预期：PASS。

- [ ] **步骤 6：跑引擎全量测试**

运行：`go test ./engine/...`
预期：通过。若 `TestRunSubAgent_AsyncDispatch_AgentDoneEvent` 失败，说明事件顺序假设被破坏，按实际输出修正测试断言（该测试断言的是 `agent_done` 携带结果摘要，本次不应影响它）。

- [ ] **步骤 7：Commit**

```bash
git add engine/loop.go engine/handoff.go engine/handoff_test.go
git commit -m "feat(engine): report the async job id on the dispatch agent_start"
```

---

## 任务 3：`turn.go` 不再重复发 start、不再把派发回执当 done

**文件：**
- 修改：`engine/turn.go:648-686`
- 测试：新建 `engine/turn_handoff_events_test.go`

- [ ] **步骤 1：编写失败的测试**

新建 `engine/turn_handoff_events_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// handoffResultExecutor stands in for the real tool layer: every handoff call
// comes back with the same preset result and emits nothing itself, so any
// agent_start/agent_done seen by the test was emitted by executeTurn.
type handoffResultExecutor struct{ result ToolResult }

func (x *handoffResultExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		r := x.result
		r.ToolCallID = c.ID
		out = append(out, r)
	}
	return out
}

func (x *handoffResultExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: HandoffToolName}}}
}

// TestExecuteTurn_AsyncDispatchEmitsNoTerminalEvents: 成功的异步派发不是结束。
// turn.go 既不能自己再发一次 agent_start（dispatchAsync 已经发过），也不能
// 立刻发 agent_done —— 否则后台还在跑的行会被标记完成（用户报告：5 个方向
// 在面板上变成 10 行，其中 5 行显示的是派发回执）。
func TestExecuteTurn_AsyncDispatchEmitsNoTerminalEvents(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "派发调研",
			ToolCalls: []ModelToolCall{{ID: "call_1", Type: "function", Function: ModelFunctionCall{
				Name:      HandoffToolName,
				Arguments: `{"agent":"researcher","goal":"调研","async":true,"step":"dir-1"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools: &handoffResultExecutor{result: ToolResult{
			ToolName:     HandoffToolName,
			Status:       "ok",
			Digest:       "Dispatched async job bg-1 (researcher): 调研. Use agent_poll(bg-1) to check the result.",
			FinishReason: HandoffReasonAsyncRunning,
		}},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "调研"}},
		config: EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "agent_start" || ev.Type == "agent_done" {
			t.Errorf("turn.go must stay silent for a successful async dispatch, got %s: %+v", ev.Type, ev)
		}
	}
}

// TestExecuteTurn_SyncHandoffEmitsPairedRunID: 同步委派仍由 turn.go 发一对
// 事件，两者带同一个 RunID（= handoff tool call id）。
func TestExecuteTurn_SyncHandoffEmitsPairedRunID(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "同步委派",
			ToolCalls: []ModelToolCall{{ID: "call_9", Type: "function", Function: ModelFunctionCall{
				Name:      HandoffToolName,
				Arguments: `{"agent":"researcher","goal":"调研","step":"dir-9"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools: &handoffResultExecutor{result: ToolResult{
			ToolName: HandoffToolName, Status: "ok", Digest: "研究小结：结论",
		}},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "调研"}},
		config: EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var start, done *ProgressEvent
	for i := range events {
		switch events[i].Type {
		case "agent_start":
			start = &events[i]
		case "agent_done":
			done = &events[i]
		}
	}
	if start == nil || done == nil {
		t.Fatalf("sync handoff must emit both start and done, got %+v", events)
	}
	if start.RunID != "call_9" || done.RunID != "call_9" {
		t.Errorf("RunID mismatch: start=%q done=%q, want both call_9", start.RunID, done.RunID)
	}
	if done.Status != "ok" {
		t.Errorf("agent_done Status = %q, want ok", done.Status)
	}
	if done.Step != "dir-9" || start.Step != "dir-9" {
		t.Errorf("Step mismatch: start=%q done=%q, want both dir-9", start.Step, done.Step)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestExecuteTurn_(AsyncDispatch|SyncHandoff)' -v`
预期：两个都 FAIL —— 异步那条会因为 turn.go 发了事件而报错；同步那条因为 `RunID` 为空而报错。

- [ ] **步骤 3：改造执行前循环**

`engine/turn.go` 中 `if len(handoffCalls) > 0 {` 之后的那段 `agent_start` 循环替换为：

```go
		// agent_start events for UI. Only the synchronous path reports here:
		// an async dispatch reports its own start inside dispatchAsync, once
		// the job id it must carry has been allocated. Emitting one here too
		// would list every async delegation twice.
		for _, call := range handoffCalls {
			if e.config.OnProgress == nil {
				continue
			}
			var params HandoffToAgentParams
			if err := json.Unmarshal(call.Input, &params); err != nil || params.Async {
				continue
			}
			name := params.Agent
			if name == "" {
				name = "sub"
			}
			e.config.OnProgress(ProgressEvent{
				Type: "agent_start", Name: name, Detail: params.Goal,
				RunID: call.ID, Step: params.Step,
			})
		}
```

- [ ] **步骤 4：改造执行后循环**

同一函数中紧随 `results := e.tools.Execute(execCtx, handoffCalls)` 之后的循环替换为：

```go
		// agent_done events for UI. Execute preserves order, so results[i]
		// corresponds to handoffCalls[i]; parse the real agent name and step
		// from the call input (r.ToolName is always "handoff_to_agent").
		//
		// A successful async dispatch is NOT a finished run: the job keeps
		// running in the background and finishBackgroundJob emits the real
		// agent_done (carrying the job's own RunID) when it ends. Emitting one
		// here would close the row while the agent is still working — the
		// dispatch receipt is not a result.
		for i, r := range results {
			if e.config.OnProgress == nil {
				continue
			}
			if r.FinishReason == HandoffReasonAsyncRunning {
				continue
			}
			var params HandoffToAgentParams
			if i < len(handoffCalls) {
				_ = json.Unmarshal(handoffCalls[i].Input, &params)
			}
			name := params.Agent
			if name == "" {
				name = "sub"
			}
			runID := ""
			if i < len(handoffCalls) {
				runID = handoffCalls[i].ID
			}
			e.config.OnProgress(ProgressEvent{
				Type: "agent_done", Name: name, Detail: briefDigest(r.Digest),
				RunID: runID, Step: params.Step, Status: handoffEventStatus(r),
			})
		}
```

- [ ] **步骤 5：新增状态映射辅助函数**

追加到 `engine/turn.go` 末尾（或紧邻 `briefDigest`）：

```go
// handoffEventStatus maps a handoff tool result to the status the UI shows for
// that row. runHandoff already normalized ToolResult.Status to
// "ok" / "cancelled" / "error"; anything unexpected reads as a success so a
// new status value can never silently render a finished run as failed.
func handoffEventStatus(r ToolResult) string {
	switch r.Status {
	case "error", "cancelled":
		return r.Status
	default:
		return "ok"
	}
}
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestExecuteTurn_(AsyncDispatch|SyncHandoff)' -v`
预期：PASS。

- [ ] **步骤 7：跑引擎全量测试**

运行：`go test ./engine/...`
预期：通过。若 `engine/handoff_test.go:227` 的 `TestRunSubAgent_AsyncDispatch_AgentDoneEvent` 仍通过（它直接调 `RunSubAgent`，不经过 turn.go），属预期。

- [ ] **步骤 8：Commit**

```bash
git add engine/turn.go engine/turn_handoff_events_test.go
git commit -m "fix(engine): stop emitting a duplicate start and a premature done for async handoffs"
```

---

## 任务 4：所有终止路径的事件都带同一 `RunID`

**文件：**
- 修改：`engine/loop.go:287-323`（`finishBackgroundJob`）
- 修改：`engine/agent_manage.go:580-640`（`closeAgentJob`）
- 修改：`engine/suspend.go:187-206`（`finishResumedJob`）
- 测试：`engine/bg_runid_events_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

新建 `engine/bg_runid_events_test.go`：

```go
package engine

import (
	"context"
	"testing"
	"time"
)

// TestBackgroundDoneCarriesJobID: 后台完成的 agent_done 必须带自己的 job id，
// 否则 UI 只能按角色名配到"第一个还在跑的同类"，5 个并发 researcher 会互相
// 顶替。
func TestBackgroundDoneCarriesJobID(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "后台调研完成", FinishReason: HandoffReasonCompleted,
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
	}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true, Step: "dir-1",
	}, 0, "中文"); err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range events {
			if ev.Type == "agent_done" && ev.RunID == "bg-1" {
				if ev.Step != "dir-1" {
					t.Errorf("agent_done Step = %q, want dir-1", ev.Step)
				}
				if ev.Status != "ok" {
					t.Errorf("agent_done Status = %q, want ok", ev.Status)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no agent_done with RunID bg-1, got %+v", events)
}

// TestCloseAgentEmitsTerminalEventWithRunID: 关闭任务必须用被关闭任务的 job id
// 收尾，否则 UI 里那一行永远停在 running。
func TestCloseAgentEmitsTerminalEventWithRunID(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	e := &Engine{
		agents:    reg,
		isChinese: true,
		state:     &TaskState{},
		config: EngineConfig{OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
	}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true, Step: "dir-2",
	}, 0, "中文"); err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	<-a.start

	if msg := e.closeAgentJob("bg-1"); msg == "" {
		t.Fatal("closeAgentJob returned an empty message")
	}

	found := false
	for _, ev := range events {
		if ev.Type == "agent_done" && ev.RunID == "bg-1" {
			found = true
			if ev.Status != "cancelled" {
				t.Errorf("agent_done Status = %q, want cancelled", ev.Status)
			}
			if ev.Step != "dir-2" {
				t.Errorf("agent_done Step = %q, want dir-2", ev.Step)
			}
		}
	}
	if !found {
		t.Fatalf("no terminal agent_done for bg-1, got %+v", events)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'Test(BackgroundDoneCarriesJobID|CloseAgentEmitsTerminalEventWithRunID)' -v`
预期：两个都 FAIL —— `RunID` 为空（关闭那条还会因 `Step` 为空失败）。

- [ ] **步骤 3：改造 `finishBackgroundJob`**

`engine/loop.go` 尾部的事件发射替换为：

```go
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{
			Type: "agent_done", Name: string(t.agent), Detail: result.Summary,
			RunID: t.id, Step: t.step, Status: handoffResultStatus(result),
		})
	}
```

并在同文件追加辅助函数：

```go
// handoffResultStatus maps a finished background result to the status the UI
// shows. Cancellation wins over failure, and a blocked run is a failure — a
// finished run must never render as a success when it did not deliver.
func handoffResultStatus(r *HandoffResult) string {
	switch {
	case r == nil:
		return "error"
	case r.BlockedBy == "cancelled":
		return "cancelled"
	case r.Blocked || r.FinishReason == HandoffReasonError:
		return "error"
	default:
		return "ok"
	}
}
```

- [ ] **步骤 4：改造 `closeAgentJob`**

`engine/agent_manage.go` 中把收集用的切片从"名字"改为"任务指针"：

```go
	terminal := make([]*bgTask, 0, len(ids)) // removed jobs needing a terminal event
```

两处 append 改为：

```go
		case t.state == bgStateAwaitingUser:
			suspended++
			terminal = append(terminal, t)
		default:
			terminal = append(terminal, t)
		}
```

发射部分替换为：

```go
	if e.config.OnProgress != nil {
		for _, t := range terminal {
			e.config.OnProgress(ProgressEvent{
				Type: "agent_done", Name: string(t.agent), Detail: "closed",
				RunID: t.id, Step: t.step, Status: "cancelled",
			})
		}
	}
```

`id`/`agent`/`step` 在任务构造后不再变更，任务已从表中删除也可以安全读取。

- [ ] **步骤 5：给再挂起事件补字段**

`engine/suspend.go` 的 `finishResumedJob` 中：

```go
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type: "agent_done", Name: string(task.agent), Detail: result.Summary,
				RunID: task.id, Step: task.step,
			})
		}
```

`Status` 故意留空：这一条表示"又挂起了"，不是终态成功；UI 把空状态按完成渲染（与现状一致）。挂起状态可视化是本次非目标。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'Test(BackgroundDoneCarriesJobID|CloseAgentEmitsTerminalEventWithRunID)' -v`
预期：PASS。

- [ ] **步骤 7：跑引擎全量测试**

运行：`go test ./engine/...`
预期：通过。

- [ ] **步骤 8：Commit**

```bash
git add engine/loop.go engine/agent_manage.go engine/suspend.go engine/bg_runid_events_test.go
git commit -m "fix(engine): carry the job id on every terminal sub-agent event"
```

---

## 任务 5：`todo_write` 的 `id` 唯一性校验

**文件：**
- 修改：`engine/turn.go:1502-1557`（`processTodoWriteCalls`）
- 测试：`engine/turn_todo_write_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/turn_todo_write_test.go`：

```go
// TestProcessTodoWriteCalls_DuplicateIDRejected: id 是分组键，重复会让两个步骤
// 争夺同一批子代理，必须在入口 fail loud。
func TestProcessTodoWriteCalls_DuplicateIDRejected(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) {
		events = append(events, ev)
	}}}

	msgs := e.processTodoWriteCalls([]ToolCallRequest{{
		ID: "call_dup", Name: TodoWriteToolName,
		Input: json.RawMessage(`{"todos":[
			{"id":"dir-1","content":"方向1","status":"in_progress"},
			{"id":"dir-1","content":"方向2","status":"pending"}
		]}`),
	}})

	if len(msgs) != 1 {
		t.Fatalf("msgs len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "duplicate") {
		t.Errorf("message = %q, want a duplicate-id error", msgs[0].Content)
	}
	for _, ev := range events {
		if ev.Type == "todo_update" {
			t.Error("a rejected snapshot must not reach the UI")
		}
	}
}

// TestProcessTodoWriteCalls_UniqueIDsAccepted: 正常路径必须原样透传 id。
func TestProcessTodoWriteCalls_UniqueIDsAccepted(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) {
		events = append(events, ev)
	}}}

	e.processTodoWriteCalls([]ToolCallRequest{{
		ID: "call_ok", Name: TodoWriteToolName,
		Input: json.RawMessage(`{"todos":[
			{"id":"dir-1","content":"方向1","status":"in_progress"},
			{"content":"汇总","status":"pending"}
		]}`),
	}})

	if len(events) != 1 || events[0].Type != "todo_update" {
		t.Fatalf("events = %+v, want one todo_update", events)
	}
	if got := events[0].Todos[0].ID; got != "dir-1" {
		t.Errorf("Todos[0].ID = %q, want dir-1", got)
	}
}
```

（`engine/turn_todo_write_test.go` 已导入 `encoding/json` 与 `strings`，无需改 import。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestProcessTodoWriteCalls_(DuplicateIDRejected|UniqueIDsAccepted)' -v`
预期：`DuplicateIDRejected` FAIL（当前无校验，快照会被放行）。

- [ ] **步骤 3：实现校验**

`engine/turn.go` 的 `processTodoWriteCalls` 中，在 `valid := true` 之后初始化集合：

```go
		valid := true
		seenIDs := make(map[string]bool, len(params.Todos))
```

在既有 `for _, t := range params.Todos {` 循环体内、`if t.Status != ...` 检查之后追加：

```go
			if id := strings.TrimSpace(t.ID); id != "" {
				if seenIDs[id] {
					msgs = append(msgs, Message{
						Role:       "tool",
						ToolCallID: call.ID,
						Content:    fmt.Sprintf("Error: duplicate todo id %q — ids must be unique when present", id),
						Timestamp:  time.Now(),
					})
					valid = false
					break
				}
				seenIDs[id] = true
			}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestProcessTodoWriteCalls' -v`
预期：PASS（含该文件既有用例）。

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/turn_todo_write_test.go
git commit -m "feat(engine): reject duplicate todo ids so step grouping stays unambiguous"
```

---

## 任务 6：UI 按 `RunID` 配对

**文件：**
- 修改：`ui/model.go:73-80`（`SubAgentStatus`）、`ui/model.go:185-196`（`ProgressMsg`）、`ui/model.go:503-543`（事件分支）
- 修改：`ui/runner.go:192`
- 测试：`ui/model_test.go`、`ui/tab_overflow_test.go:107-110`

- [ ] **步骤 1：扩展 `ProgressMsg` 与转发**

`ui/model.go`：

```go
type ProgressMsg struct {
	Type       string
	Name       string
	Detail     string
	FullDetail string
	TokensIn   int
	TokensOut  int
	CacheHit   int
	CacheMiss  int
	ModelName  string
	Todos      []engine.TodoItem
	// RunID/Step/Status mirror engine.ProgressEvent: RunID pairs a start with
	// its done, Step links the run to a TodoItem.ID, Status carries the
	// outcome of a done.
	RunID  string
	Step   string
	Status string
}
```

`ui/runner.go:192` 替换为：

```go
				msg := ProgressMsg{
					Type: event.Type, Name: event.Name, Detail: event.Detail,
					FullDetail: event.FullDetail, Todos: event.Todos,
					RunID: event.RunID, Step: event.Step, Status: event.Status,
				}
```

- [ ] **步骤 2：重构 `SubAgentStatus`**

`ui/model.go`：

```go
// SubAgentStatus tracks a dispatched sub-agent's progress for UI display.
type SubAgentStatus struct {
	// RunID is the stable handle pairing a start with its done: the handoff
	// tool call id for a synchronous run, "bg-<seq>" for an async one. Never
	// pair by Agent name — concurrent agents of the same role would close each
	// other's rows.
	RunID   string
	Agent   string // agent type e.g. "sub", "critic", "searcher"
	Goal    string // what the sub-agent is dispatched to do
	Step    string // TodoItem.ID this run executes; "" = not linked to a step
	Status  string // "running", "done", "error", "cancelled"
	Summary string // result summary when done
}
```

- [ ] **步骤 3：编写失败的测试**

追加到 `ui/model_test.go`：

```go
// TestAgentDonePairsByRunID: 5 个同名 researcher 并发时，完成事件必须落在
// 自己那一行。按角色名配到"第一个还在跑的同类"会让结果互相顶替。
func TestAgentDonePairsByRunID(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	ids := []string{"bg-1", "bg-2", "bg-3", "bg-4", "bg-5"}
	for _, id := range ids {
		res, _ := m.Update(ProgressMsg{Type: "agent_start", Name: "researcher", Detail: "调研 " + id, RunID: id, Step: "dir"})
		m = res.(Model)
	}
	res, _ := m.Update(ProgressMsg{Type: "agent_done", Name: "researcher", Detail: "bg-4 完成", RunID: "bg-4", Status: "ok"})
	m = res.(Model)

	if len(m.subAgents) != 5 {
		t.Fatalf("subAgents len = %d, want 5", len(m.subAgents))
	}
	for _, a := range m.subAgents {
		want := "running"
		if a.RunID == "bg-4" {
			want = "done"
		}
		if a.Status != want {
			t.Errorf("%s status = %q, want %q", a.RunID, a.Status, want)
		}
	}
	if got := m.subAgents[3].Summary; !strings.Contains(got, "bg-4 完成") {
		t.Errorf("bg-4 summary = %q, want the bg-4 result", got)
	}
}

// TestAgentDoneUnknownRunIDAppendsFinishedRow: 派发在子代理启动前就失败的
// 异步任务不发 agent_start，只发一条 done。UI 必须把它显示成一行已结束的
// 记录，不能静默丢掉，也不能让它顶替别的行。
func TestAgentDoneUnknownRunIDAppendsFinishedRow(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	res, _ := m.Update(ProgressMsg{Type: "agent_start", Name: "researcher", Detail: "正常调研", RunID: "bg-1"})
	m = res.(Model)
	res, _ = m.Update(ProgressMsg{Type: "agent_done", Name: "researcher", Detail: "background sub-agent limit reached", RunID: "bg-9", Status: "error"})
	m = res.(Model)

	if len(m.subAgents) != 2 {
		t.Fatalf("subAgents len = %d, want 2", len(m.subAgents))
	}
	if m.subAgents[0].Status != "running" {
		t.Errorf("bg-1 status = %q, want running (must not be consumed by bg-9)", m.subAgents[0].Status)
	}
	if m.subAgents[1].RunID != "bg-9" || m.subAgents[1].Status != "error" {
		t.Errorf("bg-9 row = %+v, want error", m.subAgents[1])
	}
}
```

- [ ] **步骤 4：运行测试验证失败**

运行：`go test ./ui/ -run 'TestAgentDone(PairsByRunID|UnknownRunIDAppendsFinishedRow)' -v`
预期：FAIL —— `RunID` 未参与配对，5 行里的第一行会被标记完成。

- [ ] **步骤 5：实现 `RunID` 配对**

`ui/model.go` 的 `agent_start` 分支中，追加子代理的部分替换为：

```go
			// Track sub-agent in the dedicated panel. RunID is the pairing key
			// (handoff tool call id for sync runs, "bg-N" for async ones).
			m.subAgents = append(m.subAgents, SubAgentStatus{
				RunID:  msg.RunID,
				Agent:  displayName,
				Goal:   msg.Detail,
				Step:   msg.Step,
				Status: "running",
			})
```

`agent_done` 分支中，原来那段"按名字找第一个 running"的循环替换为：

```go
			// Pair by RunID: concurrent sub-agents of the SAME role are
			// indistinguishable by name, so a name match could close the wrong
			// row. A done whose RunID was never started (an async dispatch that
			// failed before the agent ran) still gets a row, already finished.
			m.subAgents = applyAgentDone(m.subAgents, msg)
```

并在 `renderSubAgentPanel` 邻近位置新增：

```go
// applyAgentDone folds an agent_done event into the sub-agent panel, pairing by
// RunID. Falls back to the first running row of the same role only when the
// event carries no handle at all (or handoff calls without an id), which keeps
// the panel usable if a producer ever emits an unpaired event.
func applyAgentDone(agents []SubAgentStatus, msg ProgressMsg) []SubAgentStatus {
	status := "done"
	switch msg.Status {
	case "error", "cancelled":
		status = msg.Status
	}
	if i := subAgentIndexByRunID(agents, msg.RunID); i >= 0 {
		agents[i].Status = status
		agents[i].Summary = msg.Detail
		return agents
	}
	if msg.RunID == "" {
		for i := range agents {
			if agents[i].Agent == msg.Name && agents[i].Status == "running" {
				agents[i].Status = status
				agents[i].Summary = msg.Detail
				return agents
			}
		}
		return agents
	}
	return append(agents, SubAgentStatus{
		RunID: msg.RunID, Agent: msg.Name, Step: msg.Step,
		Status: status, Summary: msg.Detail,
	})
}

// subAgentIndexByRunID returns the row carrying runID, or -1. An empty runID
// never matches: it is the "no handle" marker, and matching it would let an
// unpaired event close an unpaired row.
func subAgentIndexByRunID(agents []SubAgentStatus, runID string) int {
	if runID == "" {
		return -1
	}
	for i := range agents {
		if agents[i].RunID == runID {
			return i
		}
	}
	return -1
}
```

- [ ] **步骤 6：修正既有测试里的 `ID` 字面量**

`ui/tab_overflow_test.go:107-110` 的 `SubAgentStatus` 字面量把 `ID: "1"` / `ID: "2"` 改为 `RunID: "bg-1"` / `RunID: "bg-2"`。

`ui/model_test.go:525` 的 `TestAgentDoneUpdatesSubAgentPanel` 给两个事件补上同一个 `RunID: "bg-1"`，让它验证的是新契约而不是回退路径。

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./ui/...`
预期：PASS。

- [ ] **步骤 8：Commit**

```bash
git add ui/model.go ui/runner.go ui/model_test.go ui/tab_overflow_test.go
git commit -m "fix(ui): pair sub-agent events by run id instead of role name"
```

---

## 任务 7：单面板步骤树渲染

**文件：**
- 修改：`ui/model.go:1905-1940`（视图组装）、`ui/model.go:2699-2819`（渲染器）
- 测试：`ui/todo_render_test.go`、`ui/model_test.go:495`

- [ ] **步骤 1：编写失败的测试**

把 `ui/todo_render_test.go` 中的 `TestRenderTodoList_ThreeStates` 改名为 `TestRenderStepsPanel_ThreeStates`，第一行改为 `lines := renderStepsPanel(items, nil, 80)`；`TestRenderTodoList_Empty` 改名 `TestRenderStepsPanel_Empty`，断言改为：

```go
	if got := renderStepsPanel(nil, nil, 80); got != nil {
		t.Errorf("expected nil for an empty panel, got %v", got)
	}
```

再追加：

```go
// TestRenderStepsPanel_NestsByStepID: 子代理按 SubAgentStatus.Step 缩进到对应
// 步骤下；step 为空或已失配的进游离组，标题复用 Sub-Agents。
func TestRenderStepsPanel_NestsByStepID(t *testing.T) {
	items := []engine.TodoItem{
		{ID: "plan", Content: "规划阶段", Status: "completed"},
		{ID: "dir-1", Content: "方向1 调研", Status: "in_progress"},
		{Content: "汇总交叉分析", Status: "pending"},
	}
	agents := []SubAgentStatus{
		{RunID: "bg-1", Agent: "researcher", Goal: "调研方向1", Step: "dir-1", Status: "running"},
		{RunID: "bg-2", Agent: "critic", Goal: "审查", Status: "running"},
		{RunID: "bg-3", Agent: "researcher", Goal: "失配", Step: "gone", Status: "done", Summary: "小结"},
	}
	lines := renderStepsPanel(items, agents, 80)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(stripAnsi(l), " ")
	}
	joined := strings.Join(plain, "\n")

	idx := func(sub string) int {
		for i, l := range plain {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("line %q not found in:\n%s", sub, joined)
		return -1
	}
	// bg-1 挂在方向1 之下：顺序必须是 方向1 → bg-1 → 汇总。
	if !(idx("方向1 调研") < idx("bg-1") && idx("bg-1") < idx("汇总交叉分析")) {
		t.Errorf("bg-1 must nest between its step and the next step:\n%s", joined)
	}
	// 游离组在步骤列表之后，且保留独立标题。
	if !(idx("汇总交叉分析") < idx("Sub-Agents") && idx("Sub-Agents") < idx("bg-2") && idx("bg-2") < idx("bg-3")) {
		t.Errorf("unlinked agents must follow the steps under the Sub-Agents heading:\n%s", joined)
	}
	if !strings.Contains(joined, "[::] Steps") {
		t.Errorf("missing Steps heading:\n%s", joined)
	}
}

// TestRenderStepsPanel_OrphanOnly: 没有步骤列表时退化为原有的子代理面板。
func TestRenderStepsPanel_OrphanOnly(t *testing.T) {
	lines := renderStepsPanel(nil, []SubAgentStatus{
		{RunID: "bg-1", Agent: "sub", Goal: "批判性审查", Status: "running"},
	}, 80)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(stripAnsi(l), " ")
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "[::] Steps") {
		t.Errorf("no step list must render no Steps heading:\n%s", joined)
	}
	if !strings.Contains(joined, "Sub-Agents") || !strings.Contains(joined, "[s]") {
		t.Errorf("orphan agents must keep the Sub-Agents heading:\n%s", joined)
	}
}

// TestRenderStepsPanel_ErrorStatus: error/cancelled 用 ✗，不再是 ✓。
func TestRenderStepsPanel_ErrorStatus(t *testing.T) {
	lines := renderStepsPanel(nil, []SubAgentStatus{
		{RunID: "bg-9", Agent: "researcher", Status: "error", Summary: "limit reached"},
	}, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "✗") {
		t.Errorf("failed run must render ✗:\n%s", joined)
	}
	if strings.Contains(joined, "✓") {
		t.Errorf("failed run must not render ✓:\n%s", joined)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestRenderStepsPanel -v`
预期：FAIL —— `undefined: renderStepsPanel`。

- [ ] **步骤 3：实现 `renderStepsPanel` 与行渲染器**

在 `ui/model.go` 中删除 `renderSubAgentPanel`、`renderTodoList`、`renderOverlayStatus`，替换为：

```go
// renderStepsPanel renders the step backbone with every sub-agent nested under
// the step it executes (SubAgentStatus.Step == TodoItem.ID). Sub-agents whose
// Step is empty or no longer matches any step keep their own Sub-Agents block,
// so a delegation that is not part of a step list — or one whose step the model
// rewrote away — is still visible instead of silently dropped.
func renderStepsPanel(items []engine.TodoItem, agents []SubAgentStatus, width int) []string {
	var children [][]SubAgentStatus
	var orphans []SubAgentStatus
	if len(items) > 0 {
		index := make(map[string]int, len(items))
		for i, it := range items {
			if it.ID != "" {
				index[it.ID] = i
			}
		}
		children = make([][]SubAgentStatus, len(items))
		for _, a := range agents {
			if i, ok := index[a.Step]; ok && a.Step != "" {
				children[i] = append(children[i], a)
				continue
			}
			orphans = append(orphans, a)
		}
	} else {
		orphans = agents
	}
	if len(items) == 0 && len(orphans) == 0 {
		return nil
	}

	var content []string
	if len(items) > 0 {
		content = append(content, DimStyle.Render("▍")+" [::] "+DimStyle.Render("Steps"))
		content = append(content, "")
		for i, it := range items {
			content = append(content, todoLine(it))
			for _, a := range children[i] {
				content = append(content, "      "+subAgentLine(a))
			}
		}
	}
	if len(orphans) > 0 {
		if len(content) > 0 {
			content = append(content, "")
		}
		content = append(content, DimStyle.Render("▍")+" [→] "+DimStyle.Render("Sub-Agents"))
		content = append(content, "")
		for _, a := range orphans {
			content = append(content, "  "+subAgentLine(a))
		}
	}
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	return splitLipglossBlock(rendered, width)
}

// todoLine renders one step row. Plain-text markers: [ ] pending, [~] in_progress,
// [✓] completed. [✓] instead of [x]: [x] reads as "failed/cancelled" to users.
func todoLine(it engine.TodoItem) string {
	switch it.Status {
	case "in_progress":
		return fmt.Sprintf("  [~]  %s", SpinnerStyle.Render(it.Content))
	case "completed":
		return fmt.Sprintf("  [✓]  %s", SpinnerDoneStyle.Render(it.Content))
	default:
		return fmt.Sprintf("  [ ]  %s", DimStyle.Render(it.Content))
	}
}

// subAgentLine renders one sub-agent row: status marker, stable run handle,
// role tag, then the goal while running or the result once finished. The handle
// is what makes concurrent same-role agents tellable apart.
func subAgentLine(a SubAgentStatus) string {
	trunc := func(s string) string {
		if len(s) > 60 {
			return s[:60] + "..."
		}
		return s
	}
	switch a.Status {
	case "running":
		return fmt.Sprintf("%s %s %s  %s", spinnerFrames[0], a.RunID, agentIcon(a.Agent), SpinnerStyle.Render(trunc(a.Goal)))
	case "error", "cancelled":
		return ErrorStyle.Render(fmt.Sprintf("✗ %s %s  %s", a.RunID, agentIcon(a.Agent), trunc(a.Summary)))
	default:
		return fmt.Sprintf("✓ %s %s  %s", a.RunID, agentIcon(a.Agent), SpinnerDoneStyle.Render(trunc(a.Summary)))
	}
}
```

- [ ] **步骤 4：迁移面板回归测试**

`ui/model_test.go:495` 的 `TestRenderSubAgentPanel_NoPhantomBlankLine` 把调用改为 `renderStepsPanel(nil, []SubAgentStatus{...}, 80)`，其余断言（恰一个空行、`[s]` 存在）保持不变——它守护的 lipgloss 宽度幻影空行回归在新渲染器上同样适用。

- [ ] **步骤 5：改视图组装**

`ui/model.go:1905-1940` 中从 `if len(m.toolTree) > 0` 之后到 `if len(m.spinners) > 0` 之前，替换为：

```go
	if len(m.todoItems) > 0 {
		// Step backbone: every sub-agent nests under the step it executes.
		// While a step list is active this panel stands in for the live
		// narration (unchanged behaviour).
		lines = append(lines, renderStepsPanel(m.todoItems, m.subAgents, width)...)
	} else {
		if len(m.subAgents) > 0 {
			// No step list: sub-agents keep their own block, and the live
			// narration/streaming still renders below it.
			lines = append(lines, renderStepsPanel(nil, m.subAgents, width)...)
		}
		if m.narration != "" {
			narrationLines := renderStreaming(m.narration, width)
			lines = append(lines, narrationLines...)
			// Message blocks (renderBody above) each end with a trailing blank line.
			// The streamed narration is not yet a message, so append the same
			// separator here. Without it, finalizing the narration into a message
			// adds one line to totalLines, shifting scrollOffset / screenToLine
			// mapping by one row — clicking a screen row would select the next
			// line's text.
			lines = append(lines, "")
		} else if m.streaming != "" {
			streamLines := renderStreaming(m.streaming, width)
			lines = append(lines, streamLines...)
			// Same trailing blank line as the narration branch above: finalizing
			// streamed text into an assistant message adds a blank separator line,
			// so the live block must match to keep scroll/click mapping stable.
			lines = append(lines, "")
		}
	}
```

- [ ] **步骤 6：编译并跑 UI 全量测试**

运行：`go build ./... && go test ./ui/...`
预期：通过。若 `ui/width_alignment_test.go`、`ui/tab_overflow_test.go` 报行宽越界，检查 `subAgentLine` 的截断是否让单行超过 `width`——`splitLipglossBlock` 会按 `ansi.StringWidth` 兜底换行，越界说明该行在 lipgloss 渲染前就已超宽，把截断上限收紧到能容纳 `RunID + 角色标签` 的宽度。

- [ ] **步骤 7：Commit**

```bash
git add ui/model.go ui/model_test.go ui/todo_render_test.go
git commit -m "feat(ui): render one step tree with sub-agents nested under their step"
```

---

## 任务 8：让模型知道这条通道（collab 提示）

**文件：**
- 修改：`skill/builtin/collab/SKILL.md:12-23`

- [ ] **步骤 1：改编排说明**

把第 1、2 步改成：

```markdown
1. **拆解**（不委派）：在全局框架基础上，把研究目标拆成 2-6 个互不重叠的研究方向。每个方向自包含——独立子代理无共享上下文，仅凭方向描述 + context 就能开工。用 `todo_write` 记录方向时，给每个方向一个短稳定 `id`（如 `dir-1`），UI 靠它把子代理嵌到对应方向下。
2. **并行委派**：对每个方向发起一次 `handoff_to_agent`（**agent="researcher"**，引擎内置只读调研角色，自带研究员 persona 与只读工具白名单），**一轮内并行发出多个 handoff**（引擎天然并行）。每个 handoff 参数：
   - `goal`：**只放研究方向描述**（角色 persona 已由 researcher 内置，无需重复）
   - `step`：填该方向在 `todo_write` 里的 `id`，UI 会把这次委派的子代理缩进到该方向下；不对应任何方向的委派则省略
```

（后续 a/b/c 三条 context 要求原样保留。）

- [ ] **步骤 2：跑 skill 包测试**

运行：`go test ./skill/...`
预期：通过（若 `skill/builtin_test.go` 断言了 SKILL.md 的内容或长度，按实际输出调整断言）。

- [ ] **步骤 3：Commit**

```bash
git add skill/builtin/collab/SKILL.md
git commit -m "docs(skill): tell collab to tag directions with ids and pass step on handoff"
```

---

## 任务 9：端到端验收

- [ ] **步骤 1：编译与全量测试**

运行：

```bash
go build ./...
go test ./engine/... ./ui/... ./skill/...
```

预期：全部通过。

- [ ] **步骤 2：手工验收（对照规格）**

在真实会话里跑一次 `/collab` 或手工派发 3 个 `async` 的 `researcher`，逐个确认：

- 面板只有**一个** `▍ [::] Steps`，子代理缩进在对应步骤下，不再出现 `10 行对 5 个方向`。
- 每个子代理行带 `bg-N`；3 个同名 researcher 各自完成时，结果落在**自己**那行。
- 派发时不再出现"派发回执"被当成完成行。
- 没有 `step` 的委派出现在 `▍ [→] Sub-Agents` 游离组。
- 子代理失败时该行显示 `✗` 而不是 `✓`。
- 窄终端（`width=80`）不出现越界换行或幻影空行。

- [ ] **步骤 3：更新 TODO/状态**

若 `docs/archive/TODO.md` 记录了本次相关条目，勾选或补记；否则跳过。

---

## 自检记录

**规格覆盖度**

| 规格章节 | 对应任务 |
|---|---|
| 一、数据链路：字段改动 | 任务 1 |
| 一、数据链路：事件表（同步/异步/失败/关闭/再挂起） | 任务 2、3、4 |
| 二、步骤关联通道（`TodoItem.ID` + `handoff.step` + 唯一性校验 + 提示词） | 任务 1、5、8 |
| 三、渲染规则（含退化为游离段、删 `ID` 字段、按 `RunID` 配对） | 任务 6、7 |
| 边界情况（同名并发、未知 RunID、失配 step、派发失败） | 任务 2、3、4、6、7 |
| 测试章节 | 每个任务的测试步骤 |
| 非目标（不做分栏/折叠/suspended 可视化/文本匹配） | 计划中无对应任务，符合预期 |

**类型一致性**

- `ProgressEvent.RunID/Step/Status` ↔ `ProgressMsg.RunID/Step/Status` ↔ `SubAgentStatus.RunID/Step/Status` 三处同名同义（任务 1、6）。
- `bgTask.step`（任务 2 定义，任务 4 消费）。
- 辅助函数命名：`handoffEventStatus`（turn.go，takes `ToolResult`）与 `handoffResultStatus`（loop.go，takes `*HandoffResult`）刻意分开，避免同名同包冲突。
- `subAgentIndexByRunID` 只被 `applyAgentDone` 调用。
- 被删除的函数：`renderSubAgentPanel`、`renderTodoList`、`renderOverlayStatus`；调用点全部在任务 7 步骤 5 收敛为 `renderStepsPanel`，测试在步骤 1、4 迁移，无遗留引用。
