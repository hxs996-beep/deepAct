# /team 辩论模式：共享代码搜索 + 胜者判定 + 实施蓝图 — 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 改造 `/team` 辩论模式的三处不足——① 辩论前先用一个子 agent 搜索代码库，把结构化调研报告作为共享基线注入所有成员；② 成员全部走完整工具循环（可自行 grep/read 验证），不再用单次推理 fast path；③ 终陈后自动判定"平均分最高"的胜者（并列用质询数据 tiebreak），并用一次 LLM 调用把胜者方案重写为详细可执行实施蓝图，替换当前过于简略的摘要界面。

**架构：** 在 `RoundtableState` 新增 `SharedContext` / `WinnerID` / `Blueprint` 三字段。预搜索复用现有 `AgentSub`（genericSubAgent）跑一次 `read/grep/glob/lsp`，结果存入 `SharedContext`。`handleDebateArena` 在提案轮前幂等地执行预搜索；`buildDebateGoal` 把 `SharedContext` 注入每轮提示。删除 `runMemberSingleShot` fast path，成员一律走 `RunWithPrompt` sub-agent 工具循环。终陈轮后新增 `determineWinner`（解析 SCORE 平均分 + tiebreak）与 `buildBlueprint`（一次 LLM 调用生成蓝图），`buildVerdictPrompt` 重写为"胜者 + 评分总览 + 蓝图"三段式，移除 VERDICT 投票统计。

**技术栈：** Go（engine/ 包）、标准库、BurntSushi/toml（已有依赖）。

---

## 背景与动机（为什么这么改）

当前 `/team` 的三处问题，用户明确指出：

1. **讨论靠"凭空推理"**：成员走 `runMemberSingleShot` 单次推理 fast path（`engine/roundtable.go:325-337`），不读代码就提方案，产出多是空泛观点而非可落地方案。
2. **胜者判定含糊**：终陈后只展示评分表 + 一段 LLM 摘要，由用户自行裁决；用户希望**自动告知"最被大家接受的方案"**（平均分最高者），并**详细列出该方案**。
3. **结果"不能用"**：因为方案从未扎根到真实代码，且展示过简。

用户拍板的决策（已在头脑风暴中确认）：
- **方向 1（收敛）**：移除投票（`VERDICT`），只认平均分；并列用质询轮数据 tiebreak；胜者方案重写为详细实施蓝图。
- **方向 2（互助搜索）**：辩论前先跑一次搜索子 agent，结果注入 4 个性格；**成员保留各自 read/grep/glob/lsp 能力自行验证**（共享 + 自查）。
- **质量优先**：不因速度砍工具循环——全部 4 轮成员都走完整 sub-agent 工具循环。

## 关键前提（已核实，实现时无需再查）

- **fast path 位置**：`engine/roundtable.go:320-337`（`if h.engine.model != nil` 分支调 `runMemberSingleShot`）；`runMemberSingleShot` 定义在 `:396-414`；`memberRolePrompt` 在 `:419-425`；常量 `roundtableMemberMaxOutputTokens` 在 `:24-25`。
- **`RoundtableState`**：`engine/types.go:437-443`，现有字段 `Goal/Phase/Members/DebateRounds`；`RoundtablePhase` 常量在 `:408-416`。
- **`buildDebateGoal` 签名**：`engine/roundtable.go:428`，当前 6 参（`goal, member, phase, allMembers, rounds, zh`），switch 前是空的 `var sb strings.Builder`（`:429`），可在 switch 前统一注入共享调研。
- **`handleDebateArena` 结构**：`engine/roundtable.go:136-186`，按 `phase` 顺序跑 4 轮，第 4 轮后置 `RoundtableAwaitingVerdict`，随后 `synthesizeDebate` + `buildVerdictPrompt`。
- **`buildScoreTable`**：`engine/roundtable.go:777`，已解析 SCORE 平均分并渲染表格（含 ★ 标记最高）；`extractConfidence`/`splitChallengeBlocks`/`extractHighConfidenceChallenges` 在 `:889-964`，可直接复用做 tiebreak。
- **`getMemberOutput`**：`engine/roundtable.go:982`，按 memberID 取某轮输出，取胜者提案用。
- **LLM 单次调用模式**：`synthesizeDebate`（`:191-244`）已示范"用 `AgentSub` + `RunWithPrompt` 跑一次、返回 `result.Summary`、`accumulateUsage`"——`buildBlueprint` 完全照此模式。
- **测试基建**：`engine/roundtable_test.go` 的 `newTestEngine`（`:143`）注册了 `mockPromptRunner`（实现 `RunWithPrompt`，`:135-140`），`handleDebateArena` 在 `e.model == nil` 时即走 sub-agent 路径，故现有测试不依赖 fast path。
- **不需要改 loop.go**：`/team` 命令启动逻辑（`engine/loop.go:315-348`）不变；预搜索在 `handleDebateArena` 内幂等执行（`SharedContext == ""` 时跑一次），`loop.go` 的辩论阶段分派（`:692-722`）和 `teamVerdictPending` 门控（`:730-736`）均无需改动。
- **不需要改 context/builder.go**：`flattenRoundtable`（`context/builder.go:348`）只渲染 volatile 展示字段；`SharedContext/WinnerID/Blueprint` 是引擎状态，随 `TaskState` JSON 持久化（新字段 `omitempty`，向后兼容旧 session），无需加入 volatile。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/types.go` | `RoundtableState` 新增 `SharedContext`/`WinnerID`/`Blueprint` 字段 | 修改 |
| `engine/roundtable.go` | 预搜索、删 fast path、SharedContext 注入、胜者判定、蓝图生成、裁决界面重写 | 修改 |
| `engine/roundtable_test.go` | 更新既有测试（去投票断言）+ 新增各功能测试 | 修改 |

---

### 任务 1：预搜索共享上下文（SharedContext 全链路）

**文件：**
- 修改：`engine/types.go`（`RoundtableState`，`:437-443`）
- 修改：`engine/roundtable.go`（常量区 + `runSharedSearch` + `buildSearchGoal` + `handleDebateArena` + `runMemberDebateTurn` + `buildDebateGoal`）
- 测试：`engine/roundtable_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/roundtable_test.go` 末尾追加：

```go
// --- SharedContext pre-search ---

func TestRunSharedSearch_StoresContext(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "实现缓存层",
		Phase:   RoundtableProposal,
		Members: DefaultDebateMembers[:2],
	}

	resp, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Roundtable.SharedContext == "" {
		t.Error("expected SharedContext to be populated by pre-search")
	}
	// mockPromptRunner.Run returns the fixed response text (含"采用微服务架构")
	if !strings.Contains(e.state.Roundtable.SharedContext, "采用微服务架构") {
		t.Errorf("SharedContext should contain the mock search result, got %q", e.state.Roundtable.SharedContext)
	}
}

func TestRunSharedSearch_RunsOnce(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:           "实现缓存层",
		Phase:          RoundtableProposal,
		Members:        DefaultDebateMembers[:1],
		SharedContext:  "already-searched", // 模拟已搜索过
	}

	// 手动把 SharedContext 置为已存在值后，pre-search 应跳过（幂等）
	// handleDebateArena 不再复写 SharedContext
	_, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}
	if e.state.Roundtable.SharedContext != "already-searched" {
		t.Errorf("SharedContext overwritten: got %q, want %q", e.state.Roundtable.SharedContext, "already-searched")
	}
}

func TestBuildDebateGoal_IncludesSharedContext(t *testing.T) {
	prompt := buildDebateGoal("测试需求", DefaultDebateMembers[0], DebateProposal,
		DefaultDebateMembers[:1], nil, true, "共享调研: cache.go 已有 TTL 相关代码")
	if !strings.Contains(prompt, "共享调研") {
		t.Errorf("prompt should include shared context, got:\n%s", prompt)
	}
	// 空上下文不注入共享区
	prompt2 := buildDebateGoal("测试需求", DefaultDebateMembers[0], DebateProposal,
		DefaultDebateMembers[:1], nil, true, "")
	if strings.Contains(prompt2, "共享代码调研") {
		t.Errorf("prompt should not include shared section when context is empty, got:\n%s", prompt2)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestRunSharedSearch|TestBuildDebateGoal_IncludesSharedContext' -v`
预期：编译失败——`RoundtableState` 无 `SharedContext` 字段、`buildDebateGoal` 仍为 6 参。

- [ ] **步骤 3：`types.go` 扩展 RoundtableState**

修改 `engine/types.go:437-443`：

```go
// RoundtableState tracks the current roundtable session within TaskState.
type RoundtableState struct {
	Goal         string             `json:"goal"`
	Phase        RoundtablePhase    `json:"phase"`
	Members      []RoundtableMember `json:"members"`
	DebateRounds []DebateRound      `json:"debate_rounds"` // 替代 Proposals + Reviews
	// SharedContext 是预搜索子 agent 产出的代码调研报告，作为所有辩论成员的共享基线。
	SharedContext string `json:"shared_context,omitempty"`
	// WinnerID 是终陈后判定的平均分最高成员 ID。
	WinnerID string `json:"winner_id,omitempty"`
	// Blueprint 是胜者方案的详细实施蓝图（LLM 生成）。
	Blueprint string `json:"blueprint,omitempty"`
}
```

- [ ] **步骤 4：`roundtable.go` 新增预搜索常量与函数**

在 `engine/roundtable.go:25` 的 `roundtableMemberMaxOutputTokens` 常量后追加：

```go
// roundtableSearchMaxIterations bounds the pre-debate shared search agent.
// A single codebase scan needs a bit more budget than a debate member's
// reasoning turn (15), but is still capped to bound wall-clock.
const roundtableSearchMaxIterations = 25
```

在 `handleDebateArena` 之前新增两个函数（放在 `NewRoundtableHall` 附近）：

```go
// runSharedSearch runs a single code-search sub-agent that scans the repository
// for context relevant to the debate goal, returning a structured report used
// as the shared baseline for all debate members. Returns "" on failure so the
// debate can proceed without it (members still have their own tools).
func (h *RoundtableHall) runSharedSearch(ctx context.Context, goal string, zh bool) string {
	if h.engine.config.OnProgress != nil {
		// 事件类型用 "team_search" 而非 "debate_phase"：
		// TestDebateArena_ProgressEvents (roundtable_test.go:355-362) 断言恰好
		// 4 个 debate_phase 事件（4 轮辩论），预搜索不可计入该轮次计数。
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "team_search",
			Name:   "search",
			Detail: pickPrompt(zh, "Searching codebase...", "正在搜索代码库..."),
		})
	}
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          buildSearchGoal(goal, zh),
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: roundtableSearchMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}
	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

// buildSearchGoal instructs the pre-debate search agent to produce a structured
// codebase report for the debate goal. Research-only: it must not propose solutions.
func buildSearchGoal(goal string, zh bool) string {
	return fmt.Sprintf(pickPrompt(zh,
		`## Task
Search the codebase for context relevant to the following requirement. Produce a concise, structured report that will be shared with multiple debate members as their baseline.

## Requirement
%s

## Report Format
- **Relevant files**: exact paths + one-line purpose each
- **Key code**: short snippets or precise references (function/type names + file:line) that the requirement touches
- **Constraints**: existing conventions, interfaces, callers that constrain changes
- **Risks**: hotspots, edge cases, likely failure points

Be factual and cite file paths. Do NOT propose solutions — this is research only.`,
		`## 任务
搜索代码库中与以下需求相关的上下文。产出一份简洁、结构化的调研报告，将作为多名辩论成员的共享基线。

## 需求
%s

## 报告格式
- **相关文件**：精确路径 + 每行一句话用途
- **关键代码**：简短片段或精确引用（函数/类型名 + file:行号），说明需求涉及哪些代码
- **约束**：现有约定、接口、调用方对改动的限制
- **风险**：热点、边界情况、可能的失败点

务必基于事实并引用文件路径。不要提方案——这只是调研。`), goal)
}
```

- [ ] **步骤 5：`handleDebateArena` 幂等注入预搜索**

修改 `engine/roundtable.go:136-150`，在 `members` 解析之后、`phase := state.Roundtable.Phase` 之前插入：

```go
	// Pre-search: run the codebase scan once as the shared baseline for all
	// members. Idempotent — skipped if already populated (re-entry after a
	// partial failure or a "debate again" round).
	if state.Roundtable.SharedContext == "" {
		state.Roundtable.SharedContext = h.runSharedSearch(ctx, goal, zh)
	}
```

- [ ] **步骤 6：`runMemberDebateTurn` 传入 SharedContext + `buildDebateGoal` 注入**

修改 `engine/roundtable.go:307`：

```go
	taskGoal := buildDebateGoal(goal, member, phase, allMembers, h.engine.state.Roundtable.DebateRounds, zh, h.engine.state.Roundtable.SharedContext)
```

修改 `engine/roundtable.go:428` 的 `buildDebateGoal` 签名，并在 `var sb strings.Builder` 之后、`switch phase {` 之前注入共享调研：

```go
func buildDebateGoal(goal string, member RoundtableMember, phase DebateRoundPhase, allMembers []RoundtableMember, rounds []DebateRound, zh bool, sharedContext string) string {
	var sb strings.Builder

	if sharedContext != "" {
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Shared Code Research\nShared findings from a code-search agent. Use as a baseline, but verify with your own tools before relying on them.\n\n%s\n\n",
			"## 共享代码调研\n代码搜索 agent 的共享调研结果。作为基线使用，但请用你自己的工具核实后再依赖。\n\n%s\n\n"), sharedContext))
	}

	switch phase {
```

（`switch` 内部各 case 不动。）

**必须同步更新既有测试**（否则改签名后编译失败）：`engine/roundtable_test.go:548` 的 `TestBuildDebateGoal_FinalRoundInstructsMemberID` 原用 6 参调用，改为 7 参（末尾补空上下文），断言保持不变：

```go
	prompt := buildDebateGoal("测试需求", DefaultDebateMembers[0], DebateFinal, DefaultDebateMembers[:2], rounds, true, "")
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./engine/ -run 'TestRunSharedSearch|TestBuildDebateGoal_IncludesSharedContext' -v`
预期：全部 PASS。

- [ ] **步骤 8：Commit**

```bash
git add engine/types.go engine/roundtable.go engine/roundtable_test.go
git commit -m "feat(engine): add shared code pre-search to /team debate"
```

---

### 任务 2：删除 fast path，成员全部走工具循环

**文件：**
- 修改：`engine/roundtable.go`（`runMemberDebateTurn`、删 `runMemberSingleShot`/`memberRolePrompt`/`roundtableMemberMaxOutputTokens`）
- 测试：`engine/roundtable_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/roundtable_test.go` 追加：

```go
// --- Member uses sub-agent tool loop even when model is set ---

func TestDebateMember_UsesSubAgentPathWithModel(t *testing.T) {
	e := newTestEngine(t)
	// 即使设置了 model（旧 fast path 的触发条件），成员也必须走 sub-agent
	// 工具循环，而非单次推理 fast path。
	e.model = &stubStreamModel{chunks: []ModelChunk{{Delta: "fast-path-would-be-here", FinishReason: "stop"}}}
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试",
		Phase:   RoundtableProposal,
		Members: DefaultDebateMembers[:1],
	}

	_, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}
	if len(e.state.Roundtable.DebateRounds) == 0 {
		t.Fatal("expected at least one debate round")
	}
	out := e.state.Roundtable.DebateRounds[0].Outputs[0]
	if strings.Contains(out.Content, "fast-path-would-be-here") {
		t.Error("member used single-shot fast path instead of the sub-agent tool loop")
	}
	// mockPromptRunner.RunWithPrompt 返回固定 response（含"采用微服务架构"）
	if !strings.Contains(out.Content, "采用微服务架构") {
		t.Errorf("expected mock sub-agent output, got %q", out.Content)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestDebateMember_UsesSubAgentPathWithModel -v`
预期：FAIL——`out.Content` 含 `fast-path-would-be-here`（走了 fast path）。

- [ ] **步骤 3：删除 fast path**

修改 `engine/roundtable.go`，删除 `runMemberDebateTurn` 中的 fast path 块（`:320-337`），使其直接走 sub-agent 路径。删除后函数体从 `taskGoal := ...` 直接进入 `handoff := Handoff{...}`：

```go
	taskGoal := buildDebateGoal(goal, member, phase, allMembers, h.engine.state.Roundtable.DebateRounds, zh, h.engine.state.Roundtable.SharedContext)
	targets := determineTargets(member.ID, phase, allMembers)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: roundtableMemberMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
```

同时删除整段 fast path 注释块（`:320-337` 的 `// FAST PATH (production): ...` 到 `return DebateOutput{MemberID: member.ID, Content: content, Targets: targets}`）。

删除已无引用的孤儿函数与常量：
- `runMemberSingleShot`（`:396-414`）
- `memberRolePrompt`（`:419-425`）
- `roundtableMemberMaxOutputTokens`（`:24-25`）

> 注意：`RoundtableMember.Prompt`/`displayPrompt` 仍被 `RunWithPrompt(ctx, handoff, member.displayPrompt(zh))` 使用（`:357`），必须保留。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestDebateMember_UsesSubAgentPathWithModel|TestDebateArena' -v`
预期：全部 PASS（含既有 `TestDebateArena_*` 系列）。

- [ ] **步骤 5：Commit**

```bash
git add engine/roundtable.go engine/roundtable_test.go
git commit -m "refactor(engine): force /team members through sub-agent tool loop"
```

---

### 任务 3：胜者判定（平均分 + 并列 tiebreak）

**文件：**
- 修改：`engine/roundtable.go`（新增 `determineWinner` + `countHighConfidenceTargeting`；`handleDebateArena` 调用）
- 测试：`engine/roundtable_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/roundtable_test.go` 追加：

```go
// --- Winner determination ---

func buildFinalRounds() []DebateRound {
	return []DebateRound{
		{Phase: DebateProposal, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "创新派方案"},
			{MemberID: "defender", Content: "防守派方案"},
		}},
		{Phase: DebateChallenge, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "### 挑战: 防守派\n过于保守\nCONFIDENCE: 0.9", Targets: []string{"defender"}},
			{MemberID: "defender", Content: "### 挑战: 创新派\n重构风险\nCONFIDENCE: 0.5", Targets: []string{"radical"}},
		}},
		{Phase: DebateRebuttal, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "反驳"},
			{MemberID: "defender", Content: "反驳"},
		}},
		{Phase: DebateFinal, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "最终立场\nSCORE: radical = 90\nSCORE: defender = 70\nVERDICT: radical"},
			{MemberID: "defender", Content: "最终立场\nSCORE: radical = 75\nSCORE: defender = 85\nVERDICT: defender"},
		}},
	}
}

func TestDetermineWinner_ByAverageScore(t *testing.T) {
	rounds := []DebateRound{
		{Phase: DebateProposal, Outputs: []DebateOutput{{MemberID: "radical", Content: "A"}, {MemberID: "defender", Content: "B"}}},
		{Phase: DebateChallenge, Outputs: []DebateOutput{{MemberID: "radical", Content: "c", Targets: []string{"defender"}}}},
		{Phase: DebateRebuttal, Outputs: []DebateOutput{{MemberID: "radical", Content: "r"}, {MemberID: "defender", Content: "r"}}},
		{Phase: DebateFinal, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "SCORE: radical = 90\nSCORE: defender = 70"},
			{MemberID: "defender", Content: "SCORE: radical = 80\nSCORE: defender = 90"},
			{MemberID: "pragmatic", Content: "SCORE: radical = 85\nSCORE: defender = 75"},
		}},
	}
	w := determineWinner(DefaultDebateMembers[:2], rounds)
	if w == nil {
		t.Fatal("expected a winner")
	}
	// radical avg = (90+80+85)/3 = 85 > defender avg = (70+90+75)/3 = 78.3
	if w.ID != "radical" {
		t.Errorf("winner = %q, want radical", w.ID)
	}
}

func TestDetermineWinner_TiebreakByFewerChallenges(t *testing.T) {
	rounds := []DebateRound{
		{Phase: DebateProposal, Outputs: []DebateOutput{{MemberID: "radical", Content: "A"}, {MemberID: "defender", Content: "B"}}},
		{Phase: DebateChallenge, Outputs: []DebateOutput{
			// radical 被 1 个高置信(0.9)挑战；defender 无
			{MemberID: "pragmatic", Content: "### 挑战: radical\n风险高\nCONFIDENCE: 0.9", Targets: []string{"radical"}},
		}},
		{Phase: DebateRebuttal, Outputs: []DebateOutput{{MemberID: "radical", Content: "r"}, {MemberID: "defender", Content: "r"}}},
		{Phase: DebateFinal, Outputs: []DebateOutput{
			{MemberID: "radical", Content: "SCORE: radical = 80\nSCORE: defender = 80"},
			{MemberID: "defender", Content: "SCORE: radical = 80\nSCORE: defender = 80"},
		}},
	}
	w := determineWinner(DefaultDebateMembers[:2], rounds)
	if w == nil {
		t.Fatal("expected a winner")
	}
	// 平均分相同(80=80)，radical 被高置信挑战更多 → defender 胜
	if w.ID != "defender" {
		t.Errorf("winner = %q, want defender (fewer high-confidence challenges)", w.ID)
	}
}

func TestDetermineWinner_NoScoresReturnsNil(t *testing.T) {
	rounds := []DebateRound{
		{Phase: DebateProposal, Outputs: []DebateOutput{{MemberID: "radical", Content: "A"}}},
		{Phase: DebateChallenge, Outputs: nil},
		{Phase: DebateRebuttal, Outputs: nil},
		{Phase: DebateFinal, Outputs: []DebateOutput{{MemberID: "radical", Content: "无评分输出"}}},
	}
	if w := determineWinner(DefaultDebateMembers[:1], rounds); w != nil {
		t.Errorf("expected nil winner when no SCORE lines, got %q", w.ID)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestDetermineWinner -v`
预期：编译失败——`determineWinner` 未定义。

- [ ] **步骤 3：实现 `determineWinner` + `countHighConfidenceTargeting`**

在 `engine/roundtable.go` 的 `parseVerdicts` 之后（`extractHighConfidenceChallenges` 附近）新增：

```go
// determineWinner returns the member with the highest average score from the
// final round's SCORE lines. On a tie for first place, the member facing fewer
// high-confidence (>=0.7) challenges in the challenge round wins; if still
// tied, the earliest in member order wins. Returns nil if no SCORE lines parse.
func determineWinner(members []RoundtableMember, rounds []DebateRound) *RoundtableMember {
	if len(rounds) < 4 {
		return nil
	}
	type avg struct {
		member RoundtableMember
		sum    float64
		count  int
	}
	var avgs []avg
	for _, m := range members {
		a := avg{member: m}
		for _, out := range rounds[3].Outputs {
			for _, line := range strings.Split(out.Content, "\n") {
				trimmed := strings.TrimSpace(line)
				lower := strings.ToLower(trimmed)
				if !strings.HasPrefix(lower, "score:") {
					continue
				}
				rest := strings.TrimSpace(trimmed[len("score:"):])
				parts := strings.SplitN(rest, "=", 2)
				if len(parts) != 2 {
					continue
				}
				if strings.TrimSpace(parts[0]) != m.ID {
					continue
				}
				s, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
				if err != nil {
					continue
				}
				a.sum += s
				a.count++
			}
		}
		if a.count > 0 {
			avgs = append(avgs, a)
		}
	}
	if len(avgs) == 0 {
		return nil
	}
	sort.SliceStable(avgs, func(i, j int) bool {
		ai := avgs[i].sum / float64(avgs[i].count)
		aj := avgs[j].sum / float64(avgs[j].count)
		if ai != aj {
			return ai > aj
		}
		// 平均分并列：被高置信挑战更少者胜
		return countHighConfidenceTargeting(avgs[i].member.ID, rounds) <
			countHighConfidenceTargeting(avgs[j].member.ID, rounds)
	})
	return &avgs[0].member
}

// countHighConfidenceTargeting returns how many high-confidence (>=0.7)
// challenges in the challenge round (index 1) target the given member.
func countHighConfidenceTargeting(memberID string, rounds []DebateRound) int {
	if len(rounds) < 2 {
		return 0
	}
	n := 0
	for _, out := range rounds[1].Outputs {
		for _, target := range out.Targets {
			if target != memberID {
				continue
			}
			for _, block := range splitChallengeBlocks(out.Content) {
				if extractConfidence(block) >= 0.7 {
					n++
				}
			}
		}
	}
	return n
}
```

- [ ] **步骤 4：`handleDebateArena` 终陈后判定胜者**

修改 `engine/roundtable.go:176-185`，在第 4 轮完成后（置 `RoundtableAwaitingVerdict` 之后）追加胜者判定：

```go
	if state.Roundtable.Phase <= RoundtableFinal {
		if err := h.runDebateRound(ctx, DebateFinal, goal, members, zh); err != nil {
			return nil, fmt.Errorf("final round: %w", err)
		}
		state.Roundtable.Phase = RoundtableAwaitingVerdict
	}

	// Determine the winner by average score (ties broken by challenge data).
	if w := determineWinner(members, state.Roundtable.DebateRounds); w != nil {
		state.Roundtable.WinnerID = w.ID
	}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run TestDetermineWinner -v`
预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add engine/roundtable.go engine/roundtable_test.go
git commit -m "feat(engine): determine /team debate winner by average score with tiebreak"
```

---

### 任务 4：实施蓝图生成（buildBlueprint）

**文件：**
- 修改：`engine/roundtable.go`（新增 `buildBlueprint`；`handleDebateArena` 调用）
- 测试：`engine/roundtable_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/roundtable_test.go` 追加：

```go
// --- Blueprint generation ---

func TestBuildBlueprint_GeneratesBlueprint(t *testing.T) {
	e := newTestEngine(t)
	rounds := buildFinalRounds()
	bp := e.roundtableHall.buildBlueprint(context.Background(), "测试需求", DefaultDebateMembers[:2], true, DefaultDebateMembers[0], rounds)
	if bp == "" {
		t.Fatal("expected non-empty blueprint")
	}
	// mockPromptRunner.RunWithPrompt 返回固定 response（含"采用微服务架构"）
	if !strings.Contains(bp, "采用微服务架构") {
		t.Errorf("blueprint should contain mock LLM output, got %q", bp)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestBuildBlueprint -v`
预期：编译失败——`buildBlueprint` 未定义。

- [ ] **步骤 3：实现 `buildBlueprint`**

在 `engine/roundtable.go` 的 `synthesizeDebate` 之后新增（完全复用其"AgentSub + RunWithPrompt + accumulateUsage"模式）：

```go
// buildBlueprint runs a single LLM call that rewrites the winning proposal into
// a detailed, executable implementation blueprint, absorbing reasonable
// corrections raised during the challenge/rebuttal rounds (the LLM judges which
// corrections to absorb from the full debate record). Returns "" on failure so
// the verdict screen falls back to the concise synthesis.
func (h *RoundtableHall) buildBlueprint(ctx context.Context, goal string, members []RoundtableMember, zh bool, winner RoundtableMember, rounds []DebateRound) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "synthesis",
			Name:   "blueprint",
			Detail: pickPrompt(zh, "Writing implementation blueprint...", "正在撰写实施蓝图..."),
		})
	}
	record := formatDebateRecord(rounds, members, zh)
	winnerProposal := getMemberOutput(winner.ID, rounds[0].Outputs)
	if winnerProposal == "" {
		return ""
	}

	taskGoal := fmt.Sprintf(pickPrompt(zh,
		`## Task
You are a senior engineer. Rewrite the winning proposal below into a detailed, executable implementation blueprint that a coding agent can follow directly. Absorb any reasonable corrections raised in the challenges (mark absorbed corrections explicitly).

## Requirement
%s

## Winning Proposal
%s

## Complete Debate Record
%s

## Output Format
## 方案概述
<2-3 sentences>

## 关键设计决策
<numbered list: each decision + rationale; mark absorbed corrections as (来自质询修正)>

## 实现步骤
<numbered list: each step = what to change + where (file/function)>

## 风险与回滚
<bullet list: risk -> mitigation; rollback plan>`,
		`## 任务
你是一位资深工程师。把下面的获胜方案重写为一份详细的、可直接执行的实施蓝图，供编码 agent 直接照做。吸收质询中提出的合理修正（被吸收的修正请明确标注）。

## 需求
%s

## 获胜方案
%s

## 完整辩论记录
%s

## 输出格式
## 方案概述
<2-3句>

## 关键设计决策
<编号列表：每个决策+理由；被吸收的修正标注 (来自质询修正)>

## 实现步骤
<编号列表：每步=改什么+改哪里（文件/函数）>

## 风险与回滚
<列表：风险 -> 缓解；回滚方案>`), goal, winnerProposal, record)

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
```

- [ ] **步骤 4：`handleDebateArena` 生成蓝图（且省 token）**

修改 `engine/roundtable.go:184-186`（胜者判定之后），改为：

```go
	// Determine the winner by average score (ties broken by challenge data).
	var synthesis string
	if w := determineWinner(members, state.Roundtable.DebateRounds); w != nil {
		state.Roundtable.WinnerID = w.ID
		// Generate the detailed blueprint; only fall back to the concise
		// synthesis LLM call if the blueprint generation fails (saves tokens).
		state.Roundtable.Blueprint = h.buildBlueprint(ctx, goal, members, zh, *w, state.Roundtable.DebateRounds)
	}
	if state.Roundtable.Blueprint == "" {
		synthesis = h.synthesizeDebate(ctx, goal, members, zh)
	}
	return h.buildVerdictPrompt(goal, members, zh, synthesis), nil
```

（将原先末尾的 `synthesis := h.synthesizeDebate(...)` 与 `return h.buildVerdictPrompt(...)` 两行替换为上述代码。）

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run 'TestBuildBlueprint|TestDebateArena' -v`
预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add engine/roundtable.go engine/roundtable_test.go
git commit -m "feat(engine): generate detailed implementation blueprint for /team winner"
```

---

### 任务 5：裁决界面重写（胜者 + 评分总览 + 蓝图）

**文件：**
- 修改：`engine/roundtable.go`（`buildVerdictPrompt` 重写 + 新增 `winnerAvgScore`）
- 测试：`engine/roundtable_test.go`

- [ ] **步骤 1：编写失败的测试**

更新既有测试并新增断言。在 `engine/roundtable_test.go` 中修改 `TestDebateArena_BuildVerdictPrompt`（`:365-447`）：

在 `resp := e.roundtableHall.buildVerdictPrompt(...)` 调用前，给 `e.state.Roundtable` 补上胜者与蓝图（模拟任务 3/4 已生成的产物）：

```go
	e.state.Roundtable.WinnerID = "radical"
	e.state.Roundtable.Blueprint = "## 方案概述\n蓝图正文：采用微服务架构。\n\n## 实现步骤\n1. 新建 internal/svc\n2. 迁移调用方"
```

然后替换原断言块（`:405-446`）为：

```go
	if !strings.Contains(resp.Summary, "辩论完成") {
		t.Errorf("verdict prompt should mention debate completion")
	}
	if !strings.Contains(resp.Summary, "创新派") || !strings.Contains(resp.Summary, "防守派") {
		t.Errorf("verdict prompt should mention member names")
	}
	if !strings.Contains(resp.Summary, "最被大家接受的方案") {
		t.Errorf("verdict prompt should declare the most-accepted proposal")
	}
	if !strings.Contains(resp.Summary, "评分总览") {
		t.Errorf("verdict prompt should contain score overview")
	}
	if !strings.Contains(resp.Summary, "平均") {
		t.Errorf("verdict prompt should contain average column in score table")
	}
	if !strings.Contains(resp.Summary, "★") {
		t.Errorf("verdict prompt should highlight top-scoring proposal with ★")
	}
	if !strings.Contains(resp.Summary, "实施蓝图") {
		t.Errorf("verdict prompt should contain the implementation blueprint section")
	}
	if !strings.Contains(resp.Summary, "蓝图正文") {
		t.Errorf("verdict prompt should render the blueprint content")
	}
	// 投票统计已移除（用户只要得分）
	if strings.Contains(resp.Summary, "投票统计") {
		t.Errorf("verdict prompt should NOT contain vote tally")
	}
	// 蓝图分支：观点/挑战/最终立场已折叠进蓝图，不应单独展示
	if strings.Contains(resp.Summary, "各角色观点") {
		t.Errorf("blueprint branch should NOT show member viewpoints")
	}
	if strings.Contains(resp.Summary, "高置信度挑战") {
		t.Errorf("blueprint branch should NOT show high-confidence challenges")
	}
```

> 说明：`TestDebateArena_BuildVerdictPrompt` 构造的 rounds 里挑战轮 radical 对 defender 的 `CONFIDENCE: 0.9`、defender 对 radical 的 `CONFIDENCE: 0.5`；blueprint 非空时走蓝图分支，`各角色观点`/`高置信度挑战`/`最终立场` 仅在三段式都为空（fallback）时显示。为覆盖"蓝图分支"与"fallback 分支"两者，fallback 分支的断言（含高/低置信挑战过滤、观点展示）全部放进下面新增的测试：

```go
func TestBuildVerdictPrompt_FallbackWithoutBlueprint(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试裁决界面",
		Phase:   RoundtableAwaitingVerdict,
		Members: DefaultDebateMembers[:2],
		DebateRounds: []DebateRound{
			{Phase: DebateProposal, Outputs: []DebateOutput{{MemberID: "radical", Content: "创新派方案"}, {MemberID: "defender", Content: "防守派方案"}}},
			{Phase: DebateChallenge, Outputs: []DebateOutput{{MemberID: "radical", Content: "### 挑战: 防守派\n方案过于保守\nCONFIDENCE: 0.9", Targets: []string{"defender"}}}},
			{Phase: DebateRebuttal, Outputs: []DebateOutput{{MemberID: "radical", Content: "反驳"}, {MemberID: "defender", Content: "反驳"}}},
			{Phase: DebateFinal, Outputs: []DebateOutput{{MemberID: "radical", Content: "最终立场\nSCORE: radical = 90\nSCORE: defender = 70"}, {MemberID: "defender", Content: "最终立场\nSCORE: radical = 75\nSCORE: defender = 85"}}},
		},
	}
	// WinnerID 未设置、Blueprint 为空、synthesis 为空 → 走 fallback（观点+高置信挑战）
	resp := e.roundtableHall.buildVerdictPrompt("测试裁决界面", DefaultDebateMembers[:2], true, "")
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !strings.Contains(resp.Summary, "最被大家接受的方案") {
		t.Errorf("verdict prompt should still declare the section header")
	}
	// fallback 展示成员观点
	if !strings.Contains(resp.Summary, "各角色观点") {
		t.Errorf("fallback should show member viewpoints")
	}
	if !strings.Contains(resp.Summary, "创新派方案") {
		t.Errorf("fallback should render radical's proposal")
	}
	// 高置信(0.9)挑战显示
	if !strings.Contains(resp.Summary, "高置信度挑战") {
		t.Errorf("fallback should show high-confidence challenges")
	}
	if !strings.Contains(resp.Summary, "置信度 90%") {
		t.Errorf("fallback should show confidence percentage for high-confidence challenge")
	}
	// 低置信(0.5)挑战不显示
	if strings.Contains(resp.Summary, "重构风险太高") {
		t.Errorf("fallback should not contain low-confidence challenges")
	}
	// fallback 展示最终立场而非原始提案
	if !strings.Contains(resp.Summary, "最终立场") {
		t.Errorf("fallback should contain final position from final round")
	}
}
```

再修改 `TestBuildVerdictPrompt_WithSynthesis`（`:586-637`）：删除 `"投票统计"` 与 `"1票"` 断言（原 `:627-629`），保留其余。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestDebateArena_BuildVerdictPrompt|TestBuildVerdictPrompt' -v`
预期：FAIL——旧界面无"最被大家接受的方案"/"实施蓝图"，且含"投票统计"。

- [ ] **步骤 3：重写 `buildVerdictPrompt`**

将 `engine/roundtable.go:668-772` 的 `buildVerdictPrompt` 整体替换为：

```go
// buildVerdictPrompt generates the verdict prompt shown to the user after the
// debate: declares the most-accepted proposal (highest average score), shows
// the score overview table, and renders the detailed implementation blueprint.
// When the blueprint is empty (LLM call failed) it falls back to the concise
// synthesis; when both are empty it falls back to verbose member viewpoints +
// high-confidence challenges.
func (h *RoundtableHall) buildVerdictPrompt(goal string, members []RoundtableMember, zh bool, synthesis string) *EngineResponse {
	var sb strings.Builder

	state := h.engine.state
	rounds := state.Roundtable.DebateRounds

	// Header
	sb.WriteString(pickPrompt(zh,
		"## Debate Complete - Most Accepted Proposal\n\n",
		"## 辩论完成 - 最被大家接受的方案\n\n",
	))
	sb.WriteString(pickPrompt(zh,
		fmt.Sprintf("**Goal**: %s\n\n", goal),
		fmt.Sprintf("**需求**: %s\n\n", goal),
	))

	// ── 胜者区块 ──
	winner := findMember(members, state.Roundtable.WinnerID)
	if winner != nil {
		sb.WriteString(fmt.Sprintf("### 🏆 %s %s", winner.Avatar, winner.displayName(zh)))
		if avg := winnerAvgScore(winner.ID, rounds, members); avg >= 0 {
			sb.WriteString(fmt.Sprintf("（平均分 %.1f）", avg))
		}
		sb.WriteString("\n\n")
	}

	// ── 评分总览 ──
	if len(rounds) >= 4 {
		table := buildScoreTable(rounds[3].Outputs, members, zh)
		if table != "" {
			sb.WriteString(pickPrompt(zh, "### Score Overview\n\n", "### 评分总览\n\n"))
			sb.WriteString(table)
			sb.WriteString("\n")
		}
	}

	// ── 蓝图（优先）→ synthesis → 观点 fallback ──
	switch {
	case state.Roundtable.Blueprint != "":
		sb.WriteString(pickPrompt(zh, "### Implementation Blueprint\n\n", "### 实施蓝图\n\n"))
		sb.WriteString(state.Roundtable.Blueprint)
		sb.WriteString("\n\n")
	case synthesis != "":
		sb.WriteString(pickPrompt(zh, "### Debate Summary\n\n", "### 辩论摘要\n\n"))
		sb.WriteString(synthesis)
		sb.WriteString("\n\n")
	default:
		if len(rounds) > 0 {
			sb.WriteString(pickPrompt(zh, "### Member Viewpoints\n\n", "### 各角色观点\n\n"))
			for _, out := range rounds[0].Outputs {
				m := findMember(members, out.MemberID)
				avatar := ""
				name := out.MemberID
				stance := ""
				if m != nil {
					avatar = m.Avatar
					name = m.displayName(zh)
					stance = m.displayStance(zh)
				}
				sb.WriteString(fmt.Sprintf("#### %s %s\n", avatar, name))
				if stance != "" {
					sb.WriteString(fmt.Sprintf("*%s*\n\n", stance))
				}
				viewpoint := ""
				if len(rounds) >= 4 {
					finalOut := getMemberOutput(out.MemberID, rounds[3].Outputs)
					if finalOut != "" {
						viewpoint = extractFinalPosition(finalOut)
					}
				}
				if viewpoint == "" {
					viewpoint = out.Content
				}
				sb.WriteString(viewpoint)
				sb.WriteString("\n\n")
			}
		}
		challenges := extractHighConfidenceChallenges(rounds, members, zh)
		if len(challenges) > 0 {
			sb.WriteString(pickPrompt(zh, "### High-Confidence Challenges\n\n", "### 高置信度挑战\n\n"))
			for _, c := range challenges {
				sb.WriteString(fmt.Sprintf("> **%s %s** %s\n\n%s\n\n",
					c.challengerAvatar, c.challengerName,
					pickPrompt(zh,
						fmt.Sprintf("(confidence %.0f%%)", c.confidence*100),
						fmt.Sprintf("(置信度 %.0f%%)", c.confidence*100),
					),
					c.content))
			}
		}
	}

	// ── Footer: Verdict instructions ──
	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"**Your verdict**: Type `support` to execute this blueprint, `but <condition>` to adjust, or `debate again`\n",
		"**你的裁决**: 输入 `支持` 执行此蓝图、`但要<条件>` 调整、或 `再辩一轮`\n",
	))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}
```

- [ ] **步骤 4：新增 `winnerAvgScore`**

在 `engine/roundtable.go` 的 `buildScoreTable` 之后新增：

```go
// winnerAvgScore returns the winner's average score across all members' final
// round SCORE lines, or -1 if none parse.
func winnerAvgScore(memberID string, rounds []DebateRound, members []RoundtableMember) float64 {
	if len(rounds) < 4 {
		return -1
	}
	var sum, count float64
	for _, out := range rounds[3].Outputs {
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			lower := strings.ToLower(trimmed)
			if !strings.HasPrefix(lower, "score:") {
				continue
			}
			rest := strings.TrimSpace(trimmed[len("score:"):])
			parts := strings.SplitN(rest, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.TrimSpace(parts[0]) != memberID {
				continue
			}
			s, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if err != nil {
				continue
			}
			sum += s
			count++
		}
	}
	if count == 0 {
		return -1
	}
	return sum / count
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run 'TestDebateArena_BuildVerdictPrompt|TestBuildVerdictPrompt' -v`
预期：全部 PASS。

- [ ] **步骤 6：Commit**

```bash
git add engine/roundtable.go engine/roundtable_test.go
git commit -m "feat(engine): rewrite /team verdict screen with winner + blueprint"
```

---

### 任务 6：清理孤儿代码与全量回归

**文件：**
- 修改：`engine/roundtable.go`（删 `parseVerdicts`/`verdictTally` 及不再引用的导入）
- 修改：`engine/roundtable_test.go`（删 `TestParseVerdicts`/`TestParseVerdicts_NoVerdicts`）
- 验证：`go build ./...` + `go test ./engine/...`

- [ ] **步骤 1：删除孤儿函数**

`parseVerdicts`（`engine/roundtable.go:627-662`）与 `verdictTally`（`:617-623`）现在已无调用方（`buildVerdictPrompt` 已移除投票统计）。删除两者。

- [ ] **步骤 2：删除对应孤儿测试**

删除 `engine/roundtable_test.go` 中的 `TestParseVerdicts`（`:557-574`）与 `TestParseVerdicts_NoVerdicts`（`:576-584`）。

- [ ] **步骤 3：检查导入**

运行 `go build ./engine/`，若有未使用导入（如 `sort` 若仍被 `determineWinner` 使用则保留）按编译器提示清理。

- [ ] **步骤 4：全量构建与测试**

运行：`go build ./... && go test ./engine/...`
预期：全部编译通过、全部测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/roundtable.go engine/roundtable_test.go
git commit -m "refactor(engine): remove dead vote-tally code from /team"
```

---

## 验收标准

- [ ] `/team <目标>` 启动后：先跑一次搜索子 agent，`RoundtableState.SharedContext` 非空，且注入到每个成员每轮提示中
- [ ] 辩论成员一律走 sub-agent 工具循环（可 grep/read 验证），即使 `model` 已设置也不走单次推理 fast path
- [ ] 终陈轮后：`determineWinner` 选出平均分最高者（并列用挑战数据 tiebreak），`WinnerID` 被设置
- [ ] 胜者方案的详细实施蓝图被生成并存入 `Blueprint`；蓝图为空时回退到摘要/观点
- [ ] 裁决界面展示"🏆 最被大家接受的方案 + 评分总览 + 实施蓝图"三要素，**不含投票统计**
- [ ] 旧 session 恢复无碍：新字段 `omitempty` 向后兼容
- [ ] `go build ./... && go test ./engine/...` 全部通过

## 风险与回退

- **辩论变慢变贵**：成员全工具循环 + 预搜索 + 蓝图 LLM 调用，相比旧 fast path 显著增加延迟与 token。这是用户明确接受的质量优先权衡。
- **蓝图/合成失败**：`buildBlueprint`/`synthesizeDebate` 均返回 "" 时，`buildVerdictPrompt` 回退到"成员观点 + 高置信挑战"（原 fallback），功能不中断。
- **SCORE 未解析**：`determineWinner` 返回 nil，`WinnerID` 不设置，界面仍展示评分总览（若可解析）与 fallback 内容。
