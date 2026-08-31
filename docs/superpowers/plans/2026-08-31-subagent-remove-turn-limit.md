# 移除 Subagent 默认轮次上限 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 移除 subagent 的默认 99 轮上限——`MaxIterations=0`（未指定）不再回退到 99，改为无上限；保留显式指定上限的能力（critic 15 轮、roundtable 3/15 轮不变）。调研型 subagent 不再被"子代理超出轮次上限"截断。

**架构：** 删除 `maxSubAgentIterations = 99` 常量，`Run`/`RunWithPrompt`/`specialistAgent.Run` 三处去掉"0→99"回退，`runLoop` 循环条件改为 `maxIterations <= 0 || iter < maxIterations`（0=无上限）。`MaxIterations > 0` 的显式上限路径与尾部 `max_iterations` 兜底分支完全不变。既有安全守卫（纯文本 3 击、同文件循环检测、120s 调用超时、输出截断 3 击）不新增不删除。规格见 `docs/superpowers/specs/2026-08-31-subagent-remove-turn-limit-design.md`。

**技术栈：** Go 1.24+，`engine` 包（`SubAgentRunner.runLoop` / `AgentSpec` / `Handoff`），`go test ./engine/`。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/sub_agent_remove_turn_limit_test.go` | 回归测试：`MaxIterations=0` 可跑 >99 轮并经 submit_result 正常完成 | 创建 |
| `engine/sub_agent.go` | 删除 `maxSubAgentIterations` 常量；`Run`/`RunWithPrompt` 去掉默认回退；`runLoop` 循环条件放开 | 修改（4 处） |
| `engine/default_agents.go` | `specialistAgent.Run` 去掉默认回退 | 修改（1 处） |
| `engine/agent.go` | `AgentSpec.MaxIterations` 注释更新（0 = 无上限）；`Handoff.MaxIterations` 补充注释 | 修改（2 处注释） |

> 既有回归守卫（**不新增，靠现有测试**）：`engine/sub_agent_finish_test.go:124` `TestSubAgentRunLoop_MaxIterationsReason` 断言 `MaxIterations=3` 持续调工具不收敛 → `HandoffReasonMaxIterations`。该测试已存在，改动后应保持绿——它就是"显式上限保留"的守护。

---

### 任务 1：编写失败的回归测试

**文件：**
- 创建：`engine/sub_agent_remove_turn_limit_test.go`

- [ ] **步骤 1：编写测试**

用 `write` 工具创建 `engine/sub_agent_remove_turn_limit_test.go`：

```go
package engine

import (
	"context"
	"testing"
)

// TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes 锁定移除默认 99 轮上限：
// MaxIterations=0（默认）时子代理可跑超过 99 轮工具调用，最终经 submit_result
// 正常完成（FinishReason=completed），绝不因轮次上限被截断。
// 改动前：MaxIterations=0 回退到 99 → 第 100 次 submit_result 永不触达，
// 循环尾部以 NoResult 结束 → 本测试红灯。
func TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes(t *testing.T) {
	const toolRounds = 100 // > 旧默认上限 99
	responses := make([]ModelResponse, 0, toolRounds+1)
	for i := 0; i < toolRounds; i++ {
		responses = append(responses, ModelResponse{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID:       "c",
				Type:     "function",
				Function: ModelFunctionCall{Name: "bash", Arguments: `{"command":"echo progress"}`},
			}}},
			FinishReason: "tool_calls",
		})
	}
	responses = append(responses, ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID:       "submit",
			Type:     "function",
			Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"调研完成"}`},
		}}},
		FinishReason: "tool_calls",
	})
	model := &stubSeqModel{responses: responses}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}

	result, err := runner.Run(context.Background(), Handoff{
		Agent:            AgentSub,
		Goal:             "g",
		StructuredResult: true, // 与生产 generic sub 一致：结构化 run，submit_result 唯一完成路径
		// MaxIterations 省略 = 0 → 无上限（本次改动的核心断言）
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls <= toolRounds {
		t.Errorf("expected sub-agent to run past the old 99-turn cap, got %d calls", model.calls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=%q (normal completion), got %q", HandoffReasonCompleted, result.FinishReason)
	}
	if result.Summary != "调研完成" {
		t.Errorf("expected summary from submit_result, got %q", result.Summary)
	}
}
```

**为什么这个桩能工作**（实现者背景，无需改动）：`stubSeqModel`（`engine/sub_agent_terminate_test.go:14`）按 `responses` 顺序逐次返回并计数 `calls`；`stubToolExecutor`（`engine/turn_test.go:75`）的 `Execute` 返回 nil，因此每次 `bash` 调用不追加 tool 结果消息、不触发 `consecutiveIntermediate`、不触发 `firstOpKey` 同文件循环检测（只跟踪 edit/write）；`SubmitResultToolName = "submit_result"`（`engine/agent.go:22`），结构化 run 撞到它即终止并返回 `HandoffReasonCompleted`。

- [ ] **步骤 2：运行测试确认红灯**

运行：`go test ./engine/ -run TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes -v`
预期：FAIL。改动前 `MaxIterations=0` 回退到 99，`model.calls` 停在 99，submit_result 未触达，断言 `expected FinishReason="completed", got "no_result"`。

---

### 任务 2：移除 sub_agent.go 的默认上限

**文件：**
- 修改：`engine/sub_agent.go:13-16`（const 块）
- 修改：`engine/sub_agent.go:119-125`（Run）
- 修改：`engine/sub_agent.go:131-137`（RunWithPrompt）
- 修改：`engine/sub_agent.go:246`（循环条件）
- 修改：`engine/sub_agent.go:141-142`（runLoop 注释）

- [ ] **步骤 1：删除 `maxSubAgentIterations` 常量**

`engine/sub_agent.go:13-16`，改为：

```go
const (
	defaultSubAgentContext = 1_048_576 // ~1M — match main engine context window
)
```

- [ ] **步骤 2：`Run` 去掉默认回退**

`engine/sub_agent.go:119-125`，改为：

```go
// Run executes a generic sub-agent with the given handoff.
// MaxIterations <= 0 means no turn cap — the loop runs until a normal
// completion or a built-in guard (stalled narration, loop detection, etc.).
func (r *SubAgentRunner) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return r.runLoop(ctx, input, "", input.MaxIterations)
}
```

- [ ] **步骤 3：`RunWithPrompt` 去掉默认回退**

`engine/sub_agent.go:131-137`，改为：

```go
func (r *SubAgentRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	return r.runLoop(ctx, input, extraPrompt, input.MaxIterations)
}
```

- [ ] **步骤 4：`runLoop` 循环条件放开 + 参数注释更新**

`engine/sub_agent.go:246`，改为：

```go
	// 0 = no turn cap (default); >0 = explicit cap set by the delegating agent.
	for iter := 0; maxIterations <= 0 || iter < maxIterations; iter++ {
```

`engine/sub_agent.go:141-142` 参数注释，改为：

```go
// maxIterations caps the number of LLM turns for this agent; 0 = no cap.
```

- [ ] **步骤 5：运行任务 1 的测试确认绿灯**

运行：`go test ./engine/ -run TestSubAgent_NoTurnLimit_RunsPastOldCapAndCompletes -v`
预期：PASS，`FinishReason=completed`，`model.calls > 99`。

- [ ] **步骤 6：运行既有显式上限回归，确认未破坏**

运行：`go test ./engine/ -run 'TestSubAgentRunLoop_MaxIterationsReason|TestSubAgentRunLoop_TerminatesOnConclusionNotLoop|TestSubAgent_TextOnly_NoClassifierProbe' -v`
预期：全部 PASS（这些测试显式传 `MaxIterations`，语义不变）。

- [ ] **步骤 7：Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_remove_turn_limit_test.go
git commit -m "feat(engine): remove subagent default turn limit (99), keep explicit caps"
```

---

### 任务 3：specialistAgent 去掉默认回退 + 注释更新

**文件：**
- 修改：`engine/default_agents.go:63-66`（specialistAgent.Run）
- 修改：`engine/agent.go:85`（AgentSpec.MaxIterations 注释）
- 修改：`engine/agent.go:53`（Handoff.MaxIterations 注释）

- [ ] **步骤 1：`specialistAgent.Run` 去掉默认回退**

`engine/default_agents.go:63-66`，改为：

```go
func (a *specialistAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	input.StructuredResult = a.spec.StructuredResult
	// spec.MaxIterations <= 0 = no turn cap (default).
	return a.runner.runLoop(ctx, input, a.promptFor(zhFromLang(input.UserLanguage)), a.spec.MaxIterations, a.spec.ModelName)
}
```

> 影响：critic 的 `spec.MaxIterations = 15`（default_agents.go:19）不变 → critic 仍 15 轮收敛。

- [ ] **步骤 2：更新 `AgentSpec.MaxIterations` 注释**

`engine/agent.go:85`，改为：

```go
	MaxIterations int      // 0 = no turn cap (default). Set > 0 for agents that must finish quickly (e.g. critic: 15).
```

- [ ] **步骤 3：补充 `Handoff.MaxIterations` 注释**

`engine/agent.go:53`（`MaxIterations  int    `json:"max_iterations,omitempty"`` 上一行加注释）：

```go
	// MaxIterations caps the number of sub-agent turns; 0 = no cap (default).
	MaxIterations  int    `json:"max_iterations,omitempty"`
```

- [ ] **步骤 4：全量验证**

运行：`go test ./engine/`
预期：全部 PASS（含任务 1 新增测试 + 既有 30+ 测试）。

运行：`go build ./...`
预期：无错误。

- [ ] **步骤 5：Commit**

```bash
git add engine/default_agents.go engine/agent.go
git commit -m "feat(engine): drop 0->99 fallback in specialistAgent, document 0=no cap"
```

---

## 自检

**1. 规格覆盖度**
- 删除 `maxSubAgentIterations = 99` 常量 → 任务 2 步骤 1
- `Run`/`RunWithPrompt` 去掉默认回退 → 任务 2 步骤 2-3
- 循环条件放开 → 任务 2 步骤 4
- `specialistAgent.Run` 去掉默认回退 → 任务 3 步骤 1
- 注释更新（agent.go:53/85）→ 任务 3 步骤 2-3
- 尾部 `max_iterations` 兜底分支保留 → 不新增任务（代码不动，仅任务 2 步骤 4 的注释更新提及语义）
- 回归测试（>99 轮 + 显式上限保留）→ 任务 1 + 任务 2 步骤 6
- 验证（go test / go build）→ 任务 3 步骤 4

**2. 占位符扫描**：无 TODO/待定；每个代码步骤含完整代码块。

**3. 类型一致性**：测试引用的 `stubSeqModel`、`stubToolExecutor`、`SubmitResultToolName`、`HandoffReasonCompleted`、`ModelResponse`、`ModelMessage`、`ModelToolCall`、`ModelFunctionCall`、`Handoff`、`SubAgentRunner` 均为 `engine` 包现有符号（已逐一核对定义位置）。
