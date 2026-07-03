package engine

import (
	"context"
	"strings"
	"sync"
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

func TestParseTeamCommand_WithMembers(t *testing.T) {
	cmd := parseTeamCommand("/team --members radical,defender 重构认证")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "重构认证" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "重构认证")
	}
	if len(cmd.MemberIDs) != 2 || cmd.MemberIDs[0] != "radical" || cmd.MemberIDs[1] != "defender" {
		t.Errorf("MemberIDs = %v, want [radical defender]", cmd.MemberIDs)
	}
}

func TestParseTeamCommand_WithAdd(t *testing.T) {
	cmd := parseTeamCommand("/team --add ~/.deepact/members/perf.toml 优化查询")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "优化查询" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "优化查询")
	}
	if cmd.AddMemberPath != "~/.deepact/members/perf.toml" {
		t.Errorf("AddMemberPath = %q", cmd.AddMemberPath)
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

// --- Phase string ---

func TestDebatePhaseStrings(t *testing.T) {
	tests := []struct {
		phase RoundtablePhase
		want  string
	}{
		{RoundtableProposal, "proposal"},
		{RoundtableChallenge, "challenge"},
		{RoundtableRebuttal, "rebuttal"},
		{RoundtableFinal, "final"},
		{RoundtableAwaitingVerdict, "awaiting_verdict"},
		{RoundtableDone, "done"},
		{RoundtableIdle, "idle"},
	}
	for _, tt := range tests {
		got := tt.phase.String()
		if got != tt.want {
			t.Errorf("RoundtablePhase(%d).String() = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

// --- Debate arena tests ---

// mockSimpleAgent implements Agent for testing debate arena.
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
		Summary:     m.response,
		Conclusions: []string{m.response},
	}, nil
}
func (m *mockSimpleAgent) SetOnProgress(fn ProgressFunc) {}

// mockPromptRunner supports RunWithPrompt for debate testing.
type mockPromptRunner struct {
	mockSimpleAgent
}

func (m *mockPromptRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	return &HandoffResult{
		Summary:     m.response + "\n\nSCORE: radical = 85\nSCORE: defender = 70\nVERDICT: radical",
		Conclusions: []string{m.response},
	}, nil
}

// newTestEngine creates a minimal engine for roundtable testing.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()

	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{
			id:       AgentSub,
			response: "## 方案\n采用微服务架构降低耦合度。\n\nSCORE: radical = 90\nSCORE: defender = 75\nVERDICT: radical",
		},
	})

	e := &Engine{
		agents:          reg,
		state:           &TaskState{TaskID: "test-debate"},
		config:          EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.roundtableHall = NewRoundtableHall(e)
	return e
}

func TestDebateArena_ProposalRound(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "实现一个简单的缓存层",
		Phase:   RoundtableProposal,
		Members: DefaultDebateMembers[:2], // use only 2 members for faster test
	}

	resp, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// Should have completed all 4 rounds and be awaiting verdict
	if e.state.Roundtable.Phase != RoundtableAwaitingVerdict {
		t.Errorf("Phase = %v, want RoundtableAwaitingVerdict", e.state.Roundtable.Phase)
	}
	if len(e.state.Roundtable.DebateRounds) != 4 {
		t.Errorf("got %d debate rounds, want 4", len(e.state.Roundtable.DebateRounds))
	}
}

func TestDebateArena_VerdictPick(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试裁决",
		Phase:   RoundtableAwaitingVerdict,
		Members: DefaultDebateMembers,
	}

	resp, err := e.roundtableHall.Advance(context.Background(), "支持创新派的方案")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Roundtable.Phase != RoundtableDone {
		t.Errorf("Phase = %v, want RoundtableDone", e.state.Roundtable.Phase)
	}
}

func TestDebateArena_VerdictDebateAgain(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试再辩",
		Phase:   RoundtableAwaitingVerdict,
		Members: DefaultDebateMembers,
	}

	_, err := e.roundtableHall.Advance(context.Background(), "再辩一轮")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if e.state.Roundtable.Phase != RoundtableProposal {
		t.Errorf("Phase = %v, want RoundtableProposal", e.state.Roundtable.Phase)
	}
}

func TestDebateArena_ProgressEvents(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试进度事件",
		Phase:   RoundtableProposal,
		Members: DefaultDebateMembers[:2],
	}

	var events []ProgressEvent
	var eventsMu sync.Mutex
	e.config.OnProgress = func(ev ProgressEvent) {
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	}

	_, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}

	phaseEvents := []string{}
	for _, ev := range events {
		if ev.Type == "debate_phase" {
			phaseEvents = append(phaseEvents, ev.Name)
		}
	}

	if len(phaseEvents) != 4 {
		t.Errorf("expected 4 debate_phase events, got %d: %v", len(phaseEvents), phaseEvents)
	}
}

func TestDebateArena_BuildVerdictPrompt(t *testing.T) {
	e := newTestEngine(t)
	e.state.Roundtable = &RoundtableState{
		Goal:    "测试裁决界面",
		Phase:   RoundtableAwaitingVerdict,
		Members: DefaultDebateMembers,
		DebateRounds: []DebateRound{
			{
				Phase: DebateProposal,
				Outputs: []DebateOutput{
					{MemberID: "radical", Content: "创新派方案"},
					{MemberID: "defender", Content: "防守派方案"},
				},
			},
			{
				Phase: DebateChallenge,
				Outputs: []DebateOutput{
					{MemberID: "radical", Content: "挑战防守派", Targets: []string{"defender"}},
					{MemberID: "defender", Content: "挑战创新派", Targets: []string{"radical"}},
				},
			},
			{
				Phase: DebateRebuttal,
				Outputs: []DebateOutput{
					{MemberID: "radical", Content: "反驳"},
					{MemberID: "defender", Content: "反驳"},
				},
			},
			{
				Phase: DebateFinal,
				Outputs: []DebateOutput{
					{MemberID: "radical", Content: "SCORE: radical = 90\nSCORE: defender = 70\nVERDICT: radical"},
					{MemberID: "defender", Content: "SCORE: radical = 75\nSCORE: defender = 85\nVERDICT: defender"},
				},
			},
		},
	}

	resp := e.roundtableHall.buildVerdictPrompt("测试裁决界面", DefaultDebateMembers, true)
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !strings.Contains(resp.Summary, "辩论完成") {
		t.Errorf("verdict prompt should mention debate completion")
	}
	if !strings.Contains(resp.Summary, "创新派") || !strings.Contains(resp.Summary, "防守派") {
		t.Errorf("verdict prompt should mention member names")
	}
	if !strings.Contains(resp.Summary, "评分") {
		t.Errorf("verdict prompt should contain scores")
	}
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
