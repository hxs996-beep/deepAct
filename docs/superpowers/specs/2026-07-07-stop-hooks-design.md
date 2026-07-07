# Stop Hooks 设计文档

## 问题

Agent 在分析任务中输出中间叙述（如"查看 buildResult 如何从子代理内容提取 Summary"）后停止循环，要求用户手动输入"继续"。根因是 `isIntermediateText`（`engine/classifier.go:41`）用文本模式匹配判断是否应继续循环，但模式列表太窄（仅 6 个前缀）、标点禁令太严格，导致大量中间叙述被误判为"最终回复"，循环以 `Done=true` 终止。

## 方案

参照 Claude Code 的 stop hooks 架构（`query.ts:1270-1305`、`utils/hooks.ts:3639`），用**结构化的 stop hook 机制**替换文本模式匹配。核心原则：判断模型是否"做完了"，看它"做了什么"（行为信号），而不是看它"说了什么"（文本内容）。

### Claude Code 的 stop hooks 模式

```
while (true) {
  流式接收 LLM 响应
  if (!needsFollowUp) {  // 无 tool_use block
    // 跳过 API 错误（有各自的恢复路径）
    const stopHookResult = handleStopHooks(..., stopHookActive)
    if (stopHookResult.preventContinuation) → 退出
    if (stopHookResult.blockingErrors.length > 0) → 注入消息, 设 stopHookActive=true, continue
    return { reason: 'completed' }
  }
}
```

关键设计点：
- `stopHookActive` 标志：hook 触发续轮后设为 true，hooks 知道自己处于续轮中
- blocking errors 注入为 user message（`isMeta: true`），模型下轮可见
- 无内置计数器——靠 `stopHookActive` + `maxTurns` 兜底
- API 错误跳过 stop hooks——避免 error → hook block → retry → error 死循环

### DeepAct 适配

DeepSeek 模型不如 Claude 可靠，需要计数器作为额外安全网（仿 `sub_agent.go` 的 3-strike 机制）。

## 设计

### 1. StopHook 接口

```go
// engine/stop_hook.go

// StopHookContext 传递给 stop hook 的上下文
type StopHookContext struct {
    RunToolCallCount   int    // 本轮 Run() 累计工具调用数
    LastContent        string // 模型最后输出的文本
    FinishReason       string // finish reason (stop/length/etc)
    StopHookActive     bool   // 是否处于 hook 触发的续轮中
    StopHookRetryCount int    // 连续 hook 阻止次数
    IsChinese          bool   // 语言偏好
}

// StopHookResult hook 返回结果
type StopHookResult struct {
    Block   bool   // 是否阻止退出、注入消息继续循环
    Message string // 注入给模型的 user 消息（Block=true 时有效）
    Reason  string // 阻止原因（日志用）
}

// StopHook 在模型输出纯文本（无 tool_call）后执行
type StopHook interface {
    Check(ctx StopHookContext) StopHookResult
}
```

### 2. 内置 hook: ZeroToolCallHook

检查本轮是否零工具调用。模型输出纯文本但从未调用过工具 → 不可能是最终结论 → 阻止退出，注入 nudge。最多阻止 3 次（仿 sub_agent 3-strike），之后放行。

```go
type ZeroToolCallHook struct {
    MaxRetries int
}

func (h *ZeroToolCallHook) Check(ctx StopHookContext) StopHookResult {
    if ctx.RunToolCallCount > 0 {
        return StopHookResult{} // 有工具调用，不干预
    }
    maxRetries := h.MaxRetries
    if maxRetries <= 0 {
        maxRetries = 3
    }
    if ctx.StopHookRetryCount >= maxRetries {
        return StopHookResult{} // 已 nudge N 次，模型仍不行动，放行
    }
    msg := "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
    if !ctx.IsChinese {
        msg = "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
    }
    return StopHookResult{Block: true, Message: msg, Reason: "zero_tool_calls"}
}
```

nudge 文案采用 `sub_agent.go:745` 的 `getNudgeMessage` 文案，比当前主代理的"继续，请直接使用工具执行下一步。"更 directive——明确说"不要只描述计划"。

### 3. 引擎状态

```go
// engine/loop.go — Engine 结构体新增字段
stopHooks          []StopHook
stopHookActive     bool  // 处于 hook 触发的续轮中
stopHookRetryCount int   // 连续 hook 阻止次数
```

重置时机（两个，仿 Claude Code + sub_agent）：
- **每次 Run() 开始**：`stopHookActive = false; stopHookRetryCount = 0`（同 `runToolCallCount`）
- **有工具调用时**：`stopHookRetryCount = 0`（同 sub_agent 的 `consecutiveIntermediate = 0`）

### 4. runStopHooks 方法

```go
func (e *Engine) runStopHooks(ctx StopHookContext) StopHookResult {
    for _, hook := range e.stopHooks {
        result := hook.Check(ctx)
        if result.Block {
            return result
        }
    }
    return StopHookResult{}
}
```

遍历已注册的 stop hooks，返回第一个 blocking 结果。无 hook block 时返回空结果（允许循环结束）。

### 5. 调用点：替换 turn.go 的 isIntermediateText 分支

当前代码（`turn.go:219-243`）：

```go
if !hasValidToolCalls(toolCalls) {
    if assistant.Content == "" && assistant.ReasoningContent == "" {
        return TurnResult{Done: true, FinishReason: finish}, nil
    }
    e.history = append(e.history, assistant)
    if finish == "length" {
        e.history = append(e.history, Message{Role: "user", Content: "继续", Timestamp: time.Now()})
        return TurnResult{Done: false, FinishReason: finish}, nil
    }
    if isIntermediateText(content) {  // ← 删除
        nudge := "继续，请直接使用工具执行下一步。"
        e.history = append(e.history, Message{Role: "user", Content: nudge, Timestamp: time.Now()})
        return TurnResult{Done: false, FinishReason: finish}, nil
    }
    return TurnResult{Done: true, FinishReason: finish}, nil
}
```

替换为：

```go
if !hasValidToolCalls(toolCalls) {
    if assistant.Content == "" && assistant.ReasoningContent == "" {
        return TurnResult{Done: true, FinishReason: finish}, nil
    }
    e.history = append(e.history, assistant)
    if finish == "length" {
        e.history = append(e.history, Message{Role: "user", Content: "继续", Timestamp: time.Now()})
        return TurnResult{Done: false, FinishReason: finish}, nil
    }

    // Run stop hooks (replaces isIntermediateText)
    hookResult := e.runStopHooks(StopHookContext{
        RunToolCallCount:   e.runToolCallCount,
        LastContent:        content,
        FinishReason:       finish,
        StopHookActive:     e.stopHookActive,
        StopHookRetryCount: e.stopHookRetryCount,
        IsChinese:          e.isChinese,
    })
    if hookResult.Block {
        e.history = append(e.history, Message{
            Role: "user", Content: hookResult.Message, Timestamp: time.Now(),
        })
        e.stopHookActive = true
        e.stopHookRetryCount++
        turnLog.Printf("stop hook blocked: reason=%s retry=%d/%d",
            hookResult.Reason, e.stopHookRetryCount, 3)
        return TurnResult{Done: false, FinishReason: finish}, nil
    }

    return TurnResult{Done: true, FinishReason: finish}, nil
}
```

注意：hook block 时 `assistant` 已经 append 到 history（在 `finish == "length"` 检查之前），所以不需要重复 append。

### 6. isIntermediateText 处置

| 位置 | 用途 | 处置 |
|---|---|---|
| `sub_agent.go:326` | 有 tool_call 时剥离噪声文本 | 保留 |
| `turn.go:191` (Layer 3) | 同上（主代理） | 保留 |
| `turn.go:226` | 无 tool_call 时判断是否 nudge | **删除，替换为 stop hooks** |

`isIntermediateText` 函数本身保留——Layer 3 的用途（有 tool_call 时剥离噪声）是合理的，不受本次改动影响。

### 7. 注册 hook

在 `cmd/run.go` 中创建引擎时注册：

```go
engine.SetStopHooks([]engine.StopHook{
    &engine.ZeroToolCallHook{MaxRetries: 3},
})
```

`SetStopHooks` 是 Engine 上的 setter 方法，与现有 `SetMaxOutputTokens` 等 setter 模式一致。不修改 `EngineConfig` 结构体。

## 改动文件清单

| 文件 | 改动 |
|---|---|
| `engine/stop_hook.go` | **新建**：StopHook 接口、StopHookContext、StopHookResult、ZeroToolCallHook、runStopHooks |
| `engine/loop.go` | Engine 加 `stopHooks` + `stopHookActive` + `stopHookRetryCount` 字段；Run() 开头重置 |
| `engine/turn.go` | 删除 `isIntermediateText(content)` 分支（line 226），替换为 `runStopHooks` 调用；工具调用后重置 `stopHookRetryCount` |
| `engine/classifier.go` | 不改（`isIntermediateText` 保留，Layer 3 仍用） |
| `cmd/run.go` | 注册 `ZeroToolCallHook` |

## 不做的事

- **不改 `isIntermediateText` 函数本身**——Layer 3 仍需要它
- **不改 `finish == "length"` 分支**——max_output_tokens 恢复是独立路径，不在本次范围
- **不加外部/用户可配置 hook**——当前只做内置 hook，接口预留扩展
- **不改 sub_agent.go**——子代理已有自己的 3-strike 机制，不需要 stop hooks

## 测试计划

1. **ZeroToolCallHook 单元测试**：
   - `runToolCallCount == 0, retryCount == 0` → Block=true
   - `runToolCallCount == 0, retryCount == 2` → Block=true
   - `runToolCallCount == 0, retryCount == 3` → Block=false（放行）
   - `runToolCallCount > 0` → Block=false（不干预）

2. **turn.go 集成测试**：
   - 模型输出纯文本无 tool_call，`runToolCallCount == 0` → 验证 nudge 注入 + Done=false
   - 模型输出纯文本无 tool_call，`runToolCallCount > 0` → 验证 Done=true（正常结束）
   - 连续 3 次 hook block 后 → 验证 Done=true（放行）

3. **回归测试**：
   - 有 tool_call 时 Layer 3 仍正常剥离噪声文本
   - `finish == "length"` 仍正常自动继续
