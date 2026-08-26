# 设计文档：结束判定全面吸收 dsh 结构化逻辑（退役 verdict classifier）

> 日期: 2026-08-26
> 状态: 已批准（brainstorming 阶段输出，用户逐条确认：A 完全 dsh 化 / 彻底 dsh 化 / B 保留 StopHook 框架）
> 关联: 替换 `engine/classifier.go` 的 LLM 结束判定 + `engine/stop_hook.go` 内建 hook + `engine/turn.go` 自问自答守卫

---

## 背景与问题

DeepAct 判断"AI 是否分析结束"的机制（`engine/classifier.go` 的 `ConclusionClassifier` / `VerdictJudge` / `TextVerdict`）：

- 每次模型输出纯文本（无工具调用）时，用 flash 模型做一次 LLM 分类（conclusion / question / intermediate），决定放行结束还是 nudge 续跑（`engine/stop_hook.go`）。
- 模型同一轮既输出文本又带 edit/write 时，用同款判定拦截"自问自答"（`engine/turn.go:349-393`）。
- 子代理的纯文本轮次也复用该判定（`engine/sub_agent.go:364`）。

对比 dsh（deepseek-harness）的结束判定，DeepAct 的问题：

1. **系统替模型猜文本**。dsh 的哲学是"data decides"：纯文本即完成，完成由显式信号（工具结果 `concludesTurn` / goal complete）声明，不反推文本内容。DeepAct 却每轮花一次 flash 调用去猜"这句话像不像结论"。
2. **AnalysisMode 下判定缺位**。`stop_hook.go:84-92` / `158-176` 中 AnalysisMode=true 时纯文本无条件放行——分析模式既不做语义判定，又无 dsh 式的结构兜底，"分析是否结束"完全交给模型自律。
3. **关键词启发式误伤**。`hasTrailingNextStepIntent`（`classifier.go:55`）把"分析"列为动作前缀、完成标记只有"已/完成/通过/了"，误判 "分析结论：问题在 loop.go" 这类文本为 next-step。
4. **提问检测 + 自问自答守卫**。DeepAct 用 LLM 判定提问 → `AwaitUser` 停下等待，并拦截"提问 + edit/write 同现"。dsh 不做提问检测：模型提问就是普通结束回合，用户自然回答。

## 目标（用户确认）

全面吸收 dsh 的结束判定逻辑，退役 verdict classifier（LLM 判定）。具体决策：

- **A. 完全 dsh 化**：移除提问检测 + 自问自答守卫。
- **彻底 dsh 化**：纯文本（零工具调用）直接结束，不做 nudge。
- **B. 保留 StopHook 框架**：`StopHook` 接口 + `runStopHooks` 调度保留（对应 dsh 的 turn-stopping 扩展点），但不再注册任何内建判定 hook。
- **子代理例外**：`sub_agent.go` 保留 `conclusionClassifier`（LLM 判定），因为子代理无用户交互，纯文本直接结束会产出半成品返回给主代理；同时去掉 `hasTrailingNextStepIntent` 二次关键词检查（sub_agent.go:370），只信 LLM 判定。

## 方案决策

### 总原则（dsh 哲学）

1. **纯文本（无工具调用）即完成** — 不判内容、不问结论与否。
2. **完成由显式信号声明** — `task_complete` 工具（= dsh 的 `concludesTurn`），模型主动说"完成了"。
3. **可插拔逃生门** — StopHook 框架保留，默认不再注册任何内建判定 hook。

### 行为变化（用户可见）

| 场景 | 现在 | 改后 |
|------|------|------|
| 模型输出纯文本"让我先看看" | LLM 判定 → nudge 续跑 | **直接结束**，文本展示给用户 |
| 模型分析中途输出"已定位 3 个问题，接下来…" | 判定中间态 → nudge | **直接结束**（AnalysisMode 特判也删除，不再需要） |
| 模型提问"你选 A 还是 B？" | 判定 question → AwaitUser 停下 | **普通结束**，用户自然回答 |
| 模型同一轮又提问又调 edit/write | 自问自答守卫拦截 | **不拦截**，直接执行 |
| 模型显式调 task_complete | 立即完成 | **不变**（保留，唯一显式完成信号） |
| 子代理输出纯文本 | LLM 判定结论/叙述 | **保留 LLM 判定**（无用户交互，需兜底） |

---

### 第 1 节：退役 verdict classifier（engine/classifier.go）

删除（主引擎 verdict 判定路径，全部退役）：

- `VerdictJudge` 接口、`TextVerdict` + `VerdictConclusion`/`VerdictQuestion`/`VerdictIntermediate` + `String()`
- `Classify`、`parseVerdictJSON`、`verdictFromString`、`pickVerdictPrompt`
- `verdictClassifierSystemPromptZh/En`
- `ConclusionJudge` 接口（仅主引擎 StalledNarrationHook 用，已退役）
- `buildToolCallSummary`（仅主引擎结束判定用）
- `hasTrailingNextStepIntent` + 其依赖 `lastSentence`、`isSentDelim`（主引擎 hook 删 + 子代理去关键词后无使用者）

保留：

- `ConclusionClassifier`（struct + 包级 `NewConclusionClassifier` + `IsConclusion` + `parseConclusionJSON` + `conclusionClassifierSystemPromptZh/En` + `pickClassifierPrompt`）— **子代理仍用**（见第 4 节），不退役。
- `ConclusionCheck`（`IsConclusion` 的参数类型，子代理用）。
- `IntentClassifier`（`NewIntentClassifier` + `Classify` + `parseIntentJSON` + intent 双 prompt）— 用户意图分类，设置 AnalysisMode，**非结束判定**，不动。
- `isIntermediateText`（`classifier.go:170`）— 有工具调用时的噪声文本清理（turn.go:221 Layer 3），非结束判定，保留。
- `extractRememberMarkers`（`classifier.go:12`）— 记忆标注提取，保留。

---

### 第 2 节：StopHook 框架保留、清空默认（engine/stop_hook.go）

保留：

- `StopHook` 接口（`Check(ctx, StopHookContext) StopHookResult`）
- `StopHookContext`（字段保留，含 `Goal`/`ToolCallSummary`/`AnalysisMode`，供未来扩展）
- `StopHookResult`（含 `Block`/`Exhausted`/`AwaitUser` 等字段）
- `runStopHooks` 调度（遍历已注册 hook，返回第一个 blocking / AwaitUser / Exhausted 结果）
- `SetStopHooks`（外部注册入口）

删除：

- `ZeroToolCallHook`（struct + Check）
- `StalledNarrationHook`（struct + Check）
- `stalledNudgeMsg`
- `SetStopHooks` 中提取 `e.verdictJudge` 的 type-switch 逻辑（`stop_hook.go:247-256`）
- `NewConclusionClassifier`（Engine 方法，`stop_hook.go:263`，主引擎不再需要）

**默认注册行为**：`cmd/exec.go` 与 `ui/runner.go` 不再注册任何 hook（`SetStopHooks([]engine.StopHook{})` 或直接不调用）。纯文本分支 `runStopHooks` 返回空结果 → `Done=true`，完全 dsh 化。

---

### 第 3 节：turn.go 变更

1. **删除自问自答守卫**（`turn.go:349-393`）：`if e.verdictJudge != nil { ... }` 整块移除。编辑/write 不再因回复文本"像提问"被拦截。
2. **删除 `e.verdictJudge` 引用**（`loop.go:134` 字段、`turn.go:359` 调用）。
3. **纯文本分支**（`turn.go:258-310`）：`runStopHooks` 调用保留，但无 hook 注册时直接返回 `Done=true`。`AwaitUser` / `Block` / `Exhausted` 分支保留（框架兼容，未来 hook 可复用）。
4. **保留 `task_complete` 拦截**（`turn.go:395-418`）：唯一显式完成信号，机制不变。

`loop.go`：
- 删 `verdictJudge VerdictJudge` 字段（`loop.go:134`）。
- `stopHookActive` / `stopHookRetryCount` 字段保留（框架兼容），无 hook 触发时永不置位。

---

### 第 4 节：sub_agent.go 变更（保留 LLM 判定，去关键词）

用户决策：**保留 `conclusionClassifier`**（子代理无用户交互，纯文本直接结束会产出半成品）。

1. 保留 `NewConclusionClassifier(...)` 构造（`sub_agent.go:208`）。
2. 保留纯文本分支的 `conclusionClassifier.IsConclusion` 调用 + critic VERDICT fast-path（`sub_agent.go:354-383`）。
3. **删除 `!hasTrailingNextStepIntent(msg.Content)` 二次检查**（`sub_agent.go:370`）— 不再用关键词覆盖 LLM 判定，只信 LLM。
4. 保留 classifier error 时"保守 nudge"（`sub_agent.go:368-369`）与 `consecutiveIntermediate >= 3` 兜底（`sub_agent.go:385`）。

---

### 第 5 节：cmd/exec.go 与 ui/runner.go

`cmd/exec.go:27-33`：

```go
// 改前
verdict := agent.NewConclusionClassifier()
agent.SetStopHooks([]engine.StopHook{
    &engine.ZeroToolCallHook{MaxRetries: 5, Verdict: verdict},
    &engine.StalledNarrationHook{MaxRetries: 4, Classifier: verdict, Verdict: verdict},
})

// 改后
// 无内建 stop hook：纯文本即结束（dsh 化）。框架保留供未来扩展。
```

`ui/runner.go:96-104` 同理移除注册段。

---

### 退役清单

| 文件 | 移除内容 | 替代 |
|------|----------|------|
| `engine/classifier.go` | `VerdictJudge`、`TextVerdict`、`Classify`、`parseVerdictJSON`、`verdictFromString`、`pickVerdictPrompt`、`verdictClassifierSystemPromptZh/En`、`ConclusionJudge` 接口、`buildToolCallSummary`、`hasTrailingNextStepIntent`、`lastSentence`、`isSentDelim` | 纯文本即结束 + task_complete（`ConclusionClassifier`/`IsConclusion`/`parseConclusionJSON`/conclusion prompt **保留供子代理**） |
| `engine/stop_hook.go` | `ZeroToolCallHook`、`StalledNarrationHook`、`stalledNudgeMsg`、`SetStopHooks` 提取 verdictJudge 逻辑、`NewConclusionClassifier`（Engine 方法） | 框架保留，默认空 |
| `engine/turn.go` | 自问自答守卫（349-393） | 删除（模型可自己问 + 改） |
| `engine/loop.go` | `verdictJudge` 字段 | 删除 |
| `cmd/exec.go` | hook 注册段 | 不注册 |
| `ui/runner.go` | hook 注册段 | 不注册 |
| `engine/sub_agent.go` | `!hasTrailingNextStepIntent` 二次检查（370） | 只信 LLM 判定 |

### 保留（不纳入退役）

- `task_complete` 工具（turn.go:395，= dsh concludesTurn 显式信号）
- `IntentClassifier`（用户意图 → AnalysisMode，非结束判定）
- `StopHook` 框架（`runStopHooks`/`SetStopHooks`/`StopHookContext`/`StopHookResult`，未来扩展点）
- `isIntermediateText`（Layer 3 噪声清理）
- loop guard / error loop / read loop（结构性循环防护）
- `AnalysisMode`（保留于 context 提示 builder.go:155 + intent 检测；不再参与 stop hook 分支）

---

### 测试策略

| 测试文件 | 处置 |
|----------|------|
| `engine/verdict_classifier_test.go` | **删除**（`Classify`/VerdictJudge 已退役） |
| `engine/conclusion_classifier_test.go` | **保留**（`IsConclusion` 仍被子代理使用，测试继续有效） |
| `engine/stop_hook_await_test.go` | **删除**（内建 hook 的 AwaitUser 判定已退役） |
| `engine/stalled_narration_test.go` | **重写**为框架级测试：无 hook 注册时纯文本 → Done；`runStopHooks` 调度行为 |
| `engine/stop_hook_test.go` | **保留并适配**：`runStopHooks` / `SetStopHooks` 框架调度测试 |
| `engine/sub_agent_terminate_test.go` | **保留**：子代理 conclusionClassifier 行为（含 critic VERDICT fast-path） |
| `engine/loop_intent_test.go` / `intent_classifier_test.go` | **保留**（IntentClassifier 不动） |
| `engine/completion_tool_def_test.go` | **保留**（task_complete 不动） |

新增：

- **框架级集成测试**（改写自 stalled_narration_test.go）：`ExecuteTurn` 无 hook 注册时，模型输出任意纯文本 → `Done=true`，直接结束。
- **子代理回归**：子代理纯文本被 LLM 判为结论 → 结束；判为非结论 → nudge；classifier error → 保守 nudge。保持现有覆盖，去掉 `hasTrailingNextStepIntent` 相关用例。

### 边界情况

- **`stopHookActive` / `stopHookRetryCount`**：字段保留（框架兼容），无 hook 触发时永不置位；`turn.go:733` 工具调用后重置逻辑保留（无 hook 时无实际影响）。
- **AnalysisMode**：不再影响结束判定（纯文本一律结束），仅保留 context [ANALYSIS MODE] 提示与 intent 检测。
- **模型提问**：不再有 AwaitUser 拦截。模型问问题时纯文本直接结束并展示给用户，用户自然回答推进下一 Run。
- **子代理**：唯一保留 LLM 结束判定的路径，行为与改动前一致（仅去关键词覆盖）。

---

### 影响范围

- **修改文件**：`engine/classifier.go`、`engine/stop_hook.go`、`engine/turn.go`、`engine/loop.go`、`cmd/exec.go`、`ui/runner.go`、`engine/sub_agent.go`
- **测试文件**：删 `verdict_classifier_test.go`、`conclusion_classifier_test.go`、`stop_hook_await_test.go`；重写 `stalled_narration_test.go`；适配 `stop_hook_test.go`；保留其余
- **新增文件**：无

### 未纳入范围

以下位置与"结束判定"无关，不在本次范围内：

- `engine/roundtable.go` 的裁决关键词匹配（roundtable 流程，独立）
- `engine/compressor.go` / `policy/checker.go` 的关键词（结构字段化，独立主题）
- `engine/loop.go` 的 `isSubstantiveSummary` / `buildRunSummary`（Run 结束的摘要防空壳，非"是否结束"判定，保留）
