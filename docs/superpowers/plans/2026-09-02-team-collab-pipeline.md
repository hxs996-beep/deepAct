# /collab 协作流水线模式 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 新增 `/collab <目标>` 命令，实现"多子 agent 协作解决问题"模式：四个中文角色沿**流水线**依次工作（侦察 → 设计 → 开发 → 把关），最后 LLM 汇总各阶段产出展示给用户，用户确认后再由主 agent 统一落地。与 `/debate`（多性格辩论出方案）互补。

**架构：** 完全复用 `/debate` 的成熟模式——`CollabHall`（对应 `RoundtableHall`）通过 `SubAgentRunner.RunWithPrompt` 给通用 `AgentSub` 注入各阶段角色提示，幂等地分阶段执行流水线，产出存入 `TaskState.Collab`（新增 `CollabState`）。流水线各 agent **不落盘改代码**（只产出分析/方案/实现内容，Tools 仅 `read/grep/glob/lsp` 无 `edit/write`），最终合并汇总后由主 agent 在你确认后统一落地。

**技术栈：** Go（engine/ 包）、标准库。复用：`RoundtableHall` 的 runSharedSearch/synthesizeDebate 模式、`Handoff`/`RunWithPrompt`/`accumulateUsage`/`pickPrompt`、loop.go 的状态机接入模式。

---

## 背景与动机

用户把 `/team` 拆成两个互补模式，并已拍板全部设计决策：

| 模式 | 命令 | 语义 | 成员 | 产出 |
|---|---|---|---|---|
| 讨论 | `/debate` | 多性格辩论出方案 | 创新/防守/务实/用户 | 平均分胜者 + 实施蓝图 |
| **协作** | `/collab`（本计划） | 多角色**流水线**协作解决问题 | 侦察/设计/开发/把关 | 各阶段产出汇总，确认后执行 |

用户明确决策：
1. **角色用中文直白名**（不看英文名也能懂干嘛），配英文翻译：**侦察(Recon) → 设计(Designer) → 开发(Builder) → 把关(Reviewer)**
2. **流水线**（非并行）：一环接一环，前序产出是后续输入
3. **产出汇总后给你看再决定**（与 `/debate` 的"裁决后执行"体验一致）

## 关键前提（已核实，实现时无需再查）

- **Hall 模式**：`RoundtableHall`（`engine/roundtable.go:124-137`）持有 `engine *Engine`；`NewRoundtableHall(e *Engine)`。`CollabHall` 完全照此。
- **角色注入**：`runMemberDebateTurn` 用 `agent.(promptRunner).RunWithPrompt(ctx, handoff, member.displayPrompt(zh))`（`roundtable.go:543-545`）给 `AgentSub` 注入角色系统提示。`/collab` 各阶段用同一机制注入角色提示。`promptRunner` 接口在 `roundtable.go:531-533` 定义。
- **LLM 汇总调用**：`synthesizeDebate`（`roundtable.go:278-335`）示范"AgentSub + RunWithPrompt + accumulateUsage + 返回 result.Summary"。`buildCollabSummary` 照抄此模式。
- **命令解析**：`parseTeamCommand`（`roundtable.go:75`）识别命令前缀，已改名判断 `cmd != "debate"`（commit 938422d）。`parseCollabCommand` 照此写，识别 `cmd != "collab"`。
- **loop.go 接入点**：
  - `Engine` 结构体（`loop.go:46-115`）——加 `collabHall *CollabHall` 字段（照 `roundtableHall` 的 `loop.go:108`）
  - `Engine` 结构体——加 `collabVerdictPending bool`（照 `teamVerdictPending` 的 `loop.go:110-114`）
  - `NewEngine` 初始化（`loop.go:196`）`e.roundtableHall = NewRoundtableHall(e)`——加 `e.collabHall = NewCollabHall(e)`
  - `/debate` 命令启动块（`loop.go:313-348`）——`/collab` 命令块照此写在旁边（`// Skill command handling` 注释之前）
  - 辩论调度块（`loop.go:692-722`）——collab 调度块照此写在其后（`// Team verdict` 注释 `:724` 之前）
  - `teamVerdictPending` 门控（`loop.go:730-736`）——`collabVerdictPending` 门控照此写在其后
  - Run 末尾 `RoundtableDone` 清理（`loop.go:895-897`）——`CollabDone` 清理照此写在其后
- **TaskState**：`engine/types.go:232-268`，已有 `Roundtable *RoundtableState` 字段（`types.go:254`）。加 `Collab *CollabState` 字段。
- **UI 注册**：`ui/model.go:90-95` 的 `slashCommands` 数组（已有 `/debate` 条目 :93）、`:2957` 欢迎文案。加 `/collab` 条目与文案。
- **测试基建**：`roundtable_test.go:143-162` 的 `newTestEngine`（注册 `mockPromptRunner`，实现 `RunWithPrompt` 返回固定文本"采用微服务架构"）。collab 测试新建 `newCollabTestEngine`（同包共享 `mockPromptRunner`）。`stubStreamModel`/`stubContextBuilder`/`stubToolExecutor` 在 `turn_test.go` 定义，同包共享。
- **进度事件**：`ProgressEvent.Type`（`types.go:44-53`）用自定义类型（如 `"collab_stage"`），避免与 `TestDebateArena_ProgressEvents` 的 4 个 `debate_phase` 断言冲突（此前已踩过此坑，见 roundtable.go:170-173 注释）。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/collab.go` | 新增：`parseCollabCommand`、`CollabHall`（流水线执行 + 汇总 + 确认界面） | 创建 |
| `engine/types.go` | `TaskState` 新增 `Collab *CollabState` 字段；新增 `CollabPhase`/`CollabStageName`/`CollabStage`/`CollabState` 类型 | 修改 |
| `engine/loop.go` | `Engine` 加 `collabHall`/`collabVerdictPending`；`NewEngine` 初始化；Run 中 `/collab` 命令解析 + 调度 + 确认门控 + 清理 | 修改 |
| `engine/collab_test.go` | `/collab` 全链路测试（新建） | 创建 |
| `ui/model.go` | `slashCommands` 加 `/collab` 条目；欢迎文案提及 | 修改 |
| `README.md` / `README.zh.md` | 文档加 `/collab` 流水线模式说明 | 修改 |

---

### 任务 1：CollabState 类型 + 命令解析 + loop.go 接入

**文件：**
- 修改：`engine/types.go`（`TaskState` 加字段 + 新增类型）
- 创建：`engine/collab.go`（`parseCollabCommand` + `CollabCommand` + `CollabHall` 占位）
- 修改：`engine/loop.go`（Engine 字段 + NewEngine 初始化 + 命令启动块）
- 测试：`engine/collab_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/collab_test.go`：

```go
package engine

import "testing"

// --- /collab command parsing ---

func TestParseCollabCommand_Valid(t *testing.T) {
	cmd := parseCollabCommand("/collab 实现一个缓存层")
	if cmd == nil {
		t.Fatal("expected non-nil CollabCommand")
	}
	if cmd.Goal != "实现一个缓存层" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "实现一个缓存层")
	}
}

func TestParseCollabCommand_NotCollab(t *testing.T) {
	cases := []string{
		"/debate 实现一个功能",
		"/team 实现一个功能",
		"/skills",
		"普通用户消息",
		"",
		"/",
	}
	for _, c := range cases {
		cmd := parseCollabCommand(c)
		if cmd != nil {
			t.Errorf("expected nil for %q, got %+v", c, cmd)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestParseCollabCommand -v`
预期：编译失败——`parseCollabCommand` 未定义。

- [ ] **步骤 3：`types.go` 新增 CollabState 类型与 TaskState 字段**

在 `engine/types.go` 的 `RoundtableState` 定义之后（约 `:443`）追加：

```go
// CollabStageName labels a single stage of the /collab pipeline.
type CollabStageName string

const (
	CollabRecon  CollabStageName = "recon"  // 侦察：扫描代码库
	CollabDesign CollabStageName = "design" // 设计：出技术方案
	CollabDev    CollabStageName = "dev"    // 开发：产实现内容
	CollabReview CollabStageName = "review" // 把关：评审挑问题
)

// CollabStage captures one pipeline stage's output.
type CollabStage struct {
	Name    CollabStageName `json:"name"`
	Content string          `json:"content"`
}

// CollabPhase describes which stage of the /collab pipeline we are in.
type CollabPhase int

const (
	CollabIdle            CollabPhase = iota
	CollabReconPhase                  // 侦察
	CollabDesignPhase                 // 设计
	CollabDevPhase                    // 开发
	CollabReviewPhase                 // 把关
	CollabAwaitingConfirmation        // 等待用户确认汇总
	CollabDone                        // 完成
)

// CollabState tracks the current /collab pipeline within TaskState.
type CollabState struct {
	Goal   string        `json:"goal"`
	Phase  CollabPhase   `json:"phase"`
	Stages []CollabStage `json:"stages"` // 各流水线段产出，按执行顺序
}
```

在 `engine/types.go` 的 `TaskState` 中、`Roundtable *RoundtableState`（`types.go:254`）之后加：

```go
	Collab              *CollabState      `json:"collab,omitempty"`
```

- [ ] **步骤 4：`collab.go` 新增 `parseCollabCommand` + `CollabCommand` + `CollabHall` 占位**

创建 `engine/collab.go`：

```go
package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// CollabCommand represents a parsed /collab command.
type CollabCommand struct {
	Goal string
}

// parseCollabCommand checks if userMsg is a /collab command.
func parseCollabCommand(userMsg string) *CollabCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "collab" {
		return nil
	}
	goal := strings.Join(parts[1:], " ")
	if goal == "" {
		return nil
	}
	return &CollabCommand{Goal: goal}
}

// CollabHall orchestrates the /collab pipeline.
type CollabHall struct {
	engine *Engine
}

func NewCollabHall(e *Engine) *CollabHall {
	return &CollabHall{engine: e}
}
```

> 注：`CollabHall` 目前只有骨架（含 `fmt`/`os`/`time`/`context` import 供后续任务使用，Go 编译器会因未使用 import 报错——任务 1 步骤 6 会先只写 `import "strings"`，任务 2 再补全。为让任务 1 通过编译，**步骤 4 的 collab.go 用最小 import**：

```go
package engine

import "strings"

// CollabCommand represents a parsed /collab command.
type CollabCommand struct {
	Goal string
}

// parseCollabCommand checks if userMsg is a /collab command.
func parseCollabCommand(userMsg string) *CollabCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "collab" {
		return nil
	}
	goal := strings.Join(parts[1:], " ")
	if goal == "" {
		return nil
	}
	return &CollabCommand{Goal: goal}
}

// CollabHall orchestrates the /collab pipeline.
type CollabHall struct {
	engine *Engine
}

func NewCollabHall(e *Engine) *CollabHall {
	return &CollabHall{engine: e}
}
```

- [ ] **步骤 5：`loop.go` 接入 Engine 字段 + 初始化 + 命令启动**

修改 `engine/loop.go`：

**Engine 结构体**（在 `roundtableHall` 字段 `:108` 后）加：
```go
	// collabHall orchestrates the /collab pipeline (recon → design → dev → review).
	collabHall *CollabHall
```

**Engine 结构体**（在 `teamVerdictPending` 字段 `:114` 后）加：
```go
	// collabVerdictPending is set when the user confirms the /collab summary,
	// skipping confirmation gates so the plan lands directly.
	collabVerdictPending bool
```

**NewEngine**（在 `:196` 的 `e.roundtableHall = NewRoundtableHall(e)` 后）加：
```go
	e.collabHall = NewCollabHall(e)
```

**命令启动块**（在 `/debate` 命令块 `:348` 之后、`// Skill command handling` 之前）加：
```go
	// Collab command handling — /collab <goal>
	// Activates the collaboration pipeline: recon → design → dev → review.
	if cc := parseCollabCommand(userMsg); cc != nil {
		e.state.Collab = &CollabState{
			Goal:  cc.Goal,
			Phase: CollabReconPhase,
		}
		// Replace raw "/collab <goal>" so the main agent loop sees a proper prompt.
		if len(e.history) > 0 {
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"协作流水线已启动：%s\n\n请等待各环节完成。", cc.Goal)
			userMsg = fmt.Sprintf("协作流水线已启动：%s\n\n请等待各环节完成。", cc.Goal)
		}
	}
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run TestParseCollabCommand -v`
预期：全部 PASS。

- [ ] **步骤 7：Commit**

```bash
git add engine/types.go engine/collab.go engine/loop.go engine/collab_test.go
git commit -m "feat(engine): add /collab command parsing and CollabState"
```

---

### 任务 2：流水线各阶段执行（recon/design/dev/review）

**文件：**
- 修改：`engine/collab.go`（补全 import + `handleCollabArena` + `runCollabStage` + 各阶段 goal/role 构造）
- 测试：`engine/collab_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/collab_test.go` 追加（文件顶部 import 需加 `"context"` 与 `"strings"`）：

```go
// --- Collab pipeline stages ---

func TestHandleCollabArena_RunsAllStages(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabReconPhase,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab.Phase != CollabAwaitingConfirmation {
		t.Errorf("Phase = %v, want CollabAwaitingConfirmation", e.state.Collab.Phase)
	}
	if len(e.state.Collab.Stages) != 4 {
		t.Fatalf("got %d stages, want 4", len(e.state.Collab.Stages))
	}
	// 各阶段都有产出（mockPromptRunner 返回固定文本"采用微服务架构"）
	for _, s := range e.state.Collab.Stages {
		if !strings.Contains(s.Content, "采用微服务架构") {
			t.Errorf("stage %q should contain mock output, got %q", s.Name, s.Content)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestHandleCollabArena -v`
预期：编译失败——`handleCollabArena` 未定义、`newCollabTestEngine` 未定义。

- [ ] **步骤 3：`collab_test.go` 新增测试引擎**

在 `engine/collab_test.go` 追加（复用 `mockPromptRunner`，已在 roundtable_test.go 定义，同包共享）：

```go
// newCollabTestEngine creates a minimal engine for /collab testing.
func newCollabTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{
			id:       AgentSub,
			response: "## 产出\n采用微服务架构。",
		},
	})
	e := &Engine{
		agents:          reg,
		state:           &TaskState{TaskID: "test-collab"},
		config:          EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.collabHall = NewCollabHall(e)
	return e
}
```

- [ ] **步骤 4：`collab.go` 补全 import 并实现流水线执行**

将 `collab.go` 顶部 `import "strings"` 替换为（注意：仅 `context`/`fmt`/`os`/`strings` 四者——本任务代码不用 `time`，避免未使用 import 编译错误）：

```go
import (
	"context"
	"fmt"
	"os"
	"strings"
)
```

在 `NewCollabHall` 之后追加：

```go
// collabStageMaxIterations bounds each pipeline stage's sub-agent loop.
// A stage produces focused output (research/design/dev-content/review), so a
// modest cap keeps the pipeline fast while allowing tool-based grounding.
const collabStageMaxIterations = 20

// handleCollabArena runs the /collab pipeline through all stages until the
// final review output is complete, then leaves the state AwaitingConfirmation
// for the user to confirm the synthesized summary. Idempotent: completed
// stages are skipped on re-entry (partial failure / resume safety).
func (h *CollabHall) handleCollabArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Collab == nil {
		return nil, nil
	}

	zh := msgIsChinese(state.Collab.Goal)
	goal := state.Collab.Goal

	order := []struct {
		phase CollabPhase
		name  CollabStageName
	}{
		{CollabReconPhase, CollabRecon},
		{CollabDesignPhase, CollabDesign},
		{CollabDevPhase, CollabDev},
		{CollabReviewPhase, CollabReview},
	}

	for _, o := range order {
		if state.Collab.Phase <= o.phase {
			content := h.runCollabStage(ctx, o.name, goal, zh)
			state.Collab.Stages = append(state.Collab.Stages, CollabStage{Name: o.name, Content: content})
			state.Collab.Phase = o.phase + 1
		}
	}

	state.Collab.Phase = CollabAwaitingConfirmation

	// 占位响应：任务 3 会升级为 buildCollabSummary + buildCollabPrompt。
	// 此处仅返回阶段完成的确认，避免引用任务 3 才定义的函数（类型一致性）。
	return &EngineResponse{
		Summary: pickPrompt(zh,
			"Collaboration pipeline complete. Awaiting confirmation...",
			"协作流水线完成，等待确认..."),
		Stage: StageAct,
	}, nil
}

// runCollabStage executes a single pipeline stage via AgentSub with a
// role-specific system prompt injected through RunWithPrompt.
func (h *CollabHall) runCollabStage(ctx context.Context, stage CollabStageName, goal string, zh bool) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "collab_stage",
			Name:   string(stage),
			Detail: collabStageLabel(stage, zh),
		})
	}

	stageGoal := buildCollabStageGoal(stage, goal, zh)
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          stageGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: collabStageMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return fmt.Sprintf("collab stage %s failed: %v", stage, err)
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	var content string
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, collabRolePrompt(stage, zh))
		if err != nil {
			content = fmt.Sprintf("collab stage %s failed: %v", stage, err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	} else {
		result, err := agent.Run(ctx, handoff)
		if err != nil {
			content = fmt.Sprintf("collab stage %s failed: %v", stage, err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	}

	fmt.Fprintf(os.Stderr, "[collab]   stage %s done, contentLen=%d\n", stage, len(content))
	return content
}

// collabRolePrompt returns the compact role system prompt for a pipeline stage.
func collabRolePrompt(stage CollabStageName, zh bool) string {
	switch stage {
	case CollabRecon:
		return pickPrompt(zh,
			"You are a codebase scout (Recon). Your job is to locate relevant files and understand the current code.",
			"你是「侦察」——代码库侦察员。你的任务是定位相关文件、摸清当前代码现状。")
	case CollabDesign:
		return pickPrompt(zh,
			"You are a system designer (Designer). Your job is to produce a concrete technical design for the requirement.",
			"你是「设计」——系统架构师。你的任务是产出针对需求的具体技术方案。")
	case CollabDev:
		return pickPrompt(zh,
			"You are an implementation engineer (Builder). Your job is to produce concrete implementation content (code-level changes, file-by-file) for the design.",
			"你是「开发」——实现工程师。你的任务是产出具体的实现内容（逐文件的代码级改动）。")
	case CollabReview:
		return pickPrompt(zh,
			"You are an independent reviewer (Reviewer). Your job is to critically review the implementation and flag risks, bugs, or missing pieces.",
			"你是「把关」——独立评审员。你的任务是批判性审查实现，指出风险、缺陷或遗漏。")
	}
	return ""
}

// buildCollabStageGoal constructs the task prompt for a pipeline stage.
func buildCollabStageGoal(stage CollabStageName, goal string, zh bool) string {
	switch stage {
	case CollabRecon:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nScan the codebase for context relevant to the requirement. Produce a structured report: relevant files (exact paths), key code references, constraints, risks.\n\n## Requirement\n%s\n\nDo NOT propose solutions — research only.",
			"## 任务\n扫描代码库中与需求相关的上下文。产出结构化报告：相关文件（精确路径）、关键代码引用、约束、风险。\n\n## 需求\n%s\n\n不要提方案——只做调研。"), goal)
	case CollabDesign:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nBased on the codebase context, produce a concrete technical design for the requirement: approach, key design decisions, file-level changes.\n\n## Requirement\n%s",
			"## 任务\n基于代码库上下文，为需求产出具体技术方案：方法、关键设计决策、逐文件改动。\n\n## 需求\n%s"), goal)
	case CollabDev:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nBased on the design, produce concrete implementation content: file-by-file changes with code snippets, function signatures, and integration points.\n\n## Requirement\n%s",
			"## 任务\n基于设计方案，产出具体实现内容：逐文件改动 + 代码片段、函数签名、集成点。\n\n## 需求\n%s"), goal)
	case CollabReview:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nCritically review the implementation plan. Flag bugs, risks, missing edge cases, and concrete fixes. Be specific.\n\n## Requirement\n%s",
			"## 任务\n批判性审查实现方案。指出 bug、风险、遗漏的边界情况，并给出具体修复建议。\n\n## 需求\n%s"), goal)
	}
	return ""
}

// collabStageLabel returns a human-readable label for a pipeline stage.
func collabStageLabel(stage CollabStageName, zh bool) string {
	switch stage {
	case CollabRecon:
		return pickPrompt(zh, "Recon", "侦察")
	case CollabDesign:
		return pickPrompt(zh, "Design", "设计")
	case CollabDev:
		return pickPrompt(zh, "Dev", "开发")
	case CollabReview:
		return pickPrompt(zh, "Review", "把关")
	}
	return string(stage)
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestHandleCollabArena -v`
预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add engine/collab.go engine/collab_test.go
git commit -m "feat(engine): run /collab pipeline stages (recon/design/dev/review)"
```

---

### 任务 3：汇总 + 确认界面 + Advance 裁决

**文件：**
- 修改：`engine/collab.go`（`buildCollabSummary` + `buildCollabPrompt` + `Advance` + `handleConfirmation`）
- 测试：`engine/collab_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/collab_test.go` 追加：

```go
// --- Collab summary + confirmation ---

func TestHandleCollabArena_SummaryGenerated(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabReconPhase,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// 汇总界面包含各阶段标题 + 协作摘要
	if !strings.Contains(resp.Summary, "协作") {
		t.Errorf("collab prompt should mention collaboration, got:\n%s", resp.Summary)
	}
	for _, label := range []string{"侦察", "设计", "开发", "把关"} {
		if !strings.Contains(resp.Summary, label) {
			t.Errorf("collab prompt should contain stage %q, got:\n%s", label, resp.Summary)
		}
	}
}

func TestCollab_AdvanceConfirm(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:   "实现一个缓存层",
		Phase:  CollabAwaitingConfirmation,
		Stages: []CollabStage{{Name: CollabRecon, Content: "调研结果"}},
	}
	resp, err := e.collabHall.Advance(context.Background(), "支持")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab.Phase != CollabDone {
		t.Errorf("Phase = %v, want CollabDone", e.state.Collab.Phase)
	}
	if !e.collabVerdictPending {
		t.Error("collabVerdictPending should be set after confirmation")
	}
	if len(e.pendingPinnedMessages) == 0 {
		t.Error("expected pendingPinnedMessages after confirmation")
	}
}

func TestCollab_AdvanceRestart(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabAwaitingConfirmation,
	}
	_, err := e.collabHall.Advance(context.Background(), "重新协作")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if e.state.Collab.Phase != CollabReconPhase {
		t.Errorf("Phase = %v, want CollabReconPhase (restart)", e.state.Collab.Phase)
	}
	if len(e.state.Collab.Stages) != 0 {
		t.Errorf("Stages should be cleared on restart, got %d", len(e.state.Collab.Stages))
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestHandleCollabArena_SummaryGenerated|TestCollab_Advance' -v`
预期：编译失败——`buildCollabSummary`/`buildCollabPrompt`/`Advance` 未定义。

- [ ] **步骤 3：`collab.go` 实现汇总与确认界面**

在 `collab.go` 末尾追加：

```go
// buildCollabSummary runs a single LLM call that merges all pipeline stage
// outputs into a concise collaboration summary for the user. Returns "" on
// failure so the prompt falls back to showing raw stage outputs.
func (h *CollabHall) buildCollabSummary(ctx context.Context, goal string, zh bool) string {
	state := h.engine.state.Collab
	if len(state.Stages) < 4 {
		return ""
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "collab_summary",
			Name:   "summary",
			Detail: pickPrompt(zh, "Synthesizing collaboration summary...", "正在合成协作摘要..."),
		})
	}

	var record strings.Builder
	for _, s := range state.Stages {
		record.WriteString(fmt.Sprintf("## %s\n%s\n\n", collabStageLabel(s.Name, zh), s.Content))
	}

	taskGoal := fmt.Sprintf(pickPrompt(zh,
		"## Task\nYou are a team lead. Below are the outputs of a collaboration pipeline (recon → design → dev → review). Merge them into a concise summary the user can confirm: what will be built, key decisions, and any review concerns.\n\n## Requirement\n%s\n\n## Pipeline Outputs\n%s",
		"## 任务\n你是协作团队负责人。下面是协作流水线（侦察 → 设计 → 开发 → 把关）各环节的产出。把它们合并成一份用户可直接确认的简洁摘要：要做什么、关键决策、把关发现的问题。\n\n## 需求\n%s\n\n## 流水线产出\n%s"), goal, record.String())

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 3,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, "")
		if err != nil || result == nil {
			return ""
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

// buildCollabPrompt renders the /collab confirmation screen: pipeline outputs
// per stage + the merged summary, ending with confirmation instructions.
func (h *CollabHall) buildCollabPrompt(goal string, zh bool, summary string) *EngineResponse {
	var sb strings.Builder

	sb.WriteString(pickPrompt(zh,
		"## Collaboration Complete - Review & Confirm\n\n",
		"## 协作完成 - 请审阅并确认\n\n",
	))
	sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", pickPrompt(zh, "Goal", "需求"), goal))

	state := h.engine.state.Collab

	if summary != "" {
		sb.WriteString(pickPrompt(zh, "### Collaboration Summary\n\n", "### 协作摘要\n\n"))
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}

	sb.WriteString(pickPrompt(zh, "### Pipeline Outputs\n\n", "### 流水线产出\n\n"))
	for _, s := range state.Stages {
		sb.WriteString(fmt.Sprintf("#### %s\n%s\n\n", collabStageLabel(s.Name, zh), s.Content))
	}

	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"**Your decision**: Type `support` to execute this plan, `but <condition>` to adjust, or `restart` to re-run the pipeline\n",
		"**你的决定**: 输入 `支持` 执行此方案、`但要<条件>` 调整、或 `重新协作` 重跑流水线\n",
	))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}

// Advance handles user input during the /collab AwaitingConfirmation phase.
func (h *CollabHall) Advance(ctx context.Context, userMsg string) (*EngineResponse, error) {
	state := h.engine.state
	if state.Collab == nil {
		return nil, nil
	}

	zh := msgIsChinese(userMsg)
	if !zh && userMsg == "" {
		zh = msgIsChinese(state.Collab.Goal)
	}

	lower := strings.ToLower(strings.TrimSpace(userMsg))

	switch state.Collab.Phase {
	case CollabAwaitingConfirmation:
		return h.handleConfirmation(userMsg, lower, zh), nil
	case CollabDone:
		return nil, nil
	default:
		return nil, nil
	}
}

// handleConfirmation processes the user's decision on the /collab summary.
func (h *CollabHall) handleConfirmation(userMsg, lower string, zh bool) *EngineResponse {
	state := h.engine.state

	// "重新协作" / "restart" → clear stages and restart the pipeline.
	if strings.Contains(lower, "重新协作") || strings.Contains(lower, "重新") ||
		strings.Contains(lower, "restart") || lower == "restart" {
		state.Collab.Phase = CollabReconPhase
		state.Collab.Stages = nil
		return &EngineResponse{
			Summary: pickPrompt(zh, "Restarting collaboration pipeline...", "正在重新启动协作流水线..."),
			Stage:   StageAct,
		}
	}

	// User confirms (or adjusts). Include all stage outputs so the executing
	// agent sees the full plan, not just the summary.
	var record strings.Builder
	for _, s := range state.Collab.Stages {
		record.WriteString(fmt.Sprintf("## %s\n%s\n\n", collabStageLabel(s.Name, zh), s.Content))
	}
	pinned := fmt.Sprintf("[COLLAB PLAN: %s]\n\n%s\n\n%s\n%s",
		state.Collab.Goal,
		userMsg,
		pickPrompt(zh,
			"## Collaboration Pipeline Output (execute this plan)",
			"## 协作流水线产出（请按此方案执行）"),
		record.String())
	h.engine.pendingPinnedMessages = append(h.engine.pendingPinnedMessages, pinned)
	state.Collab.Phase = CollabDone

	// Mark that the next Run() should skip confirmation gates — the user
	// already approved the plan through the collaboration pipeline.
	h.engine.collabVerdictPending = true

	state.Decisions = append(state.Decisions, Decision{
		ID:   "collab-plan",
		Text: userMsg,
	})

	return &EngineResponse{
		Summary: pickPrompt(zh,
			fmt.Sprintf("✓ Collaboration confirmed. Proceeding with: %s", userMsg),
			fmt.Sprintf("✓ 协作方案已确认。将按以下方向执行: %s", userMsg),
		),
		Stage: StageAct,
	}
}
```

**恢复 `handleCollabArena` 末尾的真实调用**（任务 2 中曾用占位响应代替；现在 `buildCollabSummary`/`buildCollabPrompt` 已定义，必须改回真实调用，否则 `TestHandleCollabArena_SummaryGenerated` 会失败）：

将 `handleCollabArena` 末尾的占位响应块：

```go
	state.Collab.Phase = CollabAwaitingConfirmation

	// 占位响应：任务 3 会升级为 buildCollabSummary + buildCollabPrompt。
	// 此处仅返回阶段完成的确认，避免引用任务 3 才定义的函数（类型一致性）。
	return &EngineResponse{
		Summary: pickPrompt(zh,
			"Collaboration pipeline complete. Awaiting confirmation...",
			"协作流水线完成，等待确认..."),
		Stage: StageAct,
	}, nil
}
```

替换为：

```go
	state.Collab.Phase = CollabAwaitingConfirmation

	summary := h.buildCollabSummary(ctx, goal, zh)
	return h.buildCollabPrompt(goal, zh, summary), nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestHandleCollabArena_SummaryGenerated|TestCollab_Advance' -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/collab.go engine/collab_test.go
git commit -m "feat(engine): synthesize /collab summary and confirmation screen"
```

---

### 任务 4：loop.go 调度 + 门控 + 清理

**文件：**
- 修改：`engine/loop.go`（调度块 + `collabVerdictPending` 门控 + Run 末尾清理）
- 测试：`engine/collab_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/collab_test.go` 追加：

```go
// --- Run() integration ---

func TestRun_CollabExecutesAndConfirms(t *testing.T) {
	e := &Engine{
		model:           &stubStreamModel{chunks: []ModelChunk{{Delta: "执行了协作方案。", FinishReason: "stop"}}},
		context:         &stubContextBuilder{},
		tools:           stubToolExecutor{},
		state:           &TaskState{TaskID: "test-collab-run"},
		history:         []Message{},
		config:          EngineConfig{ModelName: "test-model", MaxTurns: 10},
		guards:          &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(true)},
		readLoop:        NewReadLoopState(),
		errorLoop:       NewErrorLoopState(0),
		activatedSkills: make(map[string]bool),
	}
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{id: AgentSub, response: "## 产出\n采用微服务架构。"},
	})
	e.agents = reg
	e.collabHall = NewCollabHall(e)
	e.state.Collab = &CollabState{
		Goal:   "实现缓存层",
		Phase:  CollabAwaitingConfirmation,
		Stages: []CollabStage{{Name: CollabRecon, Content: "调研结果"}},
	}

	resp, err := e.Run(context.Background(), "支持")
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// 确认后同一 Run 执行方案（而非只返回确认 ack）
	if strings.Contains(resp.Summary, "已确认") {
		t.Errorf("collab confirm Run must execute the plan, got ack %q", resp.Summary)
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared after confirm Run, got phase %v", e.state.Collab.Phase)
	}
	if e.collabVerdictPending {
		t.Error("collabVerdictPending should be consumed within the confirm Run")
	}
	decisionFound := false
	for _, d := range e.state.Decisions {
		if d.ID == "collab-plan" {
			decisionFound = true
		}
	}
	if !decisionFound {
		t.Error("expected a collab-plan decision to be persisted")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestRun_CollabExecutesAndConfirms -v`
预期：FAIL——loop.go 尚未接入 collab 调度与门控（Run 返回 ack 而非执行方案）。

- [ ] **步骤 3：`loop.go` 接入调度块 + 门控 + 清理**

在 `loop.go` 中，**辩论调度块之后**（`:722` 之后、`// Team verdict` 注释 `:724` 之前）插入：

```go
	// Collab pipeline phase — execute pipeline stages, then await confirmation.
	if e.state.Collab != nil {
		phase := e.state.Collab.Phase
		switch phase {
		case CollabReconPhase, CollabDesignPhase, CollabDevPhase, CollabReviewPhase:
			response, err := e.collabHall.handleCollabArena(ctx)
			if err != nil {
				return nil, fmt.Errorf("collab arena: %w", err)
			}
			if response != nil {
				return response, nil
			}
		case CollabAwaitingConfirmation:
			response, err := e.collabHall.Advance(ctx, userMsg)
			if err != nil {
				return nil, fmt.Errorf("collab confirm: %w", err)
			}
			// "重新协作" restarts the pipeline (return its response); a picked
			// plan (Phase becomes CollabDone) falls through so the main agent
			// loop consumes the pinned plan + collabVerdictPending flag and
			// executes in this same Run().
			if e.state.Collab.Phase != CollabDone {
				return response, nil
			}
		case CollabDone:
			// Pipeline complete — clear collab state so normal flow resumes.
			e.state.Collab = nil
		}
	}
```

**门控**：在 `teamVerdictPending` 门控块（`:730-736`）之后追加：

```go
	// Collab verdict: the user already approved a plan through the pipeline.
	if e.collabVerdictPending {
		e.state.PlanConfirmed = true
		e.state.AnalysisReportConfirmed = true
		e.state.AnalysisMode = false
		e.collabVerdictPending = false
		loopLog.Printf("collab verdict: PlanConfirmed=true, skipping confirmation gates")
	}
```

**Run 末尾清理**：在 `RoundtableDone` 清理块（`:895-897`）之后追加：

```go
	if e.state.Collab != nil && e.state.Collab.Phase == CollabDone {
		e.state.Collab = nil
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestRun_CollabExecutesAndConfirms -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/collab_test.go
git commit -m "feat(engine): wire /collab scheduling, confirmation gate, cleanup into Run"
```

---

### 任务 5：UI 注册 + 文档 + 全量回归

**文件：**
- 修改：`ui/model.go`（`slashCommands` 加 `/collab` + 欢迎文案）
- 修改：`README.md` / `README.zh.md`（加 `/collab` 说明）
- 验证：`go build ./...` + `go test ./...`

- [ ] **步骤 1：`ui/model.go` 注册 `/collab`**

在 `ui/model.go:93`（`/debate` 条目）后加：

```go
	{Command: "/collab", Args: "<需求>", Description: "多角色协作流水线：侦察→设计→开发→把关，汇总后确认执行"},
```

在 `ui/model.go:2957` 欢迎文案后追加：

```go
	b.WriteString("Use `/collab <需求>` for multi-role collaboration pipeline.\n")
```

- [ ] **步骤 2：`README.md` 加 `/collab` 章节**

在 README 的 `/debate` 章节后加：

```markdown
### Multi-Agent Collaboration Pipeline (/collab)

```bash
deepact exec "/collab add a cache layer"
```

A pipeline of four Chinese-named roles works in sequence: **侦察 (Recon)** scans the codebase, **设计 (Designer)** produces a technical design, **开发 (Builder)** writes concrete implementation content, **把关 (Reviewer)** flags risks and bugs. The merged summary is shown for your confirmation before the main agent executes.
```

- [ ] **步骤 3：`README.zh.md` 加 `/collab` 章节**

在 README.zh 的 `/debate` 章节后加：

```markdown
### 多角色协作流水线（/collab）

```bash
deepact exec "/collab 加一个缓存层"
```

四个中文角色按流水线依次协作：**侦察** 扫描代码库、**设计** 出技术方案、**开发** 产出具体实现内容、**把关** 评审挑出风险与缺陷。合并后的摘要先给你确认，再由主 agent 统一执行。
```

- [ ] **步骤 4：全量构建与测试**

运行：`go build ./... && go test ./...`
预期：全部编译通过、全部测试 PASS（重点确认 `TestDebateArena_ProgressEvents` 未被 collab 的事件类型破坏——collab 用 `collab_stage`/`collab_summary`，不产生 `debate_phase`）。

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go README.md README.zh.md
git commit -m "feat(ui,docs): register /collab command and document pipeline"
```

---

## 验收标准

- [ ] `/collab <目标>` 触发后：依次执行侦察 → 设计 → 开发 → 把关 四段流水线，每段产出存入 `CollabState.Stages`
- [ ] 四段全部产出后 LLM 合成协作摘要，展示各阶段产出 + 摘要，状态进入 AwaitingConfirmation
- [ ] 用户输入 `支持` → 注入 `[COLLAB PLAN]` pinned + `collabVerdictPending`，同一 Run 内主 agent 执行方案，Collab 状态清理
- [ ] 用户输入 `重新协作` → 清空 Stages、回到 ReconPhase 重跑
- [ ] 流水线各 agent 不落盘改代码（Tools 仅 `read/grep/glob/lsp`，无 `edit/write`），未确认前无真实文件改动
- [ ] `/collab` 与 `/debate` 命令互不冲突（`parseCollabCommand` 只认 `collab`，`parseTeamCommand` 只认 `debate`）
- [ ] UI `/help` 显示 `/collab`，README 双语文档更新
- [ ] `go build ./... && go test ./...` 全部通过，`TestDebateArena_ProgressEvents` 无回归

## 风险与回退

- **流水线慢**：4 段串行 + 汇总 LLM 调用，比 `/debate` 慢。用户明确选择流水线（质量优先）。
- **阶段失败**：`runCollabStage` 返回错误字符串仍存入 Stages，汇总/确认界面不中断；用户可见异常产出并决定重跑。
- **汇总失败**：`buildCollabSummary` 返回 "" 时，`buildCollabPrompt` 直接展示各阶段原始产出（fallback），功能不中断。
- **未确认改动风险**：所有阶段 agent 无 `edit`/`write` 工具，从机制上保证流水线只分析不改码。
