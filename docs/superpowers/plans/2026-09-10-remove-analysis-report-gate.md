# 移除 Analysis Report Gate，以 ask_user 软引导替代 — 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 移除引擎级 Analysis Report Gate（强制"搜索后第一次 edit/write 必须被拦截、要求先输出报告等确认"），在 ask_user 工具描述中加软引导，让模型自主决定何时向用户确认方案。

**架构：** 删除 gate 的强制逻辑与相关状态（`engine/turn.go:385-462` 拦截+降级块、`engine/types.go:262-271` 的 `AnalysisReportConfirmed`、`engine/loop.go` 的 `pendingAnalysisNudge`/`analysisNudgeCount` 字段与全部引用）；`askUserOptions()` 无待决问题时返回 nil；`handleConfirmCommand` 无待决问题时静默消费；在 `askUserToolSpec`（`engine/agent.go:293`）的 desc 中文/英文分支各追加一句软引导。ask_user 全链路（拦截 → `pendingAskUser` → 选项弹窗 / awaiting_user Blocked → `/confirm N` 消费）保持不动。

**技术栈：** Go（engine 包），标准库。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/analysis_gate_test.go` | 仅测已移除的 gate 逻辑 | 删除 |
| `engine/loop_guard_reset_test.go` | 删除 `TestRun_ResetsAnalysisReportConfirmedOnNewRun`（复现旧 bug） | 修改 |
| `engine/progress_loop_test.go` | 删除 `AnalysisReportConfirmed: true` 预置行 | 修改 |
| `engine/confirm_command_test.go` | 适配 gate 状态断言、改造 gate 挂载测试为 ask_user 版、新增回归测试 | 修改 |
| `engine/ask_user_test.go` | 适配 gate 状态断言、`NoPending_FixedTwo` → `Nil`、删除 NoOptions confirm 测试、新增软引导断言 | 修改 |
| `engine/turn.go:385-462` | 删除 gate 拦截块 + 降级块 | 修改 |
| `engine/loop.go` | 删除 `handleAnalysisNudgeConfirmation`、字段、重置、verdict 赋值、Run 结束分支、`askUserOptions` 固定选项、`handleConfirmCommand` 置态/default | 修改 |
| `engine/types.go:262-271` | 删除 `AnalysisReportConfirmed` 字段 | 修改 |
| `engine/agent.go:293-327` | `askUserToolSpec` desc 追加软引导（中/英各一句） | 修改 |

**关键设计约束：** 字段删除（任务 5）必须在所有使用点删除（任务 3、4）之后，保证每个任务结束时 `go build ./engine` 通过。测试适配（任务 1、2）先于代码删除，因为引用的字段此刻仍存在，编译不受影响。

---

### 任务 1：删除 gate 专用测试 + 适配简单测试

**文件：**
- 删除：`engine/analysis_gate_test.go`
- 修改：`engine/loop_guard_reset_test.go:47-81`
- 修改：`engine/progress_loop_test.go:101`

- [ ] **步骤 1：删除 analysis_gate_test.go**

```bash
git rm engine/analysis_gate_test.go
```

- [ ] **步骤 2：删除 loop_guard_reset_test.go 的 AnalysisReportConfirmed 测试**

删除 `engine/loop_guard_reset_test.go` 第 47-81 行——整个 `TestRun_ResetsAnalysisReportConfirmedOnNewRun` 函数及其注释块（`:47-58` 的说明注释 + `:59-81` 的函数体）。保留 `TestRun_ResetsLoopGuardOnNewRun` 与 `TestRun_ResetsReadLoopStateOnNewRun`。

- [ ] **步骤 3：适配 progress_loop_test.go**

`engine/progress_loop_test.go:101`：
```go
state:   &TaskState{TurnNumber: 0, AnalysisReportConfirmed: true},
```
改为：
```go
state:   &TaskState{TurnNumber: 0},
```

- [ ] **步骤 4：运行 engine 测试验证编译通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run 'TestRun_Resets|TestProgress' -timeout 60s`
预期：编译通过、PASS（`AnalysisReportConfirmed`/`pendingAnalysisNudge` 字段此刻仍存在，残留引用不报错）。

- [ ] **步骤 5：Commit**

```bash
git add -A engine/
git commit -m "test: remove analysis-gate tests, adapt simple gate-state references"
```

---

### 任务 2：适配 confirm_command_test.go 与 ask_user_test.go

**文件：**
- 修改：`engine/confirm_command_test.go`
- 修改：`engine/ask_user_test.go`

- [ ] **步骤 1：改造 confirm_command_test.go 的 TestHandleConfirmCommand_ConfirmExecutes**

将 `engine/confirm_command_test.go:31-56` 的 `TestHandleConfirmCommand_ConfirmExecutes` 整体替换为（新语义：无待决问题静默消费）：

```go
// 无待决问题（pendingAskUser == nil）时，/confirm N 静默消费，不改写 history。
// "按报告执行"固定确认语义已随 analysis gate 移除。
func TestHandleConfirmCommand_NoPending_Noop(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: []Message{{Role: "user", Content: "/confirm 1"}},
	}

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if last != "/confirm 1" {
		t.Errorf("history should be unchanged with no pending ask_user, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should stay nil, got %+v", e.pendingAskUser)
	}
}
```

- [ ] **步骤 2：适配 confirm_command_test.go 的 TestHandleConfirmCommand_WithOptions_FirstPlanInjected**

将 `engine/confirm_command_test.go:58-84` 的 `TestHandleConfirmCommand_WithOptions_FirstPlanInjected` 整体替换为（删除对 `AnalysisReportConfirmed`/`pendingAnalysisNudge` 的断言）：

```go
// /confirm 1 选择 ask_user 声明的方案A，确认执行并注入方案描述。
func TestHandleConfirmCommand_WithOptions_FirstPlanInjected(t *testing.T) {
	e := &Engine{
		state:     &TaskState{},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
		pendingAskUser: &AskUserRequest{
			Question: "缓存方案选哪个？",
			Options:  []string{"用 Redis 缓存", "改用 MySQL"},
		},
	}

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "方案A: 用 Redis 缓存") {
		t.Errorf("history should mention 方案A: 用 Redis 缓存, got %q", last)
	}
	if e.pendingAskUser != nil {
		t.Errorf("pendingAskUser should be cleared after selecting 方案A, got %+v", e.pendingAskUser)
	}
}
```

- [ ] **步骤 3：改造 confirm_command_test.go 的 TestConfirmOptions_ReturnedWhenGateIntercepted**

将 `engine/confirm_command_test.go:86-145` 的 `TestConfirmOptions_ReturnedWhenGateIntercepted` 整体替换为（原测试依赖 gate 拦截——移除后改为验证 ask_user 有 options 挂载选项）：

```go
// 模型调用 ask_user（有 options）后 Run 结束挂载选项——无 gate 参与。
func TestConfirmOptions_AskUserWithOptions_Mounted(t *testing.T) {
	askChunks := []ModelChunk{{
		Delta: "缓存方案需要你决定。",
		ToolCalls: []ModelToolCall{
			{ID: "call_ask", Type: "function", Function: ModelFunctionCall{
				Name:      AskUserToolName,
				Arguments: `{"question":"缓存方案选哪个？","options":["用 Redis 缓存","改用 MySQL"]}`,
			}},
		},
		FinishReason: "tool_calls",
		Usage:        &ModelUsage{},
	}}
	model := &multiTurnModel{turns: [][]ModelChunk{askChunks}}
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

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 3 {
		t.Fatalf("expected 3 options, got %d: %v", len(resp.Options), resp.Options)
	}
	if !strings.Contains(resp.Options[len(resp.Options)-1], "意见") {
		t.Errorf("last option should be the free-input item, got %q", resp.Options[len(resp.Options)-1])
	}
}
```

`TestConfirmOptions_NotReturnedWithoutGate`（`engine/confirm_command_test.go:147-167`）保留不动——无 ask_user、无 gate 时正常结束不挂选项，新行为下依然成立。

- [ ] **步骤 4：适配 ask_user_test.go 的 TestAskUserOptions_NoPending_FixedTwo**

将 `engine/ask_user_test.go:155-167` 的 `TestAskUserOptions_NoPending_FixedTwo` 整体替换为（无待决 ask_user 返回 nil，固定两选项已移除）：

```go
// 无待决 ask_user 时返回 nil（固定"按报告执行 / 输入你的意见"选项已随 gate 移除）。
func TestAskUserOptions_NoPending_Nil(t *testing.T) {
	e := &Engine{}
	got := e.askUserOptions()
	if got != nil {
		t.Errorf("expected nil options without pending ask_user, got %v", got)
	}
}
```

- [ ] **步骤 5：删除 ask_user_test.go 的 TestHandleConfirmCommand_NoOptions_ConfirmExecutes**

删除 `engine/ask_user_test.go:194-216` 的 `TestHandleConfirmCommand_NoOptions_ConfirmExecutes`（其语义已由本任务步骤 1 的 `TestHandleConfirmCommand_NoPending_Noop` 覆盖）。

- [ ] **步骤 6：适配 ask_user_test.go 的其余 confirm 测试**

`engine/ask_user_test.go`：
- `TestHandleConfirmCommand_WithOptions_SelectedPlanInjected`（`:218-243`）：删除 `:233-235` 的 `AnalysisReportConfirmed` 断言两行；保留 history/方案B/pendingAskUser 清空断言。
- `TestHandleConfirmCommand_WithOptions_InvalidIndex`（`:245-273`）：删除 `:260-262` 的 `AnalysisReportConfirmed` 断言两行；保留无效提示/非降级/pendingAskUser 清空断言。
- `TestAskUser_ClearedOnFreeInputRun`（`:275-299`）：删除 `:291` 的 `e.pendingAnalysisNudge = true` 一行。

- [ ] **步骤 7：运行 engine 测试验证**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run 'TestHandleConfirmCommand|TestConfirmOptions|TestAskUserOptions|TestAskUser_Cleared' -timeout 60s`
预期：PASS（新语义断言匹配当前仍存在的实现——`handleConfirmCommand` 此刻还置 `AnalysisReportConfirmed`、`askUserOptions` 此刻还返回固定两选项，这两项断言已在步骤 1/4 改为不依赖旧行为）。

- [ ] **步骤 8：Commit**

```bash
git add engine/confirm_command_test.go engine/ask_user_test.go
git commit -m "test: adapt confirm/ask_user tests to gate-free semantics"
```

---

### 任务 3：删除 turn.go 的 gate 拦截块与降级块

**文件：**
- 修改：`engine/turn.go:385-462`

- [ ] **步骤 1：删除 gate 两块逻辑**

`engine/turn.go` 中，保留 `:378-383` 的 "Skill HARD-GATE removed" 注释块，删除其后 `:385-462` 的整个 Analysis report gate 拦截块 + Analysis gate degradation 降级块（从 `// Analysis report gate:` 注释到降级块闭合 `}`）。

删除后 `engine/turn.go` 该处直接衔接：

```go
	// Skill HARD-GATE removed (2026-09-08): the engine no longer blocks
	// edit/write calls while a skill with a pre-implementation gate is active.
	// The skill's own methodology prompt is the authority — the harness stays
	// thin. This eliminates the "agent spins in the red phase without acting"
	// deadlock: an agent under TDD/systematic-debugging trying to write its
	// failing test was HARD-GATE-blocked and could only loop on todo_write/read.

	for _, call := range calls {
```

- [ ] **步骤 2：运行 build 验证编译通过**

运行：`cd /Users/admin/gitspace/deepact && go build ./engine/`
预期：编译通过（`AnalysisReportConfirmed`/`analysisNudgeCount`/`pendingAnalysisNudge` 字段此刻仍存在，只是不再被 turn.go 引用）。

- [ ] **步骤 3：运行 gate 相关测试确认行为变更**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run 'TestExecuteTurn' -timeout 60s`
预期：PASS（`TestExecuteTurn_AnalysisGateDegradation_BatchesEdits` 已在任务 1 删除；其余 TestExecuteTurn 测试不依赖 gate 拦截）。

- [ ] **步骤 4：Commit**

```bash
git add engine/turn.go
git commit -m "feat(engine): remove analysis report gate blocking from turn execution"
```

---

### 任务 4：删除 loop.go 的 handleAnalysisNudgeConfirmation 及其调用点

**文件：**
- 修改：`engine/loop.go:487-490`（调用点 + 注释）
- 修改：`engine/loop.go:1645-1690`（方法定义）

- [ ] **步骤 1：删除调用点**

`engine/loop.go:487-490` 删除：

```go
	// Analysis report nudge: if the gate blocked in the previous Run() and the
	// agent produced a text-only analysis report, handle the user's response
	// (confirmation or feedback) before any other processing.
	e.handleAnalysisNudgeConfirmation(userMsg)
```

删除后 `engine/loop.go` 该处衔接为：

```go
	e.handleConfirmCommand(userMsg)

	// 自由输入路径：用户未通过 /confirm N 响应弹出框（走"输入你的意见"
```

- [ ] **步骤 2：删除方法定义**

删除 `engine/loop.go:1645-1690` 的整个 `handleAnalysisNudgeConfirmation` 方法（含 `:1645-1650` 注释）。删除后 `engine/loop.go` 该处衔接为 `loadPersistentMemory` 的注释块。

- [ ] **步骤 3：运行 build + 测试验证**

运行：`cd /Users/admin/gitspace/deepact && go build ./engine/ && go test ./engine/ -count=1 -timeout 60s`
预期：编译通过、全部 PASS。

- [ ] **步骤 4：Commit**

```bash
git add engine/loop.go
git commit -m "refactor(engine): remove handleAnalysisNudgeConfirmation and call site"
```

---

### 任务 5：删除字段与全部残留引用

**文件：**
- 修改：`engine/types.go:262-271`
- 修改：`engine/loop.go`（字段、重置、verdict、Run 结束分支、askUserOptions、handleConfirmCommand、注释）

- [ ] **步骤 1：删除 types.go 的 AnalysisReportConfirmed 字段**

删除 `engine/types.go:262-271` 的整个注释 + 字段。删除后 `engine/types.go` 该处衔接为：

```go
	ReadHistory []ReadRecord `json:"read_history"`

	// PlanConfirmed is set when the user confirms a plan presented by the
```

- [ ] **步骤 2：删除 loop.go 字段声明**

删除 `engine/loop.go:103-108` 的 `pendingAnalysisNudge` 注释 + 字段声明，及 `:110-114` 的 `analysisNudgeCount` 注释 + 字段声明。删除后 `engine/loop.go` 该处衔接为：

```go
	pendingAskUser *AskUserRequest

	// roundtableHall orchestrates multi-stance roundtable discussions.
	roundtableHall *RoundtableHall
```

同时更新 `engine/loop.go:123-126` 的 `teamVerdictPending` 注释：

```go
	// teamVerdictPending is set when the user's roundtable verdict is processed.
	// On the next Run(), it causes PlanConfirmed to be set, skipping the
	// edit-plan guard - the user already approved the plan through the debate
	// process.
	teamVerdictPending bool
```

- [ ] **步骤 3：删除 Run 入口重置**

删除 `engine/loop.go:327` 的 `e.analysisNudgeCount = 0` 与 `:328-336` 的注释块、`:337` 的 `e.state.AnalysisReportConfirmed = false`。删除后 `engine/loop.go` 该处衔接为：

```go
	e.runToolCallCount = 0
	e.runErrorCount = 0
```

- [ ] **步骤 4：删除 team/collab verdict 赋值**

`engine/loop.go:767-772`：
```go
	if e.teamVerdictPending {
		e.state.PlanConfirmed = true
		e.teamVerdictPending = false
		loopLog.Printf("team verdict: PlanConfirmed=true, skipping confirmation gates")
	}
```
`engine/loop.go:774-780`：
```go
	if e.collabVerdictPending {
		e.state.PlanConfirmed = true
		e.collabVerdictPending = false
		loopLog.Printf("collab verdict: PlanConfirmed=true, skipping confirmation gates")
	}
```
（各删除 `e.state.AnalysisReportConfirmed = true` 一行。）

- [ ] **步骤 5：删除 Run 结束的 analysisNudgeCount 分支 + 简化 askUserOptions**

`engine/loop.go:1010-1022` 的 Analysis gate confirmation 分支整体删除（保留 `:1000-1009` 的 ask_user 分支与 `:1023` 的默认 return）。

`engine/loop.go:1026-1047` 的 `askUserOptions` 替换为：

```go
// askUserOptions returns the presentation options after a Run.
// Pending ask_user with options → 方案A/B/C... plus the free-input entry.
// Pending ask_user without options → nil (the question is presented via the
// awaiting_user Blocked path, user answers freely). No pending ask_user →
// nil (no options popup; the model decides whether to ask via ask_user).
func (e *Engine) askUserOptions() []string {
	if e.pendingAskUser == nil {
		return nil
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

- [ ] **步骤 6：简化 handleConfirmCommand**

`engine/loop.go:1601-1643` 的注释与方法体替换为：

```go
// handleConfirmCommand processes a /confirm N message deterministically,
// bypassing isDangerousConfirmation.
//
// When the agent declared options via ask_user (pendingAskUser with a non-empty
// Options list), /confirm N selects 方案N and the choice is injected into history
// so the agent implements the selected plan; an out-of-range N injects an
// "invalid option number" feedback. With no pending ask_user, /confirm N is a
// silent no-op (consumed but produces no history rewrite). The last popup item
// ("输入你的意见") never reaches here — the UI returns to the input box.
// Returns true if userMsg was a valid /confirm command.
func (e *Engine) handleConfirmCommand(userMsg string) bool {
	n, ok := parseConfirmCommand(userMsg)
	if !ok {
		return false
	}
	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		switch {
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0 && n >= 1 && n <= len(e.pendingAskUser.Options):
			label := confirmOptionLabel(n-1, e.pendingAskUser.Options[n-1])
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了：%s，请按该方案执行修改。", label)
		case e.pendingAskUser != nil && len(e.pendingAskUser.Options) > 0:
			// 有声明方案但编号越界（n < 1 或 n > len(options)）：明确告知
			// agent 用户选择无效，由其决定下一步。
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了无效的方案编号 %d，请重新选择。", n)
			loopLog.Printf("handleConfirmCommand: /confirm %d out of range (pending options=%d)", n, len(e.pendingAskUser.Options))
		}
	}
	// 本组问题已消费（用户已选择或越界），清除避免残留到无关 Run。
	e.pendingAskUser = nil
	loopLog.Printf("handleConfirmCommand: /confirm %d processed", n)
	return true
}
```

- [ ] **步骤 7：删除 clearSessionState 重置**

`engine/loop.go:1778-1781`：
```go
	e.pendingEditPlan = nil
	e.pendingAnalysisNudge = false
	e.analysisNudgeCount = 0
	e.state.AnalysisReportConfirmed = false
```
改为：
```go
	e.pendingEditPlan = nil
```

- [ ] **步骤 8：运行 build + 全量 engine 测试**

运行：`cd /Users/admin/gitspace/deepact && go build ./... && go test ./engine/ -count=1 -timeout 60s`
预期：编译通过、全部 PASS。

- [ ] **步骤 9：grep 验证无残留**

运行：
```bash
cd /Users/admin/gitspace/deepact && grep -rn "AnalysisReportConfirmed\|analysisNudgeCount\|pendingAnalysisNudge" engine/ --include="*.go"; echo "exit=$?"
```
预期：无输出（`exit=1` 表示 grep 无匹配）。

- [ ] **步骤 10：Commit**

```bash
git add engine/types.go engine/loop.go
git commit -m "refactor(engine): remove analysis-gate state (AnalysisReportConfirmed, pendingAnalysisNudge, analysisNudgeCount)"
```

---

### 任务 6：askUserToolSpec 软引导（TDD）

**文件：**
- 修改：`engine/agent.go:293-327`
- 测试：`engine/ask_user_test.go`（新增）

- [ ] **步骤 1：编写失败的测试**

`engine/ask_user_test.go` 追加：

```go
// askUserToolSpec 描述需包含软引导（建议改代码前用 ask_user 让用户确认方案），
// 按会话语言单一渲染。这是能力层引导，替代已移除的引擎级 analysis gate。
func TestAskUserToolSpec_SoftGuidance(t *testing.T) {
	zhSpec := askUserToolSpec(true)
	if !strings.Contains(zhSpec.Function.Description, "建议先用本工具向用户确认") {
		t.Errorf("zh desc should contain soft guidance, got %q", zhSpec.Function.Description)
	}
	enSpec := askUserToolSpec(false)
	if !strings.Contains(enSpec.Function.Description, "consider confirming with the user first") {
		t.Errorf("en desc should contain soft guidance, got %q", enSpec.Function.Description)
	}
}
```

（确认 `engine/ask_user_test.go` 已 import `strings`；若未 import，添加。）

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run TestAskUserToolSpec_SoftGuidance -v`
预期：FAIL——当前 desc 不含软引导措辞。

- [ ] **步骤 3：实现软引导**

`engine/agent.go:294` 的英文 desc 末尾追加：

```
 Additionally, before you start modifying code, if your planned changes involve tradeoffs or design choices the user should weigh in on, consider confirming with the user first via this tool (you can provide plan options). This is a suggestion, not a requirement — decide based on the task.
```

`engine/agent.go:298` 的中文 desc 末尾追加：

```
另外，在开始修改代码之前，若你的改动计划存在需用户取舍的权衡或方案选择，建议先用本工具向用户确认（可提供方案选项）。这是建议而非强制，是否确认由你根据任务自主决定。
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run TestAskUserToolSpec_SoftGuidance -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/agent.go engine/ask_user_test.go
git commit -m "feat(engine): add ask_user soft guidance for plan confirmation (replaces analysis gate)"
```

---

### 任务 7：新增回归测试

**文件：**
- 修改：`engine/confirm_command_test.go`（追加）

- [ ] **步骤 1：编写回归测试**

`engine/confirm_command_test.go` 追加：

```go
// 回归测试：移除 analysis gate 后，搜索过代码（runToolCallCount > 0）再直接
// 提交 edit 不再被拦截——模型自主决定是否用 ask_user 确认。
func TestExecuteTurn_EditAfterSearch_NotBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "修改代码",
			ToolCalls: []ModelToolCall{
				{ID: "call_edit", Type: "function", Function: ModelFunctionCall{
					Name:      "edit",
					Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`,
				}},
			},
			FinishReason: "tool_calls",
		}}},
		context:          &stubContextBuilder{},
		tools:            &recordingToolExecutor{},
		state:            &TaskState{TurnNumber: 0},
		history:          []Message{{Role: "user", Content: "改"}},
		config:           EngineConfig{ModelName: "test-model"},
		guards:           &GuardSystem{loop: NewLoopGuard("", 6), scope: NewScopeGuard(true)},
		runToolCallCount: 2, // 已做过搜索
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected edit to execute without analysis-gate blocking")
	}
}
```

（`recordingToolExecutor`、`NewScopeGuard`、`NewLoopGuard`、`stubStreamModel`、`stubContextBuilder` 均在既有测试中定义，直接复用。）

- [ ] **步骤 2：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -run 'TestExecuteTurn_EditAfterSearch_NotBlocked|TestConfirmOptions|TestHandleConfirmCommand' -timeout 60s`
预期：PASS。

- [ ] **步骤 3：Commit**

```bash
git add engine/confirm_command_test.go
git commit -m "test: verify edit after search is not blocked without analysis gate"
```

---

### 任务 8：全量构建 + 测试 + 残留验证

**文件：**
- 无代码改动

- [ ] **步骤 1：全量构建 + 测试**

运行：`cd /Users/admin/gitspace/deepact && go build ./... && go test ./... -count=1 -timeout 120s`
预期：全部编译通过、全部 PASS。

- [ ] **步骤 2：残留引用验证**

运行：
```bash
cd /Users/admin/gitspace/deepact && grep -rn "AnalysisReportConfirmed\|analysisNudgeCount\|pendingAnalysisNudge" engine/ --include="*.go"
grep -rn "按报告执行" engine/ ui/ --include="*.go"
grep -rn "handleAnalysisNudgeConfirmation" . --include="*.go"
```
预期：三组均无输出。

- [ ] **步骤 3：Commit（如有测试/修复遗留）**

```bash
git add -A
git commit -m "test: e2e regression for analysis-gate removal"
```

---

## 自检

**1. 规格覆盖度：**
| 规格章节 | 对应任务 |
|---|---|
| 移除 gate 拦截块 + 降级块 | 任务 3 |
| 删除 `AnalysisReportConfirmed` 字段 | 任务 5 步骤 1 |
| 删除 `pendingAnalysisNudge`/`analysisNudgeCount` 字段 | 任务 5 步骤 2 |
| Run 入口重置删除 | 任务 5 步骤 3 |
| team/collab verdict 赋值删除（保留 PlanConfirmed） | 任务 5 步骤 4 |
| Run 结束 analysisNudgeCount 分支删除 | 任务 5 步骤 5 |
| `askUserOptions` 无 pending 返回 nil | 任务 5 步骤 5 |
| `handleConfirmCommand` 置态/default 分支删除 | 任务 5 步骤 6 |
| `handleAnalysisNudgeConfirmation` 删除 | 任务 4 |
| `clearSessionState` 重置删除 | 任务 5 步骤 7 |
| `analysis_gate_test.go` 删除 | 任务 1 |
| `loop_guard_reset_test.go` 删函数 | 任务 1 |
| confirm/ask_user 测试适配 | 任务 2 |
| turn/progress 测试编译适配 | 任务 1 步骤 3 |
| 软引导（askUserToolSpec desc） | 任务 6 |
| 软引导断言测试 | 任务 6 步骤 1 |
| 搜索后直接 edit 不被拦截回归 | 任务 7 |
| ask_user 有 options 挂载回归 | 任务 2 步骤 3 + 既有 ask_user_ends_run_test.go |
| grep 无残留验证 | 任务 5 步骤 9、任务 8 步骤 2 |

**2. 占位符扫描：** 所有删除步骤给出精确行号与替换后衔接代码；测试步骤包含完整代码；无 "类似任务 N"、"适当错误处理"、"待定" 等占位。

**3. 类型一致性：** `askUserOptions()` 签名不变（`func (e *Engine) askUserOptions() []string`）；`handleConfirmCommand` 签名不变（`func (e *Engine) handleConfirmCommand(userMsg string) bool`）；`askUserToolSpec(zh bool) ModelTool` 签名不变；测试中复用的 `recordingToolExecutor`/`NewScopeGuard`/`NewLoopGuard`/`stubStreamModel`/`stubContextBuilder`/`multiTurnModel`/`steerContextBuilder` 均为既有测试桩，不新增符号。任务 2 步骤 3 引用的 `AskUserToolName` 在 `engine/agent.go:22` 定义，保持不变。
