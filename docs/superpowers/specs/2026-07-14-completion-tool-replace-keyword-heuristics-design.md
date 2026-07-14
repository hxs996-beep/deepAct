# 设计文档：用 task_complete 工具替换关键字流程判断

> 日期: 2026-07-14
> 状态: 已批准（brainstorming 阶段输出）
> 关联: 替换 `engine/classifier.go` 关键词启发式 + `engine/stop_hook.go` 阻断逻辑

---

## 背景与问题

DeepAct 的 agent 循环通过 stop hook 决定"模型纯文本回复是否为最终结论"。当前机制在 `classifier.go` 中维护大量硬编码中英文关键词列表（`hasFutureIntent`、`hasTrailingNextStepIntent`、`hasCompletionMarker`、`isIntermediateText`），用 `strings.Contains` / `strings.HasPrefix` 匹配文本来控制流程走向。

### 已证实的脆弱性

1. **"需要"误命中**：分析报告末尾"需要我针对某个具体位置展开方案设计吗？"中的"需要"命中 `futureIntentMarkers`，被 `hasFutureIntent` 判定为"未来意图/中间态"，触发 `StalledNarrationHook` 硬阻断并注入 nudge。这是自证实实例--分析该问题的报告本身被该问题阻断。

2. **i18n 脆弱**：关键词列表覆盖有限，换一种说法（如"我接着看下"/"gonna check"）即漏判。

3. **skill 对话冲突**：brainstorming 等 skill 要求纯文本对话（提问、提方案），但 stop hook 对纯文本回复一律检查并可能阻断，导致 skill 活跃期间连续误 nudge。

4. **代码注释记录的历史事故**：fake "完成" summary、"综上，需要..."被误判为结论、中间发现被误判为完成等。

## 设计方案：task_complete 工具 + LLM 兜底

核心思路：不再从自然语言"猜"模型是否完成，让模型通过工具调用**显式声明**完成。工具调用是结构化信号，不依赖文本内容猜测。

---

### 第 1 节：task_complete 工具机制

#### 工具定义

新增工具 `task_complete`，注册方式与 `activate_skill`、`handoff_to_agent` 一致--在 `toolSpecsWithHandoff()`（`engine/turn.go:631`）追加。

```
名称: task_complete
参数: summary (string, required) - 返回给用户的最终输出文本
描述: 提交最终结论/回复给用户。目标全部完成后调用此工具。这是向用户返回输出的唯一方式。
```

工具定义位置：`engine/agent.go`（与 `HandoffToolName`、`ActivateSkillToolName` 常量并列）。

#### 拦截点

复用现有特殊工具拦截模式（`engine/turn.go:465-499`）。在 `processActivateSkillCalls` 之后、常规工具执行之前，扫描 `calls` 是否含 `task_complete`：

- **有 `task_complete`**：提取 `summary` 参数，设 `TurnResult{Done: true, CompletionSummary: summary}`，**不**走常规 Executor，**不**回传 tool result 给模型（循环直接终止）。
- **无 `task_complete`，有其他工具**：正常执行，循环继续。
- **无任何工具调用**（纯文本）：进入 stop hook（见第 2 节）。

#### TurnResult 变更

`engine/turn.go:18` 的 `TurnResult` 结构体新增字段：

```go
type TurnResult struct {
    // ... 现有字段 ...
    CompletionSummary string // task_complete 工具的 summary 参数，模型显式声明完成时设置
}
```

`engine/loop.go:729` 处 `Done=true` 时，优先用 `CompletionSummary` 构造 `EngineResponse.Summary`；若为空则回退到取最后一条 assistant 消息内容（兼容现有行为）。

#### 工具响应消息

与 `activate_skill` 一致，`task_complete` 调用后仍需向 DeepSeek API 提供一条 tool response 消息（满足 `assistant(tool_calls) -> tool` 的 API 契约），但该消息写入 history 后循环即终止，不会再发起下一轮模型调用。响应内容为简单的确认文本（如 "Task complete."）。

---

### 第 2 节：Stop hook 简化

#### 现有流程

`engine/turn.go:225-263`，纯文本回复（无工具调用）时触发：

```
ZeroToolCallHook     -> RunToolCallCount==0? 阻断(max 3)
StalledNarrationHook -> RunToolCallCount>0?
                       hasFutureIntent?           硬阻断(不调LLM)
                       hasTrailingNextStepIntent? 硬阻断(不调LLM)
                       ConclusionJudge.IsConclusion?
                         conclusion + hasCompletionMarker? 放行
                         conclusion + retry>0?            放行
                         conclusion + 无标记 + retry==0?  阻断(未确认)
                         not conclusion?                  阻断
                         error? hasCompletionMarker? 放行 : 阻断
```

#### 新流程

两个 hook 合并为单一 `CompletionToolHook`：

```
CompletionToolHook -> retry < MaxRetries?
                       阻断, nudge: "调用 task_complete 提交回复，或继续使用工具执行"
                     retry >= MaxRetries?
                       LLM 兜底: ConclusionJudge.IsConclusion?
                         conclusion  -> 放行(模型确属完成，只是忘了调工具)
                         非conclusion/error -> Exhausted(返回 Blocked)
```

#### 核心简化

纯文本 = 没调 `task_complete` = 未完成。"本轮零工具调用"与"调过工具但纯文本收尾"的区分不再必要--两种情况都意味着模型未发出完成信号。

- 关键词预检（`hasFutureIntent`、`hasTrailingNextStepIntent`）全部移除。
- 完成标记守卫（`hasCompletionMarker`）全部移除。
- `ConclusionJudge` 从主裁判降级为 MaxRetries 耗尽后的唯一兜底。
- `ZeroToolCallHook` 和 `StalledNarrationHook` 合并为 `CompletionToolHook`。

#### nudge 消息

简化为单一消息（不再按 retry 引用模型原文）：

- 中文：`"你的回复未通过 task_complete 提交。调用 task_complete 工具提交最终结论，或继续使用工具执行下一步。"`
- 英文：`"Your reply was not submitted via task_complete. Call the task_complete tool to submit your final conclusion, or continue using tools to proceed."`

#### 子代理同步

`engine/sub_agent.go:335,361` 内联了 `isIntermediateText` + `hasTrailingNextStepIntent` 逻辑，同样替换为 `task_complete` 检测 + 简化兜底。子代理的模型也获得 `task_complete` 工具规格。

---

### 第 3 节：系统提示词 + Skill 对话兼容

#### 系统提示词修改

在 `context/promptset/zh/system.md` 的"回复格式"节后增加：

```markdown
## 完成信号
目标全部完成后，调用 `task_complete` 工具提交最终结论。这是向用户返回输出的唯一方式。
- 有工具调用时继续执行，不要给出纯文本结论
- 简单问答也通过 `task_complete` 返回答案
- 纯文本回复（无任何工具调用）会被拦截并要求重新提交
```

该指令位于稳定区（系统提示词，构建一次、缓存），每轮可见。

#### Skill 对话兼容

brainstorming 等 skill 要求纯文本对话（提问、提方案、呈现设计）。新机制下：

1. 模型调用 `task_complete(question_text)` 提交问题/方案给用户。
2. 引擎返回 `CompletionSummary` 给用户。
3. 用户回应，开启新 `Run()`。
4. 因为 `task_complete` 是工具调用，stop hook 的"纯文本"分支根本不会触发--skill 对话不再被 nudge 干扰。

这是当前痛点的根本解法：不再有"纯文本回复被关键词误判为中间态"的情况，因为所有提交给用户的输出都通过 `task_complete` 工具调用发出。

#### isIntermediateText 处理

`classifier.go:170` 的 `isIntermediateText` 当前用于 `turn.go:191` 在工具调用伴随时清理中间叙述文本（如 "Let me check..."）。该函数可一并退役--流程不再依赖文本内容判断走向。若仍想清理输出噪声，可保留一个极简版本，但属可选优化，非流程必需。

---

### 第 4 节：测试策略

| 测试文件 | 内容 |
|----------|------|
| 新增 `completion_tool_test.go` | `task_complete` 拦截：模型调用时 `TurnResult.Done=true`、`CompletionSummary` 正确提取、不走常规 Executor、不回传 tool result |
| 新增 `completion_hook_test.go` | `CompletionToolHook`：纯文本 -> 阻断 + nudge；MaxRetries -> LLM 兜底放行/Exhausted |
| 新增 `completion_skill_test.go` | brainstorming 激活时，`task_complete(question)` 正常返回给用户、不被 stop hook 拦截 |
| 新增 `sub_agent_completion_test.go` | `sub_agent.go` 的 `task_complete` 检测与主引擎一致 |
| 改写 `stalled_narration_test.go` | 移除关键词相关测试，改写为 `CompletionToolHook` 行为测试 |
| 改写 `classifier_test.go` | 移除 `isIntermediateText`、`hasTrailingNextStepIntent` 测试 |

---

### 退役清单

| 文件 | 移除内容 | 替代 |
|------|----------|------|
| `engine/classifier.go` | `hasFutureIntent`、`hasTrailingNextStepIntent`、`hasCompletionMarker`、`isIntermediateText`、`completionMarkers`（var）、`futureIntentMarkers`（var）及所有内嵌关键词列表 | `task_complete` 工具调用检测 |
| `engine/stop_hook.go` | `ZeroToolCallHook`（struct + Check）、`StalledNarrationHook`（struct + Check）、`stalledNudgeMsg` | `CompletionToolHook`（单一 hook + 简化 nudge） |
| `engine/classifier.go` | `ConclusionJudge` 主裁判角色 | 保留，降级为 MaxRetries 兜底 |
| `engine/sub_agent.go:335,361` | 内联的 `isIntermediateText` + `hasTrailingNextStepIntent` | 同款 `task_complete` 检测 |

---

### 影响范围

- **新增文件**：无强制要求。工具常量 + spec 放 `engine/agent.go`（与 `HandoffToolName`、`ActivateSkillToolName` 并列），拦截逻辑放 `engine/turn.go`（与 `processActivateSkillCalls` 并列），`CompletionToolHook` 放 `engine/stop_hook.go`（替换旧 hook）。若 `turn.go` 过大可拆出新文件，但属实现期决策。
- **修改文件**：
  - `engine/turn.go`：`TurnResult` 新增 `CompletionSummary` 字段、`toolSpecsWithHandoff` 追加 `task_complete`、`executeTurn` 增加拦截逻辑、移除 `isIntermediateText` 调用（`turn.go:191`）
  - `engine/loop.go`：`Done=true` 时优先用 `CompletionSummary` 构造 `EngineResponse.Summary`
  - `engine/stop_hook.go`：`ZeroToolCallHook` + `StalledNarrationHook` 替换为 `CompletionToolHook`
  - `engine/classifier.go`：移除 `hasFutureIntent`、`hasTrailingNextStepIntent`、`hasCompletionMarker`、`isIntermediateText`、`completionMarkers`、`futureIntentMarkers` 及所有关键词列表；保留 `ConclusionJudge`
  - `engine/sub_agent.go`：`335`、`361` 行内联判断替换为 `task_complete` 检测
  - `engine/agent.go`：新增 `TaskCompleteToolName` 常量 + `taskCompleteToolSpec()` 函数
  - `context/promptset/zh/system.md`：新增"完成信号"节
  - `cmd/exec.go`、`ui/runner.go`：hook 注册从 `ZeroToolCallHook` + `StalledNarrationHook` 改为 `CompletionToolHook`
- **改写测试**：`stalled_narration_test.go`、`classifier_test.go` 移除关键词测试，改写为新机制测试

### 未纳入本设计范围的位置

以下位置同样使用关键字判断，但替换机制不同，不在本设计范围内：

- `engine/compressor.go`：`[SESSION ARCHIVE]` 前缀 -> `Message.Kind` 结构化字段
- `policy/checker.go:120-140`：破坏性操作子串匹配 -> 工具元数据 `Destructive` 字段
- `engine/roundtable.go:582`：裁决关键词匹配 -> 显式命令或 IntentClassifier
- `ui/model.go:489-495`：emoji 推断裁决 -> 结构化裁决字段
- `engine/roundtable.go:545` + `ui/model.go:1754`：score 文本解析 -> LLM 输出结构化 JSON
