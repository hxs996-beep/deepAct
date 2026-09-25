# 子代理挂起-恢复（suspend / resume）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**版本：** v2 · 已吸收两轮外部评审（B1–B4、N1–N3 + 5 条实施细节；见文末"本轮评审修正记录"）。**§设计要点 + §决策记录** 是本计划的自包含依据，评审可直接对照，无需另开文档。

**目标：** 子代理调用 `ask_user` 时不再焚毁自身上下文：把它挂起为 job 表中 `awaiting_user` 状态的一条；用户回答后由模型调用 `agent_resume` 把回答喂回**同一个 run**，从其断点续跑（复用原 history / 原前缀缓存分区 / 原预算）。范围 = 进程内跨 Run（A-lite），不做跨会话持久化。

**架构：** 复用既有 async job 设施（`bgTasks` / `agent_poll` / pinned 摘要），只新增：① `HandoffResult.Suspended` 内部字段把断点状态带回引擎层；② `bgTask` 的一个状态与**一条与现状相反的生命周期规则**（Run 结束时保留 `awaiting_user` 条目、清掉 `running` 条目）；③ 新工具 `agent_resume(run_id, answer)`（非阻塞，结果仍由 `agent_poll` 取）。回填遵循一条硬约束：**待回填的 tool 响应必须紧邻其 `assistant(tool_calls)`** —— 占位存在则替换其内容，占位缺失（嵌套冒泡路径）则在末尾追加。

**技术栈：** Go 标准库；无新依赖；无持久化格式变更。

---

## 设计要点（自包含，评审可直接对照）

> 本计划修正了设计草稿 v1 的四处已核实缺陷（B1–B4），以下是**修正后**的口径。

**不变量**

| # | 不变量 | 依据 |
|---|---|---|
| I1 | 回填**键控 `PendingToolID`**：`PendingToolMissing == true` → 在末尾**追加**一条 tool 消息；`false` → **替换**占位 tool 消息的内容。禁止在 `assistant(tool_calls)` 与其 tool 响应之间插入任何消息。**不得按"末尾那条 tool 消息"定位** | 嵌套冒泡在写 tool 响应**之前** return，故该调用没有响应；同一条 assistant 可带多个 tool_call（`[read, handoff_to_agent]`），此时末尾那条是 `read` 的响应 —— 按位置替换会打错人 |
| I2 | 恢复复用原 partition 与已 Fork 的 model client | 否则整段 history 重新 cache-miss，功能意义消失 |
| I3 | 恢复沿用同一 `Spent`（同一 run 的预算）；**迭代计数 `Iter` 与 nudge 一次性标志一并携带**（见任务 2 状态清单） | 顺带解决"问答重置预算"；否则恢复后被 capped 的 agent 白拿一轮迭代、80% 提示重复注入 |
| I4 | Run 结束**不**清 `awaiting_user` 条目；`running` 条目照旧清并 cancel | 前者是"欠用户一个回答"的债；后者是本 Run 的产物 |
| I5 | 挂起与在跑**分开计数**、各自 fail-loud；一切放弃都 fail-loud，绝不静默丢弃 | 挂起条目不占 LLM 在途，不能被 async 未清账上限误判为满容 |
| I6 | 注册在**每一层各自发生**（`runHandoff` 内），不延迟到 depth 0 | `SubAgentRunner` 无 Engine 引用，必须在 runHandoff 的 `handoffOptions` 上注入 registrar |
| I7 | 嵌套时**若子级注册失败（handle 为空）→ 父级也不挂起**，退回现状语义（父级照旧 `awaiting_user` 返回、问题冒泡、由父代理重新委派） | 否则父级的 `ChildRunID` 悬空，恢复时无子结果可回填（I5 fail-loud 的传播） |
| I8 | **对外只暴露顶层句柄、resume 落点是叶子**（H1）：模型可见的 handle 只有 `depth==0` 那次委派拿到的那个；`agent_resume(顶层, answer)` 由引擎沿 `ChildRunID` 下钻到 `ChildRunID==""` 的叶子执行回填与续跑，最终结果投递回顶层句柄。**更深层的 handle 一律不渲进任何模型可见面**（机制：handle 只在 `opts.depth==0` 的 `runHandoff` 里渲进 digest） | `awaiting_user` 同时表示"等用户"（叶子，`PendingToolMissing==false`）与"等子级结果"（中间层，`true`，见 `sub_agent.go:738-747` 早于写 tool 响应 return）。若允许对中间层 resume，用户回答会被当成 handoff 的 tool 响应写进 history —— 语义彻底错 |
| I9 | 挂起条目的 **`result` channel 在登记时创建**（H2） | `make(chan *HandoffResult, 1)` 全仓只在 `dispatchAsync`（`handoff.go:205`）创建；同步挂起路径不经那里。nil channel 在 `select{…, default:}` 里恒走 default → resume 后的最终结果**静默丢弃**、`agent_poll`（`turn.go:1466`）永远报 "still running" |

**生命周期边界（不新增"跨 Run 运行"语义）**：挂起态**本身**跨 Run 存活（它没有在途工作）；但一旦被 resume 变回 `running`，它就与既有 async job 完全同生命周期——**随调用它的那个 Run 结束而 cancel、结果丢弃**（`cancelBackgroundTasks` 既有语义）。恢复存放的 `ctx/cancel` 因此**在 resume 时**从当轮 Run 取，不在挂起时预置。

**I5 归入既有"满容行为三分通则"**（`docs/superpowers/specs/2026-09-24-subagent-resource-guards-design.md` §2）：两个放弃点——挂起表满 → 新提问不挂起；TTL 过期 → `agent_resume` 报错——**都属"语义要求立即返回 → fail-loud"**，不自定义第五类行为。

**与 2026-09-24 只读宇宙 spec §2.3 的关系**：该 spec:17 曾以"气泡使子代理以 `awaiting_user` 终止、history 焚毁"为由否决"危险命令任意深度人审"方案。本机制**只消除其中一条理由**，其余三条（只护 bash 不护 write/edit；`pendingAskUser` 单槽覆盖——本计划顺带修；工程量逼近 dsh 四层守卫）仍成立，且只读宇宙（`6edb5fc`）已从根本替代该方案。**该否决继续有效，不得借本机制重启。**

**级联为何由引擎驱动（对照 AGENTS.md 能力优先）**：中间层此刻**处于挂起、没有模型在跑**，不存在"由模型决定"的主体；且子代理**不应**获得 `agent_resume`（它没有用户通道，否则能把子代挂起来无限等）。故级联是引擎的确定性缝合，不是行为猜测。

**N1 方案：选 (b) registrar 注入，不选 (a) 回传完整 `SuspendedRun`**

事实：depth≥1 的挂起发生在 `tools/subagent.go` 的 nested 分支 → `SubAgentRunner.RunSubAgent` → 自由函数 `runHandoff`；`SubAgentRunner` **没有** `*Engine` 引用，`handoffOptions` 也没有登记回调。因此只把 `RunID` 穿线不够——没人给嵌套层分配 handle。

| | (a) `ToolResult.Suspended` 回传整条链 | **(b) registrar 注入（采纳）** |
|---|---|---|
| 穿线 | 要把含 `History`/`ModelClient` 的胖结构塞进 `ToolResult` + `ToolResultEnvelope` + 两处拷贝（`ToolResult` 还带 json tag） | 只需既定的 `RunID` 一个字符串字段 |
| 登记时机 | 延迟到 depth 0 一次性递归登记整条链 → **嵌套层渲染 digest 时还没有 handle**，模型无法引用子级 | 每层在自己的 `runHandoff` 内即时登记 → 每层 digest 都带自己的 handle |
| 装配 | 无 | `handoffOptions.register` + `SubAgentRunner.register` + `SetSuspendedRegistrar`（cmd 装配 `e.registerSuspended`） |

(b) 的落点：`runHandoff` 在 `result.Suspended != nil` 时调 `opts.register(...)`，**必须早于** `Digest: formatHandoffResult(...)`（`handoff.go:97`）——这样 handle 才进得了 digest，也顺带解决"登记 → 格式化"的先后问题。`register` 为 nil（裸 runner / 测试）时不登记 → handle 为空 → 按 I7 父级也不挂起 → 行为退回今天，安全默认。

---

## 决策记录（已拍板，评审无需重开）

| # | 决策 | 依据 |
|---|---|---|
| **D1** | 回填通道 = 新工具 `agent_resume(run_id, answer)`；**不**让引擎自动认领"下一条用户消息" | 用户完全可能换话题，自动认领会误绑；显式通道 = AGENTS.md 能力优先（不写死猜测逻辑） |
| **D2** | 嵌套链 = **链式级联**：每层记 `childRunID`，子级完成后把结果回填父级挂起 history 并恢复父级，递归向上；**不做**"只恢复叶子、放弃中间层" | 只恢复叶子会丢掉中间层自己的发现，恰是本机制要消除的那类损失 |
| **D3** | `maxSuspendedSubAgents` 默认 **4**；TTL **30 分钟**（**惰性判定**，无后台 timer） | 每条挂起持有整段 history（最坏 ~1M token，压缩阈值 95% 才触发），必须有界；惰性避免新增 goroutine |
| **D4** | 恢复交付 = **非阻塞**：resume 后 run 在后台继续，结果由 `agent_poll(run_id)` 取 | 阻塞会冻结父回合 |
| **D5** | 恢复后的 run 与既有 async job **同生命周期**：随调用它的那个 Run 结束而 cancel、结果丢弃 | 不新增"跨 Run 运行"语义（见"设计要点 · 生命周期边界"） |
| **D6** | 范围 = **A-lite**：进程内**跨 Run**、**不跨会话** | 跨会话需落盘，且直接撞上 session-resume 的"剥离工具链"约束（另案） |
| **D7** | N1 采用 **(b) registrar 注入**，不回传完整 `SuspendedRun` | 避免胖结构穿线；且保证每一层的 digest 都能带上自己的 handle |
| **D8** | 挂起条目沿用 **`bg-N`** 命名空间，与在跑计数**分离** | 统一到同一张 job 表；挂起不占 LLM 在途名额（I5） |
| **D9** | `agent_resume` **不给子代理**（仅主代理可见） | 子代理没有用户通道；否则它能把自己的子代挂起来无限等 |
| **D10** | 挂起表满 / TTL 过期一律 **fail-loud**，归入既有"满容行为三分通则"第 2 类；不自定义第五类 | `docs/superpowers/specs/2026-09-24-subagent-resource-guards-design.md` §2 |
| **D11** | **不重开** readonly-universe spec:17 否决过的"危险命令任意深度人审"方案 | 该否决的其余三条理由仍成立（见"设计要点"） |
| **D12** | `max_suspended_subagents` 按 A3 同规格落 `[context]`（默认 4）；TTL **只在被读取时判定**（poll/resume/摘要），**不是时间保证**；条目被淘汰时用 **pinned 渲染一次"已过期，不可恢复"**（仅曾以 async 露面者补 `agent_done`） | 与资源护栏"满容行为三分通则"一致；避免 UI 面板整会话停在未完成态 |
| **D13** | 模型可见的 handle **只有顶层**（`depth==0` 那次委派）；resume 落点是叶子（I8） | `awaiting_user` 混淆了"等用户"与"等子级"，若对中间层 resume 会把用户回答写进 handoff 的 tool 位 |

> 本节即 v2 spec 的决策记录表来源：任务 6 落库时**原样复制**，不要另写一份以免漂移。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `engine/loop.go`（改） | `bgTask.state`/`suspended`/`childRunID`（父级靠它反查，不设 `parentRunID`）；`cancelBackgroundTasks` 只清 `running`；`maxSuspendedSubAgents` 与在跑计数分离 |
| `engine/agent.go`（改） | `SuspendedRun` 类型；`HandoffResult.Suspended` 内部字段；`AgentResumeToolName` + `agentResumeToolSpec`；`handoffReasonHeading` 追加 handle；`buildHandoffFollowUp` 按 reason 分支 |
| `engine/sub_agent.go`（改） | `runLoop` 在两条 `awaiting_user` 返回处捕获断点状态；新增 `fillPendingToolResponse`（I1 的唯一实现点）与 `runLoopResume` 入口；`SubAgentRunner.register` 字段 + `SetSuspendedRegistrar`（N1(b)） |
| `engine/types.go`（改） | `ToolResult.RunID`（内部穿线，供父级建立父子关联）；`AskUserRequest.RunID`（问题 ↔ job handle 绑定） |
| `tools/registry.go` / `tools/subagent.go` / `tools/adapter.go`（改） | `ToolResultEnvelope.RunID` 与其两处逐字段拷贝（与现有 `Questions` 穿线完全同构，共 3 处） |
| `engine/handoff.go`（改） | `handoffOptions.register`（N1(b)）+ `runHandoff` 在**格式化 digest 之前**即时登记；`dispatchAsync` 满容判定改 `countByStateLocked(bgStateRunning)`，且挂起时**不向 `task.result` 写、不发 `agent_done`**（N2） |
| `engine/turn.go`（改） | `agent_resume` spec 挂载 + 分类分支 + `processAgentResumeCalls`；`pendingAskUser` 多槽 + `RunID`；`askUserOptions` 与调用点取队首；`/confirm` 数字映射带 RunID；三处清空语义改"只消费队首"；`agent_poll` 与 pinned 摘要按 state 分支 |
| `cmd/run.go`（改） | 注册 `agent_resume`；装配 `runner.SetSuspendedRegistrar(e.registerSuspended)` + `e.maxSuspendedSubAgents`（读 `[context].max_suspended_subagents`，默认 4；均在 `deps.AfterEngine` 内） |
| `config/config.go`（改） | 新增 `[context].max_suspended_subagents`（`<0` 启动报错；`0` = 默认 4），与 `max_outstanding_async_subagents` 同规格 |
| 测试 | `engine/bg_suspend_lifecycle_test.go`（新）、`engine/sub_agent_suspended_test.go`（新）、`engine/agent_resume_test.go`（新）、`engine/sub_agent_askuser_test.go`（改） |

---

### 任务 1：loop.go — 挂起条目生命周期与双计数（行为对 `running` 不变）

**文件：**
- 修改：`engine/loop.go`（`bgTask` 结构、`initBackgroundTasks`、`cancelBackgroundTasks`、`Engine` 字段）
- 测试：创建 `engine/bg_suspend_lifecycle_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/bg_suspend_lifecycle_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// TestSuspendedEntrySurvivesRunEnd: I4 —— Run 结束时 running 条目被清并 cancel，
// awaiting_user 条目必须留下（它是"欠用户一个回答"的债）。
func TestSuspendedEntrySurvivesRunEnd(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()

	runningCtx, runningCancel := context.WithCancel(context.Background())
	suspendedCtx, suspendedCancel := context.WithCancel(context.Background())
	defer suspendedCancel()

	e.bgMu.Lock()
	e.bgTasks["bg-1"] = &bgTask{id: "bg-1", state: bgStateRunning, ctx: runningCtx, cancel: runningCancel}
	e.bgTasks["bg-2"] = &bgTask{id: "bg-2", state: bgStateAwaitingUser, ctx: suspendedCtx, cancel: suspendedCancel,
		suspended: &SuspendedRun{Input: Handoff{Goal: "g"}}}
	e.bgMu.Unlock()

	e.cancelBackgroundTasks()

	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if _, ok := e.bgTasks["bg-2"]; !ok {
		t.Fatal("awaiting_user entry must survive Run end")
	}
	if _, ok := e.bgTasks["bg-1"]; ok {
		t.Error("running entry must be dropped at Run end")
	}
	select {
	case <-runningCtx.Done():
	default:
		t.Error("running entry must be cancelled")
	}
	select {
	case <-suspendedCtx.Done():
		t.Error("awaiting_user entry must NOT be cancelled — it has no in-flight work")
	default:
	}
}

// TestSuspendedCountSeparateFromInFlight: I5 —— 挂起条目不占用 async 未清账名额。
func TestSuspendedCountSeparateFromInFlight(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.initBackgroundTasks()
	e.bgMu.Lock()
	for i := 1; i <= 3; i++ {
		e.bgTasks[fmt.Sprintf("bg-%d", i)] = &bgTask{id: "x", state: bgStateAwaitingUser}
	}
	e.bgMu.Unlock()
	if n := e.inFlightCount(); n != 0 {
		t.Errorf("inFlightCount() = %d, want 0 (suspended entries hold no LLM slot)", n)
	}
	if n := e.suspendedCount(); n != 3 {
		t.Errorf("suspendedCount() = %d, want 3", n)
	}
}
```

顶部补 import `"fmt"`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestSuspendedEntrySurvivesRunEnd|TestSuspendedCountSeparateFromInFlight' -v`
预期：FAIL（编译错误——`bgStateRunning`/`bgStateAwaitingUser`/`CancelBackgroundTasks` 语义未实现、`SuspendedRun` 未定义）

- [ ] **步骤 3：编写实现代码**

`engine/loop.go` 的 `bgTask` 增加字段与状态常量：

```go
// Job states. A job is either running (a goroutine + an LLM request in flight)
// or suspended on ask_user (no goroutine, no in-flight work — it is waiting
// for the user's answer). Suspended jobs are the only entries that survive the
// end of a Run: dropping them would lose the question the user still owes.
const (
	bgStateRunning      = "running"
	bgStateAwaitingUser = "awaiting_user"
)

type bgTask struct {
	// ...既有字段保持不变...
	state      string // bgStateRunning | bgStateAwaitingUser
	suspended  *SuspendedRun
	childRunID string // set when this suspension was caused by a child's question
}
```

> 既有 `bgTask` 字面量（`handoff.go` 内构造点）必须补 `state: bgStateRunning`；测试里的构造点同。

`initBackgroundTasks` **保持原样**（它今天已经只是惰性建表、不清内容；真正整体置 nil 的是 `cancelBackgroundTasks`）——只加一行"不得清空"的注释，防止后人顺手改成清空：

```go
// initBackgroundTasks lazily creates the table. Safe to call multiple times.
// It must NOT clear entries: suspended jobs outlive the Run that created them.
```

`cancelBackgroundTasks` 改为只清 `running`：

```go
// cancelBackgroundTasks drops the Run's in-flight background work. Called at
// the end of every Run so no goroutine or LLM request outlives it. Suspended
// (awaiting_user) entries are KEPT: they hold no goroutine and no in-flight
// request — they are a question the user still owes an answer to, and the
// answer arrives in a later Run. Dropping them would silently lose it.
func (e *Engine) cancelBackgroundTasks() {
	e.bgMu.Lock()
	var running []*bgTask
	for id, t := range e.bgTasks {
		if t.state == bgStateAwaitingUser {
			continue
		}
		running = append(running, t)
		delete(e.bgTasks, id)
	}
	e.bgMu.Unlock()
	for _, t := range running {
		if t.cancel != nil {
			t.cancel()
		}
		loopLog.Printf("background task %s (%s) cancelled at run end; result dropped", t.id, t.agent)
	}
}
```

新增计数器：一个**持锁核心** + 两个自锁包装（登记路径本身已持 `bgMu`，若在那里调用自锁包装会死锁）：

```go
// countByStateLocked counts entries in one state. Callers must hold bgMu.
func (e *Engine) countByStateLocked(state string) int {
	n := 0
	for _, t := range e.bgTasks {
		if t.state == state {
			n++
		}
	}
	return n
}

// inFlightCount counts jobs with work in flight (async outstanding). Suspended
// entries are excluded — they hold no goroutine and no LLM request, so counting
// them would make a waiting run consume an async slot (I5).
func (e *Engine) inFlightCount() int {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	return e.countByStateLocked(bgStateRunning)
}

// suspendedCount counts jobs waiting for the user's answer.
func (e *Engine) suspendedCount() int {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	return e.countByStateLocked(bgStateAwaitingUser)
}
```

`Engine` 增加挂起上限字段（装配见任务 5）：

```go
	// maxSuspendedSubAgents caps how many suspended (awaiting_user) jobs are
	// kept. 0 = default 4. Bounded because each entry retains a whole history.
	maxSuspendedSubAgents int
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestSuspendedEntrySurvivesRunEnd|TestSuspendedCountSeparateFromInFlight' -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归**

运行：`go test ./engine/`
预期：全部 PASS（既有 async 测试不受影响：它们只构造 `running` 条目）

- [ ] **步骤 6：Commit**

```bash
git add engine/loop.go engine/bg_suspend_lifecycle_test.go
git commit -m "feat(engine): job table survives Run end for suspended sub-agents"
```

---

### 任务 2：断点状态捕获与回填（I1 的唯一实现点）

**文件：**
- 修改：`engine/agent.go`（`SuspendedRun` 类型 + `HandoffResult.Suspended` 字段）
- 修改：`engine/sub_agent.go`（两条 `awaiting_user` 返回处填充；新增 `fillPendingToolResponse`）
- 测试：创建 `engine/sub_agent_suspended_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/sub_agent_suspended_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

// TestSuspendedCapturedOnLeafAskUser: 叶子路径终止前已写入占位 tool 消息，
// 断点状态须记录它的 tool_call_id 且标记"占位存在"（回填 = 替换内容）。
func TestSuspendedCapturedOnLeafAskUser(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "已读 store.go。",
			ToolCalls: []ModelToolCall{{ID: "r1", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"store.go"}`}}}},
			FinishReason: "tool_calls"},
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？"}`}},
		}}},
	}}
	runner := &SubAgentRunner{model: model, tools: &recordingToolExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5, UserLanguage: "中文"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil {
		t.Fatal("awaiting_user result must carry Suspended state")
	}
	if s.PendingToolID != "ask1" {
		t.Errorf("PendingToolID = %q, want ask1", s.PendingToolID)
	}
	if s.PendingToolMissing {
		t.Error("leaf path already wrote the placeholder tool response; PendingToolMissing must be false")
	}
	// I1 回填：替换占位内容，history 长度不变、末尾仍是 tool 消息。
	before := len(s.History)
	filled := fillPendingToolResponse(s, "Redis")
	if len(filled) != before {
		t.Fatalf("leaf fill must replace, not append: len %d -> %d", before, len(filled))
	}
	last := filled[len(filled)-1]
	if last.Role != "tool" || last.ToolCallID != "ask1" || last.Content != "Redis" {
		t.Errorf("filled tail = %+v, want tool ask1 with the answer", last)
	}
}

// TestSuspendedCapturedOnNestedBubble: 嵌套冒泡在写 tool 响应之前 return，
// 断点 history 末尾是孤儿 assistant(tool_calls) —— 回填必须"追加"（I1）。
func TestSuspendedCapturedOnNestedBubble(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"子任务"}`}},
		}}},
	}}
	exec := &questionExecutor{questions: []string{"数据库连接串是什么？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test", registry: NewAgentRegistry()}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil {
		t.Fatal("nested awaiting_user result must carry Suspended state")
	}
	if !s.PendingToolMissing {
		t.Error("nested bubble returns BEFORE writing the tool response; PendingToolMissing must be true")
	}
	if s.PendingToolID != "h1" {
		t.Errorf("PendingToolID = %q, want h1 (the failed-to-respond handoff call)", s.PendingToolID)
	}
	before := len(s.History)
	filled := fillPendingToolResponse(s, "postgres://…")
	if len(filled) != before+1 {
		t.Fatalf("nested fill must append: len %d -> %d", before, len(filled))
	}
	last := filled[len(filled)-1]
	if last.Role != "tool" || last.ToolCallID != "h1" {
		t.Errorf("appended tail = %+v, want tool h1", last)
	}
	// 相邻性：追加后 tool 消息必须紧邻其 assistant(tool_calls)。
	prev := filled[len(filled)-2]
	if prev.Role != "assistant" || len(prev.ToolCalls) == 0 {
		t.Errorf("tool response must immediately follow its assistant(tool_calls); got %+v", prev)
	}
}

// readThenQuestionExecutor: read → 普通响应；handoff_to_agent → 带 Questions。
// 用于构造"同一条 assistant 并发发 [read, handoff]"的场景。
type readThenQuestionExecutor struct{ questions []string }

func (x *readThenQuestionExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		if c.Name == HandoffToolName {
			out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "child asked", Questions: x.questions})
			continue
		}
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func (x *readThenQuestionExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: "read"}}}
}

// TestSuspendedNestedWithSiblingToolCall: 同一条 assistant 并发发 [read, handoff]
// 时，read 的响应已在 history 末尾 —— 回填必须**键控 PendingToolID 追加**，
// 绝不能按位置替换末尾那条（N3）。
func TestSuspendedNestedWithSiblingToolCall(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "r1", Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`}},
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"g"}`}},
		}}},
	}}
	exec := &readThenQuestionExecutor{questions: []string{"选哪个？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test", registry: NewAgentRegistry()}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	s := result.Suspended
	if s == nil || !s.PendingToolMissing || s.PendingToolID != "h1" {
		t.Fatalf("want pending h1 missing, got %+v", s)
	}
	tail := s.History[len(s.History)-1]
	if tail.Role != "tool" || tail.ToolCallID != "r1" {
		t.Fatalf("precondition: tail must be the sibling read response (not an assistant), got %+v", tail)
	}
	filled := fillPendingToolResponse(s, "Redis")
	if got := filled[len(filled)-1]; got.Role != "tool" || got.ToolCallID != "h1" || got.Content != "Redis" {
		t.Errorf("must APPEND the handoff response, got tail %+v", got)
	}
	for _, m := range filled {
		if m.Role == "tool" && m.ToolCallID == "r1" && m.Content != "ok" {
			t.Errorf("sibling read response must be untouched, got %+v", m)
		}
	}
}

// TestFillPendingRefusesOrphanedInsert: 末条既不是占位 tool、也不是
// assistant(tool_calls) 时拒绝回填（防止产出违反 API 契约的 history）。
func TestFillPendingRefusesOrphanedInsert(t *testing.T) {
	s := &SuspendedRun{PendingToolID: "x", History: []ModelMessage{{Role: "assistant", Content: "纯文本"}}}
	if out := fillPendingToolResponse(s, "a"); !strings.Contains(out[len(out)-1].Content, "olp") &&
		len(out) != len(s.History) {
		t.Errorf("must not append a tool response after a non-tool-call message: %+v", out)
	}
}
```

> 最后一条用例的判定口径由实现者按最终返回约定收紧（例如返回 `(history, error)`）；**核心断言是"不得在非 tool_calls 的 assistant 消息后追加 tool 响应"**。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestSuspended -v`
预期：FAIL（编译错误——`SuspendedRun`/`fillPendingToolResponse`/`HandoffResult.Suspended` 未定义）

- [ ] **步骤 3：编写实现代码**

`engine/agent.go`：

```go
// SuspendedRun is the resumable state of a sub-agent run that stopped on
// ask_user. It is returned in-process only (HandoffResult.Suspended carries
// json:"-"), so the engine can resume the SAME run once the user answers
// instead of discarding its history and re-delegating from zero.
type SuspendedRun struct {
	// History is the breakpoint history, verbatim.
	History []ModelMessage
	// PendingToolID is the tool_call_id awaiting its response: the ask_user call
	// on the leaf path, or the handoff call whose result never got written on
	// the nested-bubble path.
	PendingToolID string
	// PendingToolMissing is true when no tool message was written for
	// PendingToolID yet (nested-bubble path returns before writing it) — the
	// resume must APPEND; otherwise the placeholder is replaced in place.
	PendingToolMissing bool
	// Input is the original handoff (goal/context/tools/depth/lang).
	Input Handoff
	// Partition and Model are the run's prefix-cache partition and its forked
	// client, reused verbatim on resume (I2) — rebuilding either would turn the
	// whole history into cache misses.
	Partition string
	Model     ModelClient
	// Spent is the usage accumulated so far; the budget continues from here (I3).
	Spent ModelUsage
	// Iter is the loop counter at suspension. runLoopResume restarts the loop
	// from here so a capped run does NOT get a fresh MaxIterations budget.
	Iter int
	// BudgetNudgedTokens carries the token-budget 80% nudge flag so resume does
	// not inject the wrap-up nudge a second time.
	BudgetNudgedTokens bool
	// ModelName is the *effective* model at suspension. NOT recomputable from
	// Input: the loop mutates it on the flash→Pro escalation path
	// (sub_agent.go:585-590), so a resumed flash agent would silently fall back
	// to flash without this.
	ModelName string
	// ChildRunID is the job id of the child whose question caused this
	// suspension (empty when this run asked the user itself). Used to stitch the
	// cascade when resuming bottom-up.
	ChildRunID string
}

// fillPendingToolResponse returns history with the pending tool call answered.
// ONE implementation for both paths, because the API contract is one rule: a
// tool response must immediately follow the assistant(tool_calls) it answers.
//   - placeholder present (leaf): replace its content, keep message count;
//   - placeholder missing (nested bubble): append it right after the trailing
//     assistant(tool_calls).
// A history whose tail is neither is left untouched (never silently produce an
// invalid sequence).
func fillPendingToolResponse(s *SuspendedRun, answer string) []ModelMessage
```

`HandoffResult` 增加字段（放在 `Questions` 附近）：

```go
	// Suspended carries the resumable state of a run that ended on
	// awaiting_user. Internal: never serialized, never model-visible.
	Suspended *SuspendedRun `json:"-"`
```

`engine/sub_agent.go`：

1. `runLoop` 顶部（partition 计算之后）把已算好的 `partition`/`model`/`input` 存进局部变量，供返回处构造 `SuspendedRun`。
2. 叶子 `ask_user` 返回处填 `SuspendedRun{History: history, PendingToolID: call.ID, PendingToolMissing: false, Input: input, Partition: partition, Model: model, Spent: totalUsage}`。
3. 嵌套冒泡返回处填同构的 `SuspendedRun`，但 `PendingToolID: res.ToolCallID`、`PendingToolMissing: true`、`ChildRunID: res.RunID`（任务 3 引入该字段）。
4. 两处都要填 `Iter: iter`、`BudgetNudgedTokens: budgetNudgedTokens`、`ModelName: modelName`（跨断点状态，见下表）。

**跨断点状态清单（任务 2 的核心交付）**

`runLoop` 拆为 `prologue`（`sub_agent.go:246-357`，构造状态）与 `loopBody(ctx, st *loopState)`（`:358-760`）。`runLoopResume` **绝不重跑 prologue** —— 重跑会 `model.Fork()` + `partitionSeq.Add(1)`（`:259-278`），**I2 当场失效** —— 而是从 `SuspendedRun` 重建 `loopState`：

| loopState 字段 | runLoop（prologue） | runLoopResume |
|---|---|---|
| `history` | 构造 system + volatile | `= s.History` |
| `model` | `Fork()` + `ForkWithBaseURL(partition)` | `= s.Model`（I2） |
| `partition` | 新分配 | `= s.Partition`（I2） |
| `totalUsage` | 零值 | `= s.Spent`（I3） |
| `modelName` / `isFlashAgent` | 由 `input.ModelOverride` 计算 | `= s.ModelName`（**非纯函数**：flash→Pro 升级会改它，`sub_agent.go:585-590`） |
| `iter` | `0` | `= s.Iter` |
| `budgetNudgedTokens` | `false` | `= s.BudgetNudgedTokens` |
| `ctx` / `cancel` | 本次 Run 的 ctx | resume 那一轮 Run 的 ctx（生命周期边界） |
| `agentName` / `structured` / `filteredTools` / `effectiveSet` / `limit` / `compressThreshold` / `maxIterations` | 由 `input` + runner 计算 | 同（确定性重建） |
| `consecutiveIntermediate` / `consecutiveTruncation` / `consecutiveBlocked` / `budgetNudged` / `tokenNudgePending` / `streamer` | 零值 | **重置**（记入已知取舍：恢复后可多拿 3 次 strike、可能重复一次 stream_delta） |

> **叶子与中间层在 resume 前都必须先 `fillPendingToolResponse` 再进 `loopBody`**：否则首个迭代顶部的 token nudge（`sub_agent.go:413`）会插在 `assistant(tool_calls)` 与其 tool 响应之间，回退成 I1 违规（这正是 B2 那个缺陷的复现路径）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestSuspended -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归**

运行：`go test ./engine/`
预期：全部 PASS（`TestSubAgentAskUser_CarriesPartialFindings`、`TestSubAgentNestedBubble` 必须仍绿）

- [ ] **步骤 6：Commit**

```bash
git add engine/agent.go engine/sub_agent.go engine/sub_agent_suspended_test.go
git commit -m "feat(engine): capture resumable state on awaiting_user"
```

---

### 任务 3：登记通路（registrar 注入）+ RunID 穿线

> **N1 落地**：depth≥1 的挂起发生在 `SubAgentRunner.RunSubAgent` → 自由函数 `runHandoff`，而 `SubAgentRunner` 没有 `*Engine` 引用 —— 因此必须给 `handoffOptions` 注入 registrar，登记在**每一层各自发生**。

**文件：**
- 修改：`engine/handoff.go`（`handoffOptions.register` + `runHandoff` 内登记，**早于** digest 格式化）
- 修改：`engine/sub_agent.go`（`SubAgentRunner.register` 字段 + `SetSuspendedRegistrar`；`RunSubAgent` 传参）
- 修改：`engine/types.go`（`ToolResult.RunID`）
- 修改：`tools/registry.go`（`ToolResultEnvelope.RunID`）、`tools/subagent.go`（拷贝）、`tools/adapter.go`（转换）
- 修改：`cmd/run.go`（装配 `runner.SetSuspendedRegistrar(e.registerSuspended)`）

- [ ] **步骤 1：编写失败的测试**

在 `engine/sub_agent_suspended_test.go` 追加：

> **不要用桩喂 `RunID`**（那会让测试绿而真路径仍断）：下面两条用例都走**真 `runHandoff`**，只有 registrar 是假的。桩复用 `handoff_test.go` 既有的 `mockAgentForHandoff`。

```go
// TestRunHandoffRegistersSuspension: runHandoff 必须把挂起交给注入的 registrar，
// 并把 handle 回填进 ToolResult.RunID（N1(b)）。
func TestRunHandoffRegistersSuspension(t *testing.T) {
	suspended := &SuspendedRun{Input: Handoff{Goal: "g"}}
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "选哪个？", Questions: []string{"选哪个？"},
		FinishReason: HandoffReasonAwaitingUser, Suspended: suspended,
	}})

	var got *SuspendedRun
	res := runHandoff(context.Background(),
		ToolCallRequest{ID: "h1", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"g"}`)},
		handoffOptions{
			resolve:  func(id AgentID) (Agent, error) { return reg.Get(id) },
			depth:    1,
			register: func(s *SuspendedRun, a AgentID, goal string) string { got = s; return "bg-7" },
		})
	if got != suspended {
		t.Fatal("runHandoff must hand the suspension to the registrar (N1)")
	}
	if res.RunID != "bg-7" {
		t.Errorf("ToolResult.RunID = %q, want bg-7", res.RunID)
	}
}

// TestSubAgentRunnerWiresRegistrar: 嵌套后端必须把 runner 的 registrar 传进
// runHandoff —— 装配缺失时此用例红（这正是 N1 的缺口）。
func TestSubAgentRunnerWiresRegistrar(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "选哪个？", Questions: []string{"选哪个？"},
		FinishReason: HandoffReasonAwaitingUser, Suspended: &SuspendedRun{Input: Handoff{Goal: "g"}},
	}})
	runner := &SubAgentRunner{registry: reg, modelName: "test"}
	called := false
	runner.SetSuspendedRegistrar(func(s *SuspendedRun, a AgentID, goal string) string {
		called = true
		return "bg-9"
	})

	res, err := runner.RunSubAgent(context.Background(), HandoffToAgentParams{Agent: "sub", Goal: "g"}, 1, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if !called {
		t.Fatal("SubAgentRunner.RunSubAgent must pass its registrar to runHandoff")
	}
	if res.RunID != "bg-9" {
		t.Errorf("RunID = %q, want bg-9", res.RunID)
	}
}
```

该文件需补 import `"encoding/json"`。**handle 进入 digest 的断言在任务 5**（那里才给 `handoffReasonHeading` 增 runID 参数）；本任务只锁"登记 + 穿线"。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestRunHandoffRegistersSuspension|TestSubAgentRunnerWiresRegistrar' -v`
预期：FAIL（编译错误——`handoffOptions.register`、`SetSuspendedRegistrar`、`ToolResult.RunID` 均不存在）

- [ ] **步骤 3：编写实现代码**

三处逐字段拷贝（与 `Questions` 现有穿线位置**完全同构**，照抄即可）：

```go
// engine/types.go — ToolResult
	// RunID is the job id of a sub-agent that suspended on a question, so the
	// parent run can stitch the resume cascade without parsing digest text.
	RunID string `json:"run_id,omitempty"`

// tools/registry.go — ToolResultEnvelope：同名字段
	RunID string `json:"run_id,omitempty"`

// tools/subagent.go — ToolResultEnvelope 构造：RunID: res.RunID,
// tools/adapter.go    — 转换处：RunID: env.RunID,
```

登记通路（N1(b)）：

```go
// engine/handoff.go — handoffOptions 增字段
	// register hands a suspended run to the engine's job table and returns its
	// job id ("" = not registered: no registrar injected, or the cap is full).
	// Called BEFORE the digest is formatted, so the handle can appear in it.
	register func(*SuspendedRun, AgentID, string) string

// engine/handoff.go — runHandoff 内，构造 ToolResult 之前：
	runID := ""
	if result.Suspended != nil && opts.register != nil {
		runID = opts.register(result.Suspended, AgentID(params.Agent), params.Goal)
	}
	// …随后 ToolResult 增 RunID: runID（格式化的 digest 里就带上 handle）

// engine/sub_agent.go — SubAgentRunner 增字段 + setter
	register func(*SuspendedRun, AgentID, string) string

// SetSuspendedRegistrar injects the engine's suspension registry so nested runs
// (depth >= 1) can hand their suspended state back — SubAgentRunner holds no
// *Engine reference by design. nil = nested runs do not suspend.
func (r *SubAgentRunner) SetSuspendedRegistrar(fn func(*SuspendedRun, AgentID, string) string) {
	r.register = fn
}

// 两处 handoffOptions 传参：
//   Engine.RunSubAgent    → register: e.registerSuspended
//   SubAgentRunner.RunSubAgent → register: r.register
```

`Engine.RunSubAgent` **不再自行登记**（避免与 `runHandoff` 内的登记重复）；`dispatchAsync` 绕过 `runHandoff`（直接调 `agent.Run`），它**仍需**自行登记（任务 4）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ ./tools/ -run 'RunID|Handoff' -v` 然后 `go build ./...`
预期：PASS，无编译错误

- [ ] **步骤 5：Commit**

```bash
git add engine/types.go engine/handoff.go tools/registry.go tools/subagent.go tools/adapter.go engine/sub_agent_suspended_test.go
git commit -m "feat(engine): plumb suspended job id through the tool result chain"
```

---

### 任务 4：登记挂起 + 多槽问题 + 清空语义

**文件：**
- 修改：`engine/types.go`（`AskUserRequest.RunID`）
- 修改：`engine/handoff.go`（`runHandoff` / `dispatchAsync` 登记、问题入队）
- 修改：`engine/loop.go`（`pendingAskUser` 单指针 → 队列、三处清空语义、`maxSuspendedSubAgents` 装配）
- 修改：`engine/turn.go`（`/confirm` 数字映射带 RunID、清空点）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/agent_resume_test.go`（本任务只用其登记相关用例）：

```go
// TestSuspendRegistersJobAndBindsQuestion: 同步 handoff 收到 awaiting_user →
// job 表出现 awaiting_user 条目、问题携带该 RunID、digest 带 handle。
func TestSuspendRegistersJobAndBindsQuestion(t *testing.T)          { /* 见任务 5 步骤 1 */ }

// TestSuspendCapFailsLoud: 挂起表满 → 新提问**不挂起**，退回现状语义
// （照旧终止 + 冒泡），且 pinned/digest 能看出原因（I5）。
func TestSuspendCapFailsLoud(t *testing.T)                          { /* 构造 maxSuspendedSubAgents=1 */ }

// TestMultiSlotQuestionsBothSurvive: 两个并行子代理同时提问，两条都不丢、
// 各自绑定 handle（修 pendingAskUser 单槽覆盖）。
func TestMultiSlotQuestionsBothSurvive(t *testing.T)                { /* 两次 RunSubAgent */ }

// TestAsyncSuspendDoesNotStaleResult: N2 —— 异步子代理挂起时不得把
// awaiting_user 结果写进 task.result，否则恢复后的最终结果会命中 channel 的
// select/default 被静默丢弃、而 agent_poll 读到陈旧问题并判 done。
// 断言：挂起后 len(task.result)==0；resume 完成后 poll 拿到的是**最终**结果。
func TestAsyncSuspendDoesNotStaleResult(t *testing.T) {}

// TestSetHistoryClearsSuspended: 细节3 —— /resume 换历史后，指向旧历史的
// 挂起 run 必须被清掉（否则出现悬空 run）。
func TestSetHistoryClearsSuspended(t *testing.T) {}
```

> 本任务先写 `TestMultiSlotQuestionsBothSurvive`、`TestSuspendCapFailsLoud`、`TestAsyncSuspendDoesNotStaleResult`、`TestSetHistoryClearsSuspended` 四条；`TestSuspendRegistersJobAndBindsQuestion` 随任务 5 补齐。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestMultiSlot|TestSuspendCap' -v`
预期：FAIL（编译错误——`AskUserRequest.RunID`、多槽字段、挂起上限未接线）

- [ ] **步骤 3：编写实现代码**

`engine/types.go`（`AskUserRequest` 声明在 `types.go:120-123`，**不在 loop.go**）：

```go
// AskUserRequest is one pending question. RunID non-empty means a sub-agent
// asked it (the job that must be resumed with the answer); empty means the main
// agent asked it itself.
type AskUserRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	RunID    string   `json:"run_id,omitempty"`
}
```

`engine/loop.go`（`pendingAskUser` 单指针 → 队列）：

```go
// Engine
	// pendingAskUser is a QUEUE: parallel sub-agents can ask at the same time, and
	// a single slot made them overwrite each other.
	pendingAskUser []*AskUserRequest
```

**三处消费点必须改为"只消费队首 / 按 RunID 定位"**（现状是整体 `= nil`，改多槽后会静默丢问题）：

| 位置 | 现状 | 改为 |
|---|---|---|
| `loop.go` 自由输入路径（Run 开头，注释"本组待决问题作废"） | `pendingAskUser = nil` | 弹出**队首**并作废；其余保留（各自的 RunID 仍有效） |
| `loop.go` `handleConfirmCommand` 取消分支 | `pendingAskUser = nil` | 同上（只作废当前被回答的那条） |
| `loop.go` `handleConfirmCommand` 处理后 | `pendingAskUser = nil` | 只移除被 `/confirm N` 消费的那条（按 RunID 定位，见下） |

`/confirm N` 的数字映射必须带 RunID：

```go
// 现状：e.pendingAskUser.Options[n-1]
// 改为：取队首 head := e.pendingAskUser[0]，映射 head.Options[n-1]，
//       若 head.RunID != "" —— 这是子代理的问题，用户的选项文本同时作为
//       agent_resume 的 answer 素材（是否回填仍由模型决定）。
```

`engine/handoff.go` 的登记（同步与异步两路）：

```go
// registerSuspended 把断点状态登记为一条 awaiting_user job。超出挂起上限时
// 返回 ""，调用方按"不挂起"的现状语义继续（I5 fail-loud，绝不静默丢弃问题）。
func (e *Engine) registerSuspended(s *SuspendedRun, agent AgentID, goal string) string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	if e.maxSuspendedSubAgents > 0 && e.countByStateLocked(bgStateAwaitingUser) >= e.maxSuspendedSubAgents {
		loopLog.Printf("suspend cap reached; run %s ends without a resumable entry", agent)
		return ""
	}
	e.bgSeq++
	id := fmt.Sprintf("bg-%d", e.bgSeq) // 沿用既有命名空间
	// ctx/cancel stay nil: a suspended entry holds no goroutine and no request.
	// They are set at RESUME time from the ctx of the Run that calls
	// agent_resume, so the resumed run is cancellable and — like every async
	// job — never outlives that Run.
	e.bgTasks[id] = &bgTask{id: id, agent: agent, goal: goal, state: bgStateAwaitingUser,
		suspended: s, startAt: time.Now(),
		// I9 (H2): the channel MUST exist at registration. The synchronous suspend
		// path never goes through dispatchAsync (handoff.go:205 is the only place
		// that creates it), and a nil channel always takes the default branch in a
		// select → the resumed run's final send would be dropped and
		// agent_poll (turn.go:1466) would report "still running" forever.
		result: make(chan *HandoffResult, 1)}
	return id
}
```

- **同步路径**：登记已在 `runHandoff` 内完成（任务 3），`Engine.RunSubAgent` 只需读 `res.RunID`：非空 → 问题入队（`&AskUserRequest{Question, Options, RunID: res.RunID}`）；为空（未登记 / 挂起表满 / registrar 未注入）→ 不入队，问题仍由 `res.Questions` 冒泡（退回现状：父代理重新委派）。
- **异步路径**（`dispatchAsync` 的 goroutine）：**现状它完全不处理 `result.Questions`**（问题只作为文本出现在 poll 结果里，UI 根本问不到用户）。改为：`result.Suspended != nil` → `registerSuspended` 并保留条目（不 cancel）；**问题不入队**（Run 可能已结束，避免与 UI 竞态），靠下一 Run 的 pinned 摘要 + `agent_poll` 呈现。
- **N2：挂起时绝不写 `task.result`、绝不发 `agent_done`。** `task.result` 是 cap-1 + `select/default` 发送，一旦先塞入 `awaiting_user` 结果，恢复后的最终结果会被 `default` 静默丢弃，而 `agent_poll` 会读到那条陈旧问题并判 done。约定：**channel 只承载最终结果**；waiting 状态一律由 `t.state`/`t.suspended` 渲染（任务 5 的 poll 分支）。不发 `agent_done` 也让 UI 面板继续显示该 agent 未完成 —— 语义正确（它在等用户）。
- **细节1：`dispatchAsync` 的满容判定必须换口径。** 现状 `len(e.bgTasks) >= cap`（`handoff.go:188`，已在 `bgMu` 临界区内）→ 改为 `e.countByStateLocked(bgStateRunning) >= cap`；否则挂起条目仍占 async 名额（违背 I5），而换成自锁包装会死锁。
- **细节2：`askUserOptions()` 与调用点取队首。** `loop.go:553` 的条件与 `loop.go:568-578` 的 `askUserOptions()` 都按单指针读 `e.pendingAskUser.Options` → 改为读队首；`ask_user_test.go:158/169/183` 三处直接调用 `askUserOptions()` 的既有测试需同步适配。
- **细节3：换历史必须清挂起。** `SetHistory`（`loop.go:252`，`/resume` 经 `ui/model.go:1508` 调用）里清空 `bgTasks` 的 `awaiting_user` 条目 —— 否则留下指向已回退历史的悬空 run（与本计划"跨 rewind 不做"的口径一致）。同时确认 rewind 路径是否另有一处历史替换点；若有，同样清空。
- **细节4 / I7：嵌套子登记失败的传播。** 子级 `res.RunID == ""`（表满 / 未注入 registrar）时，父级**不得**挂起：父级仍以 `awaiting_user` 返回、问题照旧冒泡，由父代理重新委派。理由：父级的待回填项是"子的结果"，没有子的 handle 就永远填不上。
- **I9 (H2)：挂起条目的 `result` channel 在登记时创建**（见上 `registerSuspended` 代码）。`TestAgentResumeContinuesSameRun` 必须含"resume 后 `agent_poll(顶层)` 拿到**最终结果**"的断言 —— 否则这条 1 行缺陷会以"poll 永远 still running"的形式静默存活。
- **F1 表操作纪律（级联 / 恢复 / TTL 三者并发）**：凡"查条目 → 校验 `state`/`childRunID`/未过期 → 改 state"一律走**单一持锁 helper**（如 `withTaskLocked(id string, fn func(*bgTask) bool)`，全程持 `bgMu`）；**goroutine 的启动与 `runLoopResume` 调用必须在锁外**（锁内起 goroutine 会与子代理完成回调互相持锁）。`-race` 需覆盖：resume × TTL 淘汰并发、级联回填 × `agent_poll` 并发。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestMultiSlot|TestSuspendCap|TestAsyncOutstanding' -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归**

运行：`go test ./engine/`
预期：全部 PASS（`TestProcessAgentPollCalls_StatusFlow` 等既有 ask_user/async 用例不受影响）

- [ ] **步骤 6：Commit**

```bash
git add engine/handoff.go engine/loop.go engine/turn.go engine/agent_resume_test.go
git commit -m "feat(engine): register suspended runs and queue concurrent questions"
```

---

### 任务 5：`agent_resume` 工具、恢复执行与级联

**文件：**
- 修改：`engine/agent.go`（工具 spec + heading handle + follow-up pin 分支）
- 修改：`engine/sub_agent.go`（`runLoopResume` 入口）
- 修改：`engine/turn.go`（spec 挂载 + 分类分支 + `processAgentResumeCalls` + `agent_poll`/pinned 按 state 分支）
- 修改：`cmd/run.go`（注册 + 装配 `maxSuspendedSubAgents`）

- [ ] **步骤 1：编写失败的测试**

在 `engine/agent_resume_test.go` 追加（完整实现）：

```go
// TestAgentResumeContinuesSameRun: 恢复必须续用同一 history（不重建），
// 且把用户回答落成 ask_user 的 tool 响应（I1）。
func TestAgentResumeContinuesSameRun(t *testing.T) { /* 断言首条 system 与断点前一致、tool 内容 = answer */ }

// TestAgentResumeReusesBudget: I3 —— 挂起前已花 80% 预算，恢复后第一轮即触发
// 收尾 nudge（capture stub 断言 history 里出现 tokenBudgetNudge 文案）。
func TestAgentResumeReusesBudget(t *testing.T) {}

// TestAgentResumeReusesPartition: I2 —— 恢复请求的 partition 与挂起前一致。
func TestAgentResumeReusesPartition(t *testing.T) {}

// TestAgentResumeFailLoud: resume 一个 running / done / 不存在 / 已过期条目
// 一律返回明确错误，绝不静默重建（I5）。
func TestAgentResumeFailLoud(t *testing.T) {}

// TestAgentResumeNestedCascade: depth-2 提问 → 两层都挂起 → resume 叶子 →
// 子结果回填父级挂起 history（\(\*\)）→ 父级续跑 → 主代理经 agent_poll 拿到最终结果。
func TestAgentResumeNestedCascade(t *testing.T) {}

// TestAgentResumeRejectsIntermediate: I8 —— 入口句柄是中间层（PendingToolMissing
// ==true，在等子级）时，引擎必须下钻到叶子，**不得**把用户回答写进 handoff 的
// tool 位；若无法下钻到叶子则 fail-loud。同时断言：中间层的 digest/pinned 文案
// 不出现 agent_resume 指引、也不出现更深层的 handle。
func TestAgentResumeRejectsIntermediate(t *testing.T) {}

// TestAgentResumeIterationBudgetNotReset: I3 续 —— 挂起前已用掉 maxIterations-1
// 轮，恢复后只应剩 1 轮（`Iter` 被携带），不得白拿一整轮迭代预算。
func TestAgentResumeIterationBudgetNotReset(t *testing.T) {}

// TestAgentPollReportsWaiting: awaiting_user 条目 poll 出 waiting 文案并指路
// agent_resume。
func TestAgentPollReportsWaiting(t *testing.T) {}

// TestPinnedSummaryRendersWaitingState: pinned 摘要对挂起条目渲染 waiting
// 而非硬编码的 running。
func TestPinnedSummaryRendersWaitingState(t *testing.T) {}
```

\(\*\) 该回填复用任务 2 的 `fillPendingToolResponse`，因此级联**不需要**"替换某条已存在的 handoff tool 消息"——那正是 v1 的错误假设。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestAgentResume -v && go test ./engine/ -run 'TestAgentPollReportsWaiting|TestPinnedSummaryRendersWaitingState' -v`
预期：FAIL（编译错误——`agent_resume` 未定义）

- [ ] **步骤 3：编写实现代码**

**工具 spec**（`engine/agent.go`，与 `agentPollToolSpec` 同点同形）：

```go
// AgentResumeToolName feeds the user's answer back into a suspended sub-agent.
const AgentResumeToolName = "agent_resume"

func agentResumeToolSpec(zh bool) ModelTool {
	desc := "Resume a suspended sub-agent (one that stopped to ask the user) with the user's answer. Non-blocking: the run continues in the background — fetch its result later with agent_poll(run_id). Use this only when the user's latest message is answering that sub-agent's question."
	if zh {
		desc = "把用户的回答送进挂起的子代理（它此前停下提问）。非阻塞：run 在后台继续，之后用 agent_poll(run_id) 取结果。仅当用户这条消息是在回答该子代理的问题时才使用。"
	}
	// run_id：必填，取 pinned 摘要或 digest 里的 bg-N。
	// answer：必填，用户的回答原文。
	...
}
```

**挂载/分类/拦截**（`engine/turn.go`）：与 `agent_poll` 完全同点——`toolSpecsWithHandoff()` 追加 spec；分类循环加 `case AgentResumeToolName: continue`；新增 `processAgentResumeCalls`，对每个调用：

```go
// 1) 查表（持锁 helper，F1）：不存在 / 非 awaiting_user / 已过期 → 明确错误，
//    提示"该子代理已不可恢复，请重新委派"。
// 2) I8：入口句柄沿 ChildRunID 下钻到叶子（ChildRunID == ""）。**绝不对
//    PendingToolMissing==true 的中间层做回填** —— 那会把用户回答写进 handoff
//    的 tool 位（语义错）。若句柄本身就找不到叶子 → fail-loud。
// 3) fillPendingToolResponse(leaf.suspended, answer) → 叶子续跑 history（I1）。
// 4) 叶子条目置 running，起 goroutine：runLoopResume(ctx, leafEntry)。
//    入口（顶层）条目保持 awaiting_user，作为本次委派的**交付位**。
// 5) 叶子跑完 → onSuspendedRunDone(leafID, result) 级联向上：顶层条目置 running、
//    其 suspended 被 fillPendingToolResponse(..., formatHandoffResult(childResult, zh))
//    回填、起 goroutine 续跑；顶层跑完后把最终结果写进**顶层条目的 result channel**，
//    由 agent_poll(顶层) 交付。更深层句柄全程不出现在模型可见面（I8）。
```

**级联（引擎确定性缝合）**：

```go
// onSuspendedRunDone 在一条挂起 run 真正跑完后调用，两段职责严格分离（F1）：
//   (a) 表操作（单一持锁 helper）：反查 childRunID == childID 的父条目 → 校验
//       state == awaiting_user 且未过期 → 置 running 并取出其 suspended。
//       校验失败（父条目已被 TTL/poll 删除）→ **fail-loud 日志**，子结果绝不静默丢弃。
//   (b) 锁外：fillPendingToolResponse(parentSuspended,
//       formatHandoffResult(childResult, zh)) → 起 goroutine runLoopResume(ctx, parentEntry)。
//       父条目若还有父（更上层）→ 自己跑完后继续向上级联；父条目无父（顶层）→
//       结果写 parentEntry.result，由 agent_poll 交付。
//   终止条件：反查不到父条目。父级 resume 后**若再次 ask_user** → 该父条目回到
//   awaiting_user、级联停止（等待新一轮用户输入），不得继续向上。
func (e *Engine) onSuspendedRunDone(childID string, result *HandoffResult) { ... }
```

**`agent_poll` 与 pinned 摘要按 state 分支**：

```go
// processAgentPollCalls：state == bgStateAwaitingUser 时返回
// "Job bg-3 (sub) is waiting for user input: <question>. Reply with agent_resume(bg-3, <answer>)."

// injectBackgroundJobsSummary：now := "— running"；改为
// switch t.state { case bgStateAwaitingUser: "— waiting for user input"; default: "— running" }
```

**digest 带 handle**（`engine/agent.go` 的 `handoffReasonHeading` 需要 handle 参数）：

```go
// 现状签名 handoffReasonHeading(reason string, zh bool) → 增参 runID string。
// awaiting_user 分支：pickPrompt(zh, fmt.Sprintf("Sub-agent needs user input (%s):", runID),
//                               fmt.Sprintf("子代理需要用户输入（%s）：", runID))
// runID 为空时退回不带 handle 的文案。
```

**follow-up pin 按 reason 分支**（`buildHandoffFollowUp(zh bool)` → `buildHandoffFollowUp(reason string, zh bool)`）：`awaiting_user` 的文案指向 `agent_resume`（"若这是该子代理问题的回答，用 agent_resume(<handle>, 回答) 让它继续；否则基于其部分结果自行继续"），其余 reason 保持原文案（"重新委派"）。

**TTL（惰性，非时间保证）**：`bgTask.startAt` 已有；在 `agent_resume` / `agent_poll` / 摘要渲染三处读取时判定 `time.Since(startAt) > 30*time.Minute` → 视为过期：从表中删除、resume 报错、摘要里列为过期。**无后台 timer**，故无人读取的条目不会过期（仅受 `max_suspended_subagents` 约束）。

- **`startAt` 刷新**：同一 handle 被 resume 后**再次挂起**时刷新 `startAt`（否则会按首次挂起时间提前过期）。
- **淘汰通知（D12）**：条目因 TTL/容量被清除时，**pinned 渲染一次"bg-N 已过期，不可恢复，请重新委派"**；仅当该条目曾以 async 身份露面（有 `agent_start`）才补发 `agent_done` —— 同步挂起路径没有 `agent_start`，补孤立 `agent_done` 会让 UI 事件不成对。目的：与 N2"挂起不发 `agent_done`"叠加后，避免 UI 面板整会话停在未完成态。
- **淘汰 × 级联竞态**：父条目在子级完成前被淘汰 → `onSuspendedRunDone` 反查失败必须 **fail-loud 记录**（见级联契约 (a)），不得静默丢子结果。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestAgentResume|TestAgentPollReportsWaiting|TestPinnedSummaryRendersWaitingState' -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归 + -race**

运行：`go test ./engine/ && go test -race ./engine/`
预期：全部 PASS

- [ ] **步骤 6：Commit**

```bash
git add engine/agent.go engine/sub_agent.go engine/turn.go cmd/run.go engine/agent_resume_test.go
git commit -m "feat(engine): agent_resume resumes a suspended sub-agent in place"
```

---

### 任务 6：集成验证

**文件：** 无新代码；验证跨包集成与文档同步。

- [ ] **步骤 1：全量编译与静态检查**

运行：`go build ./... && go vet ./...`
预期：无错误

- [ ] **步骤 2：全量测试（含 -race）**

运行：`go test -count=1 ./... && go test -race -count=1 ./engine/`
预期：全部 PASS

- [ ] **步骤 3：gofmt 回归（仅本次触碰的文件）**

运行：`gofmt -l engine/ tools/ cmd/`
预期：无新增文件（`engine/agent.go`/`config/config.go` 的既有非 gofmt 行不在本次范围，不要顺手重排）

- [ ] **步骤 4：手动冒烟（可选）**

```text
让子代理问一个需要用户决定的问题（如"选哪个缓存？"），
观察：① UI 弹窗/Blocked 呈现问题且 digest 带 (bg-N)；
② 下一轮 pinned 摘要显示 "— waiting for user input"；
③ 回答后主代理调用 agent_resume(bg-N, ...)，随后 agent_poll(bg-N) 取到结果；
④ 该子代理未重新读取此前已读的文件（日志里 partition 一致、cache hit 高）。
```

- [ ] **步骤 5：文档同步**

- `docs/superpowers/specs/2026-09-24-subagent-suspend-resume-design.md`：把 `/tmp` 草稿按本计划 **§设计要点 + §决策记录（D1–D11）** 修正后落库（v2）；决策记录表**原样复制**，不另写一份以免漂移。
- `README.md` / `README.zh.md`：子代理段落补一句"子代理提问后会挂起；用户回答后可用 `agent_resume` 让它带着原上下文继续"。

- [ ] **步骤 6：Commit**

```bash
git add docs/superpowers/specs/2026-09-24-subagent-suspend-resume-design.md README.md README.zh.md
git commit -m "docs: sub-agent suspend/resume spec + README"
```

---

## 自检

- **规格覆盖度：**
  - 挂起而非焚毁（同一 run 续跑、保留原始 history）→ 任务 2 + 任务 5
  - I1 回填**键控 `PendingToolID`**（含"同条 assistant 带 `[read, handoff]`"的 N3 分支）→ 任务 2 `fillPendingToolResponse` + 三条用例
  - I2 partition/model 复用 → 任务 2 捕获 + 任务 5 复用断言
  - I3 预算连续 → 任务 2 `Spent` + 任务 5 断言
  - I4 生命周期例外（`awaiting_user` 跨 Run 存活）→ 任务 1 用例
  - I5 双计数分离 + 一切放弃 fail-loud（归入既有"三分通则"第 2 类）→ 任务 1 计数 + 任务 4 容量/`dispatchAsync` 换口径 + 任务 5 TTL/resume 防护
  - I6 每层各自登记（registrar 注入）→ 任务 3 两条真路径用例 + 任务 4 异步路径
  - I7 嵌套子登记失败 → 父级不挂起 → 任务 4 描述 + 任务 5 级联用例的负例
  - 父子关联与级联（引擎确定性缝合）→ 任务 3 穿线 + 任务 5 级联
  - 模型可见面四处（`agent_resume` spec、`agent_poll` waiting、pinned 按 state、digest handle、follow-up pin 指向）→ 任务 5
  - 顺带修既有洞：async 子代理提问不冒泡 + 挂起结果污染 `task.result`（N2，任务 4）、`pendingAskUser` 单槽覆盖（任务 4）、`askUserOptions` 队首（任务 4）、`/confirm` 数字映射（任务 4）、换历史清挂起（任务 4）
  - 明确不做（跨会话持久化 / 子代理互恢复 / 跨 rewind 存活 / 只恢复叶子）→ 未出现在任何任务中
- **占位符扫描：** 任务 5 的用例体以注释给出断言口径而非完整代码——**这是本计划唯一偏离家规的地方**：恢复入口需要重构 `runLoop` 的入参形态（`runLoopResume` 与既有 `runLoop` 共用主循环体），逐行代码在写出第一版接口前无法可靠预置。实现者必须先落 `runLoopResume` 的签名与主循环共用方式，再回填这些用例；**七个用例名与断言口径是硬要求，不得删减**。
- **类型一致性：** `SuspendedRun{History,PendingToolID,PendingToolMissing,Input,Partition,Model,Spent,Iter,BudgetNudgedTokens,ModelName,ChildRunID}`、`fillPendingToolResponse(*SuspendedRun,string) []ModelMessage`、`bgStateRunning/bgStateAwaitingUser`、`AskUserRequest{Question,Options,RunID}`、`ToolResult.RunID`/`ToolResultEnvelope.RunID`、`handoffReasonHeading(reason,runID,zh)`（调用点 `formatHandoffResult`，`agent.go:489`；handle 只在 `opts.depth==0` 时渲入，I8）、`buildHandoffFollowUp(reason,zh)`、`withTaskLocked(id, fn)` 在各任务间一致。
- **已知取舍：**
  1. **不跨会话**：关掉 TUI 再开则挂起态丢失，退化为现状的重新委派（要跨会话就得落盘，并撞上 session-resume 的"剥离工具链"约束，另案）。
  2. **挂起态持有整段 history**（最坏 ~1M token/条，压缩阈值 95% 才触发）；由 `maxSuspendedSubAgents`（默认 4）兜底，量级与 A3 并发上限同类。
  3. **`bg-` 单一命名空间**：挂起条目沿用 `bg-N`，不引入 `sr-` 前缀。
  4. **级联是引擎写死的确定性缝合**，非行为猜测——中间层挂起时没有模型在跑，不存在"由模型决定"的主体（见"设计要点"）。
  5. 本计划**不重开** readonly-universe spec:17 否决过的"危险命令任意深度人审"方案（该否决的其余三条理由仍成立）。
  6. **异步挂起不发 `agent_done`**：UI 面板对挂起中的 async 子代理持续显示未完成（语义正确）。若该条目随后因 TTL/容量被淘汰，面板收不到终止事件 —— 本版接受，记为已知粗糙点。
  7. **换历史即清挂起**（`SetHistory`）：与"跨 rewind 存活 = 不做"一致，故 `/resume` 之后旧会话的挂起 run 不可恢复。

## 本轮评审修正记录（v1 → 本版）

| 评审意见 | 处置 |
|---|---|
| **N1** 嵌套无登记路径（`SubAgentRunner` 无 `*Engine` 引用） | 采纳**方案 (b)**：`handoffOptions.register` + `SubAgentRunner.SetSuspendedRegistrar` + 每层即时登记。任务 3 重写为"登记通路 + RunID 穿线"，两条用例走**真 `runHandoff`**（不再用桩喂 `RunID`），并新增 `TestSubAgentRunnerWiresRegistrar` 锁装配 |
| **N2** async 挂起结果污染 `task.result` | 采纳：挂起时不写 channel、不发 `agent_done`；channel 只承载最终结果；`TestAsyncSuspendDoesNotStaleResult` 锁定 |
| **N3** I1 对"一条 assistant 带多个 tool_call"不成立 | 采纳：I1 改为**键控 `PendingToolID` + `PendingToolMissing`**；补 `TestSuspendedNestedWithSiblingToolCall`（`[read, handoff]`，末尾是 read 的响应） |
| 细节1 `dispatchAsync` 满容口径未换 | 采纳：临界区内 `countByStateLocked(bgStateRunning)` |
| 细节2 `askUserOptions()` 读取点漏改 | 采纳：`loop.go:553` 与 `:568-578` 取队首 + `ask_user_test.go:158/169/183` 适配 |
| 细节3 换历史未清挂起表 | 采纳：`SetHistory` 清 `awaiting_user` 条目 + `TestSetHistoryClearsSuspended` |
| 细节4 嵌套 + 挂起表满未定义 | 采纳为 **I7**：子级无 handle → 父级也不挂起，退回现状语义 |
| 细节5 登记必须先于 digest 格式化 | 采纳：方案 (b) 天然满足（`runHandoff` 内、`formatHandoffResult` 之前）；digest 带 handle 的断言归任务 5 |
| v2 spec 需补决策记录表 | 已直接落进本计划 **§决策记录（D1–D11）**，含链式级联、`maxSuspendedSubAgents=4`、TTL 30min 三个数值；任务 6 步骤 5 原样复制到 spec |

## 第三轮修正记录（补丁 A–F）

| 补丁 | 内容 | 处置 |
|---|---|---|
| **A / H1** | `awaiting_user` 同时表示"等用户"（叶子）与"等子级结果"（中间层）→ 对中间层 resume 会把用户回答写进 handoff 的 tool 位 | 采纳为 **I8 + D13**：对外只暴露顶层句柄，resume 沿 `ChildRunID` 下钻到叶子；**并补机制**——handle 只在 `opts.depth==0` 的 `runHandoff` 渲进 digest（否则叶子 handle 会经"中间层 Summary = 子 digest"漏给模型） |
| **B / H2** | `registerSuspended` 未建 `result` channel → nil channel 在 `select/default` 恒走 default → resume 结果静默丢弃、poll 永远 "still running" | 采纳为 **I9**（登记时建 channel）+ 任务 4 断言"resume 后 poll 拿到最终结果" |
| **C** | `runLoop` 拆 prologue/loopBody 的**全字段清单**；`SuspendedRun` 增 `Iter`/`BudgetNudgedTokens` | 采纳；**并补** `ModelName`（flash→Pro 升级会在循环中改它，非纯函数）与 `ctx` 行；"先 `fillPendingToolResponse` 再进 loopBody"写进任务 2 |
| **D** | `processAgentResumeCalls` 下钻契约 + `onSuspendedRunDone` 两段职责 | 采纳（并入 I8 口径） |
| **E** | `[context].max_suspended_subagents` 配置面 + TTL 三条约定 | 采纳为 **D12**；淘汰通知口径改为**优先 pinned**（同步挂起路径无 `agent_start`，孤立 `agent_done` 事件不成对） |
| **F** | 表操作纪律（单一持锁 helper；goroutine 在锁外起） | 采纳，写入任务 4 |
