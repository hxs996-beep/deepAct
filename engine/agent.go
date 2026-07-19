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
	AgentSub          AgentID = "sub"
	AgentCritic       AgentID = "critic"
	AgentTeamLead     AgentID = "team-lead"

	HandoffToolName        = "handoff_to_agent"
	ActivateSkillToolName  = "activate_skill"
	TaskCompleteToolName   = "task_complete"
)

// Handoff carries delegation parameters from parent to sub-agent.
type Handoff struct {
	Agent         AgentID  `json:"agent"`
	Goal          string   `json:"goal"`
	Context       string   `json:"context"`
	Tools         []string `json:"tools,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
	Depth         int      `json:"depth"`
	NoNudge       bool     `json:"no_nudge,omitempty"`
	MaxIterations int      `json:"max_iterations,omitempty"`
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
	Usage       *ModelUsage `json:"usage,omitempty"`
}

// AgentSpec describes an agent's identity and capabilities.
type AgentSpec struct {
	ID            AgentID
	Description   string
	ToolNames     []string // default tool allowlist (empty = all tools)
	ModelName     string   // if set, overrides runner's default model for this agent
	MaxIterations int      // 0 = use default (99). Set lower for agents that should finish quickly.
}

// Agent is the interface all sub-agents implement.
type Agent interface {
	ID() AgentID
	Spec() AgentSpec
	Run(ctx context.Context, input Handoff) (*HandoffResult, error)
}

// ActivateSkillParams is the JSON schema for the activate_skill tool call.
type ActivateSkillParams struct {
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
}

// TaskCompleteParams is the JSON schema for the task_complete tool call.
type TaskCompleteParams struct {
	Summary string `json:"summary"`
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

const maxSubAgentDepth = 2

// activateSkillToolSpec returns the tool definition exposed to LLMs for suggesting skill activation.
func activateSkillToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        ActivateSkillToolName,
			Description: "Activate a skill to guide the agent's methodology for the current task. Call this proactively BEFORE searching code or analyzing, whenever the user's request matches a skill in the Available Skills list. The skill's instructions will override general rules and become the governing framework.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill_name": {
						"type": "string",
						"description": "Name of the skill to activate, e.g. 'writing-plans'"
					},
					"reasoning": {
						"type": "string",
						"description": "Explain to the user why this skill should be activated next"
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
	agentDesc := "Target agent: sub (generic), critic (adversarial verifier)"
	goalDesc := "What the agent should accomplish"
	ctxDesc := "Relevant context for the sub-agent"
	toolsDesc := "Tools the sub-agent is allowed to use (optional)"
	constraintsDesc := "Constraints for the sub-agent (optional)"
	if zh {
		desc = "将子任务委派给专门的代理。子代理可以研究代码、头脑风暴方案，或批判性地审查决策。"
		agentDesc = "目标代理：sub（通用代理），critic（对抗性验证者）"
		goalDesc = "代理需要完成的目标"
		ctxDesc = "提供给子代理的相关上下文"
		toolsDesc = "允许子代理使用的工具（可选）"
		constraintsDesc = "对子代理的约束（可选）"
	}
	params := fmt.Sprintf(`{
				"type": "object",
				"properties": {
					"agent": {
						"type": "string",
						"enum": ["sub", "critic"],
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
					}
				},
				"required": ["agent", "goal"]
			}`, agentDesc, goalDesc, ctxDesc, toolsDesc, constraintsDesc)
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        HandoffToolName,
			Description: desc,
			Parameters:  json.RawMessage(params),
		},
	}
}

// formatHandoffResult serializes a HandoffResult into a digest string for injection into tool result history.
func formatHandoffResult(result *HandoffResult, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s\n", pickPrompt(zh, "Agent completed:", "代理完成："), result.Summary))
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
