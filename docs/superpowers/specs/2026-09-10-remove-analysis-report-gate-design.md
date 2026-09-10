# 移除 Analysis Report Gate，以 ask_user 软引导替代 — 设计规格

> **日期：** 2026-09-10
> **状态：** 已批准（方案B：移除强制层 + 能力层软引导）

## 目标

移除引擎级的 **Analysis Report Gate**（强制"搜索后第一次 edit/write 必须被拦截、要求先输出分析报告并等用户确认"），改为**能力层软引导**：给模型 `ask_user` 工具并在其描述中加入"改代码前建议先让用户确认方案"的倾向，由模型自主决定是否确认。

## 背景

- **Analysis Report Gate**（`engine/turn.go:385-456`）是引擎级强制约束：当 `runToolCallCount > 0`（模型已做过搜索）且 `!AnalysisReportConfirmed` 且 `analysisNudgeCount < 2` 时，拦截 `edit`/`write` 调用，注入 nudge 要求先输出报告；拦截 2 次后降级为要求一次性提交全部改动。
- **ask_user 通道**（`engine/agent.go:293`、`engine/turn.go:1573`、`engine/loop.go:94-101`）是通用的"模型向用户提问"能力：模型任意时刻自主调用，有 options → 弹窗（方案A/B/C + 输入你的意见），无 options → awaiting_user Blocked 自由输入；用户 `/confirm N` 消费，选择注入 history。
- 按 AGENTS.md 的**能力优先 / 泛化性优先**原则，gate 是固定模式（场景枚举：仅"搜索后改代码"这一个场景），ask_user 是通用能力（覆盖任意"需用户输入"场景）。本设计移除固定模式，保留并引导通用能力。

## 范围

**做：**
1. 移除 gate 的全部强制逻辑与相关状态（详见"移除清单"）。
2. 在 `askUserToolSpec` 工具描述中加入软引导（按会话语言单一渲染，沿用现有 `zh` 分支）。
3. 适配/删除受影响测试。
4. 保留历史文档（不删除），由新规格标注已废弃。

**不做：**
- 不动 ask_user 全链路（拦截 → `pendingAskUser` → 选项弹窗 / awaiting_user Blocked → `/confirm N` 消费 → 选择注入 history）。
- 不删除 `/confirm N` 机制本身——它仍是 ask_user 选项的确定性消费通道。
- 不动 `pendingEditPlan` / `PlanConfirmed`（独立于 gate 的 plan 确认机制，另有用途）。
- 不引入任何新状态、新字段、新引擎逻辑。

## 移除清单（强制层）

| 项 | 位置 | 动作 |
|---|---|---|
| gate 拦截块（nudge 注入） | `engine/turn.go:385-421` | 删除 |
| gate 降级块（batched resubmission） | `engine/turn.go:423-456` | 删除 |
| 状态字段 `AnalysisReportConfirmed` | `engine/types.go:262-271` | 删除 |
| 引擎字段 `pendingAnalysisNudge` | `engine/loop.go:103-108` | 删除 |
| 引擎字段 `analysisNudgeCount` | `engine/loop.go:110-114` | 删除 |
| Run 入口重置 `analysisNudgeCount`、`AnalysisReportConfirmed` | `engine/loop.go:327,337` | 删除 |
| team verdict 对 `AnalysisReportConfirmed=true` 的赋值 | `engine/loop.go:769` | 删除（保留 `PlanConfirmed=true`） |
| collab verdict 对 `AnalysisReportConfirmed=true` 的赋值 | `engine/loop.go:777` | 删除（保留 `PlanConfirmed=true`） |
| Run 结束挂载 `analysisNudgeCount > 0` 分支 | `engine/loop.go:1010-1022` | 删除（保留 ask_user 有 options 的分支） |
| `askUserOptions()` 无 pendingAskUser 时的固定两选项分支 | `engine/loop.go:1027-1035` | 删除（无 pendingAskUser → 返回 nil） |
| `handleConfirmCommand` 置 `AnalysisReportConfirmed=true` / `pendingAnalysisNudge=false` | `engine/loop.go:1620-1621` | 删除（保留 pendingAskUser 消费逻辑） |
| `handleConfirmCommand` default 分支"✓ 分析报告已确认（按报告执行）" | `engine/loop.go:1635-1637` | 删除（无 options 的 `/confirm N` 仅消费 pendingAskUser，无待决问题时静默返回） |
| `handleAnalysisNudgeConfirmation` 方法及其调用点 | `engine/loop.go:485,1645-1690` | 删除 |
| `clearSessionState` 重置 | `engine/loop.go:1779-1781` | 删除 |
| 测试文件 `engine/analysis_gate_test.go` | — | 删除（全部测试仅覆盖已移除逻辑） |

> `runToolCallCount` 本身保留——它仍被其他逻辑使用（`buildRunSummary`、`recordRunEval` 等），只是不再作为 gate 触发条件。

## 软引导（能力层）

**位置：** `engine/agent.go` `askUserToolSpec` 的 `desc`（中文分支 `:298`、英文分支 `:294`）末尾追加一句。工具描述是模型每次调用决策时必读的位置，引导恰好落在"该不该问用户"的决策点；不新增状态/字段/引擎逻辑。

**中文追加：**
> 另外，在开始修改代码之前，若你的改动计划存在需用户取舍的权衡或方案选择，建议先用本工具向用户确认（可提供方案选项）。这是建议而非强制，是否确认由你根据任务自主决定。

**英文追加：**
> Additionally, before you start modifying code, if your planned changes involve tradeoffs or design choices the user should weigh in on, consider confirming with the user first via this tool (you can provide plan options). This is a suggestion, not a requirement — decide based on the task.

语言渲染与现有 `desc` 一致：按 `zh` 参数只渲染单一语言版本，无新语言机制。

## 行为变更

**变更后：**
1. 模型搜索后可直接 `edit`/`write`，引擎不再拦截——除非模型自主调用 `ask_user`。
2. UI 弹窗只剩两种来源：ask_user 有 options → "方案A/B/C + 输入你的意见"；ask_user 无 options → awaiting_user Blocked 自由输入。不再出现独立的"按报告执行 / 输入你的意见"固定确认框。
3. `handleConfirmCommand`：`/confirm N` 仅在 `pendingAskUser != nil` 时有意义（有 options → 选方案；越界 → 提示无效）；`pendingAskUser == nil` 时静默返回，不产生任何 history 改写。

## 测试变更

| 文件 | 动作 |
|---|---|
| `engine/analysis_gate_test.go` | 删除 |
| `engine/loop_guard_reset_test.go` | 删除 `TestRun_ResetsAnalysisReportConfirmedOnNewRun`（复现旧 bug，机制已不存在）；保留其余两个 Reset 测试（与 gate 无关） |
| `engine/confirm_command_test.go` | 适配：删除对 `AnalysisReportConfirmed`/`pendingAnalysisNudge` 的断言；`TestConfirmOptions_ReturnedWhenGateIntercepted`（依赖 gate 拦截）改为覆盖 ask_user 有 options 挂载选项；`TestHandleConfirmCommand_ConfirmExecutes`（无 options 时"按报告执行"）改为断言无待决问题时静默处理 |
| `engine/ask_user_test.go` | 适配：`TestHandleConfirmCommand_NoOptions_ConfirmExecutes`（依赖"按报告执行"）删除或改为无待决问题语义；`TestAskUserOptions_NoPending_FixedTwo` 改为断言无 pending 时返回 nil；其余 ask_user 测试（options/自由输入/越界/ends-run）保持不变 |
| `engine/turn_test.go` / `progress_loop_test.go` | 仅编译适配：删除 `AnalysisReportConfirmed: true` 预置行 |
| `engine/ask_user_test.go` | 新增：断言 `askUserToolSpec(true)` 含中文软引导、`askUserToolSpec(false)` 含英文软引导 |

**新增回归测试（确认新行为）：**
- 模型搜索后直接提交 `edit`（无 ask_user）→ 不再被拦截，正常执行。
- 模型搜索后调用 `ask_user`（有 options）→ Run 结束挂载选项，`/confirm N` 选择后执行。

## 文档处置

- 保留历史文档：`docs/superpowers/plans/2026-07-14-analysis-report-gate.md`、`docs/superpowers/specs/2026-07-14-analysis-report-gate-design.md`、`docs/superpowers/plans/2026-08-31-analysis-confirmation-ui-gate.md` 及对应 spec。
- 遵循仓库惯例（已移除功能的文档如 intent classifier 全部保留作历史记录），不删除。
- 本规格文档（`2026-09-10-remove-analysis-report-gate-design.md`）作为移除的权威说明，标题与内容明确标注 gate 已废弃。

## 验收标准

- [ ] `grep -rn "AnalysisReportConfirmed\|analysisNudgeCount\|pendingAnalysisNudge" engine/ --include="*.go"` 无输出
- [ ] `grep -rn "按报告执行" engine/ ui/ --include="*.go"` 无输出（软引导措辞不含该短语）
- [ ] 搜索后直接 `edit` 不再被拦截（回归测试）
- [ ] ask_user 全链路不受影响（既有测试全绿）
- [ ] `askUserToolSpec` 含软引导（新测试断言）
- [ ] `go build ./... && go test ./...` 全绿
