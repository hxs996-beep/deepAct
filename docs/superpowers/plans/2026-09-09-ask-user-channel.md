# ask_user 统一提问通道 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 用统一的 `ask_user(question, options?)` 工具替代 `present_options`，让模型在任意时刻需要用户输入时主动提问（有候选→弹窗，无候选→自由输入）。

**架构：** 复用现有 `present_options` 链路（拦截→存状态→结束 Run→挂 Options→`/confirm N` 消费→UI 弹窗），改为 `AskUserRequest{Question, Options}` 结构。无 options 时走已有的 `awaiting_user` Blocked 分支（`turn.go` 置 Blocked，`loop.go:823` 优先处理），UI 与 exec 零改动。

**技术栈：** Go（engine/ 包）、标准库。复用：`present_options` 全链路、`awaiting_user` Blocked 分支、`confirmOptionLabel`、`/confirm N` 机制、sub-agent `filterTools` allowList。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/agent.go` | 工具名常量 + `askUserToolSpec`（question+options） | 修改（新增，任务5删旧） |
| `engine/types.go` | 新增 `AskUserRequest{Question, Options}` 类型 | 修改 |
| `engine/loop.go` | 字段 `pendingAskUser`、`askUserOptions()`、组装分支、`handleConfirmCommand`、自由输入路径 | 修改 |
| `engine/turn.go` | `processAskUserCalls` 拦截、结束条件、调用名分支、工具注册 | 修改（新增，任务5删旧） |
| `engine/ask_user_test.go` | 新工具/拦截/组装/确认测试（由 present_options_test.go 演进） | 创建 |
| `engine/ask_user_ends_run_test.go` | 有/无 options 的 Run 级集成测试（由 present_options_ends_run_test.go 演进） | 创建 |
| `engine/confirm_command_test.go` | `pendingAskUser` 字段适配 | 修改 |
| `ui/finish_streaming_test.go` | 零改动（Options 消费逻辑通用，不依赖工具名） | 无 |

> 中间任务（1-4）新旧代码并存以保证每步可编译；**任务 5 统一清理**旧符号（`PresentOptionsToolName`/`presentOptionsToolSpec`/`processPresentOptionsCalls`/`pendingConfirmOptions`/`confirmOptions`）并删除旧测试文件。

---

### 任务 1：ask_user 工具定义与 AskUserRequest 类型

**文件：**
- 修改：`engine/agent.go`（新增 `AskUserToolName` 常量 + `askUserToolSpec`，暂不删旧的）
- 修改：`engine/types.go`（新增 `AskUserRequest` 类型）
- 测试：`engine/ask_user_test.go`（新建，含 `TestAskUserToolSpec`）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/ask_user_test.go`：

```go
package engine

import (
	"encoding/json"
	"testing"
)

func TestAskUserToolSpec(t *testing.T) {
	spec := askUserToolSpec(true)
	if spec.Function.Name != AskUserToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, AskUserToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Question struct {
				Type string `json:"type"`
			} `json:"question"`
			Options struct {
				Type     string `json:"type"`
				MinItems int    `json:"minItems"`
				MaxItems int    `json:"maxItems"`
				Items    struct {
					Type      string `json:"type"`
					MinLength int    `json:"minLength"`
				} `json:"items"`
			} `json:"options"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "question" {
		t.Errorf("required = %v, want [question]", params.Required)
	}
	if params.Properties.Question.Type != "string" {
		t.Errorf("question.type = %q, want string", params.Properties.Question.Type)
	}
	if params.Properties.Options.MinItems != 2 {
		t.Errorf("minItems = %d, want 2", params.Properties.Options.MinItems)
	}
	if params.Properties.Options.MaxItems != 6 {
		t.Errorf("maxItems = %d, want 6", params.Properties.Options.MaxItems)
	}
	if params.Properties.Options.Items.MinLength != 1 {
		t.Errorf("items.minLength = %d, want 1", params.Properties.Options.Items.MinLength)
	}
	if params.Properties.Options.Items.Type != "string" {
		t.Errorf("items.type = %q, want string", params.Properties.Options.Items.Type)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestAskUserToolSpec -v`
预期：编译失败——`askUserToolSpec`、`AskUserToolName` 未定义。

- [ ] **步骤 3：编写最少实现代码**

`engine/types.go` 在 `EngineResponse` 结构（`:100`）之后追加：

```go
// AskUserRequest captures an ask_user tool call: the question the model needs
// the user to answer, plus optional candidate answers. Stored on the Engine
// while awaiting the user's response; consumed by handleConfirmCommand (with
// options) or cleared on free input (without options).
type AskUserRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}
```

`engine/agent.go` 常量区（`:22`）`PresentOptionsToolName = "present_options"` 之后追加：

```go
	AskUserToolName = "ask_user"
```

`engine/agent.go` 在 `presentOptionsToolSpec`（`:291-319`）之后追加（暂不删旧函数）：

```go
// askUserToolSpec returns the tool definition for asking the user a question.
// The model calls this at any point (analysis report, mid-execution) when it
// needs the user to provide information or make a decision that cannot be
// determined from the codebase — covering config gaps, external facts, user
// preferences, tradeoff choices, etc. Provide 2-6 candidate answers to present
// them as selectable options; omit options for an open-ended question.
func askUserToolSpec(zh bool) ModelTool {
	desc := "Call this tool when you need the user to provide information or make a decision that you cannot determine yourself (missing configuration, external facts, user preferences, tradeoff choices). If you have 2-6 mutually exclusive candidate answers, provide them as options; otherwise omit options for an open-ended question. Do NOT call it for information you can verify yourself by searching the code or using tools."
	questionDesc := "The question to present to the user."
	optionsDesc := "Optional candidate answers (2-6 non-empty strings). When provided, the engine presents them as selectable options (方案A/B/C); when omitted, the user answers freely."
	if zh {
		desc = "当你需要用户提供无法自行确定的信息或做决定时（配置缺失、外部事实、用户偏好、权衡选择等），调用本工具。若有 2~6 个互斥的候选答案，作为 options 提供；否则省略 options 呈现开放式问题。能通过搜索代码或工具自行验证的信息不要调用。"
		questionDesc = "呈现给用户的问题。"
		optionsDesc = "可选候选答案（2~6 个非空字符串）。提供时引擎以可选项（方案A/B/C）呈现；省略时用户自由输入。"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"question": {
						"type": "string",
						"description": %q
					},
					"options": {
						"type": "array",
						"items": {"type": "string", "minLength": 1},
						"minItems": 2,
						"maxItems": 6,
						"description": %q
					}
				},
				"required": ["question"]
			}`, questionDesc, optionsDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        AskUserToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestAskUserToolSpec -v`
预期：PASS。（旧 `present_options` 测试仍应通过，旧符号未动。）

- [ ] **步骤 5：Commit**

```bash
git add engine/agent.go engine/types.go engine/ask_user_test.go
git commit -m "feat(engine): add ask_user tool spec and AskUserRequest type"
```

---

### 任务 2：拦截处理 + 状态字段 + 注册

**文件：**
- 修改：`engine/loop.go`（新增 `pendingAskUser` 字段）
- 修改：`engine/turn.go`（`processAskUserCalls` + 调用名分支 + 注册 + 调用点）
- 测试：`engine/ask_user_test.go`（追加拦截与注册测试）

- [ ] **步骤 1：编写失败的测试**

`engine/ask_user_test.go` 追加（import 加 `"encoding/json"` 已有、加 `"strings"`）：

```go
func TestProcessAskUserCalls_CapturesValid(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(
			`{"question":"缓存方案选哪个？","options":["用 Redis 缓存","改用 MySQL"]}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if e.pendingAskUser == nil {
		t.Fatal("pendingAskUser should be set")
	}
	if e.pendingAskUser.Question != "缓存方案选哪个？" {
		t.Errorf("question = %q", e.pendingAskUser.Question)
	}
	if len(e.pendingAskUser.Options) != 2 || e.pendingAskUser.Options[1] != "改用 MySQL" {
		t.Errorf("options = %v", e.pendingAskUser.Options)
	}
}

func TestProcessAskUserCalls_CapturesNoOptions(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(
			`{"question":"数据库连接字符串是什么？"}`)},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if e.pendingAskUser == nil || e.pendingAskUser.Question != "数据库连接字符串是什么？" {
		t.Fatalf("pendingAskUser = %+v", e.pendingAskUser)
	}
	if len(e.pendingAskUser.Options) != 0 {
		t.Errorf("options should be empty, got %v", e.pendingAskUser.Options)
	}
}

func TestProcessAskUserCalls_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty question", `{"question":""}`},
		{"blank question", `{"question":"   "}`},
		{"single option", `{"question":"q","options":["only one"]}`},
		{"too many", `{"question":"q","options":["a","b","c","d","e","f","g"]}`},
		{"blank item", `{"question":"q","options":["a","  "]} `},
		{"bad json", `{invalid}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			msgs := e.processAskUserCalls([]ToolCallRequest{
				{ID: "call_ask", Name: AskUserToolName, Input: json.RawMessage(c.input)},
			})
			if len(msgs) != 1 {
				t.Fatalf("expected 1 error response, got %d", len(msgs))
			}
			if len(msgs[0].Content) < 6 || msgs[0].Content[:6] != "Error:" {
				t.Errorf("expected Error response, got %q", msgs[0].Content)
			}
			if e.pendingAskUser != nil {
				t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
			}
		})
	}
}

func TestProcessAskUserCalls_IgnoresOtherTools(t *testing.T) {
	e := &Engine{}
	msgs := e.processAskUserCalls([]ToolCallRequest{
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{}`)},
	})
	if len(msgs) != 0 {
		t.Errorf("expected no responses for non-ask_user, got %d", len(msgs))
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
	}
}

func TestToolSpecsWithHandoff_IncludesAskUser(t *testing.T) {
	e := &Engine{tools: stubToolExecutor{}, isChinese: true}
	specs := e.toolSpecsWithHandoff()
	found := false
	for _, s := range specs {
		if s.Function.Name == AskUserToolName {
			found = true
			break
		}
	}
	if !found {
		t.Error("toolSpecsWithHandoff should include ask_user")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestProcessAskUserCalls|TestToolSpecsWithHandoff_IncludesAskUser' -v`
预期：编译失败——`processAskUserCalls` 未定义、`pendingAskUser` 字段不存在。

- [ ] **步骤 3：编写最少实现代码**

`engine/loop.go` 字段区，`pendingConfirmOptions`（`:94-99`）之后追加：

```go
	// pendingAskUser holds the question the agent asked the user via ask_user.
	// Non-nil means the engine is awaiting the user's response — with Options
	// the popup shows 方案A/B/C... for the user to choose, without Options the
	// question is presented via the awaiting_user Blocked path for free input.
	// NOT reset at Run start — it must survive until the next Run's
	// handleConfirmCommand (with options) or the free-input path reads it.
	// Cleared once consumed.
	pendingAskUser *AskUserRequest
```

`engine/turn.go` 在 `processPresentOptionsCalls`（`:1531-1589`）之后追加：

```go
// processAskUserCalls intercepts ask_user tool calls from the assistant's
// response. Each call declares a question (and optionally 2-6 candidate
// answers) the user must respond to; the engine stores it for presentation.
// Every call receives a tool response message (satisfying the DeepSeek API
// requirement that every tool_call_id has a matching tool response).
func (e *Engine) processAskUserCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != AskUserToolName {
			continue
		}
		var params struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid ask_user arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if strings.TrimSpace(params.Question) == "" {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: ask_user requires a non-empty question",
				Timestamp:  time.Now(),
			})
			continue
		}
		if len(params.Options) > 0 {
			if len(params.Options) < 2 || len(params.Options) > 6 {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Error: ask_user options require 2 to 6 items. Provide 2-6 candidate answers or omit options for an open-ended question.",
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
						Content:    "Error: ask_user requires non-empty option strings",
						Timestamp:  time.Now(),
					})
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
		}
		e.pendingAskUser = &AskUserRequest{
			Question: params.Question,
			Options:  append([]string(nil), params.Options...),
		}
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    "✓ 已记录问题，等待用户回答。",
			Timestamp:  time.Now(),
		})
	}
	return msgs
}
```

`engine/turn.go` 调用名分支（`:561`）`} else if call.Name == PresentOptionsToolName {` 之后追加：

```go
		} else if call.Name == AskUserToolName {
			continue
```

`engine/turn.go` 拦截调用点（`:529-545`）：`pendingOptionsMsgs := e.processPresentOptionsCalls(calls)` 之后追加 `pendingAskUserMsgs := e.processAskUserCalls(calls)`，并在 `for _, msg := range pendingOptionsMsgs` 循环之后追加对应循环：

```go
	pendingActivateMsgs := e.processActivateSkillCalls(calls)
	pendingTodoMsgs := e.processTodoWriteCalls(calls)
	pendingOptionsMsgs := e.processPresentOptionsCalls(calls)
	pendingAskUserMsgs := e.processAskUserCalls(calls)

	e.history = append(e.history, assistant)

	// Add activate_skill tool messages AFTER the assistant message, so the
	// DeepSeek API sees the correct order: assistant(tool_calls) → tool.
	for _, msg := range pendingActivateMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingTodoMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingOptionsMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingAskUserMsgs {
		e.history = append(e.history, msg)
	}
```

`engine/turn.go` 注册（`:706`）`specs = append(specs, presentOptionsToolSpec(e.isChinese))` 之后追加：

```go
	specs = append(specs, askUserToolSpec(e.isChinese))
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestProcessAskUserCalls|TestToolSpecsWithHandoff_IncludesAskUser' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/turn.go engine/ask_user_test.go
git commit -m "feat(engine): intercept ask_user calls and store pendingAskUser"
```

---

### 任务 3：结束 Run + 响应组装

**文件：**
- 修改：`engine/turn.go`（结束条件改用 `pendingAskUser`）
- 修改：`engine/loop.go`（`confirmOptions`→`askUserOptions`、组装分支）
- 测试：`engine/ask_user_ends_run_test.go`（新建，迁移 ends-run 集成测试）、`engine/ask_user_test.go`（追加 askUserOptions 测试）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/ask_user_ends_run_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// 有 options 的 ask_user：模型调用 ask_user（声明互斥候选）后应立即结束 Run，
// CompletionSummary 携带问题前的报告文本，不再触发额外模型调用。
func TestAskUser_EndsRunWithOptions(t *testing.T) {
	reportText := "缓存方案需要你决定。\n\n方案A：改为前缀匹配。\n方案B：改引擎 Summary 源。"

	grepChunks := []ModelChunk{
		{Delta: "搜索代码",
			ToolCalls: []ModelToolCall{
				{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "grep", Arguments: `{"pattern":"foo","path":"."}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	editChunks := []ModelChunk{
		{Delta: "修改代码",
			ToolCalls: []ModelToolCall{
				{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: "edit", Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	reportChunks := []ModelChunk{
		{Delta: reportText,
			ToolCalls: []ModelToolCall{
				{ID: "c3", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"缓存方案选哪个？","options":["改为前缀匹配","改引擎 Summary 源"]}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, editChunks, reportChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		readLoop:  NewReadLoopState(),
	}

	resp, err := e.Run(context.Background(), "优化方案显示")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// 1) Options 必须被挂载（方案A/B + 意见）
	if len(resp.Options) != 3 {
		t.Fatalf("expected 3 options, got %v", resp.Options)
	}
	// 2) Summary 必须是问题前的报告文本，而不是后续兜底 turn 的文本
	if resp.Summary != reportText {
		t.Errorf("resp.Summary = %q, want %q", resp.Summary, reportText)
	}
	// 3) Run 应在 ask_user turn 结束，不再额外调用模型
	if model.callIdx != 3 {
		t.Errorf("model.callIdx = %d, want 3", model.callIdx)
	}
	// 4) 有 options 走 Done 路径，不应标记 Blocked
	if resp.Blocked {
		t.Errorf("expected non-Blocked response, got BlockedBy=%q", resp.BlockedBy)
	}
}

// 无 options 的 ask_user：模型调用 ask_user（开放式问题）后应立即结束 Run，
// 通过 awaiting_user Blocked 分支呈现问题，用户自由输入。
func TestAskUser_NoOptions_EndsRunBlockedAwaitingUser(t *testing.T) {
	question := "数据库连接字符串是什么？"

	grepChunks := []ModelChunk{
		{Delta: "搜索配置",
			ToolCalls: []ModelToolCall{
				{ID: "c1", Type: "function", Function: ModelFunctionCall{Name: "grep", Arguments: `{"pattern":"dsn","path":"."}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	askChunks := []ModelChunk{
		{Delta: "没有找到 DSN 配置，我需要你提供。",
			ToolCalls: []ModelToolCall{
				{ID: "c2", Type: "function", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"数据库连接字符串是什么？"}`}},
			},
			FinishReason: "tool_calls", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{grepChunks, askChunks}}
	e := &Engine{
		model:     model,
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
		config:    EngineConfig{ModelName: "test-model"},
		isChinese: true,
		guards:    &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(false)},
		readLoop:  NewReadLoopState(),
	}

	resp, err := e.Run(context.Background(), "配置数据库连接")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !resp.Blocked {
		t.Fatal("expected Blocked response for no-options ask_user")
	}
	if resp.BlockedBy != "awaiting_user" {
		t.Errorf("BlockedBy = %q, want awaiting_user", resp.BlockedBy)
	}
	if len(resp.Questions) != 1 || resp.Questions[0] != question {
		t.Errorf("Questions = %v, want [%s]", resp.Questions, question)
	}
	if len(resp.Options) != 0 {
		t.Errorf("expected no options, got %v", resp.Options)
	}
	if model.callIdx != 2 {
		t.Errorf("model.callIdx = %d, want 2", model.callIdx)
	}
}
```

`engine/ask_user_test.go` 追加（import 加 `"strings"`）：

```go
func TestAskUserOptions_NoPending_FixedTwo(t *testing.T) {
	e := &Engine{}
	got := e.askUserOptions()
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

func TestAskUserOptions_PendingWithOptions_ABCPrefixed(t *testing.T) {
	e := &Engine{pendingAskUser: &AskUserRequest{
		Question: "缓存方案选哪个？",
		Options:  []string{"用 Redis 缓存", "改用 MySQL"},
	}}
	got := e.askUserOptions()
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

func TestAskUserOptions_PendingNoOptions_Nil(t *testing.T) {
	e := &Engine{pendingAskUser: &AskUserRequest{Question: "数据库连接字符串是什么？"}}
	got := e.askUserOptions()
	if got != nil {
		t.Errorf("expected nil options without options, got %v", got)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestAskUser_EndsRun|TestAskUser_NoOptions|TestAskUserOptions' -v`
预期：FAIL——`askUserOptions` 未定义；ends-run 测试因结束条件未接 `pendingAskUser` 而失败（模型被多调用或 Options 未挂载）。

- [ ] **步骤 3：编写最少实现代码**

`engine/turn.go` 结束条件（`:645-648`）：

```go
	if len(e.pendingConfirmOptions) > 0 {
		result.Done = true
		result.CompletionSummary = content
	}
```

替换为：

```go
	if e.pendingAskUser != nil {
		result.Done = true
		result.CompletionSummary = content
		// No options → present the question via the awaiting_user Blocked path
		// (loop.go processes Blocked before Done). The engine must never decide
		// on the user's behalf; use the structured Question, not narration text.
		if len(e.pendingAskUser.Options) == 0 {
			result.Blocked = true
			result.BlockedBy = "awaiting_user"
			result.Questions = []string{e.pendingAskUser.Question}
		}
	}
```

`engine/loop.go` 将 `confirmOptions`（`:983-1000`）整体替换为 `askUserOptions`：

```go
// askUserOptions returns the presentation options after a Run.
// No pending ask_user → the analysis-gate fixed two items (按报告执行 /
// 输入你的意见). Pending ask_user with options → 方案A/B/C... plus the
// free-input entry. Pending ask_user without options → nil (the question is
// presented via the awaiting_user Blocked path, user answers freely).
func (e *Engine) askUserOptions() []string {
	if e.pendingAskUser == nil {
		return []string{
			"按报告执行",
			"输入你的意见",
		}
	}
	if len(e.pendingAskUser.Options) == 0 {
		return nil
	}
	opts := make([]string, 0, len(e.pendingAskUser.Options)+1)
	for i, o := range e.pendingAskUser.Options {
		opts = append(opts, confirmOptionLabel(i, o))
	}
	opts = append(opts, "输入你的意见")
	return opts
}
```

`engine/loop.go` 组装（`:973-980`），在 `if e.analysisNudgeCount > 0 {` 之前插入 ask_user 分支，并把 `confirmOptions()` 改为 `askUserOptions()`：

```go
	// ask_user with options: the agent asked a question and declared candidate
	// answers — present them as selectable options (方案A/B/C + 输入你的意见).
	// 无 options 已由 awaiting_user Blocked 分支（loop.go:823）处理，不走此处。
	if e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0 {
		return &EngineResponse{
			Summary: summary,
			Options: e.askUserOptions(),
			Stage:   StageAct,
		}, nil
	}
	if e.analysisNudgeCount > 0 {
		return &EngineResponse{
			Summary: summary,
			Options: e.askUserOptions(),
			Stage:   StageVerifyCompact,
		}, nil
	}
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
```

> 注意：任务 3 删除 `confirmOptions` 函数后，旧 `present_options_test.go` 中引用 `e.confirmOptions()` 的 `TestConfirmOptions_*` 会编译失败。**因此本任务必须先 git rm 旧测试文件，再运行新测试。** 旧 `present_options_test.go` 中的确认行为测试已在任务 4 覆盖（`TestHandleConfirmCommand_*`）。

```bash
git rm engine/present_options_test.go
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestAskUser_EndsRun|TestAskUser_NoOptions|TestAskUserOptions|TestAskUserToolSpec|TestProcessAskUserCalls|TestToolSpecsWithHandoff_IncludesAskUser' -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add -A engine/ask_user_test.go engine/ask_user_ends_run_test.go engine/turn.go engine/loop.go
git commit -m "feat(engine): end Run on ask_user (Blocked for open questions, Options for candidates)"
```

---

### 任务 4：handleConfirmCommand 适配

**文件：**
- 修改：`engine/loop.go`（`handleConfirmCommand` 改用 `pendingAskUser`）
- 测试：`engine/ask_user_test.go`（追加/适配 confirm 测试）、`engine/confirm_command_test.go`（字段适配）

- [ ] **步骤 1：编写失败的测试**

`engine/ask_user_test.go` 追加：

```go
// 无待决问题（纯分析门控）时，/confirm 1 确认报告（"按报告执行"）。
func TestHandleConfirmCommand_NoOptions_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after confirm")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "按报告执行") {
		t.Errorf("history should mention 按报告执行, got %q", last)
	}
}

// 有待决问题且带 options 时，/confirm N 选择方案N并注入方案描述。
func TestHandleConfirmCommand_WithOptions_SelectedPlanInjected(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after selecting a plan")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案B: 改用 MySQL") {
		t.Errorf("history should mention the selected plan 方案B: 改用 MySQL, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after selection, got %+v", e.pendingAskUser)
	}
}

// 有待决问题但编号越界（n > len(options)）时，不静默降级为"按报告执行"。
func TestHandleConfirmCommand_WithOptions_InvalidIndex(t *testing.T) {
	e := &Engine{
		state:     &TaskState{AnalysisReportConfirmed: false},
		history:   []Message{{Role: "user", Content: "/confirm 5"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 5") {
		t.Fatal("handleConfirmCommand should handle /confirm 5")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after out-of-range confirm")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "无效") {
		t.Errorf("history should mention invalid option for out-of-range N, got %q", last)
	}
	if strings.Contains(last, "按报告执行") {
		t.Errorf("out-of-range N must NOT degrade to 按报告执行, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after out-of-range confirm, got %+v", e.pendingAskUser)
	}
}
```

`engine/confirm_command_test.go` 中 `TestHandleConfirmCommand_WithOptions_FirstPlanInjected`（`:59-81`）的字段适配——`pendingConfirmOptions: []string{...}` 改为：

```go
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
```

（断言不变：`方案A: 用 Redis 缓存`。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestHandleConfirmCommand' -v`
预期：编译失败——`pendingAskUser` 分支未在 `handleConfirmCommand` 实现；`TestHandleConfirmCommand_WithOptions_*` 引用已删的 `pendingConfirmOptions` 字段。

- [ ] **步骤 3：编写最少实现代码**

`engine/loop.go` `handleConfirmCommand`（`:1567-1596`）替换为：

```go
func (e *Engine) handleConfirmCommand(userMsg string) bool {
	n, ok := parseConfirmCommand(userMsg)
	if !ok {
		return false
	}
	// 置确认态（任何 /confirm N 都确认执行）。
	e.state.AnalysisReportConfirmed = true
	e.pendingAnalysisNudge = false

	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		switch {
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0 && n >= 1 && n <= len(e.pendingAskUser.Options):
			label := confirmOptionLabel(n-1, e.pendingAskUser.Options[n-1])
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了：%s，请按该方案执行修改。", label)
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0:
			// 有声明方案但编号越界（n < 1 或 n > len(options)）：不静默降级为
			// "按报告执行"，明确告知 agent 用户选择无效，由其决定下一步。
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了无效的方案编号 %d，请重新选择。", n)
			loopLog.Printf("handleConfirmCommand: /confirm %d out of range (pending options=%d)", n, len(e.pendingAskUser.Options))
		default:
			e.history[len(e.history)-1].Content = "✓ 分析报告已确认（按报告执行），可以开始修改代码。"
		}
	}
	// 本组问题已消费（用户已选择、越界或确认），清除避免残留到无关 Run。
	e.pendingAskUser = nil
	loopLog.Printf("handleConfirmCommand: /confirm %d processed", n)
	return true
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestHandleConfirmCommand|TestAskUserOptions|TestParseConfirmCommand' -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/ask_user_test.go engine/confirm_command_test.go
git commit -m "feat(engine): adapt /confirm N to select ask_user options"
```

---

### 任务 5：自由输入路径 + 清理旧代码 + 全量回归

**文件：**
- 修改：`engine/loop.go`（自由输入路径改用 `pendingAskUser`）
- 修改：`engine/agent.go`（删 `PresentOptionsToolName`、`presentOptionsToolSpec`）
- 修改：`engine/turn.go`（删 `processPresentOptionsCalls`、`PresentOptionsToolName` 分支、旧调用点）
- 测试：`engine/ask_user_test.go`（追加自由输入测试）、`engine/present_options_ends_run_test.go`（删除，已迁移）

- [ ] **步骤 1：编写失败的测试**

`engine/ask_user_test.go` 追加（import 加 `"context"`）：

```go
// 自由输入路径：用户未发 /confirm N（无 options 的 ask_user 直接输入），
// Run 主逻辑中的清除块应清空待决问题，避免残留到下一轮再次弹出。
func TestAskUser_ClearedOnFreeInputRun(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成。", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "连接字符串是 mysql://root@localhost/db"}},
		config:  EngineConfig{ModelName: "test-model"},
		pendingAskUser: &AskUserRequest{
			Question: "数据库连接字符串是什么？",
		},
	}
	e.pendingAnalysisNudge = true

	if _, err := e.Run(context.Background(), "连接字符串是 mysql://root@localhost/db"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after a free-input Run, got %+v", e.pendingAskUser)
	}
}
```

删除已迁移的旧集成测试文件：

```bash
git rm engine/present_options_ends_run_test.go
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestAskUser_ClearedOnFreeInputRun -v`
预期：FAIL——自由输入清除块仍引用已删的 `pendingConfirmOptions`（编译失败或清除逻辑未改）。

- [ ] **步骤 3：清理旧代码**

`engine/loop.go` 自由输入路径（`:487-489`）：

```go
	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
	// 回输入框），本组待决方案作废，避免残留到下一轮门控拦截时再次弹出。
	if len(e.pendingConfirmOptions) > 0 {
		e.pendingConfirmOptions = nil
	}
```

替换为：

```go
	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
	// 回输入框，或无 options 的 ask_user 直接输入），本组待决问题作废，
	// 避免残留到下一轮再次弹出。
	if e.pendingAskUser != nil {
		e.pendingAskUser = nil
	}
```

`engine/agent.go` 删除：常量 `PresentOptionsToolName = "present_options"`（`:22`）、函数 `presentOptionsToolSpec`（`:287-319`）。

`engine/loop.go` 删除字段声明 `pendingConfirmOptions`（`:94-99`，连同注释）——任务 2 追加的 `pendingAskUser` 是唯一待决状态：

```go
	// pendingConfirmOptions holds the options the agent declared via
	// present_options in its analysis report. Non-empty means the popup shows
	// 方案A/B/C... for the user to choose instead of the fixed "按报告执行".
	// NOT reset at Run start — it must survive until the next Run's
	// handleConfirmCommand reads it. Cleared once consumed.
	pendingConfirmOptions []string
```

（若 grep 确认 `pendingConfirmOptions` 已无引用，直接删除上述注释块与字段声明即可。）

`engine/turn.go` 删除：
- `processPresentOptionsCalls` 函数（`:1531-1589`）
- 调用名分支 `} else if call.Name == PresentOptionsToolName { continue }`（`:561-563`）
- 拦截调用点 `pendingOptionsMsgs := e.processPresentOptionsCalls(calls)` 及其 append 循环（`:531`、`:543-545`）
- 注册行 `specs = append(specs, presentOptionsToolSpec(e.isChinese))`（`:706`）

（`loop.go` 中的 `pendingConfirmOptions` 字段声明 `:94-99` 已在本步骤上方删除；`confirmOptions` 函数已在任务 3 替换为 `askUserOptions`。本任务只剩自由输入路径 `:487` 一处。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestAskUser_ClearedOnFreeInputRun -v`
预期：PASS。

- [ ] **步骤 5：全量构建与测试**

运行：`go build ./... && go test ./...`
预期：全部编译通过、全部测试 PASS。重点确认：
- `present_options` 相关测试文件已全部迁移（`present_options_test.go`、`present_options_ends_run_test.go` 不再存在）
- `TestConfirmOptions_ReturnedWhenGateIntercepted`、`TestConfirmOptions_NotReturnedWithoutGate`（`confirm_command_test.go`）不引用 `confirmOptions`/`pendingConfirmOptions`，只断言 `resp.Options`——不受影响
- UI 测试 `go test ./ui/` 全绿（`TestFinishStreaming_NonBlockedOptionsShowPopup` 只构造 `Options` 字符串数组，不依赖工具名）

用 grep 确认无残留引用：

```bash
grep -rn "PresentOptionsToolName\|presentOptionsToolSpec\|processPresentOptionsCalls\|pendingConfirmOptions\|confirmOptions\b" engine/ --include="*.go"
```

预期：无输出。

- [ ] **步骤 6：Commit**

```bash
git add -A engine/
git commit -m "refactor(engine): remove present_options, replace with unified ask_user"
```

---

## 验收标准

- [ ] `ask_user(question, options?)` 工具已定义，`question` 必填、`options` 可选（2-6）
- [ ] 模型调用 `ask_user` 有 options → Run 结束，`Options` 挂载 方案A/B/C + 输入你的意见
- [ ] 模型调用 `ask_user` 无 options → Run 结束，`Blocked:true`、`BlockedBy:"awaiting_user"`、`Questions:[question]`
- [ ] `/confirm N` 选择 ask_user 选项 → history 注入"用户选择了：方案X: ..."；越界 → "无效方案编号"；`pendingAskUser` 清空
- [ ] 无待决问题时 `/confirm 1` → "按报告执行"（分析门控语义保留）
- [ ] 自由输入 → 清空 `pendingAskUser`，正常走模型循环
- [ ] `present_options` 全链路已删除（常量/函数/字段/测试），无残留引用
- [ ] sub-agent 不暴露 `ask_user`（`filterTools` allowList 天然隔离，零改动）
- [ ] `go build ./... && go test ./...` 全绿
