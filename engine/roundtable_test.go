package engine

import (
	"context"
	"strings"
	"testing"
)

// --- /team command parsing ---

func TestParseTeamCommand_Valid(t *testing.T) {
	cmd := parseTeamCommand("/team 实现一个代码评审功能")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "实现一个代码评审功能" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "实现一个代码评审功能")
	}
}

func TestParseTeamCommand_WithExtraWhitespace(t *testing.T) {
	cmd := parseTeamCommand("  /team   设计用户权限系统  ")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "设计用户权限系统" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "设计用户权限系统")
	}
}

func TestParseTeamCommand_NoGoal(t *testing.T) {
	cmd := parseTeamCommand("/team")
	if cmd != nil {
		t.Errorf("expected nil for empty goal, got %+v", cmd)
	}
	cmd = parseTeamCommand("/team ")
	if cmd != nil {
		t.Errorf("expected nil for whitespace-only goal, got %+v", cmd)
	}
}

func TestParseTeamCommand_NotTeam(t *testing.T) {
	cases := []string{
		"/round 实现一个功能",
		"/skills",
		"/skill brainstorming",
		"普通用户消息",
		"",
		"/",
	}
	for _, c := range cases {
		cmd := parseTeamCommand(c)
		if cmd != nil {
			t.Errorf("expected nil for %q, got %+v", c, cmd)
		}
	}
}

// --- Team goal builder ---

func TestBuildTeamExploreGoal(t *testing.T) {
	goal := "实现代码评审功能"
	member := DefaultRoundtableMembers[0] // 架构师
	result := buildTeamExploreGoal(goal, member)

	if !strings.Contains(result, goal) {
		t.Errorf("result should contain the goal %q", goal)
	}
	if !strings.Contains(result, member.Name) {
		t.Errorf("result should contain member name %q", member.Name)
	}
	if !strings.Contains(result, member.Stance) {
		t.Errorf("result should contain member stance %q", member.Stance)
	}
	if !strings.Contains(result, "SUMMARY:") {
		t.Errorf("result should require SUMMARY: marker")
	}
}

func TestBuildTeamExploreGoal_AllMembers(t *testing.T) {
	goal := "设计一个日志系统"
	for _, member := range DefaultRoundtableMembers {
		result := buildTeamExploreGoal(goal, member)
		if !strings.Contains(result, goal) {
			t.Errorf("member %s: result missing goal", member.ID)
		}
		if !strings.Contains(result, member.Name) {
			t.Errorf("member %s: result missing name", member.ID)
		}
	}
}

// --- Team output extraction ---

func TestExtractTeamThoughts(t *testing.T) {
	content := `一些分析内容
SUMMARY: 建议采用微服务架构，降低耦合度
更多内容`
	summary := extractTeamSummary(content)
	if summary != "建议采用微服务架构，降低耦合度" {
		t.Errorf("summary = %q, want %q", summary, "建议采用微服务架构，降低耦合度")
	}
}

func TestExtractTeamThoughts_NoSummary(t *testing.T) {
	content := `一些分析内容但没有 SUMMARY 行`
	summary := extractTeamSummary(content)
	if summary != "" {
		t.Errorf("expected empty summary, got %q", summary)
	}
}

func TestExtractTeamThoughts_CaseInsensitive(t *testing.T) {
	content := `分析内容
summary: 注意输入验证`
	summary := extractTeamSummary(content)
	if summary != "注意输入验证" {
		t.Errorf("summary = %q, want %q", summary, "注意输入验证")
	}
}

// --- Phase string ---

func TestTeamPhaseStrings(t *testing.T) {
	tests := []struct {
		phase RoundtablePhase
		want  string
	}{
		{RoundtableTeamExplore, "team_explore"},
		{RoundtableTeamSynthesize, "team_synthesize"},
	}
	for _, tt := range tests {
		got := tt.phase.String()
		if got != tt.want {
			t.Errorf("RoundtablePhase(%d).String() = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

// --- Team orchestration (integration) ---

// mockSimpleAgent implements Agent for testing team exploration.
// Returns a fixed HandoffResult containing the test content.
type mockSimpleAgent struct {
	id       AgentID
	response string
}

func (m *mockSimpleAgent) ID() AgentID { return m.id }
func (m *mockSimpleAgent) Spec() AgentSpec {
	return AgentSpec{ID: m.id, Description: "mock agent for testing"}
}
func (m *mockSimpleAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return &HandoffResult{
		Summary: m.response,
		Conclusions: []string{m.response},
	}, nil
}
func (m *mockSimpleAgent) SetOnProgress(fn ProgressFunc) {}

// mockPromptRunner supports RunWithPrompt for role injection testing.
type mockPromptRunner struct {
	mockSimpleAgent
}

func (m *mockPromptRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	// Include the extra prompt in the response so we can verify it was injected
	return &HandoffResult{
		Summary: m.response + "\n\n[received prompt: " + extraPrompt + "]",
		Conclusions: []string{m.response},
	}, nil
}

// newTestEngine creates a minimal engine for roundtable testing.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()

	// Register mock sub agent
	reg.Register(&mockPromptRunner{
		mockSimpleAgent{
			id:       AgentSub,
			response: "# 分析结果\n从架构角度看，建议采用微服务架构。\nSUMMARY: 建议采用微服务架构",
		},
	})

	// Register mock planner agent
	reg.Register(&mockSimpleAgent{
		id:       AgentPlanner,
		response: "# 统一方案\n1. 采用微服务架构\n2. 做好权限控制\nSUMMARY: 统一方案",
	})

	e := &Engine{
		agents:     reg,
		state:      &TaskState{TaskID: "test-team"},
		config:     EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.roundtableHall = NewRoundtableHall(e)
	return e
}

func TestHandleTeamFlow_PhaseTransition(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:  "实现代码评审功能",
		Phase: RoundtableTeamExplore,
	}

	resp, err := e.roundtableHall.handleTeamFlow(context.Background())
	if err != nil {
		t.Fatalf("handleTeamFlow() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil EngineResponse")
	}

	// Phase should be done after flow
	if e.state.Roundtable.Phase != RoundtableDone {
		t.Errorf("Phase = %v, want RoundtableDone", e.state.Roundtable.Phase)
	}

	// Should have pinned messages injected
	if len(e.pendingPinnedMessages) == 0 {
		t.Error("expected pendingPinnedMessages to be non-empty")
	}

	// Response summary should contain team collaboration output
	if !strings.Contains(resp.Summary, "团队协作方案") &&
		!strings.Contains(resp.Summary, "Team Collaboration Plan") {
		t.Errorf("summary should mention team collaboration, got: %s", resp.Summary[:min(80, len(resp.Summary))])
	}
}

func TestHandleTeamFlow_AllMembersCalled(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:  "设计用户权限系统",
		Phase: RoundtableTeamExplore,
	}

	resp, err := e.roundtableHall.handleTeamFlow(context.Background())
	if err != nil {
		t.Fatalf("handleTeamFlow() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil EngineResponse")
	}

	// All 4 default members should appear in the output
	for _, m := range DefaultRoundtableMembers {
		if !strings.Contains(resp.Summary, m.Name) {
			t.Errorf("summary should contain member %q", m.Name)
		}
	}

	// The synthesized plan should be in pinned messages
	found := false
	for _, msg := range e.pendingPinnedMessages {
		if strings.Contains(msg, "[TEAM PLAN") || strings.Contains(msg, "[TEAM THOUGHTS") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected [TEAM PLAN] or [TEAM THOUGHTS] in pinned messages")
	}
}

func TestHandleTeamFlow_MemberPromptInjection(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:  "实现日志系统",
		Phase: RoundtableTeamExplore,
	}

	resp, err := e.roundtableHall.handleTeamFlow(context.Background())
	if err != nil {
		t.Fatalf("handleTeamFlow() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil EngineResponse")
	}

	// The mockPromptRunner includes the injected prompt in its output.
	// Verify that each member's role prompt was injected.
	for _, m := range DefaultRoundtableMembers {
		if !strings.Contains(resp.Summary, "received prompt") {
			t.Errorf("member %q: expected prompt injection evidence in summary", m.ID)
			break
		}
	}
}

func TestRunTeamExplore_ReturnsThoughtsForAllMembers(t *testing.T) {
	e := newTestEngine(t)

	thoughts := e.roundtableHall.runTeamExplore(context.Background(), "测试需求", DefaultRoundtableMembers)

	if len(thoughts) != len(DefaultRoundtableMembers) {
		t.Fatalf("expected %d thoughts, got %d", len(DefaultRoundtableMembers), len(thoughts))
	}

	for i, m := range DefaultRoundtableMembers {
		if thoughts[i].MemberID != m.ID {
			t.Errorf("thought[%d].MemberID = %q, want %q", i, thoughts[i].MemberID, m.ID)
		}
		if thoughts[i].Summary == "" {
			t.Errorf("thought[%d] missing summary", i)
		}
	}
}

func TestSynthesizeTeamOutput_WithPlanner(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:  "测试需求",
		Phase: RoundtableTeamExplore,
	}

	thoughts := []TeamThought{
		{MemberID: "architect", Content: "从架构角度看，建议微服务", Summary: "微服务"},
		{MemberID: "security", Content: "从安全角度看，需要权限控制", Summary: "权限控制"},
	}

	plan := e.roundtableHall.synthesizeTeamOutput(context.Background(), "测试需求", thoughts)

	if plan == "" {
		t.Fatal("expected non-empty plan from synthesis")
	}
	if !strings.Contains(plan, "统一方案") {
		t.Errorf("plan should contain synthesized output, got: %s", plan[:min(60, len(plan))])
	}
}

func TestSynthesizeTeamOutput_FallbackWhenNoPlanner(t *testing.T) {
	// Engine with only a sub agent, no planner
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent{
			id:       AgentSub,
			response: "mock response",
		},
	})
	e := &Engine{
		agents:     reg,
		state:      &TaskState{TaskID: "test-fallback"},
		config:     EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.roundtableHall = NewRoundtableHall(e)
	e.state.Roundtable = &RoundtableState{
		Goal:  "测试需求",
		Phase: RoundtableTeamExplore,
	}

	thoughts := []TeamThought{
		{MemberID: "architect", Content: "架构分析", Summary: "架构方案"},
		{MemberID: "security", Content: "安全分析", Summary: "安全方案"},
	}

	plan := e.roundtableHall.synthesizeTeamOutput(context.Background(), "测试需求", thoughts)

	// Fallback should concatenate summaries
	if plan == "" {
		t.Fatal("expected non-empty plan even without planner")
	}
	if !strings.Contains(plan, "架构方案") || !strings.Contains(plan, "安全方案") {
		t.Errorf("fallback plan should contain member summaries, got: %s", plan[:min(60, len(plan))])
	}
}

func TestHandleTeamFlow_ProgressEvents(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:  "测试进度事件",
		Phase: RoundtableTeamExplore,
	}

	var events []ProgressEvent
	e.config.OnProgress = func(ev ProgressEvent) {
		events = append(events, ev)
	}

	_, err := e.roundtableHall.handleTeamFlow(context.Background())
	if err != nil {
		t.Fatalf("handleTeamFlow() unexpected error: %v", err)
	}

	// Check for phase events
	phaseEvents := []string{}
	for _, ev := range events {
		if ev.Type == "roundtable_phase" {
			phaseEvents = append(phaseEvents, ev.Name)
		}
	}

	expected := []string{"team_explore", "team_synthesize", "team_done"}
	for _, exp := range expected {
		found := false
		for _, got := range phaseEvents {
			if got == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected progress event %q, got %v", exp, phaseEvents)
		}
	}
}

func TestHandleTeamFlow_CustomMembers(t *testing.T) {
	e := newTestEngine(t)
	customMembers := []RoundtableMember{
		{ID: "custom1", Name: "自定义分析师", Avatar: "🔬", Stance: "测试", Prompt: "自定义角色prompt"},
	}
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试自定义成员",
		Phase:   RoundtableTeamExplore,
		Members: customMembers,
	}

	thoughts := e.roundtableHall.runTeamExplore(context.Background(), "测试自定义成员", customMembers)

	if len(thoughts) != 1 {
		t.Fatalf("expected 1 thought for 1 custom member, got %d", len(thoughts))
	}
	if thoughts[0].MemberID != "custom1" {
		t.Errorf("MemberID = %q, want %q", thoughts[0].MemberID, "custom1")
	}
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
