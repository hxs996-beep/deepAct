# 子 agent 工具化收敛 + ask_user 回界面 — 设计规格

> **日期：** 2026-09-12
> **状态：** 已批准（方案 A+B+A：LLM 侧工具化 + 收敛全套 + ask_user 轻量冒泡）

## 目标

把 `handoff_to_agent` 从"引擎特判拦截"收敛为注册在 `tools.Registry` 的**标准工具**，与 read/grep/edit 等工具同等对待，从而：

1. **消除硬编码**：`maxSubAgentDepth = 2` 常量、agent schema 枚举 `["sub"]`、以及主 agent / 子 agent 两套 handoff 实现（`executeHandoff` 与 `executeSubHandoff`）的重复。
2. **让子 agent 与主 agent 统一**：同一工具实现、同一委托核心、同一执行通道，未来无论 LLM 决定用子 agent 还是引擎编程式调用，组合都更容易。
3. **打通子 agent 询问回界面**：子 agent 可见并调用 `ask_user`，问题结构化冒泡到主引擎，复用现有 awaiting_user / Options 弹窗通道（轻量：重新委派继续）。

**范围**：只改 LLM 侧。`/debate`、`/collab` 保持编程式调用（`agent.Run` / `RunWithPrompt`）不动，后续迁移。

## 背景

### 现状

- 主 agent 侧：`toolSpecsWithHandoff()`（`engine/turn.go:694`）把 `handoff_to_agent` 作为 ModelTool 追加，但执行是引擎特判——`executeTurn` 单独拆出 `handoffCalls`（`turn.go:485`），走 `executeHandoffsParallel` → `executeHandoff` → `agent.Run`。
- 子 agent 侧：`executeSubHandoff`（`engine/sub_agent.go:725`）特判嵌套委派，深度硬编码 `maxSubAgentDepth = 2`（`agent.go:159`），`filterTools` 强制构造 handoffToolSpec（`sub_agent.go:703`）。
- 工具 schema：agent 枚举只有 `["sub"]`（`agent.go:213`）。
- 询问：子 agent 的 `filterTools` 不含 ask_user；子 agent 循环无 ask_user 拦截。子 agent 无法问用户。

### 参考实现（deepseek-harness dsh）

- `dsh-tool-subagent`：委派做成标准工具，一个实例一个工具名，provider 声明 capabilities，`maxDepth` 默认 3（0 禁止委派），深度检查在调用时执行并报错拒绝。
- `dsh-tool-ask-user`：`ask_user_question` 工具可用，但 user-questions seam 做身份校验——live child agent 调用返回 `DELEGATED_CALLER`，必须把未解决问题写进最终结果。
- 本设计采纳"统一工具 + 调用时深度校验"，但 ask_user 采用更直接的方案：**子 agent 允许调用 ask_user 并结构化冒泡**（dsh 是拒绝 child 直连，本设计是打通 child 直连的轻量版）。

## 架构

### 新文件

- `engine/handoff.go`：共享委托核心 `runHandoff(...)`——解析参数、取 agent、构造 Handoff、注入主 agent 上下文、Run、usage 累积、`formatHandoffResult` 格式化。`executeHandoff`（Engine）与 `executeSubHandoff`（SubAgentRunner）都改为薄包装调用它。
- `tools/subagent.go`：`SubAgentTool` 实现 `tools.Tool`（`Spec()` / `Run()`），构造注入两个 backend 与 agentList 提供者。

### 双 backend 分发

`SubAgentTool.Run` 按 `tc.Depth` 分发：

- `Depth == 0` → **Engine backend**（主 agent 语义：state 注入 + `isChinese` 语言）。Engine 实现 `RunSubAgent(ctx, params, depth, userLang) (ToolResult, error)`。
- `Depth > 0` → **SubAgentRunner backend**（嵌套语义：depth+1 + userLang）。SubAgentRunner 实现同一接口。

`SubAgentTool` 持有两个 `RunSubAgent` 函数字段（由 cmd/run.go 注入闭包），避免 tools 包 import engine 之外的新依赖。

### 组装（cmd/run.go）

`NewEngine` 之后：

```go
registry.Register(tools.NewSubAgentTool(engineInst, runner, agentList))
```

`agentList` 是 `func() []string`，闭包捕获 `agentReg.AgentSpecs()`，供 Spec 动态生成 enum。

无循环依赖：cmd→tools、cmd→engine、tools→engine（已存在），engine 不 import tools。

## 数据流透传

三处类型扩展 + adapter 双向透传：

| 类型 | 新增字段 | 用途 |
|---|---|---|
| `engine.ToolExecContext` | `Ctx context.Context`、`Depth int`、`UserLang string` | 取消传播、委派深度、会话语言 |
| `tools.ToolContext` | `Ctx context.Context`、`Depth int`、`UserLang string` | 同上（tools 侧） |
| `tools.ToolResultEnvelope` | `FinishReason string`、`Questions []string` | 结构化 reason 与问题冒泡 |

`tools/adapter.go` 双向透传：`Execute` 入参 `ToolCallRequest` → `ToolCall` 时带上 Ctx/Depth/UserLang；结果 `ToolResultEnvelope` → `ToolResult` 时带上 FinishReason/Questions。

## 动态化

1. **agent 枚举**：`SubAgentTool.Spec()` 从 `agentList` 动态生成 `enum: ["sub", ...]`，不再硬编码 `["sub"]`。Spec 语言放弃按会话本地化（注册为通用工具的固有代价，中英混合描述）。
2. **maxDepth**：`SubAgentRunner` 加 `MaxDepth` 字段（默认 2，`SetMaxDepth` 可配置），替换 `maxSubAgentDepth` 常量。工具 Run 校验 `tc.Depth+1 > MaxDepth` 返回错误 result；runLoop 开头校验保留（防编程式绕过）。
3. **filterTools**：从"强制构造 handoffToolSpec"改为"从 all specs 强制保留名为 handoff_to_agent 的工具"（子 agent 工具列表已含注册工具）。

## ask_user 回界面（轻量冒泡 + 重新委派）

### 4a. 子 agent 可见 ask_user

`filterTools` 在保留 handoff_to_agent 的同时，追加 `askUserToolSpec(userLang)`——子 agent 的 LLM 能看到并调用 ask_user。

### 4b. runLoop 拦截

处理 tool calls 时特判 `call.Name == AskUserToolName`（与主引擎 `processAskUserCalls` 同模式）：

- 校验 question/options → 写入 tool 响应消息（满足 API 契约，`tool_call_id` 必须有响应）。
- 把问题存入 `HandoffResult.Questions`。
- **立即结束 run**，`FinishReason = HandoffReasonAwaitingUser`（新增常量 `"awaiting_user"`）。
- 不执行、不进入普通工具循环、不触发 stalled_narration 等守卫。

### 4c. 冒泡

- `HandoffResult` 加 `Questions []string`。
- `ToolResult` 加 `Questions []string`（adapter 透传）。
- `Engine.RunSubAgent` 在结果含 Questions 时写入 `e.pendingAskUser = &AskUserRequest{Question, Options}`。
- **嵌套冒泡**：当子 agent（任意深度）执行 handoff 后收到含 Questions 的 ToolResult 时，它不自行处理——runLoop 检测到该工具结果含 Questions，立即终止自身 run 并向上冒泡（自身 `HandoffResult.Questions` 继承该问题）。这样多层嵌套下问题总能源源不断冒到主引擎。`ask_user` 只允许**叶子**子 agent 主动发起；中间层只透传。

### 4d. 复用现有 UI 通道

`executeTurn` 已有（`turn.go:579`）：

```go
if e.pendingAskUser != nil {
    result.Done = true
    // 有 options → Options 弹窗；无 options → Blocked+awaiting_user
}
```

子 agent 的问题自动走这条现成路径，零新 UI 代码。

### 4e. 回答后继续（重新委派）

用户回答 → 下轮 Run → 主 agent 看到回答 + 子 agent 的 Summary（`formatHandoffResult` 带 "Blocked/受阻" 上下文）→ **重新委派**（把回答并入新 context）或自行继续。

`isHandoffFollowUpReason` 把 `awaiting_user` 视为需要父 agent 继续 → 自动 pinned follow-up 提示主 agent。

## turn.go 与子 agent 循环

### turn.go

- 保留 `handoffCalls` 单独拆出（为了 agent_start/agent_done 事件、follow-up、progress 计数），但**执行改走 `e.tools.Execute(execCtx, handoffCalls)`**（注册工具）。
- 删除 `executeHandoffsParallel` / `executeHandoff` / `processHandoffResults` 里的委托逻辑，只剩：结果写历史、follow-up 判定（`isHandoffFollowUpReason`）、cancelled 文案、事件。
- progress switch 加 `case "handoff_to_agent": MadeProgress = true`（保持现在"handoff 算进展"的语义）。

### sub_agent.go runLoop

- 删除 `if call.Name == HandoffToolName && input.Depth < maxSubAgentDepth { executeSubHandoff }` 特判，handoff 走普通 `r.tools.Execute`。
- 新增 ask_user 拦截分支（见 4b）。

## 错误处理

- backend 不可用（agents==nil）→ `Status:"error", Digest:"no agent registry configured"`（保持现有语义）。
- maxDepth 超限 → 工具返回错误 result（"Max nesting depth reached"），与现在一致。
- `awaiting_user`：非错误，是正常"需要用户输入"信号；`isHandoffFollowUpReason` 返回 true。
- 子 agent 失败 → `FinishReason` 结构化传递，follow-up pinned 逻辑不变。

## 测试计划

- `engine/handoff.go`：runHandoff 单测（mock registry：正常完成、agent 不存在、cancelled）。
- `tools/subagent.go`：Spec 动态 enum（agentList 变化反映在 enum）、深度分发（depth 0 → main、depth>0 → nested）、maxDepth 拒绝。
- 子 agent ask_user：runLoop 拦截 → Questions 冒泡 → FinishReason=awaiting_user；Engine.RunSubAgent 写 pendingAskUser；executeTurn 停在该问题。
- 现有测试适配：`stubToolExecutor.Specs()` 返回 nil 的测试场景无 handoff 调用，不受影响。

## 明确不做

- `/debate`、`/collab` 保持编程式调用（`agent.Run` / `RunWithPrompt` 保留），后续迁移。
- **continuable 持久子 agent**：本次不做。但 `HandoffResult.Questions` 冒泡通道已为它预留——未来只需把"重新委派"换成"恢复挂起子 agent"（保存 history/model fork/compressor，用户回答后从断点恢复）。
- 不引入 dsh 的 `DELEGATED_CALLER` 式身份拒绝（本设计允许子 agent 直连 ask_user）。
