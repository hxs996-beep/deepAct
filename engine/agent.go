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
	AgentBrainstorm   AgentID = "brainstorm"
	AgentCritic       AgentID = "critic"
	AgentCodeSearcher AgentID = "code_searcher"
	AgentProposer     AgentID = "proposer"
	AgentSearcher     AgentID = "searcher"
	AgentPlanner      AgentID = "planner"
	AgentChallenger   AgentID = "challenger"
	AgentTester       AgentID = "tester"

	HandoffToolName = "handoff_to_agent"
)

// Handoff carries delegation parameters from parent to sub-agent.
type Handoff struct {
	Agent       AgentID  `json:"agent"`
	Goal        string   `json:"goal"`
	Context     string   `json:"context"`
	Tools       []string `json:"tools,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
	Depth       int      `json:"depth"`
}

// HandoffResult is returned by a sub-agent after execution.
type HandoffResult struct {
	Conclusions []string    `json:"conclusions"`
	Summary     string      `json:"summary"`
	Artifacts   []string    `json:"artifacts,omitempty"`
	Blocked     bool        `json:"blocked"`
	BlockedBy   string      `json:"blocked_by,omitempty"`
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

// HandoffToAgentParams is the JSON schema for the handoff_to_agent tool call.
type HandoffToAgentParams struct {
	Agent       string   `json:"agent"`
	Goal        string   `json:"goal"`
	Context     string   `json:"context,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

const maxSubAgentDepth = 2

// handoffToolSpec returns the tool definition exposed to LLMs for delegating to sub-agents.
func handoffToolSpec() ModelTool {
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        HandoffToolName,
			Description: "Delegate a sub-task to a specialized agent. Sub-agents can research code, brainstorm solutions, or critically review decisions.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"agent": {
						"type": "string",
						"enum": ["sub", "brainstorm", "critic", "code_searcher", "proposer", "searcher", "planner", "challenger", "tester"],
						"description": "Target agent: sub (generic), brainstorm (ideas), critic (review), code_searcher (find code), proposer (propose), searcher (code search), planner (plan), challenger (review with scorecard), tester (verify code)"
					},
					"goal": {
						"type": "string",
						"description": "What the agent should accomplish"
					},
					"context": {
						"type": "string",
						"description": "Relevant context for the sub-agent"
					},
					"tools": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Tools the sub-agent is allowed to use (optional)"
					},
					"constraints": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Constraints for the sub-agent (optional)"
					}
				},
				"required": ["agent", "goal"]
			}`),
		},
	}
}

// formatHandoffResult serializes a HandoffResult into a digest string for injection into tool result history.
func formatHandoffResult(result *HandoffResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Agent completed: %s\n", result.Summary))
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
