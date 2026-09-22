# handoff_to_agent 异步委派（Async Sub-Agent）— 设计规格

> **日期：** 2026-09-22
> **状态：** 已批准（用户拍板：`handoff_to_agent` 加 `async:true`；后台任务生命周期随 Run 结束；保留 `agent_poll` 作为只读查询通道）

## 目标

让主 agent 委派子代理后**不再同步阻塞**：`handoff_to_agent` 加 `async:true` 参数，后台异步运行子代理，主 agent 立即继续执行其它工作；主 agent 通过只读 `agent_poll(job_id)` 查询进度/取结果。用户输入（steer）在子代理运行期间照常注入生效。

**设计哲学（AGENTS.md 能力优先）**：给模型 `async` 参数 + `agent_poll` 通道，由模型自主决定"当前委派走同步还是异步"；引擎不写死判定规则。默认 `async:false`（同步）保持现状，`/collab`、`/ratd` 等结构化流程语义不变。

## 背景与决策

### 现状（已核实）

- 委派链路同步阻塞：`turn.go:632` handoff → `handoff.go:69 agent.Run()` 阻塞 → 主 agent 等待。
- bash 链路同步阻塞：`turn.go:697` → `bash.go:105 cmd.Run()`（默认 30s 超时）。
- 工具执行期间无中断检查：steer 软中断（`turn.go:211 checkSteerMidStream`）只在流式输出期间生效，被 bash/handoff 阻塞时失效。
- 并发安全已具备：`sub_agent.go:187` 每次 `model.Fork()` 独立 client；`llm/adapter.go:70 Fork()` 独立 ReasoningEchoManager；`accumulateUsage` 有 `usageMu`（`loop.go:842`）；`OnProgress` 可并发（`cmd/run.go:190`）。
- UI 子代理面板已存在：`agent_start`/`agent_done` 事件驱动（`ui/model.go:510-550`），异步任务后台完成可直接复用。

### 决策记录（用户已确认）

| # | 决策 | 选择 |
|---|---|---|
| 1 | 委派工具形态 | **`handoff_to_agent` 加 `async:true`**（不新增独立 dispatch 工具）——让模型在同一个工具上判断同步/异步 |
| 2 | 查询通道 | **新增 `agent_poll(job_id)`** 只读工具（查状态/结果，done 后返回并移除） |
| 3 | 后台任务生命周期 | **随 Run 结束**：Run 退出 cancel 全部 + 丢弃未取结果，不跨 Run 持久 |
| 4 | 同步语义 | `/collab`、`/ratd` 不传 async = 同步，不变 |
| 5 | 嵌套异步 | **不支持**（depth > 0 忽略 async，同步运行）——YAGNI，先只解决主 agent 场景 |
| 6 | usage | 后台任务完成时经 `accumulateUsage` 累加（`usageMu` 线程安全），agent_poll 返回时展示 |

### 模型如何知道"还可以干别的活"

两处配合（缺一不可）：

1. **tool spec 描述**（`tools/subagent.go` Spec 的 parameters）：`async` 字段说明写清楚"true = 后台异步执行，立即返回 job_id，你可以继续其它工作，之后用 agent_poll(job_id) 查询结果；省略 = 同步等待"。
2. **每轮 pinned 后台任务摘要**：`executeTurn` 构建 messages 时，若有未取结果的后台任务，注入 pinned 消息（复用 `pendingPinnedMessages` 机制，不持久化、不污染历史）：
   ```
   [Background jobs] bg-1 (researcher): 调研缓存方案 — running
   ```
   模型每轮都看到"还有 N 个后台任务在跑"，自然知道"我可以继续干活，但别忘 poll 它们"。

## 架构

### 新增状态：Engine 后台任务表

`engine/loop.go` Engine 结构体新增（与 `steerQueue` 等并发字段并列）：

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
// Engine 新增字段：
bgMu    sync.Mutex
bgSeq   int
bgTasks map[string]*bgTask
```

`ctx` 派生自 Run 的 ctx（`context.WithCancel`）；后台子代理随 Run 结束而取消（不泄漏 goroutine/请求）。

### 工具定义

**① `handoff_to_agent` parameters 增加 `async`**（`tools/subagent.go` Spec 的 JSON）：

```json
"async": {
  "type": "boolean",
  "description": "true = 后台异步执行，立即返回 job_id，你可以继续其它工作，之后用 agent_poll(job_id) 查询结果；false/省略 = 同步等待（默认）。长时间独立任务（编译、测试、批量脚本、独立调研）建议用 async。"
}
```

`engine/agent.go` 的 `HandoffToAgentParams` 增加字段：

```go
// Async starts the sub-agent in the background and returns immediately with
// a job_id; the delegating agent polls it later via agent_poll. Only honored
// at depth 0 (main agent); nested delegations ignore it.
Async bool `json:"async,omitempty"`
```

**② 新只读工具 `agent_poll`**（`engine/agent.go` 新增 spec，仿 `askUserToolSpec`，引擎级拦截，不进 tools registry）：

```go
const AgentPollToolName = "agent_poll"
// agentPollToolSpec(zh): Name="agent_poll", 参数 {job_id: string (required)}
// 描述: "查询后台异步子代理任务的状态与结果。返回 running / done / error；done 时返回结果并移除该任务。"
```

`toolSpecsWithHandoff`（`turn.go:862`）追加 `agentPollToolSpec`。

### 数据流

1. 主 agent 调 `handoff_to_agent(async:true, agent, goal, ...)`。
2. `turn.go:632` handoff 分支照旧走 `e.tools.Execute` → `SubAgentTool.Run` → `Engine.RunSubAgent`（`handoff.go:103`）。
3. `Engine.RunSubAgent` 检测 `params.Async && depth == 0` → **异步分支**：
   - 生成 `job_id = "bg-<seq>"`，`context.WithCancel` 派生后台 ctx，注册 `bgTasks[job_id]`。
   - `go func(){ result, _ := agent.Run(bgCtx, handoff); accumulateUsage(result.Usage); resultChan <- result; emit agent_done }()`。
   - 立即返回 `ToolResult{Status:"ok", Digest:"Dispatched async job bg-1 (researcher): <goal>. Use agent_poll(bg-1) to check the result.", FinishReason: HandoffReasonAsyncRunning}`。
4. `turn.go` 正常收尾（`processHandoffResults` 记 running digest），**turn 不阻塞**，主 agent 下一轮继续。
5. 主 agent 后续任一轮调 `agent_poll(job_id)` → `turn.go` 的 `processAgentPollCalls` 拦截（仿 `processTodoWriteCalls`）：
   - 查 `bgTasks`；不存在 → error tool message（"job not found"）。
   - `select` 读 `result` channel：未就绪 → `{status:"running"}`；就绪 → 从表删除，返回 `{status:"done", summary, conclusions, usage}`。
6. Run 结束（`loop.go` Run 所有退出路径的 defer，紧邻 `persistHistory`）→ 遍历 `bgTasks` cancel 全部，日志记录未取任务，丢弃结果。

### 修改清单（最小、贴合现有模式）

| 文件 | 改动 |
|---|---|
| `engine/agent.go` | `HandoffToAgentParams.Async`；常量 `AgentPollToolName`；`agentPollToolSpec(zh)`（仿 `askUserToolSpec`） |
| `engine/loop.go` | Engine 结构体加 `bgTask`/`bgTasks`/`bgMu`/`bgSeq`；Run 的 defer 加 `cancelBackgroundTasks()` |
| `engine/handoff.go` | `Engine.RunSubAgent` 加 async 分支（goroutine + channel + agent_done 事件） |
| `engine/turn.go` | `toolSpecsWithHandoff` 追加 `agentPollToolSpec`；新增 `processAgentPollCalls` 拦截（仿 `processTodoWriteCalls`）；`executeTurn` 每轮注入 pinned 后台任务摘要 |
| `tools/subagent.go` | `Spec()` parameters JSON 加 `async` 字段描述 |
| `context/builder.go`（可选） | volatile 中暴露 `pending bg jobs: N`（增强可见性，可后置） |
| `ui/model.go` | 无新组件（复用子代理面板）；如需区分异步可在 agent_done 沿用现有逻辑 |

**不改**：`sub_agent.go` 的 `runLoop`（后台复用同一 `agent.Run`）、`handoff.go` 的 `runHandoff` 同步路径、`/collab`、`/ratd` 状态机。

### 错误处理

| 场景 | 处理 |
|---|---|
| 后台子代理 error | 结果写入 channel，`agent_poll` 返回 error；主 agent 决定重试/降级 |
| Run 结束未 poll | cancel + 丢弃，日志记录（含 usage 统计） |
| 并发 poll 同一 job | 幂等：查表返回当前状态；done 后首次 poll 移除（`bgMu` 保护） |
| `agent_poll` 参数缺失/不存在 | error tool message（同 `processTodoWriteCalls` 模式） |
| 嵌套 async（depth>0） | 忽略 async，同步运行（`Engine.RunSubAgent` 只处理 depth==0；`SubAgentRunner.RunSubAgent` 不处理） |
| 后台 goroutine 泄漏 | Run defer `cancelBackgroundTasks` + 可选 `wg.Wait()` 上限 |
| `agent_poll` 结果过大 | 复用现有 digest 截断（`briefDigest`），完整结果走 artifact |

### 测试

| 测试 | 验证 |
|---|---|
| `TestRunSubAgent_AsyncDispatch` | async=true → 立即返回 running（含 job_id），后台 goroutine 开始执行 |
| `TestAgentPoll_StatusFlow` | 未完成 → running；完成 → done + summary；之后 poll → not found |
| `TestAgentPoll_ConcurrentPoll` | 并发 poll 同一 job 幂等，无数据竞争 |
| `TestRun_CancelBackgroundOnEnd` | Run 结束 cancel 全部后台任务，无 goroutine 泄漏 |
| `TestHandoffAsyncSpec` | handoff spec 含 `async` 字段、`agent_poll` spec 存在 |
| `TestAgentPollNotInRegularCalls` | `agent_poll` 不进 regularCalls，不产生重复 tool message |
| `ui/` | agent_done 事件在异步完成时正常更新面板（现有机制，补断言） |

### 规格自检

- **占位符**：无 TODO/待定。
- **一致性**：async 由模型自主决定（能力优先）；`agent_poll` 与 `todo_write`/`load_skill` 拦截模式同构；后台任务表生命周期与 Run 一致，与 steer/pinned 机制一致。
- **范围**：聚焦主 agent 异步委派；嵌套异步、跨 Run 持久、并发上限均为 YAGNI 不纳入。
- **模糊性**："何时该异步"归模型判断，引擎只在 tool spec 给启发式（长时间独立任务）。

## 后续（不做，YAGNI）

- 嵌套子代理 async（depth > 0 异步）。
- 后台任务跨 Run 持久 + 会话恢复重建。
- `agent_dispatch` 独立工具、并发上限、任务取消（cancel 特定 job）等扩展。
- bash 异步后台任务（本设计的 agent 泛化机制已覆盖"长时间脚本交给子代理 async"场景，无需单做 bash 后台）。
