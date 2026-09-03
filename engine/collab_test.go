package engine

import (
	"context"
	"strings"
	"testing"
)

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

// capturePromptRunner implements RunWithPrompt and records the last input +
// extraPrompt, returning a fixed response. Used to assert stage goal content
// and the Tools safety allowlist.
type capturePromptRunner struct {
	mockSimpleAgent
	lastInput  Handoff
	lastExtra  string
	stageGoals []string
	stageTools [][]string // Tools captured per stage run
}

func (c *capturePromptRunner) RunWithPrompt(_ context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	c.lastInput = input
	c.lastExtra = extraPrompt
	// 仅把"阶段运行"（携带 role prompt）计入 stageGoals/stageTools；
	// buildCollabSummary 的汇总调用（extraPrompt==""）不计入，避免污染阶段计数。
	if extraPrompt != "" {
		c.stageGoals = append(c.stageGoals, input.Goal)
		c.stageTools = append(c.stageTools, input.Tools)
	}
	return &HandoffResult{Summary: c.response, Conclusions: []string{c.response}}, nil
}

func newCaptureCollabTestEngine(t *testing.T) (*Engine, *capturePromptRunner) {
	t.Helper()
	captor := &capturePromptRunner{mockSimpleAgent: mockSimpleAgent{
		id:       AgentSub,
		response: "## 产出\n采用微服务架构。",
	}}
	reg := NewAgentRegistry()
	reg.Register(captor)
	e := &Engine{
		agents:          reg,
		state:           &TaskState{TaskID: "test-collab-capture"},
		config:          EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.collabHall = NewCollabHall(e)
	return e, captor
}

// TestCollabStageGoal_CarriesPriorOutputs (C2): downstream stages must receive
// the rendered outputs of already-completed stages; recon ignores prior.
func TestCollabStageGoal_CarriesPriorOutputs(t *testing.T) {
	prior := "### 侦察\n扫描发现 engine/collab.go。"
	designGoal := buildCollabStageGoal(CollabDesign, "目标", prior, true)
	if !strings.Contains(designGoal, "## 前序阶段产出") || !strings.Contains(designGoal, prior) {
		t.Errorf("design goal must embed prior, got:\n%s", designGoal)
	}
	devGoal := buildCollabStageGoal(CollabDev, "目标", prior, false)
	if !strings.Contains(devGoal, "## Previous Stage Outputs") || !strings.Contains(devGoal, prior) {
		t.Errorf("dev goal must embed prior (en), got:\n%s", devGoal)
	}
	reviewGoal := buildCollabStageGoal(CollabReview, "目标", prior, true)
	if !strings.Contains(reviewGoal, "## 前序阶段产出") || !strings.Contains(reviewGoal, prior) {
		t.Errorf("review goal must embed prior, got:\n%s", reviewGoal)
	}
	// recon ignores prior entirely.
	reconGoal := buildCollabStageGoal(CollabRecon, "目标", prior, true)
	if strings.Contains(reconGoal, prior) || strings.Contains(reconGoal, "前序阶段产出") {
		t.Errorf("recon goal must NOT embed prior, got:\n%s", reconGoal)
	}
	// Empty prior is harmless — no dangling prior section header.
	emptyDesign := buildCollabStageGoal(CollabDesign, "目标", "", true)
	if strings.Contains(emptyDesign, "前序阶段产出") {
		t.Errorf("design goal with empty prior must not include a prior section, got:\n%s", emptyDesign)
	}
	if !strings.Contains(emptyDesign, "## 需求\n目标") {
		t.Errorf("design goal with empty prior must still carry the requirement, got:\n%s", emptyDesign)
	}
}

// TestRenderCollabPrior renders completed stages as labeled blocks.
func TestRenderCollabPrior(t *testing.T) {
	if got := renderCollabPrior(nil, true); got != "" {
		t.Errorf("empty stages should render \"\", got %q", got)
	}
	stages := []CollabStage{
		{Name: CollabRecon, Content: "文件 A"},
		{Name: CollabDesign, Content: "方案 B"},
	}
	got := renderCollabPrior(stages, true)
	want := "### 侦察\n文件 A\n\n### 设计\n方案 B"
	if got != want {
		t.Errorf("renderCollabPrior() = %q, want %q", got, want)
	}
}

// TestHandleCollabArena_IdempotentResume (I1): starting from CollabDesignPhase
// with an existing recon output must only run design/dev/review, advance to
// AwaitingConfirmation, keep 4 total stages, and NOT rewrite the recon stage.
func TestHandleCollabArena_IdempotentResume(t *testing.T) {
	e, captor := newCaptureCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDesignPhase,
		Stages: []CollabStage{
			{Name: CollabRecon, Content: "recon 已完成的内容"},
		},
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if e.state.Collab.Phase != CollabAwaitingConfirmation {
		t.Errorf("Phase = %v, want CollabAwaitingConfirmation", e.state.Collab.Phase)
	}
	if len(e.state.Collab.Stages) != 4 {
		t.Fatalf("got %d stages, want 4", len(e.state.Collab.Stages))
	}
	// recon must be preserved verbatim — not re-run/rewritten.
	if e.state.Collab.Stages[0].Content != "recon 已完成的内容" {
		t.Errorf("recon stage was rewritten, got %q", e.state.Collab.Stages[0].Content)
	}
	// Only design/dev/review ran — 3 stage goals captured.
	if len(captor.stageGoals) != 3 {
		t.Fatalf("expected 3 stage runs (design/dev/review), got %d", len(captor.stageGoals))
	}
	for _, name := range []CollabStageName{CollabDesign, CollabDev, CollabReview} {
		found := false
		for _, s := range e.state.Collab.Stages[1:] {
			if s.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing stage %q in output", name)
		}
	}
}

// TestHandleCollabArena_DesignGoalHasReconOutput (C2): when the pipeline
// resumes at design, the design goal must contain the recon output.
func TestHandleCollabArena_DesignGoalHasReconOutput(t *testing.T) {
	e, captor := newCaptureCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabDesignPhase,
		Stages: []CollabStage{
			{Name: CollabRecon, Content: "recon 已完成的内容"},
		},
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if len(captor.stageGoals) == 0 {
		t.Fatal("expected at least one stage goal captured")
	}
	// First captured goal = design, must carry the recon output.
	if !strings.Contains(captor.stageGoals[0], "recon 已完成的内容") {
		t.Errorf("design goal must carry recon output, got:\n%s", captor.stageGoals[0])
	}
}

// TestHandleCollabArena_ToolsAllowlist (I3): every stage handoff must use
// exactly the read-only safety allowlist [read grep glob lsp], never edit/write.
func TestHandleCollabArena_ToolsAllowlist(t *testing.T) {
	e, captor := newCaptureCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabReconPhase,
	}
	_, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if len(captor.stageGoals) != 4 {
		t.Fatalf("expected 4 stage runs, got %d", len(captor.stageGoals))
	}
	allowed := map[string]bool{"read": true, "grep": true, "glob": true, "lsp": true}
	for i, tools := range captor.stageTools {
		if len(tools) != len(allowed) {
			t.Errorf("stage %d tools = %v, want exactly %d allowlisted tools", i, tools, len(allowed))
		}
		for _, got := range tools {
			if !allowed[got] {
				t.Errorf("stage %d leaked forbidden tool %q into handoff: %v", i, got, tools)
			}
		}
	}
	for _, got := range captor.stageTools[0] {
		if got == "edit" || got == "write" {
			t.Errorf("forbidden tool %q in first stage handoff: %v", got, captor.stageTools[0])
		}
	}
}

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
		Goal:  "实现一个缓存层",
		Phase: CollabAwaitingConfirmation,
		Stages: []CollabStage{
			{Name: CollabRecon, Content: "调研结果A"},
			{Name: CollabDesign, Content: "设计方案B"},
			{Name: CollabDev, Content: "实现代码C"},
			{Name: CollabReview, Content: "评审意见D"},
		},
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
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("expected exactly 1 pinned message after confirmation, got %d", len(e.pendingPinnedMessages))
	}
	// /collab 的关键特性：确认时把全部阶段产出拼进 [COLLAB PLAN] pinned，
	// 供主 agent 在执行方案时看到完整产出，而非只看到汇总。
	pinned := e.pendingPinnedMessages[0]
	if !strings.Contains(pinned, "[COLLAB PLAN: 实现一个缓存层]") {
		t.Errorf("pinned should carry the [COLLAB PLAN] header, got:\n%s", pinned)
	}
	for _, want := range []string{"调研结果A", "设计方案B", "实现代码C", "评审意见D"} {
		if !strings.Contains(pinned, want) {
			t.Errorf("pinned plan should contain stage output %q, got:\n%s", want, pinned)
		}
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

func TestCollab_AdvanceConfirmWithAdjustment_NotRestart(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:   "实现一个缓存层",
		Phase:  CollabAwaitingConfirmation,
		Stages: []CollabStage{{Name: CollabRecon, Content: "调研结果"}},
	}
	// "支持但要重新审视设计" 是确认+调整指令，不应被误判为 restart
	resp, err := e.collabHall.Advance(context.Background(), "支持但要重新审视设计")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab.Phase != CollabDone {
		t.Errorf("Phase = %v, want CollabDone (confirmed, not restart)", e.state.Collab.Phase)
	}
	if len(e.state.Collab.Stages) != 1 {
		t.Errorf("Stages should NOT be cleared (len 1), got %d", len(e.state.Collab.Stages))
	}
	if len(e.pendingPinnedMessages) == 0 {
		t.Error("expected pinned plan after confirmation")
	}
}

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
	// 确认后同一 Run 执行方案（而非只返回确认 ack）：正向断言——Summary 必须是
	// stub model 执行方案的结论"执行了协作方案。"（"不含'已确认' ack"仅作辅助）。
	if !strings.Contains(resp.Summary, "执行了协作方案。") {
		t.Errorf("collab confirm Run must execute the plan and report the conclusion, got %q", resp.Summary)
	}
	if strings.Contains(resp.Summary, "已确认") {
		t.Errorf("collab confirm Run must not return a confirmation ack, got %q", resp.Summary)
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
