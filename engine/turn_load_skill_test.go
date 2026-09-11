package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

// stubContextBuilder is a minimal ContextBuilder for testing load_skill
// interception. Other methods are no-ops.
type stubContextBuilder struct{}

func (s *stubContextBuilder) Build(_ *TaskState, _ []Message, _ []ToolResult) []ModelMessage {
	return nil
}
func (s *stubContextBuilder) EstimateTokens(_ []ModelMessage) int { return 0 }

// TestProcessLoadSkillCalls_NoOrphanedToolCalls verifies that every
// load_skill call receives a tool response message — even when the
// call is invalid (bad JSON, empty name, unknown skill). Without a response,
// the DeepSeek API rejects the next request because the assistant message
// contains a tool_call_id with no matching tool message, permanently
// stalling the session.
func TestProcessLoadSkillCalls_NoOrphanedToolCalls(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	tests := []struct {
		name    string
		callID  string
		input   string
		wantSub string
	}{
		{
			name:    "bad JSON",
			callID:  "call_bad_json",
			input:   `{invalid json}`,
			wantSub: "invalid load_skill arguments",
		},
		{
			name:    "empty skill_name",
			callID:  "call_empty_name",
			input:   `{"skill_name":""}`,
			wantSub: "non-empty skill_name",
		},
		{
			name:    "unknown skill",
			callID:  "call_unknown",
			input:   `{"skill_name":"nonexistent"}`,
			wantSub: `skill "nonexistent" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := []ToolCallRequest{
				{ID: tt.callID, Name: LoadSkillToolName, Input: json.RawMessage(tt.input)},
			}
			msgs := e.processLoadSkillCalls(calls)
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

// TestProcessLoadSkillCalls_ValidReturnsFullContent verifies that a valid
// load_skill call returns the skill's full content as the tool result and
// writes NO engine state (load semantics, not activate semantics).
func TestProcessLoadSkillCalls_ValidReturnsFullContent(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	calls := []ToolCallRequest{
		{ID: "call_ok", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
	}
	msgs := e.processLoadSkillCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_ok" {
		t.Errorf("ToolCallID = %q, want call_ok", msgs[0].ToolCallID)
	}
	if !strings.Contains(msgs[0].Content, "[SKILL — brainstorming]") {
		t.Errorf("Content = %q, want [SKILL — brainstorming] marker", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "step 1...") {
		t.Errorf("Content = %q, want full skill content", msgs[0].Content)
	}
}

// TestProcessLoadSkillCalls_Mixed verifies that when load_skill calls
// are mixed with regular tool calls, only load_skill calls get responses
// from processLoadSkillCalls (regular calls are handled elsewhere).
func TestProcessLoadSkillCalls_Mixed(t *testing.T) {
	skillReg := skill.NewRegistry()
	skillReg.Register(&skill.Skill{
		Name:        "brainstorming",
		Description: "design before code",
		Content:     "step 1...",
	})

	e := &Engine{
		skills: skillReg,
		state:  &TaskState{},
		config: EngineConfig{},
	}

	calls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"foo.go"}`)},
		{ID: "call_bad", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"nope"}`)},
		{ID: "call_ok", Name: LoadSkillToolName, Input: json.RawMessage(`{"skill_name":"brainstorming"}`)},
		{ID: "call_grep", Name: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)},
	}

	msgs := e.processLoadSkillCalls(calls)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 tool responses (for load_skill only), got %d", len(msgs))
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
		t.Error("regular call 'read' should not get a response from processLoadSkillCalls")
	}
	if ids["call_grep"] {
		t.Error("regular call 'grep' should not get a response from processLoadSkillCalls")
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

	msgs := e.processHandoffResults(handoffCalls, results)
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

// TestProcessHandoffResults_NoOrphansNormal verifies the happy path: every
// handoff call gets a response message.
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

	msgs := e.processHandoffResults(handoffCalls, results)

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (handoff only), got %d", len(msgs))
	}
}
