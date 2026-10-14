package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// AgentID identifies a sub-agent type.
type AgentID string

const (
	AgentSub AgentID = "sub"

	HandoffToolName        = "handoff_to_agent"
	LoadSkillToolName      = "load_skill"
	TaskCompleteToolName   = "task_complete"
	TodoWriteToolName      = "todo_write"
	SubmitResultToolName   = "submit_result"
	AskUserToolName        = "ask_user"
	PlanTaskToolName       = "plan_task"
	AgentPollToolName      = "agent_poll"
	AgentResumeToolName    = "agent_resume"
	ListAgentsToolName     = "list_agents"
	WaitAgentToolName      = "wait_agent"
	SendMessageToolName    = "send_message"
	InterruptAgentToolName = "interrupt_agent"
	CloseAgentToolName     = "close_agent"
)

// HandoffResult.FinishReason vocabulary — the structured reason a sub-agent
// run ended with. The parent reacts on this instead of parsing prefixes in
// the digest text. Mirrors the harness subagent stop-reason vocabulary.
const (
	HandoffReasonCompleted        = "completed"
	HandoffReasonMaxTokens        = "max_tokens"        // a turn was cut off by the output cap (finish_reason=length)
	HandoffReasonCancelled        = "cancelled"         // context cancelled mid-run
	HandoffReasonError            = "error"             // LLM call failed
	HandoffReasonMaxIterations    = "max_iterations"    // iteration cap reached
	HandoffReasonLoopDetected     = "loop_detected"     // same operation repeated
	HandoffReasonStalledNarration = "stalled_narration" // text-only narration without acting
	HandoffReasonNoResult         = "no_result"         // structured run ended without submitting a result
	HandoffReasonMaxDepth         = "max_depth"         // nesting depth exceeded
	HandoffReasonAwaitingUser     = "awaiting_user"     // sub-agent asked the user; parent must present the question
	HandoffReasonBudgetExceeded   = "budget_exceeded"   // sub-agent exhausted its token budget (cache-miss + completion)
	// HandoffReasonFanoutLimit and HandoffReasonTreeBudgetExceeded are
	// REFUSALS, not failures: the delegation tree was already at its limit and
	// nothing was run, so the result carries no partial output. Their headings
	// must therefore never use the "partial result" wording the run-that-
	// stopped-halfway reasons use — the parent's correct move is to collect a
	// running result or do the step itself.
	HandoffReasonFanoutLimit        = "fanout_limit"         // in-flight width cap reached
	HandoffReasonTreeBudgetExceeded = "tree_budget_exceeded" // this Run's sub-agent token cap exhausted
	// HandoffReasonAsyncRunning is the FinishReason on the immediate dispatch
	// result of an async handoff (handoff_to_agent async:true). It is NOT a
	// failure: the sub-agent is still running in the background and the
	// delegating agent should poll it later via agent_poll.
	HandoffReasonAsyncRunning = "async_running"
)

// Handoff carries delegation parameters from parent to sub-agent.
type Handoff struct {
	Agent       AgentID  `json:"agent"`
	Goal        string   `json:"goal"`
	Context     string   `json:"context"`
	Tools       []string `json:"tools,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
	// ExpectedOutput states what a successful result looks like (acceptance
	// criteria the delegating agent sets). Injected into the volatile prompt
	// so the sub-agent knows the deliverable's shape instead of guessing.
	ExpectedOutput string `json:"expected_output,omitempty"`
	// Persona is the role's stable system instruction (codex-style developer
	// instructions). Unlike the volatile goal, it is injected as part of the
	// stable system prefix, so it stays constant across the sub-agent's turns
	// and keeps the shared system prompt prefix-cache hot. Empty = no role.
	Persona string `json:"persona,omitempty"`
	// ModelOverride, if set, overrides the runner's default model for this run
	// (e.g. a cheap role uses flash). "flash" selects the flash model.
	ModelOverride string `json:"model_override,omitempty"`
	Depth         int    `json:"depth"`
	// TextResult declares this run's completion semantics: a text-only reply IS
	// the result (the role delivers its payload as text, e.g. ratd's
	// CodePayload/TestPayload/decision JSON). Set from AgentSpec.TextResult by
	// the agent before Run executes; never model-settable. Without it, a
	// non-structured run treats a text-only reply as narration
	// (3-strike → stalled_narration).
	TextResult bool `json:"text_result,omitempty"`
	// MaxIterations caps the number of sub-agent turns; 0 = no cap (default).
	MaxIterations int `json:"max_iterations,omitempty"`
	// TokenBudget caps this run's billable tokens (cache-miss + completion;
	// cache hits are free). Three-state, identical at every level: 0 = inherit
	// from the next level down (spec → runner default), -1 = explicit
	// unlimited, >0 = explicit cap. Never in the tool schema: the model can
	// neither set nor widen it. The 80% wrap-up nudge may tell the model the
	// remaining amount — information, not a setting.
	TokenBudget int `json:"token_budget,omitempty"`
	// StructuredResult turns this run into a structured run: the loop injects
	// submit_result, and only a successful submission completes it. Set from
	// AgentSpec.StructuredResult by the agent before Run executes.
	StructuredResult bool `json:"structured_result,omitempty"`
	// ForkedHistory is the parent conversation prefix copied into this run,
	// injected verbatim after the system prompt. Internal: never model-settable,
	// never serialized (the model opts in through fork_turns, not by supplying
	// history itself).
	ForkedHistory []ModelMessage `json:"-"`
	// Inbox delivers mid-run messages injected by send_message. Internal: it is
	// never model-settable (the model writes only through the send_message tool,
	// which the engine routes here) and never serialized. nil for sync/nested
	// runs, which have no job handle to address.
	Inbox <-chan string `json:"-"`
}

// HandoffResult is returned by a sub-agent after execution.
type HandoffResult struct {
	Conclusions []string `json:"conclusions"`
	Summary     string   `json:"summary"`
	Artifacts   []string `json:"artifacts,omitempty"`
	Blocked     bool     `json:"blocked"`
	BlockedBy   string   `json:"blocked_by,omitempty"`
	TimedOut    bool     `json:"timed_out,omitempty"` // true when max iterations reached
	// FinishReason is the structured reason the run ended with
	// (HandoffReason* constants). "completed" means the agent delivered a
	// genuine result; every other value signals partial/no output and lets
	// the parent handle the outcome deterministically.
	FinishReason string      `json:"finish_reason,omitempty"`
	Usage        *ModelUsage `json:"usage,omitempty"`
	// Questions holds ask_user questions a sub-agent asked before ending.
	// Bubbles up through the tool result to the parent engine.
	Questions []string `json:"questions,omitempty"`
	// Suspended carries the resumable state of a run that ended on
	// awaiting_user. Internal: never serialized, never model-visible. The engine
	// registers it as an awaiting_user job so the SAME run can continue once the
	// user answers (agent_resume) instead of re-delegating from zero.
	Suspended *SuspendedRun `json:"-"`
	// RunID is the handle this suspension was registered under (internal). Only
	// the top-level delegation (depth 0) sets it, so a deeper handle never
	// reaches the model through a nested digest — see the plan's I8.
	RunID string `json:"-"`
}

// SuspendedRun is the resumable state of a sub-agent run that stopped on
// ask_user. Only the LEAF of a delegation chain may be resumed by the model:
// an intermediate entry is waiting for its child's result, not for the user, so
// filling it with the user's answer would write that answer into a handoff tool
// slot (see the plan's I8).
type SuspendedRun struct {
	// History is the breakpoint history, verbatim.
	History []ModelMessage
	// Question is what the user must answer. Set on the LEAF only: an
	// intermediate entry is waiting for its child's result, not for the user
	// (that distinction is what keeps agent_resume pointed at the leaf).
	Question string
	// PendingToolID is the tool_call_id awaiting its response: the ask_user call
	// on the leaf path, or the handoff call whose result never got written on
	// the nested-bubble path.
	PendingToolID string
	// PendingToolMissing is true when no tool message exists yet for
	// PendingToolID (the nested-bubble path returns before writing it) — resume
	// must APPEND; otherwise the placeholder's content is replaced in place.
	PendingToolMissing bool
	// Input is the original handoff (goal/context/tools/depth/lang).
	Input Handoff
	// Partition and Model are the run's prefix-cache partition and its forked
	// client, reused verbatim on resume — rebuilding either would turn the whole
	// history back into cache misses and defeat the point.
	Partition string
	Model     ModelClient
	// Spent is the usage accumulated so far; the budget continues from here.
	Spent ModelUsage
	// Iter is the loop counter at suspension. Resume restarts the loop from here
	// so a capped run does NOT get a fresh MaxIterations budget.
	Iter int
	// BudgetNudgedTokens carries the token-budget 80% nudge flag so resume does
	// not inject the wrap-up nudge a second time.
	BudgetNudgedTokens bool
	// ModelName is the *effective* model at suspension. NOT recomputable from
	// Input: the loop mutates it on the flash→Pro escalation path, so a resumed
	// flash agent would silently fall back to flash without this.
	ModelName string
	// ChildRunID is the job id of the child whose question caused this
	// suspension (empty when this run asked the user itself). Used to stitch the
	// resume cascade bottom-up.
	ChildRunID string
}

// AgentSpec describes an agent's identity and capabilities.
type AgentSpec struct {
	ID            AgentID
	Description   string
	ToolNames     []string // default tool allowlist (empty = all tools)
	ModelName     string   // if set, overrides runner's default model for this agent
	MaxIterations int      // 0 = no turn cap (default). Set > 0 for agents that must finish quickly (e.g. critic: 15).
	// TokenBudget is the role-level token budget merged into Handoff when the
	// delegating harness does not set one. Same three-state semantics as
	// Handoff.TokenBudget (0 = inherit, -1 = unlimited, >0 = explicit cap).
	TokenBudget int
	// Persona is the role's stable system instruction (codex-style developer
	// instructions). Injected as part of the sub-agent's stable system prefix
	// when this agent runs, keeping the shared prefix cache hot across turns.
	// Empty = generic sub-agent (no role personality).
	Persona string
	// StructuredResult injects a scoped submit_result tool: a text-only reply
	// never completes the run — the agent MUST call submit_result with its
	// final summary, so termination never depends on an LLM judgment call.
	StructuredResult bool
	// TextResult declares that this role's text-only reply IS its result, so a
	// non-structured run completes on it instead of counting it as narration.
	// Use it for roles whose contract is a text payload (ratd's
	// CodePayload/TestPayload/decision JSON). It is structurally the complement
	// of StructuredResult — a structured run completes only through
	// submit_result — so a role must not set both.
	TextResult bool
}

// Agent is the interface all sub-agents implement.
type Agent interface {
	ID() AgentID
	Spec() AgentSpec
	Run(ctx context.Context, input Handoff) (*HandoffResult, error)
}

// LoadSkillParams is the JSON schema for the load_skill tool call.
type LoadSkillParams struct {
	SkillName string `json:"skill_name"`
	// Resource optionally names a file inside the skill directory (a path
	// relative to it, e.g. "references/root-cause.md") to read instead of the
	// SKILL.md itself — progressive disclosure for the files a SKILL.md points at.
	Resource  string `json:"resource,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// HandoffToAgentParams is the JSON schema for the handoff_to_agent tool call.
type HandoffToAgentParams struct {
	Agent       string   `json:"agent"`
	Goal        string   `json:"goal"`
	Context     string   `json:"context,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
	// ExpectedOutput states what a successful result looks like — acceptance
	// criteria the delegating agent sets for the sub-agent.
	ExpectedOutput string `json:"expected_output,omitempty"`
	// Persona, when set, overrides the target agent's default persona for this
	// run (codex-style developer instructions injected into the stable system
	// prefix). Most callers leave it empty and use the role's built-in persona.
	Persona string `json:"persona,omitempty"`
	// ForkTurns copies the MAIN agent's conversation prefix into the sub-agent's
	// starting context: "none"/"" (default), "all", or a positive integer N (the
	// last N user turns). Only honored at depth 0 — a nested delegation has no
	// main history to fork. Forked tokens count toward the sub-agent's budget.
	ForkTurns string `json:"fork_turns,omitempty"`
	// Async starts the sub-agent in the background and returns immediately
	// with a job_id; the delegating agent polls it later via agent_poll.
	// Only honored at depth 0 (main agent); nested delegations ignore it.
	Async bool `json:"async,omitempty"`
	// Step optionally names the todo_write step (its "id" field) this
	// sub-agent executes. It is echoed back on the sub-agent's progress events
	// so the UI can nest the run under that step. Empty = no step linkage.
	Step string `json:"step,omitempty"`
}

// TaskCompleteParams is the JSON schema for the task_complete tool call.
type TaskCompleteParams struct {
	Summary string `json:"summary"`
}

// SubmitResultParams is the JSON schema for the sub-agent submit_result call.
// A sub-agent's structured run only completes through a valid submission.
type SubmitResultParams struct {
	Summary     string   `json:"summary"`
	Conclusions []string `json:"conclusions,omitempty"`
}

// taskCompleteToolSpec returns the tool definition for signaling task completion.
// The model calls this to submit its final output to the user.
func taskCompleteToolSpec() ModelTool {
	desc := "Submit your final conclusion or reply to the user. Call this when the user's goal is fully accomplished. This is the ONLY way to return output to the user. If your step panel (todo_write) still lists a step as in_progress, this call is deferred once and answered with a reminder to send the final snapshot first."
	summaryDesc := "Your final conclusion, analysis result, or reply to the user"
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["summary"]
			}`, summaryDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        TaskCompleteToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// loadSkillToolSpec returns the tool definition exposed to LLMs for loading
// a skill's full instructions on demand (deepseek-harness tool-skill model).
func loadSkillToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        LoadSkillToolName,
			Description: "Load the full instructions for an available skill, or one of its resource files. Call this with the exact skill name from the Available Skills list before acting on a task that names or clearly matches that skill. When the loaded SKILL.md points at another file inside the skill (e.g. references/foo.md), pass that path as `resource` to read it with this same tool; the path is relative to the skill directory. The catalog contains summaries only; do not infer or follow a skill's instructions until it has been loaded.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill_name": {
						"type": "string",
						"description": "Name of the skill to load, e.g. 'writing-plans'"
					},
					"resource": {
						"type": "string",
						"description": "Optional path of a file inside the skill directory to read instead of SKILL.md, e.g. 'references/root-cause.md'"
					},
					"reasoning": {
						"type": "string",
						"description": "Explain to the user why this skill should be loaded next"
					}
				},
				"required": ["skill_name"]
			}`),
		},
	}
}

// planTaskToolSpec returns the tool definition for the model to invoke
// deep-planning on complex tasks. Mirrors load_skill: the engine intercepts
// the call and injects the built-in planning methodology as the tool result.
// No parameters. The model decides when a task is complex enough to plan.
func planTaskToolSpec() ModelTool {
	desc := "When you judge the current task to be complex, multi-step, or requiring deep analysis before acting, call this tool to receive a deep-planning methodology. The engine injects a planning framework; follow it to understand the background, decompose, analyze, and produce a plan before executing. For simple tasks, use todo_write directly instead."
	params := `{"type":"object","properties":{},"required":[]}`
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        PlanTaskToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// handoffToolSpec returns the static fallback tool definition for delegating to
// sub-agents, used when the registered spec (tools/subagent.go's SubAgentTool,
// registered in cmd/run.go) is absent; filterTools prefers the registered one, so
// the two copies must stay in sync.
// The goal and context descriptions tell the delegating model to write them in the
// user's language, and the tool description carries the same rule for every prose
// field (a field added later is covered without touching either one): the goal is
// rendered in the UI's sub-agent row, and a run without fork_turns has no
// user-authored message, so the handed request is its only language evidence.
func handoffToolSpec() ModelTool {
	desc := "Delegate a sub-task to a specialized agent. Sub-agents are read-only: they research code, brainstorm solutions, or critically review decisions, but cannot modify files or run commands; the main agent applies changes itself. Write the goal, context, constraints and expected output in the user's language — the sub-agent mirrors the language it is handed, and the goal is shown to the user."
	agentDesc := "Target agent (role): sub (general read-only analysis), researcher (read-only investigation), critic (adversarial review), proposer/redteam/arbitrator (ratd pipeline roles)"
	goalDesc := "What the agent should accomplish, written in the user's language (the user sees this goal)"
	ctxDesc := "Relevant context for the sub-agent, written in the user's language"
	toolsDesc := "Read-only tools the sub-agent may use (optional; defaults to the role's set — you may only narrow it)"
	constraintsDesc := "Constraints for the sub-agent (optional)"
	expectedOutputDesc := "What a successful result looks like — acceptance criteria, output shape, or format the sub-agent must deliver (optional)"
	asyncDesc := "true = run the sub-agent in the background and return immediately with a job_id; you can continue other work and later query the result with agent_poll(job_id). false/omitted = synchronous wait (default). Prefer async for long-running independent read-only tasks (large-scope research, parallel review)."
	forkTurnsDesc := `How much of YOUR conversation to copy into the sub-agent as starting context: "none" (default, independent run), "all", or a number of preceding user turns as a string, e.g. "2". Only the first-level delegation can fork.`
	stepDesc := `Optional id of the todo_write step this sub-agent executes (that step's "id" field). It is echoed back on the sub-agent's progress events, and the UI nests the sub-agent under that step. Omit it when this delegation does not correspond to a step.`
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
				"agent": {
					"type": "string",
					"enum": ["sub", "researcher", "critic", "proposer", "redteam", "arbitrator"],
					"description": %q
				},
					"goal": {
						"type": "string",
						"description": %q
					},
					"context": {
						"type": "string",
						"description": %q
					},
					"tools": {
						"type": "array",
						"items": {"type": "string"},
						"description": %q
					},
					"constraints": {
						"type": "array",
						"items": {"type": "string"},
						"description": %q
					},
					"expected_output": {
						"type": "string",
						"description": %q
					},
					"fork_turns": {
						"type": "string",
						"description": %q
					},
					"async": {
						"type": "boolean",
						"description": %q
					},
					"step": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["agent", "goal"]
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc, expectedOutputDesc, forkTurnsDesc, asyncDesc, stepDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        HandoffToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// todoWriteToolSpec returns the tool definition for tracking step progress.
// The model calls this to report the current state of its step-by-step todo
// list as a FULL snapshot (not a diff). The UI renders it as a plain-text
// todo list above the input. Skill-agnostic: any skill can use it.
func todoWriteToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        TodoWriteToolName,
			Description: "Report the current state of your step-by-step todo list. Call this whenever you start, complete, or change the status of a step, in the same turn the change happens: the step you are starting reads in_progress immediately, and a step whose work just finished reads completed right away — including a delegated step, once its sub-agent has returned. Batching updates until the end leaves the panel stuck on the step the user last heard about. Before you finish the task, send one final snapshot with every step completed (or the ones you knowingly left unfinished) and then call task_complete — that snapshot is what the user reads. Pass the FULL list of steps each time (snapshot, not diff). The UI displays it as a plain-text todo list. Status must be one of: pending, in_progress, completed. Give a step an \"id\" when you delegate work for it: handoff_to_agent(step=<id>) then nests that sub-agent under this step in the UI. Ids must be unique within the list when present.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"todos": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"id": {
									"type": "string",
									"description": "Optional stable key for this step, unique within the list. handoff_to_agent(step=<id>) links a sub-agent to it."
								},
								"content": {
									"type": "string",
									"description": "Step description (plain text)"
								},
								"status": {
									"type": "string",
									"enum": ["pending", "in_progress", "completed"]
								}
							},
							"required": ["content", "status"]
						}
					}
				},
				"required": ["todos"]
			}`),
		},
	}
}

// askUserToolSpec returns the tool definition for asking the user a question.
// The model calls this at any point (analysis report, mid-execution) when it
// needs the user to provide information or make a decision that cannot be
// determined from the codebase — covering config gaps, external facts, user
// preferences, tradeoff choices, etc. Provide 2-6 candidate answers to present
// them as selectable options; omit options for an open-ended question.
func askUserToolSpec() ModelTool {
	desc := "Call this tool when you need the user to provide information or make a decision that you cannot determine yourself (missing configuration, external facts, user preferences, tradeoff choices). If you have 2-6 mutually exclusive candidate answers, provide them as options; otherwise omit options for an open-ended question. Do NOT call it for information you can verify yourself by searching the code or using tools. Additionally, before you start modifying code, if your planned changes involve tradeoffs or design choices the user should weigh in on, consider confirming with the user first via this tool (you can provide plan options). This is a suggestion, not a requirement — decide based on the task."
	questionDesc := "The question to present to the user."
	optionsDesc := "Optional candidate answers (2-6 non-empty strings). When provided, the engine presents them as selectable options; when omitted, the user answers freely."
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"question": {
						"type": "string",
						"description": %q
					},
					"options": {
						"type": "array",
						"items": {"type": "string", "minLength": 1},
						"minItems": 2,
						"maxItems": 6,
						"description": %q
					}
				},
				"required": ["question"]
			}`, questionDesc, optionsDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        AskUserToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// agentPollToolSpec returns the tool definition for polling a background
// async sub-agent task. The engine intercepts the call and returns the
// task's current status (running/done/error); a done task delivers its
// result and is removed.
func agentPollToolSpec() ModelTool {
	desc := "Query the status and result of a background async sub-agent task (started with handoff_to_agent async:true). Returns running / done / error; a done task returns its result and is removed."
	jobDesc := "The job_id returned by the async handoff dispatch"
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": %q
					}
				},
				"required": ["job_id"]
			}`, jobDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        AgentPollToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// agentResumeToolSpec returns the tool definition for resuming a sub-agent that
// stopped to ask the user. The engine intercepts the call, drills down to the
// leaf of that delegation chain and continues it with the answer.
func agentResumeToolSpec() ModelTool {
	desc := "Resume a sub-agent that stopped to ask the user (see agent_poll: waiting for user input). Non-blocking: the run continues in the background, fetch its result later with agent_poll(run_id). Use it only when the user's latest message is answering that question."
	runDesc := "The job handle shown in the sub-agent's digest, e.g. bg-3"
	answerDesc := "The user's answer, verbatim"
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"run_id": {"type": "string", "description": %q},
					"answer": {"type": "string", "description": %q}
				},
				"required": ["run_id", "answer"]
			}`, runDesc, answerDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        AgentResumeToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// submitResultToolSpec returns the scoped tool definition for a structured
// sub-agent run. The model must report its final result through this call —
// plain text never completes a structured run (mirrors the harness
// structured_output tool).
func submitResultToolSpec() ModelTool {
	desc := "Report your final result. When your work is complete you MUST call this tool — only a submit_result call counts as your result, a plain text reply does not. Call it exactly once."
	summaryDesc := "Your final conclusion, analysis result, or reply to the parent agent"
	conclusionsDesc := "Key findings (optional)"
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"summary": {
						"type": "string",
						"description": %q
					},
					"conclusions": {
						"type": "array",
						"items": {"type": "string"},
						"description": %q
					}
				},
				"required": ["summary"]
			}`, summaryDesc, conclusionsDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        SubmitResultToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// submitResultInstruction is appended as the trailing user message of a
// structured run, so the requirement is visible at the highest recency
// position (mirrors the harness structured-output instruction section).
func submitResultInstruction() string {
	return "When you have your final answer, you MUST report it by calling the submit_result tool with arguments matching its parameter schema exactly. Do not finish with a plain text answer: only the submit_result call counts as your result."
}

// formatHandoffResult serializes a HandoffResult into a digest string for injection into tool result history.
// The heading is reason-aware: a run that ended without delivering a result
// must never claim "Agent completed" (the parent model decides based on the
// heading, so this is a deterministic signal, not a text-prefix heuristic).
func formatHandoffResult(result *HandoffResult) string {
	var sb strings.Builder
	cancelled := "Sub-agent was cancelled."
	switch result.FinishReason {
	case HandoffReasonCompleted, "":
		sb.WriteString(fmt.Sprintf("Agent completed: %s\n", result.Summary))
	case HandoffReasonCancelled:
		sb.WriteString(cancelled + "\n")
	default:
		sb.WriteString(handoffReasonHeading(result.FinishReason, result.RunID))
		sb.WriteString("\n")
		if result.Summary != "" {
			sb.WriteString(result.Summary + "\n")
		}
	}
	if len(result.Conclusions) > 0 {
		sb.WriteString("Key findings:\n")
		for _, c := range result.Conclusions {
			sb.WriteString(fmt.Sprintf("- %s\n", c))
		}
	}
	if len(result.Artifacts) > 0 {
		sb.WriteString("Artifacts:\n")
		for _, a := range result.Artifacts {
			sb.WriteString(fmt.Sprintf("  %s\n", a))
		}
	}
	if result.Blocked {
		sb.WriteString(fmt.Sprintf("Blocked: %s\n", result.BlockedBy))
	}
	return sb.String()
}

// handoffReasonHeading renders the reason-specific heading line for a run
// that did not deliver a result. Must not contain "completed" wording.
func handoffReasonHeading(reason string, runID string) string {
	switch reason {
	case HandoffReasonMaxTokens:
		return "Agent hit the response token limit (partial result):"
	case HandoffReasonError:
		return "Sub-agent failed:"
	case HandoffReasonMaxIterations:
		return "Sub-agent exceeded the turn limit (partial result):"
	case HandoffReasonLoopDetected:
		return "Sub-agent stopped for repeating the same operation:"
	case HandoffReasonStalledNarration:
		return "Sub-agent kept narrating without acting (partial result):"
	case HandoffReasonNoResult:
		return "Sub-agent ended without submitting a result (partial answer below):"
	case HandoffReasonMaxDepth:
		return "Sub-agent stopped: max nesting depth reached."
	case HandoffReasonBudgetExceeded:
		return "Sub-agent exceeded its token budget (partial result):"
	case HandoffReasonFanoutLimit:
		return "Delegation refused: too many sub-agents are already in flight."
	case HandoffReasonTreeBudgetExceeded:
		return "Delegation refused: this Run's sub-agent token budget is exhausted."
	case HandoffReasonAwaitingUser:
		// Not a failure: the run stopped because it needs input the user must
		// give. The handle is appended when the suspension was registered (top
		// level only), so the parent knows which job to resume.
		if runID != "" {
			return fmt.Sprintf("Sub-agent needs user input (%s):", runID)
		}
		return "Sub-agent needs user input:"
	default:
		return "Sub-agent ended abnormally:"
	}
}
