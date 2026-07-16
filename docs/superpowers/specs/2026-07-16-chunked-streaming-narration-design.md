# 分块流式输出：展示 AI 中间意图

## 问题

当前输出永远分成 3 块：`[?] Search`、`[>_] Execute / [~] Changes`、最终结论。AI 的中间叙述完全不可见。

### 根因

1. `engine/turn.go:122-124`：`chunk.Delta` 只写入 contentBuilder，从未作为 ProgressEvent 发给 UI
2. `engine/turn.go:194-196`：`isIntermediateText()` 将叙述文本设为空
3. `ui/model.go:1846-1862`：`renderToolTree()` 按类型全局分组，丢失时间顺序

## 设计方案

### 核心思路

引擎发射 `content_delta` 事件，UI 按轮次分块快照。`tool_start` 是轮次边界。

### 改动清单

1. `engine/turn.go` L122：发射 `ProgressEvent{Type:"content_delta", Detail: chunk.Delta}`
2. `engine/types.go` L37：Type 注释追加 `"content_delta"`
3. `ui/model.go`：新增 `narration` 字段；ProgressMsg 新增 content_delta case；tool_start 时调用 finalizeTurnBlocks()；finishStreaming 调整；renderBody 显示 narration；renderMessage 新增 narration role
4. `ui/styles.go`：新增 NarrationStyle

### finalizeTurnBlocks 机制

tool_start 到达时：
1. m.narration -> DisplayMessage{Role:"narration"}，清空
2. m.toolTree -> DisplayMessage{Role:"toolsummary"}，清空
3. 添加新工具到 m.toolTree

### 边界情况

- 无文本只工具：跳过 narration 固化
- 无工具只文本（结论）：finishStreaming 处理
- 单轮多工具：每次 tool_start 前固化
- sub-agent stream_delta：不受影响

### 不改动

isIntermediateText、StalledNarrationHook、buildRunSummary、stream_delta
