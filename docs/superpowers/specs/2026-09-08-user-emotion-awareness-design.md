# 移除 IntentClassifier + 用户负面情绪感知

日期：2026-09-08
状态：已批准（方案 A：整体移除 + 关键词情绪检测 + prompt 注入式反思）

## 背景与问题

1. **每轮 LLM 调用浪费**：`IntentClassifier`（`engine/classifier.go:69-160`）在每次 `Run()` 对用户消息做一次 flash 模型分类（`detectUserIntent`，`loop.go:705`），输出 `{"intent": "analyze"|"continue"|"new_topic"}`。其中：
   - `analyze` 职能已被 **analysis report gate**（`turn.go:430-507`）覆盖——该 gate 在 agent 动手改代码前强制要求先出分析报告+用户确认，是引擎级硬保护。即使没有 `IntentAnalyze`，修改前拦截依然生效。
   - `continue` 只是默认值，无信息量。
   - `new_topic` 的唯一价值是重置 `PlanConfirmed`（防用户换话题后旧编辑计划被放行）。用户决策：**不保留**——信任当前上下文结构让 LLM 自行感知话题切换，换话题时旧计划是否放行由模型判断，可接受。
2. **缺乏用户情绪感知**：对话中收到用户负面反馈（"不对"、"没用"、"重新来"、"this sucks" 等）时，agent 没有机制意识到自己路径可能走偏，不会主动反思重规划。claude-code 有 `matchesNegativeKeyword`（纯正则，零 LLM 成本）但只做检测不做行为；本项目需要检测 + 反思行为。

## 目标

1. **移除 `IntentClassifier` 及其全部调用链**，每轮用户消息节省 1 次 flash 模型调用（省钱、省延迟）。
2. **新增中文负面情绪感知**：关键词检测负面反馈 → prompt 注入反思指引，让主 agent 暂停当前路径、反思是否符合需求、重新规划执行路线。零额外 LLM 调用。

## 方案决策

### 1. 移除 IntentClassifier（方案 A + b1 连带删除 AnalysisMode）

- 三态判定（analyze/continue/new_topic）全部删除，不保留任何意图分类。
- `detectIntentShift`（`loop.go:1382`，skill 自动停用机制）**保留**——它是独立的关键词机制，不依赖 LLM。
- `AnalysisMode`（唯一设置点是 intent switch）连带删除（b1）：字段、`[ANALYSIS MODE]` prompt 注入、stop_hook 字段全部移除，不留死代码。
- `pendingAnalysisNudge` **保留**——它属于 analysis report gate（`turn.go:460`），与 intent 无关。

### 2. 负面情绪感知（方案 A：关键词 + prompt 注入，轻量）

- 纯正则/关键词匹配，零 LLM 成本，每轮用户消息跑一次。
- 触发后改写 history 最后一条 user 消息（照 `pendingEditPlan` 反馈路径模式，`loop.go:500-517`），注入反思指引，零新状态、零新流程。

## 改动点

### 第 1 节：移除 IntentClassifier

**`engine/classifier.go`** — 删除：
- `IntentCheck`（69-72）、`IntentJudge`（77-79）、`IntentClassifier`（83-87）、`NewIntentClassifier`（89-91）、`Classify`（95-120）、`parseIntentJSON`（124-140）、`intentFromString`（142-153）、`pickIntentPrompt`（155-160）、`intentClassifierSystemPromptZh/En`（162-176）
- **保留**：`rememberRe` / `extractRememberMarkers` / `isIntermediateText`（被 `turn.go`、`sub_agent.go` 使用）

**`engine/types.go`** — 删除 `UserIntent` 枚举（8-15）与 `AnalysisMode` 字段（264-269）

**`engine/loop.go`** — 删除：
- `intentJudge` 字段（143-146）
- `skillJustActivated` 变量与赋值（457、464）——只被 intent switch 使用
- `SetIntentJudge`（1559-1560）、`NewIntentClassifier()`（1562-1566）
- `detectUserIntent`（1654-1683）
- 意图 switch 块（700-731，含 `IntentAnalyze`/`IntentNewTopic`/`IntentContinue` 分支）
- 所有 `e.state.AnalysisMode = false` 赋值（712、718、722、729、730、814、823、1727、1764、1889）与相关注释
- **保留**：`detectIntentShift`、`isDangerousConfirmation`、`pendingAnalysisNudge` 机制、analysis report gate 逻辑（`turn.go`）

**`engine/stop_hook.go`** — 删除 `AnalysisMode` 字段（18）

**`engine/turn.go`** — 删除 `AnalysisMode: e.state.AnalysisMode` 传参（278）

**`context/builder.go`** — 删除 `[ANALYSIS MODE]` 注入块（168-177）

**`cmd/exec.go:27`** — 删除 `agent.SetIntentJudge(agent.NewIntentClassifier())`

**`ui/runner.go:116`** — 删除 `r.eng.SetIntentJudge(r.eng.NewIntentClassifier())`

**测试文件**：
- `intent_classifier_test.go`：删 6 个 `TestIntentClassifier_*`；**保留** `errBoom` / `stubCompleteModel`（compressor_test、roundtable_test 在用）
- `loop_intent_test.go`：整个文件删除（`stubIntentJudge` + 8 个测试全是 detectUserIntent）
- `confirm_command_test.go`：删 `TestDetectUserIntent_ConfirmCommandFastPath`（91-101）
- `analysis_gate_test.go` / `confirm_command_test.go` / `present_options_test.go` / `present_options_ends_run_test.go`：删测试中 `AnalysisMode: true` 的 state 字段（保留其余断言；确认后 `AnalysisReportConfirmed` 相关断言不变）

### 第 2 节：负面情绪感知

**新文件 `engine/feedback.go`**：

```go
// detectNegativeFeedback 检测用户消息是否为负面情绪反馈（失望/反驳/质疑/停止重做）。
// 纯关键词匹配，零 LLM 成本。
func detectNegativeFeedback(userMsg string) bool
```

**关键词表（5 类，中英双语）**：

| 类别 | 关键词 |
|------|--------|
| 失望/不满 | 不对、错了、没用、白做、白费、浪费、不行、太差、差劲、垃圾、什么玩意、失望、离谱、莫名其妙、一塌糊涂、糊弄 |
| 反驳/纠正方向 | 不是这样、不是我要的、我让你、我让你改、你理解错了、搞错了、想错了、方向不对、走偏了、跑偏了、没按我说的、说反了 |
| 质疑/批评 | 你怎么搞的、就这、就这水平、这也算、什么逻辑、越做越差、还不如之前、能力不行 |
| 停止/重做 | 重新来、重做、再来一遍、推倒重来、停下、停一下、别做了、别改了、撤销、回退、恢复原样 |
| 英文 | this sucks、not what I asked、that's wrong、try again、undo that、why did you、wrong direction |

**防误报规则**：
- `停` 单字不命中（避开"停车场""停止"等正常语句），只用复合词 `停下`/`停一下`/`别做了`
- `不对` 需独立成句或带语气/标点（`不对，`/`不对！`/`不对吧`），避免误伤条件句"如果不对就跳过"
- `/confirm N`、`/clear`、`/collab`、`/debate` 等命令前缀天然不含负面词，不特殊处理
- 确认类词（确认、继续、好的、可以）不命中

**`engine/loop.go`** — 在原意图 switch 位置（700-731 删除后）插入：

```go
// 用户负面反馈：暂停当前路径，反思并重新规划执行路线。
// 改写 history 最后一条 user 消息（照 pendingEditPlan 反馈路径模式），
// 让主 agent 本轮自行反思，不引入新状态。
if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
    if detectNegativeFeedback(userMsg) {
        if e.isChinese {
            e.history[len(e.history)-1].Content = fmt.Sprintf(
                "用户对当前进展给出了负面反馈：%s\n\n请暂停当前执行，反思此前的路径是否正确、是否符合用户需求。找出偏离点，然后重新规划执行路线，向用户说明你的新计划。",
                userMsg)
        } else {
            e.history[len(e.history)-1].Content = fmt.Sprintf(
                "The user expressed negative feedback about the current progress: %s\n\nPause the current work, reflect on whether the previous path was correct and matched the user's needs. Identify where it went off track, then re-plan the execution route and explain your new plan to the user.",
                userMsg)
        }
    }
}
```

**位置说明**：放在所有命令处理（/team /collab /skill /confirm、pendingEditPlan、skill gate approval）之后、原 intent switch 位置——此时 userMsg 是自由文本，且 history 最后一条是 user 消息（命令改写已完成）。

**新文件 `engine/feedback_test.go`**：
1. 关键词命中：5 类各 2-3 个代表词（中文 + 英文）
2. 防误报：`停车场`、`如果不对就跳过`、`确认`、`/confirm 1`、正常请求"帮我加个按钮"均不命中
3. 注入行为：Run 收到负面消息 → history 最后一条被改写为反思指引
4. 语言自适应：英文用户负面反馈 → 英文文案

## 测试

- 删除/修改上述测试文件后，`go test ./engine/ ./ui/ ./cmd/` 全绿
- 重点验证：analysis report gate 在无 AnalysisMode 下仍工作（`turn.go:430` 不依赖它）；`/confirm N` 确认路径不受影响（`handleConfirmCommand` 删除的只是 `AnalysisMode = false` 行）

## 风险与回滚

- **风险**：删除 `AnalysisMode` 后，`[ANALYSIS MODE]` 软提醒消失，analysis gate 成为唯一"先报告后修改"约束。gate 在 `runToolCallCount > 0` 时才触发——纯文本分析（无工具）后直接 edit 的场景不再被软约束提醒。可接受（gate 仍是硬保护）。
- **风险**：负面关键词误报/漏报。误报可通过防误报规则收敛；漏报可通过扩充词表迭代。零成本，低风险。
- **回滚**：git revert 本提交即可完整恢复 IntentClassifier 与 AnalysisMode。
