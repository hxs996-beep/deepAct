# 子代理资源护栏设计（Sub-agent Resource Guards）

日期：2026-09-24
状态：已实施（含一条非破坏性行为变更：子代理新增默认 token 预算与 async 未清账上限；`-1`/TOML 提供逃生口）
前序：`2026-09-24-subagent-readonly-universe-design.md`（只读宇宙 + 执行闸）

## 问题

只读化（A1/A2）解决了"子代理能做什么"，没有解决"子代理能烧多少"。四条事实：

1. `sub` 角色 `MaxIterations=0` = 轮次无上限（2026-08-31 设计移除 99 轮回退）；per-call 120s 超时同期移除。
2. 无 run 并发上限：一条消息 N 个 handoff = N 个并行 run-loop goroutine（HTTP 在途维度已由子代理独立 limiter ≤4 slots 约束；缺口是 run 数量与 history 内存维度）。
3. usage 只记账不拦截：`sub_agent.go` 累加 → `loop.go` 上报父累计器，全链路无消费点。
4. 子代理侧无进度判定（progressLoop breaker 是主循环专属）；反复 read 同一文件不触发文本 3-strike，95% 压缩释放上下文后循环继续。

最尖锐组合：无轮次上限 × 正常工具调用 × 压缩释放 = "1M 上下文 × 无限轮"的稳态烧钱循环，且 async 后台任务无人类在场。

## 决策

**资源闸独立成理，不借安全判据。** 前序 spec 的"不可逆数据破坏 → 确定性保护"判据仅适用于安全类约束、不泛化为通则；本决策是一条独立的工程理由：**无人监督的代理不可拥有无界的外部成本提取权**。它的适用范围同样限于"无界外部成本"一类，不外推到行为/风格取舍（何时收敛、什么算进步仍归模型）。

实现形态沿用 budgetTailNudge 家规：**先建议（80% 收尾提示，告知剩余额度——给模型信息），后硬顶（100% 终止，保留部分结果）**。

**满容行为三分通则**（后续资源维度按此归类，不逐案重议）：有天然等待者 → 阻塞排队；语义要求立即返回 → fail-loud；无法中途打断的长飞行 → 软顶（调用边界检查）。

## token 预算

- **口径**：`spent = CacheMissTokens + CompletionTokens`，cache hit 免费（hit ≈ miss 1/10 价；按 hit 计会误杀便宜的持续工作）。钱包保护用钱包口径。为此补齐了 `totalUsage.CacheMissTokens` 累加（原先只累加 Prompt/Completion/Total/CacheHit 四项——miss 大头此前从未进账）；relay 后端由 `llm/client.go` 的 `miss = prompt - hit` 推导保证可算。
- **三态语义，全层级一致**：`Handoff.TokenBudget` / `AgentSpec.TokenBudget` / runner 级 `[context].sub_agent_token_budget` 都是 `0 = 继承下一级；-1 = 显式无限制；>0 = 显式上限`。解析（`tokenBudgetFor`）：`input != 0` 即用之（-1 短路为无限），仅 `input==0` 才查 runner，runner 同样三态，末级默认 `2 × 有效上下文窗`（随 `[context].max_budget_tokens` 自适应）。显式 -1 在任何层级都不会静默落到默认值——"放弃保护必须刻意写下"。
- **模型不可设置、以信息可见**：`token_budget` 不进工具 schema（注入/诱导无法触达或加宽）；80% nudge 告知剩余额度（如 budgetTailNudge 告知剩余轮次）。
- **检查点**：runLoop 每轮累加点后、任何分支之前——每轮必经；软顶语义：最多超支一次 LLM 调用。终止码 `budget_exceeded`，Summary 为 `summarizeHistory` 部分结果，带 `Usage`。
- **TOML 校验**：`< -1` 启动报错（`[context]` 与 `[agents.X]` 两处，config.Apply 返回 error）。
- **层级**：`[agents.X].token_budget`（角色级）→ specSubAgent 合并（`input==0` 时取 spec）→ runner 默认。`/ratd`、`/collab` 直接构造 Handoff 可差异化设预算（含 -1）。
- 顺带统一：`max_iterations` 兜底路径补 `Usage`（父累计器此前漏记该路径最后一段）；`budget_exceeded` 经排除表语义自动获得父代理 follow-up pin（部分发现 + 续跑/重委派提示）。

## async 未清账上限

`dispatchAsync` 在 `bgMu` 临界区内检查 `len(e.bgTasks) >= cap`（bgTasks 计"已派发未取回"——slot 仅在 `agent_poll` 消费 done 结果时释放）：

- **fail-loud**（async 语义要求立即返回）：错误 digest 用 "outstanding" 措辞 + 指路 `agent_poll`，模型先收结果再派新。
- 旋钮 `[context].max_outstanding_async_subagents`（0 = 默认 8；引擎侧解析，裸 Engine 也有 cap）。放 `[context]` 而非 `[model]`——与 HTTP 限流 `max_concurrent_requests` 是正交维度，不混居一个节。
- 与 dsh jobs `maxConcurrentJobsPerOwner` 的 fail 行为对齐。

## 缓议：sync depth-0 信号量（记录论证，条件触发再上）

HTTP 在途已被子代理 limiter 约束，信号量的增量收益只剩"同时运行的 run-loop 数与 history 内存"；该场景需模型单轮发出数十个 handoff，未见实测发生。上界通式：子代理工具调用串行 → 每个运行中的 run 任意时刻至多 1 个直接子代在飞 → **全树并发 ≤ W × maxDepth**（默认 maxDepth=2 时即 2W=16）。

**重访触发条件**：实测出现 >16 并发 run 或可归因的内存压力，或并行编排技能的需求增长至 limiter 成为瓶颈。若届时实施：只在 depth==0 获取（全深度获取在 cap=N、N 个并行 depth-1 各自委派时构成 hold-and-wait 死锁）；满容行为 = 阻塞排队（sync 父回合本就 wg.Wait）。

## 范围线

主代理自身花费不在本闸内（progressLoop 4/6 硬终止 + 每轮用户可见 + Esc 可打断——豁免依据）；子代理护栏止于 handoff 边界。已知边界：主循环对任意 handoff 无条件置进展位，重复派同一失败任务理论上可烧 999 轮，但每轮含主循环完整 LLM 调用，用户可感知。

## 测试

`engine/sub_agent_budget_test.go`：Miss-only 硬顶（锁死 CacheMiss 累加本身）、80% nudge 恰一次且含剩余额度、completion-only 计费、hit 免费、三态解析（含 -1 各层级短路）、spec 合并（-1 存活）、端到端显式无限跑过预算、digest 标题、follow-up pin、max_iterations Usage、并发 `-race`。
`engine/sub_agent_outstanding_test.go`：满容 fail-loud（outstanding + agent_poll 措辞）、poll 释放 slot、引擎侧默认 cap、并发 dispatch `-race`。
`config` 校验：`< -1` 两处 fail-loud、-1/正数保留、角色级预算到 spec。

## 与 deepseek-harness 的对照

dsh 无 turn 预算（其文档明载局限），但有激活容量池（默认 8）+ 输出上限分摊；其 `maxConcurrentJobsPerOwner`（fail + 指路 job_kill）是本方案 async 上限的行为先例。本方案比 dsh 多一层"per-run 钱包闸"（dsh 子代理 usage 亦只记账）——这是无人类在场的后台任务形态下更贴身的护栏；sync 并发维度则比 dsh 保守（缓议），因为 deepact 的 HTTP 侧已有独立 limiter 而 dsh 没有。
