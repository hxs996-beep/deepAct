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

	HandoffToolName      = "handoff_to_agent"
	LoadSkillToolName    = "load_skill"
	TaskCompleteToolName = "task_complete"
	TodoWriteToolName    = "todo_write"
	SubmitResultToolName = "submit_result"
	AskUserToolName      = "ask_user"
	PlanTaskToolName     = "plan_task"
	AgentPollToolName    = "agent_poll"
	AgentResumeToolName  = "agent_resume"
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
	Depth          int    `json:"depth"`
	NoNudge        bool   `json:"no_nudge,omitempty"`
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
	// UserLanguage is the detected user language ("中文" etc.), set by the engine
	// before delegating. Used to inject language directives into sub-agent context.
	UserLanguage string `json:"-"`
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
	// Async starts the sub-agent in the background and returns immediately
	// with a job_id; the delegating agent polls it later via agent_poll.
	// Only honored at depth 0 (main agent); nested delegations ignore it.
	Async bool `json:"async,omitempty"`
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
func taskCompleteToolSpec(zh bool) ModelTool {
	desc := "Submit your final conclusion or reply to the user. Call this when the user's goal is fully accomplished. This is the ONLY way to return output to the user."
	summaryDesc := "Your final conclusion, analysis result, or reply to the user"
	if zh {
		desc = "提交最终结论或回复给用户。目标全部完成后调用此工具。这是向用户返回输出的唯一方式。"
		summaryDesc = "你的最终结论、分析结果或给用户的回复"
	}
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
			Description: "Load the full instructions for an available skill. Call this with the exact skill name from the Available Skills list before acting on a task that names or clearly matches that skill. The catalog contains summaries only; do not infer or follow a skill's instructions until it has been loaded.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill_name": {
						"type": "string",
						"description": "Name of the skill to load, e.g. 'writing-plans'"
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
func planTaskToolSpec(zh bool) ModelTool {
	desc := "When you judge the current task to be complex, multi-step, or requiring deep analysis before acting, call this tool to receive a deep-planning methodology. The engine injects a planning framework; follow it to understand the background, decompose, analyze, and produce a plan before executing. For simple tasks, use todo_write directly instead."
	if zh {
		desc = "当你判断当前任务复杂、多步骤、需要先深度分析再动手时，调用本工具获得深度分析方法论。引擎会注入规划框架，请先理解背景、拆解、多假设验证、产出计划，再开始执行。简单任务请直接用 todo_write，不必调用本工具。"
	}
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

// handoffToolSpec returns the tool definition exposed to LLMs for delegating to sub-agents.
// Tool description and parameter descriptions are localized to match the session language,
// preventing the English tool schema from biasing the LLM toward generating English goals
// in an otherwise Chinese session.
func handoffToolSpec(zh bool) ModelTool {
	desc := "Delegate a sub-task to a specialized agent. Sub-agents are read-only: they research code, brainstorm solutions, or critically review decisions, but cannot modify files or run commands; the main agent applies changes itself."
	agentDesc := "Target agent (role): sub (general read-only analysis), researcher (read-only investigation), critic (adversarial review), proposer/redteam/arbitrator (ratd pipeline roles)"
	goalDesc := "What the agent should accomplish"
	ctxDesc := "Relevant context for the sub-agent"
	toolsDesc := "Read-only tools the sub-agent may use (optional; defaults to the role's set — you may only narrow it)"
	constraintsDesc := "Constraints for the sub-agent (optional)"
	expectedOutputDesc := "What a successful result looks like — acceptance criteria, output shape, or format the sub-agent must deliver (optional)"
	asyncDesc := "true = run the sub-agent in the background and return immediately with a job_id; you can continue other work and later query the result with agent_poll(job_id). false/omitted = synchronous wait (default). Prefer async for long-running independent read-only tasks (large-scope research, parallel review)."
	if zh {
		desc = "将子任务委派给专门的代理。子代理是只读的：研究代码、头脑风暴方案、批判性审查；不能修改文件或运行命令，修改由主代理执行。"
		agentDesc = "目标代理（角色）：sub（通用只读分析）、researcher（只读调研）、critic（对抗审查）、proposer/redteam/arbitrator（ratd 管道角色）"
		goalDesc = "代理需要完成的目标"
		ctxDesc = "提供给子代理的相关上下文"
		toolsDesc = "允许子代理使用的只读工具（可选；默认用角色的工具集——只能收窄，不能放宽）"
		constraintsDesc = "对子代理的约束（可选）"
		expectedOutputDesc = "什么样的结果算完成——验收标准、输出结构或子代理必须交付的格式（可选）"
		asyncDesc = "true = 后台运行子代理并立即返回 job_id；你可以继续其他工作，稍后用 agent_poll(job_id) 查询结果。false/省略 = 同步等待（默认）。长耗时的独立只读任务（大范围调研、并行审查）优先用 async。"
	}
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
					"async": {
						"type": "boolean",
						"description": %q
					}
				},
				"required": ["agent", "goal"]
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc, expectedOutputDesc, asyncDesc)
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
			Description: "Report the current state of your step-by-step todo list. Call this whenever you start, complete, or change the status of a step. Pass the FULL list of steps each time (snapshot, not diff). The UI displays it as a plain-text todo list. Status must be one of: pending, in_progress, completed.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"todos": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
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
func askUserToolSpec(zh bool) ModelTool {
	desc := "Call this tool when you need the user to provide information or make a decision that you cannot determine yourself (missing configuration, external facts, user preferences, tradeoff choices). If you have 2-6 mutually exclusive candidate answers, provide them as options; otherwise omit options for an open-ended question. Do NOT call it for information you can verify yourself by searching the code or using tools. Additionally, before you start modifying code, if your planned changes involve tradeoffs or design choices the user should weigh in on, consider confirming with the user first via this tool (you can provide plan options). This is a suggestion, not a requirement — decide based on the task."
	questionDesc := "The question to present to the user."
	optionsDesc := "Optional candidate answers (2-6 non-empty strings). When provided, the engine presents them as selectable options; when omitted, the user answers freely."
	if zh {
		desc = "当你需要用户提供无法自行确定的信息或做决定时（配置缺失、外部事实、用户偏好、权衡选择等），调用本工具。若有 2~6 个互斥的候选答案，作为 options 提供；否则省略 options 呈现开放式问题。能通过搜索代码或工具自行验证的信息不要调用。另外，在开始修改代码之前，若你的改动计划存在需用户取舍的权衡或方案选择，建议先用本工具向用户确认（可提供方案选项）。这是建议而非强制，是否确认由你根据任务自主决定。"
		questionDesc = "呈现给用户的问题。"
		optionsDesc = "可选候选答案（2~6 个非空字符串）。提供时引擎以可选项呈现；省略时用户自由输入。"
	}
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
func agentPollToolSpec(zh bool) ModelTool {
	desc := "Query the status and result of a background async sub-agent task (started with handoff_to_agent async:true). Returns running / done / error; a done task returns its result and is removed."
	jobDesc := "The job_id returned by the async handoff dispatch"
	if zh {
		desc = "查询后台异步子代理任务（通过 handoff_to_agent async:true 启动）的状态与结果。返回 running / done / error；done 时返回结果并移除该任务。"
		jobDesc = "异步委派返回的 job_id"
	}
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
func agentResumeToolSpec(zh bool) ModelTool {
	desc := "Resume a sub-agent that stopped to ask the user (see agent_poll: waiting for user input). Non-blocking: the run continues in the background, fetch its result later with agent_poll(run_id). Use it only when the user's latest message is answering that question."
	runDesc := "The job handle shown in the sub-agent's digest, e.g. bg-3"
	answerDesc := "The user's answer, verbatim"
	if zh {
		desc = "让停下提问的子代理继续（见 agent_poll 的 waiting for user input）。非阻塞：run 在后台继续，之后用 agent_poll(run_id) 取结果。仅当用户这条消息正是在回答该问题时使用。"
		runDesc = "子代理 digest 里显示的句柄，例如 bg-3"
		answerDesc = "用户的回答原文"
	}
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
func submitResultToolSpec(zh bool) ModelTool {
	desc := "Report your final result. When your work is complete you MUST call this tool — only a submit_result call counts as your result, a plain text reply does not. Call it exactly once."
	summaryDesc := "Your final conclusion, analysis result, or reply to the parent agent"
	conclusionsDesc := "Key findings (optional)"
	if zh {
		desc = "提交最终结果。工作完成后必须调用此工具——只有 submit_result 调用算作完成，纯文本回复不算。只能调用一次。"
		summaryDesc = "你的最终结论、分析结果或给父代理的回复"
		conclusionsDesc = "关键发现（可选）"
	}
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
func submitResultInstruction(zh bool) string {
	if zh {
		return "当你得到最终答案时，必须调用 submit_result 工具提交结果（参数必须符合其 schema）。不要以纯文本结束：只有 submit_result 调用才算你的结果。"
	}
	return "When you have your final answer, you MUST report it by calling the submit_result tool with arguments matching its parameter schema exactly. Do not finish with a plain text answer: only the submit_result call counts as your result."
}

// formatHandoffResult serializes a HandoffResult into a digest string for injection into tool result history.
// The heading is reason-aware: a run that ended without delivering a result
// must never claim "Agent completed" (the parent model decides based on the
// heading, so this is a deterministic signal, not a text-prefix heuristic).
func formatHandoffResult(result *HandoffResult, zh bool) string {
	var sb strings.Builder
	cancelled := pickPrompt(zh, "Sub-agent was cancelled.", "子代理已取消。")
	switch result.FinishReason {
	case HandoffReasonCompleted, "":
		sb.WriteString(fmt.Sprintf("%s %s\n", pickPrompt(zh, "Agent completed:", "代理完成："), result.Summary))
	case HandoffReasonCancelled:
		sb.WriteString(cancelled + "\n")
	default:
		sb.WriteString(handoffReasonHeading(result.FinishReason, result.RunID, zh))
		sb.WriteString("\n")
		if result.Summary != "" {
			sb.WriteString(result.Summary + "\n")
		}
	}
	if len(result.Conclusions) > 0 {
		sb.WriteString(pickPrompt(zh, "Key findings:\n", "关键发现：\n"))
		for _, c := range result.Conclusions {
			sb.WriteString(fmt.Sprintf("- %s\n", c))
		}
	}
	if len(result.Artifacts) > 0 {
		sb.WriteString(pickPrompt(zh, "Artifacts:\n", "产出物：\n"))
		for _, a := range result.Artifacts {
			sb.WriteString(fmt.Sprintf("  %s\n", a))
		}
	}
	if result.Blocked {
		sb.WriteString(fmt.Sprintf("%s %s\n", pickPrompt(zh, "Blocked:", "受阻："), result.BlockedBy))
	}
	return sb.String()
}

// handoffReasonHeading renders the reason-specific heading line for a run
// that did not deliver a result. Must not contain "completed" wording.
func handoffReasonHeading(reason string, runID string, zh bool) string {
	switch reason {
	case HandoffReasonMaxTokens:
		return pickPrompt(zh, "Agent hit the response token limit (partial result):", "子代理达到输出上限（部分结果）：")
	case HandoffReasonError:
		return pickPrompt(zh, "Sub-agent failed:", "子代理失败：")
	case HandoffReasonMaxIterations:
		return pickPrompt(zh, "Sub-agent exceeded the turn limit (partial result):", "子代理超出轮次上限（部分结果）：")
	case HandoffReasonLoopDetected:
		return pickPrompt(zh, "Sub-agent stopped for repeating the same operation:", "子代理因重复同一操作被终止：")
	case HandoffReasonStalledNarration:
		return pickPrompt(zh, "Sub-agent kept narrating without acting (partial result):", "子代理持续叙述未执行（部分结果）：")
	case HandoffReasonNoResult:
		return pickPrompt(zh, "Sub-agent ended without submitting a result (partial answer below):", "子代理未提交结果（下方为部分答案）：")
	case HandoffReasonMaxDepth:
		return pickPrompt(zh, "Sub-agent stopped: max nesting depth reached.", "子代理停止：达到最大嵌套深度。")
	case HandoffReasonBudgetExceeded:
		return pickPrompt(zh, "Sub-agent exceeded its token budget (partial result):", "子代理超出 token 预算（部分结果）：")
	case HandoffReasonAwaitingUser:
		// Not a failure: the run stopped because it needs input the user must
		// give. The handle is appended when the suspension was registered (top
		// level only), so the parent knows which job to resume.
		if runID != "" {
			return pickPrompt(zh,
				fmt.Sprintf("Sub-agent needs user input (%s):", runID),
				fmt.Sprintf("子代理需要用户输入（%s）：", runID))
		}
		return pickPrompt(zh, "Sub-agent needs user input:", "子代理需要用户输入：")
	default:
		return pickPrompt(zh, "Sub-agent ended abnormally:", "子代理异常结束：")
	}
}
