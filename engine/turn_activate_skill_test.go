package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

// stubContextBuilder is a minimal ContextBuilder for testing activate_skill
// interception. Only SetActiveSkill is exercised; other methods are no-ops.
type stubContextBuilder struct {
	activeSkill string
}

func (s *stubContextBuilder) Build(_ *TaskState, _ []Message, _ []ToolResult) []ModelMessage {
	return nil
}
func (s *stubContextBuilder) EstimateTokens(_ []ModelMessage) int { return 0 }
func (s *stubContextBuilder) SetActiveSkill(name, _ string) { s.activeSkill = name }

// TestProcessActivateSkillCalls_NoOrphanedToolCalls verifies that every
// activate_skill call receives a tool response message — even when the
// call is invalid (bad JSON, empty name, unknown skill). Without a response,
// the DeepSeek API rejects the next request because the assistant message
// contains a tool_call_id with no matching tool message, permanently
// stalling the session.
func TestProcessActivateSkillCalls_NoOrphanedToolCalls(t *testing.T) {
	// Set up a minimal engine with one known skill.
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills:          skillReg,
		state:           &TaskState{},
		activatedSkills: make(map[string]bool),
		config:          EngineConfig{},
	}

	tests := []struct {
		name    string
		callID  string
		input   string
		wantErr bool
		wantSub string
	}{
		{
			name:    "bad JSON",
			callID:  "call_bad_json",
			input:   `{invalid json}`,
			wantErr: true,
			wantSub: "invalid activate_skill arguments",
		},
		{
			name:    "empty skill_name",
			callID:  "call_empty_name",
			input:   `{"skill_name":""}`,
			wantErr: true,
			wantSub: "non-empty skill_name",
		},
		{
			name:    "unknown skill",
			callID:  "call_unknown",
			input:   `{"skill_name":"nonexistent"}`,
			wantErr: true,
			wantSub: `skill "nonexistent" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := []ToolCallRequest{
				{ID: tt.callID, Name: ActivateSkillToolName, Input: json.RawMessage(tt.input)},
			}
			msgs := e.processActivateSkillCalls(calls)
			if len(msgs) != 1 {
				t.Fatalf("expected 1 tool response, got %d — tool_call %q is orphaned", len(msgs), tt.callID)
			}
			if msgs[0].ToolCallID != tt.callID {
				t.Errorf("ToolCallID = %q, want %q", msgs[0].ToolCallID, tt.callID)
			}
			if msgs[0].Role != "tool" {
				t.Errorf("Role = %q, want %q", msgs[0].Role, "tool")
			}
			if !strings.Contains(msgs[0].Content, tt.wantSub) {
				t.Errorf("Content = %q, want substring %q", msgs[0].Content, tt.wantSub)
			}
		})
	}
}

// TestProcessActivateSkillCalls_ValidActivation verifies that a valid
// activate_skill call produces a success tool response AND activates the skill.
func TestProcessActivateSkillCalls_ValidActivation(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills:          skillReg,
		state:           &TaskState{},
		activatedSkills: make(map[string]bool),
		config:          EngineConfig{},
		context:         &stubContextBuilder{},
	}

	calls := []ToolCallRequest{
		{ID: "call_ok", Name: ActivateSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
	}
	msgs := e.processActivateSkillCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_ok" {
		t.Errorf("ToolCallID = %q, want call_ok", msgs[0].ToolCallID)
	}
	if !strings.Contains(msgs[0].Content, "Activated skill") {
		t.Errorf("Content = %q, want success message", msgs[0].Content)
	}
	if e.state.ActiveSkillName != "brainstorming" {
		t.Errorf("ActiveSkillName = %q, want brainstorming", e.state.ActiveSkillName)
	}
	if !e.activatedSkills["brainstorming"] {
		t.Error("activatedSkills should contain brainstorming")
	}
}

// TestProcessActivateSkillCalls_Mixed verifies that when activate_skill calls
// are mixed with regular tool calls, only activate_skill calls get responses
// from processActivateSkillCalls (regular calls are handled elsewhere).
func TestProcessActivateSkillCalls_Mixed(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills:          skillReg,
		state:           &TaskState{},
		activatedSkills: make(map[string]bool),
		config:          EngineConfig{},
		context:         &stubContextBuilder{},
	}

	calls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		{ID: "call_bad", Name: ActivateSkillToolName, Input: json.RawMessage(`{"skill_name":"nope"}`)},
		{ID: "call_ok", Name: ActivateSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)},
	}

	msgs := e.processActivateSkillCalls(calls)

	// Only 2 activate_skill calls → 2 responses. Regular calls are skipped.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 tool responses (for activate_skill only), got %d", len(msgs))
	}

	ids := map[string]bool{}
	for _, m := range msgs {
		ids[m.ToolCallID] = true
	}
	if !ids["call_bad"] {
		t.Error("missing tool response for call_bad (unknown skill)")
	}
	if !ids["call_ok"] {
		t.Error("missing tool response for call_ok (valid skill)")
	}
	if ids["call_read"] {
		t.Error("regular call 'read' should not get a response from processActivateSkillCalls")
	}
	if ids["call_grep"] {
		t.Error("regular call 'grep' should not get a response from processActivateSkillCalls")
	}
}

// --- Handoff result processing ---

// TestProcessHandoffResults_CancelledGetsResponse verifies that cancelled
// sub-agents still produce a tool response, preventing orphaned tool_calls.
func TestProcessHandoffResults_CancelledGetsResponse(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	handoffCalls := []ToolCallRequest{
		{ID: "call_cancelled", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"x"}`)},
		{ID: "call_ok", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"y"}`)},
	}
	results := []ToolResult{
		{ToolCallID: "call_cancelled", Status: "cancelled", Digest: ""},
		{ToolCallID: "call_ok", Status: "ok", Digest: "done"},
	}

	msgs, criticFail := e.processHandoffResults(handoffCalls, results, nil)
	if criticFail != "" {
		t.Fatalf("expected no critic fail, got %q", criticFail)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Cancelled call should get "Sub-agent cancelled." not an empty digest.
	if msgs[0].ToolCallID != "call_cancelled" {
		t.Errorf("msg[0] ToolCallID = %q, want call_cancelled", msgs[0].ToolCallID)
	}
	if msgs[0].Content != "Sub-agent cancelled." {
		t.Errorf("msg[0] Content = %q, want %q", msgs[0].Content, "Sub-agent cancelled.")
	}

	// Normal call should get its digest.
	if msgs[1].ToolCallID != "call_ok" {
		t.Errorf("msg[1] ToolCallID = %q, want call_ok", msgs[1].ToolCallID)
	}
	if msgs[1].Content != "done" {
		t.Errorf("msg[1] Content = %q, want %q", msgs[1].Content, "done")
	}
}

// TestProcessHandoffResults_CriticFailNoOrphans verifies that when a critic
// returns FAIL, tool responses are added for ALL calls — the critic, remaining
// handoffs, and regular calls — so no tool_call_id is orphaned.
func TestProcessHandoffResults_CriticFailNoOrphans(t *testing.T) {
	e := &Engine{config: EngineConfig{}, state: &TaskState{}}

	criticDigest := "VERDICT: FAIL\nIssues found."
	handoffCalls := []ToolCallRequest{
		{ID: "call_sub", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"review code"}`)},
		{ID: "call_critic", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"critic","goal":"verify"}`)},
		{ID: "call_sub2", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"more work"}`)},
	}
	results := []ToolResult{
		{ToolCallID: "call_sub", Status: "ok", Digest: "sub result"},
		{ToolCallID: "call_critic", Status: "ok", Digest: criticDigest},
		{ToolCallID: "call_sub2", Status: "ok", Digest: "sub2 result"},
	}
	regularCalls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"x.go"}`)},
	}

	msgs, criticFail := e.processHandoffResults(handoffCalls, results, regularCalls)

	if criticFail == "" {
		t.Fatal("expected non-empty criticFail")
	}

	// Every call (3 handoff + 1 regular) should have a response.
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages (3 handoff + 1 regular), got %d", len(msgs))
	}

	// Build a set of responded IDs.
	responded := map[string]string{}
	for _, m := range msgs {
		responded[m.ToolCallID] = m.Content
	}

	// call_sub was processed before the critic — should have its digest.
	if responded["call_sub"] != "sub result" {
		t.Errorf("call_sub content = %q, want %q", responded["call_sub"], "sub result")
	}
	// call_critic should have the critic digest.
	if responded["call_critic"] != criticDigest {
		t.Errorf("call_critic content = %q, want %q", responded["call_critic"], criticDigest)
	}
	// call_sub2 was after the critic — should have its digest (from results).
	if responded["call_sub2"] != "sub2 result" {
		t.Errorf("call_sub2 content = %q, want %q", responded["call_sub2"], "sub2 result")
	}
	// call_read is a regular call — should be skipped.
	if responded["call_read"] != "Skipped: critic returned FAIL." {
		t.Errorf("call_read content = %q, want skipped message", responded["call_read"])
	}
}

// TestProcessHandoffResults_NoOrphansNormal verifies the happy path: all
// handoff calls get responses, criticFail is empty, and regular calls are
// untouched (they're handled by the caller).
func TestProcessHandoffResults_NoOrphansNormal(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	handoffCalls := []ToolCallRequest{
		{ID: "call_1", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"a"}`)},
		{ID: "call_2", Name: HandoffToolName, Input: json.RawMessage(`{"agent":"sub","goal":"b"}`)},
	}
	results := []ToolResult{
		{ToolCallID: "call_1", Status: "ok", Digest: "result 1"},
		{ToolCallID: "call_2", Status: "ok", Digest: "result 2"},
	}
	regularCalls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"x.go"}`)},
	}

	msgs, criticFail := e.processHandoffResults(handoffCalls, results, regularCalls)

	if criticFail != "" {
		t.Fatalf("expected empty criticFail, got %q", criticFail)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (handoff only), got %d", len(msgs))
	}

	// Regular calls should NOT appear in handoff messages.
	for _, m := range msgs {
		if m.ToolCallID == "call_read" {
			t.Error("regular call should not get a response from processHandoffResults")
		}
	}
}
