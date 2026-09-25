# 子代理挂起-恢复（suspend / resume）设计（v2 · 已实施）

- 日期：2026-09-24
- 状态：**已实施**（单 PR）。实现计划见 `docs/superpowers/plans/2026-09-24-subagent-suspend-resume.md`
- 前序：A1+A2 只读宇宙 + 执行闸（`6edb5fc`）；A3 资源护栏（token 预算 + async 未清账上限）；B2 档 1（ask_user 携带部分发现 + heading 修正）
- 范围：进程内**跨 Run**、**不跨会话**（A-lite）

## 问题：档 1 之后，原始上下文仍被焚毁

档 1 已让父代理在子代理提问时看到 **问题 + 文字化发现**。残余损失三条：

| # | 残余损失 |
|---|---|
| 1 | **原始上下文不可恢复**：子代理读过的文件内容、tool 结果、中间推理随 `runLoop` 返回而消失；父代理重新委派 = 从零重读 |
| 2 | **缓存与预算双重重置**：新 run 走新 partition（整段前缀重新 cache-miss）、预算从 0 起算 |
| 3 | **是否保住上下文全凭模型裁量**：档 1 只把材料送达父代理，没有任何引擎保证 |

## 决策：挂起，而不是焚毁

**ask_user 终止时把可恢复状态存成 job 表中 `awaiting_user` 的一条；用户回答后由模型调用 `agent_resume` 把回答喂回同一个 run，从断点续跑。**

两条边界（AGENTS.md 能力优先）：

- **不写死"下一条用户消息 = 答案"**：用户可能换话题，回填由模型显式调用工具决定。
- **不新造概念**：挂起 run 就是 job 表中的一个状态，复用 `bgTasks` / `agent_poll` / pinned 摘要三套现成机制。

**生命周期边界**：挂起态**本身**跨 Run 存活（无在途工作）；一旦被 resume 变回 `running`，就与既有 async job 同生命周期——**随回答那一轮的 Run 结束而 cancel、结果丢弃**。

## 不变量

| # | 不变量 |
|---|---|
| I1 | 回填**键控 `PendingToolID`**：`PendingToolMissing==true` → 末尾**追加**；`false` → **替换**占位 tool 消息内容。禁止在 `assistant(tool_calls)` 与其 tool 响应之间插消息；**不得按位置定位**（同条 assistant 可带 `[read, handoff]`） |
| I2 | 恢复复用原 partition 与已 Fork 的 client（重建会让整段 history 重新 cache-miss） |
| I3 | 恢复沿用 `Spent`（token 预算）、`Iter`（迭代预算）、`BudgetNudgedTokens`（nudge 一次性） |
| I4 | Run 结束**不**清 `awaiting_user` 条目（`running` 条目照旧清并 cancel） |
| I5 | 挂起与在跑**分开计数**、各自 fail-loud；一切放弃 fail-loud，绝不静默丢弃 |
| I6 | 登记在**每一层各自发生**（`runHandoff` 内，早于 digest 格式化） |
| I7 | 嵌套时子级登记失败 → 父级也不挂起（退回现状：问题冒泡 + 重新委派） |
| I8 | **对外只暴露顶层句柄、resume 落点是叶子**：`agent_resume(顶层)` 由引擎沿 `ChildRunID` 下钻到叶子回填；更深层 handle 不渲进任何模型可见面 |
| I9 | 挂起条目的 **`result` channel 在登记时创建**（nil channel 在 `select{…, default:}` 里恒走 default → 结果静默丢弃、poll 永远 "still running"） |

I5 归入既有"满容行为三分通则"第 2 类（语义要求立即返回 → fail-loud），不自定义第五类。

**与只读宇宙 spec:17 的关系**：该 spec 曾以"气泡使子代理以 `awaiting_user` 终止、history 焚毁"为由否决"危险命令任意深度人审"。本机制**只消除其中一条理由**；其余三条（只护 bash 不护 write/edit、`pendingAskUser` 单槽覆盖——本批顺带修、工程量逼近 dsh 四层守卫）仍成立，且只读宇宙（`6edb5fc`）已从根本替代该方案。**该否决继续有效。**

## 实现

| 部位 | 形态 |
|---|---|
| 表与状态 | `bgTask.state`（`running` / `awaiting_user`）+ `suspended *SuspendedRun` + `childRunID`；`cancelBackgroundTasks` 只清 running；`countByStateLocked` / `inFlightCount` / `suspendedCount` |
| 断点状态 | `SuspendedRun{History, Question, PendingToolID, PendingToolMissing, Input, Partition, Model, Spent, Iter, BudgetNudgedTokens, ModelName, ChildRunID}`，经 `HandoffResult.Suspended`（`json:"-"`）回传 |
| 登记 | `handoffOptions.register`（N1 方案 (b)：`SubAgentRunner` 无 Engine 引用，故注入 registrar）→ `runHandoff` 在**格式化 digest 之前**登记并把 handle 写进 `ToolResult.RunID`；async 路径用 `suspendExistingJob` **沿用同一 job id** 翻转为挂起（不写 channel、不发 `agent_done`） |
| 恢复 | `agent_resume(run_id, answer)` → `takeLeafForResume`（校验 + 沿 `ChildRunID` 下钻 ≤16 跳 + 认领叶子）→ goroutine `resumeSuspendedJob` → 角色 → `SubAgentRunner.RunSuspended` → `runLoopResume`（**先 `fillPendingToolResponse` 再进循环体**，否则首轮顶部的 nudge 会插进 assistant 与其 tool 响应之间） |
| 级联 | `finishResumedJob`：再次挂起 → 条目回 awaiting_user（刷新 TTL）；否则反查父条目 → 用**子的 digest** 回填父的待回填项并恢复父 → 递归；无父（顶层）→ 结果投递到顶层句柄 channel 由 `agent_poll` 取。整链跑在"回答那轮 Run"的 ctx 上 |
| 模型可见面 | ① 新工具 `agent_resume`；② `agent_poll` 对挂起条目返回 waiting 文案 + 指向 `agent_resume`；③ pinned 摘要三态渲染（叶子 waiting for user input / 中间层 waiting for a sub-agent result / 其余 running）；④ digest 的 awaiting_user heading 带 handle（**仅 depth 0**）；⑤ `buildHandoffFollowUp(reason, zh)`：awaiting_user 指向 `agent_resume` |
| TTL / 容量 | `suspendedJobTTL = 30min`，**惰性判定**（poll / resume），无 timer；`[context].max_suspended_subagents`（默认 4，负数启动报错），满时新提问不挂起（I5/I7） |

## 决策记录

| # | 决策 |
|---|---|
| D1 | 回填通道 = `agent_resume(run_id, answer)`，不让引擎自动认领"下一条用户消息"（防误绑） |
| D2 | 嵌套链 = **链式级联**（每层记 `childRunID`，子完成后回填父级并递归恢复） |
| D3 | `max_suspended_subagents` 默认 **4**；TTL **30 分钟**、惰性判定 |
| D4 | 恢复交付 = 非阻塞，结果由 `agent_poll` 取 |
| D5 | 恢复后的 run 与既有 async job 同生命周期（随回答那轮 Run 结束 cancel） |
| D6 | 范围 = A-lite：跨 Run、不跨会话 |
| D7 | 登记采用 registrar 注入（不把胖 `SuspendedRun` 塞进 `ToolResult`） |
| D8 | 沿用 `bg-N` 命名空间；挂起与在跑计数分离 |
| D9 | `agent_resume` 不给子代理（它没有用户通道） |
| D10 | 挂起表满 / TTL 过期一律 fail-loud |
| D11 | 不重开只读宇宙已否决的"危险命令任意深度人审" |
| D12 | 容量按 A3 同规格落 `[context]`；淘汰/过期时明确告知，不静默 |
| D13 | 模型可见 handle 只有顶层（`depth==0`）；resume 落点是叶子 |

## 实施记录（与计划的偏差，均已记录）

1. **机器码在 `engine/suspend.go`**，不是 `engine/resume.go` —— 后者是**会话恢复功能**（`RebuildHistory` / `DefaultResumeBudget`）的文件名，不可占用。
2. `RegisterSuspended` **导出**（`cmd` 是另一个包，`SetSuspendedRegistrar(e.RegisterSuspended)` 是装配缝）；相应地**移除**了 `Engine.maxSuspendedSubAgents` 字段，`maxSuspendedSubAgentsEff()` 直读 `EngineConfig`（与 `maxOutstandingAsyncSubAgents` 同模式，单一真源）。
3. `SuspendedRun` 增 `Question`（叶子专有：poll/pinned 渲染与"是否可被用户回答"的判别都需要它，且避免了"等待用户"与"等待子级"两种 `awaiting_user` 的混淆）；`HandoffResult` 增内部 `RunID`（digest handle，depth 门控）。
4. **`Iter` 语义**：恢复从**断点那一轮的索引**继续（即"提问那一轮"被退回），而不是 `iter+1`。取舍：早期挂起（iter 0）会多拿一轮，但没有它就会出现"答案到手却无预算可行动"的病态情形；计划里的 `maxIterations-1` 场景仍精确为 1 轮（有用例锁定）。
5. 下钻深度上限 16 跳（防御环）；级联统一使用"回答那一轮 Run"的 ctx。
6. 既有测试适配：`ask_user_test.go` / `confirm_command_test.go` / `handoff_test.go` 改读队首（`peekAskUser()`）+ 队列字面量。

## 明确不做

跨会话持久化（关掉 TUI 再开则挂起态丢失，退化为重新委派）；子代理互相恢复；挂起态跨 `/resume` 存活（`SetHistory` 一律清空）；嵌套子代理 async；"只恢复叶子、放弃中间层"。

## 风险 / 已知粗糙点

- **挂起态持有整段 history**（最坏 ~1M token/条，压缩阈值 95% 才触发）→ 由 `max_suspended_subagents`（默认 4）兜底。
- **异步挂起不发 `agent_done`**：UI 面板对挂起中的 async 子代理持续显示未完成（语义正确）；条目随后因 TTL/容量被淘汰时面板收不到终止事件 —— 已知粗糙点。
- **淘汰通知**：D12 采取 pinned 提示（同步挂起路径没有 `agent_start`，补孤立 `agent_done` 会让 UI 事件不成对）。
