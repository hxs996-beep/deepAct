# 代码审查报告（第二轮复核：handoff 目标语言修复）

## 📋 审查速览

**变更规模**: 9 文件, +111/-20 行（代码面 5 文件 +76/-12：`system.md`、`sub_agent.md`、`promptset_test.go`、`agent.go`、`subagent.go` 及其 2 个测试）
**核心变更**: 把「父代理递给子代理的 goal/context 也必须用用户语言书写」写进提示词与两份 handoff 工具 schema，并把子代理语言锚点改指它实际能看到的证据
**风险等级**: 🟢 低风险 — 纯提示词与 schema 文案改动，无引擎分支、无语言检测，构建与全量测试通过
**关键发现**: 0 个 Critical, 0 个 Important — 上一轮的核心问题（Important #1 与 Minor #3）已正确闭合；本轮新增 3 个 Minor / 4 个 Low，均为枚举完整性与遗留清理
**建议行动**: 1) 把锚点/schema 的语言要求从「goal + context」扩到「你收到的全部委托内容」（覆盖 `expected_output`/`constraints`）；2) 补做人工双语冒烟（设计文档定义的真验收，至今未做）

> 风险判断: 高=有Critical或涉及认证/支付/数据删除; 中=有Important或影响>5文件; 低=仅Minor/Low

---

## 审查元信息
| 字段 | 值 |
|------|------|
| **基准版本 (Source)** | `db84fe0946bcce0cde1ff730d6723ec2c52c3595`（上一轮 HEAD） |
| **目标版本 (Target)** | `6f46b4e` (HEAD) |
| **Merge Base** | `db84fe0`（线性，无分叉） |
| **审查日期** | 2026-10-14 |
| **审查 Agent 数** | 3 (change-summarizer 摘要 + bug-detector/critic 复核 + 主 agent 覆盖与影响追溯) |
| **置信度阈值** | >= 50 显示，所有分数保留 |

## 变更概览
**文件变更数**: 9 | **新增行数**: +111 | **删除行数**: -20

### 变更文件列表
| 文件 | 变更 | 变更类型 |
|------|------|----------|
| `context/promptset/en/system.md` | +1/-1 | 修改（提示词，覆盖面枚举补入 goal/context） |
| `context/promptset/en/sub_agent.md` | +1/-1 | 修改（提示词，锚点改为可观测证据） |
| `context/promptset/promptset_test.go` | +8/-2 | 修改（守卫测试，补锚点 + 负向断言） |
| `engine/agent.go` | +10/-6 | 修改（fallback handoff schema 文案 + 失效注释改写） |
| `tools/subagent.go` | +2/-2 | 修改（注册版 handoff schema 文案） |
| `engine/handoff_test.go` | +28/-0 | 新增（字段级守卫用例） |
| `tools/subagent_test.go` | +25/-0 | 新增（字段级守卫用例） |
| `docs/superpowers/plans/2026-10-14-reply-language-follows-user.md` | +11/-0 | 修改（追加第三轮记录） |
| `docs/superpowers/specs/2026-10-14-reply-language-follows-user-design.md` | +25/-8 | 修改（同步第三次修订与改动清单） |

Tier 分级：Tier 5 = 5 个 Go/提示词文件；Tier 2 = 2 份 docs；无 Tier 0/1/3/4。

---

## 🎯 风险评估

| 维度 | 评分 | 说明 |
|------|------|------|
| 安全性 | 10/10 | 无安全面；schema 文案改动不影响工具执行 |
| 可靠性 | 8/10 | 两份 schema 副本已同步，回退路径与注册路径行为一致；剩余字段漂移不影响本次语义 |
| 复杂度 | 2/10 | 文案 + 断言，无控制流变化 |
| 测试覆盖 | 8/10 | 新增 2 个字段级用例（优于计数写法），负向断言锁住"锚点不得指向不可见证据"；仍缺 `stableSystemPrompt` 装配断言与人工冒烟 |
| 影响范围 | 3/10 | 无 Go 符号签名变化；影响面是提示词文本与工具描述 |

---

## 变更内容说明

### 模块一: 语言链闭合（handoff 环节）
**一句话概括**: 让父代理在把任务交给子代理时，用用户的语言写任务说明——因为这段说明既会显示在界面上，也是子代理判断该用什么语言的唯一线索。
**变更类型**: Bug 修复（提示词 + 工具 schema 文案）
**涉及文件**: `context/promptset/en/system.md`, `context/promptset/en/sub_agent.md`, `tools/subagent.go`, `engine/agent.go`

- `system.md:96`：覆盖面枚举补入 `the goal and context you hand to a sub-agent (the sub-agent mirrors their language)` —— 直接闭合上一轮 ⚠️Important #1。
- `sub_agent.md:7`：锚点由「the language of the user's own messages」改为「the language of the goal and context you were given」—— 直接闭合上一轮 ℹ️Minor #3，且修法比建议更进一步：承认默认 `fork_turns=none` 时子代理看不到任何用户消息，锚点必须指向它实际持有的证据。
- `tools/subagent.go:58-59` 与 `engine/agent.go:357-358`：两份 handoff schema 的 `goal`/`context` 描述同步加 `written in the user's language`；`goal` 另注明「用户会看到这个 goal」。

**关键改动:**
- `system.md` 的覆盖面枚举：+1 项（父代理递给子代理的文本）
- `sub_agent.md` 的锚点语义：从"用户消息"改为"交给你的 goal/context"
- `handoffToolSpec()`（`engine/agent.go:354`）+ `SubAgentTool.Spec()`（`tools/subagent.go:40`）：两个副本的 goal/context 描述同步

### 模块二: 守卫测试强化
**一句话概括**: 让"语言规则被误删或指向错东西"这件事在 CI 里必然变红。
**变更类型**: 测试
**涉及文件**: `context/promptset/promptset_test.go`, `engine/handoff_test.go`, `tools/subagent_test.go`

- `promptset_test.go`：新增锚点 `the language of the goal and context you were given`；新增负向断言——`SubAgent` 不得再含 `user's own messages`。
- `engine/handoff_test.go:130-157`、`tools/subagent_test.go:44-68`：各自解析 JSON 后**逐字段**断言 `properties.goal.description` 与 `properties.context.description` 含目标短语。设计文档明确记录了放弃 `strings.Count(...)==2` 的理由（词组落到错字段时假绿、无关字段加同句时假红），并称已用变异实验证实——我逐一复核了两种失真路径，成立。

### 模块三: 文档同步
- 计划文档追加「第三轮」记录（外部审查结论 + 修法 + 本轮偏差声明，含"以实际文件内容为准"的免责）。
- 设计文档同步第三次修订：新增"锚点必须指向子代理可观测的证据"小节、两份 schema 副本必须同步的说明、"不允许的修法"（恢复按会话语言本地化工具描述已被排除），以及一处标注为**待查（未决）**的 schema 漂移观察。

---

## 需求对照

> 需求来源：`docs/superpowers/specs/2026-10-14-reply-language-follows-user-design.md` + 上一轮审查报告 `docs/code-review-report/deepact-2026-10-14-子代理回复语言锚点.md`

### 上一轮 issue 闭环状态

| 上轮 issue | 级别 | 状态 | 核实依据 |
|---|---|---|---|
| #1 语言链在 handoff goal 处断链 | ⚠️ Important | ✅ **已闭环** | `system.md:96` 补入 goal/context；两份 schema 均加语言要求（`tools/subagent.go:58-59`、`engine/agent.go:357-358`） |
| #3 `sub_agent.md:7` 锚点指向子代理看不到的用户消息 | ℹ️ Minor | ✅ **已闭环** | `sub_agent.md:7` 改为"交给你的 goal/context"；`promptset_test.go` 加负向断言 |
| #7a `engine/agent.go:347` 失效注释（称工具描述按会话语言本地化） | 💬 Low | ✅ **已闭环** | 注释重写为 fallback schema 的真实职责 |
| #4 守卫测试未覆盖 `stableSystemPrompt` 装配 | ℹ️ Minor | ❌ 未处理 → 见 Issue #2 | `grep stableSystemPrompt` 在测试中仍零命中 |
| #5 就近锚点只写 "report"，未含 running narration | ℹ️ Minor | ❌ 未处理 → 见 Issue #3 | `sub_agent.md:7` 仍为 "Write your report..."，未提叙述流 |
| #6 `Output contract` 英文小节标题冲突 | 💬 Low | ❌ 未处理 | 文本未变 |
| #7b `engine/sub_agent.go:1489` `language-appropriate nudge` 失效注释 | 💬 Low | ❌ 未处理 → 见 Issue #6 | 注释仍在 |
| #2 `promptset_test.go:11` "红灯"失效注释 | 💬 Low | ❌ 未处理 → 见 Issue #6 | 注释仍在（该行不在本轮 diff 内） |
| 设计文档定义的**人工双语冒烟**（真验收） | — | ❌ 仍未做 → 见 Issue #7 | 计划文档与设计文档均无新增冒烟记录 |

**超出需求范围的变更:** 无。本轮把 `tools/subagent.go` 与 `engine/agent.go` 纳入修改，属设计文档允许范围内（两处是同一 schema 的两份副本，只改一份会失效或造成回退分支不一致），且计划文档已显式声明该偏差。

---

## 变更影响范围

> **分析工具**: LSP + Grep（无 `.code-review-graph/`）

本轮无 Go 符号签名变化，追的是"文案如何到达用户与模型"：

### `handoff_to_agent` 的 `goal`（`tools/subagent.go:58` / `engine/agent.go:357`）
**变更内容**: 描述新增 `written in the user's language (the user sees this goal)`

| 消费位置 | 行号 | 引用方式 | 影响评估 |
|----------|------|----------|----------|
| `engine/turn.go` | 669-671 | 同步 handoff 发 `agent_start{Detail: params.Goal}` | 🚨 goal 进 UI 面板 |
| `engine/handoff.go` | 358-361 | 异步 handoff 发 `agent_start{Detail: params.Goal}` | 🚨 同上 |
| `ui/model.go` | 573-579 | `SubAgentStatus.Goal = msg.Detail` | 🚨 运行中由 `subAgentLine()` 渲染给用户 |
| `ui/model.go` | 2906-2910 | `subAgentLine()` running 分支渲染 `trunc(a.Goal)` | 🚨 用户直接读到的文本 |
| `engine/sub_agent.go` | 503-505 | goal 经 `buildVolatilePrompt` 成为子代理唯一 user-role 内容（默认无 fork） | 🚨 决定子代理输出语言 |

> 上轮 ⚠️Important #1 的两条前提（"用户看得到 goal"、"子代理只看得到 goal"）在本轮被我逐条读码复核，**全部成立**。

### 两份 schema 副本的一致性
| 项 | `tools/subagent.go`（注册版，`cmd/run.go:398`） | `engine/agent.go:354`（fallback） | 一致性 |
|---|---|---|---|
| `goal.description` | 已加语言要求 | 已加语言要求 | ✅ 一致 |
| `context.description` | 已加语言要求 | 已加语言要求 | ✅ 一致 |
| `step` 字段 | 有（注册版独有） | 无 | ⚠️ 既有漂移（→ Issue #5） |
| `tools` / `expected_output` 文案 | 较详 | 较简 | ⚠️ 既有漂移（→ Issue #5） |

### 影响总结
- 🚨 需同步修改的文件: 0 个（两份 schema 已同步）
- ⚠️ 可能需要关注的文件: 2 个（`engine/sub_agent.go` 的 nudge 注释、`context/promptset/promptset_test.go:11` 注释）
- ✅ 无影响的符号: 全部

---

## 🧪 建议回归测试范围

### 必须测试
- [ ] `go test -count=1 ./context/promptset/... ./engine/... ./tools/...` —— 本轮 3 个新/改用例应绿（已实跑：5 个包全 `ok`）
- [ ] `go build ./...` + `go vet ./...`（已实跑：exit 0，无输出）
- [ ] **人工双语冒烟（仍未做）**：中文会话下检查 ① 主代理叙述/结论 ② `todo_write` 步骤文案 ③ `ask_user` 弹窗 ④ **UI 子代理行的 goal 文本** ⑤ 子代理的流式叙述与最终报告 是否一致为中文
- [ ] 委派实验：中文会话中委派一个默认（无 fork）子代理，确认报告的**中间叙述**而非仅最终报告为中文（→ Issue #3 的判定实验）

### 建议测试
- [ ] 英文会话 → 派生中文子任务，检查是否出现语言串扰
- [ ] 长会话触发历史压缩后再观察语言漂移（设计文档自述的已知不足）

---

## 代码审查结果

### 🚨 Critical Issues (90-100) — 必须修复

无。

### ⚠️ Important Issues (80-89) — 应当修复

无。

### ℹ️ Minor Issues (50-79) — 供参考

1. ℹ️ **[主 agent] 语言要求的枚举只覆盖 goal/context，漏掉同样被注入子代理的 `expected_output` 与 `constraints`**
   - **文件**: `context/promptset/en/sub_agent.md:7`、`tools/subagent.go:58-63`、`engine/agent.go:357-360`
   - **置信度**: 60
   - **来源**: 🔴 **本次引入** (提交人: hxs996-beep)
   - **问题**: `buildVolatilePrompt`（`engine/sub_agent.go:1222-1235`）向子代理注入四个字段：`## Goal`、`## Expected Output`、`## Context`、`## Constraints`。本轮的语言要求只落在 goal/context 上——`sub_agent.md:7` 的锚点写 "the goal and context you were given"，两份 schema 的 `expected_output`/`constraints` 描述也未加语言要求（`engine/agent.go:359-360`）。若父代理用英文写这两项，子代理仍可能被拉向英文。这与上一轮 Issue #1 是同一类问题（用枚举表达覆盖面，枚举必然漏项），removed 本轮只把漏项从 1 个减到 2 个。
   - **建议修复**: 仍不引入引擎分支，只把话说全——`sub_agent.md:7` 改为 "the language of the request your parent handed you"（或 "the goal, context and constraints you were given"）；schema 侧把 `constraints`/`expected_output` 描述同样加上 language 措辞，或把语言要求提到工具级 `description` 上（一处覆盖全部参数，天然免疫漏项）。

2. ℹ️ **[主 agent / 上轮 #4 未闭环] 仍无测试断言"规则真的进了子代理 system prompt"**
   - **文件**: `context/promptset/promptset_test.go:92`（断言对象是 `Get().SubAgent`）
   - **置信度**: 60
   - **来源**: 已有问题（本轮未处理）
   - **建议**: 在 `engine` 侧补一条断言 `stableSystemPrompt()` 含该片段的用例。当前若有人删掉 `engine/sub_agent.go:1217` 的 `+ prompts.SubAgent`，三个 prompt 用例全绿而规则静默消失。

3. ℹ️ **[主 agent / 上轮 #5 未闭环] 就近锚点仍只说 "report"，未覆盖子代理的流式叙述**
   - **文件**: `context/promptset/en/sub_agent.md:7`
   - **置信度**: 55
   - **来源**: 已有问题（本轮未处理）
   - **问题**: 子代理最显眼的用户可见文本是**中间叙述**（`engine/sub_agent.go:395` 的 `stream_delta` → `ui/model.go:630` 直接累加渲染），而锚点只说 "Write your report"。本轮改了锚点的**指向**，却没改**覆盖面**——而"埋在英文语料中部、需要就近补锚点"这个理由对 narration 同样成立。
   - **建议**: 扩为 "Write your report and your running narration in the user's language"。

### 💬 Low Confidence Issues (0-49) — 仅供参考

1. 💬 **[主 agent] "the user sees this goal" 在嵌套委派下不成立**
   - **文件**: `tools/subagent.go:58`、`engine/agent.go:357`
   - **置信度**: 35
   - **来源**: 🔴 **本次引入** (提交人: hxs996-beep)
   - **说明**: `engine/handoff.go:113-124` 明确「Depth 0 only: a nested delegation owns no UI row」，即嵌套子代理的 goal 不进 UI。文案对顶层委派成立、对嵌套不成立。因该短语只是加强"用用户语言"的动机、不参与任何判定，实际危害极低。

2. 💬 **[主 agent] 两份 handoff schema 的其余字段漂移仍未处理**
   - **文件**: `tools/subagent.go:40-78` vs `engine/agent.go:354-370`
   - **置信度**: 40
   - **来源**: 已有问题
   - **说明**: 设计文档已自述该漂移（`step` 字段、`tools` 清单、`expected_output` 文案不同），并标注为**待查（未决）**——包括"会话中实际收到的 schema 含只存在于 `engine/agent.go` 的 `step` 参数，无法用当前 HEAD 调用路径解释"。本轮只同步了 goal/context，故不影响本次修复；但"两份副本必须手工同步"本身是结构性风险，值得单独排查（更彻底的做法是让 fallback 直接复用注册版的 schema 构造）。

3. 💬 **[主 agent] 人工双语冒烟（设计文档定义的真验收）至今未做**
   - **文件**: `docs/superpowers/specs/2026-10-14-reply-language-follows-user-design.md`（验证章节）
   - **置信度**: 45
   - **来源**: 已有问题
   - **说明**: 设计文档明确"守卫用例只能防规则被误删/改写，无法验证模型行为；真验收须人工冒烟"。计划文档在第二轮自述"尚未完成"，本轮记录也未包含冒烟结果。三轮提示词修订的效果目前**全部只有静态证据**。

4. 💬 **[主 agent] 两处失效注释未清理**
   - **文件**: `engine/sub_agent.go:1489`、`context/promptset/promptset_test.go:11`
   - **置信度**: 40
   - **来源**: 已有问题
   - **说明**: `sub_agent.go:1489` 仍称 `getNudgeMessage` 返回 "a language-appropriate nudge"（该机制已删）；`promptset_test.go:11` 仍称"当前 sub_agent.md 输出契约未提及预期输出 → 红灯"（实际 `sub_agent.md:26` 已含，用例为绿）。本轮修了 `engine/agent.go:347` 的同类问题，但没扫其余两处。

---

## 补充分析

### 逻辑模拟 (logic-simulator)
本变更无状态机或控制流（文案 + 断言），跳过 dry-run。唯一可模拟的链路（用户语言 → 父代理写的 goal → 子代理输出语言 → UI 渲染）已在「变更影响范围」中逐跳核实。

### 测试覆盖 (test-coverage-analyst)

| 目标 | 关键性 | 覆盖情况 | 缺口 |
|------|--------|----------|------|
| `system.md` 覆盖面枚举 | 8/10 | 部分覆盖 | 6 条锚点钩住已列项目；"枚举是否完整"仍无测试可守（→ Issue #1） |
| `sub_agent.md` 锚点 | 7/10 | 较好 | 3 条正向锚点 + 1 条负向断言；未覆盖 `stableSystemPrompt` 装配（→ Issue #2） |
| 两份 handoff schema | 8/10 | ✅ 良好 | 字段级断言（优于计数），两副本各自独立用例 |
| 模型实际行为 | 10/10 | ❌ 未覆盖 | 需人工冒烟（→ Issue #3/Low-3） |

### 合规检查 (compliance-auditor)
对照 `AGENTS.md`「泛化性优先 / 能力优先」：

- ✅ **未恢复语言分支**：设计文档显式排除"按会话语言本地化工具描述"，本轮只改文本，符合"能力优先、给模型判断"的原则。
- ✅ **单一事实源**：语言规则仍只在 `system.md:96` + `sub_agent.md:7` 两处；schema 文案是同一规则的通道级复述，非新机制。
- ⚠️ **泛化性仍有缺口**：`system.md:96`（列表）与 `sub_agent.md:7`（"goal and context"）都以**枚举**表达覆盖面，因而各漏一项（`constraints`/`expected_output`）。按 AGENTS.md 的判据，把覆盖面写成"用户会读到的一切 / 你收到的全部委托内容"比逐项列举更能覆盖无限场景——这是本轮最值得改的一点（→ Issue #1）。

---

## ✅ 值得肯定的实践

- **修法比建议更准确**：没有照抄"在清单里加 goal/context"就收工，而是先确认"子代理到底看得到什么"（默认 `fork_turns=none` 无用户消息），据此把锚点改指 goal/context——这正好印证了上一轮 ⚠️Important #1 的机制判断，并用代码证据（`engine/sub_agent.go:503-505`）写进了设计文档。
- **发现并同步了第二份 schema 副本**（`tools/subagent.go` 注册版 vs `engine/agent.go` fallback）：只改一份会"要么修复不生效、要么回退分支不一致"，这是本轮的实质增量。
- **测试写法经变异实验选定**：明确弃用 `strings.Count(...)==2`，改用字段级 JSON 断言，并把两种失真路径（落到错字段假绿、无关字段加同句假红）写进文档——断言强度与原由都有据可查。
- **负向断言**：`promptset_test.go` 用 `must not contain "user's own messages"` 把"锚点不得指向子代理看不到的东西"这一**判据本身**锁住，比只锁正向文本更有守护力。
- **主动声明偏差**：计划文档写明 `tools/subagent.go` 不在原计划内、以及 `sub_agent.md` bullet 文本已被本轮再次改写，避免后续读者照抄过期文本。
- **诚实标注未决项**：把无法解释的 schema 漂移现象标为"待查（未决）"而不是掩盖，便于后续排查。

---

## 审查总结
| 类别 | 数量 |
|------|------|
| 🚨 Critical | 0 |
| ⚠️ Important | 0 |
| ℹ️ Minor | 3 |
| 💬 Low Confidence | 4 |
| **合计** | 7 |

### Phase 4.5 自检结果

- **合并**: 1 条（上轮 #7a 与本轮 `engine/agent.go` 注释改写视为同一问题的闭环，记入「需求对照」而非新 issue）
- **降级**: 1 条（"the user sees this goal" 对嵌套委派不成立 → Low，实际危害极低）
- **可疑**: 0 条（所有 file:line 均逐个用 Read 核实，含 `engine/turn.go:669`、`engine/handoff.go:358-361`、`ui/model.go:573-579`、`ui/model.go:2906-2910`）
- **交叉验证合并**: 0 条（本轮无多 agent 同文件共识项）

> 自检失败的 issue 不会被剔除，仅降级或合并。

## 建议

### 本周必须处理
1. **把语言要求从枚举改为原则**（Issue #1）：`sub_agent.md:7` 改为 "the language of the request your parent handed you"，schema 的语言要求提到工具级 `description`（一处覆盖 goal/context/constraints/expected_output）。这既是补漏，也是把"枚举必然漏项"这个根因去掉。
2. **补做人工双语冒烟**（Low-3）：三轮修订的效果目前只有静态证据；设计文档把冒烟定为真验收，不补则整个修复处于"未验证"状态。

### 下个迭代处理
1. 补 `stableSystemPrompt()` 拼接断言（Issue #2）。
2. 把 narration 纳入就近锚点（Issue #3）。
3. 顺手清理两处失效注释（Low-4）。

### 可选改进
1. 排查两份 handoff schema 的其余漂移，优先考虑让 fallback 复用注册版的构造以消除手工同步（Low-2）。
2. 仓库根目录遗留 `/.code-review-tmp-59353/` 与 `/.cr-tmp-path` 未跟踪文件（上轮审查的临时产物），建议清理或加入 `.gitignore`。

---
*报告由 super-code-review-cn 生成*
*并行审查 + 置信度二次评分过滤*
*所有问题均保留供参考，按置信度分组方便决策*
