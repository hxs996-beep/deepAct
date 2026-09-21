package engine

import (
	"context"
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

	for _, id := range []AgentID{AgentSub, AgentResearcher, AgentCritic, AgentProposer, AgentRedTeam, AgentArbitrator} {
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
	c := got[AgentCritic]
	if c.MaxIterations == 0 {
		t.Error("critic should have a turn cap")
	}
	if !c.StructuredResult {
		t.Error("critic should use structured result")
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
	if _, err := reg.Get(AgentCritic); err != nil {
		t.Errorf("critic should remain registered: %v", err)
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
