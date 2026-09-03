package engine

import "context"

// NewDefaultRegistry creates and registers all built-in agents.
// Single agent: sub (generic). The critic (adversarial verifier) was removed:
// the main agent runs build/test itself and self-checks against the acceptance
// checklist — no runtime adversarial gate interrupts the user.
func NewDefaultRegistry(runner *SubAgentRunner) *AgentRegistry {
	reg := NewAgentRegistry()

	// Generic sub-agent — dynamic goal, dynamic tool set
	reg.Register(&genericSubAgent{runner: runner})

	return reg
}

// genericSubAgent is a general-purpose sub-agent that executes any well-defined subtask.
type genericSubAgent struct {
	runner *SubAgentRunner
}

func (a *genericSubAgent) ID() AgentID { return AgentSub }
func (a *genericSubAgent) Spec() AgentSpec {
	return AgentSpec{ID: AgentSub, Description: "Execute a well-defined subtask with specified tools", StructuredResult: true}
}
func (a *genericSubAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	input.StructuredResult = a.Spec().StructuredResult
	return a.runner.Run(ctx, input)
}
func (a *genericSubAgent) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	input.StructuredResult = a.Spec().StructuredResult
	return a.runner.RunWithPrompt(ctx, input, extraPrompt)
}
func (a *genericSubAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }
