package engine

import (
	"context"
	"strings"
	"testing"
)

// A role that declares its text IS the result must complete on a text-only
// reply instead of being counted as narration and killed by the 3-strike guard.
func TestSubAgentTextResult_CompletesOnPlainText(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: `{"decision":"ACCEPT","feedback":"x"}`}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	res, err := runner.Run(context.Background(), Handoff{
		Agent: AgentArbitrator, Goal: "judge", TextResult: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("a text-result role must complete on a text reply, got %q", res.FinishReason)
	}
	if !strings.Contains(res.Summary, "ACCEPT") {
		t.Errorf("the text reply must BE the result, got %q", res.Summary)
	}
	if model.calls != 1 {
		t.Errorf("a declared text-result role must not be nudged: got %d model calls", model.calls)
	}
}

// Regression: without the declaration the guard must still bite, or the fix
// would silently disable runaway protection for every role.
func TestSubAgentTextResult_UndeclaredStillStrikes(t *testing.T) {
	text := ModelResponse{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"}
	model := &stubSeqModel{responses: []ModelResponse{text, text, text}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonStalledNarration {
		t.Fatalf("undeclared role must still be guarded, got %q", res.FinishReason)
	}
}

// The three ratd roles deliver their payload as plain text (persona: "Output
// ONLY a … JSON object"), so they must declare text-result completion — and
// must NOT be structured, because the two contracts contradict.
func TestDefaultAgents_RatdRolesDeclareTextResult(t *testing.T) {
	reg := NewDefaultRegistry(&SubAgentRunner{})
	specs := map[AgentID]AgentSpec{}
	for _, s := range reg.AgentSpecs() {
		specs[s.ID] = s
	}
	for _, id := range []AgentID{AgentProposer, AgentRedTeam, AgentArbitrator} {
		s, ok := specs[id]
		if !ok {
			t.Fatalf("role %q is not registered", id)
		}
		if !s.TextResult {
			t.Errorf("role %q delivers JSON text and must declare TextResult", id)
		}
		if s.StructuredResult {
			t.Errorf("role %q must not be structured and text-result at once", id)
		}
	}
}

// The declaration is a role invariant: the agent merges it into the handoff, so
// the delegating model can neither set nor clear it.
func TestSpecSubAgent_TextResultComesFromSpec(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: AgentSpec{ID: AgentArbitrator, TextResult: true}}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentArbitrator, Goal: "judge"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !capture.input.TextResult {
		t.Error("specSubAgent must merge the role's TextResult into the handoff")
	}
}

// End-to-end through the registry: an arbitrator answering with one JSON text
// reply completes on the FIRST reply, with no nudge and no strike. This is the
// regression that keeps the built-in ratd pipeline working.
func TestRatdArbitrator_CompletesWithoutNudge(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: `{"decision":"REJECT","feedback":"tests faulty"}`},
		FinishReason: "stop",
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	reg := NewDefaultRegistry(runner)
	agent, err := reg.Get(AgentArbitrator)
	if err != nil {
		t.Fatalf("arbitrator not registered: %v", err)
	}
	res, err := agent.Run(context.Background(), Handoff{Agent: AgentArbitrator, Goal: "judge"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("arbitrator must complete on its JSON reply, got %q", res.FinishReason)
	}
	if model.calls != 1 {
		t.Errorf("expected exactly one model call, got %d", model.calls)
	}
}
