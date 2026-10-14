package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewDefaultRegistry_BuiltinRoles(t *testing.T) {
	runner := &SubAgentRunner{}
	reg := NewDefaultRegistry(runner)

	specs := reg.AgentSpecs()
	got := map[AgentID]AgentSpec{}
	for _, s := range specs {
		got[s.ID] = s
	}

	for _, id := range []AgentID{AgentSub, AgentResearcher, AgentProposer, AgentRedTeam, AgentArbitrator} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing built-in role %q; specs=%+v", id, got)
		}
	}

	r := got[AgentResearcher]
	if r.Persona == "" {
		t.Error("researcher should carry a persona")
	}
	if len(r.ToolNames) == 0 {
		t.Error("researcher should have a read-only tool allowlist")
	}
	// A researcher's deliverable IS prose ("a concise research note"), so it
	// must terminate through submit_result like every other structured role.
	// As a non-structured role its finished report was treated as "narration":
	// the engine nudged it to "use tools", it kept going, and the run ended as
	// stalled_narration / max_iterations with a misleading "partial result"
	// heading. That was the observed failure mode for 4 of 6 collab researchers.
	if !r.StructuredResult {
		t.Error("researcher must be structured: its prose report has to terminate via submit_result, not the narration guard")
	}
	// A pointedly tight cap: 30 turns of 1M-context reads per direction is what
	// made one /collab round cost ~15M prompt tokens. One direction needs far
	// fewer turns; the wrap-up nudge (see budgetNudgeAt) covers the tail.
	if r.MaxIterations != 15 {
		t.Errorf("researcher MaxIterations = %d, want 15 (a single direction must not be allowed 30 turns)", r.MaxIterations)
	}
	// The persona must state a stopping rule: "thoroughly investigate" (the old
	// wording) reads as an instruction to exhaust the budget.
	if !strings.Contains(r.Persona, "Stop as soon as") {
		t.Errorf("researcher persona must state a stopping rule, got %q", r.Persona)
	}
	// ratd roles must carry their JSON-contract persona (stable system prefix)
	for _, id := range []AgentID{AgentProposer, AgentRedTeam, AgentArbitrator} {
		p := got[id]
		if p.Persona == "" {
			t.Errorf("ratd role %q should carry a persona", id)
		}
		if !containsJSONContract(p.Persona) {
			t.Errorf("ratd role %q persona should embed a JSON contract", id)
		}
		if len(p.ToolNames) == 0 {
			t.Errorf("ratd role %q should have a read-only tool allowlist", id)
		}
	}
}

// containsJSONContract reports whether the persona text mentions a JSON shape
// the role must output (CodePayload/TestPayload/decision).
func containsJSONContract(persona string) bool {
	for _, marker := range []string{"CodePayload", "TestPayload", "decision"} {
		if strings.Contains(persona, marker) {
			return true
		}
	}
	return false
}

func TestNewDefaultRegistry_UserOverridesBuiltin(t *testing.T) {
	runner := &SubAgentRunner{}
	// User redefines "researcher" with their own persona/model → user wins.
	userSpec := AgentSpec{
		ID:        AgentResearcher,
		Persona:   "你是用户定义的研究员",
		ModelName: "my-custom-model",
	}
	reg := NewDefaultRegistry(runner, []AgentSpec{userSpec})

	a, err := reg.Get(AgentResearcher)
	if err != nil {
		t.Fatalf("Get researcher: %v", err)
	}
	spec := a.Spec()
	if spec.Persona != "你是用户定义的研究员" {
		t.Errorf("researcher persona = %q, want user override", spec.Persona)
	}
	if spec.ModelName != "my-custom-model" {
		t.Errorf("researcher model = %q, want user override", spec.ModelName)
	}
	// Registry still has the other built-ins.
	if _, err := reg.Get(AgentProposer); err != nil {
		t.Errorf("proposer should remain registered: %v", err)
	}
}

func TestSpecSubAgent_Run_AppliesSpec(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: AgentSpec{
		ID:               AgentResearcher,
		Persona:          "研究员",
		ToolNames:        []string{"read", "grep"},
		ModelName:        "flash",
		MaxIterations:    12,
		StructuredResult: true,
	}}
	_, err := a.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "调研"})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if capture.input.Persona != "研究员" {
		t.Errorf("Persona = %q, want spec persona", capture.input.Persona)
	}
	if len(capture.input.Tools) != 2 || capture.input.Tools[0] != "read" {
		t.Errorf("Tools = %v, want spec tool allowlist", capture.input.Tools)
	}
	if capture.input.ModelOverride != "flash" {
		t.Errorf("ModelOverride = %q, want flash", capture.input.ModelOverride)
	}
	if capture.input.MaxIterations != 12 {
		t.Errorf("MaxIterations = %d, want 12", capture.input.MaxIterations)
	}
	if !capture.input.StructuredResult {
		t.Error("StructuredResult should be true from spec")
	}
}

// captureSubRunner implements subRunner and captures the Handoff passed to Run.
type captureSubRunner struct {
	input Handoff
}

func (c *captureSubRunner) Run(_ context.Context, input Handoff) (*HandoffResult, error) {
	c.input = input
	return &HandoffResult{Summary: "ok", FinishReason: HandoffReasonCompleted}, nil
}

func (c *captureSubRunner) SetOnProgress(ProgressFunc) {}

// TestNoCriticRole locks the removal decision: review is a user-invoked skill
// (/review) whose rubric lives in skill/builtin/review/SKILL.md, not a built-in
// role with a thin persona the delegating model can pick on its own. A role the
// model may choose costs a full sub-agent run every time it guesses; the skill
// costs one only when the user asks.
func TestNoCriticRole(t *testing.T) {
	reg := NewDefaultRegistry(&SubAgentRunner{})
	if _, err := reg.Get("critic"); err == nil {
		t.Error(`role "critic" must not be registered: review is /review, a user-invoked skill`)
	}
	// The static fallback schema is the one the model sees when the registered
	// SubAgentTool is absent (engine/sub_agent.go:1250), so its enum must drop
	// critic too, or the model can still ask for a role that no longer exists.
	spec := handoffToolSpec()
	var params struct {
		Properties struct {
			Agent struct {
				Enum []string `json:"enum"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal handoff parameters: %v", err)
	}
	for _, id := range params.Properties.Agent.Enum {
		if id == "critic" {
			t.Errorf("handoff schema enum must not offer %q, got %v", "critic", params.Properties.Agent.Enum)
		}
	}
	if !strings.Contains(string(spec.Function.Parameters), `"researcher"`) {
		t.Errorf("sanity: enum should still carry the roles that remain, got %v", params.Properties.Agent.Enum)
	}
}
