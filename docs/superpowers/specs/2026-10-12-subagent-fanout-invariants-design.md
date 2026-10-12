# 子代理委派树不变量（宽度 / 深度 / token 聚合）设计

日期：2026-10-12
状态：已批准（用户拍板：方案 A，宽度默认 6）

## 问题

委派树的规模没有任何确定性边界，三类"膨胀"各自缺一条不变量：

| 维度 | 现状 | 证据 |
|---|---|---|
| 同步扇出 | **完全无上限**。一轮内 N 个 `handoff_to_agent` → `Executor.Execute` 一调用起一个 goroutine，无计数器 | `tools/registry.go:96-105` |
| 异步在飞 | 有上限（默认 8，可配） | `engine/handoff.go:332-339`；`config/config.go:142-145` |
| 挂起条目 | 有上限（默认 4，可配），每条持一份完整 history | `engine/loop.go:183-186` |
| 嵌套深度 | **固定 2，不可配置**：`SetMaxDepth` 存在但没有任何配置键调用它 | `engine/sub_agent.go:97,202-206`；`cmd/run.go:413` 只读不写 |
| token | **只有 per-run 上限**（默认 2× 子代理上下文窗口），无 Run 级聚合 | `engine/sub_agent.go:1324-1339`；`config/config.go:137-141` |

已有的间接约束：子代理 LLM 客户端槽位 `min(4, maxConcurrent/2)`（`cmd/run.go:471-473`）限住了**烧 token 的速率**，但**同时活着的 run 数**没有任何上限——每个 run 各持一份独立 history（挂起注释标注 worst case ~1M，`engine/loop.go:185-186`），这才是真正的膨胀面。

## 决策

**加三条确定性不变量，不加任何"跑飞判定"。**

判据来自 AGENTS.md：不变量约束的是**资源上界**（安全/不可逆/资源耗尽类），不是"行为是否理想"。模型的行为（拆几个方向、铺宽还是嵌套、何时收敛）仍归模型；引擎只保证"总共就这么多"。

与 codex 的对照（codex-rs 实测）：codex 同样没有轮次上限、没有无进展检测、没有 per-agent 超时；它的规模控制恰好就是**拓扑上限**（`DEFAULT_AGENT_MAX_DEPTH = 1`、并发 6/4）+ 预算信息 + 授权边界。本方案对齐它的第 1、3 层，**不引入它的第 2 层（授权边界）**——deepact 的委派是模型随时可用的能力，收紧授权会与能力优先冲突。

另外三处不做（YAGNI，且理由充分）：
- **不加全局轮次/token 推理上限**：已有 per-role `MaxIterations` 与 per-run token 预算。
- **不加"无进展检测"**：2026-09-11 的 `remove-heuristics-unify-loop-guards` 已删过一轮启发式判定；codex 也没有。
- **不改斜杠技能的行为**：`/collab` 的 2-6 方向仍可在一轮内并行发出（宽度默认 6 的选择即为对齐它）。

## 统一收口点

四条委派路径全部汇经 `SubAgentRunner.runLoopFrom`：

| 路径 | 调用链 | 经过 runLoopFrom |
|---|---|---|
| 同步 depth 0 | `Engine.RunSubAgent` → `runHandoff` → `specSubAgent.Run` → `runner.Run` | ✓ |
| 异步 | `dispatchAsync` → goroutine → `specSubAgent.Run` → `runner.Run` | ✓ |
| 嵌套 depth>0 | `runner.RunSubAgent` → `runHandoff` → 子 `specSubAgent.Run` → `runner.Run` | ✓ |
| 续跑 | `specSubAgent.RunSuspended` → `runner.RunSuspended` → `runLoopResume` | ✓（`runLoopFrom` 是共同实现） |

registry 中的每个 `specSubAgent` 都持有**同一个** runner 实例（`cmd/run.go:254,289`），所以"在跑计数"与"Run 累计 token"都能放在 runner 上，一处生效、四条路径统一。这是本方案的关键前提，也决定了不变量放在 runner 而非 Engine。

## 不变量 1：委派树在跑上限（宽度）

**语义：整个委派树"同时在跑"的子代理总数 ≤ N，跨同步/异步/嵌套统一计数。** 由模型自主决定把预算花在"铺宽（6 个浅方向）"还是"嵌套（4 个方向 + 2 个子委派）"——预算是确定的，分配归模型。

- **计数点**：`runLoopFrom` 入口 CAS 占位，出口（含所有 return 路径）释放。用 defer 保证异常路径不泄漏。
- **挂起不占位**：run 停在 `awaiting_user` 会离开 `runLoopFrom`（挂起条目无 goroutine、无在飞请求），因此自动不占槽——与 async outstanding 上限的既有判据一致（`engine/handoff.go:330-331`）。
- **超限行为：fail-loud 拒绝**（不排队）。排队会让"一轮内 N 个并行调用"产生不可预期等待，且 UI 表现为卡住。错误经既有 digest 通道回给模型：

  ```
  Sub-agent limit reached (6 in flight). Currently running: [researcher×3, critic]. 
  Wait for a result (agent_poll / wait_agent) or run this step yourself, then delegate again.
  ```

  文案承载模型教育：这一轮被拒的委派**不是终点**，模型可选择等结果或自己做——与 `max_outstanding_async_subagents` 的既有文案风格一致（`engine/handoff.go:332-339`）。
- **新 FinishReason**：`fanout_limit`，进 `HandoffReason*` 词表 + `handoffReasonHeading` 标题 + `isHandoffFollowUpReason`（半途被拒的委派需要父代理继续，与 `max_iterations` 同类）。
- **默认 6**：与 `/collab` 的"2-6 个方向"对齐（`skill/builtin/collab/SKILL.md:14`）——正常多方向调研不受影响；6 方向打满时嵌套委派会被拒（可见、可恢复、可配）。这是**有意的预算语义**：总量固定，分配归模型。

## 不变量 2：嵌套深度（可配置）

- 现状 `maxDepth` 默认 2（`engine/sub_agent.go:97,202-206`），检查点 `input.Depth > r.MaxDepth()` 已存在（`:285-290`）。
- 新增 `[context] max_subagent_depth`，`cmd/run.go` 接上 `runner.SetMaxDepth(...)`；**默认保持 2**——不改现行为，避免破坏既有嵌套委派用法。
- 违规仍走既有 `HandoffReasonMaxDepth` 路径，无新机制。
- `resume` 路径天然受同一检查约束（续跑复用原 `Handoff.Depth`）。

## 不变量 3：Run 级 token 聚合上限

**语义：一次 Run 内、整棵委派树消耗的计费 token（cache-miss + completion）总和 ≤ B。**

- **计数点与宽度同源**：`runLoopFrom` 出口把本 run 的 `totalUsage` 中 `CacheMissTokens + CompletionTokens` 累加到 runner 的原子计数器。这条路径覆盖**每一个** run（含嵌套子 run 自己的 `runLoopFrom`），因此聚合是真实的——**不依赖 `Engine.runUsageAccum`**。
- **Run 作用域**：Engine 在 Run 开始时经新增的可选依赖 `EngineDeps.SubAgentBudget`（`*SubAgentRunner` 实现）调用 `StartSubAgentRun()` 清零；Run 内不回滚。
- **超限行为**：拒绝新的委派（fail-loud digest，**不中断正在跑的 run**）。**不复用** `budget_exceeded`：既有词表的每个 reason 都对应"这个 run 跑了一半停下"的 partial-result 语义与标题（`engine/agent.go:646-679`），而被拒的委派**什么都没跑**，套用会产生误导性标题。因此新增 `tree_budget_exceeded`，与 `fanout_limit` 同属"拒绝"类（两者都是父代理需要继续的 follow-up reason）。语义上与现有 `tokenBudget` 硬停一致，但作用域从"单个 run"升到"整棵树的本次 Run"。
- **阈值提示**：达到 80% 时注入一条 pinned 提示（复用 `pendingPinnedMessages` 机制，不持久化），告知剩余总量并提示收敛。这是**信息而不是刀**，与 `tokenBudgetNudge` 的设计一致（`engine/sub_agent.go:1346-1360`：把剩余量告诉模型，由模型决定怎么收）。
- **默认值 20× 子代理上下文窗口**（≈20M）。参考量级：一次 6 方向 `/collab`、每个 researcher 15 轮、1M 上下文窗口的正常开销远低于此；默认值的角色是"防失控"而非"日常预算"。
- **已证实的既有缺口（不在本次范围）**：`Engine.runUsageAccum`（`engine/loop.go:1024-1037`）只累加 depth-0 委派返回的用量，嵌套子 run 的用量在 `runHandoff` 的 nested 分支被丢弃（`engine/handoff.go:424` `accumulate: nil`，且 `ToolResultEnvelope` 不携带 usage）。本方案在 runner 侧独立计数从而不受该缺口影响；修正 `runUsageAccum` 的嵌套归集列为后续项，不与本方案耦合。

## 配置

| 键 | 默认 | 语义 | 违规 |
|---|---|---|---|
| `[context] max_concurrent_subagents` | 6 | 委派树在跑上限（跨同步/异步/嵌套） | fail-loud 拒绝新委派 |
| `[context] max_subagent_depth` | 2 | 嵌套深度 | 既有 `max_depth` 拒绝 |
| `[context] run_sub_agent_token_budget` | 0（=20× 上下文窗口） | 本次 Run 整棵树计费 token 上限；-1 = 无限 | 拒绝新委派 + 80% 提示 |

校验沿用既有模式（`config/config.go:415-431`）：非法值启动期 fail-loud（负深度、0 并发、< -1 的 token 预算），错误文案给出合法取值。

## 模型可见面

三处同步（与 `2026-09-24-subagent-readonly-universe-design.md` 的"模型可见面四处同步"同法）：
1. 三条 fail-loud digest（宽度/深度/token），双语，列出建议动作。
2. 80% token 提示（pinned，双语）。
3. `handoff_to_agent` 工具描述补一句总量语义（"同时最多 N 个子代理在跑；铺宽还是嵌套由你决定"），进 stable 前缀，一次性缓存失效可接受。

## 测试

- **宽度**：单轮 N 个 handoff，第 N+1 个收到 fail-loud digest、前 N 个正常完成；异步+同步混合计数；嵌套占同一预算；挂起不占位；出口释放（run 结束后可再次委派）；并发 `-race`。
- **深度**：`max_subagent_depth` = 1/2/3 各自生效；`resume` 路径同样受限；默认 2 时既有行为不变（回归）。
- **token**：超限后新委派被拒、在跑 run 不受影响；80% 提示只注入一次；`-1` 显式无上限不被默认值覆盖（沿用三态语义）。
- **回归（关键）**：`/collab` 6 方向仍能一轮并行发出并全部完成。

## 破坏性变更

无。三个默认值都保持在既有正常用法以内（深度维持 2；宽度 6 覆盖 `/collab` 上限；token 20× 远高于日常）。唯一的可观察变化是"失控用法"（一轮发 >6 个委派、或本次 Run 烧掉 >20M token）现在会被 fail-loud 拒绝，而此前是静默继续。
