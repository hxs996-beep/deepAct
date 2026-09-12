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
	AgentTeamLead AgentID = "team-lead"

	HandoffToolName        = "handoff_to_agent"
	LoadSkillToolName      = "load_skill"
	TaskCompleteToolName   = "task_complete"
	TodoWriteToolName      = "todo_write"
	SubmitResultToolName   = "submit_result"
	AskUserToolName        = "ask_user"
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
	Depth          int    `json:"depth"`
	NoNudge        bool   `json:"no_nudge,omitempty"`
	// MaxIterations caps the number of sub-agent turns; 0 = no cap (default).
	MaxIterations  int    `json:"max_iterations,omitempty"`
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
	Conclusions []string    `json:"conclusions"`
	Summary     string      `json:"summary"`
	Artifacts   []string    `json:"artifacts,omitempty"`
	Blocked     bool        `json:"blocked"`
	BlockedBy   string      `json:"blocked_by,omitempty"`
	TimedOut    bool        `json:"timed_out,omitempty"` // true when max iterations reached
	// FinishReason is the structured reason the run ended with
	// (HandoffReason* constants). "completed" means the agent delivered a
	// genuine result; every other value signals partial/no output and lets
	// the parent handle the outcome deterministically.
	FinishReason string      `json:"finish_reason,omitempty"`
	Usage        *ModelUsage `json:"usage,omitempty"`
	// Questions holds ask_user questions a sub-agent asked before ending.
	// Bubbles up through the tool result to the parent engine.
	Questions []string `json:"questions,omitempty"`
}

// AgentSpec describes an agent's identity and capabilities.
type AgentSpec struct {
	ID            AgentID
	Description   string
	ToolNames     []string // default tool allowlist (empty = all tools)
	ModelName     string   // if set, overrides runner's default model for this agent
	MaxIterations int      // 0 = no turn cap (default). Set > 0 for agents that must finish quickly (e.g. critic: 15).
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

// handoffToolSpec returns the tool definition exposed to LLMs for delegating to sub-agents.
// Tool description and parameter descriptions are localized to match the session language,
// preventing the English tool schema from biasing the LLM toward generating English goals
// in an otherwise Chinese session.
func handoffToolSpec(zh bool) ModelTool {
	desc := "Delegate a sub-task to a specialized agent. Sub-agents can research code, brainstorm solutions, or critically review decisions."
	agentDesc := "Target agent: sub (generic)"
	goalDesc := "What the agent should accomplish"
	ctxDesc := "Relevant context for the sub-agent"
	toolsDesc := "Tools the sub-agent is allowed to use (optional)"
	constraintsDesc := "Constraints for the sub-agent (optional)"
	expectedOutputDesc := "What a successful result looks like — acceptance criteria, output shape, or format the sub-agent must deliver (optional)"
	if zh {
		desc = "将子任务委派给专门的代理。子代理可以研究代码、头脑风暴方案，或批判性地审查决策。"
		agentDesc = "目标代理：sub（通用代理）"
		goalDesc = "代理需要完成的目标"
		ctxDesc = "提供给子代理的相关上下文"
		toolsDesc = "允许子代理使用的工具（可选）"
		constraintsDesc = "对子代理的约束（可选）"
		expectedOutputDesc = "什么样的结果算完成——验收标准、输出结构或子代理必须交付的格式（可选）"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"agent": {
						"type": "string",
						"enum": ["sub"],
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
					}
				},
				"required": ["agent", "goal"]
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc, expectedOutputDesc)
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
	optionsDesc := "Optional candidate answers (2-6 non-empty strings). When provided, the engine presents them as selectable options (方案A/B/C); when omitted, the user answers freely."
	if zh {
		desc = "当你需要用户提供无法自行确定的信息或做决定时（配置缺失、外部事实、用户偏好、权衡选择等），调用本工具。若有 2~6 个互斥的候选答案，作为 options 提供；否则省略 options 呈现开放式问题。能通过搜索代码或工具自行验证的信息不要调用。另外，在开始修改代码之前，若你的改动计划存在需用户取舍的权衡或方案选择，建议先用本工具向用户确认（可提供方案选项）。这是建议而非强制，是否确认由你根据任务自主决定。"
		questionDesc = "呈现给用户的问题。"
		optionsDesc = "可选候选答案（2~6 个非空字符串）。提供时引擎以可选项（方案A/B/C）呈现；省略时用户自由输入。"
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
		sb.WriteString(handoffReasonHeading(result.FinishReason, zh))
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
func handoffReasonHeading(reason string, zh bool) string {
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
	default:
		return pickPrompt(zh, "Sub-agent ended abnormally:", "子代理异常结束：")
	}
}
