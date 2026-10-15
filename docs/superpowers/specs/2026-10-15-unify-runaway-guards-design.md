# 统一收敛边界与状态语义（拔除预算档位 / progress 守卫，估算器接管预算口径）

日期：2026-10-15
状态：已实现（本规格的所有改动已落地；验证见文末）

## 背景

上一份规格（`2026-10-15-remove-fixed-turn-limits-design.md`）删除了所有固定轮数上限，把收敛
交给 token 预算 + 三击守卫 + 完成契约。随后的代码审查（`docs/code-review-report/deepact-2026-10-15-删除固定轮数上限.md`）
发现「统一到通用机制」的前提没有被兜住，用户据此做了四条决策：

1. **预算的三态档位（`0=继承 / -1=无限 / >0=上限`）没有意义，直接拔除**——只保留"引擎算出来的
   一个预算"，不存在"放弃保护"的档位。
2. **Run 级 tree budget 同样拔除**：主 agent 不该有上限（用户会持久使用会话，也可能要求 agent
   连续跑 N 天的任务，"设上限就是限制能力"），所以只剩"无上限"一种情况 = 不需要该机制。
3. **主 agent 的 progress 守卫删除**："有没有进展"应作为能力交给 LLM 判断（AGENTS.md 能力优先），
   而不是由引擎规则代判。
4. **预算口径改用自家估算器**，第 1 轮即生效（不再依赖 provider 上报 usage）。
5. 另附两条小修：无效参数调用计 strike；状态判定按 `FinishReason` 统一。
6. 配置侧：**已删除的配置键必须 fail-loud**，不能静默失效。

## 目标

- 收敛边界只有一个来源、一个口径：**引擎按 context window 算出的 per-run token 预算**。
- 主 agent 不再有任何轮数/进度/花费上限：唯一终止来源是模型自身收敛（`task_complete`）、
  用户中断、scope/stop-hook 守卫。
- 状态语义只有一个实现，`ok/error/cancelled` 与 follow-up pin 同口径。

## 改动点

### A. 拔除预算档位（`engine/agent.go`、`engine/sub_agent.go`、`engine/types.go`、`config/config.go`、`cmd/run.go`）

- 删 `Handoff.TokenBudget`、`AgentSpec.TokenBudget`、`specSubAgent.Run` 的合并逻辑。
- 删 `SubAgentRunner.tokenBudget` + `SetSubAgentTokenBudget`；`tokenBudgetFor` 收敛为
  `runTokenBudget() = defaultSubAgentBudgetWindows(2) × contextLimit()`（不再有 0/-1/>0 分支）。
- 删 Run 级 tree budget 全家：`treeBudget`、`treeSpent`、`SetRunTokenBudget`、准入检查、
  `HandoffReasonTreeBudgetExceeded` 与 heading、`treeBudgetRefusalText`、模型侧 tree 预算通知、
  `EngineConfig.RunSubAgentTokenBudget`。
- 删 `EngineConfig.SubAgentTokenBudget`、`[context].sub_agent_token_budget`、`[agents.X].token_budget`
  及其三处 fail-loud 校验；`cmd/run.go` 去掉两个 `Set*` 调用。
- **保留**并发宽度（6 in flight）与嵌套深度（2）：它们是物理/递归保护，不是预算档位。

### B. 预算口径改由估算器接管（`engine/sub_agent.go`）

- 每轮在循环顶部测量 `cur := estimateModelHistoryTokens(history)`，累加 `estSpent += cur - prevTokens`
  （上一轮之后新增的内容=prompt 增长=天然 cache miss，与 billable 定义一致），首轮从 0 起算，
  因此**第 1 轮就有预算口径**。
- 生效口径 `spent = max(estSpent, providerBillable)`：provider 上报时取更准的，不上报时估算独自顶上。
- 返回给父层的 `Usage` 仍是 provider 上报值（成本核算口径不变）；估算只用于收敛判定与提示。
- 80% 收尾提示与硬门共用这一个 `spent`。

### C. 删除主 agent 的 progress 守卫（`engine/loop.go`、`engine/turn.go`、`engine/guards.go`）

- 删 `Engine.progressLoop`（初始化/Reset/Check 的 nudge 与 `loop_detected` 阻断）、`progressKeys`、
  `TurnResult.MadeProgress` 及其计算、`madeProgressFromCalls`、`infoKeyFor`。
- 删 `guards.go` 的 `LoopTracker`/`NewLoopTracker`/`Check`/`Reset` 与 `GuardDiagnose`/`GuardBlock`
  （保留 `GuardAction`/`GuardAllow`/`GuardAskUser` —— ScopeGuard 仍在用）。
- **保留 `annotateRepeats`**（`engine/key.go`）：把"同一目标本 run 第 N 次调用"作为客观事实贴在
  工具结果上，模型自行决定——这是"有没有进展"的能力通道，与子 agent 现在的做法一致。
- 主循环退出只剩：ctx 取消、`Blocked`（scope guard / awaiting_user / stop-hook 耗尽）、`Done`。

### D. 无效参数调用计 strike（`engine/sub_agent.go`）

新增 `consecutiveInvalidCalls`：参数非法的 `submit_result` 与参数非法的 `ask_user` 各计 1，
成功派发时清零；达到 `blockedStrikeLimit`(3) → 结构化 run `no_result`、非结构化 `loop_detected`。

### E. 状态判定统一按 `FinishReason`（`engine/loop.go`、`engine/handoff.go`）

`handoffResultStatus` 改为排除表：`completed`/空 → `ok`；`awaiting_user`、`async_running` → `ok`
（不是失败）；`cancelled` → `cancelled`；其余 → `error`。同步路径（`handoff.go`）改调同一函数
（消除两份判定各写一份的漂移）。附表驱动用例覆盖全部 reason。

### F. 已删除配置键 fail-loud（`config/config.go`）

`Load` 改用 `toml.Decode` 取 `MetaData`，对 `Undecoded()` 报错：命中已删除的键
（`max_iterations`、`token_budget`、`sub_agent_token_budget`、`run_sub_agent_token_budget`）时给出
"该键已移除，收敛由引擎预算负责"的明确提示；其余未知键同样报错（消除"写了不生效"的静默类）。
**注意**：这是行为变更——任何未知键都会让启动失败。

## 已知取舍

- 主 agent 从此没有任何花费/轮数上限：一次 Run 的花费只受用户中断与模型自身收敛约束。这是用户
  明确接受的取舍（"设上限就是限制能力"）。
- 子 agent 的 per-run 预算是它唯一的收敛机制（否则持续产生有效工具调用的 run 没有出口），
  倍数 `2 × context window` 与 context 同尺度，随模型窗口自适应；改倍数是一行常量的事。

## 决策补充（2026-10-15 第二轮确认）

1. **Run 级 total 预算确定不恢复**：per-run 是 `2 × context window`、并发宽度是 6，在途上限已
   经是 `12 × context window`；原 `20 × context window` 的 Run 级总量不但宽松还只在准入点检查，
   确实是多余的一层（用户："这个 total 的 20 × context window 确实没意义了"）。
2. **per-run 预算暂不做配置入口**：保持引擎固定的 `2 × context window`，先跑一阵看实际表现，
   需要可调时再加（只接受正数，不重新引入"无限"档位）。
3. **主 agent 保留的约束（本轮逐项确认后保留，理由如下）**：
   - 用户中断（Esc / ctx 取消）、scope guard（危险 bash 确认）、ask_user（等用户回答）：
     分别是用户主导权、安全护栏与能力通道，不是引擎给模型设的工作上限。
   - **stop-hook 耗尽守卫（`stalled_narration_exhausted`）保留**：它不限制"能做多少"，而是一道
     完成契约——模型反复宣告"完成"却没给出结论时，引擎不接受这个假 Done。把它归为
     轮数/花费上限一并删掉，会让"只说计划就结束"直接成为成功终态，与项目对"假完成"的一贯
     容忍度相反（参见 2026-07-07 / 2026-07-11 的 stop-hook 设计与判定）。如果将来想验证
     "完全由模型决定何时结束"，它是最该先放开的那个旋钮，但属另一议题。
   - `max_output_tokens`（单次回复上限）由 API 协议决定，不可删。
   - 异步未回收 8 / 挂起表 4 属子 agent 侧控制，保留。

## 验证

- `go build ./...`、`go vet ./...`
- `go test ./... -count=1 -race -timeout=180s`
- 新增用例：无效调用 3 击用例、`handoffResultStatus` 全 reason 表驱动用例、已删除配置键 fail-loud 用例

## 实现记录（落地时的补充说明）

- **A + B 合并**：预算口径与估算器一起改——`spent = max(provider 上报, 引擎估算)`，估算量取
  「本轮请求历史的增量」（= 新增的 cache-miss 内容），第 1 轮即生效。
- **Run 级 tree budget 与 `SubAgentBudget` 接口、引擎侧预算通知一并删除**（用户决策：主 agent
  不应有上限）。`[context].run_sub_agent_token_budget` 随之消失。
- **并发宽度（6）与嵌套深度（2）保留**：它们是物理/递归保护，不是预算档位。
- **progress 守卫删除的连带**：`engine/guards.go` 只删 `LoopTracker`/`NewLoopTracker`/`Check`/`Reset`
  与 `GuardDiagnose`（`GuardAction`/`GuardAllow`/`GuardBlock`/`GuardAskUser` 仍是 ScopeGuard 的词汇）；
  `buildProgressNudge`/`buildProgressBlockMsg`/`madeProgressFromCalls`/`infoKeyFor`/`canonicalArgs`/
  `TurnResult.MadeProgress`/`Engine.progressKeys`/`observedAgentStates`/`noteAgentState(s)` 全部删除；
  `engine/progress_loop_test.go` 删除，`guards_test.go` 只删 LoopTracker 用例（其余键/ScopeGuard 用例保留），
  `guards_normalize_test.go` 与 `read_multi_integration_test.go` 的两个守卫用例改写为断言
  `annotateRepeats` 的重复计数（保住了“不同路径形式必须归并到一个 key”这个回归点）。
- **M2 新增 `consecutiveInvalidCalls`**：非法 `submit_result`（→ `no_result`）与非法 `ask_user`
  （→ `loop_detected`）各计一击，达 3 击结束，真实派发时清零。
- **M3 `handoffResultStatus` 成为唯一实现**：`Blocked`/`BlockedBy==cancelled` 优先，其余按
  `FinishReason` 排除表；同步路径（`handoff.go`）改调同一函数，附表驱动用例覆盖全部 reason。
- **F 配置严格解码**：`Load` 改用 `toml.Decode` + `md.Undecoded()`，任何没被读取的键都报错；
  已删除的 5 个键（`max_iterations`/`max_turns`/`token_budget`/`sub_agent_token_budget`/
  `run_sub_agent_token_budget`）给出“已删除 + 替代机制”的明确提示。
