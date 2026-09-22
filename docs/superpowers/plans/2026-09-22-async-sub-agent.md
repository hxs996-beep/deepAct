# handoff_to_agent 异步委派实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给 `handoff_to_agent` 加 `async:true` 参数，让主 agent 委派子代理后不阻塞、立即返回 job_id，主 agent 继续其它工作；新增只读 `agent_poll(job_id)` 查询进度/取结果；后台任务随 Run 结束 cancel。

**架构：** `Engine.RunSubAgent`（`handoff.go:103`）检测 `params.Async && depth==0` → 后台 goroutine 跑 `agent.Run`（复用现有同步链路），立即返回 running 状态。Engine 持有 `bgTasks` map（`bgMu` 保护），Run 的 `defer` cancel 全部。`agent_poll` 走 `processAgentPollCalls` 拦截（仿 `processTodoWriteCalls`），每轮把未取结果的后台任务摘要注入 pinned 消息。

**技术栈：** Go（engine/ 包 + tools/ 包），标准库（context/sync/time）。

**规格：** `docs/superpowers/specs/2026-09-22-async-sub-agent-design.md`

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/agent.go` | `HandoffToAgentParams.Async` 字段；`AgentPollToolName` 常量；`agentPollToolSpec(zh)`；`HandoffReasonAsyncRunning` 常量 | 修改 |
| `engine/handoff.go` | `Engine.RunSubAgent` async 分支（goroutine + channel + agent_done 事件） | 修改 |
| `engine/loop.go` | Engine 结构体加 `bgTask`/`bgTasks`/`bgMu`/`bgSeq`；Run 的 defer 加 `cancelBackgroundTasks` | 修改 |
| `engine/turn.go` | `toolSpecsWithHandoff` 追加 `agentPollToolSpec`；`processAgentPollCalls` 拦截；`executeTurn` 每轮注入 pinned 后台任务摘要 | 修改 |
| `tools/subagent.go` | `Spec()` parameters JSON 加 `async` 字段 | 修改 |
| `engine/handoff_test.go` | 测试（复用 `mockAgentForHandoff`） | 修改 |
| `engine/turn_agent_poll_test.go` | `agent_poll` 拦截与 pinned 摘要测试 | 创建 |

**不改**：`sub_agent.go` 的 `runLoop`、`handoff.go` 的 `runHandoff` 同步路径、`/collab`、`/ratd`。

---

### 任务 1：类型与工具定义（编译保持绿，增量）

**文件：**
- 修改：`engine/agent.go`
- 修改：`tools/subagent.go`
- 测试：`engine/handoff_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/handoff_test.go` 末尾追加：

```go
func TestHandoffToAgentParams_AsyncField(t *testing.T) {
	// async 字段必须存在于 handoff spec 的 parameters JSON 中。
	spec := handoffToolSpec(false)
	if !strings.Contains(string(spec.Function.Parameters), `"async"`) {
		t.Fatalf("handoff spec parameters must contain async field, got: %s", spec.Function.Parameters)
	}
}

func TestAgentPollToolSpec_Exists(t *testing.T) {
	spec := agentPollToolSpec(false)
	if spec.Function.Name != AgentPollToolName {
		t.Fatalf("Name = %q, want %q", spec.Function.Name, AgentPollToolName)
	}
	if !strings.Contains(string(spec.Function.Parameters), `"job_id"`) {
		t.Fatalf("agent_poll parameters must contain job_id, got: %s", spec.Function.Parameters)
	}
}

func TestHandoffReasonAsyncRunning_Exists(t *testing.T) {
	if HandoffReasonAsyncRunning == "" {
		t.Fatal("HandoffReasonAsyncRunning must be non-empty")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestHandoffToAgentParams_AsyncField|TestAgentPollToolSpec_Exists|TestHandoffReasonAsyncRunning_Exists' -v`
预期：编译失败——`agentPollToolSpec` 未定义、`AgentPollToolName`/`HandoffReasonAsyncRunning` 未定义。

- [ ] **步骤 3：engine/agent.go 新增类型与工具**

在 `engine/agent.go` 常量块（`agent.go:13-23`）追加：

```go
	AgentPollToolName = "agent_poll"
```

在 `HandoffReason` 常量块（`agent.go:28-39`）追加：

```go
	// HandoffReasonAsyncRunning is the FinishReason on the immediate dispatch
	// result of an async handoff (handoff_to_agent async:true). It is NOT a
	// failure: the sub-agent is still running in the background and the
	// delegating agent should poll it later via agent_poll.
	HandoffReasonAsyncRunning = "async_running"
```

在 `HandoffToAgentParams` 结构体（`agent.go:124-137`）追加字段：

```go
	// Async starts the sub-agent in the background and returns immediately
	// with a job_id; the delegating agent polls it later via agent_poll.
	// Only honored at depth 0 (main agent); nested delegations ignore it.
	Async bool `json:"async,omitempty"`
```

在 `askUserToolSpec` 函数（`agent.go:333`）之后追加：

```go
// agentPollToolSpec returns the tool definition for polling a background
// async sub-agent task. The engine intercepts the call and returns the
// task's current status (running/done/error); a done task delivers its
// result and is removed.
func agentPollToolSpec(zh bool) ModelTool {
	desc := "Query the status and result of a background async sub-agent task (started with handoff_to_agent async:true). Returns running / done / error; a done task returns its result and is removed."
	jobDesc := "The job_id returned by the async handoff dispatch"
	if zh {
		desc = "查询后台异步子代理任务（通过 handoff_to_agent async:true 启动）的状态与结果。返回 running / done / error；done 时返回结果并移除该任务。"
		jobDesc = "异步委派返回的 job_id"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["job_id"]
			}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        AgentPollToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}
```

- [ ] **步骤 4：tools/subagent.go Spec 加 async 字段**

在 `tools/subagent.go` 的 `Spec()` 的 params JSON（`subagent.go:53-68`）中，`"expected_output"` 属性之后追加：

```json
			"async": {"type": "boolean",
				"description": "true = run the sub-agent in the background and return immediately with a job_id; you can continue other work and later query the result with agent_poll(job_id). false/omitted = synchronous wait (default). Prefer async for long-running independent tasks (builds, tests, batch scripts, standalone research)."}
```

修改后的 params 为：

```go
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"agent": {"type": "string", "enum": %s,
				"description": "Target agent (role). Roles are registered agents with a stable persona, default tool set, and optional model/turn config."},
			"goal": {"type": "string", "description": "What the agent should accomplish"},
			"context": {"type": "string", "description": "Relevant context for the sub-agent"},
			"tools": {"type": "array", "items": {"type": "string"},
				"description": "Tools the sub-agent is allowed to use (optional; defaults to the role's tool set)"},
			"constraints": {"type": "array", "items": {"type": "string"},
				"description": "Constraints for the sub-agent (optional)"},
			"expected_output": {"type": "string",
				"description": "What a successful result looks like — acceptance criteria (optional)"},
			"async": {"type": "boolean",
				"description": "true = run the sub-agent in the background and return immediately with a job_id; you can continue other work and later query the result with agent_poll(job_id). false/omitted = synchronous wait (default). Prefer async for long-running independent tasks (builds, tests, batch scripts, standalone research)."}
		},
		"required": ["agent", "goal"]
	}`, enumJSON)
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run 'TestHandoffToAgentParams_AsyncField|TestAgentPollToolSpec_Exists|TestHandoffReasonAsyncRunning_Exists' -v`
预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add engine/agent.go tools/subagent.go engine/handoff_test.go
git commit -m "feat(engine): add async handoff param and agent_poll tool spec"
```

---

### 任务 2：Engine 后台任务表 + Run 结束 cancel

**文件：**
- 修改：`engine/loop.go`
- 测试：`engine/handoff_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/handoff_test.go` 末尾追加：

```go
type blockingAgent struct {
	id     AgentID
	start  chan struct{}
	released chan struct{}
	result *HandoffResult
}

func (m *blockingAgent) ID() AgentID { return m.id }
func (m *blockingAgent) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "blocking mock"}
}
func (m *blockingAgent) Run(ctx context.Context, _ Handoff) (*HandoffResult, error) {
	close(m.start)
	select {
	case <-m.released:
	case <-ctx.Done():
		return &HandoffResult{Summary: "(cancelled)", FinishReason: HandoffReasonCancelled}, ctx.Err()
	}
	return m.result, nil
}

func TestRunSubAgent_AsyncDispatch_RegistersAndRuns(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "后台调研", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("Status = %q, want ok", res.Status)
	}
	if res.FinishReason != HandoffReasonAsyncRunning {
		t.Fatalf("FinishReason = %q, want async_running", res.FinishReason)
	}
	if !strings.Contains(res.Digest, "bg-1") {
		t.Fatalf("Digest = %q, want contain job_id bg-1", res.Digest)
	}
	select {
	case <-a.start:
	case <-time.After(2 * time.Second):
		t.Fatal("background goroutine did not start")
	}
	e.bgMu.Lock()
	n := len(e.bgTasks)
	e.bgMu.Unlock()
	if n != 1 {
		t.Fatalf("bgTasks len = %d, want 1", n)
	}
	close(a.released)
}

func TestCancelBackgroundTasks_OnRunEnd(t *testing.T) {
	a := &blockingAgent{id: AgentSub, start: make(chan struct{}), released: make(chan struct{})}
	reg := NewAgentRegistry()
	reg.Register(a)
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	_, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "后台任务", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	<-a.start // 确保 goroutine 已启动并阻塞

	e.cancelBackgroundTasks()

	e.bgMu.Lock()
	n := len(e.bgTasks)
	e.bgMu.Unlock()
	if n != 0 {
		t.Fatalf("bgTasks len after cancel = %d, want 0", n)
	}
	// blockingAgent.Run 在 ctx.Done 后返回 —— 验证没有 goroutine 泄漏。
	// released 永不关闭，但 ctx cancel 会让 Run 返回。
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestRunSubAgent_AsyncDispatch_RegistersAndRuns|TestCancelBackgroundTasks_OnRunEnd' -v`
预期：编译失败——`initBackgroundTasks`/`cancelBackgroundTasks` 未定义、`bgMu`/`bgTasks` 字段不存在。

- [ ] **步骤 3：loop.go Engine 结构体加字段**

在 `Engine` 结构体（`loop.go:49-130`）中，`askUserMu` 字段（`loop.go:92`）之后追加：

```go
	// Background async sub-agent tasks (handoff_to_agent async:true).
	// bgMu guards bgTasks and bgSeq. Tasks are cancelled at Run exit —
	// they never outlive the Run that started them.
	bgMu    sync.Mutex
	bgSeq   int
	bgTasks map[string]*bgTask
```

- [ ] **步骤 4：loop.go 新增 bgTask 类型 + init/cancel 方法**

在 `Engine` 结构体定义之后（`loop.go:130` 之后）追加：

```go
// bgTask is one background (async) sub-agent run started via
// handoff_to_agent(async:true). It lives only for the current Run(): the
// Run's exit path cancels it and drops any un-polled result.
type bgTask struct {
	id      string            // "bg-<seq>"
	agent   AgentID
	goal    string
	ctx     context.Context
	cancel  context.CancelFunc
	result  chan *HandoffResult // 容量1：完成/错误/等待中
	startAt time.Time
}

// initBackgroundTasks initializes the background task map. Safe to call
// multiple times (idempotent).
func (e *Engine) initBackgroundTasks() {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if e.bgTasks == nil {
		e.bgTasks = make(map[string]*bgTask)
	}
}

// cancelBackgroundTasks cancels every outstanding background task and clears
// the table. Called at the end of every Run() so no sub-agent goroutine or
// LLM request outlives the Run. Un-polled results are dropped (the UI already
// showed their progress via agent_done events).
func (e *Engine) cancelBackgroundTasks() {
	e.bgMu.Lock()
	tasks := e.bgTasks
	e.bgTasks = nil
	e.bgMu.Unlock()
	for id, t := range tasks {
		t.cancel()
		loopLog.Printf("background task %s (%s) cancelled at run end; result dropped", id, t.agent)
	}
}
```

- [ ] **步骤 5：loop.go Run 的 defer 加 cancel**

在 `Run` 方法（`loop.go:213`）的 defer 区，`persistHistory`（`loop.go:219`）之后追加：

```go
	// Background async sub-agent tasks never outlive this Run.
	defer e.cancelBackgroundTasks()
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestRunSubAgent_AsyncDispatch_RegistersAndRuns|TestCancelBackgroundTasks_OnRunEnd' -v`
预期：PASS（`TestRunSubAgent_AsyncDispatch_RegistersAndRuns` 会因 `RunSubAgent` 尚未实现 async 分支而失败——见任务 3；`TestCancelBackgroundTasks_OnRunEnd` 应 PASS）。

> **注：** 若 `TestRunSubAgent_AsyncDispatch_RegistersAndRuns` 此时失败（async 分支未实现），属预期——任务 3 补齐。`TestCancelBackgroundTasks_OnRunEnd` 应通过（它只依赖 init/cancel）。

- [ ] **步骤 7：Commit**

```bash
git add engine/loop.go engine/handoff_test.go
git commit -m "feat(engine): background task table with run-end cancellation"
```

---

### 任务 3：Engine.RunSubAgent async 分支（核心）

**文件：**
- 修改：`engine/handoff.go`
- 测试：`engine/handoff_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/handoff_test.go` 末尾追加：

```go
func TestRunSubAgent_AsyncDispatch_AgentDoneEvent(t *testing.T) {
	a := &mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "后台调研完成", FinishReason: HandoffReasonCompleted,
	}}
	reg := NewAgentRegistry()
	reg.Register(a)
	var events []ProgressEvent
	e := &Engine{
		agents:   reg,
		isChinese: true,
		state:    &TaskState{},
		config:   EngineConfig{OnProgress: func(ev ProgressEvent) { events = append(events, ev) }},
	}
	e.initBackgroundTasks()

	// 同步 agent 立即完成 —— 轮询直到 done。
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "调研", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.FinishReason != HandoffReasonAsyncRunning {
		t.Fatalf("FinishReason = %q, want async_running", res.FinishReason)
	}

	// 后台 goroutine 会很快完成；轮询 bgTasks 直到结果 channel 有值。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.bgMu.Lock()
		tasks := e.bgTasks
		e.bgMu.Unlock()
		if len(tasks) == 0 {
			break // 已被 agent_poll 或 cancel 移除？此处不应发生 —— 测试失败
		}
		for _, t := range tasks {
			select {
			case r := <-t.result:
				if r == nil || r.Summary != "后台调研完成" {
					t.Fatalf("background result = %+v, want 后台调研完成", r)
				}
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 验证 agent_done 事件已发出（异步完成时）。
	foundDone := false
	for _, ev := range events {
		if ev.Type == "agent_done" && strings.Contains(ev.Detail, "后台调研完成") {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected agent_done event with result, got events: %+v", events)
	}
}
```

> **注：** 这个测试直接依赖后台 goroutine 的完成时间（mock agent 立即返回），用轮询等待。如果 mock agent 同步完成太快，`agent_done` 可能在 `RunSubAgent` 返回前已发出——事件顺序无关紧要，只断言最终出现。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestRunSubAgent_AsyncDispatch_AgentDoneEvent -v`
预期：FAIL——`RunSubAgent` 尚未实现 async 分支，`FinishReason` 不会是 `async_running`，事件缺失。

- [ ] **步骤 3：实现 Engine.RunSubAgent async 分支**

在 `engine/handoff.go` 的 `Engine.RunSubAgent`（`handoff.go:103-137`）开头、`res := runHandoff(...)` 之前插入 async 分支：

```go
func (e *Engine) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang string) (ToolResult, error) {
	if e.agents == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	if params.Async && depth == 0 {
		return e.dispatchAsync(ctx, params, userLang)
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	// ...（原有代码不变）
}
```

在 `Engine.RunSubAgent` 之后、`SubAgentRunner.RunSubAgent` 之前追加：

```go
// dispatchAsync starts the sub-agent in the background and returns
// immediately with a job_id. The delegating agent polls the result later via
// agent_poll(job_id). The background task is cancelled at Run exit — it never
// outlives the Run that started it.
func (e *Engine) dispatchAsync(ctx context.Context, params HandoffToAgentParams, userLang string) (ToolResult, error) {
	agent, err := e.agents.Get(AgentID(params.Agent))
	if err != nil {
		return ToolResult{Status: "error", Digest: fmt.Sprintf("agent not found: %s - %v", params.Agent, err)}, nil
	}
	handoff := Handoff{
		Agent:          AgentID(params.Agent),
		Goal:           params.Goal,
		Context:        params.Context,
		Tools:          params.Tools,
		Constraints:    params.Constraints,
		ExpectedOutput: params.ExpectedOutput,
		Persona:        params.Persona,
		Depth:          0,
		UserLanguage:   userLang,
	}
	if e.state != nil {
		handoff.Context = injectMainAgentContext(handoff.Context, e.state)
	}

	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgSeq++
	jobID := fmt.Sprintf("bg-%d", e.bgSeq)
	bgCtx, cancel := context.WithCancel(ctx)
	task := &bgTask{
		id:      jobID,
		agent:   AgentID(params.Agent),
		goal:    params.Goal,
		ctx:     bgCtx,
		cancel:  cancel,
		result:  make(chan *HandoffResult, 1),
		startAt: time.Now(),
	}
	e.bgTasks[jobID] = task
	e.bgMu.Unlock()

	// agent_start event for UI (same as synchronous handoff).
	if e.config.OnProgress != nil {
		name := params.Agent
		if name == "" {
			name = "sub"
		}
		e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: name, Detail: params.Goal})
	}

	go func() {
		result, runErr := agent.Run(bgCtx, handoff)
		if runErr != nil && result == nil {
			result = &HandoffResult{
				Summary:      "(sub-agent error: " + runErr.Error() + ")",
				Blocked:      true,
				BlockedBy:    "sub_agent_error",
				FinishReason: HandoffReasonError,
			}
		}
		if result.Usage != nil {
			e.accumulateUsage(result.Usage)
		}
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

	return ToolResult{
		ToolCallID:   "",
		ToolName:     HandoffToolName,
		Status:       "ok",
		Digest:       fmt.Sprintf("Dispatched async job %s (%s): %s. Use agent_poll(%s) to check the result.", jobID, params.Agent, params.Goal, jobID),
		FinishReason: HandoffReasonAsyncRunning,
	}, nil
}
```

> **注意：** `handoff.go` 已 import `context`/`fmt`/`strings`；`time` 需新增。检查 `handoff.go` 顶部 import，若缺 `time` 则补 `"time"`。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestRunSubAgent_Async|TestCancelBackgroundTasks|TestRunSubAgent_EngineBackend' -v`
预期：全部 PASS（async 分支已实现；同步后端测试不受影响）。

- [ ] **步骤 5：Commit**

```bash
git add engine/handoff.go engine/handoff_test.go
git commit -m "feat(engine): async handoff dispatch with background sub-agent"
```

---

### 任务 4：agent_poll 拦截 + 每轮 pinned 后台任务摘要

**文件：**
- 修改：`engine/turn.go`
- 创建：`engine/turn_agent_poll_test.go`
- 修改：`engine/handoff_test.go`（或并入 turn_agent_poll_test.go）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/turn_agent_poll_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

func TestAgentPollToolSpec_Registered(t *testing.T) {
	specs := (&Engine{}).toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == AgentPollToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("toolSpecsWithHandoff must include agent_poll")
	}
}

func TestProcessAgentPollCalls_StatusFlow(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "结果A", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()

	// 同步 agent 立即完成 —— dispatch 后结果马上可用。
	_, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "g", Async: true,
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	var jobID string
	e.bgMu.Lock()
	for id := range e.bgTasks {
		jobID = id
	}
	e.bgMu.Unlock()
	if jobID == "" {
		t.Fatal("no background task registered")
	}

	// 等待后台完成（mock 同步返回，结果应立即可读）。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		e.bgMu.Lock()
		t := e.bgTasks[jobID]
		e.bgMu.Unlock()
		select {
		case r := <-t.result:
			if r != nil {
				goto done
			}
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background result never arrived")
done:

	// agent_poll 应返回 done 并移除任务。
	msgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc1", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": jobID}),
	}})
	if len(msgs) != 1 {
		t.Fatalf("processAgentPollCalls returned %d messages, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "done") || !strings.Contains(msgs[0].Content, "结果A") {
		t.Errorf("poll done message = %q, want contain done + 结果A", msgs[0].Content)
	}
	e.bgMu.Lock()
	_, stillThere := e.bgTasks[jobID]
	e.bgMu.Unlock()
	if stillThere {
		t.Error("job should be removed after done poll")
	}
}

func TestProcessAgentPollCalls_NotFound(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	msgs := e.processAgentPollCalls([]ToolCallRequest{{
		ID: "tc1", Name: AgentPollToolName, Input: mustJSON(map[string]string{"job_id": "bg-999"}),
	}})
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "not found") {
		t.Errorf("not-found message = %q, want contain 'not found'", msgs[0].Content)
	}
}

func TestBackgroundJobsPinnedInjection(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{id: "bg-1", agent: "researcher", goal: "调研缓存"}
	e.bgMu.Unlock()

	e.pendingPinnedMessages = nil
	e.injectBackgroundJobsSummary()
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("pinned messages = %d, want 1", len(e.pendingPinnedMessages))
	}
	if !strings.Contains(e.pendingPinnedMessages[0], "bg-1") || !strings.Contains(e.pendingPinnedMessages[0], "调研缓存") {
		t.Errorf("pinned summary = %q, want contain bg-1 + goal", e.pendingPinnedMessages[0])
	}
}
```

> **注意：** `TestProcessAgentPollCalls_StatusFlow` 使用 `time`——`turn_agent_poll_test.go` 需 import `"time"`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestAgentPollToolSpec_Registered|TestProcessAgentPollCalls|TestBackgroundJobsPinnedInjection' -v`
预期：编译失败——`processAgentPollCalls`/`injectBackgroundJobsSummary` 未定义。

- [ ] **步骤 3：turn.go toolSpecsWithHandoff 追加 agent_poll**

在 `toolSpecsWithHandoff`（`turn.go:862-870`）末尾追加：

```go
	specs = append(specs, agentPollToolSpec(e.isChinese))
```

- [ ] **步骤 4：turn.go 新增 processAgentPollCalls**

在 `processTodoWriteCalls`（`turn.go:1465`）之后追加：

```go
// processAgentPollCalls intercepts agent_poll tool calls from the assistant's
// response. Each call queries one background async sub-agent task by job_id.
// A running task returns "running"; a done task returns its result and is
// removed from the table; an unknown job_id returns an error. Every call
// receives a tool response (satisfying the DeepSeek API requirement that
// every tool_call_id has a matching tool response).
func (e *Engine) processAgentPollCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != AgentPollToolName {
			continue
		}
		var params struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: agent_poll requires a non-empty job_id",
				Timestamp:  time.Now(),
			})
			continue
		}
		e.initBackgroundTasks()
		e.bgMu.Lock()
		task, ok := e.bgTasks[params.JobID]
		if !ok {
			e.bgMu.Unlock()
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: agent_poll job %q not found (already consumed or run ended).", params.JobID),
				Timestamp:  time.Now(),
			})
			continue
		}
		e.bgMu.Unlock()

		select {
		case result := <-task.result:
			// Done: remove from table and return the result.
			e.bgMu.Lock()
			delete(e.bgTasks, params.JobID)
			e.bgMu.Unlock()
			if result == nil {
				result = &HandoffResult{Summary: "(no result)"}
			}
			content := fmt.Sprintf("Job %s done.\n%s", params.JobID, formatHandoffResult(result, e.isChinese))
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    content,
				Timestamp:  time.Now(),
			})
		default:
			// Still running.
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Job %s (%s) is still running. Goal: %s. Poll again later.", params.JobID, task.agent, task.goal),
				Timestamp:  time.Now(),
			})
		}
	}
	return msgs
}
```

- [ ] **步骤 5：turn.go 新增 injectBackgroundJobsSummary + 每轮注入**

在 `processAgentPollCalls` 之后追加：

```go
// injectBackgroundJobsSummary appends a pinned message listing outstanding
// background async sub-agent tasks so the model knows it has jobs running and
// can continue working while remembering to poll them. No-op when none exist.
// The pinned message is consumed next turn (pendingPinnedMessages semantics)
// and never persists into history.
func (e *Engine) injectBackgroundJobsSummary() {
	e.bgMu.Lock()
	n := len(e.bgTasks)
	if n == 0 {
		e.bgMu.Unlock()
		return
	}
	var b strings.Builder
	b.WriteString("[Background jobs] ")
	first := true
	for id, t := range e.bgTasks {
		if !first {
			b.WriteString("; ")
		}
		first = false
		fmt.Fprintf(&b, "%s (%s): %s — running", id, t.agent, t.goal)
	}
	e.bgMu.Unlock()
	b.WriteString(". Continue your work; use agent_poll(job_id) to fetch results.")
	e.pendingPinnedMessages = append(e.pendingPinnedMessages, b.String())
}
```

在 `executeTurn` 的 pinned 消息注入点（`turn.go:85-88`，`for _, pm := range e.pendingPinnedMessages` 之前）追加：

```go
	// Remind the model about outstanding background async sub-agent tasks.
	e.injectBackgroundJobsSummary()
```

> **注意：** 注入点必须在 `for _, pm := range e.pendingPinnedMessages` 之前，这样后台任务摘要作为本轮 pinned 消息被发送给模型。

- [ ] **步骤 6：turn.go 的 executeTurn 拦截 agent_poll 调用**

在 `executeTurn` 中，与 `pendingAskUserMsgs` 并列处（`turn.go:589`）追加：

```go
	pendingPollMsgs := e.processAgentPollCalls(calls)
```

并在 `e.history = append(e.history, assistant)` 之后、与其它 pending 消息并列处（`turn.go:604` 之后）追加：

```go
	for _, msg := range pendingPollMsgs {
		e.history = append(e.history, msg)
	}
```

同时确保 `agent_poll` 不进 `regularCalls`：在 handoff 分离处（`turn.go:615-629`）的 else-if 链中追加：

```go
		} else if call.Name == AgentPollToolName {
			continue
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./engine/ -run 'TestAgentPollToolSpec_Registered|TestProcessAgentPollCalls|TestBackgroundJobsPinnedInjection' -v`
预期：全部 PASS。

再运行全量回归：`go build ./... && go test ./engine/ -short`
预期：编译通过、无回归（`agent_poll` 不进 regularCalls 不影响既有 handoff/load_skill/todo 路径）。

- [ ] **步骤 8：Commit**

```bash
git add engine/turn.go engine/turn_agent_poll_test.go
git commit -m "feat(engine): agent_poll interception and background job summary"
```

---

### 任务 5：全量构建、测试、UI 面板断言

**文件：**
- 修改：`ui/model_test.go`（可选断言）
- 验证：`go build ./... && go test ./...`

- [ ] **步骤 1：UI 面板断言（现有机制复用，补一个集成断言）**

在 `ui/model_test.go` 末尾追加（`TestAgentDoneUpdatesSubAgentPanel` 验证异步完成时 `agent_done` 事件仍更新面板——现有逻辑已覆盖，此测试锁定回归）：

```go
func TestAgentDoneUpdatesSubAgentPanel(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateRunning
	m.width = 80
	m.height = 40
	// agent_start 追加子代理
	m.Update(ProgressMsg{Type: "agent_start", Name: "researcher", Detail: "调研缓存方案"})
	// agent_done 标记完成
	m.Update(ProgressMsg{Type: "agent_done", Name: "researcher", Detail: "调研完成"})
	if len(m.subAgents) != 1 {
		t.Fatalf("subAgents len = %d, want 1", len(m.subAgents))
	}
	if m.subAgents[0].Status != "done" {
		t.Errorf("subAgent status = %q, want done", m.subAgents[0].Status)
	}
	if !strings.Contains(m.subAgents[0].Summary, "调研完成") {
		t.Errorf("subAgent summary = %q, want contain 调研完成", m.subAgents[0].Summary)
	}
}
```

- [ ] **步骤 2：运行全量构建与测试**

运行：`go build ./...`
预期：编译通过。

运行：`go test ./... 2>&1 | tail -20`
预期：全部 PASS。

运行：`go test -race ./engine/ -run 'TestRunSubAgent_Async|TestProcessAgentPollCalls|TestCancelBackgroundTasks'`
预期：无数据竞争（`bgMu` 保护 bgTasks；`accumulateUsage` 有 `usageMu`）。

- [ ] **步骤 3：Commit**

```bash
git add ui/model_test.go
git commit -m "test(ui): assert agent_done updates sub-agent panel"
```

---

## 验收标准

- [ ] `handoff_to_agent` parameters 含 `async` 字段（中英双语描述）
- [ ] `HandoffToAgentParams.Async` 字段存在，`depth==0` 时生效
- [ ] `async:true` 委派立即返回 `{job_id, status:"running"}`（`FinishReason=async_running`），主 agent turn 不阻塞
- [ ] 后台 goroutine 复用 `agent.Run`，完成时 emit `agent_done`（UI 面板更新）
- [ ] `agent_poll(job_id)`：running / done（返回结果并移除）/ not found（error message）
- [ ] `agent_poll` 不进 regularCalls，不产生重复 tool message
- [ ] 每轮 pinned 注入后台任务摘要（无任务时不注入）
- [ ] Run 结束 `cancelBackgroundTasks` 取消全部后台任务，无 goroutine 泄漏
- [ ] 同步语义不变：不传 async 的 handoff 行为与现状完全一致；`/collab`、`/ratd` 不受影响
- [ ] `go build ./...`、`go test ./...` 全部通过；`-race` 无数据竞争

## 风险与回退

- **模型误用 async**：大量后台任务并发消耗 token。缓解：tool spec 强调"仅长时间独立任务"；模型需 poll 汇总；Run 结束强制 cancel 兜底。
- **结果丢失**：Run 结束未 poll 丢弃。缓解：pinned 摘要提醒；`/collab` 同步语义不受影响。
- **steer 与后台任务竞争**：模型自主决定"继续 poll"还是"响应新指令"——能力自主，符合项目原则。
- **并发安全**：`bgTasks` 用 `bgMu`；usage 用 `usageMu`；`model.Fork()` 独立 client——`-race` 验证。
