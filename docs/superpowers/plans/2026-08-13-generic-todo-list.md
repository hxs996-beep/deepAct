# 通用 Todo List 步骤展示 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 新增通用 `todo_write` 工具 + 纯文字 todo list 渲染，替代 TDD 专用红绿展示；任何 skill 都能用同一展示通道报告步骤进度。

**架构：** LLM 通过 `todo_write` 工具主动报告步骤快照（全量式）；引擎拦截该调用（仿 `activate_skill` 模式，不进 tools registry），发 `todo_update` ProgressEvent；UI 在输入框上方 overlay 区渲染纯文字 `[ ]`/`[~]`/`[x]` 列表。TDD 硬编码推断逻辑（`inferTDDPhase`）整体退役，TDD skill 文件改用 `todo_write` 维护步骤。

**技术栈：** Go（engine + bubbletea TUI）。设计文档：`docs/superpowers/specs/2026-08-13-generic-todo-list-design.md`（已 commit `82c7bf3`）。

---

**注意：** 工作区存在大量未提交的用户改动（git status 显示 cmd/exec.go、engine/loop.go 等 15 个文件已修改）。所有 commit 步骤**只 add 本任务涉及的文件**，绝不 `git add -A`。

## 文件结构

| 文件 | 职责 |
|------|------|
| `engine/types.go` | 新增 `TodoItem` 类型；`ProgressEvent` 加 `Todos` 字段 |
| `engine/agent.go` | 新增 `TodoWriteToolName` 常量 + `todoWriteToolSpec()` 工具定义 |
| `engine/turn.go` | `toolSpecsWithHandoff` 追加 spec；新增 `processTodoWriteCalls`；拦截 todo_write 不进 regularCalls；`summarizeArgs` 分支；**删除** `inferTDDPhase`/`isTestFile`/`extractCmd`/`isTestCommand` 及调用点 |
| `engine/loop.go` | **删除** `tddPhase`/`tddPhaseDetail` 字段及两处重置 |
| `ui/model.go` | **删除** TDD 结构/渲染/事件分支；新增 `todoItems` 字段、`todo_update` 分支、`renderTodoList` |
| `ui/runner.go` | `ProgressMsg` 映射透传 `Todos` |
| `.claude/skills/test-driven-development/SKILL.md` | 新增"进度展示"章节，指示 LLM 用 `todo_write` 维护步骤 |
| `engine/turn_todo_write_test.go`（新建） | `processTodoWriteCalls` 单元测试 |
| `ui/todo_render_test.go`（新建） | `renderTodoList` 渲染测试 |

---

### 任务 1：engine/types.go — TodoItem 类型 + ProgressEvent.Todos 字段

**文件：**
- 修改：`engine/types.go:36-43`

- [ ] **步骤 1：修改类型定义**

将 `ProgressEvent` 结构（L36-43）替换为：

```go
// TodoItem is a generic step-tracking item reported by the todo_write tool.
// It is skill-agnostic: any skill can instruct the model to report its
// step-by-step progress through this channel, and the UI renders it as a
// plain-text todo list (no skill-specific theming).
type TodoItem struct {
	Content string `json:"content"` // step description (plain text)
	Status  string `json:"status"`  // "pending" | "in_progress" | "completed"
}

type ProgressEvent struct {
	Type       string // "tool_start" | "tool_done" | "thinking" | "content_delta" | "reasoning_delta" | "agent_start" | "agent_done" | "usage" | "todo_update"
	Name       string
	Detail     string // brief digest for live display
	FullDetail string // full content (e.g., diff) for final rendering
	Usage      *ModelUsage
	ModelName  string // which model was used for this API call
	// Todos carries the full todo-list snapshot for Type == "todo_update".
	Todos []TodoItem
}
```

- [ ] **步骤 2：验证编译**

运行：`go build ./engine/`
预期：成功，无错误

- [ ] **步骤 3：Commit**

```bash
git add engine/types.go
git commit -m "feat(engine): add TodoItem type and Todos field to ProgressEvent"
```

---

### 任务 2：engine/agent.go — TodoWriteToolName 常量 + 工具 spec

**文件：**
- 修改：`engine/agent.go:18-21`（常量区）、`engine/agent.go:140` 之后（`handoffToolSpec` 后面新增函数）

- [ ] **步骤 1：添加常量**

在 `engine/agent.go` 常量区（L18-21）追加：

```go
	TodoWriteToolName     = "todo_write"
```

- [ ] **步骤 2：添加工具 spec 函数**

在 `handoffToolSpec` 函数之后新增：

```go
// todoWriteToolSpec returns the tool definition for tracking step progress.
// The model calls this to report the current state of its step-by-step todo
// list as a FULL snapshot (not a diff). The UI renders it as a plain-text
// todo list above the input. Skill-agnostic: any skill can use it.
func todoWriteToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        TodoWriteToolName,
			Description: "Report the current state of your step-by-step todo list. Call this whenever you start, complete, or change the status of a step. Pass the FULL list of steps each time (snapshot, not diff). The UI displays it as a plain-text todo list. Status must be one of: pending, in_progress, completed.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"todos": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"content": {
									"type": "string",
									"description": "Step description (plain text)"
								},
								"status": {
									"type": "string",
									"enum": ["pending", "in_progress", "completed"]
								}
							},
							"required": ["content", "status"]
						}
					}
				},
				"required": ["todos"]
			}`),
		},
	}
}
```

- [ ] **步骤 3：验证编译**

运行：`go build ./engine/`
预期：成功，无错误

- [ ] **步骤 4：Commit**

```bash
git add engine/agent.go
git commit -m "feat(engine): add todo_write tool spec and TodoWriteToolName constant"
```

---

### 任务 3：engine/turn.go — processTodoWriteCalls 拦截（TDD：先写测试）

**文件：**
- 修改：`engine/turn.go:746-753`（`toolSpecsWithHandoff`）、`engine/turn.go:585-593`（调用+追加 history）、`engine/turn.go:600-610`（calls 分离）、`engine/turn.go:1073-1097`（summarizeArgs 分支）、`engine/turn.go:1768` 之后（新增处理函数）
- 测试：`engine/turn_todo_write_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/turn_todo_write_test.go`：

```go
package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessTodoWriteCalls_Valid verifies a valid todo_write call forwards
// the full snapshot to OnProgress AND produces a tool response message
// (preventing orphaned tool_call_ids).
func TestProcessTodoWriteCalls_Valid(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_todo", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [
				{"content": "红灯 - 编写失败的测试", "status": "in_progress"},
				{"content": "重构 - 清理代码", "status": "pending"}
			]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d — tool_call %q is orphaned", len(msgs), "call_todo")
	}
	if msgs[0].ToolCallID != "call_todo" || msgs[0].Role != "tool" {
		t.Errorf("response = %+v, want ToolCallID=call_todo Role=tool", msgs[0])
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 progress event, got %d", len(got))
	}
	if got[0].Type != "todo_update" {
		t.Errorf("event Type = %q, want todo_update", got[0].Type)
	}
	if len(got[0].Todos) != 2 {
		t.Fatalf("event Todos len = %d, want 2", len(got[0]).Todos)
	}
	if got[0].Todos[0].Content != "红灯 - 编写失败的测试" || got[0].Todos[0].Status != "in_progress" {
		t.Errorf("Todos[0] = %+v, want content=红灯... status=in_progress", got[0].Todos[0])
	}
}

// TestProcessTodoWriteCalls_InvalidStatus verifies an invalid status produces
// an error response and NO progress event.
func TestProcessTodoWriteCalls_InvalidStatus(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_bad", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [{"content": "step", "status": "almost_done"}]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "invalid todo status") {
		t.Errorf("Content = %q, want invalid todo status error", msgs[0].Content)
	}
	if len(got) != 0 {
		t.Errorf("expected no progress event on invalid status, got %d", len(got))
	}
}

// TestProcessTodoWriteCalls_EmptyContent verifies empty content is rejected.
func TestProcessTodoWriteCalls_EmptyContent(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	calls := []ToolCallRequest{
		{ID: "call_empty", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [{"content": "  ", "status": "pending"}]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "non-empty content") {
		t.Errorf("Content = %q, want non-empty content error", msgs[0].Content)
	}
}

// TestProcessTodoWriteCalls_BadJSON verifies malformed input gets an error
// response and no event.
func TestProcessTodoWriteCalls_BadJSON(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_badjson", Name: TodoWriteToolName, Input: json.RawMessage(`{invalid}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "invalid todo_write arguments") {
		t.Errorf("Content = %q, want invalid arguments error", msgs[0].Content)
	}
	if len(got) != 0 {
		t.Errorf("expected no progress event on bad JSON, got %d", len(got))
	}
}

// TestProcessTodoWriteCalls_Mixed verifies only todo_write calls get responses
// from this function (regular calls are handled elsewhere).
func TestProcessTodoWriteCalls_Mixed(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	calls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"x.go"}`)},
		{ID: "call_todo", Name: TodoWriteToolName, Input: json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response (todo_write only), got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_todo" {
		t.Errorf("ToolCallID = %q, want call_todo", msgs[0].ToolCallID)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestProcessTodoWriteCalls' -v`
预期：FAIL，编译错误 `undefined: TodoWriteToolName` / `undefined: processTodoWriteCalls`

- [ ] **步骤 3：实现 processTodoWriteCalls**

在 `engine/turn.go` 的 `processActivateSkillCalls` 函数之后（L1768 附近）新增：

```go
// processTodoWriteCalls intercepts todo_write tool calls from the assistant's
// response. Each call carries a FULL snapshot of the step list; the engine
// validates it and forwards it to the UI as a "todo_update" progress event.
// Every call receives a tool response message (satisfying the DeepSeek API
// requirement that every tool_call_id has a matching tool response).
func (e *Engine) processTodoWriteCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != TodoWriteToolName {
			continue
		}
		var params struct {
			Todos []TodoItem `json:"todos"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid todo_write arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		valid := true
		for _, t := range params.Todos {
			if strings.TrimSpace(t.Content) == "" {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Error: todo_write requires non-empty content for each item",
					Timestamp:  time.Now(),
				})
				valid = false
				break
			}
			if t.Status != "pending" && t.Status != "in_progress" && t.Status != "completed" {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("Error: invalid todo status %q (must be pending, in_progress, or completed)", t.Status),
					Timestamp:  time.Now(),
				})
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "todo_update", Todos: params.Todos})
		}
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("✓ 已更新 %d 项 todo", len(params.Todos)),
			Timestamp:  time.Now(),
		})
	}
	return msgs
}
```

- [ ] **步骤 4：接线 toolSpecsWithHandoff**

在 `engine/turn.go` `toolSpecsWithHandoff`（L746-753）中追加：

```go
	specs = append(specs, handoffToolSpec(e.isChinese))
	specs = append(specs, activateSkillToolSpec())
	specs = append(specs, taskCompleteToolSpec(e.isChinese))
	specs = append(specs, todoWriteToolSpec())
	return specs
```

- [ ] **步骤 5：接线调用 + history 追加 + calls 分离**

`engine/turn.go` L585 处，将：

```go
	pendingActivateMsgs := e.processActivateSkillCalls(calls)
```

替换为：

```go
	pendingActivateMsgs := e.processActivateSkillCalls(calls)
	pendingTodoMsgs := e.processTodoWriteCalls(calls)
```

L591-593 追加 history 循环之后，新增：

```go
	for _, msg := range pendingTodoMsgs {
		e.history = append(e.history, msg)
	}
```

L602-609 calls 分离循环中，将：

```go
	} else if call.Name == ActivateSkillToolName {
		continue
	} else {
```

替换为：

```go
	} else if call.Name == ActivateSkillToolName {
		continue
	} else if call.Name == TodoWriteToolName {
		continue
	} else {
```

- [ ] **步骤 6：接线 summarizeArgs**

`engine/turn.go` L1074-1082 的 `switch toolName` 中，在 `case "skill_install", "activate_skill":` 之前新增：

```go
	case "todo_write":
		if todos, ok := m["todos"].([]interface{}); ok {
			return fmt.Sprintf("update todos: %d 项", len(todos))
		}
		return "update todos"
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./engine/ -run 'TestProcessTodoWriteCalls' -v`
预期：PASS（5 个子测试）

- [ ] **步骤 8：Commit**

```bash
git add engine/turn.go engine/turn_todo_write_test.go
git commit -m "feat(engine): intercept todo_write tool calls and forward todo_update events"
```

---

### 任务 4：退役 TDD 硬编码推断

**文件：**
- 修改：`engine/turn.go:687-690`（调用点）、`engine/turn.go:1482-1681`（isTestFile/extractCmd/isTestCommand/inferTDDPhase 整块删除）、`engine/loop.go:86-89`、`engine/loop.go:276-277`、`engine/loop.go:1342-1344`

- [ ] **步骤 1：删除 inferTDDPhase 调用点**

`engine/turn.go` L687-690，删除：

```go
		// Infer TDD phase from tool calls when TDD skill is active
		if e.state != nil && e.state.ActiveSkillName == "test-driven-development" {
			e.inferTDDPhase(allCalls, allResults)
		}
```

- [ ] **步骤 2：删除辅助函数 + inferTDDPhase 整块**

`engine/turn.go` L1482-1681：删除 `addToWorkingSet` 函数之后到 `processActivateSkillCalls` 之前的所有内容，即 `isTestFile`（L1486-1508）、`extractCmd`（L1510-1523）、`isTestCommand`（L1525-1561）、`inferTDDPhase`（L1563-1681）整块。保留空行分隔。

- [ ] **步骤 3：删除 loop.go 字段与重置**

`engine/loop.go` L86-89 删除：

```go
	// tddPhase tracks the current TDD phase when test-driven-development skill is active.
	// Phases: "" (inactive), "red", "red_verify", "green", "green_verify", "refactor".
	tddPhase       string
	tddPhaseDetail string
```

L276-277 删除：

```go
	e.tddPhase = ""
	e.tddPhaseDetail = ""
```

L1342-1344 删除：

```go
	// Reset TDD-specific phase tracking
	e.tddPhase = ""
	e.tddPhaseDetail = ""
```

- [ ] **步骤 4：验证编译 + 全量 engine 测试**

运行：`go build ./engine/ && go test ./engine/`
预期：编译成功，全部测试 PASS（确认无测试引用已删符号；先前 grep 已确认无 TDD 相关测试文件）

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/loop.go
git commit -m "refactor(engine): retire TDD hardcoded phase inference in favor of todo_write"
```

---

### 任务 5：UI — 纯文字 todo list 渲染（TDD：先写测试）

**文件：**
- 修改：`ui/model.go:80-85`（删 TDDStage）、`ui/model.go:158-159`（字段）、`ui/model.go:171-180`（ProgressMsg）、`ui/model.go:442`（Done 清理）、`ui/model.go:617-644`（删 tdd_phase 分支 + 加 todo_update）、`ui/model.go:1681-1685`（renderBody 调用）、`ui/model.go:2436-2512`（删 tddPhaseMeta + renderTDDStatus，新增 renderTodoList）、`ui/model.go:2514-2571`（renderOverlayStatus 适配）、`ui/runner.go:141`
- 测试：`ui/todo_render_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

创建 `ui/todo_render_test.go`：

```go
package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestRenderTodoList_ThreeStates verifies the three plain-text status markers:
// [ ] pending, [~] in_progress, [x] completed. No emoji.
func TestRenderTodoList_ThreeStates(t *testing.T) {
	items := []engine.TodoItem{
		{Content: "红灯 - 编写失败的测试", Status: "completed"},
		{Content: "绿灯 - 编写最小实现", Status: "in_progress"},
		{Content: "重构 - 清理代码", Status: "pending"},
	}
	lines := renderTodoList(items, 80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty rendered lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[x]") {
		t.Errorf("missing [x] marker for completed item:\n%s", joined)
	}
	if !strings.Contains(joined, "[~]") {
		t.Errorf("missing [~] marker for in_progress item:\n%s", joined)
	}
	if !strings.Contains(joined, "[ ]") {
		t.Errorf("missing [ ] marker for pending item:\n%s", joined)
	}
	if strings.Contains(joined, "🔴") || strings.Contains(joined, "🟢") || strings.Contains(joined, "✅") {
		t.Errorf("todo list must not contain emoji:\n%s", joined)
	}
	if !strings.Contains(joined, "红灯 - 编写失败的测试") {
		t.Errorf("missing step content:\n%s", joined)
	}
}

// TestRenderTodoList_Empty verifies an empty list renders nothing.
func TestRenderTodoList_Empty(t *testing.T) {
	if got := renderTodoList(nil, 80); got != nil {
		t.Errorf("expected nil for empty todo list, got %v", got)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run 'TestRenderTodoList' -v`
预期：FAIL，编译错误 `undefined: renderTodoList`

- [ ] **步骤 3：改字段 + ProgressMsg + 事件分支 + Done 清理**

`ui/model.go`：
1. 删除 `TDDStage` 结构（L80-85）。
2. L158-159 `// TDD (test-driven-development) phase tracking` + `tddStages []TDDStage` 替换为：

```go
	// Generic step-progress tracking (driven by todo_write from any skill)
	todoItems []engine.TodoItem
```

3. `ProgressMsg`（L171-180）追加字段：

```go
	Todos []engine.TodoItem
```

4. Done 清理（L442）`m.tddStages = nil` 替换为：

```go
		m.todoItems = nil // todo list done, clear items
```

5. L617-643 `case "tdd_phase":` 整块替换为：

```go
		case "todo_update":
			// Full snapshot replacement from engine todo_write interception
			m.todoItems = msg.Todos
```

- [ ] **步骤 4：新增 renderTodoList + 删旧渲染**

删除 `tddPhaseMeta`（L2436-2447）和 `renderTDDStatus`（L2451-2512），替换为：

```go
// renderTodoList renders the generic step-progress todo list above the input.
// Plain-text markers: [ ] pending, [~] in_progress, [x] completed.
// No emoji, no skill-specific theming — any skill drives it via todo_write.
func renderTodoList(items []engine.TodoItem, width int) []string {
	if len(items) == 0 {
		return nil
	}
	var content []string
	content = append(content, DimStyle.Render("▍")+" [::] "+DimStyle.Render("Steps"))
	content = append(content, "")
	for _, it := range items {
		switch it.Status {
		case "in_progress":
			content = append(content, fmt.Sprintf("  [~]  %s", SpinnerStyle.Render(it.Content)))
		case "completed":
			content = append(content, fmt.Sprintf("  [x]  %s", SpinnerDoneStyle.Render(it.Content)))
		default:
			content = append(content, fmt.Sprintf("  [ ]  %s", DimStyle.Render(it.Content)))
		}
	}
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	rawLines := strings.Split(rendered, "\n")
	var result []string
	for _, l := range rawLines {
		result = append(result, wrapLineAnsi(l, width)...)
	}
	return result
}
```

- [ ] **步骤 5：适配 renderOverlayStatus + renderBody**

`renderOverlayStatus`（L2517）签名 `tddStages []TDDStage` → `todoItems []engine.TodoItem`；函数体内 `tddActive := len(tddStages) > 0` → `tddActive := len(todoItems) > 0`；`renderTDDStatus(tddStages, halfWidth)` → `renderTodoList(todoItems, halfWidth)`；`renderTDDStatus(tddStages, width)` → `renderTodoList(todoItems, width)`。

`renderBody`（L1681-1685）`len(m.tddStages) > 0` → `len(m.todoItems) > 0`，`renderOverlayStatus(m.tddStages, ...)` → `renderOverlayStatus(m.todoItems, ...)`。

- [ ] **步骤 6：runner 透传**

`ui/runner.go` L141，`ProgressMsg{...}` 追加字段：

```go
			msg := ProgressMsg{Type: event.Type, Name: event.Name, Detail: event.Detail, FullDetail: event.FullDetail, Todos: event.Todos}
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./ui/ -run 'TestRenderTodoList' -v && go build ./...`
预期：PASS（2 个子测试），全仓编译成功

- [ ] **步骤 8：Commit**

```bash
git add ui/model.go ui/runner.go ui/todo_render_test.go
git commit -m "feat(ui): render generic plain-text todo list replacing TDD red/green cards"
```

---

### 任务 6：TDD skill 文件迁移

**文件：**
- 修改：`.claude/skills/test-driven-development/SKILL.md`（gitignored，不入库；仅本地改动）

- [ ] **步骤 1：新增进度展示章节**

在 `.claude/skills/test-driven-development/SKILL.md` 的"红-绿-重构"章节之后插入：

```markdown
## 进度展示（todo_write）

本 skill 激活期间，用 `todo_write` 工具在 UI 中维护步骤进度（纯文字 todo list，无 emoji）。
每个阶段开始/结束时调用一次，每次传入**完整列表**（快照式，不是增量）：

| 步骤 | status |
|------|--------|
| 红灯 - 编写失败的测试 | pending → in_progress → completed |
| 红灯验证 - 运行测试确认失败 | pending → in_progress → completed |
| 绿灯 - 编写最小实现 | pending → in_progress → completed |
| 绿灯验证 - 运行测试确认通过 | pending → in_progress → completed |
| 重构 - 清理代码 | pending → in_progress → completed |

示例调用：

```
todo_write({"todos":[
  {"content":"红灯 - 编写失败的测试","status":"in_progress"},
  {"content":"红灯验证 - 运行测试确认失败","status":"pending"},
  {"content":"绿灯 - 编写最小实现","status":"pending"},
  {"content":"绿灯验证 - 运行测试确认通过","status":"pending"},
  {"content":"重构 - 清理代码","status":"pending"}
]})
```
```

- [ ] **步骤 2：验证 skill 解析**

运行：`go test ./skill/ -v`
预期：PASS（验证 skill 文件格式未被破坏；SKILL.md 改动不涉及解析但确认整体测试仍绿）

---

### 任务 7：全量验证 + 对抗审查（3+ 文件修改，触发验证门）

**文件：** 无代码改动

- [ ] **步骤 1：全量测试**

运行：`go build ./... && go test ./...`
预期：全部编译成功，全部测试 PASS

- [ ] **步骤 2：critic 审查**

由于本次实现修改 3 个及以上文件，在报告完成前调用 `handoff_to_agent`（agent=critic）审查以下内容：
- 原始请求：改造 TDD 红绿展示为通用纯文字 todo list
- 改动文件：engine/types.go、engine/agent.go、engine/turn.go、engine/loop.go、ui/model.go、ui/runner.go
- 方案：todo_write 全量快照工具 + todo_update 事件 + renderTodoList

- [ ] **步骤 3：汇总 commit**

确认各任务 commit 已按步骤完成；如有 critic FAIL，呈现给用户决定。
