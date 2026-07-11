# Stop Hook LLM 结论判定 设计文档

## 问题

`StalledNarrationHook`（`engine/stop_hook.go:75`）用 `looksLikeNextStepNarration`（`engine/classifier.go:69`）判定"纯文本回合是否中间叙述"。该启发式只做**关键词前缀匹配**：文本去前导符号后须以 `nextStepPrefixes`（"查看/让我/接下来/Let me/..."）之一开头、且不含 `conclusionMarkers`，才 block；否则放行 -> `Done=true` -> 整个 Run 结束。

盲区：模型在多步任务中途输出的中间文本，大量**不**以下一步动词开头（如"上述修改已写入 turn.go。下面运行测试验证。"、"目前看到三个候选位置，分别对应不同路径。"）。这些非前缀型中间叙述被当成最终结论，循环提前终止，用户只能手动输入"继续"触发新 Run。

`stalled_narration_test.go` 的 `reportedStallExamples` 恰好都以"查看/继续验证"开头，测试全绿但覆盖不了真实分布。根因是用静态关键词去判定一个语义问题（这段文本是不是最终结论）。

## 方案

取消关键词前缀判定，改用 **LLM 判定**：纯文本回合（`runToolCallCount>0`）调一次轻量 `Complete`，对照用户目标判断助手文本是否最终结论。是结论 -> 放行结束；不是 -> nudge 续接；调用失败 -> 保守 nudge；连续 nudge 到上限 -> `Blocked` 交回用户。

复用 `compressor.go:140-150` 的轻量判定范式（`Complete` + `Temperature:0` + `JsonMode:true` + flash 模型），不引入新调用通道或依赖。`ZeroToolCallHook` 不变（`runToolCallCount==0` 是明确场景，无需 LLM 成本）。

## 设计

### 1. 新增 ConclusionClassifier

独立单元，放 `engine/classifier.go`，职责单一：给定"用户目标 + 助手文本"，返回是否最终结论。

```go
// engine/classifier.go

// ConclusionClassifier 用轻量 LLM 调用判定助手文本是否为对用户目标的最终结论。
// 复用 compressor 的 Complete + JsonMode 范式，跑 flash 模型控制成本。
type ConclusionClassifier struct {
    model          ModelClient
    flashModelName string
    isChinese      bool
}

func NewConclusionClassifier(model ModelClient, flashModelName string, isChinese bool) *ConclusionClassifier {
    return &ConclusionClassifier{model: model, flashModelName: flashModelName, isChinese: isChinese}
}

// IsConclusion 返回 true 表示 text 是对 goal 的最终结论/完成总结；
// false 表示中间过程/下一步计划/部分结果/待办陈述；
// err 表示 LLM 调用或解析失败（调用方按保守策略处理）。
func (c *ConclusionClassifier) IsConclusion(ctx context.Context, goal, text string) (bool, error)
```

判定器是纯逻辑单元（输入 model/goal/text，输出 bool/err），可独立 stub 测试，不耦合 Engine 内部状态。

### 2. 判定 prompt

复用 `compressor.go:140-150` 范式：`ModelRequest{Model: flashModelName, Messages, Temperature:0, JsonMode:true}`，`max_tokens=64`。

- **system**：判定规则--"给定用户目标与助手回复，判断回复是否为对目标的**最终结论/完成总结**。中间过程、下一步计划、部分结果、待办陈述均判 false。仅输出 JSON：`{"conclusion": bool}`。"（中英双语两份，按 `isChinese` 选取，仿 `compressor` 的 `pickPrompt`。）
- **user**：`目标：{goal}\n\n助手回复：{text}`
- **输出**：`{"conclusion": bool}`，`JsonMode:true` 强制合法 JSON。
- **超时**：调用套 `context.WithTimeout(ctx, 10*time.Second)`。

`goal` 取自 `e.state.Goal`（`engine/types.go:216`，`loop.go:1031` 等处在用），是当前 Run 的用户目标。`runToolCallCount>0` 时 Run 必有用户指令，`Goal` 正常非空；若异常为空，判定器基于文本本身是否像完成总结判断，`MaxRetries` 保守兜底不受影响。

### 3. StalledNarrationHook 改造

去掉 `looksLikeNextStepNarration`，持有 `Classifier`，在 `runToolCallCount>0` 时调 `IsConclusion`。

```go
type StalledNarrationHook struct {
    MaxRetries int
    Classifier *ConclusionClassifier // 新增：LLM 判定器
}

func (h *StalledNarrationHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
    if sc.RunToolCallCount == 0 {
        return StopHookResult{} // ZeroToolCallHook 负责
    }
    maxRetries := h.MaxRetries
    if maxRetries <= 0 {
        maxRetries = 2
    }
    if sc.StopHookRetryCount >= maxRetries {
        return StopHookResult{Exhausted: true} // 兜底：连续 nudge 到上限仍非结论
    }
    isConclusion, err := h.Classifier.IsConclusion(ctx, sc.Goal, sc.LastContent)
    if err != nil {
        // 保守策略：判定失败视为"非结论"，nudge 续接（不提前结束）
        turnLog.Printf("conclusion classifier error: %v (conservative block)", err)
        return StopHookResult{Block: true, Message: nudgeMsg(sc), Reason: "classifier_error"}
    }
    if isConclusion {
        return StopHookResult{} // 放行 -> Done
    }
    return StopHookResult{Block: true, Message: nudgeMsg(sc), Reason: "stalled_narration"}
}
```

nudge 消息沿用现有中英双语逻辑（含 retry 时引用 `LastContent` 片段），不依赖关键词。

### 4. 接口变更

两个改动点：

**(a) `StopHook.Check` 加 `context.Context`**：LLM 判定要支持超时/取消。

```go
type StopHook interface {
    Check(ctx context.Context, sc StopHookContext) StopHookResult
}
```

`ZeroToolCallHook` 实现多一个 `ctx context.Context` 参数（忽略不用），逻辑不变。`runStopHooks` 透传 `ctx`。

**(b) `StopHookContext` 加 `Goal string`**：供判定对照用户目标。

```go
type StopHookContext struct {
    RunToolCallCount   int
    LastContent        string
    FinishReason       string
    StopHookActive     bool
    StopHookRetryCount int
    IsChinese          bool
    Goal               string // 新增：当前 Run 的用户目标（e.state.Goal）
}
```

`turn.go:225` 构造 context 时填 `Goal: e.state.Goal`。

### 5. 数据流

```
纯文本回合 (无有效 tool_calls)  turn.go:211
  ├─ content+reasoning 均空          -> Done=true（不变）
  ├─ finish=="length"                -> 注入"继续"续接（不变，绕过 hooks）
  └─ runStopHooks(ctx, sc):
       ZeroToolCallHook:  runToolCallCount==0             -> block nudge
       StalledNarrationHook (runToolCallCount>0):
          retry >= MaxRetries                            -> Exhausted -> Blocked 交回用户
          IsConclusion(goal, text) == true               -> 放行 -> Done=true
          IsConclusion == false                          -> block nudge
          IsConclusion 出错/超时                          -> 保守 block nudge
```

### 6. Engine 暴露判定器构造

`engine/loop.go` 新增方法，用 Engine 已有字段构造判定器，不暴露 `e.model` 私有字段：

```go
func (e *Engine) NewConclusionClassifier() *ConclusionClassifier {
    return NewConclusionClassifier(e.model, e.config.FlashModelName, e.isChinese)
}
```

`cmd/exec.go:26` 注册时注入：

```go
classifier := agent.NewConclusionClassifier()
agent.SetStopHooks([]engine.StopHook{
    &engine.ZeroToolCallHook{MaxRetries: 5},
    &engine.StalledNarrationHook{MaxRetries: 4, Classifier: classifier},
})
```

## 错误处理与兜底

- **LLM 调用失败**（网络/超时/JSON 解析失败）-> 保守 block（视为非结论，nudge）+ 记日志。对冲"提前结束"--宁可多 nudge 也不误放行。
- **MaxRetries 兜底**：连续 nudge 到上限（MaxRetries=4）仍判"非结论" -> `Exhausted` -> `turn.go:248` 返回 `Blocked=true` 交回用户。防 LLM 持续误判或死循环。机制已存在，保留。
- **阻塞控制**：判定在 turn 主路径同步执行，套 10s 超时；失败走保守 block，不阻断 loop。
- **成本**：判定只在"纯文本回合 + `runToolCallCount>0`"触发，非每 turn。flash 模型 + 短 prompt + `max_tokens=64`，单次成本与 compressor 一次压缩同档。

## 测试策略

- **`ConclusionClassifier` 单元测试**（stub `ModelClient`）：
  - 返回 `{"conclusion":true}` -> `IsConclusion=true`
  - 返回 `{"conclusion":false}` -> `IsConclusion=false`
  - 非法 JSON / 调用出错 -> 返回 error
  - 断言请求 prompt 含 `goal` 与 `text`、`Model` 为 flashModelName、`JsonMode=true`
- **`StalledNarrationHook` 改造测试**（stub classifier，替代现有关键词用例）：
  - `runToolCallCount>0` + `IsConclusion=false` -> block（覆盖非前缀中间态，如"上述修改已写入 turn.go。下面运行测试验证。"）
  - `IsConclusion=true` -> 放行
  - classifier error -> 保守 block
  - `retry>=MaxRetries` -> Exhausted
  - `runToolCallCount==0` -> 不调 classifier、放行（交 ZeroToolCallHook）
- **`executeTurn` 集成测试**：stub classifier 覆盖中间态 nudge / 真结论 Done / error block（沿用 `stalled_narration_test.go` 现有 `stubStreamModel` 范式，把 `stopHooks` 换成带 stub classifier 的 `StalledNarrationHook`）。
- **删除** `TestLooksLikeNextStepNarration` 及 `looksLikeNextStepNarration`/`nextStepPrefixes`/`conclusionMarkers`（仅 StalledNarrationHook 用，废弃）。
- **保留** `isIntermediateText`（`turn.go:191`，有工具调用时的噪音清理，独立路径）。

## 文件改动清单

| 文件 | 改动 |
|---|---|
| `engine/classifier.go` | 新增 `ConclusionClassifier` + 双语 system prompt；删 `looksLikeNextStepNarration`/`nextStepPrefixes`/`conclusionMarkers`；保留 `isIntermediateText`、`extractRememberMarkers` |
| `engine/stop_hook.go` | `StopHook.Check` 加 `ctx context.Context`；`StopHookContext` 加 `Goal string`；`StalledNarrationHook` 持有 `Classifier`、改调 `IsConclusion`；`ZeroToolCallHook` 签名适配；`runStopHooks` 透传 ctx |
| `engine/turn.go:225` | context 填 `Goal: e.state.Goal`；`runStopHooks` 调用传 `ctx` |
| `engine/loop.go` | 新增 `NewConclusionClassifier()` 方法 |
| `cmd/exec.go:26` | `classifier := agent.NewConclusionClassifier()`，注入 `StalledNarrationHook{Classifier: classifier, MaxRetries:4}` |
| `engine/stalled_narration_test.go` | 改 stub classifier、删旧关键词用例、新增 classifier error / 非前缀中间态用例 |
| 新增 `engine/conclusion_classifier_test.go` | `ConclusionClassifier` 单元测试 |

## 范围边界

本次只把 `StalledNarrationHook` 的关键词判定换成 LLM 判定。`isIntermediateText`（`turn.go:191`，有工具调用时清空纯意图噪音）是独立路径，不改--若后续也想让它去关键词化，可另开一轮。
