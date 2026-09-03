# 动态方案确认弹出框（present_options 工具）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让分析门控确认弹出框支持动态方案选择——LLM 通过 `present_options` 工具声明多个互斥方案时，弹出框按"方案A/B/C..."展示供用户选择；无方案时保持固定两项（按报告执行 / 输入你的意见）。

**架构：** 新增引擎级拦截工具 `present_options`（仿 `todo_write` 模式：工具规格在 `engine/agent.go`、装配进 `toolSpecsWithHandoff`、在 `turn.go` 被 `processPresentOptionsCalls` 拦截并存入 `e.pendingConfirmOptions`）。`engine/loop.go` 的 `confirmOptions()` 从硬编码改为动态构建（无方案固定两项 / 有方案生成"方案A/B/C..."），`handleConfirmCommand` 根据 `pendingConfirmOptions` 区分模式：选任意方案 → 确定性确认执行并注入所选方案描述。UI 弹出框无需改动（已支持任意数量选项，末项"输入你的意见"自动回到输入框）。

**技术栈：** Go（engine/ 包）、charmbracelet/bubbletea（UI）。

**关键前提（已核实，实现时无需再查）：**
- `engine/agent.go:17-22` 已有工具名常量块（`HandoffToolName`/`ActivateSkillToolName`/`TaskCompleteToolName`/`TodoWriteToolName`/`SubmitResultToolName`）。
- `engine/turn.go:738-745` `toolSpecsWithHandoff()` 已 append `handoff/activate_skill/task_complete/todo_write` 4 个引擎级工具。
- `engine/turn.go:580-581` `processActivateSkillCalls`/`processTodoWriteCalls` 并列调用；`turn.go:599-611` 分流把 `activate_skill`/`todo_write` 从 `regularCalls` 排除。
- `engine/turn.go:1514-1569` `processTodoWriteCalls` 是拦截工具的模板（校验 → tool 响应消息）。
- `engine/loop.go:92` `pendingEditPlan` 字段附近可加新字段；`loop.go:932` 挂载 Options 调 `confirmOptions()`（当前 `loop.go:944-949` 硬编码）；`loop.go:443` 调 `handleConfirmCommand`（当前 `loop.go:1628-1650`，N=1 确认 / N≥2 反馈语义将被替换）。
- UI 无需改：`ui/model.go:1223-1241` 末项 Enter 自动回输入框（`n == total`），多方案末项同样适用。
- 测试桩：`engine/turn_test.go:57` `stubStreamModel`、`:75` `stubToolExecutor`；`engine/confirm_command_test.go` 已有 `parseConfirmCommand` 测试与真实 Engine Run 测试。
- 现有会受影响的测试：`engine/confirm_command_test.go:155-159`（断言末项含"其他"，将改为"意见"）、`:104` `TestHandleConfirmCommand_FeedbackVariant`（N≥2 反馈语义被替换为多方案选择语义）、`ui/confirm_test.go:37`、`ui/finish_streaming_test.go:281`（字面量同步）。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/agent.go` | 新增 `PresentOptionsToolName` 常量 + `presentOptionsToolSpec()` | 修改 |
| `engine/turn.go` | 装配进 `toolSpecsWithHandoff`；分流排除；新增 `processPresentOptionsCalls()` | 修改 |
| `engine/loop.go` | 新增 `pendingConfirmOptions` 字段；`confirmOptions()` 动态化 + `confirmOptionLabel()` helper；`handleConfirmCommand` 模式化语义 + 清除 | 修改 |
| `engine/present_options_test.go` | 任务 1、2、3、4、5 的测试（新建） | 创建 |
| `engine/confirm_command_test.go` | 更新末项断言（"其他"→"意见"）、替换 `TestHandleConfirmCommand_FeedbackVariant` 为多方案选择测试 | 修改 |
| `ui/confirm_test.go` | 固定项字面量同步 | 修改 |
| `ui/finish_streaming_test.go` | 固定项字面量同步 | 修改 |

---

### 任务 1：`present_options` 工具规格（engine/agent.go）

**文件：**
- 修改：`engine/agent.go`（常量块 + 新增 `presentOptionsToolSpec`）
- 测试：`engine/present_options_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

新建 `engine/present_options_test.go`：

```go
package engine

import (
	"encoding/json"
	"testing"
)

func TestPresentOptionsToolSpec(t *testing.T) {
	spec := presentOptionsToolSpec(true)
	if spec.Function.Name != PresentOptionsToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, PresentOptionsToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type     string `json:"type"`
		Required []string `json:"required"`
		Properties struct {
			Options struct {
				Type      string `json:"type"`
				MinItems  int    `json:"minItems"`
				MaxItems  int    `json:"maxItems"`
				MinLength int    `json:"minLength"`
			} `json:"options"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "options" {
		t.Errorf("required = %v, want [options]", params.Required)
	}
	if params.Properties.Options.MinItems != 2 {
		t.Errorf("minItems = %d, want 2", params.Properties.Options.MinItems)
	}
	if params.Properties.Options.MaxItems != 6 {
		t.Errorf("maxItems = %d, want 6", params.Properties.Options.MaxItems)
	}
	if params.Properties.Options.MinLength != 1 {
		t.Errorf("minLength = %d, want 1", params.Properties.Options.MinLength)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestPresentOptionsToolSpec -v`
预期：FAIL，编译错误 "undefined: presentOptionsToolSpec" / "undefined: PresentOptionsToolName"

- [ ] **步骤 3：编写实现代码**

`engine/agent.go` 常量块（`:17-22`）追加：

```go
const (
	HandoffToolName        = "handoff_to_agent"
	ActivateSkillToolName  = "activate_skill"
	TaskCompleteToolName   = "task_complete"
	TodoWriteToolName      = "todo_write"
	SubmitResultToolName   = "submit_result"
	PresentOptionsToolName = "present_options"
)
```

在 `todoWriteToolSpec`（`:249-284`）之后新增：

```go
// presentOptionsToolSpec returns the tool definition for declaring mutually
// exclusive options in an analysis report. When the agent has 2+ real plans to
// offer, calling this makes the confirmation popup show 方案A/B/C... choices.
// Single-plan reports must NOT call it — they use the fixed "按报告执行" path.
func presentOptionsToolSpec(zh bool) ModelTool {
	desc := "After your analysis report, if you have multiple mutually exclusive options to offer, call this tool to declare them; the engine will present them as \"方案A/B/C\" for the user to choose. Do NOT call it when there is only one option or no options."
	optionsDesc := "The list of options (2-6 non-empty strings). Each element is the raw text of a plan; the engine prefixes 方案A/B/C."
	if zh {
		desc = "分析报告后，若你有多个互斥的可选方案，调用本工具声明；引擎将按'方案A/B/C'展示供用户选择。只有一个方案或只是分析报告（无多方案）时不要调用。"
		optionsDesc = "可选方案列表（2~6 个非空字符串）。每个元素是方案的原始文本；引擎会加上'方案A/B/C'前缀。"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"options": {
						"type": "array",
						"items": {"type": "string", "minLength": 1},
						"minItems": 2,
						"maxItems": 6,
						"description": %q
					}
				},
				"required": ["options"]
			}`, optionsDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        PresentOptionsToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}
```

（`engine/agent.go` 已 import `encoding/json` 与 `fmt`，无需新增 import。）

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestPresentOptionsToolSpec -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/agent.go engine/present_options_test.go
git commit -m "feat(engine): add present_options tool spec for dynamic confirm popup"
```

---

### 任务 2：拦截与校验 `processPresentOptionsCalls`（engine/turn.go）

**文件：**
- 修改：`engine/turn.go`（新增 `processPresentOptionsCalls`）
- 测试：`engine/present_options_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/present_options_test.go`：

```go
func TestProcessPresentOptionsCalls_CapturesValid(t *testing.T) {
	e := &Engine{}
	msgs := e.processPresentOptionsCalls([]ToolCallRequest{
		{ID: "call_opt", Name: PresentOptionsToolName, Input: json.RawMessage(
			`{"options":["用 Redis 缓存","改用 MySQL"]}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].Content != "✓ 已记录 2 个可选方案，等待用户选择。" {
		t.Errorf("tool response = %q", msgs[0].Content)
	}
	if len(e.pendingConfirmOptions) != 2 || e.pendingConfirmOptions[1] != "改用 MySQL" {
		t.Errorf("pendingConfirmOptions = %v, want [用 Redis 缓存 改用 MySQL]", e.pendingConfirmOptions)
	}
}

func TestProcessPresentOptionsCalls_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty array", `{"options":[]}`},
		{"single option", `{"options":["only one"]}`},
		{"too many", `{"options":["a","b","c","d","e","f","g"]}`},
		{"blank item", `{"options":["a","  "]} `},
		{"bad json", `{invalid}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			msgs := e.processPresentOptionsCalls([]ToolCallRequest{
				{ID: "call_opt", Name: PresentOptionsToolName, Input: json.RawMessage(c.input)},
			})
			if len(msgs) != 1 {
				t.Fatalf("expected 1 error response, got %d", len(msgs))
			}
			if msgs[0].Content[:6] != "Error:" {
				t.Errorf("expected Error response, got %q", msgs[0].Content)
			}
			if len(e.pendingConfirmOptions) != 0 {
				t.Errorf("pendingConfirmOptions should stay empty, got %v", e.pendingConfirmOptions)
			}
		})
	}
}

func TestProcessPresentOptionsCalls_IgnoresOtherTools(t *testing.T) {
	e := &Engine{}
	msgs := e.processPresentOptionsCalls([]ToolCallRequest{
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{}`)},
	})
	if len(msgs) != 0 {
		t.Errorf("expected no responses for non-present_options, got %d", len(msgs))
	}
	if len(e.pendingConfirmOptions) != 0 {
		t.Errorf("pendingConfirmOptions should stay empty, got %v", e.pendingConfirmOptions)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestProcessPresentOptionsCalls -v`
预期：FAIL，编译错误 "e.processPresentOptionsCalls undefined"

- [ ] **步骤 3：编写实现代码**

`processPresentOptionsCalls` 需要 `e.pendingConfirmOptions` 字段。先在
`engine/loop.go:92`（`pendingEditPlan` 字段之后）新增字段：

```go
	// pendingConfirmOptions holds the options the agent declared via
	// present_options in its analysis report. Non-empty means the popup shows
	// 方案A/B/C... for the user to choose instead of the fixed "按报告执行".
	// NOT reset at Run start — it must survive until the next Run's
	// handleConfirmCommand reads it. Cleared once consumed.
	pendingConfirmOptions []string
```

然后在 `engine/turn.go` 的 `processTodoWriteCalls`（`:1514-1569`）之后新增：

```go
// processPresentOptionsCalls intercepts present_options tool calls from the
// assistant's response. Each call declares a set of mutually exclusive options
// the user can choose from; the engine stores them (2-6 non-empty strings) for
// the analysis-gate popup. Every call receives a tool response message
// (satisfying the DeepSeek API requirement that every tool_call_id has a
// matching tool response).
func (e *Engine) processPresentOptionsCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != PresentOptionsToolName {
			continue
		}
		var params struct {
			Options []string `json:"options"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid present_options arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if len(params.Options) < 2 || len(params.Options) > 6 {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: present_options requires 2 to 6 options. A single option should be presented as a normal report, not via present_options.",
				Timestamp:  time.Now(),
			})
			continue
		}
		valid := true
		for _, o := range params.Options {
			if strings.TrimSpace(o) == "" {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Error: present_options requires non-empty option strings",
					Timestamp:  time.Now(),
				})
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		e.pendingConfirmOptions = append([]string(nil), params.Options...)
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("✓ 已记录 %d 个可选方案，等待用户选择。", len(params.Options)),
			Timestamp:  time.Now(),
		})
	}
	return msgs
}
```

（`engine/turn.go` 已 import `encoding/json`、`fmt`、`strings`、`time`，无需新增。）

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestProcessPresentOptionsCalls -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/turn.go engine/present_options_test.go
git commit -m "feat(engine): intercept present_options calls with validation"
```

---

### 任务 3：装配 `present_options` 进工具列表 + 分流排除（engine/turn.go）

**文件：**
- 修改：`engine/turn.go`（`toolSpecsWithHandoff` + 工具调用分流 + 拦截调用点）
- 测试：`engine/present_options_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/present_options_test.go`：

```go
func TestToolSpecsWithHandoff_IncludesPresentOptions(t *testing.T) {
	e := &Engine{tools: stubToolExecutor{}, isChinese: true}
	specs := e.toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == PresentOptionsToolName {
			found = true
			break
		}
	}
	if !found {
		t.Error("toolSpecsWithHandoff should include present_options")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestToolSpecsWithHandoff_IncludesPresentOptions -v`
预期：FAIL，toolSpecsWithHandoff 未包含 present_options

- [ ] **步骤 3：编写实现代码**

`engine/turn.go:737-745` `toolSpecsWithHandoff` 追加一行：

```go
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, handoffToolSpec(e.isChinese))
	specs = append(specs, activateSkillToolSpec())
	specs = append(specs, taskCompleteToolSpec(e.isChinese))
	specs = append(specs, todoWriteToolSpec())
	specs = append(specs, presentOptionsToolSpec(e.isChinese))
	return specs
}
```

工具调用分流（`engine/turn.go:599-611`）排除 `present_options`（与 `todo_write` 并列）：

```go
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else if call.Name == ActivateSkillToolName {
			continue
		} else if call.Name == TodoWriteToolName {
			continue
		} else if call.Name == PresentOptionsToolName {
			continue
		} else {
			regularCalls = append(regularCalls, call)
		}
	}
```

同时让拦截调用生效（`engine/turn.go:580-581` 附近），追加 `processPresentOptionsCalls` 调用：

```go
	pendingActivateMsgs := e.processActivateSkillCalls(calls)
	pendingTodoMsgs := e.processTodoWriteCalls(calls)
	pendingOptionsMsgs := e.processPresentOptionsCalls(calls)
```

并在 `:587-592` 的追加块之后，追加 options 消息：

```go
	for _, msg := range pendingTodoMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingOptionsMsgs {
		e.history = append(e.history, msg)
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run 'TestToolSpecsWithHandoff_IncludesPresentOptions|TestProcessPresentOptionsCalls|TestPresentOptionsToolSpec' -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/turn.go engine/present_options_test.go
git commit -m "feat(engine): wire present_options into tool list and intercept"
```

---

### 任务 4：`pendingConfirmOptions` 字段 + 动态 `confirmOptions()`（engine/loop.go）

**文件：**
- 修改：`engine/loop.go`（新字段 + `confirmOptions` 改方法 + `confirmOptionLabel` helper + 挂载点调用改方法）
- 测试：`engine/present_options_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/present_options_test.go`：

```go
func TestConfirmOptions_NoDeclaredOptions_FixedTwo(t *testing.T) {
	e := &Engine{}
	got := e.confirmOptions()
	want := []string{"按报告执行", "输入你的意见"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestConfirmOptions_DeclaredOptions_ABCPrefixed(t *testing.T) {
	e := &Engine{pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"}}
	got := e.confirmOptions()
	want := []string{"方案A: 用 Redis 缓存", "方案B: 改用 MySQL", "输入你的意见"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("option %d = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestConfirmOptions_ -v`
预期：FAIL，编译错误（`confirmOptions` 目前是无接收者的函数，改成方法后测试用 `e.confirmOptions()` 调用——步骤 2 因方法尚未定义而编译失败）

- [ ] **步骤 3：编写实现代码**

> 注：`pendingConfirmOptions` 字段已在任务 2（`engine/loop.go:92`）定义，
> 此处不再重复。

`engine/loop.go:942-949` 的 `confirmOptions` 改为方法 + 新增 helper：

```go
// confirmOptions returns the confirmation options for the analysis-gate popup.
// No declared options → fixed two items (按报告执行 / 输入你的意见). Declared
// options → 方案A/B/C... plus the free-input entry. The last item is always
// the free-input entry — selecting it returns to the input box.
func (e *Engine) confirmOptions() []string {
	if len(e.pendingConfirmOptions) == 0 {
		return []string{
			"按报告执行",
			"输入你的意见",
		}
	}
	opts := make([]string, 0, len(e.pendingConfirmOptions)+1)
	for i, o := range e.pendingConfirmOptions {
		opts = append(opts, confirmOptionLabel(i, o))
	}
	opts = append(opts, "输入你的意见")
	return opts
}

// confirmOptionLabel renders the 方案X: <desc> label for a declared option at
// 0-based index i. Shared by confirmOptions (popup display) and
// handleConfirmCommand (history injection) so both agree on the label.
func confirmOptionLabel(i int, opt string) string {
	return fmt.Sprintf("方案%c: %s", 'A'+i, opt)
}
```

挂载点调用（`engine/loop.go:935`）改为方法调用：

```go
	if e.analysisNudgeCount > 0 {
		return &EngineResponse{
			Summary: summary,
			Options: e.confirmOptions(),
			Stage:   StageVerifyCompact,
		}, nil
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestConfirmOptions_ -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/loop.go engine/present_options_test.go
git commit -m "feat(engine): dynamic confirm options with 方案A/B/C labels"
```

---

### 任务 5：`handleConfirmCommand` 模式化语义 + 清除（engine/loop.go）

**文件：**
- 修改：`engine/loop.go`（`handleConfirmCommand`）
- 测试：`engine/present_options_test.go`（追加）+ `engine/confirm_command_test.go`（替换 `TestHandleConfirmCommand_FeedbackVariant`）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/present_options_test.go`：

```go
func TestHandleConfirmCommand_NoOptions_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state:    &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		history:  []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode || !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisMode should be false / AnalysisReportConfirmed true after confirm")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "按报告执行") {
		t.Errorf("history should mention 按报告执行, got %q", last)
	}
}

func TestHandleConfirmCommand_WithOptions_SelectedPlanInjected(t *testing.T) {
	e := &Engine{
		state:    &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		history:  []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
		pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"},
	}

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if e.state.AnalysisMode || !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisMode should be false / AnalysisReportConfirmed true after selecting a plan")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案B: 改用 MySQL") {
		t.Errorf("history should mention the selected plan 方案B: 改用 MySQL, got %q", last)
	}
	if len(e.pendingConfirmOptions) != 0 {
		t.Errorf("pendingConfirmOptions should be cleared after selection, got %v", e.pendingConfirmOptions)
	}
}
```

`engine/confirm_command_test.go:104-132` 的 `TestHandleConfirmCommand_FeedbackVariant` 替换为多方案下 `/confirm 1` 注入方案A 的测试：

```go
// 多方案模式下 /confirm 1 → 选择方案A，确认执行并注入方案描述。
func TestHandleConfirmCommand_WithOptions_FirstPlanInjected(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:              []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese:            true,
		pendingConfirmOptions: []string{"用 Redis 缓存", "改用 MySQL"},
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after selecting 方案A")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after selecting 方案A")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案A: 用 Redis 缓存") {
		t.Errorf("history should mention 方案A: 用 Redis 缓存, got %q", last)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run 'TestHandleConfirmCommand_' -v`
预期：FAIL——新语义未实现时 `TestHandleConfirmCommand_NoOptions_ConfirmExecutes` 因历史注入仍是"方案A：按报告执行修改"而失败；`TestHandleConfirmCommand_WithOptions_*` 因 `pendingConfirmOptions` 未参与注入而失败。

- [ ] **步骤 3：编写实现代码**

`engine/loop.go:1619-1650` 的 `handleConfirmCommand` 整体替换为：

```go
// handleConfirmCommand processes a /confirm N message deterministically,
// bypassing isDangerousConfirmation and the intent LLM classifier.
//
// Any /confirm N flips AnalysisReportConfirmed + clears AnalysisMode so the
// agent's next edit/write in this same Run passes the analysis gate.
// When the agent declared options via present_options (pendingConfirmOptions
// non-empty), /confirm N selects 方案N and the choice is injected into history
// so the agent implements the selected plan. When no options were declared,
// /confirm 1 confirms the report ("按报告执行"). The last popup item
// ("输入你的意见") never reaches here — the UI returns to the input box.
// Returns true if userMsg was a valid /confirm command.
func (e *Engine) handleConfirmCommand(userMsg string) bool {
	n, ok := parseConfirmCommand(userMsg)
	if !ok {
		return false
	}
	// 置确认态（任何 /confirm N 都确认执行）。
	e.state.AnalysisReportConfirmed = true
	e.state.AnalysisMode = false
	e.pendingAnalysisNudge = false

	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		if len(e.pendingConfirmOptions) > 0 && n >= 1 && n <= len(e.pendingConfirmOptions) {
			label := confirmOptionLabel(n-1, e.pendingConfirmOptions[n-1])
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了方案：%s，请按该方案执行修改。", label)
		} else {
			e.history[len(e.history)-1].Content = "✓ 分析报告已确认（按报告执行），可以开始修改代码。"
		}
	}
	// 本组方案已消费（用户已选择或确认），清除避免残留到无关 Run。
	e.pendingConfirmOptions = nil
	loopLog.Printf("handleConfirmCommand: /confirm %d processed", n)
	return true
}
```

同时处理**自由输入路径**的清除：用户在弹出框出现时选"输入你的意见"回到输入框自由输入，此时不经过 `/confirm`。在 `engine/loop.go` 的 Run 主逻辑，`handleAnalysisNudgeConfirmation`（`:448`）之后、`pendingEditPlan` 分支（`:450`）之前插入：

```go
	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
	// 回输入框），本组待决方案作废，避免残留到下一轮门控拦截时再次弹出。
	if len(e.pendingConfirmOptions) > 0 {
		e.pendingConfirmOptions = nil
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run 'TestHandleConfirmCommand_' -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/loop.go engine/present_options_test.go engine/confirm_command_test.go
git commit -m "feat(engine): confirm command selects declared 方案A/B/C plans"
```

---

### 任务 6：同步受影响测试与字面量（engine + ui）

**文件：**
- 修改：`engine/confirm_command_test.go`（末项断言"其他"→"意见"）
- 修改：`ui/confirm_test.go`、`ui/finish_streaming_test.go`（固定项字面量同步）

- [ ] **步骤 1：编写/更新测试**

`engine/confirm_command_test.go:155-160` 的末项断言更新：

```go
	if len(resp.Options) != 2 {
		t.Fatalf("expected 2 options, got %d: %v", len(resp.Options), resp.Options)
	}
	if !strings.Contains(resp.Options[len(resp.Options)-1], "意见") {
		t.Errorf("last option should be the free-input item, got %q", resp.Options[len(resp.Options)-1])
	}
```

`ui/confirm_test.go:36-39` 的 `activeOptions` 固定项同步：

```go
	m.activeOptions = []string{
		"按报告执行",
		"输入你的意见",
	}
```

`ui/confirm_test.go:69` 末项测试数据同步：

```go
	m.activeOptions = []string{"按报告执行", "输入你的意见"}
```

`ui/finish_streaming_test.go:280-283` 的 Options 固定项同步：

```go
		Options: []string{
			"按报告执行",
			"输入你的意见",
		},
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/ -run TestConfirmOptions_ReturnedWhenGateIntercepted -v`
预期：在同步前 FAIL（末项含"其他"断言失败，因为新固定项是"输入你的意见"）。

- [ ] **步骤 3：应用测试/字面量改动**

按步骤 1 的代码块应用全部 4 处改动（`engine/confirm_command_test.go`、`ui/confirm_test.go` 两处、`ui/finish_streaming_test.go`）。

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./engine/... ./ui/... 2>&1 | tail -30`
预期：全部 PASS（engine 与 ui 包）

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git add engine/confirm_command_test.go ui/confirm_test.go ui/finish_streaming_test.go
git commit -m "test: sync fixed confirm popup copy and option assertions"
```

---

### 任务 7：全量验证

**文件：** 无（验证 + 收尾）

- [ ] **步骤 1：全量构建**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go build ./...`
预期：无错误

- [ ] **步骤 2：全量测试**

运行：`cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup && go test ./... 2>&1 | grep -v '^ok\|no test files' | head -40`
预期：无 FAIL（所有测试 ok）

- [ ] **步骤 3：核对规格验收项**

逐项核对规格文档（`docs/superpowers/specs/2026-09-03-confirm-option-label-design.md`）的验收清单：
- [ ] 无方案时弹出框：`[1] 按报告执行`、`[2] 输入你的意见`
- [ ] 声明 2 个方案时：`[1] 方案A: ...`、`[2] 方案B: ...`、`[3] 输入你的意见`
- [ ] 选任意方案 → `/confirm N` 确定性确认执行，历史注入所选方案描述
- [ ] 选"输入你的意见" → 回到输入框自由输入

- [ ] **步骤 4：Commit 收尾**

```bash
cd /Users/admin/gitspace/deepact/.worktrees/confirm-options-popup
git log --oneline -6
```

预期：6 个 commit（任务 1-6），工作树干净。
