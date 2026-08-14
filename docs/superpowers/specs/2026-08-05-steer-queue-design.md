# Steer Queue：运行时用户消息注入

## 背景

DeepAct 的 `Engine.Run()` 是同步阻塞的 turn 循环。用户发送消息后，引擎在循环中运行直到完成或阻塞才返回 `EngineResponse`。期间用户无法补充信息--即使 UI（Bubble Tea）是事件驱动的、能接收输入，也没有通道将输入传递给引擎。

Pi Agent 通过 steering/follow-up 双队列解决了此问题。本设计借鉴 Pi 的 steer 模式，为 DeepAct 增加运行时消息注入能力。

## 目标

- 用户在 agent 运行期间可以输入补充信息
- 补充信息在当前 turn 的工具执行完毕后、下一次 LLM 调用前注入
- 注入的消息作为普通 user message 进入 history，不破坏前缀缓存布局
- agent 完成时如果队列非空，自动继续处理排队消息
- agent 阻塞时队列消息保留，下次 `Run()` 时注入

## 非目标

- 不实现 follow-up 队列（agent 停止后追加新任务）
- 不实现立即中断当前 LLM 流的能力
- 不修改子代理（SubAgentRunner）的执行流程

## 方案选择

| 方案 | 机制 | 优点 | 缺点 | 结论 |
|------|------|------|------|------|
| A. Engine 级 steer 队列 | Engine 持有 `steerQueue []string`，turn 间隙 drain | 最简单，可直接测试，Pi 验证过 | 需要加锁 | **采用** |
| B. Channel 异步输入 | `steerCh chan string`，turn 循环 select | Go 惯用法 | channel 生命周期管理复杂 | 否 |
| C. 回调查询 | UI 管理队列，Engine 通过回调拉取 | 最灵活 | 过度设计，双向依赖 | 否 |

选择方案 A 的理由：改动最小，不引入 channel 生命周期问题，与现有 `sync.Mutex` 风格一致（LoopGuard、ErrorLoopState 等均用 mutex）。

## 详细设计

### 1. Engine 层：队列存储

在 `engine/loop.go` 的 `Engine` struct 中新增字段：

```go
type Engine struct {
    // ...existing fields...
    steerMu    sync.Mutex
    steerQueue []string
}
```

新增公开方法 `Steer`，供 UI 在 agent 运行期间调用：

```go
func (e *Engine) Steer(msg string) {
    msg = strings.TrimSpace(msg)
    if msg == "" {
        return
    }
    e.steerMu.Lock()
    defer e.steerMu.Unlock()
    e.steerQueue = append(e.steerQueue, msg)
    if e.config.OnProgress != nil {
        e.config.OnProgress(ProgressEvent{Type: "steer_queued", Detail: msg})
    }
}
```

新增私有方法 `drainSteerQueue`，在 turn 循环间隙调用：

```go
// drainSteerQueue 将队列中的消息追加到 history。
// 返回 true 表示有消息被注入（调用方据此决定是否继续循环）。
func (e *Engine) drainSteerQueue() bool {
    e.steerMu.Lock()
    pending := e.steerQueue
    e.steerQueue = nil
    e.steerMu.Unlock()

    if len(pending) == 0 {
        return false
    }
    for _, msg := range pending {
        e.history = append(e.history, Message{
            Role:      "user",
            Content:   msg,
            Timestamp: time.Now(),
        })
    }
    if e.config.OnProgress != nil {
        e.config.OnProgress(ProgressEvent{
            Type:   "steer_injected",
            Detail: strings.Join(pending, "\n"),
        })
    }
    loopLog.Printf("steer: injected %d message(s)", len(pending))
    return true
}
```

### 2. 注入点：turn 循环

在 `engine/loop.go` 的 `Run()` 方法中，`executeTurn()` 返回后的三个关键位置注入 drain 调用。

#### 2a. Run() 开头：注入上次 Blocked 保留的消息

用户确认：agent 阻塞时队列消息保留，下次 `Run()` 时注入。

在 `Run()` 方法中，用户消息追加到 history 之后、turn 循环之前，drain 队列：

```go
func (e *Engine) Run(ctx context.Context, userMsg string) (*EngineResponse, error) {
    // ...existing code: language detection, emitEvent, history append...

    // drain 上次 Blocked 保留的 steer 消息，在用户的新消息之后注入
    e.drainSteerQueue()

    // ...existing code: skill matching, intent detection, turn loop...
}
```

注入位置在用户消息之后。drain 出的消息追加到 history 末尾（在用户消息之后），紧接着 turn 循环的第一次 `executeTurn` 会再追加 Block B。最终 LLM 看到的顺序：

```
[...previous history...] [user: new message] [user: queued msg 1] [user: queued msg 2] [Block B]
```

#### 2b. 正常 turn 间隙

`executeTurn` 返回 `Done=false` 且非 Blocked 时，在 `turns++` 之前 drain：

```go
// 现有代码：loop detection 之后
// ...

// drain steer queue before next turn
e.drainSteerQueue()

turns++
```

下一次 `executeTurn` 调用 `context.Build()` 时，排队消息已在 `e.history` 中，会作为正常 user message 出现在 LLM 上下文里。

#### 2c. Done 时队列非空：自动继续

用户确认：agent 完成时如果队列非空，继续运行处理排队消息。

```go
if turnResult.Done {
    completionSummary = turnResult.CompletionSummary
    // 检查是否有排队的 steer 消息
    if e.drainSteerQueue() {
        // 有排队消息，不结束，继续循环让 LLM 处理补充信息
        completionSummary = ""
        continue
    }
    break
}
```

`completionSummary` 清空，避免上一轮的完成摘要泄漏到继续运行后的 `EngineResponse`。继续循环后，`buildRunSummary` 会基于新的 history 重新生成摘要。

### 3. 上下文中的位置与缓存影响

drain 后，下一次 LLM 调用看到的消息序列：

```
[system prompt]          ← 稳定区（缓存命中）
[session context]        ← 稳定区（缓存命中）
[skills list]            ← 稳定区（缓存命中）
[active skill]           ← 稳定区（缓存命中）
[...历史消息...]          ← 历史区（缓存命中）
[assistant + tool calls] ← 上一轮（缓存命中）
[tool results]           ← 上一轮工具结果（缓存命中）
[user: "补充信息"]        ← steer 消息（缓存未命中）
[Block B]                ← 易变尾（缓存未命中）
```

只有 steer 消息 + Block B 是 cache miss，与正常 turn 的 cache miss 量级相同。不破坏前缀缓存布局。

### 4. clearSessionState 清空队列

`/clear` 命令调用 `clearSessionState()`，需要同时清空 steer 队列：

```go
func (e *Engine) clearSessionState() {
    // ...existing code...

    // 清空 steer 队列
    e.steerMu.Lock()
    e.steerQueue = nil
    e.steerMu.Unlock()
}
```

### 5. EngineRunner 接口扩展

`ui/runner.go` 中的 `EngineRunner` 接口新增 `Steer` 方法：

```go
type EngineRunner interface {
    Run(prompt string) tea.Cmd
    Cancel()
    SetProgressChan(ch chan ProgressMsg)
    ValidateConnection() error
    Steer(msg string) // 新增
}
```

`ProgressEngineRunner` 实现：

```go
func (r *ProgressEngineRunner) Steer(msg string) {
    r.getEngine().Steer(msg)
}
```

`DefaultEngineRunner` 实现：

```go
func (r *DefaultEngineRunner) Steer(msg string) {
    r.Eng.Steer(msg)
}
```

### 6. UI 层：stateRunning 时的输入处理

在 `ui/model.go` 的 `Update()` 中，当 `m.state == stateRunning` 且用户按 Enter 提交输入：

当前行为：输入被忽略或缓冲。

新行为：
1. 调用 `m.engine.Steer(text)` 将消息加入引擎队列
2. 在 UI 消息列表中显示该消息，标记为"已排队"
3. 收到 `steer_injected` progress 事件后更新标记为"已注入"

```go
// stateRunning 时提交输入
if m.state == stateRunning && m.inputBuf.Len() > 0 {
    text := m.inputBuf.String()
    m.inputBuf.Reset()
    m.engine.Steer(text)
    // 显示为排队的用户消息
    m.messages = append(m.messages, DisplayMessage{
        Role:    "user",
        Content: text,
        Queued:  true, // 新字段，渲染器据此显示 ⏳
    })
}
```

`DisplayMessage` struct 新增 `Queued bool` 字段。渲染时：
- `Queued=true`：消息前缀显示 `⏳`，文字 dimmed
- `Queued=false`（注入后）：前缀显示 `✅`，文字恢复正常

### 7. Progress 事件

`ProgressEvent.Type` 已是 `string` 类型，无需修改结构体。新增两个 Type 值：

| Type | 触发时机 | Detail 内容 | UI 行为 |
|------|---------|------------|---------|
| `"steer_queued"` | `Steer()` 被调用 | 用户输入的文本 | 消息显示为"⏳ 已排队" |
| `"steer_injected"` | `drainSteerQueue()` 成功注入 | 所有注入消息的拼接 | 消息更新为"✅ 已注入" |

UI 在 progress 事件处理中新增这两个 case。

### 8. Blocked 队列保留

Blocked 时 `steerQueue` 保留在 Engine 实例上。下次 `Run()` 调用时，`drainSteerQueue()` 在用户消息之后注入。

示例流程：

```
用户: "修 bug"
[agent 运行，Blocked: 需要确认危险命令]
用户（运行期间）: "用 sed 替代"  ← Steer，进入队列
[Engine 返回 Blocked 响应]
用户: "继续"  ← 新 Run()
  -> drainSteerQueue 注入 "用 sed 替代"
  -> history: [..., user:"继续", user:"用 sed 替代", Block B]
```

如果用户在 Blocked 后直接退出，队列消息自然丢弃（Engine 被 GC）。这是正确行为--用户放弃了这次会话。

## 边界 case

| 场景 | 处理 |
|------|------|
| 队列为空 | `drainSteerQueue` 返回 false，no-op |
| 多条排队消息 | 全部 drain，按顺序追加到 history |
| Done 时队列非空 | drain 后 continue，不返回给用户 |
| Blocked 时有排队消息 | 不 drain，返回给用户；下次 Run() 时 drain |
| 压缩触发 | steer 消息在 history 中，压缩器正常处理 |
| 上下文溢出 | 下一轮 `executeTurn` 开头的压缩检查会触发 |
| 空字符串 Steer | `Steer()` 内 TrimSpace 后为空则忽略 |
| 多次 Steer 同一消息 | 每次都追加到队列，不去重 |
| /clear 命令 | `clearSessionState()` 中清空 steerQueue |

## 涉及修改的文件

| 文件 | 改动 | 预估行数 |
|------|------|---------|
| `engine/loop.go` | Engine struct 加 `steerQueue`+`steerMu`；加 `Steer()` 和 `drainSteerQueue()`；Run() 加 drain 调用（开头 + turn 间隙 + Done 分支）；`clearSessionState()` 清空队列 | ~40 行 |
| `ui/runner.go` | EngineRunner 接口加 `Steer()`；ProgressEngineRunner 和 DefaultEngineRunner 各加实现 | ~10 行 |
| `ui/model.go` | DisplayMessage 加 `Queued` 字段；stateRunning 时 Enter 调用 Steer；progress 事件处理加 steer case | ~20 行 |

总计约 70 行新增代码，不修改现有逻辑。

## 测试计划

1. **单元测试**（`engine/loop_test.go`）：
   - `Steer()` 后 `drainSteerQueue()` 将消息注入 history
   - Done 时队列非空 -> 自动继续
   - Blocked 时队列保留 -> 下次 Run() 注入
   - 空字符串 Steer 被忽略
   - 多条 Steer 按顺序注入
   - `clearSessionState()` 清空队列

2. **集成测试**：
   - 使用 faux model 模拟 agent 运行中 Steer 调用，验证消息出现在下一轮 LLM 上下文中
