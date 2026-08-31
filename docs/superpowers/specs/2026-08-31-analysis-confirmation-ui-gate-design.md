# 方案确认的确定性 UI 通道

日期：2026-08-31
状态：已批准（最小确定性修正 + UI 弹出框选择器）

## 背景与问题

用户确认一个编辑方案时，确认动作被反复误判为 analyze / 反馈，导致需要多次
交互（`a` → `确认执行A` → `赶紧改吧` 才最终执行）。根因有两条：

1. **确认依赖 LLM 解读文本**：`isDangerousConfirmation` 靠穷举关键词名单
   （`engine/loop.go:1075-1118`），`a`、`赶紧` 等真实确认词不在名单内；
   `确认执行A` 因 `A` 不是确认词被 `isConcatOfConfirmWords` 分割失败。
2. **兜底交给 LLM 意图分类器**（`engine/classifier.go:95`），其输入是
   `Goal`（第一条消息整段原文，永不更新，`updateGoalFromFirstMessage` 只在
   空时设置）——旧历史偏置下，用户后续确认消息被判 `analyze`，重新置起
   `AnalysisMode`，下一轮又注入 `[ANALYSIS MODE]` 硬禁令约束
   （`context/builder.go:166-172`），agent 因此宣称"引擎级约束禁止修改、
   要用户去界面解除模式"，形成死循环。

用户决策：**最小确定性修正**——确认动作从 LLM 解读改为 UI 确定性选择 →
引擎确定性状态；不做 plan 落盘。

## 目标

1. 用户确认编辑方案**不再经过** `isDangerousConfirmation` 名单与 intentJudge
   LLM 分类，而是 UI 弹出框确定性选择。
2. 弹出框选项用方向键选择（方案 A/B/C…），末项为"其他（输入你的意见）"：
   - 选方案 → 确定性确认并进入执行
   - 选"其他"/直接输入 → 作为用户意见，走分析/反馈流程
3. 消除 `[ANALYSIS MODE]` 硬禁令措辞与"引擎级禁止修改"的误读。

## 方案决策

### 确定采用：复用 `Options` + 内部 `/confirm N` 命令

- `EngineResponse.Options []string`（`engine/types.go:105`）已存在但引擎从未
  赋值——直接复用为确认选项载体。
- UI 弹出框基础设施已存在（`ui/model.go:1218-1243` 键盘导航、
  `renderOptionsPopup` `:2762`）：现有 Enter 行为是把数字写进输入框再走文本
  提交（`model.go:1228`），改为内部命令直连引擎，不经过文本提交。
- 引擎侧新增 `/confirm N` 保留前缀解析，确定性确认，绕过关键词名单与
  intentJudge。

### 未采纳的方案

- **新增 `EngineRunner.ConfirmOption()` 接口方法**：更显式，但要动接口 +
  mock 测试，改动面大于复用 `Options`，偏离"最小确定性修正"。
- **Claude Code 式 plan 文件落盘**：用户明确不需要强制落盘（C 答复），
  仅 UI 展示。
- **意图分类器兜底保留**：作为确认主通道仍不可靠（本次 bug 根源），
  仅在"其他/输入"路径继续作为反馈语义使用，不再承担确认判定。

## 改动点

### 1. `engine/loop.go` — 新增 `/confirm N` 确定性处理

`Run` 入口（`isClearCommand` 判断旁，`loop.go:1585` 附近）新增保留前缀解析：

```go
// confirmRe 匹配 "/confirm N"（N 为 1 起编号）
var confirmRe = regexp.MustCompile(`^/confirm\s+(\d+)$`)

// Run 开头：
if m := confirmRe.FindStringSubmatch(strings.TrimSpace(userMsg)); m != nil {
    idx, _ := strconv.Atoi(m[1]) // 1-based
    return e.confirmOption(ctx, idx, userMsg)
}
```

`confirmOption` 语义：
- 设置 `AnalysisReportConfirmed = true`、`AnalysisMode = false`、
  `pendingAnalysisNudge = false`（确定性，不调 intentJudge）。
- 若 `e.pendingEditPlan != nil`：走现有确认执行路径（`loop.go:467` 分支），
  执行计划（`pendingEditPlan.Calls`）。
- 否则：仅切换状态，返回简短确认摘要，让 agent 继续。

### 2. `engine/turn.go` — 分析门控命中时返回确认选项

`turn.go:439-475` 分析报告门控阻塞 edit/write 时，`EngineResponse` 填充：

```go
Options: []string{
    "方案A: 按报告执行修改",
    "方案B: 调整方案后执行",
    "方案C: 取消本次修改",
    "其他（输入你的意见）",
},
BlockedBy: "awaiting_confirmation",
```

`turnResult` 增加 `Options []string` 字段（`turn.go:28-30`），透传到
`loop.go:814-821` 的 `EngineResponse.Options`。

### 3. 数据流：`/confirm N` → 确定性判定

`pendingEditPlan.Calls` 当前为单组 tool calls，引擎没有多方案并存能力。
选项的差异化语义（A 直接执行 / B 调整后执行 / C 取消）由 agent 在方案文本中
说明。引擎侧确定性判定（此判定不经过 `isDangerousConfirmation` 与
intentJudge）：

- `/confirm 1`（选"方案A: 按报告执行"）→ 确定性确认：设置
  `AnalysisReportConfirmed=true`、`AnalysisMode=false`，并执行
  `pendingEditPlan.Calls`（走 `loop.go:467` 现有确认执行路径，与现在
  `isDangerousConfirmation` 命中后行为一致）。
- `/confirm N`（N≥2，选"方案B 调整"/"方案C 取消"）→ **不执行**，把用户选择
  （方案编号 + 对应文案）作为反馈注入 `pendingEditPlan` 反馈分支
  （`loop.go:443-464`），让 agent 据此调整方案或确认取消。
- 选"其他（输入你的意见）" → UI 回到输入框自由输入，走普通文本路径
  （引擎按现有 analyze/continue 判定处理，即反馈语义）。

> 说明：`/confirm 1` 是唯一"确认执行"信号；其余选择都是"反馈"信号。
> 这保证只有用户明确选择第一项（按报告执行）时才真正动代码，消除
> "选了取消却执行了"的歧义。

### 4. `ui/model.go` — Enter 选择直连引擎

`model.go:1225-1231`（activeOptions 的 Enter 分支）改为：

```go
case tea.KeyEnter:
    if !msg.Alt {
        // 直接发内部确认命令，不经输入框文本提交
        n := m.selectedOption + 1
        m.activeOptions = nil
        return m, m.submitConfirm(n)
    }
```

新增 `submitConfirm(n int)`：发 `m.engine.Run(fmt.Sprintf("/confirm %d", n))`
并置 `stateRunning`（复用 `submitInput` 的 Run 启动路径）。

末项"其他（输入你的意见）"：Enter 时 `n == len(activeOptions)` →
不发命令，清空 `activeOptions` 回到输入框，用户输入意见后正常 `Run(文本)`。

### 5. `context/builder.go` — 约束措辞软化

`:167` 从"禁止：edit、write、或任何修改文件的操作"改为：

```
[ANALYSIS MODE] 本任务处于分析阶段：请先输出分析报告 / 方案，
等待用户通过确认选项确认后，再执行修改。
```

消除"引擎级永久禁止修改"的误读（本次 bug 中 agent 反复让用户去界面解除
模式的直接原因）。

## 测试

### 引擎（`engine/`）
- `/confirm N` 在 `AnalysisMode=true` 下仍确定性确认，且**不调用**
  intentJudge（stub judge 断言 `called == false`）。
- `/confirm N` 且 `pendingEditPlan != nil` → 走确认执行路径，执行
  `pendingEditPlan.Calls`。
- `/confirm` 未匹配（如 `/confirm` 无编号、普通文本）→ 不触发确认。
- 分析门控命中时 `Options` 返回 4 项（含"其他"）。

### UI（`ui/`）
- Enter 选方案 → 发送 `/confirm N`（而非写入输入框）。
- Enter 选末项"其他" → 回到输入框，不发送命令。

## 范围外（YAGNI）

- `Goal` 承载全文、`memory_markers` 跨任务残留——不动，留作后续可选项。
- Claude Code 式 plan 文件落盘——用户明确不需要（C 答复）。
- 移除 `isDangerousConfirmation` / intentJudge——保留（其他流程仍用），
  仅不再作为确认主通道。
