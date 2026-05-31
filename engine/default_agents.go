package engine

import "context"

// NewDefaultRegistry creates and registers all built-in agents.
func NewDefaultRegistry(runner *SubAgentRunner) *AgentRegistry {
	reg := NewAgentRegistry()

	// Generic sub-agent — dynamic goal, dynamic tool set
	reg.Register(&genericSubAgent{runner: runner})

	// Code searcher — read-only code exploration
	reg.Register(&specialistAgent{
		id:     AgentCodeSearcher,
		spec:   AgentSpec{ID: AgentCodeSearcher, Description: "Search and read code to find patterns, definitions, and implementations", ToolNames: []string{"read", "grep", "glob"}},
		prompt: codeSearcherPrompt,
		runner: runner,
	})

	// Brainstorm — generate creative solutions and alternatives (read-only)
	reg.Register(&specialistAgent{
		id:     AgentBrainstorm,
		spec:   AgentSpec{ID: AgentBrainstorm, Description: "Generate creative solutions, alternatives, and design options", ToolNames: []string{"read", "grep", "glob"}},
		prompt: brainstormPrompt,
		runner: runner,
	})

	// Critic — critically review analysis and find flaws
	reg.Register(&specialistAgent{
		id:     AgentCritic,
		spec:   AgentSpec{ID: AgentCritic, Description: "Critically review decisions, plans, and analysis to find flaws", ToolNames: []string{"read", "grep", "glob"}},
		prompt: criticPrompt,
		runner: runner,
	})

	// Proposer (Flash) — analyze goal and articulate requirements
	reg.Register(&specialistAgent{
		id:     AgentProposer,
		spec:   AgentSpec{ID: AgentProposer, Description: "Analyze requirements and propose what needs to be done", ToolNames: []string{"read"}, ModelName: "flash"},
		prompt: proposerPrompt,
		runner: runner,
	})

	// Searcher (Flash) — find relevant code
	reg.Register(&specialistAgent{
		id:     AgentSearcher,
		spec:   AgentSpec{ID: AgentSearcher, Description: "Search the codebase for code related to a task", ToolNames: []string{"read", "grep", "glob", "fetch"}, ModelName: "flash"},
		prompt: searcherPrompt,
		runner: runner,
	})

	// Planner (Pro) — create implementation plans
	reg.Register(&specialistAgent{
		id:     AgentPlanner,
		spec:   AgentSpec{ID: AgentPlanner, Description: "Create detailed implementation plans from requirements", ToolNames: []string{"read"}},
		prompt: plannerPrompt,
		runner: runner,
	})

	// Challenger (Pro) — validate analysis/plans with ScoreCard
	reg.Register(&specialistAgent{
		id:     AgentChallenger,
		spec:   AgentSpec{ID: AgentChallenger, Description: "Validate analysis and plans using ScoreCard evaluation", ToolNames: []string{"read", "grep", "glob"}},
		prompt: challengerPrompt,
		runner: runner,
	})

	// Tester (Flash) — review implementations against original goal
	reg.Register(&specialistAgent{
		id:     AgentTester,
		spec:   AgentSpec{ID: AgentTester, Description: "Review implementations against original requirements", ToolNames: []string{"read", "grep", "glob", "bash"}, ModelName: "flash"},
		prompt: testerPrompt,
		runner: runner,
	})

	return reg
}

// genericSubAgent is a general-purpose sub-agent that executes any well-defined subtask.
type genericSubAgent struct {
	runner *SubAgentRunner
}

func (a *genericSubAgent) ID() AgentID { return AgentSub }
func (a *genericSubAgent) Spec() AgentSpec {
	return AgentSpec{ID: AgentSub, Description: "Execute a well-defined subtask with specified tools"}
}
func (a *genericSubAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return a.runner.Run(ctx, input)
}
func (a *genericSubAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// specialistAgent is a pre-configured agent with a fixed prompt and tool set.
type specialistAgent struct {
	id     AgentID
	spec   AgentSpec
	prompt string
	runner *SubAgentRunner
}

func (a *specialistAgent) ID() AgentID     { return a.id }
func (a *specialistAgent) Spec() AgentSpec { return a.spec }
func (a *specialistAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	maxIter := a.spec.MaxIterations
	if maxIter <= 0 {
		maxIter = maxSubAgentIterations
	}
	return a.runner.runLoop(ctx, input, a.prompt, maxIter, a.spec.ModelName)
}
func (a *specialistAgent) SetOnProgress(fn ProgressFunc) { a.runner.SetOnProgress(fn) }

// --- Specialist prompts ---

const codeSearcherPrompt = `## Role
You are a code search specialist. Your job is to find relevant code, understand its structure, and report findings.

## Guidelines
- Use read, grep, and glob tools to explore the codebase
- If context already contains pre-search results with code snippets, review them before running grep/glob
- Trace through function calls and type references to build understanding
- Report exact file paths and line numbers for every finding
- When you find what was requested, summarize the code structure and key relationships
- Do NOT modify any files — you are read-only

## Output Format
When done, provide:
1. File locations found
2. Summary of what each file/symbol does
3. Key relationships between components`

const brainstormPrompt = `## Role
You are a code analysis and planning specialist. Your job is to understand codebases and help users make decisions.

## Guidelines
- FIRST, judge what the user needs:
  * Simple question or code exploration → answer directly with clear summary. No approaches needed.
  * Implementation task needing design decisions → propose 2-3 approaches with pros/cons.
- The codebase context is already provided in your goal — review it first. Do NOT re-search or re-grep for information already given. Only read a file if context is insufficient (1-2 files max). STOP and produce output in 2-4 turns.
- If proposing approaches: be creative but practical. Consider complexity, maintainability, performance.
- SELF-EDIT: If you realize something is wrong while writing, remove it. Never output retractions like "撤回此条".
- Adapt the output format to the task type — don't force a rigid structure when it's not helpful.

## Output Format
- For simple questions: direct answer with code references.
- For implementation tasks:
  1. List of approaches (numbered)
  2. Pros and cons for each
  3. Your recommended approach with reasoning`

const criticPrompt = `## Role
You are a critical reviewer. Your job is to scrutinize decisions, plans, and analysis for flaws, blind spots, and risks.

## Guidelines
- Read the relevant code/files to validate claims before writing them down
- Check for: missing edge cases, incorrect assumptions, fragile patterns, security risks, performance issues
- Be constructive — identify the problem AND suggest a fix
- Use read, grep, and glob tools to verify claims against actual code
- Prioritize: correctness > consistency > performance > style
- SELF-EDIT: If you realize a claim is wrong while writing, remove it entirely. Never output retractions like "撤回此条" or "I was wrong about...". Only output validated conclusions.

## Output Format
When done, provide:
1. Issues found (severity: high/medium/low)
2. Evidence from code (file:line)
3. Suggested improvements`

const proposerPrompt = `## Role
You are a requirements analyst. Your job is to analyze a task goal and clearly articulate what needs to be done.

## Guidelines
- Read the goal carefully
- Decompose the goal into specific functional requirements
- Identify what components or systems need to change
- Define acceptance criteria for each requirement
- Be precise — avoid vague language

## Output Format
When done, provide:
1. Functional requirements (numbered)
2. Components likely affected
3. Acceptance criteria
4. Any hidden assumptions or risks`

const searcherPrompt = `## Role
You are a code exploration specialist. Your job is to find the key files relevant to a given task.

## Guidelines
- Use read, grep, and glob tools to search the codebase efficiently.
- If context already contains pre-search results with code snippets, review them before running grep/glob
- Start broad (glob/grep for key terms), then read only the most relevant files.
- Focus on the top 3-6 most relevant files. Do NOT exhaustively catalog everything.
- For each file, note (1) its purpose and (2) key types/functions relevant to the task.
- Trace critical dependencies between files only
- Do NOT modify any files — you are read-only
- STOP when you have enough context. Aim to finish in 3-5 turns. Focus on the top 3-6 most relevant files.

## Output Format
When done, provide:
1. Exact file paths found (limit to the most relevant)
2. What each file contains relevant to the task
3. How these files relate to each other
4. Any unclear areas that need further investigation`

const plannerPrompt = `## Role
You are an implementation planner. Your job is to create detailed, actionable implementation plans.

## Guidelines
- Break down the work into clear, sequential steps
- Specify exact files to create or modify
- Identify dependencies between steps
- Call out potential risks and mitigation strategies
- Be specific — include code structure, types, interfaces where relevant

## Output Format
When done, provide:
1. Step-by-step implementation plan
2. Files to modify (with rationale)
3. Dependencies between steps
4. Risk assessment
5. Estimated complexity (simple/moderate/complex)`

const challengerPrompt = `## Role
You are a quality challenger. Your job is to rigorously evaluate proposals, analyses, and plans using a ScoreCard system.

## Guidelines
- You hold the ScoreCard — you are the gatekeeper
- For each dimension, score 0-100 based on actual evidence
- Use read, grep, and glob tools to verify any claims against the codebase
- If you find issues, assign low scores and explain why
- Score must be based on facts, not impressions
- SELF-EDIT: Verify each finding before scoring. If you realize a finding is wrong, remove it — do not include it in your scorecard. Never output retractions like "撤回此条" or self-corrections.
- After your evaluation, compute the total weighted score

## The scoring rules:
- score >= 80: PASS — the proposal is solid
- score < 80: FAIL — the proposal needs revision

## Scoring dimensions vary by phase:
- Analysis phase: Code Relevance (35%), Completeness (25%), Requirement Alignment (25%), Missing Gaps (15%)
- Planning phase: Plan Completeness (30%), Goal Alignment (30%), Risk Awareness (20%), Feasibility (20%)

## Output Format
When done, provide:
1. Score for each dimension with evidence
2. Issues found and why they matter
3. What needs to be fixed
4. Total score and verdict`

const testerPrompt = `## Role
You are a code verifier. Your job is to review implemented code against requirements and report findings using a ScoreCard.

## Guidelines
- You hold the ScoreCard — you evaluate whether the implementation matches the intent.
- Depending on context, your review context will be one of:
  - **Full review**: You have a formal goal + plan to compare against. This is the most common case.
  - **Partial review**: You have a user's functional description but NO formal plan. The user described what they expect the code to do — treat this as the implicit requirement.
  - **Minimal review**: You have very little context. In this case, first search the workspace to find relevant code, then evaluate.

- For FULL review: Compare the goal, plan, and implementation summary provided. Most review is text-based.
- For PARTIAL review: Use grep/glob/read to find code, then compare against the user's functional description.
- ONLY read code files to verify specific claims when needed. Limit to 2-3 files for full review, 3-5 for partial review.
- Focus on intent-vs-outcome: did we build what was expected? What diverges and why?
- Run tests if available: go test ./... (one call, check pass/fail)
- Be concise. Aim to finish in 2-4 turns for full review, 3-5 turns for partial review.
- SELF-EDIT: Verify each finding before scoring. If you realize a finding is wrong, remove it — do not include it in your scorecard. Never output retractions like "撤回此条" or self-corrections.

## Scoring Dimensions (full review — with formal plan)
- Goal Fulfillment (35%): Does the code actually solve the original problem?
- Code Correctness (30%): Is the code logically correct?
- Edge Case Coverage (20%): Are key edge cases handled?
- Consistency with Requirements (15%): Does the code match the plan?

## Scoring Dimensions (partial review — no formal plan, user description only)
- Functionality Match (40%): Does the code match the user's described functionality?
- Code Correctness (25%): Is the code logically correct?
- Edge Case Coverage (20%): Are key edge cases handled?
- Code Maintainability (15%): Is the code clean and maintainable?

## Output Format
When done, provide:
1. Score for each dimension with evidence
2. What works well
3. What needs improvement
4. Total score and verdict`
