# 通用 Todo List 步骤展示（替代 TDD 专用红绿展示）

日期：2026-08-13
状态：已批准（用户确认布局 B + 机制 A）

## 背景与问题

当前 `test-driven-development` skill 激活时，引擎通过硬编码的 `inferTDDPhase`
（`engine/turn.go`）分析工具调用（write/edit/bash）推断 TDD 的 5 个阶段
（red/red_verify/green/green_verify/refactor），UI 在输入框上方渲染卡片式红绿状态
（emoji 🔴🟢、spinner、✅，`ui/model.go` 的 `tddPhaseMeta` + `renderTDDStatus`）。

问题：
1. **不通用**：推断逻辑 + 展示逻辑都硬编码绑定 TDD，其他需要步骤展示的 skill 无法复用。
2. **展示非纯文字**：emoji/卡片样式不适合作为通用模式。

## 目标

1. 提供一个**与 skill 无关**的步骤进度通道：任何 skill 都能用它展示步骤列表。
2. UI 以**纯文字 todo list**（`[ ]` / `[~]` / `[x]`）呈现，无 emoji。
3. TDD skill 迁移到新通道，硬编码推断逻辑退役。

## 方案决策

- **布局**：保留现有 overlay 辅助区（输入框上方，与 roundtable 成员状态并排时各占半宽），
  不做左右分栏大改（用户选择 B）。
- **机制**：新增通用 `todo_write` 工具，由 LLM 主动报告步骤快照（用户选择 A）。
  - 对齐 Claude Code 生态的 TodoWrite。
  - **全量快照式**：LLM 每次重发完整列表，UI 全量替换。无增量状态机，最简单、不易漂移。
  - 工具始终暴露（不依赖 skill 状态），与 `activate_skill` 同策略。

## 数据流

```
LLM (任意 skill)
   │  todo_write({todos:[...]})          ← 每次传全量快照
   ▼
engine 拦截 (turn.go processTodoWriteCalls，仿 activate_skill，不进 tools registry)
   │  ProgressEvent{Type:"todo_update", Todos:[...]}
   ▼
UI 渲染 (输入框上方辅助区 renderTodoList，纯文字)
```

## 数据模型（engine/types.go）

```go
// TodoItem 是通用步骤跟踪项，与具体 skill 无关。
type TodoItem struct {
    Content string // 步骤描述（纯文字）
    Status  string // "pending" | "in_progress" | "completed"
}

type ProgressEvent struct {
    // ... 现有字段
    Todos []TodoItem // Type == "todo_update" 时的完整快照
}
```

## 工具定义（engine/agent.go）

- 名称：`todo_write`（常量 `TodoWriteToolName`）
- 参数：
  ```json
  {
    "type": "object",
    "properties": {
      "todos": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "content": {"type": "string"},
            "status": {"type": "string", "enum": ["pending", "in_progress", "completed"]}
          },
          "required": ["content", "status"]
        }
      }
    },
    "required": ["todos"]
  }
  ```
- 通过 `toolSpecsWithHandoff`（turn.go）append，始终对 LLM 可见。

## 引擎处理（engine/turn.go）

1. 新增 `processTodoWriteCalls(calls []ToolCallRequest) []Message`（仿 `processActivateSkillCalls`）：
   - 解析 `{todos:[...]}`，校验：content 非空、status ∈ {pending, in_progress, completed}。
   - 调用 `e.config.OnProgress(ProgressEvent{Type: "todo_update", Todos: parsed})`。
   - 为每个调用生成 tool 响应消息（如 `"✓ 已更新 N 项 todo"`）加入 history，
     满足 DeepSeek API 每 tool_call 必须有响应的约束。
2. 在 turn 流程中拦截：`todo_write` 不进 `regularCalls`（仿 `activate_skill` 分支，
   避免 `Execute` 产生 "tool not found" 重复响应）。tool 响应消息追加顺序：
   `assistant(tool_calls) → tool(responses)`。
3. **退役 TDD 推断**：
   - 删除 `inferTDDPhase`（turn.go）及调用点（L689）。
   - 删除辅助函数 `isTestFile`、`isTestCommand`、`extractCmd`（无其他使用者）。
   - 删除 `engine/loop.go` 的 `tddPhase` / `tddPhaseDetail` 字段及两处重置
     （L276-277、L1343-1344）。
   - `extractPathFromArgs` 保留（loop guard 等仍在使用）。
4. `summarizeArgs`（turn.go）新增 `todo_write` 分支：显示 `"update todos: N 项"`，
   避免 tool_start 事件显示空白。

## UI 渲染（ui/model.go）

**删除**：
- `TDDStage` 结构（L80-85）、`tddStages` 字段（L159）
- `tddPhaseMeta`（L2436）、`renderTDDStatus`（L2451）
- `case "tdd_phase":` 事件分支（L617-643）
- `m.tddStages = nil` 清理（L442）

**新增**：
- `todoItems []engine.TodoItem` 字段（复用 engine.TodoItem）
- `case "todo_update":` 全量替换 `m.todoItems`
- Done 时 `m.todoItems = nil` 清理
- `renderTodoList(items []engine.TodoItem, width int) []string`：
  ```
  ▍ Steps                      ← 无 emoji
    [ ]  红灯 - 编写失败的测试
    [~]  红灯验证 - 运行测试确认失败   ← in_progress 项 SpinnerStyle 高亮
    [x]  绿灯 - 编写最小实现
    [x]  绿灯验证 - 运行测试确认通过
    [ ]  重构 - 清理代码
  ```
  标记映射：pending → `[ ]`，in_progress → `[~]`，completed → `[x]`。
- `renderOverlayStatus` 改名/适配：`renderTodoList` + `renderMemberProgress` 并排逻辑不变。

**数据通道**：
- `ProgressMsg`（ui/model.go:171）加 `Todos []engine.TodoItem`
- `ui/runner.go:141` 映射处透传 `event.Todos`

## TDD skill 文件迁移（.claude/skills/test-driven-development/SKILL.md）

在 TDD skill 内容中新增使用说明：指示 LLM 在每个阶段（红灯/绿灯/重构）用
`todo_write` 维护 5 个步骤（红灯-写失败测试 / 红灯验证 / 绿灯-最小实现 /
绿灯验证 / 重构）的进度。TDD 实际行为不变，但走通用通道。
（该文件位于本地 gitignored 的 `.claude/` 目录，不入库。）

## 测试

- 引擎：新增 `processTodoWriteCalls` 单元测试（合法快照→事件+响应消息、
  非法 status→错误响应、空 content→错误响应）。
- UI：新增 `renderTodoList` 渲染测试（三种状态标记、空列表返回 nil）。
- 删除/更新现有 TDD 相关测试（若有引用 `inferTDDPhase`/`tddPhase`）。

## 范围检查

不涉及：
- 真正的左右分栏布局（用户选择 B，不做）
- roundtable 成员状态展示（保留，仅共享 overlay 布局）
- 其他 skill 文件（除 TDD 外的 skill 如需步骤展示，由用户后续自行在其
  SKILL.md 中指示 LLM 使用 `todo_write`，无需改代码）
