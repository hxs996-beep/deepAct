package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Built-in agent IDs (AgentSub is declared in agent.go).
const (
	AgentResearcher AgentID = "researcher" // read-only codebase investigator
	AgentProposer   AgentID = "proposer"   // ratd: produce implementation (CodePayload JSON)
	AgentRedTeam    AgentID = "redteam"    // ratd: produce adversarial tests (TestPayload JSON)
	AgentArbitrator AgentID = "arbitrator" // ratd: judge ACCEPT/REJECT (decision JSON)
)

// NewDefaultRegistry creates and registers all built-in agents, then overlays
// user-defined roles (from config.toml [agents]). User roles override built-ins
// with the same name (user wins).
func NewDefaultRegistry(runner *SubAgentRunner, userSpecs ...[]AgentSpec) *AgentRegistry {
	reg := NewAgentRegistry()
	// Built-in roles first (lowest priority).
	reg.Register(&specSubAgent{runner: runner, spec: subSpec()})
	reg.Register(&specSubAgent{runner: runner, spec: researcherSpec()})
	reg.Register(&specSubAgent{runner: runner, spec: proposerSpec()})
	reg.Register(&specSubAgent{runner: runner, spec: redteamSpec()})
	reg.Register(&specSubAgent{runner: runner, spec: arbitratorSpec()})
	// User-defined roles override built-ins on name conflict (user wins).
	for _, specs := range userSpecs {
		for _, spec := range specs {
			reg.Register(&specSubAgent{runner: runner, spec: spec})
		}
	}
	return reg
}

// subRunner is the minimal surface specSubAgent needs: run one handoff and
// optionally receive a progress callback. A concrete *SubAgentRunner satisfies
// it; tests inject a capture stub.
type subRunner interface {
	Run(ctx context.Context, input Handoff) (*HandoffResult, error)
	SetOnProgress(fn ProgressFunc)
}

// specSubAgent is a role-carrying sub-agent driven entirely by its AgentSpec.
type specSubAgent struct {
	runner subRunner
	spec   AgentSpec
}

func (a *specSubAgent) ID() AgentID { return a.spec.ID }
func (a *specSubAgent) Spec() AgentSpec {
	return a.spec
}
func (a *specSubAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	spec := a.spec
	// Apply the role's structural configuration (codex-style): stable persona
	// injected as system instructions, default tool allowlist, model override,
	// and structured-result completion. The delegating model may
	// only NARROW the tool set per call: the read-only universe and the role's
	// own allowlist are invariants a per-call tools override cannot widen.
	if input.Persona == "" {
		input.Persona = spec.Persona
	}
	if len(input.Tools) == 0 {
		input.Tools = spec.ToolNames
	} else {
		if bad := universeViolations(input.Tools); len(bad) > 0 {
			return nil, errors.New(fmt.Sprintf("tools not available to sub-agents: [%s]. Sub-agents are read-only; available tools: [%s]. Modification work belongs to the main agent — run it yourself",
				strings.Join(bad, ", "), strings.Join(subAgentUniverseNames, ", ")))
		}
		if len(spec.ToolNames) > 0 {
			narrowed := intersectToolSets(input.Tools, spec.ToolNames)
			if len(narrowed) == 0 {
				return nil, errors.New(fmt.Sprintf("role %s only has tools [%s]; requested [%s] does not overlap — delegate to a role that has them, or drop the tools override",
					string(spec.ID), strings.Join(spec.ToolNames, ", "), strings.Join(input.Tools, ", ")))
			}
			input.Tools = narrowed
		}
	}
	if input.ModelOverride == "" {
		input.ModelOverride = spec.ModelName
	}
	input.StructuredResult = spec.StructuredResult
	// Completion semantics are a role invariant, so the spec — not the caller —
	// decides whether text completes the run. An explicit caller opt-in is kept.
	if spec.TextResult {
		input.TextResult = true
	}
	return a.runner.Run(ctx, input)
}

func (a *specSubAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// RunSuspended continues a suspended run of this role. The runner is held as a
// narrow interface, so a runner without resume support fails loud instead of
// silently dropping the answer.
func (a *specSubAgent) RunSuspended(ctx context.Context, s *SuspendedRun, answer string) (*HandoffResult, error) {
	r, ok := a.runner.(interface {
		RunSuspended(context.Context, *SuspendedRun, string) (*HandoffResult, error)
	})
	if !ok {
		return nil, errors.New("this agent's runner does not support resuming")
	}
	return r.RunSuspended(ctx, s, answer)
}

// intersectToolSets returns the intersection of a and b, preserving a's
// order. A role-restricted agent can only have its tool set narrowed.
func intersectToolSets(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, n := range b {
		set[n] = true
	}
	var out []string
	for _, n := range a {
		if set[n] {
			out = append(out, n)
		}
	}
	return out
}

// subSpec is the general-purpose read-only analyst: no persona, the full
// read-only universe. It is distinct from researcher (evidence-driven
// investigation with file:line citations): sub is the one-shot analyst to feed
// a large context into and get a structured conclusion back from — including
// the independent reviewer the /review skill delegates to. Sub-agents cannot
// modify files or run commands; the main agent applies changes itself.
func subSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentSub,
		Description:      "General-purpose read-only analyst: answer a well-scoped question or analyze the provided context, and return a structured conclusion. Cannot modify files or run commands.",
		StructuredResult: true,
	}
}

// researcherSpec is a read-only investigator for codebase questions. It mirrors
// codex's explorer role: fast, authoritative, tool-restricted to read-only.
//
// It is STRUCTURED on purpose: a researcher's deliverable is prose, so as a
// non-structured role its finished report was classified as "narration": the
// engine nudged it to "use tools", it kept going, and the run ended as
// stalled_narration under a misleading "partial result" heading. 4 of 6
// researchers in one /collab round ended exactly that way. Structured runs are
// nudged toward submit_result instead, which is actionable.
//
// No turn cap and no budget knob: convergence is the run's own token budget —
// computed by the engine from the context window, never inherited or disabled —
// plus the 80% wrap-up nudge.
func researcherSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentResearcher,
		Description:      "Read-only codebase investigator: answer specific, well-scoped questions with file:line evidence. Use for research, not edits.",
		ToolNames:        []string{"read", "read_multi", "grep", "glob", "lsp"},
		StructuredResult: true,
		Persona:          researcherPersona,
	}
}

// proposerSpec is the ratd implementation role: it produces full implementation
// code as a CodePayload JSON object. Read-only tools because it returns code as
// text; the orchestrator (not the proposer) applies it to disk.
func proposerSpec() AgentSpec {
	return AgentSpec{
		ID:          AgentProposer,
		Description: "ratd implementation role: produce complete implementation code for a requirement, output only a CodePayload JSON object.",
		ToolNames:   []string{"read", "read_multi", "grep", "glob", "lsp"},
		Persona:     proposerPersona,
		// Its contract IS a text payload ("Nothing but the JSON object"), so its
		// text reply is the result — not narration.
		TextResult: true,
	}
}

// redteamSpec is the ratd adversarial-test role: it produces tests that attack
// the implementation, output only a TestPayload JSON object.
func redteamSpec() AgentSpec {
	return AgentSpec{
		ID:          AgentRedTeam,
		Description: "ratd red-team role: produce adversarial tests against an implementation, output only a TestPayload JSON object.",
		ToolNames:   []string{"read", "read_multi", "grep", "glob", "lsp"},
		Persona:     redteamPersona,
		// Delivers TestPayload JSON as text (see proposerSpec).
		TextResult: true,
	}
}

// arbitratorSpec is the ratd judgment role: it decides ACCEPT (implementation
// is faulty, return to proposer) or REJECT (tests are faulty), output only a
// decision JSON object.
func arbitratorSpec() AgentSpec {
	return AgentSpec{
		ID:          AgentArbitrator,
		Description: "ratd arbitrator role: judge whether the implementation or the tests are at fault, output only a decision JSON object.",
		ToolNames:   []string{"read", "read_multi", "grep", "glob", "lsp"},
		Persona:     arbitratorPersona,
		// Delivers the decision as a JSON text reply (see proposerSpec).
		TextResult: true,
	}
}

// researcherPersona is the stable system instruction for the researcher role.
// It states a STOPPING RULE: "thoroughly investigate" (the old wording) reads as
// an instruction to exhaust the budget, and runaway researchers did exactly that.
const researcherPersona = `You are a researcher — an independent investigator. Your job is to investigate one research direction using read-only tools and produce a concise research note with concrete evidence (file:line references). Do not modify any files.
Stop as soon as you can answer the direction you were given: report what you have found instead of exploring exhaustively. When your note is ready, submit it with submit_result — only that call counts as your result.`

// proposerPersona is the stable system instruction for the ratd proposer role.
const proposerPersona = `You are a proposer — a senior engineer. Based on the requirement, produce the implementation code. Output ONLY a CodePayload JSON object with exactly this shape:
{"language":"go","files":[{"path":"relative/path","content":"full code"}],"design_notes":"design notes"}
Constraints: prefer a single file; do NOT write tests; code must be self-consistent and compilable. Nothing but the JSON object.`

// redteamPersona is the stable system instruction for the ratd red-team role.
const redteamPersona = `You are a red-team — an adversarial test engineer. Against the given implementation, produce adversarial tests. Output ONLY a TestPayload JSON object with exactly this shape:
{"tests":[{"path":"relative/path","content":"test code"}],"no_issues":false}
If there are no issues, output {"tests":[],"no_issues":true}. Nothing but the JSON object.`

// arbitratorPersona is the stable system instruction for the ratd arbitrator role.
const arbitratorPersona = `You are an arbitrator. Based on the failing tests and the sandbox output, judge which side is at fault. Output ONLY a decision JSON object with exactly this shape:
{"decision":"ACCEPT","feedback":"rework direction"} or {"decision":"REJECT","feedback":"red-team test defect"}
ACCEPT = the implementation is faulty; return to the proposer for rework. REJECT = the tests are faulty; delete those tests and return to the red-team. Nothing but the JSON object.`
