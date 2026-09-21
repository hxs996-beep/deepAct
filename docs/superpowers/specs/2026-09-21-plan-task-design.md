# plan_task：模型自主触发的深度规划能力 — 设计规格

> **日期：** 2026-09-21
> **状态：** 已批准（方案甲：纯能力注入，零状态零门 / A+B：collab 增强 + 引擎工具 / 用户确认沿用现有 ask_user）

## 目标

给 deepact 增加一个**模型自主触发的深度规划工具 `plan_task`**：当 LLM 判断当前是复杂/多步骤/需深度分析的任务时，调用 `plan_task`，引擎拦截并注入内置"深度分析方法论"全文作为 tool result，模型随后按方法论"吃透背景 → 拆解 → 多假设 → 验证 → 计划 → 执行"。

设计哲学（AGENTS.md 能力优先）：**给模型能力、由模型自主决定**，而不是引擎写死规划状态机。`plan_task` 与现有 `load_skill` 机制完全同构——区别只是方法论是**内置常量**而非外部 skill 文件。不引入 dsh 的 `/plan` 状态机、持久化投影、exit_plan_mode 评审门等硬机制。

## 背景

### 与 dsh 的对比（本次会话调研结论）

- dsh（deepseek-harness）的 plan-mode 是**用户主导的协作状态机**：`/plan` 进入 → `plan:policy` 提示段动态注入 → `exit_plan_mode` 评审门 → 用户 `Approve` 才执行。核心机制是状态、不是工具。
- deepact 的目标**不是复刻 dsh 的功能**，而是**提升当前 agent 的分析问题深度**。
- 手段偏好：**提示词优先**——"通过提示词就能提深也是最好的处理措施"。
- 现有 `ask_user` 已承担"改动前用户确认"职责（`engine/agent.go:289`），dsh 的 exit_plan_mode 评审门与它同构，不需要照搬。

### 决策记录（用户已确认）

| # | 决策 | 选择 |
|---|---|---|
| 1 | 规划载体 | **新引擎工具 `plan_task`**（非 skill）：引擎拦截注入方法论，后续可接线 |
| 2 | 触发方式 | **模型自主判断**（复杂任务才调用），仿 `load_skill` |
| 3 | 行为边界 | **方案甲：纯能力注入**——只拦截注入方法论，零状态、零门 |
| 4 | collab 联动（A+B） | **A**：collab skill 增强（规划阶段 + 强制 context）；**B**：规划工具方法论与 collab 协同（复杂目标 → `/collab` 拆解并行） |
| 5 | 用户确认 | 沿用现有 `ask_user`，不新增评审门 |

## 架构

### 新增文件

- `engine/plan_methodology.go`：内置"深度分析方法论"常量（中英双语，仿 langpack 双语模式）。核心内容：
  1. **何时用**：复杂/多步骤/需深度分析的任务；简单任务直接用 `todo_write`
  2. **吃透背景**：读文件、目录树、AGENTS.md、现有模式——先理解再规划
  3. **拆解**：把目标拆成可独立验证的子问题
  4. **多假设**：列出 2-3 个可能方向，不锚定第一个直觉
  5. **验证优先**：用只读工具取证（grep/read/lsp），证据支撑结论，标注未验证假设
  6. **产出计划**：明确步骤、每步验证点、边界与失败模式
  7. **执行纪律**：按计划走，每步验证；偏差时更新计划而非闷头改
  8. **collab 协同**：目标需多方向并行调研 → 用 `/collab`，背景+框架作为 context 传给子代理
- `engine/plan_task_test.go`：测试（见下文）

### 改动清单（最小、贴合现有模式）

| 文件 | 改动 |
|---|---|
| `engine/agent.go` | 新增常量 `PlanTaskToolName = "plan_task"`；新增 `planTaskToolSpec(zh)`（仿 `loadSkillToolSpec`，`engine/agent.go:180`） |
| `engine/turn.go` | ① `toolSpecsWithHandoff()`（`engine/turn.go:809`）追加 `planTaskToolSpec`；② 拦截 `plan_task`（仿 `processLoadSkillCalls` 结构，`engine/turn.go:1315`，但方法论是内置常量）；③ `summarizeArgs`（`engine/turn.go:832`）加 case |
| `engine/plan_methodology.go` | 新增：方法论常量（中英双语） |
| `skill/builtin/collab/SKILL.md` | 增强（见下） |

### 工具定义（`planTaskToolSpec`）

```go
// planTaskToolSpec returns the tool definition for the model to invoke
// deep-planning. Mirrors load_skill: intercept + inject methodology.
func planTaskToolSpec(zh bool) ModelTool
```

- Name: `plan_task`
- 描述（双语）："当你判断当前任务复杂、多步骤、需要先深度分析再动手时，调用本工具获得深度分析方法论。引擎会注入规划框架，请遵循它完成分析、拆解与计划，再开始执行。简单任务请直接用 todo_write。"
- 参数：空对象（无参数）

### 拦截处理（`processPlanTaskCalls`）

仿 `processLoadSkillCalls`（`engine/turn.go:1315`）：
- 拦截 `plan_task` 调用，返回内置方法论全文作为 tool result（`[PLAN_METHODOLOGY — 深度规划]...`）
- 坏 JSON / 异常 → 错误 tool message
- 每个 `plan_task` 调用都必须有 tool 响应（DeepSeek API 要求 tool_call_id 一一对应）
- `plan_task` **不进** `regularCalls`（防重复 tool message，同 `load_skill` 处理，`engine/turn.go:570`）

### collab skill 增强（A 部分）

当前 `skill/builtin/collab/SKILL.md` 的"拆解"只说"拆成 2-6 个方向"、`context` 可选。增强为：

- **规划阶段**（新增第 0 步）：拆解前先用只读工具吃透背景（用户请求、代码库、AGENTS.md），形成全局框架（目标、拆解理由、方向间关系）。
- **强制 context**：每个 handoff 的 `context` 必填：用户请求原文 + 全局框架 + 本方向定位（负责什么、边界在哪、与谁互补）。子代理"带着框架深挖"而非"从零开始"。
- **方向独立性**：保持"互不重叠"，但 context 携带全局视图。

### 方法论与 collab 协同（B 部分）

`plan_methodology.go` 的"collab 协同"条：当模型分析后发现目标需多方向并行调研 → 用 `/collab`，把已形成的全局框架作为 context 基础，避免"主 agent 先规划，子 agent 从零开始"的脱节。

## 错误处理

- `plan_task` 调用坏 JSON / 异常 → 错误 tool message（同 `load_skill`）。
- 方法论注入失败不影响回合。

## 测试

仿 `load_skill` 测试结构（`engine/turn_load_skill_test.go`）：

| 测试 | 验证 |
|---|---|
| `TestPlanTaskToolSpec_Exists` | tool spec 存在、Name 正确、参数为空对象 |
| `TestProcessPlanTaskCalls_InjectMethodology` | 合法调用返回方法论全文（含 `[PLAN_METHODOLOGY` 前缀） |
| `TestProcessPlanTaskCalls_BadJSON` | 坏 JSON → 错误 tool message |
| `TestPlanTaskNotInRegularCalls` | `plan_task` 不进 `regularCalls`，不产生重复 tool message |

## 规格自检

- **占位符**：无 TODO/待定。
- **一致性**：方案甲（纯能力）与 AGENTS.md 能力优先一致；与 `load_skill` 同构；collab 增强只改 skill 不改引擎状态机。
- **范围**：聚焦——引擎工具注入 + skill 增强，单实现计划可覆盖。
- **模糊性**："复杂任务"判断权归模型（方法论里给启发式），引擎不判定；双语方法论随会话语言选择。

## 后续（不做，YAGNI）

- 计划产物写入 `TaskState.Plan`（已有字段，`engine/types.go:248`）——方案乙，本次不做。
- 破坏性工具前"计划已确认"软门（`PlanConfirmed` 字段已存在但无确认门实现）——方案乙，本次不做。
- dsh 式 `/plan` 状态机 + 持久化投影 + exit_plan_mode 评审门——不学风格，本次不做。
