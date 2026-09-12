# 子 agent 工具化收敛 + ask_user 回界面 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 `handoff_to_agent` 从引擎特判拦截收敛为注册在 `tools.Registry` 的标准工具（合并主/子两套实现、动态化 depth 与 agent 枚举），并打通子 agent 调用 `ask_user` 回界面的轻量冒泡通道。

**架构：** 新增共享委托核心 `engine/handoff.go`（`runHandoff`）+ 双 backend 分发（Engine 与 SubAgentRunner 各实现 `RunSubAgent`）；新增 `tools/subagent.go` 的 `SubAgentTool` 注册为标准工具，按 `tc.Depth` 分发（0→Engine，>0→runner）；`ToolExecContext`/`tools.ToolContext` 扩展 Ctx/Depth/UserLang 透传；子 agent runLoop 拦截 `ask_user` 把问题冒泡到主引擎 `pendingAskUser`，复用现有 awaiting_user / Options 弹窗通道。

**技术栈：** Go，无新依赖。

---

## 关键语义约定（先读）

- **`tc.Depth` 含义 = 本次 handoff 产生的新子 agent 深度**（不是调用者深度）：
  - 主 agent 执行 handoff：`ToolExecContext.Depth = 0`（委派第一层）。
  - 子 agent 深度 `d` 执行 handoff（runLoop 内）：`ToolExecContext.Depth = d + 1`。
  - `SubAgentTool.Run` 分发：`tc.Depth == 0` → Engine backend；`tc.Depth > 0` → runner backend。
- **`RunSubAgent(ctx, params, depth, userLang)` 的 `depth` = 新子 agent 深度**，即 `tc.Depth`。
- **子 agent ask_user 冒泡只传问题文本（Options 降级为 nil）**：`HandoffResult.Questions []string` 只携带问题文本；主引擎收到后走 `awaiting_user` 自由输入路径（无弹窗）。子 agent 场景以"需要用户提供信息"为主，自由输入够用（YAGNI）。
- **嵌套冒泡**：runLoop 执行 handoff 后，若工具结果 `Questions` 非空，立即终止自身 run 并向上冒泡（`HandoffResult.Questions` 继承，`FinishReason = HandoffReasonAwaitingUser`）。
- **`maxSubAgentDepth` 常量删除**必须在 `SubAgentRunner.MaxDepth` 字段落地（任务 4）之后，避免编译断裂。

---

## 文件结构

**创建：**
- `engine/handoff.go` — 共享委托核心 `runHandoff` + `Engine.RunSubAgent` + `SubAgentRunner.RunSubAgent`（含主 agent state 注入、usage 累积、格式化）。
- `engine/handoff_test.go` — runHandoff / RunSubAgent 单测。
- `tools/subagent.go` — `SubAgentTool`（`tools.Tool` 实现：Spec 动态 enum、Run 深度分发、maxDepth 预检）。
- `tools/subagent_test.go` — Spec enum、深度分发、maxDepth 拒绝、Questions 透传。
- `docs/superpowers/plans/2026-09-12-subagent-tool-convergence.md` — 本文件。

**修改：**
- `engine/types.go` — `ToolExecContext` 加 `Ctx/Depth/UserLang`；`ToolResult` 加 `Questions`。
- `engine/agent.go` — `HandoffResult` 加 `Questions`；新增 `HandoffReasonAwaitingUser`；删除 `maxSubAgentDepth`（任务 4 时）。
- `engine/sub_agent.go` — `MaxDepth` 字段 + `SetMaxDepth` + `MaxDepth()`；runLoop 深度检查改用 `r.MaxDepth`；删嵌套特判改普通工具执行（`tc.Depth=input.Depth+1`）；`filterTools` 改造；`ask_user` 拦截 + 嵌套冒泡。
- `tools/registry.go` — `ToolContext` 加 `Ctx/Depth/UserLang`；`ToolResultEnvelope` 加 `FinishReason/Questions`。
- `tools/adapter.go` — 双向透传 Ctx/Depth/UserLang/FinishReason/Questions。
- `engine/turn.go` — `toolSpecsWithHandoff` 移除 handoffToolSpec 追加；handoff 执行改走 `e.tools.Execute`；删 `executeHandoff`/`executeHandoffsParallel`；progress switch 加 `handoff_to_agent`。
- `engine/loop.go` — `EngineDeps` 加 `AfterEngine` 钩子；`NewEngine` 末尾调用；plan 重放点 handoff 改走 `e.tools.Execute`；`isHandoffFollowUpReason` 加 `awaiting_user`。
- `cmd/run.go` — `AfterEngine` 钩子内注册 `SubAgentTool`。

---

## 任务 1：类型扩展（Ctx/Depth/UserLang/Questions 透传基础）

**文件：**
- 修改：`engine/types.go`、`engine/agent.go`、`tools/registry.go`、`tools/adapter.go`
- 测试：`tools/adapter_test.go`（新建）

- [ ] **步骤 1：编写失败的透传测试**

创建 `tools/adapter_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deepact/deepact/engine"
)

type passthroughTool struct{}

func (passthroughTool) Spec() ToolSpec {
	return ToolSpec{Name: "echo", Description: "echo", Parameters: json.RawMessage(`{}`)}
}

func (passthroughTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	return ToolResultEnvelope{
		Status:       StatusOK,
		Digest:       "ok",
		FinishReason: "completed",
		Questions:    []string{"问题？"},
	}, nil
}

// TestAdapter_PassthroughCtxAndQuestions verifies the EngineExecutor adapter
// forwards Ctx/Depth/UserLang into ToolContext and FinishReason/Questions
// back into ToolResult.
func TestAdapter_PassthroughCtxAndQuestions(t *testing.T) {
	reg := NewRegistry()
	reg.Register(passthroughTool{})
	exec := NewEngineExecutor(reg)

	ctx := context.WithValue(context.Background(), "k", "v")
	results := exec.Execute(engine.ToolExecContext{
		WorkDir: "/tmp", SessionID: "s1", TurnNumber: 3,
		Ctx: ctx, Depth: 1, UserLang: "中文",
	}, []engine.ToolCallRequest{{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FinishReason != "completed" {
		t.Errorf("FinishReason = %q, want completed", results[0].FinishReason)
	}
	if len(results[0].Questions) != 1 || results[0].Questions[0] != "问题？" {
		t.Errorf("Questions = %v, want [问题？]", results[0].Questions)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./tools/ -run TestAdapter_PassthroughCtxAndQuestions -v`
预期：FAIL——`ToolExecContext` / `ToolResultEnvelope` / `ToolResult` 尚无 Ctx/Depth/UserLang/FinishReason/Questions 字段，编译失败。

- [ ] **步骤 3：扩展 engine 侧类型**

`engine/types.go` 的 `ToolExecContext`（当前第 179 行）：

```go
type ToolExecContext struct {
	WorkDir    string
	SessionID  string
	TurnNumber int
	// Ctx propagates the run's cancellation signal into tools (sub-agent
	// delegation needs it to cancel child runs when the parent cancels).
	Ctx context.Context
	// Depth is the depth of the NEW sub-agent this handoff produces:
	// 0 = main agent delegating the first level; d+1 = a sub-agent at depth d.
	Depth int
	// UserLang is the session language ("中文" or "") for localized tool output.
	UserLang string
}
```

`engine/types.go` 的 `ToolResult`（当前第 191 行）末尾加：

```go
	// Questions carries ask_user questions bubbled up from a sub-agent so the
	// parent engine can present them via the awaiting_user path.
	Questions []string `json:"questions,omitempty"`
```

`engine/types.go` 需补 `context` import。

`engine/agent.go` 的 `HandoffResult`（当前第 65 行）末尾加：

```go
	// Questions holds ask_user questions a sub-agent asked before ending.
	// Bubbles up through the tool result to the parent engine.
	Questions []string `json:"questions,omitempty"`
```

`engine/agent.go` 的 FinishReason 常量块（当前第 28 行附近）加：

```go
	HandoffReasonAwaitingUser = "awaiting_user" // sub-agent asked the user; parent must present the question
```

- [ ] **步骤 4：扩展 tools 侧类型**

`tools/registry.go` 的 `ToolContext`（当前第 16 行）：

```go
type ToolContext struct {
	WorkDir     string
	SessionID   string
	TurnNumber  int
	ArtifactDir string // base directory for artifact store (e.g., ~/.deepact/artifacts)
	// Ctx/Depth/UserLang mirror engine.ToolExecContext — see engine/types.go.
	Ctx      context.Context
	Depth    int
	UserLang string
}
```

`tools/registry.go` 的 `ToolResultEnvelope`（当前第 23 行）末尾加：

```go
	FinishReason string   `json:"finish_reason,omitempty"`
	Questions    []string `json:"questions,omitempty"`
```

`tools/registry.go` 需补 `context` import。

- [ ] **步骤 5：adapter 双向透传**

`tools/adapter.go`：

```go
func (e *EngineExecutor) Execute(ctx engine.ToolExecContext, calls []engine.ToolCallRequest) []engine.ToolResult {
	if e == nil || e.exec == nil {
		return nil
	}
	toolCtx := ToolContext{
		WorkDir: ctx.WorkDir, SessionID: ctx.SessionID, TurnNumber: ctx.TurnNumber,
		ArtifactDir: e.ArtifactDir, Ctx: ctx.Ctx, Depth: ctx.Depth, UserLang: ctx.UserLang,
	}
	toolCalls := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		toolCalls = append(toolCalls, ToolCall{ID: call.ID, Name: call.Name, Input: call.Input})
	}
	results := e.exec.Execute(toolCtx, toolCalls)
	engineResults := make([]engine.ToolResult, 0, len(results))
	for _, result := range results {
		engineResults = append(engineResults, engine.ToolResult{
			ToolCallID:  result.ToolCallID,
			ToolName:    result.ToolName,
			Status:      result.Status,
			Digest:      result.Digest,
			ArtifactRef: result.ArtifactRef,
			ExitCode:    result.ExitCode,
			FinishReason: result.FinishReason,
			Questions:    result.Questions,
		})
	}
	return engineResults
}
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./tools/ -run TestAdapter_PassthroughCtxAndQuestions -v`
预期：PASS。再跑 `go test ./tools/ ./engine/` 确认无回归。

- [ ] **步骤 7：Commit**

```bash
git add tools/adapter_test.go tools/adapter.go tools/registry.go engine/types.go engine/agent.go
git commit -m "feat: extend tool context/envelope with Ctx Depth UserLang Questions"
```

---

## 任务 2：共享委托核心 engine/handoff.go

**文件：**
- 创建：`engine/handoff.go`
- 测试：`engine/handoff_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/handoff_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

type mockAgentForHandoff struct {
	id     AgentID
	result *HandoffResult
	err    error
}

func (m *mockAgentForHandoff) ID() AgentID { return m.id }
func (m *mockAgentForHandoff) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "mock"}
}
func (m *mockAgentForHandoff) Run(_ context.Context, _ Handoff) (*HandoffResult, error) {
	return m.result, m.err
}

func TestRunSubAgent_EngineBackend_Completed(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "分析完成", FinishReason: HandoffReasonCompleted,
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "分析 X",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if !strings.Contains(res.Digest, "分析完成") {
		t.Errorf("Digest = %q, want contain 分析完成", res.Digest)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Errorf("FinishReason = %q, want completed", res.FinishReason)
	}
}

func TestRunSubAgent_EngineBackend_AgentNotFound(t *testing.T) {
	e := &Engine{agents: NewAgentRegistry(), isChinese: true, state: &TaskState{}}
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "nope", Goal: "x",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("Status = %q, want error", res.Status)
	}
}

func TestRunSubAgent_EngineBackend_NilRegistry(t *testing.T) {
	e := &Engine{isChinese: true, state: &TaskState{}}
	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "x",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "error" || res.Digest != "no agent registry configured" {
		t.Errorf("got %q %q, want error no agent registry configured", res.Status, res.Digest)
	}
}

func TestRunSubAgent_EngineBackend_BubblesQuestions(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "需要信息", FinishReason: HandoffReasonAwaitingUser,
		Questions: []string{"数据库连接串是什么？"},
	}})
	e := &Engine{agents: reg, isChinese: true, state: &TaskState{}}

	res, err := e.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "配置 DB",
	}, 0, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", res.FinishReason)
	}
	if len(res.Questions) != 1 || res.Questions[0] != "数据库连接串是什么？" {
		t.Errorf("Questions = %v", res.Questions)
	}
	if e.pendingAskUser == nil || e.pendingAskUser.Question != "数据库连接串是什么？" {
		t.Errorf("pendingAskUser = %+v, want question set", e.pendingAskUser)
	}
}

func TestSubAgentRunnerRunSubAgent_Nested(t *testing.T) {
	reg := NewAgentRegistry()
	reg.Register(&mockAgentForHandoff{id: AgentSub, result: &HandoffResult{
		Summary: "嵌套完成", FinishReason: HandoffReasonCompleted,
	}})
	runner := &SubAgentRunner{model: &stubCompleteModel{resp: "ok"}, tools: stubToolExecutor{}, registry: reg, modelName: "test"}

	res, err := runner.RunSubAgent(context.Background(), HandoffToAgentParams{
		Agent: "sub", Goal: "子任务",
	}, 1, "中文")
	if err != nil {
		t.Fatalf("RunSubAgent error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("Status = %q, want ok", res.Status)
	}
	if res.Digest != "Agent completed:\n嵌套完成" {
		t.Errorf("Digest = %q, want Agent completed digest", res.Digest)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestRunSubAgent|TestSubAgentRunnerRunSubAgent' -v`
预期：FAIL——`RunSubAgent` 未定义。

- [ ] **步骤 3：编写 engine/handoff.go 核心**

创建 `engine/handoff.go`：

```go
package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

// runHandoff is the shared delegation core used by both backends.
// opts carries per-caller differences: how to resolve the agent registry,
// how to inject parent context, whether to accumulate usage, and how to
// localize output.
type handoffOptions struct {
	// resolve returns the target agent, or an error digest if unavailable.
	resolve func(AgentID) (Agent, error)
	// injectParentCtx optionally appends parent working context to params.Context.
	injectParentCtx func(params *HandoffToAgentParams)
	// accumulate reports sub-agent usage into the parent's counter.
	accumulate func(*ModelUsage)
	// zh localizes the result digest.
	zh bool
	// depth is the depth of the new sub-agent run (0 = first level).
	depth int
	// userLang is the session language ("中文" or "").
	userLang string
}

func runHandoff(ctx context.Context, call ToolCallRequest, opts handoffOptions) ToolResult {
	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := opts.resolve(AgentID(params.Agent))
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent not found: %s - %v", params.Agent, err),
		}
	}

	if opts.injectParentCtx != nil {
		opts.injectParentCtx(&params)
	}

	handoff := Handoff{
		Agent:          AgentID(params.Agent),
		Goal:           params.Goal,
		Context:        params.Context,
		Tools:          params.Tools,
		Constraints:    params.Constraints,
		ExpectedOutput: params.ExpectedOutput,
		Depth:          opts.depth,
		UserLanguage:   opts.userLang,
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent error: %v", err),
		}
	}

	if result.Usage != nil && opts.accumulate != nil {
		opts.accumulate(result.Usage)
	}

	status := "ok"
	if result.BlockedBy == "cancelled" {
		status = "cancelled"
	}
	return ToolResult{
		ToolCallID:   call.ID,
		ToolName:     HandoffToolName,
		Status:       status,
		Digest:       formatHandoffResult(result, opts.zh),
		FinishReason: result.FinishReason,
		Questions:    result.Questions,
	}
}

// RunSubAgent implements the main-agent backend for SubAgentTool: depth 0.
func (e *Engine) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang string) (ToolResult, error) {
	if e.agents == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	userLang = ""
	if e.isChinese {
		userLang = "中文"
	}
	res := runHandoff(ctx, call, handoffOptions{
		resolve: func(id AgentID) (Agent, error) { return e.agents.Get(id) },
		injectParentCtx: func(p *HandoffToAgentParams) {
			if e.state != nil {
				*p = injectMainAgentContext(*p, e.state)
			}
		},
		accumulate: e.accumulateUsage,
		zh:         e.isChinese,
		depth:      depth,
		userLang:   userLang,
	})
	// Bubble up sub-agent questions into the pending ask_user seam so the
	// existing awaiting_user / Options UI path presents them.
	if len(res.Questions) > 0 {
		e.pendingAskUser = &AskUserRequest{Question: res.Questions[0]}
	}
	return res, nil
}

// RunSubAgent implements the nested-agent backend for SubAgentTool: depth > 0.
func (r *SubAgentRunner) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang string) (ToolResult, error) {
	if r.registry == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	res := runHandoff(ctx, call, handoffOptions{
		resolve: func(id AgentID) (Agent, error) { return r.registry.Get(id) },
		accumulate: func(u *ModelUsage) {
			if r.onProgress != nil {
				// nested usage is folded into the parent's own usage already
			}
		},
		zh:       zhFromLang(userLang),
		depth:    depth,
		userLang: userLang,
	})
	return res, nil
}

// injectMainAgentContext appends the main agent's working set, memory markers,
// and modified files to the handoff context (moved from Engine.executeHandoff).
func injectMainAgentContext(params HandoffToAgentParams, state *TaskState) HandoffToAgentParams {
	extra := mainAgentContextText(state)
	if extra == "" {
		return params
	}
	if params.Context != "" {
		params.Context = params.Context + extra
	} else {
		params.Context = extra
	}
	return params
}

// mainAgentContextText builds the working-set/markers/modified-files block.
func mainAgentContextText(state *TaskState) string {
	var sb strings.Builder
	if len(state.WorkingSet.Files) > 0 {
		sb.WriteString("\n## Main Agent Context (Review Starting Point)\n")
		sb.WriteString("The main agent examined these files. Re-examine them from your own perspective:\n")
		for _, f := range state.WorkingSet.Files {
			sb.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Notes))
		}
	}
	if len(state.MemoryMarkers) > 0 {
		sb.WriteString("\nKey findings from the main agent (review for blind spots):\n")
		for _, m := range state.MemoryMarkers {
			sb.WriteString(fmt.Sprintf("  • %s\n", m))
		}
	}
	if len(state.ModifiedFiles) > 0 {
		sb.WriteString("\nFiles modified so far:\n")
		for _, f := range state.ModifiedFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
	}
	return sb.String()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
```

需要补 `strings` import；`zhFromLang` 已存在于 `prompts_i18n.go`。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestRunSubAgent|TestSubAgentRunnerRunSubAgent' -v`
预期：PASS（5 个测试：Engine backend 的 Completed / AgentNotFound / NilRegistry / BubblesQuestions + runner 的 Nested）。

- [ ] **步骤 5：Commit**

```bash
git add engine/handoff.go engine/handoff_test.go
git commit -m "feat: shared handoff core with Engine/SubAgentRunner RunSubAgent backends"
```

---

## 任务 3：tools/subagent.go SubAgentTool

**文件：**
- 创建：`tools/subagent.go`
- 测试：`tools/subagent_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `tools/subagent_test.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestSubAgentTool_Spec_DynamicEnum(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func() []string { return []string{"sub", "team-lead"} },
		2,
	)
	spec := tool.Spec()
	var params struct {
		Properties struct {
			Agent struct {
				Enum []string `json:"enum"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Parameters, &params); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(params.Properties.Agent.Enum) != 2 || params.Properties.Agent.Enum[1] != "team-lead" {
		t.Errorf("enum = %v, want [sub team-lead]", params.Properties.Agent.Enum)
	}
}

func TestSubAgentTool_Run_DepthDispatch(t *testing.T) {
	var mainCalled, nestedCalled bool
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			mainCalled = true
			return engine.ToolResult{Status: "ok", Digest: "main"}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			nestedCalled = true
			return engine.ToolResult{Status: "ok", Digest: "nested"}, nil
		},
		func() []string { return []string{"sub"} },
		3, // maxDepth=3 so depth 2 still dispatches to nested
	)

	// depth 0 → main backend
	_, _ = tool.Run(ToolContext{Depth: 0, UserLang: "中文", Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if !mainCalled || nestedCalled {
		t.Errorf("depth 0: mainCalled=%v nestedCalled=%v, want main only", mainCalled, nestedCalled)
	}

	// depth 2 (maxDepth=3) → nested backend
	mainCalled = false
	nestedCalled = false
	_, _ = tool.Run(ToolContext{Depth: 2, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if mainCalled || !nestedCalled {
		t.Errorf("depth 2: mainCalled=%v nestedCalled=%v, want nested backend only", mainCalled, nestedCalled)
	}
}

func TestSubAgentTool_Run_QuestionsPassthrough(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{Status: "ok", Digest: "d", Questions: []string{"Q?"}}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{}, nil
		},
		func() []string { return []string{"sub"} },
		2,
	)
	env, err := tool.Run(ToolContext{Depth: 0, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(env.Questions) != 1 || env.Questions[0] != "Q?" {
		t.Errorf("Questions = %v, want [Q?]", env.Questions)
	}
}

func TestSubAgentTool_Run_MaxDepthRejected(t *testing.T) {
	tool := NewSubAgentTool(
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			t.Fatal("main backend must not be called at depth > 0")
			return engine.ToolResult{}, nil
		},
		func(ctx context.Context, p engine.HandoffToAgentParams, d int, l string) (engine.ToolResult, error) {
			return engine.ToolResult{Status: "ok", Digest: "nested"}, nil
		},
		func() []string { return []string{"sub"} },
		2,
	)
	env, err := tool.Run(ToolContext{Depth: 3, Ctx: context.Background()},
		json.RawMessage(`{"agent":"sub","goal":"g"}`))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if env.Status != "error" {
		t.Errorf("Status = %q, want error (max depth)", env.Status)
	}
	if !strings.Contains(env.Digest, "Max nesting depth") {
		t.Errorf("Digest = %q, want contain Max nesting depth", env.Digest)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./tools/ -run 'TestSubAgentTool' -v`
预期：FAIL——`SubAgentTool` / `NewSubAgentTool` 未定义。

- [ ] **步骤 3：编写 tools/subagent.go**

创建 `tools/subagent.go`：

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepact/deepact/engine"
)

// SubAgentBackend runs one handoff delegation. depth is the depth of the NEW
// sub-agent (0 = first level). userLang is the session language ("中文" or "").
type SubAgentBackend func(ctx context.Context, params engine.HandoffToAgentParams, depth int, userLang string) (engine.ToolResult, error)

// SubAgentTool is the model-facing handoff_to_agent tool registered in the
// standard tool registry. It dispatches by ToolContext.Depth:
//   - depth == 0 → main backend (Engine.RunSubAgent): first-level delegation.
//   - depth > 0  → nested backend (SubAgentRunner.RunSubAgent): deeper nesting.
type SubAgentTool struct {
	main    SubAgentBackend
	nested  SubAgentBackend
	agents  func() []string
	maxDepth int
}

// NewSubAgentTool constructs the tool. agents returns the current registered
// agent IDs for the dynamic enum; maxDepth caps nesting (0 forbids delegation).
func NewSubAgentTool(main, nested SubAgentBackend, agents func() []string, maxDepth int) *SubAgentTool {
	return &SubAgentTool{main: main, nested: nested, agents: agents, maxDepth: maxDepth}
}

func (t *SubAgentTool) Spec() ToolSpec {
	agents := []string{"sub"}
	if t.agents != nil {
		agents = t.agents()
		if len(agents) == 0 {
			agents = []string{"sub"}
		}
	}
	enumJSON, err := json.Marshal(agents)
	if err != nil {
		enumJSON = []byte(`["sub"]`)
	}
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"agent": {"type": "string", "enum": %s,
				"description": "Target agent (sub = generic; other registered agents as available)"},
			"goal": {"type": "string", "description": "What the agent should accomplish"},
			"context": {"type": "string", "description": "Relevant context for the sub-agent"},
			"tools": {"type": "array", "items": {"type": "string"},
				"description": "Tools the sub-agent is allowed to use (optional)"},
			"constraints": {"type": "array", "items": {"type": "string"},
				"description": "Constraints for the sub-agent (optional)"},
			"expected_output": {"type": "string",
				"description": "What a successful result looks like — acceptance criteria (optional)"}
		},
		"required": ["agent", "goal"]
	}`, enumJSON)
	return ToolSpec{
		Name:        engine.HandoffToolName,
		Description: "Delegate a sub-task to a specialized agent. Sub-agents can research code, brainstorm solutions, or critically review decisions.",
		Parameters:  json.RawMessage(params),
	}
}

func (t *SubAgentTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	if t.maxDepth > 0 && ctx.Depth > t.maxDepth {
		return ToolResultEnvelope{
			Status: StatusError,
			Digest: fmt.Sprintf("Max nesting depth (%d) reached. Cannot delegate further.", t.maxDepth),
		}, nil
	}
	var params engine.HandoffToAgentParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResultEnvelope{
			Status: StatusError,
			Digest: fmt.Sprintf("invalid handoff params: %v", err),
		}, nil
	}
	var res engine.ToolResult
	var err error
	if ctx.Depth == 0 {
		res, err = t.main(ctx.Ctx, params, 0, ctx.UserLang)
	} else {
		res, err = t.nested(ctx.Ctx, params, ctx.Depth, ctx.UserLang)
	}
	if err != nil {
		return ToolResultEnvelope{Status: StatusError, Digest: err.Error()}, nil
	}
	return ToolResultEnvelope{
		ToolCallID:  res.ToolCallID,
		ToolName:    res.ToolName,
		Status:      res.Status,
		Digest:      res.Digest,
		ArtifactRef: res.ArtifactRef,
		ExitCode:    res.ExitCode,
		FinishReason: res.FinishReason,
		Questions:    res.Questions,
	}, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./tools/ -run 'TestSubAgentTool' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add tools/subagent.go tools/subagent_test.go
git commit -m "feat: SubAgentTool as registered handoff_to_agent tool with depth dispatch"
```

---

## 任务 4：sub_agent.go 改造（MaxDepth / filterTools / 删特判 / ask_user 拦截 / 嵌套冒泡）

**文件：**
- 修改：`engine/sub_agent.go`、`engine/agent.go`（删 `maxSubAgentDepth`）
- 测试：`engine/sub_agent_askuser_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/sub_agent_askuser_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// TestSubAgentAskUser_BubblesAndEnds verifies a sub-agent calling ask_user:
// the run ends with awaiting_user, and the questions bubble into the result.
func TestSubAgentAskUser_BubblesAndEnds(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "ask1", Function: ModelFunctionCall{Name: AskUserToolName, Arguments: `{"question":"选哪个缓存？","options":["Redis","MySQL"]}`}},
		}}},
	}}
	runner := &SubAgentRunner{
		model: model, tools: stubToolExecutor{}, modelName: "test",
		registry: NewAgentRegistry(),
	}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", result.FinishReason)
	}
	if len(result.Questions) != 1 || result.Questions[0] != "选哪个缓存？" {
		t.Errorf("Questions = %v, want [选哪个缓存？]", result.Questions)
	}
}

// TestSubAgentNestedBubble verifies a sub-agent receiving a handoff result
// carrying Questions terminates and bubbles them up.
func TestSubAgentNestedBubble(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{
			{ID: "h1", Function: ModelFunctionCall{Name: HandoffToolName, Arguments: `{"agent":"sub","goal":"子任务"}`}},
		}}},
	}}
	// questionExecutor simulates the registered SubAgentTool returning a
	// handoff result that carried questions from a deeper child.
	exec := &questionExecutor{questions: []string{"数据库连接串是什么？"}}
	runner := &SubAgentRunner{model: model, tools: exec, modelName: "test", registry: NewAgentRegistry()}
	runner.SetMaxDepth(2)

	result, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", MaxIterations: 5})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.FinishReason != HandoffReasonAwaitingUser {
		t.Errorf("FinishReason = %q, want awaiting_user", result.FinishReason)
	}
	if len(result.Questions) != 1 || result.Questions[0] != "数据库连接串是什么？" {
		t.Errorf("Questions = %v, want [数据库连接串是什么？]", result.Questions)
	}
}

// questionExecutor returns a handoff result carrying the given questions.
type questionExecutor struct {
	questions []string
}

func (q *questionExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolResult{
			ToolCallID: c.ID, ToolName: c.Name, Status: "ok",
			Digest: "child asked a question", FinishReason: HandoffReasonAwaitingUser,
			Questions: q.questions,
		})
	}
	return out
}

func (q *questionExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: HandoffToolName}}}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestSubAgentAskUser|TestSubAgentNestedBubble' -v`
预期：FAIL——runLoop 尚无 ask_user 拦截（ask_user 会落到普通工具执行 → "tool not found" 错误，不会 awaiting_user）。

- [ ] **步骤 3：改造 sub_agent.go**

**3a. 加 MaxDepth 字段与访问器**（`SubAgentRunner` struct 末尾，`sub_agent.go` 第 34 行附近）：

```go
	maxDepth int // absolute delegation-depth cap; 0 = default 2
```

在 `SetSubAgentBaseURL` 附近加：

```go
// SetMaxDepth caps how deep sub-agent nesting may go. 0 resets to the default (2).
func (r *SubAgentRunner) SetMaxDepth(d int) {
	if d <= 0 {
		d = 2
	}
	r.maxDepth = d
}

// MaxDepth returns the current nesting cap.
func (r *SubAgentRunner) MaxDepth() int {
	if r.maxDepth <= 0 {
		return 2
	}
	return r.maxDepth
}
```

**3b. runLoop 深度检查改用 r.MaxDepth**（`sub_agent.go` 第 157 行）：

```go
	if input.Depth > r.MaxDepth() {
		return &HandoffResult{
			Summary:      fmt.Sprintf("Max agent nesting depth (%d) exceeded. Cannot delegate further.", r.MaxDepth()),
			Blocked:      true,
			BlockedBy:    "max_depth",
			FinishReason: HandoffReasonMaxDepth,
		}, nil
	}
```

**3c. filterTools 改造**（`sub_agent.go` 第 703 行，替换整个函数）：

```go
// filterTools returns a tool spec list filtered to the allowed tools. The
// handoff_to_agent and ask_user tools are ALWAYS included — delegation and
// user-questions are core sub-agent capabilities that an allowList must not
// strip (the old code always prepended handoff; /debate and /collab pass a
// read-only allowList but their members still need to delegate and ask).
// The constructed specs are used for both so the registry copy (if present)
// is not duplicated.
func (r *SubAgentRunner) filterTools(allowList []string, userLang string) []ModelTool {
	all := r.tools.Specs()
	result := []ModelTool{handoffToolSpec(zhFromLang(userLang))}
	result = append(result, askUserToolSpec(zhFromLang(userLang)))

	if len(allowList) == 0 {
		for _, spec := range all {
			if spec.Function.Name == HandoffToolName || spec.Function.Name == AskUserToolName {
				continue // already added above
			}
			result = append(result, spec)
		}
		return result
	}

	allowSet := make(map[string]bool, len(allowList))
	for _, name := range allowList {
		allowSet[name] = true
	}
	for _, spec := range all {
		if spec.Function.Name == HandoffToolName || spec.Function.Name == AskUserToolName {
			continue // already added above
		}
		if allowSet[spec.Function.Name] {
			result = append(result, spec)
		}
	}
	return result
}
```

**3d. 删除嵌套特判，handoff 走普通工具执行；新增 ask_user 拦截与嵌套冒泡**（`sub_agent.go` 第 528 行起的 `for _, call := range calls` 循环，替换为）：

```go
		for _, call := range calls {
			if r.onProgress != nil {
				r.onProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, r.workDir)})
			}
			// ask_user: the sub-agent needs user input. Validate, record a tool
			// response, then end the run with awaiting_user so the questions
			// bubble to the parent engine.
			if call.Name == AskUserToolName {
				q, ok := parseAskUserInput(call.Input)
				if !ok {
					history = append(history, ModelMessage{
						Role: "tool", ToolCallID: call.ID,
						Content: "Error: ask_user requires a non-empty question (options 2-6 when provided).",
					})
					continue
				}
				history = append(history, ModelMessage{
					Role: "tool", ToolCallID: call.ID,
					Content: "✓ 已记录问题，等待用户回答。",
				})
				return &HandoffResult{
					Summary:      q.Question,
					Questions:    []string{q.Question},
					FinishReason: HandoffReasonAwaitingUser,
					Usage:        &totalUsage,
				}, nil
			}
			env := ToolExecContext{WorkDir: r.workDir, SessionID: r.sessionID, Ctx: ctx, Depth: input.Depth + 1, UserLang: input.UserLanguage}
			results := r.tools.Execute(env, []ToolCallRequest{call})
			if len(results) > 0 {
				res := results[0]
				if r.onProgress != nil {
					r.onProgress(ProgressEvent{Type: "tool_done", Name: res.ToolName, Detail: briefDigest(res.Digest), FullDetail: res.Digest})
				}
				// Nested bubble: a child's handoff result carried questions →
				// stop and bubble them up.
				if len(res.Questions) > 0 {
					return &HandoffResult{
						Summary:      res.Digest,
						Questions:    res.Questions,
						FinishReason: HandoffReasonAwaitingUser,
						Usage:        &totalUsage,
					}, nil
				}
				// cancelled results are not written to history: the run is
				// unwinding (ctx cancelled) and no further LLM call will follow.
				if res.Status != "cancelled" {
					history = append(history, ModelMessage{
						Role: "tool", ToolCallID: res.ToolCallID, Content: res.Digest,
					})
				}
			}
		}
```

**3e. 新增 parseAskUserInput 辅助**（`sub_agent.go` 文件末尾）：

```go
// parseAskUserInput validates an ask_user call input and returns the question.
// Options are accepted for validation but bubbled as nil (YAGNI: the parent
// presents via the awaiting_user free-input path).
func parseAskUserInput(input json.RawMessage) (struct{ Question string }, bool) {
	var p struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(input, &p); err != nil || strings.TrimSpace(p.Question) == "" {
		return struct{ Question string }{}, false
	}
	if len(p.Options) > 0 && (len(p.Options) < 2 || len(p.Options) > 6) {
		return struct{ Question string }{}, false
	}
	for _, o := range p.Options {
		if strings.TrimSpace(o) == "" {
			return struct{ Question string }{}, false
		}
	}
	return struct{ Question string }{Question: p.Question}, true
}
```

**3f. 删除 `executeSubHandoff`**（`sub_agent.go` 第 724-778 行整段删除）与 `engine/agent.go` 第 159 行 `const maxSubAgentDepth = 2`。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestSubAgentAskUser|TestSubAgentNestedBubble' -v`
预期：PASS（ask_user 拦截 → awaiting_user + Questions 冒泡；嵌套 handoff 收到带 Questions 的结果 → 立即终止冒泡）。

再跑：`go test ./engine/ -run 'TestRunSubAgent|TestSubAgentRunnerRunSubAgent' -v`
预期：PASS（任务 2 的 backend 测试在 MaxDepth 落地后仍通过）。

再跑全量：`go test ./engine/`
预期：PASS（含 `sub_agent_finish_test.go` 等现有测试——`questionExecutor.Specs()` 只返回 handoff，filterTools 构造 handoff+ask_user，行为保持）。

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent.go engine/agent.go engine/sub_agent_askuser_test.go
git commit -m "feat: sub-agent ask_user bubble-up, dynamic MaxDepth, unified tool dispatch"
```

---

## 任务 5：cmd/run.go 组装 + EngineDeps.AfterEngine 钩子 + toolSpecsWithHandoff 移除追加

**文件：**
- 修改：`engine/loop.go`（EngineDeps + NewEngine）、`engine/turn.go`（toolSpecsWithHandoff）、`cmd/run.go`
- 测试：编译 + 现有测试

- [ ] **步骤 1：EngineDeps 加 AfterEngine 钩子**

`engine/loop.go` 第 31-42 行 `EngineDeps` 末尾加：

```go
	// AfterEngine, when set, is called at the end of NewEngine with the new
	// Engine instance. Used by cmd/run.go to register tools that need a live
	// Engine reference (e.g. the SubAgentTool backends).
	AfterEngine func(*Engine)
```

`engine/loop.go` `NewEngine`（第 163-207 行）return 前加：

```go
	if deps.AfterEngine != nil {
		deps.AfterEngine(e)
	}
```

- [ ] **步骤 2：cmd/run.go 注册 SubAgentTool**

`cmd/run.go` `buildEngineDeps` 中 `deps` 构造（第 312 行）后加：

```go
	deps.AfterEngine = func(e *engine.Engine) {
		registry.Register(tools.NewSubAgentTool(
			func(ctx context.Context, p engine.HandoffToAgentParams, depth int, lang string) (engine.ToolResult, error) {
				return e.RunSubAgent(ctx, p, depth, lang)
			},
			func(ctx context.Context, p engine.HandoffToAgentParams, depth int, lang string) (engine.ToolResult, error) {
				return runner.RunSubAgent(ctx, p, depth, lang)
			},
			func() []string {
				specs := agentReg.AgentSpecs()
				ids := make([]string, 0, len(specs))
				for _, s := range specs {
					ids = append(ids, string(s.ID))
				}
				return ids
			},
			runner.MaxDepth(),
		))
	}
```

`cmd/run.go` 需补 `context` import（若未导入）。

- [ ] **步骤 3：toolSpecsWithHandoff 移除 handoffToolSpec 追加**

`engine/turn.go` 第 694-703 行：

```go
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, loadSkillToolSpec())
	specs = append(specs, taskCompleteToolSpec(e.isChinese))
	specs = append(specs, todoWriteToolSpec())
	specs = append(specs, askUserToolSpec(e.isChinese))
	return specs
}
```

（handoff_to_agent 已由注册的 SubAgentTool 提供，经 `e.tools.Specs()` 自然包含；移除手写追加避免同名 spec 重复。）

- [ ] **步骤 4：编译 + 测试**

运行：`go build ./...`
预期：编译通过。

运行：`go test ./engine/ ./tools/ ./cmd/`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/turn.go cmd/run.go
git commit -m "feat: register SubAgentTool via AfterEngine hook, drop duplicated tool spec"
```

---

## 任务 6：turn.go + loop.go 改造（handoff 执行走工具通道）

**文件：**
- 修改：`engine/turn.go`、`engine/loop.go`
- 测试：编译 + 现有测试

- [ ] **步骤 1：turn.go handoff 执行改走 tools.Execute**

`engine/turn.go` 第 501-508 行替换为：

```go
	// Execute handoff calls through the registered SubAgentTool.
	if len(handoffCalls) > 0 {
		userLang := ""
		if e.isChinese {
			userLang = "中文"
		}
		// agent_start events for UI.
		for _, call := range handoffCalls {
			var params HandoffToAgentParams
			if err := json.Unmarshal(call.Input, &params); err == nil && e.config.OnProgress != nil {
				name := params.Agent
				if name == "" {
					name = "sub"
				}
				e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: name, Detail: params.Goal})
			}
		}
		execCtx := ToolExecContext{
			WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber,
			Ctx: ctx, Depth: 0, UserLang: userLang,
		}
		results := e.tools.Execute(execCtx, handoffCalls)
		// agent_done events for UI.
		for _, r := range results {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: r.ToolName, Detail: briefDigest(r.Digest)})
			}
		}
		msgs := e.processHandoffResults(handoffCalls, results)
		for _, msg := range msgs {
			e.history = append(e.history, msg)
		}
	}
```

- [ ] **步骤 2：loop.go plan 重放点改走 tools.Execute**

`engine/loop.go` 第 526-533 行替换为：

```go
		// Execute handoff calls through the registered SubAgentTool.
		if len(handoffCalls) > 0 {
			execCtx := ToolExecContext{
				WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber,
				Ctx: ctx, Depth: 0,
			}
			results := e.tools.Execute(execCtx, handoffCalls)
			for i := range handoffCalls {
				result := results[i]
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
		}
```

（plan 重放点不设 pendingAskUser——Engine.RunSubAgent 内部已设。）

- [ ] **步骤 3：删除 executeHandoff / executeHandoffsParallel**

删除 `engine/turn.go` 第 705-818 行（`executeHandoff`）与第 820-893 行（`executeHandoffsParallel`）。两者在任务 6 步骤 1/2 替换后已无引用。

- [ ] **步骤 4：编译 + 测试**

运行：`go build ./...`
预期：编译通过（`executeHandoffsParallel` 已无引用）。

运行：`go test ./engine/ ./tools/ ./cmd/`
预期：PASS。注意 `turn_load_skill_test.go` 的 `TestProcessHandoffResults_*` 直接调用 `processHandoffResults`（保留未删），不受影响。

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/loop.go
git commit -m "refactor: handoff execution through registered tool, drop engine special-case"
```

---

## 任务 7：端到端验证

**文件：** 无（验证）

- [ ] **步骤 1：全量构建 + 测试（race）**

运行：`go build ./... && go test ./... -race -count=1`
预期：全部 PASS。

- [ ] **步骤 2：手动冒烟（可选）**

运行：`go run . exec "用子代理调研 engine/handoff.go 的改动，然后总结"`（如 CLI 支持 exec 模式）
预期：子代理正常启动、执行、返回 digest，主 agent 收到后继续。

- [ ] **步骤 3：验证嵌套冒泡路径**

在代码评审中人工检查：主 agent → 子 agent → ask_user 时，问题以 awaiting_user 呈现；用户回答后主 agent 重新委派。

---

## 自检记录

- **规格覆盖度**：LLM 侧工具化（任务 3/5/6）、合并两套 handoff（任务 2）、动态化 depth 与 agent 枚举（任务 3/4）、ask_user 冒泡（任务 4/2）、嵌套冒泡（任务 4）、复用 UI 通道（任务 2 `pendingAskUser`）、/debate /collab 不动（无任务——符合规格"不做"）。
- **占位符扫描**：无"待定/TODO"；所有代码块含完整实现。
- **类型一致性**：`RunSubAgent(ctx, params, depth, userLang)` 签名在任务 2/3/5 一致；`tc.Depth` 语义（新子 agent 深度）在任务 1/3/6 一致；`HandoffReasonAwaitingUser` 在任务 1/2/4 一致。
- **执行顺序**：任务 5（注册 SubAgentTool）先于任务 6（turn.go/loop.go 改走工具通道）——避免真实运行 handoff 报 "tool not found" 的中间断裂态。
