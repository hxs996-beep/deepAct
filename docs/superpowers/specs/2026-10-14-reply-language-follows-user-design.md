# 回复语言跟随用户输入（Design）

日期：2026-10-14
状态：已批准，待实施
前置：`docs/superpowers/specs/2026-10-13-prompt-english-single-language-design.md`（提示词英文单语改造）

## 背景与目标

`76f90f2 refactor(prompt): make model-facing prompts English-only` 把模型可见文本统一为英文，并**整体删除**了会话语言检测与锁定机制（`detectUserLanguage`、`UserLanguage` 家族、`isChinese`，生产代码零残留）。此后实际观感是**输出全是英文**，而期望是**输出与用户输入的语言保持一致**。

目标：在不恢复引擎侧语言判定、不引入启发式的前提下，让模型产出的全部面向用户文本（叙述、结论、`todo_write` 步骤文案、`ask_user` 问题与选项、子代理报告）与用户输入语言一致。

## 根因分析

改造后全仓只剩**一条**语言指令：`context/promptset/en/system.md:95`。它同时存在三个失效点：

1. **锚点被误导**。原文 `The session language is locked from the user's first message` 中的"第一条消息"，在模型眼里指向的是 `context/builder.go:96` 注入的 **Block S** —— 一块 `role: "user"`、内容 100% 英文的环境/目录树/AGENTS.md 脚手架，它**先于用户真正的第一条输入**。最自然的解读就是"锁成英文"。
2. **不实陈述**。`locked` 描述的是一个已被删除的引擎机制（2026-10-13 审查 Issue #1，置信度 85）。规则写成"系统已锁定"的**描述**，模型可能据此认为无需自己判断。
3. **显著度与覆盖不足**。规则位于 276 行英文提示词的第 95 行、`# Reply format` 的普通 bullet 中；整篇提示词（含 `examples.md`、语言包、Block S、工具 description、引擎 nudge）都是英文，唯一语言锚点被英文语境淹没。且措辞只说 `Reply`，未覆盖叙述/步骤/子代理报告。

## 方案（已选定：B）

只改提示词层，三处文件：

### 1. `context/promptset/en/system.md` 的 `# Reply format`

用两条 bullet 替换原来的那一条：

```markdown
- **Reply in the user's language.** The session language comes from the messages the user
  types — the first request fixes it and it holds for the whole session, even if later
  messages or tool output switch language. Read it from what the user writes, not from
  injected context blocks, tool output, or this prompt: those are English by design and
  say nothing about the reply language.
- **Write everything the user reads in that language**: narration, conclusions,
  `todo_write` step text, `ask_user` questions and options, and the sub-agent reports you
  relay — not only the final answer.
```

对应修复三个失效点：锚点改为"用户打字的**请求**"并显式排除注入块/工具输出/本提示词；去掉 `locked` 改为行为化祈使；覆盖面扩到全部面向用户的产出。仍然是 `# Reply format` 内的单一 bullet 组、单一事实源，不新增小节。

### 2. `context/promptset/en/sub_agent.md` 的 `## How to work` 末尾

```markdown
- Write your report in the user's language — the language of the goal and context you were
  given — not the language of this prompt or of tool output.
```

子代理的流会渲染进 UI（`ProgressEvent{Type:"stream_delta"}`），其 summary 由父代理转述给用户。子代理 system 提示词为 `prompts.System + Examples + Language Pack + sub_agent.md`（`engine/sub_agent.go:1209-1217`），主提示词的规则本已继承，但同样埋在英文语料中部，故就近补一条锚点。

**锚点必须指向子代理可观测的证据**：默认 `fork_turns=none`（`engine/agent.go:359`）时子代理看不到任何**用户书写的**消息，它唯一收到的 user-role 内容是 `buildVolatilePrompt` 生成的 goal/context 块（`engine/sub_agent.go:503-505`、`:1222-1235`）。因此这里锚定为“交给你的 goal/context 的语言”，而非子代理看不到的用户消息。

### 3. 守卫测试

按 TDD 顺序：先加断言（红灯），再改提示词（转绿）。

- `context/promptset/promptset_test.go`：
  - `TestSystemPrompt_RepliesInUserLanguage`：断言 `System` 含 `Reply in the user's language`、`not from injected context blocks`、`` `todo_write` step text ``、`` `ask_user` questions and options ``、`goal and context you hand to a sub-agent`、`sub-agent reports you relay`；并断言**不含** `" locked"`（带前导空格，避免误命中 `blocked`）。
  - `TestSubAgentPrompt_RepliesInUserLanguage`：断言 `SubAgent` 含 `Write your report in the user's language`、`the language of the goal and context you were given`、`not the language of this prompt or of tool output`；并断言**不含** `user's own messages`（把“锚点不得指向子代理看不到的东西”锁住）。
- `engine/handoff_test.go` 与 `tools/subagent_test.go`：各新增一个用例，**逐字段**断言 handoff schema 的 `properties.goal.description` 与 `properties.context.description` 各自含 `written in the user's language`。用字段级断言而非 `strings.Count(...)==2`：计数在“词组落到错字段”时假绿、在“无关字段也加上同句”时假红（已用变异实验证实两种失真）。

锚点选取原则（初版写错，已按代码质量审查修订）：每个锚点取自**只能由它所守护的子句产生**的最小片段。裸短语若在改动前就已存在、或全文多处出现（如 `Reply in the user's language`、裸 `todo_write`），就是恒真断言、不具守护力。2026-10-13 审查 Issue #8 的「避免锁长散文」是**降低假失败**，不是放弃有效性。

## 已决策项

| 决策 | 取值 | 说明 |
|---|---|---|
| 覆盖范围 | 仅模型的回复 | 引擎产出的用户可见文案（`engine/approval.go`、`engine/turn.go:487` 等）与 `ui/` 不在本次范围 |
| 判定语义 | 会话锁定 | 由用户第一条消息定语言，整会话不变；用户中途切换语言也不跟随 |
| 规则位置 | `# Reply format` 内 | 不提升为顶部独立小节 |

## 不自洽点与取舍

- **"会话锁定"是行为约定，不是引擎保证**。引擎侧语言状态已删除，规则由模型自律维持（改造前亦然：旧的 `UserLanguage` 家族只用于选择提示词变体，从不校验输出语言）。
- **已知不足**：长会话历史被压缩归档后，首条消息的语言证据可能丢失，锁定语义会弱化。若人工冒烟仍不生效，下一步候选是**结构化**改动而非继续加提示词 —— 把 Block S 的 `role` 从 `user` 改为 `system`，或在 Block S 首行声明它是注入的会话上下文而非用户发言。
- **本设计不修改 Block S 的消息结构**（role、顺序、内容均不动），只通过提示词消歧。
- **不重新引入语言检测**：不恢复 `classifyTextLanguage` 类启发式，不新增引擎侧会话语言状态。
- **不改 UI 文案**：`ui/`、`cmd/eval.go` 保持中文。
- **不改模型可见的英文资产**：工具 description、`context/langpacks/*.md`、`engine/prompts_i18n.go` 与各 nudge 维持英文单语设计。

## 改动清单

| 文件 | 变更 |
|---|---|
| `context/promptset/en/system.md` | `# Reply format` 中一条 bullet 拆写为两条（锚点消歧 + 去不实陈述 + 扩覆盖面）；覆盖面枚举补入“递给子代理的 goal/context”（第三次修订） |
| `context/promptset/en/sub_agent.md` | `## How to work` 末尾新增一条语言规则（第三次修订把锚点改为子代理可观测的 goal/context） |
| `context/promptset/promptset_test.go` | 新增 2 个守卫用例 |
| `tools/subagent.go`、`engine/agent.go` | handoff 工具 schema 的 goal/context 描述各加 `written in the user's language`（**两份副本必须同步**） |
| `engine/handoff_test.go`、`tools/subagent_test.go` | 各新增 1 个字段级守卫用例 |

约 90 行（含测试）。

## 第三次修订（2026-10-14）：handoff 的 goal/context

首轮修订只覆盖了模型对用户的直接输出，漏掉了**父代理递给子代理的 goal/context**。经代码核实：

- goal 会被渲染进 UI 的子代理行：`engine/turn.go:669`（`agent_start` 的 `Detail: params.Goal`）、`engine/handoff.go:123`/`:358` → `ui/model.go:573-579` → `ui/model.go:2906-2910`（running 态渲染 `a.Goal`）；`ui/model.go:567` 另把 goal 放进 toolTree 节点。
- 默认 `fork_turns=none` 时子代理看不到用户消息，只能镜像交给它的 goal/context。
- handoff schema 有**两份副本**：`tools/subagent.go:40`（`cmd/run.go:398` 注册，经 `e.tools.Specs()` 进主代理工具列表）与 `engine/agent.go:350`（`engine/sub_agent.go:1250` 的回退，子代理未注册时使用）。只改一份要么修复不生效、要么回退分支与注册版不一致，故两份同步。
- **待查（未决）**：本次会话实际收到的 `handoff_to_agent` schema 同时带有 `tools/subagent.go:75` 的描述文本（含 "(role)" 与并发预算句）与**只存在于 `engine/agent.go:403` 的 `step` 参数**，无法用当前 HEAD 的调用路径解释；会话所用 `deepact` 二进制经核对为旧构建（不含本次改动串）。两份副本早已漂移：`step`、`tools` 字段清单、`expected_output` 文案、工具级描述均不同。本次修复覆盖两份，故不受影响，但该漂移值得单独排查。

**不允许的修法**（已排除）：恢复按会话语言本地化工具描述 —— 那是 `76f90f2` 主动删除的机制，且需要引擎侧会话语言通道。本次只改提示词与工具描述**文本**，不引入任何语言分支。

## 验证

```
go build ./...                     # 期望 exit 0
go vet ./...                       # 期望无输出
go test -count=1 ./context/...     # 新用例先红后绿
```

测试局限（须写明）：守卫用例只能防"规则被误删/改写"，**无法验证模型行为**。

真验收须人工冒烟：新开一个中文会话 → 回复、叙述、`todo_write` 步骤文案均为中文；会话中途粘贴一段英文 → 仍保持中文。
