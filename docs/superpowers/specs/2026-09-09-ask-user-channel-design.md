# ask_user 统一提问通道

日期：2026-09-09
状态：已批准（方案 C 统一通道 + 方案 A 通用触发时机）

## 背景与问题

1. **"agent 不问用户、自己拿主意"是核心缺陷**：面对不确定时（配置缺失、外部事实、用户偏好、权衡选择……），agent 倾向自己决定而非回头问用户。用户指出根源在于产品缺少"让模型主动提问"的通用通道。
2. **现有 `present_options` 只覆盖一个时机**：分析报告后的方案选择（`engine/agent.go:291`）。它无法覆盖执行过程中的即时提问（配置找不到、需要外部事实、用户偏好）。
3. **场景枚举无泛化能力**：找 bug / 方案选择 / 配置找不到只是三个场景，还有 N 个。按 AGENTS.md 第一性原理，方案应覆盖一类问题（"模型需要用户输入"），而非逐个场景打补丁。

## 目标

1. 提供**统一的提问通道** `ask_user`，替代 `present_options`，覆盖所有"模型需要用户输入"的场景。
2. 模型在**任意时刻**（分析报告后、执行中）需要用户输入时主动调用——由模型自主决定何时提问（AGENTS.md 能力优先原则）。
3. 有候选答案 → 弹窗选择（复用现有 Options 弹窗）；无候选 → 呈现问题，用户自由输入。
4. 与分析报告门控（scope gate / `/confirm N` 的"按报告执行"）分离，互不干扰。

## 方案决策

### 1. 工具定义：`ask_user(question, options?)`

替换 `present_options`（`engine/agent.go:291-319` `presentOptionsToolSpec` → `askUserToolSpec`）：

- `question` 必填：呈现给用户的问题。
- `options` 可选（2-6 个非空字符串）：候选答案。有 → 弹窗方案A/B/C 选择；无 → 仅呈现问题，用户直接输入。
- 描述语义（中英双语）：**模型在任意时刻**需要用户提供信息或做决定时调用——覆盖配置缺失、外部事实、用户偏好、权衡选择等一类场景。只在真实需要用户输入时调用；能自行验证（搜索代码/工具）的信息不得调用。

### 2. 引擎状态

- 新增类型 `AskUserRequest{Question string; Options []string}`（`engine/types.go`）。
- `Engine.pendingConfirmOptions []string`（`engine/loop.go:99`）→ `pendingAskUser *AskUserRequest`。

### 3. 拦截与结束（`engine/turn.go`）

- `processPresentOptionsCalls`（`:1537`）→ `processAskUserCalls`：校验 `question` 非空；`options` 若给出则 2-6 个非空；通过后存 `e.pendingAskUser`。每个调用返回 tool 响应消息（满足 DeepSeek API 每 tool_call_id 有响应）。
- 调用名分支（`:561`）：`PresentOptionsToolName` → `AskUserToolName`。
- 结束 Run（`:645-648`）：`len(e.pendingConfirmOptions) > 0` → `e.pendingAskUser != nil`，`Done=true`、`CompletionSummary=content`。
- **无 options 时**额外返回 `Blocked:true, BlockedBy:"awaiting_user", Questions:[question]`。`question` 取 `pendingAskUser.Question`（结构化字段，比模型叙述文本可靠）。

### 4. 工具注册（`engine/turn.go:700-708`）

`toolSpecsWithHandoff` 中 `presentOptionsToolSpec(e.isChinese)`（`:706`）→ `askUserToolSpec(e.isChinese)`。

### 5. 响应组装（`engine/loop.go`）

- `confirmOptions()`（`:987-1000`）→ `askUserOptions()`：`pendingAskUser` 为空 → 固定两项（`按报告执行` / `输入你的意见`，保留分析门控语义）；非空且有 options → 方案A/B/C + `输入你的意见`；非空且无 options → 返回 `nil`（不挂 Options，问题通过 Blocked 分支呈现，用户直接输入）。
- Done 路径组装（`:975-978`）：`Options: e.askUserOptions()`，`Stage` 用 `StageAct`（ask_user 是通用通道，非分析门控语义）。
- `handleConfirmCommand`（`:1567-1596`）：`len(e.pendingConfirmOptions)` → `e.pendingAskUser != nil`；选项取值 `e.pendingAskUser.Options[n-1]`；`pendingConfirmOptions = nil` → `pendingAskUser = nil`。
- 自由输入路径（`:487-489`）：`len(e.pendingConfirmOptions) > 0` → `e.pendingAskUser != nil`，清空 `pendingAskUser`（用户未走 `/confirm N` 直接输入意见，待决问题作废；模型从 history 的 user 消息上下文识别这是回答）。

### 6. UI（`ui/model.go`）

- **有 options**：非 Blocked 响应的 `Options` 消费（`:1857-1860`）触发弹窗，零改动。
- **无 options**：Blocked + `BlockedBy=="awaiting_user"` 分支（`:1786-1804`）呈现问题，零改动。
- `activeOptions` / `selectedOption` / 键盘处理（`:1223-1259`）与 `/confirm N` 提交逻辑不变。

### 7. exec / sub-agent

- `cmd/exec.go`：零改动。无 options 走 `Blocked` 分支打印问题（`:34-40`）；有 options 走 Done 分支打印 Summary（CI 模式无弹窗，可接受）。
- **sub-agent 不暴露 ask_user**：零改动。子代理工具由 `Handoff.Tools` allowList 经 `filterTools`（`sub_agent.go:738`）过滤，`ask_user` 不在 allowList 中天然不可用。信息缺口由子代理向父 agent 汇报，父 agent 决定是否 `ask_user`。

## 改动点

| 文件 | 操作 |
|---|---|
| `engine/agent.go` | 常量 `PresentOptionsToolName`（`:22`）→ `AskUserToolName`；`presentOptionsToolSpec`（`:291-319`）→ `askUserToolSpec`（question + options 参数） |
| `engine/types.go` | 新增 `AskUserRequest` 类型 |
| `engine/loop.go` | 字段（`:99`）、自由输入（`:487-489`）、`confirmOptions`→`askUserOptions`（`:987-1000`）、组装（`:975-978`）、`handleConfirmCommand`（`:1567-1596`） |
| `engine/turn.go` | 调用名（`:561`）、结束（`:645-648`）、注册（`:706`）、`processPresentOptionsCalls`→`processAskUserCalls`（`:1531-1589`） |
| `engine/present_options_test.go` | → `engine/ask_user_test.go` |
| `engine/present_options_ends_run_test.go` | → `engine/ask_user_ends_run_test.go` |
| `engine/confirm_command_test.go` | `pendingConfirmOptions` → `pendingAskUser` |
| `ui/finish_streaming_test.go` | `TestFinishStreaming_NonBlockedOptionsShowPopup` 改用 ask_user Options |

UI `model.go`、`cmd/exec.go`、`sub_agent.go` 零改动（复用现有分支）。

## 测试

- **ask_user 无 options**：调用 → Run 结束，`Blocked:true`、`BlockedBy:"awaiting_user"`、`Questions:[question]`。
- **ask_user 有 options**：调用 → Run 结束，`Done:true`、`Options` 挂载 方案A/B/C + 输入你的意见。
- **参数校验**：question 空、options 1 个 / 7 个 / 含空串 → 返回 Error tool 响应，不结束 Run。
- **`/confirm N`**：选中方案 → history 注入"用户选择了：label"；越界 N → 注入"无效方案编号"；`pendingAskUser` 清空。
- **自由输入**：`pendingAskUser` 非空 + 用户直接输入 → 清空待决问题，正常走模型循环。
- **回归**：分析门控"按报告执行"路径不变（`pendingAskUser` 为空时的固定两项）；`go test ./engine/ ./ui/ ./cmd/` 全绿。

## 风险与回滚

- **风险**：模型滥用 `ask_user`（本可自查却提问）。缓解：工具描述明确"能自行验证的信息不得调用"；有 options 时弹窗保留"输入你的意见"逃生口。
- **风险**：无 options 的 Blocked 响应导致 `runErrorCount++`（`loop.go:824`）。与现有 stop hook awaiting_user 走同一 Blocked 分支，无新增风险。
- **风险**：UI 无 options 呈现依赖 awaiting_user 分支的 Questions 追加（`model.go:1809-1814`），若模型叙述已作为 narration 显示会去重（`:1792-1804`）——与现有 stop hook 行为一致。
- **回滚**：git revert 本提交即可完整恢复 `present_options` 链路。
