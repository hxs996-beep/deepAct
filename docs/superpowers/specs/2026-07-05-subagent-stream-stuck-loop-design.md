# 子 agent 流式空行与卡死循环修复设计

日期：2026-07-05
状态：设计阶段

## 问题

用户在 DeepAct 会话中输入 `critic 这个子agent的stream 输出过程中 为什么两行文字之前有大量空行` 后，出现两个症状：

1. **子 agent 流式输出中两行文字之间出现大量空行**
2. **卡死**：之后无论输入什么，回复始终是同一句旧旁白 ——
   `关键路径在 sub_agent.go 产生 stream_delta 、 ui/model.go 消费并渲染。我并行读取这几段。`

会话记录 `~/.deepact/sessions/session-1783258799055831000.jsonl` 第 1 条消息（21:40）即用户贴入的 bug 描述，证实这是 DeepAct 用自身调试自身空行 bug 时触发：那句旁白是 DeepAct 作为 assistant 生成的分析旁白，进入 `e.history` 后再也甩不掉。

## 根因

### 根因 1：`buildRunSummary` 无轮次边界，回吐旧轮旁白

`engine/loop.go:770-798`：

```go
func buildRunSummary(history []Message, toolCallCount int, zh bool) string {
    summary := ""
    for i := len(history) - 1; i >= 0; i-- {
        if history[i].Role == "assistant" && history[i].Content != "" {
            summary = history[i].Content
            break
        }
    }
    ...
}
```

它从**整条 history** 往前找最近一条 `Content != ""` 的 assistant 消息，没有"本轮"概念。

DeepSeek 习惯裸发工具调用不带正文；`engine/turn.go:191-193` 又把伴随工具调用的意图文本剥成空：

```go
if hasValidToolCalls(toolCalls) && isIntermediateText(content) {
    content = ""
}
```

`isIntermediateText`（`engine/classifier.go:41`）只剥 "让我/我来/我先…" 这类**纯意图短句**（含 `、。；，\n` 即判否）。而 `关键路径在…我并行读取这几段。` 含 `、` 和 `。`，不会被剥，作为带正文的 assistant 消息留在 history。

之后 agent 陷入"只发工具调用、不发正文"的循环，被 loop guard / max_turns 兜底终止，走到 `loop.go:660`（Blocked）或 `loop.go:753`（正常结束）调用 `buildRunSummary`。本轮没有新增带正文 assistant 文本 → 一路向前找到那条旧旁白 → 原样返回。下一轮重复 → 卡死式重复。

### 根因 2：`stream_delta` 整段重发导致累积

`engine/sub_agent.go:286-291`：

```go
} else if resp.Message.Content != "" {
    preview := firstLine(resp.Message.Content, 60)
    r.onProgress(ProgressEvent{Type: "thinking", ...})
    r.onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: resp.Message.Content})
}
```

子 agent 用**非流式** `model.Complete`（`sub_agent.go:264`），`resp.Message.Content` 是整段完整内容。但每个"产正文、无工具调用"的循环迭代都把**整段内容**当 `stream_delta` 发一次。

UI 侧是累加（`ui/model.go:672-674`）：

```go
case "stream_delta":
    m.streaming += msg.Detail
```

`m.streaming +=` 是为**主 agent 增量 delta** 设计的累加语义，但子 agent 发的是**整段**。子 agent 循环迭代 2~3 次（`consecutiveIntermediate < 3`，`sub_agent.go:316-322`）或并行 handoff 多个 agent 时，整段内容被重复拼接，段落数成倍膨胀。`renderStreaming` 只把 3+ 连续换行压成 2（`model.go:2277-2279`），2 换行（1 空行）的间隙保留，视觉上即大量空行夹着重复文字。

子 agent 循环有 `maxIterations` 上界（`sub_agent.go:205`），不会无限循环，故空行是"成段重复"造成，非无限累积。

## 设计

范围：**最小确定性修复**（用户确认）。修掉误导性摘要与空行累积，不加停滞恢复机制、不动模型行为。

### 第 1 节 · `buildRunSummary` 加轮次边界（方案 A）

**数据流：**

- `Engine` 新增字段 `runStartHistoryLen int`。
- 在 `Run()` 中，**intent 检测之后、turn 循环之前**赋值 `e.runStartHistoryLen = len(e.history)`。
  - 选此时机：skill 激活与 `pendingPinnedMessages` 注入可能在本轮 append history 之前发生；赋值晚一点确保"本轮"边界干净，只覆盖 turn 循环产生的 assistant/tool 消息。
- `buildRunSummary` 签名改为 `buildRunSummary(history []Message, startIdx int, toolCallCount int, zh bool)`，遍历区间从 `len(history)-1` 到 `startIdx`（含），不再向前穿透。
- 两个调用点同步改：
  - `loop.go:660`（Blocked 分支）：`buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh)`
  - `loop.go:753`（正常结束）：同上。
- 本轮无新增非空 assistant Content → 落入函数末尾原有诊断串兜底（`"（本轮未生成回复文本，已执行 N 次工具调用）"` / 英文对称文案），不再回吐旧旁白。

**不变：** `buildRunSummary` 的三级回退（Content → ReasoningContent → 诊断串）、`isSubstantiveSummary`、`stripDSMLTokens` 全部保留。

### 第 2 节 · `stream_delta` 仅发一次（方案 A）

- `runLoop`（`sub_agent.go:143`）在 `consecutiveIntermediate` 旁新增局部变量 `streamed := false`。
- `sub_agent.go:286-291` 的发射块用 `if !streamed` 守卫，发射后置 `streamed = true`：

```go
} else if resp.Message.Content != "" {
    preview := firstLine(resp.Message.Content, 60)
    r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, preview)})
    if !streamed {
        r.onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: resp.Message.Content})
        streamed = true
    }
}
```

- 首条"有正文、无工具调用"的内容即子 agent 的结构化作答（它已停止工具调用）；后续 `consecutiveIntermediate` 轮的重复文本不再重发。
- run 末尾主引擎 `Summary` 仍会接管最终展示（`finishStreaming` 在 `Summary != ""` 时清空 `m.streaming`，`model.go:1578-1582`），对"首条非最终答案"的情形自动纠偏。

### 第 3 节 · 测试（TDD，先写测试）

- **`buildRunSummary` 表驱动测试**（扩 `engine/loop_summary_test.go`）：
  - 既有用例（`loop_summary_test.go:69`）因签名变更需同步加 `startIdx` 入参 —— 既有 case 传 `0` 即可保持原语义（从 history 头开始），确保旧断言不回归。
  - case 1（新）：history = [旧轮 assistant 旁白（带正文）, 本轮 assistant（tool_calls，Content 被剥空）, tool 结果]，`startIdx` 指向本轮起点 → 期望诊断串，**不**返回旧旁白。
  - case 2（新）：history = [旧轮旁白, 本轮 assistant（有正文）]，`startIdx` 指向本轮 → 期望本轮正文。
  - case 3（新）：本轮完全无 assistant 消息（仅 tool）→ 期望诊断串。
- **`sub_agent` stream_delta 测试**（新增 `engine/sub_agent_stream_test.go`）：
  - 注入 mock `onProgress` 计数 `stream_delta` 事件；构造子 agent 连续 2 轮产正文、无工具调用的场景（`NoNudge` 关闭，`consecutiveIntermediate < 3`）→ 期望 `stream_delta` 仅触发 1 次，第 2 轮不再发。

### 第 4 节 · 不动的地方（YAGNI）

- 不改 `isIntermediateText` 的判定规则。
- 不改主 agent 流式（`reasoning_delta` 等）。
- 不动 loop guard / max_turns 阈值。
- 不加停滞恢复（注入重置提示、清理旧旁白等）—— 按用户选定的最小范围。
- 不引入新的 tea 消息类型（方案 C 否决）。

## 验证

- `make test` 全绿（含新增测试）。
- `make build` 成功。
- 手工回归：复现会话场景，确认旧旁白不再被回吐、子 agent 流式无重复空行。
