# 动态方案确认弹出框（present_options 工具）

日期：2026-09-03
状态：已批准（用户确认最终语义）

## 背景与问题

分析门控确认弹出框当前展示固定两项：

```
[1] 方案A: 按报告执行修改
[2] 其他（输入你的意见）
```

"方案A"前缀来自 2026-08-31 设计（
`docs/superpowers/specs/2026-08-31-analysis-confirmation-ui-gate-design.md`），
该设计最初设想 4 个方案（A 直接执行 / B 调整 / C 取消 / 其他），但最终实现
收敛为单个真实确认项 + 自由输入项。只有一个方案却带"A/B/C"编号没有意义。

用户澄清的**真实意图**：
- 这个弹出框本质是"获得编辑权限"的确认门。
- 只有当 LLM 真的输出多个可选方案（N≥2）时，才应出现"方案A/B/C..."选择。
- 没有多方案时（如 bug 分析报告），只有两种情形：按报告执行 /
  输入你的意见（用户可借末项表达"需要澄清一些问题"等）。

## 目标

1. 弹出框支持**动态**展示方案选择：LLM 声明多个方案 → 按"方案A/B/C"展示。
2. 无多方案时保持固定两项（按报告执行 / 输入你的意见）。
3. 方案前缀"方案A/B/C"由**引擎确定性生成**，不依赖 LLM 格式。
4. 两种模式末尾都保留"输入你的意见"逃生通道（回到输入框自由输入）。

## 方案决策

### 确定采用：新增 `present_options` 引擎级拦截工具（仿 `todo_write`）

LLM 在分析报告后，若有多个互斥可选方案，调用 `present_options(options=[...])`
声明方案；引擎在 Run 结束时捕获为动态 Options。这是确定性通道，不依赖解析
报告文本（解析方案文本脆弱、依赖 LLM 格式遵守）。

### 未采纳的方案

- **解析报告文本提取方案**（nudge 提示词要求"方案A: …"格式，Run 结束时正则
  解析）：无新工具，但依赖 LLM 格式遵守，脆弱。
- **报告末尾 JSON 块声明方案**：仍依赖格式遵守，且 JSON 块可能被 strip/截断。

## 两种模式的选项构建

**无方案模式**（默认，LLM 未调用 `present_options`）：
```
[1] 按报告执行
[2] 输入你的意见
```

**多方案模式**（LLM 调用 `present_options` 声明 N≥2 个方案）：
```
[1] 方案A: <x1>
[2] 方案B: <x2>
...
[N] 方案N: <xN>
[N+1] 输入你的意见
```

- 多方案模式**没有**"按报告执行"项——直接问是否按方案X执行。
- "方案A/B/C"前缀由引擎生成（LLM 只声明原始文本 `options` 数组）。
- 末项"输入你的意见"两种模式统一，选中后回到输入框自由输入
  （用户可用来表达"需要澄清一些问题"等）。

## 改动点

### 1. `engine/agent.go` — 新增常量 + 工具规格

- 常量：`PresentOptionsToolName = "present_options"`。
- `presentOptionsToolSpec(zh bool) ModelTool`：
  - 参数 `{options: array of string}`，schema 用 `"minItems": 2`、`"maxItems": 6`、
    每项 `"minLength": 1`（与运行时校验一致——单个方案无意义，应走普通报告）。
  - 工具描述：**"分析报告后，若你有多个互斥的可选方案，调用本工具声明；
    引擎将按'方案A/B/C'展示供用户选择。只有一个方案或只是分析报告
    （无多方案）时不要调用。"**（中英双语）

### 2. `engine/turn.go` — 装配 + 拦截

- `toolSpecsWithHandoff()`（`turn.go:738-745`）追加 `presentOptionsToolSpec`。
- 工具调用分流（`turn.go:599-611`）：`present_options` 从 `regularCalls`
  排除（纯拦截，不进工具执行器），与 `activate_skill`/`todo_write` 同模式。
- 新增 `processPresentOptionsCalls(calls) []Message`（仿 `processTodoWriteCalls`，
  `turn.go:1514`）：
  - 校验 options：非空数组、每项非空、数量 **2~6**（拒绝 len<2——单个方案应走
    普通报告 + "按报告执行"路径，与"方案A 单个无意义"的用户意图一致）。
    非法 → 返回 Error tool 响应，错误消息提示"单方案请作为普通报告输出，
    不要调用 present_options"。
  - 合法 → 存入新字段 `e.pendingConfirmOptions []string`。
  - 返回 tool 响应 `"✓ 已记录 N 个可选方案，等待用户选择。"`。
  - 处理顺序：与 `processTodoWriteCalls` 并列调用（`turn.go:580-581` 附近）。

### 3. `engine/loop.go` — 动态 Options 构建

- `Engine` 新增字段 `pendingConfirmOptions []string`。**不在 Run 开头重置**
  ——它必须存活到下一次 Run 的 `handleConfirmCommand`（`loop.go:443`，
  早于任何重置点）读取；在确认/反馈处理块之后（`loop.go:472` 附近，即
  `handleAnalysisNudgeConfirmation` 与 pendingEditPlan 分支之间）清除。
  清除时机：用户已通过 `/confirm N` 或"输入你的意见"响应，本组方案已消费。
- `confirmOptions()` 改为方法 `(e *Engine) confirmOptions() []string`：
  - `len(e.pendingConfirmOptions) == 0` → `["按报告执行", "输入你的意见"]`
  - 否则 → 生成 `["方案A: <x1>", ..., "方案N: <xN>", "输入你的意见"]`，
    用 `fmt.Sprintf("方案%c: %s", 'A'+i, opt)` 生成前缀（i 为 0 基，上限 6）。
- 挂载条件不变：`loop.go:932` 仍为 `analysisNudgeCount > 0`。

### 4. `engine/loop.go` — `/confirm N` 语义更新

`handleConfirmCommand`（`loop.go:1628-1650`）：
- **判别条件**：`len(e.pendingConfirmOptions) == 0` → 无方案模式；否则多方案模式。
- **无方案模式**：`N=1`（按报告执行）→ 确认执行，置 `AnalysisReportConfirmed`、
  清 `AnalysisMode`，历史注入 `"✓ 分析报告已确认（按报告执行），可以开始修改代码。"`
  （去掉"方案A："字样）。
- **多方案模式**：`N=1..M`（方案A..M）→ 确认执行，并注入历史
  `"用户选择了方案X：<desc>，请按该方案执行修改。"`。其中 X 为方案标签，
  **复用 `confirmOptions()` 的前缀生成逻辑**（`方案A`/`方案B`/…，同一
  `fmt.Sprintf("方案%c: %s", 'A'+i, opt)`），desc 取 `pendingConfirmOptions[N-1]`
  ——保证弹出框展示的标签与 agent 收到的方案描述一致。
- **末项"输入你的意见"**（两种模式）→ UI 末项自由输入，不发 `/confirm`
  （UI 现有逻辑不变，`model.go:1223-1237`）。
- ⚠️ 行为变更：原设计中 N≥2 是"反馈（保持分析模式）"，现改为"选择该方案并
  执行"——因为多方案模式下方案是真实可选计划；反馈语义走"输入你的意见"通道。

### 5. 文案与测试同步

- `loop.go:1639` 历史注入去掉"方案A："。
- 同步测试字面量：
  - `ui/confirm_test.go:37`、`ui/finish_streaming_test.go:281`：固定项改
    `"按报告执行"` / `"输入你的意见"`。
- 引擎新增测试（`engine/`）：
  - `processPresentOptionsCalls` 捕获合法选项、拒绝空/超限/空数组。
  - 无方案 → `confirmOptions` 返回固定两项。
  - 有方案（N=2）→ 返回 `["方案A: ...", "方案B: ...", "输入你的意见"]`。
  - `/confirm 2`（多方案模式）→ 置确认态 + 历史注入"用户选择了方案B…"。
  - 末项"输入你的意见"不发 `/confirm`（UI 现有测试已覆盖，补多方案断言）。

## 不改的东西（YAGNI）

- `engine/loop.go:1644` N≥2 反馈分支——重写为新语义（选择方案），不再保留。
- docs 归档文档（2026-08-31 设计、plan）是历史记录，不动。
- 不实现"多方案持久化/落盘"——当前 `pendingConfirmOptions` 单 Run 内存即可。

## 数据流

```
LLM 输出分析报告 + 调用 present_options(options=[...])
  → processPresentOptionsCalls 校验 → e.pendingConfirmOptions
  → Run 结束 loop.go:932 analysisNudgeCount>0 → confirmOptions() 动态构建
  → EngineResponse.Options → UI activeOptions → renderOptionsPopup
  → 用户方向键选方案 → Enter → submitConfirm(n) → /confirm N
  → handleConfirmCommand 反查 pendingConfirmOptions[N-1] → 置确认态 + 注入历史
```

## 验收

- 无方案时弹出框：`[1] 按报告执行`、`[2] 输入你的意见`。
- LLM 声明 2 个方案时弹出框：`[1] 方案A: ...`、`[2] 方案B: ...`、`[3] 输入你的意见`。
- 选任意方案 → `/confirm N` 确定性确认执行，历史注入所选方案描述。
- 选"输入你的意见" → 回到输入框自由输入。
- `go build ./...` 与相关测试（`engine`、`ui`）通过。
