# 子代理 Run 内管理面 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。每完成一个任务后 commit。

**目标：** 补齐 deepact 子代理的 5 项 P0 结构性能力缺口——`list_agents` / `wait_agent` / `send_message` / `interrupt_agent` / `close_agent` / `fork` 父历史——让主代理能观测、等待、干预和延续它派出的子代理。

**架构：** 全部能力实现为**引擎内建工具**（与现有 `agent_poll` / `agent_resume` 同层），在 `executeTurn` 的工具分发点被拦截，不改动 tools 层与子代理的只读宇宙。基础设施是对 `bgTask` 增加三个字段（`done` 完成信号、`inbox` 运行中收件箱、`resultBrief` 结果摘要）并把结果投递收敛到唯一的 `finishBackgroundJob`。**范围限定为同一 Run 内**：running 任务在 Run 退出时仍被取消（`engine/loop.go:351`），跨 Run 常驻代理不在本计划内。

**技术栈：** Go 1.x、`go test ./...`、现有 engine 测试桩（`engine/test_stubs_test.go`、`mockAgentForHandoff`、`blockingAgent`）

---

## 范围与不做的事（YAGNI）

- **不改**工具执行器（`tools/`）与子代理只读宇宙：5 个新工具是主代理专属，子代理的工具面（`engine/sub_agent.go:1093-1137`）不变。
- **不改**生命周期模型：Run 结束仍取消 running 任务；挂起（`awaiting_user`）仍只在 Run 内登记、TTL 30 分钟。跨 Run 常驻代理、`bgTask` 持久化是**后续独立计划**。
- **不新增配置项**：`wait_agent` 的超时由工具参数控制，边界用命名常量；不引入 `[context]` 开关。
- **不新增 UI 命令**：新工具通过既有 `tool_start` / `tool_done` ProgressEvent 自然可见（`engine/turn.go:790`、`summarizeArgs`）。UI 专项渲染留待需要时再加。
- **命名说明**：本计划采用方案 A 里约定的工具名 `list_agents` / `wait_agent` / `send_message` / `interrupt_agent` / `close_agent`。它们与现有 `agent_poll` / `agent_resume` 的前缀风格不一致；若要统一为 `agent_*` 前缀，只需改 `engine/agent.go` 的常量字符串与两处 spec 描述，后续任务不受影响。

## 关键既有事实（实现前必须知道的约束）

| 事实 | 证据 |
|---|---|
| running 任务不能跨 Run 存活 | `engine/loop.go:351` 调 `cancelBackgroundTasks()`；删除逻辑 `engine/loop.go:216-228` |
| 结果投递目前有两处，且无完成信号 | 异步派发 goroutine `engine/handoff.go:349-359`；resume 级联终点 `engine/suspend.go:213-222` |
| 作业表纯内存，`bg-N` 为唯一身份 | `engine/loop.go:112-114`；id 生成 `engine/handoff.go:210-211,293-294` |
| 每条 tool_call 必须恰好一条 tool 响应 | 现有 `processAgentPollCalls` `engine/turn.go:1490-1500` 的"每个分支都 append"模式 |
| 主代理工具 spec 装配点 | `engine/turn.go:836-846` `toolSpecsWithHandoff` |
| 主代理工具分发放行/排除点 | `engine/turn.go:594-612` |
| 子代理迭代顶部的注入点（先例） | token 预算 nudge `engine/sub_agent.go:493-502` |
| 子代理只读工具白名单/闸门 | `engine/sub_agent.go:27`、`:766-786` |
| 新增内建工具的完整改动面 | `engine/agent.go:13-25`、`engine/turn.go:836-846`、`engine/turn.go:560-585`、`engine/turn.go:594-612`、`engine/agent.go:456-511`（spec 样板） |

---

## 文件结构

| 文件 | 职责 |
|---|---|
| 创建 `engine/agent_manage.go` | 5 个新工具的 spec（`*ToolSpec`）与拦截处理器（`process*Calls`）+ 共享作业渲染（`renderAgentList` / `agentJobLineLocked` / `agentJobState` / `agentJobSettled` / `channelClosed` / `agentsToolMessage`）。一个文件一个职责：**Run 内管理面**。 |
| 创建 `engine/agent_manage_test.go` | 上述 5 个工具与共享渲染的单元测试。 |
| 创建 `engine/fork.go` | 父历史 fork 的纯函数：`forkHistoryFor` / `lastNUserTurns` / `forkablePrefix` / `toModelMessage`。无 IO、无锁，纯变换。 |
| 创建 `engine/fork_test.go` | fork 纯函数 + 子代理侧注入行为的测试。 |
| 修改 `engine/agent.go` | 新增 5 个工具名常量；`HandoffToAgentParams` 增 `ForkTurns`；`Handoff` 增 `ForkedHistory`、`Inbox`；`handoffToolSpec` 增 `fork_turns` 参数。 |
| 修改 `engine/loop.go` | `bgTask` 增 `done` / `inbox` / `resultBrief`；新增 `finishBackgroundJob` 与 `inboxBufferSize`。 |
| 修改 `engine/handoff.go` | `dispatchAsync` 接线（建 `done`/`inbox`、`handoff.Inbox`、改用 `finishBackgroundJob`）；`RegisterSuspended` 建 `done`；`handoffOptions` 增 `fork` 并在 `runHandoff` 映射。 |
| 修改 `engine/suspend.go` | `finishResumedJob` 终点改用 `finishBackgroundJob`；`takeLeafForResume` / `takeParentForCascade` 重建 `inbox` 并写回 `s.Input.Inbox`。 |
| 修改 `engine/turn.go` | `toolSpecsWithHandoff` 追加 5 个 spec；`executeTurn` 调用 5 个处理器并按序追加消息；分发 switch 增 5 个 `continue`。 |
| 修改 `engine/sub_agent.go` | 新增 `drainInbox` 并在 `runLoopFrom` 迭代顶部调用；注入 `input.ForkedHistory` 并合并尾随 user 消息。 |
| 修改 `tools/subagent.go` | `SubAgentTool.Spec()` 增 `fork_turns` 参数（主代理看到的 spec 来源）。 |

---

## 任务 0：后台作业表增强（`done` 信号 + `inbox` + 统一收尾）

**为什么先做：** 任务 1-4 全部依赖"作业何时完成"与"如何向运行中的作业投递消息"这两个原语。

**文件：**
- 修改：`engine/loop.go`（`bgTask` 结构 `:173-192` 附近；新增常量与 helper）
- 修改：`engine/handoff.go`（`dispatchAsync` `:256-369`；`RegisterSuspended` `:202-227`）
- 修改：`engine/suspend.go`（`finishResumedJob` `:213-222`；`takeLeafForResume` `:123-131`；`takeParentForCascade` `:246-251`）
- 测试：`engine/agent_manage_test.go`（本任务先建文件，只放任务 0 的测试）

- [ ] **步骤 1：编写失败的测试**

追加到新建的 `engine/agent_manage_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// firstJobID returns the single registered job's id (tests dispatch one job).
func firstJobID(t *testing.T, e *Engine) string {
	t.Helper()
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for id := range e.bgTasks {
		return id
	}
	t.Fatal("no background job registered")
	return ""
}

func TestFinishBackgroundJob_ClosesDoneAndSetsBrief(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	jobID := firstJobID(t, e)
	waitJobPublished(t, e, jobID)

	e.bgMu.Lock()
	task := e.bgTasks[jobID]
	settled := agentJobSettled(task)
	brief := ""
	if task != nil {
		brief = task.resultBrief
	}
	e.bgMu.Unlock()

	if !settled {
		t.Fatal("done channel must be closed once the result is published")
	}
	if brief == "" {
		t.Error("resultBrief must be set so list/wait can show a short result")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestFinishBackgroundJob_ClosesDoneAndSetsBrief -v`
预期：编译失败，报 `undefined: agentJobSettled`（以及 `task.resultBrief` 不存在）

- [ ] **步骤 3：扩展 `bgTask` 并新增统一收尾**

在 `engine/loop.go` 的 `bgTask` 结构体末尾（`childRunID` 之后）追加字段：

```go
	// done is closed exactly once when the job publishes a terminal result.
	// It is a SIGNAL, not a channel to consume: several waiters and the
	// list/wait renderers can all observe completion without stealing the
	// result, which stays in `result` for agent_poll to collect.
	done chan struct{}
	// inbox carries mid-run messages injected by send_message. The sub-agent
	// run drains it at the top of each iteration (see drainInbox). nil for
	// sync/nested runs, which have no job handle to address.
	inbox chan string
	// resultBrief is a one-line digest of the terminal result, set when the job
	// finishes, so list_agents / wait_agent can show what a done job produced
	// without consuming its result channel.
	resultBrief string
```

在同文件 `defaultMaxSuspendedSubAgents` 常量旁新增：

```go
// inboxBufferSize bounds how many send_message calls may queue for one running
// sub-agent before the sender is told to back off. Small on purpose: messages
// are steered into the next iteration, not accumulated.
const inboxBufferSize = 8
```

在同文件 `inFlightCount` 之后新增收尾 helper：

```go
// finishBackgroundJob publishes a terminal result for a background job: it
// marks the result available (done closed) and records a short brief, then
// notifies the UI. The entry STAYS in the table until agent_poll consumes it —
// that preserves the outstanding-cap semantics (a slot frees on collection, not
// on completion) and lets list/wait still observe the finished job.
//
// done is closed under bgMu with a closed-check so a double finish (e.g. a
// cancellation racing a normal return) can never panic.
func (e *Engine) finishBackgroundJob(t *bgTask, result *HandoffResult) {
	if t == nil || result == nil {
		return
	}
	e.bgMu.Lock()
	if t.done != nil {
		select {
		case <-t.done:
			e.bgMu.Unlock()
			return // already finished
		default:
		}
		close(t.done)
	}
	t.resultBrief = briefDigest(result.Summary)
	e.bgMu.Unlock()

	select {
	case t.result <- result:
	default:
	}
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: string(t.agent), Detail: result.Summary})
	}
}
```

- [ ] **步骤 4：把两处结果投递改走 `finishBackgroundJob`**

`engine/handoff.go` 的 `dispatchAsync` goroutine：把收尾段

```go
		select {
		case task.result <- result:
		default:
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type:   "agent_done",
				Name:   string(task.agent),
				Detail: result.Summary,
			})
		}
	}()
```

替换为：

```go
		e.finishBackgroundJob(task, result)
	}()
```

同一函数里创建 `task` 时补上 `done` 与 `inbox`，并把 inbox 交给子代理：

```go
	e.bgSeq++
	jobID := fmt.Sprintf("bg-%d", e.bgSeq)
	bgCtx, cancel := context.WithCancel(ctx)
	inbox := make(chan string, inboxBufferSize)
	task := &bgTask{
		id:      jobID,
		agent:   AgentID(params.Agent),
		goal:    params.Goal,
		ctx:     bgCtx,
		cancel:  cancel,
		result:  make(chan *HandoffResult, 1),
		done:    make(chan struct{}),
		inbox:   inbox,
		startAt: time.Now(),
		state:   bgStateRunning,
	}
	// The run reads this inbox at each iteration boundary (send_message).
	handoff.Inbox = inbox
```

`engine/suspend.go` 的 `finishResumedJob`：把终点分支

```go
	if parentID == "" {
		// Top of the chain: deliver on its own channel for agent_poll.
		select {
		case task.result <- result:
		default:
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: string(task.agent), Detail: result.Summary})
		}
		return
	}
```

替换为：

```go
	if parentID == "" {
		// Top of the chain: publish for agent_poll / wait_agent.
		e.finishBackgroundJob(task, result)
		return
	}
```

`engine/handoff.go` 的 `RegisterSuspended`：创建 `bgTask` 时补 `done`（挂起条目也要能被 list 渲染、被 wait 观测）：

```go
	e.bgTasks[id] = &bgTask{
		id:         id,
		agent:      agent,
		goal:       goal,
		state:      bgStateAwaitingUser,
		suspended:  s,
		childRunID: s.ChildRunID,
		startAt:    time.Now(),
		result:     make(chan *HandoffResult, 1),
		done:       make(chan struct{}),
	}
```

`engine/suspend.go` 的 `takeLeafForResume` 在 `leafTask.ctx, leafTask.cancel = ...` 之前补：

```go
	// A resumed run is running again: give it a fresh inbox and hand it to the
	// run through the breakpoint's Input (runLoopResume copies Input forward).
	leafTask.inbox = make(chan string, inboxBufferSize)
	leaf.Input.Inbox = leafTask.inbox
```

`takeParentForCascade` 同理，在 `p.ctx, p.cancel = ...` 之前补：

```go
	p.inbox = make(chan string, inboxBufferSize)
	suspended.Input.Inbox = p.inbox
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestFinishBackgroundJob_ClosesDoneAndSetsBrief -v`
预期：PASS

- [ ] **步骤 6：跑全量 engine 测试防回归**

运行：`go test ./engine/ ./tools/ -count=1`
预期：PASS（既有 `TestProcessAgentPollCalls_*`、`TestAsyncOutstanding_*`、`TestBackgroundJobsPinnedInjection` 全绿）

- [ ] **步骤 7：Commit**

```bash
git add engine/loop.go engine/handoff.go engine/suspend.go engine/agent_manage_test.go
git commit -m "feat(engine): add bgTask done/inbox/resultBrief and unify job finishing"
```

---

## 任务 1：`list_agents`（作业可枚举）

**文件：**
- 创建：`engine/agent_manage.go`（spec + handler + 共享渲染）
- 修改：`engine/agent.go:13-25`（常量）、`engine/turn.go:836-846`、`engine/turn.go:560-585`、`engine/turn.go:594-612`
- 测试：`engine/agent_manage_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/agent_manage_test.go`：

```go
func TestListAgentsToolSpec_Registered(t *testing.T) {
	specs := (&Engine{tools: stubToolExecutor{}}).toolSpecsWithHandoff()
	for _, s := range specs {
		if s.Function.Name == ListAgentsToolName {
			return
		}
	}
	t.Fatal("toolSpecsWithHandoff must include list_agents")
}

func TestProcessListAgentsCalls_ShowsJobs(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{
		id: "bg-1", agent: "researcher", goal: "调研缓存",
		state: bgStateRunning, startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgTasks["bg-2"] = &bgTask{
		id: "bg-2", agent: "critic", goal: "审查方案",
		state: bgStateAwaitingUser, startAt: time.Now(), done: make(chan struct{}),
	}
	e.bgMu.Unlock()

	msgs := e.processListAgentsCalls([]ToolCallRequest{{ID: "tc1", Name: ListAgentsToolName}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	for _, want := range []string{"bg-1", "researcher", "bg-2", "awaiting_user"} {
		if !strings.Contains(msgs[0].Content, want) {
			t.Errorf("list output %q missing %q", msgs[0].Content, want)
		}
	}
}

func TestProcessListAgentsCalls_Empty(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processListAgentsCalls([]ToolCallRequest{{ID: "tc1", Name: ListAgentsToolName}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "No background sub-agent jobs") {
		t.Fatalf("empty list = %+v, want the no-jobs sentence", msgs)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestListAgents -v`
预期：编译失败，报 `undefined: ListAgentsToolName`

- [ ] **步骤 3：写实现**

在 `engine/agent.go` 常量块（`AgentResumeToolName` 之后）追加：

```go
	ListAgentsToolName    = "list_agents"
	WaitAgentToolName     = "wait_agent"
	SendMessageToolName   = "send_message"
	InterruptAgentToolName = "interrupt_agent"
	CloseAgentToolName    = "close_agent"
```

创建 `engine/agent_manage.go`：

```go
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file implements the Run-scoped sub-agent management surface: the
// main agent's ability to observe, wait for, message, interrupt and close the
// background jobs it dispatched. All five are ENGINE built-ins intercepted in
// executeTurn (like agent_poll / agent_resume): they never enter the tool
// executor, and sub-agents never see them (their tool面 is filtered in
// sub_agent.go). Every intercepted call MUST get exactly one tool response,
// or the next request breaks the assistant(tool_calls) → tool contract.

// inboxStateRunning / display helpers -----------------------------------------

// channelClosed reports whether ch has been closed, without consuming a value.
// A nil channel is never closed.
func channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// agentJobSettled reports whether a job has reached a state where waiting on it
// is pointless: it published a result, or it is suspended waiting for the user.
func agentJobSettled(t *bgTask) bool {
	if t == nil {
		return true
	}
	return t.state == bgStateAwaitingUser || channelClosed(t.done)
}

// agentJobState renders the job's display state. A finished-but-uncollected job
// stays bgStateRunning in the table (the outstanding cap still counts it), so
// the done channel — not the state field — is the truth for "done".
func agentJobState(t *bgTask) string {
	switch {
	case t.state == bgStateAwaitingUser:
		return "awaiting_user"
	case channelClosed(t.done):
		return "done (collect with agent_poll)"
	default:
		return "running"
	}
}

// agentJobLineLocked renders one job as a single line. Callers must hold bgMu.
func agentJobLineLocked(t *bgTask, now time.Time) string {
	elapsed := now.Sub(t.startAt).Round(time.Second)
	line := fmt.Sprintf("%s | %s | %s | %s | %s", t.id, t.agent, agentJobState(t), elapsed, t.goal)
	if t.resultBrief != "" {
		line += " | result: " + t.resultBrief
	}
	return line
}

// renderAgentList renders every background job, in id order for stable output.
func (e *Engine) renderAgentList() string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	ids := make([]string, 0, len(e.bgTasks))
	for id := range e.bgTasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := time.Now()
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, e.agentJobLineLocked(e.bgTasks[id], now))
	}
	e.bgMu.Unlock()
	if len(lines) == 0 {
		return "No background sub-agent jobs."
	}
	return strings.Join(lines, "\n")
}

// agentsToolMessage builds the tool-role Message answering one intercepted call.
func agentsToolMessage(callID, content string) Message {
	return Message{Role: "tool", ToolCallID: callID, Content: content, Timestamp: time.Now()}
}

// list_agents ----------------------------------------------------------------

// listAgentsToolSpec returns the tool definition for enumerating background jobs.
func listAgentsToolSpec(zh bool) ModelTool {
	desc := "List the background sub-agent jobs of this session with their status (running / done / awaiting_user), agent role, elapsed time and goal. Call it before dispatching more work to see what is already running."
	if zh {
		desc = "列出本会话的后台子代理任务及其状态（running / done / awaiting_user）、角色、已运行时长与目标。派发新任务前先用它查看已在运行的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        ListAgentsToolName,
			Description: desc,
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
	}
}

// processListAgentsCalls answers every list_agents call with the current table.
func (e *Engine) processListAgentsCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != ListAgentsToolName {
			continue
		}
		msgs = append(msgs, agentsToolMessage(call.ID, e.renderAgentList()))
	}
	return msgs
}
```

- [ ] **步骤 4：接线（3 处，缺一不可）**

`engine/turn.go` 的 `toolSpecsWithHandoff`（`:843-844` 之后）追加：

```go
	specs = append(specs, listAgentsToolSpec(e.isChinese))
```

`engine/turn.go` 的 `executeTurn` 拦截区（`:565` 之后）追加：

```go
	pendingListMsgs := e.processListAgentsCalls(calls)
```

并在 `:583-585` 之后追加消息落盘：

```go
	for _, msg := range pendingListMsgs {
		e.history = append(e.history, msg)
	}
```

`engine/turn.go` 的分发 switch（`:607-608` 之后）追加排除分支：

```go
		} else if call.Name == ListAgentsToolName {
			continue
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestListAgents -v`
预期：PASS

- [ ] **步骤 6：Commit**

```bash
git add engine/agent.go engine/agent_manage.go engine/turn.go
git commit -m "feat(engine): add list_agents built-in tool"
```

---

## 任务 2：`wait_agent`（阻塞等待原语）

**文件：**
- 修改：`engine/agent_manage.go`、`engine/turn.go`（同任务 1 的 3 处接线位置）
- 测试：`engine/agent_manage_test.go`

**语义（写进工具描述，必须与实现一致）：** `wait_agent` 只**观测**，不消费结果——收集仍由 `agent_poll` 负责。理由：`result` 是单值 channel，wait 若消费它，后续 poll 会报 "not found"。

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/agent_manage_test.go`：

```go
func TestWaitAgent_ReturnsImmediatelyWhenDone(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	jobID := firstJobID(t, e)
	waitJobPublished(t, e, jobID)

	start := time.Now()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"job_ids": []string{jobID}, "timeout_ms": 5000}),
	}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("wait on a finished job took %v, want immediate", elapsed)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "done") {
		t.Fatalf("wait output = %+v, want a done line", msgs)
	}
	// 结果必须仍可被 agent_poll 收集（wait 不消费）。
	e.bgMu.Lock()
	_, still := e.bgTasks[jobID]
	e.bgMu.Unlock()
	if !still {
		t.Fatal("wait_agent must not consume/remove the job")
	}
}

func TestWaitAgent_TimesOutOnRunningJob(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	defer close(a.released)

	start := time.Now()
	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"timeout_ms": 200}),
	}})
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("wait returned after %v, want it to block until the timeout", elapsed)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "timeout") {
		t.Fatalf("wait output = %+v, want a timeout report", msgs)
	}
}

func TestWaitAgent_WakesOnCompletion(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(a.released)
	}()

	msgs := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "tc1", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"timeout_ms": 5000}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "activity") {
		t.Fatalf("wait output = %+v, want an activity wake", msgs)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestWaitAgent -v`
预期：编译失败，报 `undefined: WaitAgentToolName`

- [ ] **步骤 3：写实现**

追加到 `engine/agent_manage.go`（常量放在文件顶部 import 之后）：

```go
// wait_agent timeout bounds. The tool takes a timeout_ms argument; the model
// can neither wait forever nor spin in a zero-length wait.
const (
	defaultAgentWaitMS      = 30_000
	minAgentWaitMS          = 100
	maxAgentWaitMS          = 600_000
	agentWaitPollInterval   = 250 * time.Millisecond
)

// WaitAgentParams is the argument schema of the wait_agent tool call.
// JobIDs empty means "every running job".
type WaitAgentParams struct {
	JobIDs    []string `json:"job_ids,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// clampAgentWaitMS bounds a caller-supplied timeout, defaulting when unset.
func clampAgentWaitMS(ms int) int {
	if ms <= 0 {
		return defaultAgentWaitMS
	}
	if ms < minAgentWaitMS {
		return minAgentWaitMS
	}
	if ms > maxAgentWaitMS {
		return maxAgentWaitMS
	}
	return ms
}

// waitAgentToolSpec returns the tool definition for waiting on background jobs.
func waitAgentToolSpec(zh bool) ModelTool {
	desc := "Block until one of the given background sub-agent jobs finishes, all of them finish, the timeout elapses, or the turn is cancelled — then report each job's status. OBSERVES only: it never consumes a result, so collect it afterwards with agent_poll(job_id). Omit job_ids to wait on every running job."
	if zh {
		desc = "阻塞等待，直到指定的后台子代理任务之一完成、全部完成、超时或本轮被取消，然后报告各任务状态。只观测、不消费结果——之后仍要用 agent_poll(job_id) 取结果。省略 job_ids 表示等待所有运行中的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        WaitAgentToolName,
			Description: desc,
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_ids": {"type": "array", "items": {"type": "string"},
						"description": "Job handles to wait for (e.g. bg-1). Omit to wait on every running job."},
					"timeout_ms": {"type": "integer",
						"description": "Maximum wait in milliseconds (default 30000, max 600000)."}
				},
				"required": []
			}`),
		},
	}
}

// processWaitAgentCalls answers every wait_agent call. It blocks the turn for
// up to the requested timeout — that is the point of the tool (the alternative
// is the model burning turns re-polling).
func (e *Engine) processWaitAgentCalls(ctx context.Context, calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != WaitAgentToolName {
			continue
		}
		var params WaitAgentParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, agentsToolMessage(call.ID,
				`Error: wait_agent expects {"job_ids": ["bg-1", ...], "timeout_ms": <int>}`))
			continue
		}
		msgs = append(msgs, agentsToolMessage(call.ID, e.waitForAgents(ctx, params)))
	}
	return msgs
}

// waitForAgents blocks until any target job settles, the timeout elapses, or
// ctx is cancelled. It polls the done signal at agentWaitPollInterval instead of
// a select over the dynamic target set: a goroutine-per-target select would leak
// one goroutine per target after a timed-out wait.
func (e *Engine) waitForAgents(ctx context.Context, params WaitAgentParams) string {
	timeout := time.Duration(clampAgentWaitMS(params.TimeoutMS)) * time.Millisecond

	e.initBackgroundTasks()
	e.bgMu.Lock()
	targets := e.waitTargetsLocked(params.JobIDs)
	e.bgMu.Unlock()
	if len(targets) == 0 {
		return "No matching background jobs to wait for."
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(agentWaitPollInterval)
	defer ticker.Stop()

	reason := ""
	for reason == "" {
		e.bgMu.Lock()
		settled := false
		for _, t := range targets {
			if agentJobSettled(t) {
				settled = true
				break
			}
		}
		e.bgMu.Unlock()
		if settled {
			reason = "activity"
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			reason = "timeout"
		case <-ctx.Done():
			reason = "cancelled"
		}
	}

	e.bgMu.Lock()
	var b strings.Builder
	fmt.Fprintf(&b, "wait_agent (%s):\n", reason)
	now := time.Now()
	for _, t := range targets {
		b.WriteString(e.agentJobLineLocked(t, now))
		b.WriteByte('\n')
	}
	e.bgMu.Unlock()
	b.WriteString("Collect a done job's result with agent_poll(job_id).")
	return b.String()
}

// waitTargetsLocked selects the jobs wait_agent observes: the named ones, or
// every running job when none were named. Callers must hold bgMu.
func (e *Engine) waitTargetsLocked(ids []string) []*bgTask {
	if len(ids) == 0 {
		out := make([]*bgTask, 0, len(e.bgTasks))
		for _, t := range e.bgTasks {
			if t.state == bgStateRunning {
				out = append(out, t)
			}
		}
		return out
	}
	out := make([]*bgTask, 0, len(ids))
	for _, id := range ids {
		if t, ok := e.bgTasks[id]; ok {
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **步骤 4：接线（3 处）**

`engine/turn.go`：

```go
// toolSpecsWithHandoff 内：
	specs = append(specs, waitAgentToolSpec(e.isChinese))

// executeTurn 拦截区：
	pendingWaitMsgs := e.processWaitAgentCalls(ctx, calls)

// 消息落盘：
	for _, msg := range pendingWaitMsgs {
		e.history = append(e.history, msg)
	}

// 分发 switch：
		} else if call.Name == WaitAgentToolName {
			continue
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestWaitAgent -v`
预期：PASS（3 个用例）

- [ ] **步骤 6：Commit**

```bash
git add engine/agent_manage.go engine/turn.go engine/agent_manage_test.go
git commit -m "feat(engine): add wait_agent blocking observation tool"
```

---

## 任务 3：`send_message`（运行中通信）

**文件：**
- 修改：`engine/agent_manage.go`、`engine/agent.go`（`Handoff.Inbox`）、`engine/turn.go`（接线）、`engine/sub_agent.go`（`drainInbox` + 迭代顶部调用）
- 测试：`engine/agent_manage_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/agent_manage_test.go`：

```go
func TestDrainInbox_AppendsUserMessages(t *testing.T) {
	inbox := make(chan string, 3)
	inbox <- "先看 P0 那几项"
	inbox <- "  "
	inbox <- "顺便看下测试"

	history := drainInbox(inbox, nil)
	if len(history) != 2 {
		t.Fatalf("drained %d messages, want 2 (blank skipped)", len(history))
	}
	for _, m := range history {
		if m.Role != "user" {
			t.Errorf("drained message role = %q, want user", m.Role)
		}
	}
	if len(drainInbox(inbox, nil)) != 0 {
		t.Error("a drained inbox must be empty")
	}
	if got := drainInbox(nil, nil); len(got) != 0 {
		t.Error("a nil inbox (sync/nested run) must be a no-op")
	}
}

func TestProcessSendMessageCalls_DeliversToRunningJob(t *testing.T) {
	a := &blockingAgent{id: "one", start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	defer close(a.released)
	jobID := firstJobID(t, e)

	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": jobID, "message": "换个方向"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "delivered") {
		t.Fatalf("send_message output = %+v, want delivered", msgs)
	}
	e.bgMu.Lock()
	task := e.bgTasks[jobID]
	e.bgMu.Unlock()
	if got := len(task.inbox); got != 1 {
		t.Errorf("inbox depth = %d, want 1", got)
	}
}

func TestProcessSendMessageCalls_UnknownJob(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	msgs := e.processSendMessageCalls([]ToolCallRequest{{
		ID: "tc1", Name: SendMessageToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-999", "message": "hi"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "not found") {
		t.Fatalf("send_message output = %+v, want not found", msgs)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestDrainInbox|TestProcessSendMessage' -v`
预期：编译失败，报 `undefined: drainInbox`

- [ ] **步骤 3：写实现——`Handoff.Inbox` 与 `drainInbox`**

`engine/agent.go` 的 `Handoff` 结构（`UserLanguage` 字段之前）追加：

```go
	// Inbox delivers mid-run messages injected by send_message. Internal: it is
	// never model-settable (the model writes only through the send_message tool,
	// which the engine routes here) and never serialized. nil for sync/nested
	// runs, which have no job handle to address.
	Inbox <-chan string `json:"-"`
```

`engine/sub_agent.go` 末尾追加：

```go
// drainInbox moves every pending send_message into history as a user message,
// at the top of an iteration. Appending mid-iteration would land between an
// assistant(tool_calls) message and its tool responses, which the API rejects —
// the same reason tokenBudgetNudge is armed and injected here.
//
// Non-blocking by design: an empty inbox (the common case) costs one select.
func drainInbox(inbox <-chan string, history []ModelMessage) []ModelMessage {
	if inbox == nil {
		return history
	}
	for {
		select {
		case msg, ok := <-inbox:
			if !ok {
				return history
			}
			if strings.TrimSpace(msg) == "" {
				continue
			}
			history = append(history, ModelMessage{Role: "user", Content: msg})
		default:
			return history
		}
	}
}
```

在 `runLoopFrom` 的迭代顶部、`tokenNudgePending` 注入块之后（`engine/sub_agent.go:502` 之后）插入：

```go
		// Mid-run messages from send_message. Drained at the top of the turn so
		// the injected user message never splits a tool-call/tool-response pair.
		history = drainInbox(input.Inbox, history)
```

- [ ] **步骤 4：写实现——`send_message` 工具**

追加到 `engine/agent_manage.go`：

```go
// SendMessageParams is the argument schema of the send_message tool call.
type SendMessageParams struct {
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

// sendMessageToolSpec returns the tool definition for messaging a running job.
func sendMessageToolSpec(zh bool) ModelTool {
	desc := "Send a message to a RUNNING background sub-agent. The sub-agent reads it at its next step and can act on it (redirect, add a constraint, answer its question). Use agent_poll to see its state first; for a suspended job use agent_resume instead."
	jobDesc := "The job handle, e.g. bg-3"
	msgDesc := "The message, stated as an instruction or fact for the sub-agent"
	if zh {
		desc = "向正在运行的后台子代理发送一条消息。它会在下一步读到并据此行动（改方向、加约束、回答它的疑问）。先用 agent_poll 看状态；挂起中的任务请改用 agent_resume。"
		jobDesc = "任务句柄，例如 bg-3"
		msgDesc = "消息内容，写成给子代理的指令或事实"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"job_id": {"type": "string", "description": %q},
			"message": {"type": "string", "description": %q}
		},
		"required": ["job_id", "message"]
	}`, jobDesc, msgDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        SendMessageToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// processSendMessageCalls delivers every send_message call into its target's
// inbox. A non-running or unknown job is refused with a pointer to the right
// tool, so the model can self-correct in one turn.
func (e *Engine) processSendMessageCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != SendMessageToolName {
			continue
		}
		var params SendMessageParams
		if err := json.Unmarshal(call.Input, &params); err != nil ||
			strings.TrimSpace(params.JobID) == "" || strings.TrimSpace(params.Message) == "" {
			msgs = append(msgs, agentsToolMessage(call.ID,
				"Error: send_message requires a non-empty job_id and message"))
			continue
		}

		e.initBackgroundTasks()
		e.bgMu.Lock()
		task := e.bgTasks[params.JobID]
		if task == nil {
			e.bgMu.Unlock()
			msgs = append(msgs, agentsToolMessage(call.ID, fmt.Sprintf(
				"Error: send_message job %q not found (already consumed or run ended).", params.JobID)))
			continue
		}
		if task.state != bgStateRunning || channelClosed(task.done) {
			state := agentJobState(task)
			e.bgMu.Unlock()
			msgs = append(msgs, agentsToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s is not running (%s). Use agent_poll(%s) to collect it, or agent_resume(%s, <answer>) if it is waiting for the user.",
				params.JobID, state, params.JobID, params.JobID)))
			continue
		}
		inbox := task.inbox
		e.bgMu.Unlock()

		if inbox == nil {
			msgs = append(msgs, agentsToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s has no message inbox (it was not started as an async job).", params.JobID)))
			continue
		}
		select {
		case inbox <- params.Message:
			msgs = append(msgs, agentsToolMessage(call.ID, fmt.Sprintf(
				"Message delivered to %s; it reads it at its next step.", params.JobID)))
		default:
			msgs = append(msgs, agentsToolMessage(call.ID, fmt.Sprintf(
				"Error: job %s's inbox is full — it has not consumed the previous messages yet. Poll it before sending more.", params.JobID)))
		}
	}
	return msgs
}
```

- [ ] **步骤 5：接线（3 处）**

`engine/turn.go`：

```go
	specs = append(specs, sendMessageToolSpec(e.isChinese))

	pendingSendMsgs := e.processSendMessageCalls(calls)

	for _, msg := range pendingSendMsgs {
		e.history = append(e.history, msg)
	}

		} else if call.Name == SendMessageToolName {
			continue
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestDrainInbox|TestProcessSendMessage|TestFinishBackgroundJob' -v`
预期：PASS

- [ ] **步骤 7：Commit**

```bash
git add engine/agent.go engine/agent_manage.go engine/turn.go engine/sub_agent.go engine/agent_manage_test.go
git commit -m "feat(engine): add send_message mid-run inbox for running sub-agents"
```

---

## 任务 4：`interrupt_agent` + `close_agent`（可中断、可关闭）

**文件：**
- 修改：`engine/agent_manage.go`、`engine/turn.go`（接线）
- 测试：`engine/agent_manage_test.go`

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/agent_manage_test.go`；同时新增一个尊重 ctx 的测试桩：

```go
// ctxWaitAgent blocks until its context is cancelled, then reports a cancelled
// run — the shape a real sub-agent takes when interrupt_agent fires.
type ctxWaitAgent struct {
	id     AgentID
	start  chan struct{}
	cancel chan struct{}
}

func (a *ctxWaitAgent) ID() AgentID     { return a.id }
func (a *ctxWaitAgent) Spec() AgentSpec { return AgentSpec{ID: a.id} }
func (a *ctxWaitAgent) Run(ctx context.Context, _ Handoff) (*HandoffResult, error) {
	close(a.start)
	<-ctx.Done()
	close(a.cancel)
	return &HandoffResult{
		Summary: "(cancelled)", Blocked: true, BlockedBy: "cancelled",
		FinishReason: HandoffReasonCancelled,
	}, nil
}

func TestInterruptAgent_StopsRunningJob(t *testing.T) {
	a := &ctxWaitAgent{id: "one", start: make(chan struct{}), cancel: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "慢任务", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	jobID := firstJobID(t, e)

	msgs := e.processInterruptAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: InterruptAgentToolName,
		Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "interrupted") {
		t.Fatalf("interrupt output = %+v, want interrupted", msgs)
	}
	select {
	case <-a.cancel:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt must cancel the running sub-agent's context")
	}
	waitJobPublished(t, e, jobID)

	// 中断后条目仍在，结果可被 poll 收集（部分结果语义）。
	pollMsgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc2", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(pollMsgs) != 1 || !strings.Contains(pollMsgs[0].Content, "done") {
		t.Fatalf("poll after interrupt = %+v, want done", pollMsgs)
	}
}

func TestCloseAgent_RemovesJobAndDescendants(t *testing.T) {
	a := &ctxWaitAgent{id: "one", start: make(chan struct{}), cancel: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{id: "bg-1", agent: "one", state: bgStateRunning,
		startAt: time.Now(), done: make(chan struct{}), cancel: func() {}}
	e.bgTasks["bg-2"] = &bgTask{id: "bg-2", agent: "one", state: bgStateAwaitingUser,
		startAt: time.Now(), done: make(chan struct{}), childRunID: ""}
	e.bgTasks["bg-2"].suspended = &SuspendedRun{ChildRunID: "bg-1"} // bg-2 is a child of bg-1
	e.bgMu.Unlock()

	msgs := e.processCloseAgentCalls([]ToolCallRequest{{
		ID: "tc1", Name: CloseAgentToolName,
		Input: mustJSON(map[string]string{"job_id": "bg-1"}),
	}})
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Closed") {
		t.Fatalf("close output = %+v, want Closed", msgs)
	}
	e.bgMu.Lock()
	_, has1 := e.bgTasks["bg-1"]
	_, has2 := e.bgTasks["bg-2"]
	e.bgMu.Unlock()
	if has1 || has2 {
		t.Errorf("close_agent must remove the job and its descendants (bg-1=%v bg-2=%v)", has1, has2)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestInterruptAgent|TestCloseAgent' -v`
预期：编译失败，报 `undefined: InterruptAgentToolName`

- [ ] **步骤 3：写实现**

追加到 `engine/agent_manage.go`：

```go
// AgentJobParams is the argument schema shared by interrupt_agent and
// close_agent: both name a single job handle.
type AgentJobParams struct {
	JobID string `json:"job_id"`
}

// interruptAgentToolSpec returns the tool definition for soft-stopping a job.
func interruptAgentToolSpec(zh bool) ModelTool {
	desc := "Interrupt a running background sub-agent. It stops at its next safe point with finish_reason=cancelled; the job stays collectable, so fetch the partial result with agent_poll(job_id). Use it when the sub-agent has gone off track and waiting would waste budget."
	jobDesc := "The job handle, e.g. bg-3"
	if zh {
		desc = "中断正在运行的后台子代理。它会在下一个安全点停下（finish_reason=cancelled），任务仍可收集——用 agent_poll(job_id) 取部分结果。当子代理跑偏、继续等待会浪费预算时使用。"
		jobDesc = "任务句柄，例如 bg-3"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {"job_id": {"type": "string", "description": %q}},
		"required": ["job_id"]
	}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        InterruptAgentToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// closeAgentToolSpec returns the tool definition for permanently dropping a job.
func closeAgentToolSpec(zh bool) ModelTool {
	desc := "Close a background sub-agent and every job below it, permanently. Unlike interrupt_agent, the job is removed and its result can no longer be collected — use it when the work is no longer wanted."
	jobDesc := "The job handle, e.g. bg-3"
	if zh {
		desc = "彻底关闭一个后台子代理及其下级所有任务。与 interrupt_agent 不同，任务会被移除，结果不再可取——当这项工作已经不需要时使用。"
		jobDesc = "任务句柄，例如 bg-3"
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {"job_id": {"type": "string", "description": %q}},
		"required": ["job_id"]
	}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        CloseAgentToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// processInterruptAgentCalls answers every interrupt_agent call.
func (e *Engine) processInterruptAgentCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != InterruptAgentToolName {
			continue
		}
		var params AgentJobParams
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, agentsToolMessage(call.ID, "Error: interrupt_agent requires a non-empty job_id"))
			continue
		}
		msgs = append(msgs, agentsToolMessage(call.ID, e.interruptJob(params.JobID)))
	}
	return msgs
}

// interruptJob cancels a running job's context. The entry STAYS in the table so
// the partial result is still collectable — that is the difference from
// closeAgentJob.
func (e *Engine) interruptJob(jobID string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	task, ok := e.bgTasks[jobID]
	if !ok {
		e.bgMu.Unlock()
		return fmt.Sprintf("Error: interrupt_agent job %q not found.", jobID)
	}
	if task.state != bgStateRunning || channelClosed(task.done) {
		state := agentJobState(task)
		e.bgMu.Unlock()
		return fmt.Sprintf("Job %s is not running (%s) — nothing to interrupt.", jobID, state)
	}
	cancel := task.cancel
	e.bgMu.Unlock()
	if cancel == nil {
		return fmt.Sprintf("Job %s has no in-flight request to interrupt.", jobID)
	}
	cancel()
	return fmt.Sprintf("Job %s interrupted; it stops at its next safe point. Collect the partial result with agent_poll(%s).", jobID, jobID)
}

// processCloseAgentCalls answers every close_agent call.
func (e *Engine) processCloseAgentCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != CloseAgentToolName {
			continue
		}
		var params AgentJobParams
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, agentsToolMessage(call.ID, "Error: close_agent requires a non-empty job_id"))
			continue
		}
		msgs = append(msgs, agentsToolMessage(call.ID, e.closeAgentJob(params.JobID)))
	}
	return msgs
}

// closeAgentJob cancels and forgets a job and every job below it. Cancellation
// happens AFTER the table edit (cancel funcs are captured first) so a run
// finishing during the edit cannot resurrect an entry that was just dropped.
func (e *Engine) closeAgentJob(jobID string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	if _, ok := e.bgTasks[jobID]; !ok {
		e.bgMu.Unlock()
		return fmt.Sprintf("Error: close_agent job %q not found.", jobID)
	}
	ids := e.descendantJobIDsLocked(jobID)
	cancels := make([]context.CancelFunc, 0, len(ids))
	suspended := 0
	for _, id := range ids {
		t := e.bgTasks[id]
		if t == nil {
			continue
		}
		if t.state == bgStateAwaitingUser {
			suspended++
		}
		if t.cancel != nil {
			cancels = append(cancels, t.cancel)
		}
		if t.done != nil && !channelClosed(t.done) {
			close(t.done) // release any waiter
		}
		delete(e.bgTasks, id)
	}
	e.bgMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	msg := fmt.Sprintf("Closed %d job(s): %s.", len(ids), strings.Join(ids, ", "))
	if suspended > 0 {
		msg += fmt.Sprintf(" %d suspended job(s) were dropped — their pending questions can no longer be answered.", suspended)
	}
	return msg
}

// descendantJobIDsLocked returns id plus every entry that descends from it,
// found by reverse childRunID lookup. Callers must hold bgMu.
func (e *Engine) descendantJobIDsLocked(id string) []string {
	out := []string{id}
	for i := 0; i < len(out); i++ {
		for pid, pt := range e.bgTasks {
			if pt.childRunID == out[i] && !containsString(out, pid) {
				out = append(out, pid)
			}
		}
	}
	return out
}
```

- [ ] **步骤 4：接线（3 处 ×2 工具）**

`engine/turn.go`：

```go
	specs = append(specs, interruptAgentToolSpec(e.isChinese))
	specs = append(specs, closeAgentToolSpec(e.isChinese))

	pendingInterruptMsgs := e.processInterruptAgentCalls(calls)
	pendingCloseMsgs := e.processCloseAgentCalls(calls)

	for _, msg := range pendingInterruptMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingCloseMsgs {
		e.history = append(e.history, msg)
	}

		} else if call.Name == InterruptAgentToolName {
			continue
		} else if call.Name == CloseAgentToolName {
			continue
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run 'TestInterruptAgent|TestCloseAgent' -v`
预期：PASS

- [ ] **步骤 6：Commit**

```bash
git add engine/agent_manage.go engine/turn.go engine/agent_manage_test.go
git commit -m "feat(engine): add interrupt_agent and close_agent lifecycle tools"
```

---

## 任务 5：`fork` 父历史（上下文注入能力）

**文件：**
- 创建：`engine/fork.go`、`engine/fork_test.go`
- 修改：`engine/agent.go`（`HandoffToAgentParams.ForkTurns`、`Handoff.ForkedHistory`、`handoffToolSpec`）、`engine/handoff.go`（`handoffOptions.fork`）、`engine/sub_agent.go`（注入 + 合并尾随 user 消息）、`tools/subagent.go`（主代理 spec）

**设计取舍（已定）：** 采用"fork 到最后一个 user 消息边界"的保守策略。理由：user 消息天然是安全边界（其之前的 tool_calls 必然已被响应），而当前回合末尾那条携带本次 handoff 的 `assistant(tool_calls)` 绝不能进入子代理历史——那会在子代理历史里留下无响应的 tool_call_id。代价是 fork 只到"用户上一轮请求"，不包含主代理已产出的分析结论；这是安全性优先的可接受损失。
**被否决的方案：** 直接复制 `e.history` 再做"dangling tool_call 修剪"——修剪规则在工具并发/失败路径下有多个边界情况，不如 user 边界可证安全。

- [ ] **步骤 1：编写失败的测试**

创建 `engine/fork_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

func forkHistory() []Message {
	return []Message{
		{Role: "user", Content: "第一个请求"},
		{Role: "assistant", Content: "好的", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read", Arguments: "{}"}}},
		{Role: "tool", ToolCallID: "c1", Content: "文件内容"},
		{Role: "assistant", Content: "结论一"},
		{Role: "user", Content: "第二个请求"},
		{Role: "assistant", Content: "正在处理", ToolCalls: []MessageToolCall{{ID: "c2", Name: HandoffToolName, Arguments: "{}"}}},
	}
}

func TestForkablePrefix_EndsAtLastUserMessage(t *testing.T) {
	got := forkablePrefix(forkHistory())
	if len(got) != 5 {
		t.Fatalf("prefix len = %d, want 5 (through the second user message)", len(got))
	}
	if got[len(got)-1].Role != "user" {
		t.Fatalf("prefix must end on a user message, got %q", got[len(got)-1].Role)
	}
	// 悬挂的 assistant(tool_calls)（本次 handoff）绝不能被 fork。
	for _, m := range got {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c2" {
				t.Fatal("the in-flight handoff call must not be forked into the sub-agent")
			}
		}
	}
}

func TestLastNUserTurns(t *testing.T) {
	got := lastNUserTurns(forkHistory(), 2)
	if len(got) != 6 || got[0].Content != "第一个请求" {
		t.Fatalf("last 2 turns = %d msgs starting %q, want 6 starting at 第一个请求", len(got), got[0].Content)
	}
	got = lastNUserTurns(forkHistory(), 1)
	if len(got) != 2 || got[0].Content != "第二个请求" {
		t.Fatalf("last 1 turn = %+v, want to start at 第二个请求", got)
	}
	if got := lastNUserTurns(forkHistory(), 9); len(got) != len(forkHistory()) {
		t.Errorf("asking for more turns than exist must return everything")
	}
}

func TestForkHistoryFor_Specs(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.history = forkHistory()

	if got := e.forkHistoryFor("none"); got != nil {
		t.Errorf(`fork_turns="none" = %v, want nil`, got)
	}
	if got := e.forkHistoryFor(""); got != nil {
		t.Errorf("empty fork_turns = %v, want nil", got)
	}
	if got := e.forkHistoryFor("全部"); got != nil {
		t.Errorf("invalid fork_turns must be ignored, got %v", got)
	}
	all := e.forkHistoryFor("all")
	if len(all) != 5 || all[0].Content != "第一个请求" {
		t.Errorf(`fork_turns="all" = %d msgs, want 5 from the start`, len(all))
	}
	one := e.forkHistoryFor("1")
	if len(one) != 2 || one[0].Content != "第二个请求" {
		t.Errorf(`fork_turns="1" = %+v, want just the last user turn prefix`, one)
	}
	if one[0].ToolCalls != nil || one[1].Role != "assistant" {
		t.Errorf("forked messages must carry tool calls converted to ModelMessage")
	}
}

func TestRunLoopFrom_InjectsForkBeforeVolatileGoal(t *testing.T) {
	model := &stubCompleteModel{resp: "done"}
	r := NewSubAgentRunner(model, stubToolExecutor{}, nil, "deepact-chat")
	r.workDir = t.TempDir()

	_, err := r.runLoop(context.Background(), Handoff{
		Agent:         AgentSub,
		Goal:          "总结上面的讨论",
		ForkedHistory: []ModelMessage{{Role: "user", Content: "第一个请求"}, {Role: "assistant", Content: "结论一"}},
	}, "", 3)
	if err != nil {
		t.Fatalf("runLoop: %v", err)
	}
	msgs := model.last.Messages
	if len(msgs) < 4 {
		t.Fatalf("request has %d messages, want system + fork + goal", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content == "" {
		t.Fatalf("first message must be the system prompt, got %q", msgs[0].Role)
	}
	if msgs[1].Content != "第一个请求" || msgs[2].Content != "结论一" {
		t.Fatalf("forked history must follow the system prompt, got %q / %q", msgs[1].Content, msgs[2].Content)
	}
	// 尾随 user 与 volatile goal 必须合并，避免连续两条 user 消息。
	if !strings.Contains(msgs[3].Content, "总结上面的讨论") {
		t.Fatalf("volatile goal must be present, got %q", msgs[3].Content)
	}
	if msgs[2].Role == "user" && msgs[3].Role == "user" {
		t.Fatal("forked prefix + volatile goal must not produce two adjacent user messages")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestFork|TestLastNUserTurns|TestRunLoopFrom_InjectsFork' -v`
预期：编译失败，报 `undefined: forkablePrefix`

- [ ] **步骤 3：写实现——纯函数**

创建 `engine/fork.go`：

```go
package engine

import "strconv"

// Forking gives a sub-agent the parent's conversation prefix as its starting
// context, instead of forcing the delegating model to re-narrate what it
// already knows. The copy is deliberately conservative: it always ends on a
// USER message boundary.
//
// Why that boundary: a user message can never leave a dangling tool_call_id
// (every tool call before it must already have been answered, or the parent
// loop would have failed), and it excludes the in-flight assistant message that
// carries THIS handoff call — which has no tool response yet and would violate
// the assistant(tool_calls) → tool contract inside the sub-agent's history.

// forkHistoryFor materializes the fork spec into messages. Spec: "" or "none"
// = no fork; "all" = the whole safe prefix; a positive integer N = the last N
// user turns. Anything else is ignored (no fork) rather than guessed at.
func (e *Engine) forkHistoryFor(spec string) []ModelMessage {
	switch spec {
	case "", "none":
		return nil
	case "all":
		return toModelMessages(forkablePrefix(e.history))
	}
	n, err := strconv.Atoi(spec)
	if err != nil || n <= 0 {
		return nil
	}
	return toModelMessages(forkablePrefix(lastNUserTurns(e.history, n)))
}

// forkablePrefix returns history up to and including the LAST user message.
// Returns nil when there is no user message (nothing safe to copy).
func forkablePrefix(history []Message) []Message {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[:i+1]
		}
	}
	return nil
}

// lastNUserTurns returns the suffix of history starting at the Nth-from-last
// user message. Asking for more turns than exist returns history unchanged.
func lastNUserTurns(history []Message, n int) []Message {
	seen := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" {
			continue
		}
		seen++
		if seen == n {
			return history[i:]
		}
	}
	return history
}

// toModelMessages converts conversation Messages into the sub-agent's model
// history shape, preserving tool calls and tool-call ids.
func toModelMessages(history []Message) []ModelMessage {
	if len(history) == 0 {
		return nil
	}
	out := make([]ModelMessage, 0, len(history))
	for _, m := range history {
		mm := ModelMessage{
			Role:             m.Role,
			Content:          m.Content,
			ToolCallID:       m.ToolCallID,
			ReasoningContent: m.ReasoningContent,
		}
		for _, tc := range m.ToolCalls {
			mm.ToolCalls = append(mm.ToolCalls, ModelToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: ModelFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		out = append(out, mm)
	}
	return out
}
```

- [ ] **步骤 4：写实现——参数、Handoff 字段与 spec**

`engine/agent.go` 的 `HandoffToAgentParams`（`Async` 之前）追加：

```go
	// ForkTurns copies the MAIN agent's conversation prefix into the sub-agent's
	// starting context: "none"/"" (default), "all", or a positive integer N (the
	// last N user turns). Only honored at depth 0 — a nested delegation has no
	// main history to fork. Forked tokens count toward the sub-agent's budget.
	ForkTurns string `json:"fork_turns,omitempty"`
```

`engine/agent.go` 的 `Handoff`（`Inbox` 之前）追加：

```go
	// ForkedHistory is the parent conversation prefix copied into this run,
	// injected verbatim after the system prompt. Internal: never model-settable,
	// never serialized (the model opts in through fork_turns, not by supplying
	// history itself).
	ForkedHistory []ModelMessage `json:"-"`
```

`engine/agent.go` 的 `handoffToolSpec`（子代理侧回退 spec）：`asyncDesc` 之后新增描述变量并在 params 中加字段：

```go
	forkTurnsDesc := `How much of YOUR conversation to copy into the sub-agent as starting context: "none" (default, independent run), "all", or the number of preceding user turns as a string, e.g. "2". Only the first-level delegation can fork.`
	if zh {
		forkTurnsDesc = `把你自己对话的多少内容复制给子代理作为起始上下文："none"（默认，独立运行）、"all"，或前面 N 轮用户消息的数字字符串，如 "2"。只有第一层委派可以 fork。`
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					...
					"fork_turns": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["agent", "goal"]
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc, expectedOutputDesc, asyncDesc, forkTurnsDesc)
```

（`...` 处为现有 `agent`/`goal`/`context`/`tools`/`constraints`/`expected_output`/`async` 字段，原样保留；只新增 `fork_turns` 字段并把 `forkTurnsDesc` 追加到 `fmt.Sprintf` 参数末尾。）

`tools/subagent.go` 的 `SubAgentTool.Spec()`：同样在 params 里新增 `fork_turns` 字段，并在 `fmt.Sprintf` 参数末尾追加同名描述变量：

```go
			"fork_turns": {"type": "string",
				"description": "How much of YOUR conversation to copy into the sub-agent as starting context: \"none\" (default), \"all\", or a number of preceding user turns as a string, e.g. \"2\". Only the first-level delegation can fork."}
```

- [ ] **步骤 5：写实现——映射与注入**

`engine/handoff.go` 的 `handoffOptions` 结构末尾追加：

```go
	// fork materializes params.ForkTurns into the copied conversation prefix.
	// nil (nested backend, tests) = fork_turns is ignored.
	fork func(spec string) []ModelMessage
```

`engine/handoff.go` 的 `runHandoff`，在 `handoff := Handoff{...}` 之后追加：

```go
	if opts.fork != nil {
		handoff.ForkedHistory = opts.fork(params.ForkTurns)
	}
```

`engine/handoff.go` 的 `Engine.RunSubAgent` 的 `handoffOptions{...}` 中加：

```go
		fork:     e.forkHistoryFor,
```

`engine/sub_agent.go` 的 `runLoopFrom` 中，`history = []ModelMessage{{Role: "system", Content: system}}` 之后、volatile 注入之前插入 fork，并把 volatile 合并进尾随 user 消息：

```go
		// Forked parent context sits between the stable system prefix and the
		// volatile goal, so the goal stays at the highest-recency position.
		history = append(history, input.ForkedHistory...)
		if volatileContent := r.buildVolatilePrompt(input); volatileContent != "" {
			// A forked prefix ends on a user message; appending the goal as a
			// second user message would break role alternation for providers
			// that enforce it. Merge instead of dropping either part.
			if n := len(history); n > 0 && history[n-1].Role == "user" {
				history[n-1].Content += "\n\n" + volatileContent
			} else {
				history = append(history, ModelMessage{Role: "user", Content: volatileContent})
			}
		}
```

（替换现有的 `if volatileContent := r.buildVolatilePrompt(input); volatileContent != "" { history = append(history, ModelMessage{Role: "user", Content: volatileContent}) }` 块。）

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestFork|TestLastNUserTurns|TestRunLoopFrom_InjectsFork' -v`
预期：PASS（4 个用例）

- [ ] **步骤 7：跑全量测试防回归**

运行：`go test ./engine/ ./tools/ -count=1`
预期：PASS

- [ ] **步骤 8：Commit**

```bash
git add engine/fork.go engine/fork_test.go engine/agent.go engine/handoff.go engine/sub_agent.go tools/subagent.go
git commit -m "feat(engine): add fork_turns parent-context injection for sub-agents"
```

---

## 任务 6：端到端联调与文档同步

**文件：**
- 修改：`engine/agent_manage_test.go`（端到端用例）
- 修改：`context/langpacks/*.md` 或系统提示中与子代理管理相关的段落（若存在需要同步的描述）

- [ ] **步骤 1：编写端到端测试**

追加到 `engine/agent_manage_test.go`：

```go
// TestAgentManagement_EndToEnd walks the intended model flow: dispatch async →
// list → wait → interrupt → poll. It asserts the five tools cooperate on one
// job without any of them stealing another's result.
func TestAgentManagement_EndToEnd(t *testing.T) {
	a := &ctxWaitAgent{id: "one", start: make(chan struct{}), cancel: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	if _, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "one", Goal: "长调研", Async: true,
	}, 0, "中文"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	<-a.start
	jobID := firstJobID(t, e)

	if out := e.processListAgentsCalls([]ToolCallRequest{{ID: "t1", Name: ListAgentsToolName}}); !strings.Contains(out[0].Content, "running") {
		t.Fatalf("list = %q, want running", out[0].Content)
	}
	if out := e.processWaitAgentCalls(context.Background(), []ToolCallRequest{{
		ID: "t2", Name: WaitAgentToolName,
		Input: mustJSON(map[string]any{"job_ids": []string{jobID}, "timeout_ms": 200}),
	}}); !strings.Contains(out[0].Content, "timeout") {
		t.Fatalf("wait = %q, want timeout while still running", out[0].Content)
	}
	if out := e.processInterruptAgentCalls([]ToolCallRequest{{
		ID: "t3", Name: InterruptAgentToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}}); !strings.Contains(out[0].Content, "interrupted") {
		t.Fatalf("interrupt = %q", out[0].Content)
	}
	<-a.cancel
	waitJobPublished(t, e, jobID)

	poll := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "t4", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if !strings.Contains(poll[0].Content, "done") {
		t.Fatalf("poll = %q, want done", poll[0].Content)
	}
}
```

- [ ] **步骤 2：运行端到端测试**

运行：`go test ./engine/ -run TestAgentManagement_EndToEnd -v`
预期：PASS

- [ ] **步骤 3：全量验证**

运行：`go build ./... && go test ./... -count=1`
预期：全部 PASS；若 `golangci-lint` 在 CI 中启用，再跑 `make lint`（以 Makefile 为准）

- [ ] **步骤 4：同步系统提示中的工具清单（如适用）**

确认 `context/promptset/zh/system.md`、`context/langpacks/*.md` 中是否有"主代理可用的子代理工具"枚举段落。若有，补上 5 个新工具的一句话说明；若没有枚举工具清单（当前实现未见），本步骤跳过并在 commit message 中注明。

- [ ] **步骤 5：Commit**

```bash
git add engine/agent_manage_test.go
git commit -m "test(engine): end-to-end coverage for the sub-agent management surface"
```

---

## 自检记录（写计划后已执行）

1. **规格覆盖度**：5 项 P0 → 任务 1（list_agents）、任务 2（wait_agent）、任务 3（send_message）、任务 4（interrupt_agent + close_agent）、任务 5（fork 父历史）。基础设施由任务 0 承担。**无遗漏**；跨 Run 常驻已按范围线显式排除。
2. **占位符扫描**：无"待定/TODO/类似任务 N"；每个代码步骤都给了完整可粘贴的 Go 代码；仅任务 6 步骤 4 是条件性步骤（有明确判定条件与跳过标准，非占位）。
3. **类型一致性**：`bgTask.done chan struct{}` / `inbox chan string` / `resultBrief string` 在任务 0 定义，任务 1-4 一致使用；`Handoff.Inbox <-chan string` 与 `bgTask.inbox chan string` 赋值兼容（`chan T` → `<-chan T`）；`Handoff.ForkedHistory []ModelMessage` 与 `forkHistoryFor` 返回类型一致；`AgentJobParams` 被 interrupt/close 共用；`agentsToolMessage` 全文件唯一定义。已 grep 确认 25 个新标识符全仓零冲突。
4. **既有语义保持**：`finishBackgroundJob` 不改变 `state`（done 任务在表中仍为 `bgStateRunning`），因此 `max_outstanding_async_subagents` 的"slot 在 poll 时才释放"语义与既有测试（`engine/sub_agent_outstanding_test.go`）不变。

## 执行记录与计划修正（任务 0 实施时回填）

执行任务 0 时发现并修正了计划中的两处任务排序缺陷，以及质量审查提出的改进项。后续任务按修正后的边界执行：

1. **`Handoff.Inbox` 字段提前到任务 0**（原排在任务 3）。任务 0 的 `dispatchAsync` 接线立即需要它，否则无法编译。
2. **作业视图原语 `channelClosed` / `agentJobSettled` 提前到任务 0**（原排在任务 1）。任务 0 的测试需要 `agentJobSettled`，否则任务 0 无法独立通过。为避开 golangci-lint 的 `unused`（`Makefile:22-23` 的 lint 门禁），同批的 `agentJobState` / `agentJobLineLocked` / `agentsToolMessage` **推迟到任务 1 随其调用者一起引入**；引入时采用修正后的形态：状态函数只返回纯 token（`running`/`done`/`awaiting_user`），操作提示（"collect with agent_poll"）留给渲染器。
3. **`finishBackgroundJob` 的发布顺序**（质量审查 N2）：改为在**同一把 bgMu 内**按 `result 投递 → resultBrief → close(done)` 顺序发布。这样被 done 唤醒的 waiter 一定能立刻读到 result，且测试的 `waitJobPublished` 不会因 `resultBrief` 尚未写入而 flake。
4. **`agentJobSettled` 的锁契约**（质量审查 N1）：文档明确要求调用方持 bgMu（`t.state` 由后台 goroutine 在锁下改写）。
5. **nil 分支加日志**（质量审查 N3）：`finishBackgroundJob` 收到 nil 时 `loopLog.Printf`，不再静默 no-op。
6. **测试补强**：新增"finish 不得改动 `state`"（outstanding cap 硬约束）与"双重 finish 幂等（不 panic、不重复投递）"两条断言。

**已知既有问题（非本次引入，记录待后续单独处理）**：`finishResumedJob` 的"再次挂起"分支仍直接发 `agent_done`（`engine/suspend.go:202-204`），与 `dispatchAsync` 的"挂起时不发 `agent_done`"约定不一致——一个作业一生可能出现两次 `agent_done`。本计划范围不含该修复。

---

## 执行交接

**计划已完成并保存到 `docs/superpowers/plans/2026-09-29-subagent-run-scoped-management.md`。两种执行方式：**

**1. 子代理驱动（推荐）** - 每个任务调度一个新的子代理，任务间进行审查，快速迭代
   - **必需子技能：** 使用 superpowers:subagent-driven-development

**2. 内联执行** - 在当前会话中使用 executing-plans 执行任务，批量执行并设有检查点
   - **必需子技能：** 使用 superpowers:executing-plans

**另外：** 本计划未在专用 worktree 中创建。开始实现前，若要隔离改动，可用 `using-git-worktrees` 技能新建一个 worktree（仓库已有 `.worktrees/feat/` 目录可复用）。
