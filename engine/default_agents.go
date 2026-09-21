package engine

import "context"

// Built-in agent IDs (AgentSub is declared in agent.go).
const (
	AgentResearcher AgentID = "researcher" // read-only codebase investigator
	AgentCritic     AgentID = "critic"     // adversarial reviewer
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
	reg.Register(&specSubAgent{runner: runner, spec: criticSpec()})
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
	// turn cap, and structured-result completion. The delegating model may
	// still override tools/constraints per call; the role provides defaults.
	if input.Persona == "" {
		input.Persona = spec.Persona
	}
	if len(input.Tools) == 0 {
		input.Tools = spec.ToolNames
	}
	if input.ModelOverride == "" {
		input.ModelOverride = spec.ModelName
	}
	if input.MaxIterations == 0 {
		input.MaxIterations = spec.MaxIterations
	}
	input.StructuredResult = spec.StructuredResult
	return a.runner.Run(ctx, input)
}

func (a *specSubAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// subSpec is the generic, fully-capable sub-agent (the historical default).
func subSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentSub,
		Description:      "Execute a well-defined subtask with specified tools",
		StructuredResult: true,
	}
}

// researcherSpec is a read-only investigator for codebase questions. It mirrors
// codex's explorer role: fast, authoritative, tool-restricted to read-only.
func researcherSpec() AgentSpec {
	return AgentSpec{
		ID:            AgentResearcher,
		Description:   "Read-only codebase investigator: answer specific, well-scoped questions with file:line evidence. Use for research, not edits.",
		ToolNames:     []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations: 30,
		Persona:       researcherPersona,
	}
}

// criticSpec is an adversarial reviewer that checks work for blind spots.
// Tool-restricted to read-only; turn-capped so it converges quickly.
func criticSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentCritic,
		Description:      "Adversarial reviewer: critically examine code or a plan for defects, blind spots, and edge cases. Returns findings, does not edit.",
		ToolNames:        []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations:    15,
		StructuredResult: true,
		Persona:          criticPersona,
	}
}

// proposerSpec is the ratd implementation role: it produces full implementation
// code as a CodePayload JSON object. Read-only tools because it returns code as
// text; the orchestrator (not the proposer) applies it to disk.
func proposerSpec() AgentSpec {
	return AgentSpec{
		ID:            AgentProposer,
		Description:   "ratd implementation role: produce complete implementation code for a requirement, output only a CodePayload JSON object.",
		ToolNames:     []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations: 20,
		Persona:       proposerPersona,
	}
}

// redteamSpec is the ratd adversarial-test role: it produces tests that attack
// the implementation, output only a TestPayload JSON object.
func redteamSpec() AgentSpec {
	return AgentSpec{
		ID:            AgentRedTeam,
		Description:   "ratd red-team role: produce adversarial tests against an implementation, output only a TestPayload JSON object.",
		ToolNames:     []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations: 20,
		Persona:       redteamPersona,
	}
}

// arbitratorSpec is the ratd judgment role: it decides ACCEPT (implementation
// is faulty, return to proposer) or REJECT (tests are faulty), output only a
// decision JSON object.
func arbitratorSpec() AgentSpec {
	return AgentSpec{
		ID:            AgentArbitrator,
		Description:   "ratd arbitrator role: judge whether the implementation or the tests are at fault, output only a decision JSON object.",
		ToolNames:     []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations: 5,
		Persona:       arbitratorPersona,
	}
}

// researcherPersona is the stable system instruction for the researcher role.
const researcherPersona = `You are a researcher — an independent investigator. Your job is to thoroughly investigate one research direction using read-only tools and produce a concise research note with concrete evidence (file:line references). Do not modify any files.`

// criticPersona is the stable system instruction for the critic role.
const criticPersona = `You are a critic — an adversarial reviewer. Your job is to examine the provided code or plan for defects, blind spots, and edge cases, and report findings. You do not edit files; you only report. Be rigorous and specific, citing file:line evidence.`

// proposerPersona is the stable system instruction for the ratd proposer role.
const proposerPersona = `You are a proposer — a senior engineer. Based on the requirement, produce the implementation code. Output ONLY a CodePayload JSON object with exactly this shape:
{"language":"go","files":[{"path":"相对路径","content":"完整代码"}],"design_notes":"设计说明"}
Constraints: prefer a single file; do NOT write tests; code must be self-consistent and compilable. Nothing but the JSON object.`

// redteamPersona is the stable system instruction for the ratd red-team role.
const redteamPersona = `You are a red-team — an adversarial test engineer. Against the given implementation, produce adversarial tests. Output ONLY a TestPayload JSON object with exactly this shape:
{"tests":[{"path":"相对路径","content":"测试代码"}],"no_issues":false}
If there are no issues, output {"tests":[],"no_issues":true}. Nothing but the JSON object.`

// arbitratorPersona is the stable system instruction for the ratd arbitrator role.
const arbitratorPersona = `You are an arbitrator. Based on the failing tests and the sandbox output, judge which side is at fault. Output ONLY a decision JSON object with exactly this shape:
{"decision":"ACCEPT","feedback":"重构方向"} or {"decision":"REJECT","feedback":"红队测试缺陷"}
ACCEPT = the implementation is faulty; return to the proposer for rework. REJECT = the tests are faulty; delete those tests and return to the red-team. Nothing but the JSON object.`
