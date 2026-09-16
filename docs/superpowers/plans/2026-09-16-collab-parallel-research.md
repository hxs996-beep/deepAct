# /collab 并行研究模式 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 `/collab` 从"串行流水线（侦察→设计→开发→把关）"改造为**并行研究模式**：拆解 agent 把研究目标拆成 2~6 个研究方向，多个 worker 并发只读调研，最后汇总成研究报告。目的是加快研究进度。

**架构：** 仿 RATD 的**单 Run 状态机**（`RATDHall.handleRATDArena` 的 `for { switch phase }` 模式）：`CollabState` 保留名字，`CollabPhase` 重构为 `Decompose → Parallel → Synthesize → Done`。拆解/汇总各一次 `RunWithPrompt` 单次调用；并行阶段用信号量（上限 4）+ goroutine 并发跑 worker。全自动无确认门。worker 只读工具 `read/grep/glob/lsp`，`MaxIterations=99`。

**技术栈：** Go（engine/ 包）、标准库。复用：`topLevelJSONObjects`（ratd.go:150）、`RunWithPrompt` 角色注入、`accumulateUsage`、loop.go 状态机接入模式。

**⚠️ 原子重构注意：** `CollabPhase`/`CollabState` 类型变更会破坏所有引用方（collab.go / loop.go / context/builder.go / collab_test.go / context/builder_test.go）。Go 强类型下，**类型删除与新状态机必须同一 commit**，否则中间 commit 编译失败。任务 1 采用"先增量加新，后原子替换"两阶段避免大爆炸。

---

## 关键前提（已核实，实现时无需再查）

- **状态类型**：`CollabState`/`CollabPhase`/`CollabStageName`/`CollabStage` 在 `engine/types.go:345-379`；`TaskState.Collab` 字段在 `types.go:258`。
- **JSON 复用**：`topLevelJSONObjects` 在 `engine/ratd.go:150`（同包，直接调用）。
- **并发安全已内置**：`SubAgentRunner.runLoop` 每次 `model.Fork()` 独立 client（sub_agent.go:187）；`accumulateUsage` 用 `usageMu` 保护（loop.go:1236）；`OnProgress` 回调可并发调用。
- **命令解析**：`parseCollabCommand` 不变（collab.go:16），loop.go 命令启动块（loop.go:323-336）只改 `Phase` 初始值。
- **loop.go 接入点**：
  - Engine 字段 `collabHall`/`collabVerdictPending`（loop.go:102-107）
  - `NewEngine` 初始化（loop.go:189）
  - 调度块（loop.go:633-665）
  - `collabVerdictPending` 门控（loop.go:667-672）
  - Run 末尾 CollabDone 清理（loop.go:856-863）
- **context/builder.go**：`flattenCollab`/`collabPhaseName`/`collabVolatile`（builder.go:279-319）。`collabVolatile.Stages int` 需改为 `Tasks int`。
- **context/builder_test.go**：`TestFormatTaskStateVolatile_Collab`（:183-208）引用旧类型，需同步。
- **UI**：`ui/model.go:95` `/collab` 描述；`:3197` 欢迎文案。
- **README**：`README.md:115-121` / `README.zh.md:115-121` 的 `/collab` 章节。
- **测试基建**：`mockSimpleAgent`/`mockPromptRunner` 在 `engine/test_stubs_test.go:35-63`（同包共享）。`newCollabTestEngine`/`newCaptureCollabTestEngine` 在 `engine/collab_test.go:68-126`（需重写）。
- **RATD 状态机参照**：`engine/ratd.go:562-651`（`handleRATDArena` 的 for/switch 循环 + 幂等重入 + 错误返回模式）。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/types.go` | `CollabPhase`/`CollabState` 重构 + 新增 `CollabTask`；删 `CollabStageName`/`CollabStage` | 修改 |
| `engine/collab.go` | 重写为状态机（拆解/并行/汇总）+ 角色提示 + `parseCollabTasks`；删 `Advance`/`handleConfirmation` 等 | 修改 |
| `engine/loop.go` | 调度块改新 Phase、删 `collabVerdictPending` 门控与字段 | 修改 |
| `context/builder.go` | `collabVolatile.Stages`→`Tasks`、`collabPhaseName` 同步 | 修改 |
| `ui/model.go` | `/collab` 描述 + 欢迎文案 | 修改 |
| `engine/collab_test.go` | 重写为新流程测试 | 修改 |
| `context/builder_test.go` | `TestFormatTaskStateVolatile_Collab` 同步 | 修改 |
| `README.md` / `README.zh.md` | `/collab` 语义更新 | 修改 |

---

### 任务 1：新增类型 + parseCollabTasks + 角色提示（增量，编译保持绿）

**文件：**
- 修改：`engine/types.go`（新增常量/类型，**不删旧**）
- 修改：`engine/collab.go`（新增 parseCollabTasks/角色提示/常量，**不动旧状态机**）
- 测试：`engine/collab_test.go`（新增 parseCollabTasks 测试，保留旧测试）

- [ ] **步骤 1：编写失败的测试**

在 `engine/collab_test.go` 追加（import 需加 `"fmt"`）：

```go
// --- parseCollabTasks ---

func TestParseCollabTasks_Valid(t *testing.T) {
	content := `{
		"tasks": [
			{"id": "t1", "title": "调研现有缓存实现", "direction": "定位并阅读缓存模块"},
			{"id": "t2", "title": "调研第三方选型", "direction": "评估可复用方案"}
		]
	}`
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].ID != "t1" || tasks[0].Direction == "" {
		t.Errorf("task[0] = %+v, want id=t1 and non-empty direction", tasks[0])
	}
}

func TestParseCollabTasks_ToleratesProse(t *testing.T) {
	content := "好的，我拆解如下：\n```json\n{\"tasks\": [{\"id\": \"t1\", \"title\": \"A\", \"direction\": \"dir A\"}]}\n```\n以上是任务列表。"
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
}

func TestParseCollabTasks_TruncatesOverMax(t *testing.T) {
	var items []string
	for i := 1; i <= 8; i++ {
		items = append(items, fmt.Sprintf(`{"id": "t%d", "title": "T%d", "direction": "d%d"}`, i, i, i))
	}
	content := `{"tasks": [` + strings.Join(items, ",") + `]}`
	tasks, err := parseCollabTasks(content)
	if err != nil {
		t.Fatalf("parseCollabTasks() unexpected error: %v", err)
	}
	if len(tasks) != collabMaxTasks {
		t.Fatalf("got %d tasks, want truncated to %d", len(tasks), collabMaxTasks)
	}
	if tasks[0].ID != "t1" {
		t.Errorf("truncation must preserve order, first task = %q", tasks[0].ID)
	}
}

func TestParseCollabTasks_TooFewFails(t *testing.T) {
	content := `{"tasks": [{"id": "t1", "title": "A", "direction": "d"}]}`
	_, err := parseCollabTasks(content)
	if err == nil {
		t.Fatal("expected error for single task (< min)")
	}
}

func TestParseCollabTasks_EmptyFails(t *testing.T) {
	if _, err := parseCollabTasks(`{"tasks": []}`); err == nil {
		t.Fatal("expected error for empty tasks")
	}
	if _, err := parseCollabTasks(`no json here`); err == nil {
		t.Fatal("expected error for non-JSON content")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestParseCollabTasks -v`
预期：编译失败——`parseCollabTasks` 未定义、`collabMaxTasks` 未定义。

- [ ] **步骤 3：types.go 新增类型（不删旧）**

在 `engine/types.go` 中，旧 `CollabPhase` 常量块**之后**新增新 Phase 常量与 `CollabTask`（保留旧类型不动，任务 2 再删）：

```go
// --- Parallel research phases (new /collab) ---
const (
	CollabDecompose  CollabPhase = iota + 10
	CollabParallel
	CollabSynthesize
)
```

> **注：** 用 `iota + 10` 偏移，避免与旧常量（`CollabIdle`=0、`CollabReconPhase`=1 等）数值冲突——新常量暂与旧常量共存，任务 2 删除旧常量后数值会重新归一。新 Phase 是 `CollabPhase` 类型。

在 `CollabState` 结构体之后新增：

```go
// CollabTask is one research direction produced by the decomposer.
type CollabTask struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Direction string `json:"direction"` // 研究方向（自包含，worker 独立开工）
	Status    string `json:"status"`    // pending/running/done/failed
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}
```

- [ ] **步骤 4：collab.go 新增常量 + parseCollabTasks + 角色提示（不动旧代码）**

在 `engine/collab.go` 顶部 import 区补充（保留现有 import，仅新增 `encoding/json`）：

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)
```

> **注意：** 现有 `collab.go` 已 import `context/fmt/os/strings`，只需**新增** `encoding/json`。`sync` 等任务 2 实现 runWorkers 时再加（Go 编译器会因未使用 import 报错）。

在 import 块之后、`CollabHall` 结构体之前新增常量（与旧 `collabStageMaxIterations` 注释区并列）：

```go
const (
	collabMaxConcurrency           = 4
	collabMaxTasks                 = 6
	collabMinTasks                 = 2
	collabDecomposerMaxIterations  = 5
	collabWorkerMaxIterations      = 99
	collabSynthesizerMaxIterations = 5
)
```

> **注意：** 任务 1 步骤 1 的 `TestParseCollabTasks_TruncatesOverMax` 引用 `collabMaxTasks`，`parseCollabTasks` 引用 `collabMaxTasks`/`collabMinTasks`——常量必须在本任务定义，否则编译失败。

在 `NewCollabHall` 之后追加：

```go
// collabTaskPayload mirrors the Decomposer's output contract.
type collabTaskPayload struct {
	Tasks []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Direction string `json:"direction"`
	} `json:"tasks"`
}

// parseCollabTasks parses the Decomposer's JSON task list. Tolerates prose
// wrapping (via topLevelJSONObjects), skips invalid entries, truncates to
// collabMaxTasks, and errors when fewer than collabMinTasks valid tasks remain.
func parseCollabTasks(content string) ([]CollabTask, error) {
	objs := topLevelJSONObjects(content)
	if len(objs) == 0 {
		return nil, fmt.Errorf("invalid Decomposer output: no JSON object found")
	}
	for _, obj := range objs {
		var p collabTaskPayload
		if err := json.Unmarshal([]byte(obj), &p); err != nil {
			continue
		}
		if len(p.Tasks) == 0 {
			continue
		}
		var tasks []CollabTask
		for _, t := range p.Tasks {
			if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Direction) == "" {
				continue
			}
			tasks = append(tasks, CollabTask{
				ID:        t.ID,
				Title:     t.Title,
				Direction: t.Direction,
				Status:    "pending",
			})
			if len(tasks) >= collabMaxTasks {
				break
			}
		}
		if len(tasks) < collabMinTasks {
			continue
		}
		return tasks, nil
	}
	return nil, fmt.Errorf("invalid Decomposer output: no valid task list (objects: %d)", len(objs))
}

// collabResearchRolePrompt returns the compact role system prompt for a
// parallel-research harness role. Named distinctly from the legacy
// collabRolePrompt(CollabStageName) which still exists until task 2 removes
// the serial pipeline.
func collabResearchRolePrompt(role string, zh bool) string {
	switch role {
	case "decomposer":
		return pickPrompt(zh,
			"You are a Decomposer — a senior architect. Split the research goal into 2-6 non-overlapping research directions. Each direction must be self-contained so an independent researcher can start without shared context. Do NOT write code solutions — define research directions only. Output ONLY the tasks JSON: {\"tasks\":[{\"id\":\"t1\",\"title\":\"...\",\"direction\":\"...\"}]}.",
			"你是「拆解员」——资深架构师。把研究目标拆成 2~6 个互不重叠的研究方向。每个方向必须自包含，让独立研究员无需共享上下文即可开工。不要写代码方案——只定研究方向。只输出 tasks JSON：{\"tasks\":[{\"id\":\"t1\",\"title\":\"...\",\"direction\":\"...\"}]}。")
	case "worker":
		return pickPrompt(zh,
			"You are an independent researcher (Worker). Your job is to investigate ONE research direction thoroughly using read-only tools, and produce a concise research summary with concrete evidence (file:line references). Do NOT edit files.",
			"你是「研究员」——独立研究者。你的任务是只用只读工具彻底调研一个研究方向，产出简洁的研究小结，附具体证据（file:line 引用）。不要改任何文件。")
	case "synthesizer":
		return pickPrompt(zh,
			"You are a Synthesizer — a research team lead. Merge the worker reports below into one structured research report: an overview, per-direction findings, and a cross-cutting analysis with recommendations. Mark failed tasks as incomplete explicitly.",
			"你是「汇总员」——研究团队负责人。把下面的各 worker 报告合并成一份结构化研究报告：总体结论、各方向发现、跨方向综合分析建议。失败任务明确标注未完成。")
	}
	return ""
}
```

> **⚠️ 命名注意：** 新函数命名为 `collabResearchRolePrompt`（而非 `collabRolePrompt`），因为旧 `collabRolePrompt(stage CollabStageName, zh bool)`（collab.go:147）仍在被旧 `runCollabStage` 使用，Go 不支持同名不同类型参数重载。任务 2 删除旧状态机后，本函数名保持不变（任务 2 的 runRole 调用它）。旧 `capturePromptRunner` 靠 `extraPrompt != ""` 判定阶段运行，本任务**不改动任何旧代码**，旧测试不受影响。

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestParseCollabTasks -v`
预期：全部 PASS（旧 `TestHandleCollabArena_*` 测试不受影响，因旧状态机未动）。

- [ ] **步骤 6：Commit**

```bash
git add engine/types.go engine/collab.go engine/collab_test.go
git commit -m "feat(engine): add collab decomposer task parsing and role prompts"
```

---

### 任务 2：原子重构 — 新状态机 + 删旧类型（核心 commit）

**文件（必须同 commit，编译才绿）：**
- 修改：`engine/types.go`（删旧 Phase 常量/旧字段/旧类型，归一数值）
- 修改：`engine/collab.go`（删旧状态机，实现新 `handleCollabArena` 全流程）
- 修改：`engine/loop.go`（调度块改新 Phase、删 collabVerdictPending）
- 修改：`context/builder.go`（collabVolatile.Tasks + collabPhaseName）
- 修改：`context/builder_test.go`（TestFormatTaskStateVolatile_Collab 同步）
- 修改：`engine/collab_test.go`（删旧测试、加新状态机测试 + 新测试基建）

- [ ] **步骤 1：types.go 删旧 + 归一**

删除旧 `CollabStageName`/`CollabStage` 类型、旧 `CollabPhase` 常量（`CollabIdle`/`CollabReconPhase`/`CollabDesignPhase`/`CollabDevPhase`/`CollabReviewPhase`/`CollabAwaitingConfirmation`/`CollabDone`）、`CollabState` 的 `Stages` 字段。最终 `CollabPhase` 与 `CollabState` 为：

```go
// CollabPhase describes which stage of the /collab parallel research we are in.
type CollabPhase int

const (
	CollabIdle      CollabPhase = iota
	CollabDecompose             // 拆解：LLM 拆解 agent 产出任务列表
	CollabParallel              // 并行：并发执行各任务（只读调研）
	CollabSynthesize            // 汇总：LLM 合并各 worker 报告
	CollabDone                  // 完成：展示最终报告并清理
)

// CollabTask is one research direction produced by the decomposer.
type CollabTask struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Direction string `json:"direction"` // 研究方向（自包含，worker 独立开工）
	Status    string `json:"status"`    // pending/running/done/failed
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CollabState tracks the current /collab parallel research within TaskState.
type CollabState struct {
	Goal   string       `json:"goal"`
	Phase  CollabPhase  `json:"phase"`
	Tasks  []CollabTask `json:"tasks"`
	Report string       `json:"report,omitempty"` // 汇总研究报告
}
```

> **注意：** 任务 1 加的 `CollabDecompose` 等是 `iota + 10`，此处删旧后重新归一为 `iota`（0-4），并补 `CollabDone`。`CollabTask` 已在任务 1 定义，此处保留。

- [ ] **步骤 2：collab.go 删旧状态机，实现新状态机**

删除旧 `handleCollabArena`/`runCollabStage`/`buildCollabStageGoal`/`collabPriorSection`/`renderCollabPrior`/`collabStageLabel`/`buildCollabSummary`/`buildCollabPrompt`/`Advance`/`handleConfirmation` 全部函数。保留 `parseCollabCommand`/`CollabCommand`/`CollabHall`/`NewCollabHall`/`parseCollabTasks`/`collabResearchRolePrompt`/`collabTaskPayload`/常量。

补充 `sync` import，然后追加：

```go
// handleCollabArena runs the /collab parallel research state machine to
// completion within one Run(). Idempotent: re-entering after a partial failure
// resumes from the stored Phase. On completion it renders the research report
// and clears Collab state.
func (h *CollabHall) handleCollabArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Collab == nil {
		return nil, nil
	}
	zh := msgIsChinese(state.Collab.Goal)
	goal := state.Collab.Goal

	for {
		switch state.Collab.Phase {
		case CollabDecompose:
			payload := h.runRole(ctx, "decomposer", buildDecomposerGoal(goal, zh), zh, collabDecomposerMaxIterations)
			tasks, err := parseCollabTasks(payload)
			if err != nil {
				state.Collab = nil
				return nil, fmt.Errorf("collab decompose: %w", err)
			}
			state.Collab.Tasks = tasks
			state.Collab.Phase = CollabParallel

		case CollabParallel:
			h.runWorkers(ctx, state.Collab, zh)
			state.Collab.Phase = CollabSynthesize

		case CollabSynthesize:
			state.Collab.Report = h.runRole(ctx, "synthesizer", buildSynthesizerGoal(goal, state.Collab, zh), zh, collabSynthesizerMaxIterations)
			state.Collab.Phase = CollabDone

		case CollabDone:
			resp := h.buildCollabReport(goal, state.Collab, zh)
			h.engine.state.Collab = nil
			return resp, nil

		default:
			h.engine.state.Collab = nil
			return nil, nil
		}
	}
}

// buildDecomposerGoal instructs the Decomposer to split the goal into
// research directions, output as JSON.
func buildDecomposerGoal(goal string, zh bool) string {
	return fmt.Sprintf(pickPrompt(zh,
		"## Task\nSplit the following research goal into 2-6 non-overlapping research directions. Each direction must be self-contained: an independent researcher with read-only tools and NO shared context must be able to start from the direction text alone. Output ONLY the tasks JSON.\n\n## Research Goal\n%s",
		"## 任务\n把下面的研究目标拆成 2~6 个互不重叠的研究方向。每个方向必须自包含：一个只有只读工具、没有共享上下文的独立研究员，仅凭方向描述就能开工。只输出 tasks JSON。\n\n## 研究目标\n%s"), goal)
}

// runRole executes a harness role via the sub agent with its role prompt.
// Returns the sub-agent's Summary (the JSON payload / report) or "" on failure.
func (h *CollabHall) runRole(ctx context.Context, role, goal string, zh bool, iterations int) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "collab_phase",
			Name:   role,
			Detail: collabPhaseLabel(role, zh),
		})
	}
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: iterations,
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
		result, err := pr.RunWithPrompt(ctx, handoff, collabResearchRolePrompt(role, zh))
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

// runWorkers executes all collab tasks concurrently with a concurrency cap.
// A single worker failure is tolerated — the task is marked failed and other
// workers continue.
func (h *CollabHall) runWorkers(ctx context.Context, c *CollabState, zh bool) {
	sem := make(chan struct{}, collabMaxConcurrency)
	var wg sync.WaitGroup
	for i := range c.Tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(t *CollabTask) {
			defer wg.Done()
			defer func() { <-sem }()
			t.Status = "running"
			h.emitWorkerEvent("member_start", t.ID, t.Title, zh)
			result, err := h.runWorker(ctx, t, zh)
			if err != nil || result == "" {
				t.Status = "failed"
				t.Error = errText(err)
				if result != "" {
					t.Result = result
				}
			} else {
				t.Status = "done"
				t.Result = result
			}
			h.emitWorkerEvent("member_done", t.ID, t.Title, zh)
		}(&c.Tasks[i])
	}
	wg.Wait()
}

// runWorker executes a single research task via AgentSub with the worker role.
func (h *CollabHall) runWorker(ctx context.Context, t *CollabTask, zh bool) (string, error) {
	goal := fmt.Sprintf(pickPrompt(zh,
		"## Task\nInvestigate ONE research direction and produce a concise research summary with concrete evidence (file:line references).\n\n## Research Direction\n%s",
		"## 任务\n调研一个研究方向，产出简洁的研究小结，附具体证据（file:line 引用）。\n\n## 研究方向\n%s"), t.Direction)
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: collabWorkerMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return "", err
	}
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, collabResearchRolePrompt("worker", zh))
		if err != nil || result == nil {
			return "", err
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary, nil
	}
	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return "", err
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary, nil
}

// buildSynthesizerGoal assembles the Synthesizer's input: goal + all reports.
func buildSynthesizerGoal(goal string, c *CollabState, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(pickPrompt(zh,
		"## Task\nMerge the worker reports below into one structured research report: an overview, per-direction findings, and a cross-cutting analysis with recommendations. Mark failed tasks as incomplete explicitly.\n\n## Research Goal\n%s\n\n## Worker Reports\n",
		"## 任务\n把下面的各 worker 报告合并成一份结构化研究报告：总体结论、各方向发现、跨方向综合分析建议。失败任务明确标注未完成。\n\n## 研究目标\n%s\n\n## 各 worker 报告\n"), goal))
	for _, t := range c.Tasks {
		sb.WriteString(fmt.Sprintf("### %s (%s) — %s\n", t.Title, t.ID, t.Status))
		if t.Result != "" {
			sb.WriteString(t.Result + "\n\n")
		}
		if t.Error != "" {
			sb.WriteString(fmt.Sprintf("(error: %s)\n\n", t.Error))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collabPhaseLabel returns a human-readable label for a harness role.
func collabPhaseLabel(role string, zh bool) string {
	switch role {
	case "decomposer":
		return pickPrompt(zh, "decomposing...", "拆解中...")
	case "synthesizer":
		return pickPrompt(zh, "synthesizing...", "汇总中...")
	case "worker":
		return pickPrompt(zh, "researching...", "调研中...")
	}
	return role
}

// emitWorkerEvent emits a member_start/member_done progress event for a worker.
func (h *CollabHall) emitWorkerEvent(eventType, taskID, title string, zh bool) {
	if h.engine.config.OnProgress == nil {
		return
	}
	h.engine.config.OnProgress(ProgressEvent{
		Type:   eventType,
		Name:   "worker-" + taskID,
		Detail: title,
	})
}

// errText returns a compact error string, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// buildCollabReport renders the final research report for the user.
// Falls back to listing task results when the synthesized report is empty.
func (h *CollabHall) buildCollabReport(goal string, c *CollabState, zh bool) *EngineResponse {
	var sb strings.Builder
	sb.WriteString(pickPrompt(zh,
		"## Parallel Research Complete\n\n",
		"## 并行研究完成\n\n"))
	sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", pickPrompt(zh, "Goal", "需求"), goal))

	if c.Report != "" {
		sb.WriteString(c.Report)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(pickPrompt(zh, "### Task Results\n\n", "### 各任务结果\n\n"))
		for _, t := range c.Tasks {
			sb.WriteString(fmt.Sprintf("#### %s (%s) — %s\n", t.Title, t.ID, t.Status))
			if t.Result != "" {
				sb.WriteString(t.Result + "\n\n")
			}
			if t.Error != "" {
				sb.WriteString(fmt.Sprintf("**(error: %s)**\n\n", t.Error))
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"Research complete. You can ask follow-up questions or run more research.",
		"研究完成。你可以追问细节，或发起新的研究。"))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}
```

- [ ] **步骤 3：loop.go 接线**

**3a. 命令启动块**（loop.go:325-329）：`Phase: CollabReconPhase` → `Phase: CollabDecompose`，注释同步：

```go
	if cc := parseCollabCommand(userMsg); cc != nil {
		e.state.Collab = &CollabState{
			Goal:  cc.Goal,
			Phase: CollabDecompose,
		}
		// Replace raw "/collab <goal>" so the main agent loop sees a proper prompt.
		if len(e.history) > 0 {
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"并行研究已启动：%s\n\n请等待各环节完成。", cc.Goal)
			userMsg = fmt.Sprintf("并行研究已启动：%s\n\n请等待各环节完成。", cc.Goal)
		}
	}
```

**3b. 调度块**（loop.go:633-665）替换为：

```go
	// Collab parallel research phase — run decompose → parallel → synthesize.
	if e.state.Collab != nil {
		phase := e.state.Collab.Phase
		switch phase {
		case CollabDecompose, CollabParallel, CollabSynthesize:
			response, err := e.collabHall.handleCollabArena(ctx)
			if err != nil {
				return nil, fmt.Errorf("collab research: %w", err)
			}
			if response != nil {
				return response, nil
			}
		case CollabDone:
			// Pipeline complete — clear collab state so normal flow resumes.
			e.state.Collab = nil
		}
	}
```

**3c. 删 `collabVerdictPending` 门控**（loop.go:667-672）整块删除。

**3d. Engine 字段**（loop.go:105-107）删除：

```go
	// collabVerdictPending is set when the user confirms the /collab summary,
	// skipping confirmation gates so the plan lands directly.
	collabVerdictPending bool
```

**3e. 清理区注释**（loop.go:856-860）更新：

```go
	// Clean up a completed collab pipeline.
	// Run 末尾清理处理本 Run 内产生的 CollabDone；上一 Run 遗留的 pre-existing
	// CollabDone 已在调度块 CollabDone case 清空，两者互补不重复。
```

- [ ] **步骤 4：context/builder.go + context/builder_test.go 同步**

`context/builder.go`：
- `collabVolatile.Stages int` → `Tasks int`（:285）
- `flattenCollab` 的 `Stages: len(c.Stages)` → `Tasks: len(c.Tasks)`（:296）
- `collabPhaseName` switch 更新（:302-318）：

```go
func collabPhaseName(p engine.CollabPhase) string {
	switch p {
	case engine.CollabDecompose:
		return "decompose"
	case engine.CollabParallel:
		return "parallel"
	case engine.CollabSynthesize:
		return "synthesize"
	case engine.CollabDone:
		return "done"
	default:
		return "idle"
	}
}
```

`context/builder_test.go:183-208` 的 `TestFormatTaskStateVolatile_Collab` 替换为：

```go
func TestFormatTaskStateVolatile_Collab(t *testing.T) {
	state := &engine.TaskState{
		TurnNumber: 3,
		Collab: &engine.CollabState{
			Goal:  "实现登录页改造",
			Phase: engine.CollabParallel,
			Tasks: []engine.CollabTask{
				{ID: "t1", Title: "调研缓存", Direction: "读 cache.go", Status: "done", Result: "发现 TTL 逻辑"},
				{ID: "t2", Title: "调研选型", Direction: "评估第三方库", Status: "running"},
			},
		},
	}
	got := formatTaskStateVolatile(state)
	for _, want := range []string{
		`"collab":{`,
		`"phase":"parallel"`,
		`"goal":"实现登录页改造"`,
		`"tasks":2`,
	} {
		if !strContains(got, want) {
			t.Errorf("output should contain %q, got %q", want, got)
		}
	}
}
```

- [ ] **步骤 5：collab_test.go 重写**

**5a. 新增测试基建**（替换旧 `newCollabTestEngine`/`newCaptureCollabTestEngine`）：

```go
// decomposerMockRunner returns valid tasks JSON for the decompose phase, then
// falls back to the fixed response for worker/synthesizer phases.
type decomposerMockRunner struct {
	mockPromptRunner
	decomposed bool
}

func (m *decomposerMockRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	if !m.decomposed && strings.Contains(extraPrompt, "拆解员") {
		m.decomposed = true
		return &HandoffResult{Summary: `{"tasks":[{"id":"t1","title":"调研缓存","direction":"读 cache.go 梳理接口"},{"id":"t2","title":"调研选型","direction":"评估第三方库"}]}`}, nil
	}
	return m.mockPromptRunner.RunWithPrompt(ctx, input, extraPrompt)
}

// newCollabTestEngine creates a minimal engine for /collab parallel research testing.
func newCollabTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(&decomposerMockRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
	})
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	return e
}
```

**5b. 新增状态机测试**：

```go
// --- Collab parallel research state machine ---

func TestHandleCollabArena_FullPipeline(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared on completion, got phase %v", e.state.Collab.Phase)
	}
	if !strings.Contains(resp.Summary, "并行研究完成") {
		t.Errorf("report should mention completion, got:\n%s", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "实现一个缓存层") {
		t.Errorf("report should carry the goal, got:\n%s", resp.Summary)
	}
}

func TestHandleCollabArena_DecomposeFailure(t *testing.T) {
	// 用返回非 JSON 的纯 mockPromptRunner（非 decomposerMockRunner）
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{id: AgentSub, response: "## 产出\n采用微服务架构。"},
	})
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab-fail"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err == nil {
		t.Fatal("expected error when decomposer output is not valid JSON")
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared on decompose failure, got phase %v", e.state.Collab.Phase)
	}
}

func TestHandleCollabArena_TasksAllCompleted(t *testing.T) {
	// capture 基建：记录所有 worker 的 MaxIterations（99）与只读工具。
	e, captor := newCaptureCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDecompose,
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if len(captor.workerCalls) != 2 {
		t.Fatalf("expected 2 worker runs, got %d", len(captor.workerCalls))
	}
	allowed := map[string]bool{"read": true, "grep": true, "glob": true, "lsp": true}
	for i, call := range captor.workerCalls {
		if call.maxIterations != collabWorkerMaxIterations {
			t.Errorf("worker %d MaxIterations = %d, want %d", i, call.maxIterations, collabWorkerMaxIterations)
		}
		for _, got := range call.tools {
			if !allowed[got] {
				t.Errorf("worker %d leaked forbidden tool %q: %v", i, got, call.tools)
			}
		}
	}
}
```

**5c. 新增 capture 基建**：

```go
type collabCall struct {
	tools         []string
	maxIterations int
}

// captureCollabRunner implements RunWithPrompt, records worker calls, returns
// valid tasks JSON for the decompose phase and fixed text otherwise.
type captureCollabRunner struct {
	mockPromptRunner
	decomposed  bool
	workerCalls []collabCall
}

func (c *captureCollabRunner) RunWithPrompt(_ context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	if strings.Contains(extraPrompt, "拆解员") && !c.decomposed {
		c.decomposed = true
		return &HandoffResult{Summary: `{"tasks":[{"id":"t1","title":"调研缓存","direction":"读 cache.go"},{"id":"t2","title":"调研选型","direction":"评估第三方库"}]}`}, nil
	}
	if strings.Contains(extraPrompt, "研究员") {
		c.workerCalls = append(c.workerCalls, collabCall{tools: input.Tools, maxIterations: input.MaxIterations})
	}
	return &HandoffResult{Summary: c.response, Conclusions: []string{c.response}}, nil
}

func newCaptureCollabTestEngine(t *testing.T) (*Engine, *captureCollabRunner) {
	t.Helper()
	captor := &captureCollabRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{
				id:       AgentSub,
				response: "## 产出\n采用微服务架构。",
			},
		},
	}
	reg := NewAgentRegistry()
	reg.Register(captor)
	e := &Engine{
		agents: reg,
		state:  &TaskState{TaskID: "test-collab-capture"},
		config: EngineConfig{},
	}
	e.collabHall = NewCollabHall(e)
	return e, captor
}
```

**5d. 删除旧测试**（引用已删类型/函数会编译失败）：
- `TestHandleCollabArena_RunsAllStages`（:40-65）
- `TestCollabStageGoal_CarriesPriorOutputs`（:130-157）
- `TestRenderCollabPrior`（:160-173）
- `TestHandleCollabArena_IdempotentResume`（:178-217）
- `TestHandleCollabArena_DesignGoalHasReconOutput`（:221-241）
- `TestHandleCollabArena_ToolsAllowlist`（:245-282）
- `TestHandleCollabArena_SummaryGenerated`（:286-308）
- `TestCollab_AdvanceConfirm`（:310-349）
- `TestCollab_AdvanceRestart`（:351-367）
- `TestCollab_AdvanceConfirmWithAdjustment_NotRestart`（:369-393）
- `TestRun_CollabExecutesAndConfirms`（:397-451）

保留：`TestParseCollabCommand_Valid`/`TestParseCollabCommand_NotCollab`（:11-36）、`TestParseCollabTasks_*`（任务 1）。

> **注意：** 删除 `TestRun_CollabExecutesAndConfirms` 前确认它引用的 `stubStreamModel`/`stubContextBuilder`/`stubToolExecutor` 等是否被其他测试使用（`turn_test.go` 等）——只删本测试，不动共享基建。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestHandleCollabArena|TestParseCollabTasks|TestParseCollabCommand' -v`
预期：全部 PASS。
再运行：`go test ./context/ -run TestFormatTaskStateVolatile_Collab -v`
预期：PASS（`"phase":"parallel"`、`"tasks":2`）。
最后：`go build ./...`
预期：编译通过（无残留旧类型引用）。

- [ ] **步骤 7：Commit**

```bash
git add engine/types.go engine/collab.go engine/loop.go context/builder.go context/builder_test.go engine/collab_test.go
git commit -m "feat(engine): rework /collab into parallel research state machine"
```

---

### 任务 3：Run() 集成测试 + UI + 文档 + 全量回归

**文件：**
- 测试：`engine/collab_test.go`（新增 Run 集成测试）
- 修改：`ui/model.go`（:95、:3197）
- 修改：`README.md` / `README.zh.md`（/collab 章节）
- 验证：`go build ./...` + `go test ./...`（含 `-race`）

- [ ] **步骤 1：编写失败的测试**

在 `engine/collab_test.go` 追加：

```go
// --- Run() integration ---

func TestRun_CollabParallelResearch(t *testing.T) {
	e := &Engine{
		model:     &stubStreamModel{chunks: []ModelChunk{{Delta: "研究完成。", FinishReason: "stop"}}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TaskID: "test-collab-run"},
		history:   []Message{},
		config:    EngineConfig{ModelName: "test-model", MaxTurns: 10},
		guards:    &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(true)},
		readLoop:  NewLoopTracker(3, 4, false),
		errorLoop: NewLoopTracker(0, 3, true),
	}
	reg := NewAgentRegistry()
	reg.Register(&decomposerMockRunner{
		mockPromptRunner: mockPromptRunner{
			mockSimpleAgent: mockSimpleAgent{id: AgentSub, response: "## 产出\n采用微服务架构。"},
		},
	})
	e.agents = reg
	e.collabHall = NewCollabHall(e)

	resp, err := e.Run(context.Background(), "/collab 研究缓存方案")
	if err != nil {
		t.Fatalf("Run() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab != nil {
		t.Errorf("collab state should be cleared after run, got phase %v", e.state.Collab.Phase)
	}
	if !strings.Contains(resp.Summary, "并行研究完成") {
		t.Errorf("run should surface the research report, got %q", resp.Summary)
	}
}
```

> **逻辑：** `/collab 研究缓存方案` 命令启动块把 `Phase` 设为 `CollabDecompose`；调度块（loop.go 主 loop 之前）先跑 `handleCollabArena`，状态机跑完返回 `buildCollabReport` 响应，Run 直接 return——所以 `resp.Summary` 含"并行研究完成"，主 loop 不执行（stub model 的"研究完成。"不会出现）。

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./engine/ -run TestRun_CollabParallelResearch -v`
预期：PASS。

- [ ] **步骤 3：更新 ui/model.go**

`:95` 描述改为：

```go
	{Command: "/collab", Args: "<需求>", Description: "并行研究：拆解→并行调研→汇总报告"},
```

`:3197` 欢迎文案改为：

```go
	b.WriteString("Use `/collab <需求>` for parallel research: decompose → parallel research → synthesis.\n")
```

- [ ] **步骤 4：更新 README.md**

`:115-121` 章节替换为：

```markdown
### Parallel Research (/collab)

```bash
deepact exec "/collab add a cache layer"
```

A Decomposer agent splits the goal into 2-6 research directions; multiple researcher agents then investigate them **in parallel** with read-only tools (up to 4 concurrent). A Synthesizer merges their findings into one structured research report — faster than serial investigation when you need breadth quickly.
```

- [ ] **步骤 5：更新 README.zh.md**

`:115-121` 章节替换为：

```markdown
### 并行研究（/collab）

```bash
deepact exec "/collab 加一个缓存层"
```

**拆解员** agent 把目标拆成 2~6 个研究方向；多个**研究员** agent 用只读工具**并行**调研（最多 4 个并发）；最后**汇总员**把各方向发现合并成一份结构化研究报告——需要快速摸清广度时，比串行调研更快。
```

- [ ] **步骤 6：全量构建与测试**

运行：`go build ./... && go test ./...`
预期：全部编译通过、全部测试 PASS。
再运行：`go test -race ./engine/ -run TestHandleCollabArena -v`
预期：无数据竞争（并行 worker 各写自己的 `*CollabTask`，goroutine 无共享写）。

- [ ] **步骤 7：Commit**

```bash
git add engine/collab_test.go ui/model.go README.md README.zh.md
git commit -m "feat(ui,docs): integrate /collab parallel research Run test and update docs"
```

---

## 验收标准

- [ ] `/collab <目标>` 触发后：单 Run 内跑完 拆解→并行→汇总→展示报告，无确认门
- [ ] 拆解 agent 输出 JSON 任务列表，解析后 2~6 个任务（超出截断、少于 2 报错）
- [ ] 并行 worker 只读工具 `read/grep/glob/lsp`，`MaxIterations=99`，并发上限 4
- [ ] 单 worker 失败不中断其他，汇总报告标注未完成
- [ ] 汇总 agent 合并报告；失败时 fallback 展示各任务结果
- [ ] 完成后 `Collab` 状态清理，主流程正常
- [ ] `collabVerdictPending` 门控、`Advance`/`handleConfirmation`、`CollabStage`/`CollabStageName` 全部移除
- [ ] context/builder.go `collabVolatile.Tasks` + 新阶段名映射正确
- [ ] UI `/help` 显示新描述，README 双语文档更新
- [ ] `go build ./... && go test ./...` 全部通过；`-race` 无数据竞争

## 风险与回退

- **并行成本**：N 个 worker 并发 = N 倍 token。用户明确选择"更彻底"（MaxIterations=99），接受成本。
- **API 限流**：并发 4 可能触发 DeepSeek 限流。现有 client 有 limiter/retry，失败 worker 标记 failed 不阻塞整体。
- **拆解跑偏**：拆解方向不对则整体方向偏。用户选择全自动（求快），接受此风险；失败任务在汇总报告中可见。
- **数据竞争**：goroutine 并发写各自 `*CollabTask`，`-race` 验证。
