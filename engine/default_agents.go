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
	// only NARROW the tool set per call: the read-only universe and the role's
	// own allowlist are invariants a per-call tools override cannot widen.
	if input.Persona == "" {
		input.Persona = spec.Persona
	}
	if len(input.Tools) == 0 {
		input.Tools = spec.ToolNames
	} else {
		zh := zhFromLang(input.UserLanguage)
		if bad := universeViolations(input.Tools); len(bad) > 0 {
			return nil, errors.New(pickPrompt(zh,
				fmt.Sprintf("tools not available to sub-agents: [%s]. Sub-agents are read-only; available tools: [%s]. Modification work belongs to the main agent — run it yourself",
					strings.Join(bad, ", "), strings.Join(subAgentUniverseNames, ", ")),
				fmt.Sprintf("工具 [%s] 对子代理不可用。子代理是只读的；可用工具：[%s]。修改类操作由你（主代理）直接执行",
					strings.Join(bad, ", "), strings.Join(subAgentUniverseNames, ", "))))
		}
		if len(spec.ToolNames) > 0 {
			narrowed := intersectToolSets(input.Tools, spec.ToolNames)
			if len(narrowed) == 0 {
				return nil, errors.New(pickPrompt(zh,
					fmt.Sprintf("role %s only has tools [%s]; requested [%s] does not overlap — delegate to a role that has them, or drop the tools override",
						string(spec.ID), strings.Join(spec.ToolNames, ", "), strings.Join(input.Tools, ", ")),
					fmt.Sprintf("角色 %s 只拥有工具 [%s]，请求的 [%s] 与之无交集——请委派给拥有这些工具的角色，或去掉 tools 覆盖",
						string(spec.ID), strings.Join(spec.ToolNames, ", "), strings.Join(input.Tools, ", "))))
			}
			input.Tools = narrowed
		}
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
// investigation with file:line citations) and critic (adversarial review):
// sub is the one-shot analyst to feed a large context into and get a
// structured conclusion back from. Sub-agents cannot modify files or run
// commands; the main agent applies changes itself.
func subSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentSub,
		Description:      "General-purpose read-only analyst: answer a well-scoped question or analyze the provided context, and return a structured conclusion. Cannot modify files or run commands.",
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
