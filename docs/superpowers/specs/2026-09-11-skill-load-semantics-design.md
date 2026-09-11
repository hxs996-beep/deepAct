# Skill 从"激活"改为"加载"语义，移除语义匹配器 — 设计规格

> **日期：** 2026-09-11
> **状态：** 已批准（方案B：对齐 deepseek-harness 的"加载"语义，不持久注入）

## 目标

把技能机制从当前的"**激活**"语义（引擎持久注入方法论到 stable zone + 每轮一次 LLM 语义匹配）改为"**加载**"语义（模型在主循环内通过工具按需加载技能全文，对齐 deepseek-harness `tool-skill` 的设计），从而：

1. **消除每轮一次的多余 LLM 调用**（语义匹配器 `SkillMatcher.Match`，性质与已删的 Intent Judge / ConclusionClassifier 相同）。
2. **符合 AGENTS.md 的"能力优先"原则**：技能选择权交给主模型自主决定（通过工具），不再由引擎用固定判断抢跑。

## 背景

### 现状（激活语义）

- 技能目录注入 stable zone（`cmd/run.go:146 buildSkillsBlock` + `context/builder.go:48 SetSkillsBlock`），措辞为"BLOCKING REQUIREMENT: ... 调用 activate_skill 工具激活它"。
- `activate_skill` 工具（`engine/agent.go:162`）执行时**改引擎状态**（`ActiveSkillName`/`ActiveSkillContent`）+ **持久注入**方法论到 stable zone（`context.SetActiveSkill`），之后每轮都在模型上下文里。
- 语义匹配器（`engine/loop.go:442-449`）：每次用户消息且无激活技能时，把"用户消息 + 全部技能描述"发给主模型做一次独立 LLM 调用选技能。这是**唯一残留的每轮额外 LLM 调用**。
- 链式自动激活（`deactivateSkill`，`engine/loop.go:1426-1451`）：技能完成后自动激活 NextSkills。
- 意图转移自动去激活（`engine/loop.go:643-661`）。

### 参考实现（deepseek-harness `packages/skill/tool-skill`）

- 一个 `skill` 工具，参数=技能名，**执行时返回技能全文作为工具结果**（不持久注入）。
- 技能目录 `<available_skills>` 摘要（name + 一句话 description）作为会话消息注入。
- 措辞核心："This catalog contains summaries only; do not infer or follow a skill's instructions until it has been loaded."
- 用户显式 `/<name>` 是确定性加载手势，`disable-model-invocation` 技能的唯一入口。
- **零额外 LLM 调用**：技能选择由主模型在工具调用循环内自主完成。

## 范围

**做：**
1. `activate_skill` → `load_skill`，执行语义从"改状态+持久注入"改为"返回全文作为 tool result"。
2. 技能目录措辞改为"摘要仅供选择，加载前不得遵循"（保留 stable zone 注入目录，只改文案）。
3. 删除语义匹配器（`SkillMatcher` 接口 + `SemanticMatcher` + `Match` 调用 + deps 注入 + 相关文件）。
4. 删除"激活"语义的持久状态与自动逻辑（`ActiveSkillName`/`ActiveSkillContent`/`matchedSkillsContent`/`activatedSkills`/`lastActivatedSkill`/`SetActiveSkill`/`deactivateSkill` 链式自动激活/意图转移自动去激活/handoff 注入）。
5. `/<name>` 显式命令改为"当轮注入全文"（经 `pendingPinnedMessages`），不持久。
6. 适配/删除受影响测试。

**不做：**
- 不删除技能系统本身、`skill_install` 工具、`/skills` 列表。
- 不删除 `DisableModelInvocation` frontmatter 语义（目录过滤保留）。
- 不删除 `NextSkills` 元数据字段（模型加载技能后自行决定下一步；引擎不再自动激活）。
- 不引入新的每轮 LLM 判断。

## 移除清单（激活语义层）

| 项 | 位置 | 动作 |
|---|---|---|
| 语义匹配调用块 | `engine/loop.go:438-449` | 删除 |
| `SkillMatcher` 接口 | `skill/matcher.go:12`（`engine/interfaces.go` 无此接口） | 删除 |
| 引擎字段 `skillMatcher` | `engine/loop.go:41,56,196` | 删除 |
| matchFn 构建 + deps 注入 | `cmd/run.go:330-346,358` | 删除 |
| 文件 | `skill/matcher.go`、`skill/matcher_llm.go`、`skill/matcher_test.go` | 删除 |
| 状态字段 `ActiveSkillName`/`ActiveSkillContent` | `engine/types.go:250-251` | 删除 |
| 状态字段 `PendingActivateSkill` | `engine/types.go:249`（无使用点，死字段） | 删除 |
| 引擎字段 `matchedSkillsContent` | `engine/loop.go:80-83` | 删除 |
| 引擎字段 `activatedSkills` | `engine/loop.go:85-88` | 删除 |
| 引擎字段 `lastActivatedSkill` | `engine/loop.go:90-93` | 删除 |
| `context.SetActiveSkill` + `activeSkillBlock` | `context/builder.go:59-74` | 删除 |
| `activateSkill` 方法 | `engine/loop.go:1843-1864` | 删除（由 load 逻辑替代） |
| `deactivateSkill` 方法（含链式自动激活） | `engine/loop.go:1396-1452` | 删除 |
| 意图转移自动去激活 | `engine/loop.go:643-661` | 删除 |
| handoff 注入 matchedSkillsContent | `engine/turn.go:738-745` | 删除 |
| clearSessionState 重置 | `engine/loop.go:1688-1689`（`activatedSkills`/`lastActivatedSkill`）；`loop.go:1687` 的 `deactivateSkill()` 调用点随方法删除移除 | 删除 |
| `skill_activated`/`skill_deactivated` ProgressEvent | `engine/loop.go:1435-1440,652-658,1858-1863` | 删除 |
| `skill_activated` 事件 UI 消费 | `ui/model.go:642-646` | 删除（UI 不再显示"Skill activated"系统消息） |
| `SetActiveSkill` 接口方法 | `engine/interfaces.go`（ContextBuilder 接口） | 删除（连同 stub 实现） |
| `skill/skill.go` 包注释 | `skill/skill.go:1-7`（描述"can be activated... by the model"） | 更新为 load 语义（"loaded via /<name> or the load_skill tool"） |

## 变更（加载语义层）

### 1. `load_skill` 工具

**位置：** `engine/agent.go:18`（常量名 `ActivateSkillToolName` → `LoadSkillToolName = "load_skill"`）、`engine/agent.go:162-184`（`activateSkillToolSpec` → `loadSkillToolSpec`）。

**描述（英文，对齐 harness）：**
> Load the full instructions for an available skill. Call this with the exact skill name from the Available Skills list before acting on a task that names or clearly matches that skill. The catalog contains summaries only; do not infer or follow a skill's instructions until it has been loaded.

**参数：** 保留 `skill_name`（required）+ `reasoning`（optional，说明为何加载，供 UI/日志展示）。

### 2. 执行逻辑（`processActivateSkillCalls` → `processLoadSkillCalls`，`engine/turn.go:1378-1449`）

保留现有拦截结构（验证参数 → 查技能 → 返回 tool message，保证每个 `tool_call_id` 有响应）。唯一变化：

- **删除**全部状态写入（`activatedSkills`/`lastActivatedSkill`/`ActiveSkillName`/`ActiveSkillContent`/`SetActiveSkill`/`matchedSkillsContent`/`pendingPinnedMessages` 的确认消息/`skill_activated` 事件）。
- **新增**：成功时返回技能全文作为 tool result：
  ```
  [SKILL — <name>]

  <content>
  ```
  （复用现 `matchedSkillsContent` 的 `[SKILL — %s]` 格式，只是改为当轮 tool message，不持久。）

### 3. 技能目录措辞（`cmd/run.go:146 buildSkillsBlock`）

从"BLOCKING REQUIREMENT: 调用 activate_skill 激活它"改为 harness 版：

> 以下为可用技能摘要，仅供选择。当用户明确命名某技能，或任务明显匹配某技能描述时，先调用 `load_skill` 工具加载其全文，再遵循其中指令。摘要不含完整指令，加载前不得推断或遵循。用户也可用 `/<name>` 直接加载。若当前技能到达终态需切换，调用 `load_skill` 加载下一个技能。

### 4. `/<name>` 显式命令（`engine/loop.go:404-435` case "activate"）

- 保留解析与大小写回退。
- 删除 `e.activateSkill(s, ...)`。
- 改为把全文注入 `pendingPinnedMessages`（当轮/下一轮 prompt 尾部消费，`engine/turn.go:80-86`），返回确认消息（"✓ Skill `X` loaded. Full methodology injected for this turn."）。

> **机制说明：** `/<name>` 无 taskText 时本轮不执行 turn，`pendingPinnedMessages` 留到下一 Run 的 turn 开头消费；`/<name> <task>` 时改写 history 后继续主循环，本 Run 的 turn 开头消费。全文只出现一次，不持久。

### 5. 链式 NextSkills

- 删除 `deactivateSkill` 中的自动激活（`engine/loop.go:1426-1451`）。
- `NextSkills` 字段（`skill/skill.go:14`）保留为元数据；技能全文内自带链式指引（如 brainstorming 的"调用 writing-plans 技能"），由模型自主决定何时加载下一个技能。

### 6. 保留项确认

- `/skills` 列表（`engine/loop.go:379-402`）：保留，措辞中"激活"改"加载"。
- `skill_install` 工具：保留。
- `DisableModelInvocation` 过滤（`cmd/run.go:156-158`）：保留，此类技能不进目录、模型不可加载，仅 `/<name>` 可用。
- `pendingPinnedMessages` 消费机制：保留（turn.go:80-86），仅不再用于技能确认消息（改为全文注入）。

## 行为变更

1. **技能不再常驻上下文**：方法论只在加载当轮（tool result / pinned）出现，后续轮次靠消息历史保留；压缩后若丢失，模型可再次 `load_skill`。这是与"stable zone 常驻"的明确取舍，已确认接受。
2. **零每轮额外 LLM 调用**：语义匹配器删除后，主循环不再为"选技能"发独立请求；技能选择完全由主模型在工具循环内自主完成。
3. **引擎不再自动激活/去激活技能**：链式、切换、意图转移全部由模型基于技能全文自主判断。
4. **状态更干净**：`TaskState` 不再含技能激活字段，session 持久化与 resume 不受影响（字段 `omitempty` 且已删）。

## 测试变更

| 文件 | 动作 |
|---|---|
| `engine/turn_activate_skill_test.go` | 改造为 `load_skill` 语义：断言 tool result 含全文、不含 `ActiveSkillName`；stub 去掉 `SetActiveSkill` |
| `engine/skill_gate_removal_test.go:46` | 删除 `ActiveSkillName: "systematic-debugging"` 预置（该测试锁定"skill 硬门已移除"，改为无技能状态） |
| `context/builder_test.go:161-166,364-411` | 删除 `ActiveSkillName`/`SetActiveSkill` 相关用例（`TestBuild_ActiveSkillInTail`） |
| `engine/collab_test.go`、`steer_test.go`、`roundtable_test.go` | 删除 `activatedSkills: make(...)` 预置行；`steerContextBuilder.SetActiveSkill` stub 删除 |
| `ui/nodelabel_test.go`、`engine/summarizeargs_test.go` | `activate_skill` → `load_skill` |
| `skill/matcher_test.go` | 删除（随 matcher 文件） |
| 新增 | `TestLoadSkill_ReturnsFullContent`（工具拦截返回全文）；目录措辞断言（`buildSkillsBlock` 含"加载前不得遵循"类文案） |

## 兼容性说明

- 工具名 `activate_skill` → `load_skill` 是破坏性 API 变更，但仅影响模型可见工具与既有 session 快照，无外部依赖。
- 旧 session 中的 `active_skill_name` 字段不再被读取（字段已删），resume 时静默忽略。
