# 删除所有固定轮数上限（子 agent MaxIterations + 主 agent MaxTurns）

日期：2026-10-15
状态：已批准（方案 A：彻底删除轮数上限，统一到守卫 / 预算 / 完成契约）

## 背景与问题

2026-08-31 已经删掉子 agent 的**默认** 99 轮上限，但"轮数是收敛手段"的设计并没有
消失，它以两种形式留在代码里：

1. **内置角色的硬编码轮数**：researcher 15、proposer 20、redteam 20、arbitrator 5
   （`engine/default_agents.go:166/180/195/210`）。
2. **主 agent 的硬编码轮数**：`MaxTurns: 999`（`cmd/run.go:528/555`），
   `engine/loop.go:593` 在 `<=0` 时兜底 50。

固定轮数上限是糟糕的设计：它把"这个任务需要多少轮"这个只有模型知道的问题，用一个
事先写死的数字回答。写小了在正常任务中途截断（2026-08-31 的调研截断即此），写大了
（999）等于没有限制——`999` 既不是保护也不是收敛，只是把跑飞的边界推远到看不见。
按 AGENTS.md 的泛化性 / 能力优先原则，收敛应由**可被证据触发的通用机制**负责，而不是
由固定轮数负责。

用户决策：**彻底删除轮数上限**，子 agent 与主 agent 统一使用已有的防跑飞机制。

## 目标

1. 删除所有设置固定轮数上限的地方（字段、配置项、预设值、兜底默认值）。
2. 两级循环都变成"无轮数上限"，收敛完全交给通用机制。
3. 不新增机制；60% 轮次收尾提示因失去承载对象而删除，token 版 80% 收尾提示保留。

## 统一后的防跑飞机制（本设计依赖的既有能力）

子 agent（`engine/sub_agent.go`）：

| 机制 | 位置 | 触发 |
|---|---|---|
| token 预算 | `tokenBudgetFor`（默认 2×context window） | billable token 用尽 → `budget_exceeded` |
| 80% token 收尾提示 | `sub_agent.go:780` 武装、:658 注入 | 提示模型收尾 |
| 纯文本 3 击 | `narrationStrikeLimit` | `stalled_narration` |
| 输出截断 3 击 | `truncationStrikeLimit` | `max_tokens` |
| 越权调用 3 击 | `blockedStrikeLimit` | `loop_detected` |
| 结构化完成契约 | `submit_result` | 结构化 run 的唯一正常完成 |
| 并发 / Run 预算 / 深度 | 6 / 20 windows / 2 | `fanout_limit` / `tree_budget_exceeded` / `max_depth` |

主 agent（`engine/loop.go`）：

| 机制 | 位置 | 触发 |
|---|---|---|
| 进度守卫 | `progressLoop.Check` | nudge → `loop_detected` 阻断 |
| 纯文本收敛守卫 | `turn.go:491` | `stalled_narration_exhausted` |
| 完成契约 | `task_complete` | 正常收敛 |
| 用户中断 | `ctx` 取消 | `cancelled` |

## 方案决策

### A 彻底删除轮数上限（用户已选）

删除字段、配置项、预设值、`max_iterations` 结束原因、60% 轮次收尾提示、主 agent
`MaxTurns`。子 agent 与主 agent 都不再有轮数边界。

### 未采纳的方案

- **B 只删内置预设，保留字段与配置项**：轮数上限作为"能力"仍在，代码里仍会有角色设
  它，收敛手段没有真正统一。
- **C 只保留 `[agents.X] max_iterations` 作为唯一入口**：等于把写死的数字从代码搬到
  用户配置，跑飞风险仍在，且多出一处需要维护的配置面。

## 改动点

### 1. `engine/agent.go`

- 删 `Handoff.MaxIterations`（:89-90）。
- 删 `AgentSpec.MaxIterations`（:193）。
- 删 `HandoffReasonMaxIterations` 常量（:40）与 `handoffReasonHeading` 的对应 case
  （:636-637）。
- 删 `HandoffResult.TimedOut`：唯一设置点在删除后不可达的尾部兜底，成为死字段。

### 2. `engine/default_agents.go`

- 删 `researcherSpec` / `proposerSpec` / `redteamSpec` / `arbitratorSpec` 的
  `MaxIterations` 预设。
- 删 `specSubAgent.Run` 里的 spec → input 合并（:85-87）。
- `researcherSpec` 注释重写：删除"capped at 15 / 旧 30 轮 / 六方向 15M prompt
  tokens"的轮数论证，改为"上限由 per-run token 预算 + 80% 收尾提示承担"。

### 3. `engine/sub_agent.go`

- `runLoop` / `runLoopFrom` 去掉 `maxIterations` 参数；`Run`、`runLoopResume` 调用点同步。
- 循环条件改为无上限：`for iter := startIter; ; iter++`（每条退出路径都是循环内的
  `return`，无 `break`，因此循环后代码不再可达）。
- 删 60% 轮次收尾提示全套：`budgetNudgeAt`、`budgetTailNudge`、`budgetNudgeFraction`、
  循环内注入分支与 `budgetNudged` 变量。
- 删循环后的 max-iterations 兜底块（:1051-1064，不可达）。
- `SuspendedRun.Iter` 保留（resume 从断点继续计数），仅更新注释。

### 4. `config/config.go`

- 删 `agentRoleConfig.MaxIterations`（:72）、映射（:475）、结构体注释里的
  `max_iterations = 15` 示例行。
- 存量 `.deepact/config.toml` 里残留该键：`toml.Unmarshal`（BurntSushi）不校验未知键，
  静默忽略，不额外加报错。

### 5. 主 agent

- `engine/types.go`：删 `EngineConfig.MaxTurns`。
- `cmd/run.go`：删两处 `MaxTurns: 999`。
- `engine/loop.go`：删 `maxTurns` 计算（含 `<=0 → 50` 兜底）、`turns >= maxTurns`
  分支、`max_turns` 事件与 Blocked 返回。主循环退出只剩 ctx 取消、
  `turnResult.Blocked`、`turnResult.Done`(+steer)、进度守卫阻断。

## 测试改造

- **删除**：`TestSubAgentRunLoop_MaxIterationsReason`；`sub_agent_finish_test.go` heading
  表里的 max_iterations 项；`default_agents_test.go` 的 `== 15` 断言；
  `config_test.go` 的 max_iterations 断言；`turn_agent_poll_test.go:176` 的
  `isHandoffFollowUpReason(HandoffReasonMaxIterations)`；`sub_agent_budget_nudge_test.go`
  中针对轮次 nudge 的用例。
- **改写**：`sub_agent_budget_test.go` 中两个以 `MaxIterations:3` 触发 max_iterations 的
  用例，改为用 `TokenBudget` 触发 `budget_exceeded`（同义：预算耗尽），保留"预算耗尽仍
  返回部分结论 + usage"的断言价值。
- **删参数**：其余测试中作为"stub 保险丝"的 `MaxIterations: N`（`sub_agent_askuser_test.go`
  等 10 个文件）。已验证 `stubSeqModel` 脚本耗尽后返回空响应 → 走 3 击守卫收敛，
  不会死循环；仍以 `-timeout=120s` 全量跑确认。
- `sub_agent_remove_turn_limit_test.go` 保留，作为"无轮数上限"的回归锁。

## 已知取舍

- 子 agent 没有"重复相同调用"的硬守卫：重复次数只作为事实报告给模型
  （`sub_agent.go:930`）。大量 cache hit 的重复读令 billable token 增长慢，理论上可在
  token 预算内运行很久。删掉 15 轮上限后，这个洞的边界从"15 轮"变成"token 预算"。
  补一个基于 `(tool, args)` 签名的通用重复守卫属于另一议题，本次不动。
- 主 agent 没有 token 预算兜底，删掉 `MaxTurns` 后只有进度守卫 / 纯文本守卫 /
  完成契约 / 用户中断。这是用户明确接受的取舍：999 不是保护，只是把边界推远。

## 验证

- `go build ./...`
- `go test ./... -count=1 -race -timeout=120s`
- `go test ./engine/ -run 'NoTurnLimit|Budget|Guard' -v -count=1`

## 数据流

```
Run(ctx, input)
  └─ runLoop(ctx, input, extra)                 // 无轮数参数
       └─ for iter := start; ; iter++           // 无轮数上限
            ├─ token 预算耗尽   → budget_exceeded
            ├─ submit_result   → completed
            ├─ 纯文本 3 击      → stalled_narration / no_result
            ├─ 越权调用 3 击    → loop_detected
            └─ 输出截断 3 击    → max_tokens

Engine.Run(ctx)
  └─ for { executeTurn(); if Done → break }     // 无 MaxTurns 检查
       ├─ 进度守卫阻断 → loop_detected
       ├─ 纯文本守卫   → stalled_narration_exhausted
       └─ ctx 取消     → cancelled
```
