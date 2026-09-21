package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepact/deepact/engine"
)

// SubAgentBackend runs one handoff delegation. depth is the depth of the NEW
// sub-agent (0 = first level). userLang is the session language ("中文" or "").
type SubAgentBackend func(ctx context.Context, params engine.HandoffToAgentParams, depth int, userLang string) (engine.ToolResult, error)

// AgentInfo describes a registered sub-agent for the handoff tool's dynamic
// enum: the ID is the selectable value, Description tells the delegating model
// when to use this role (codex-style role guidance in the tool schema).
type AgentInfo struct {
	ID          string
	Description string
}

// SubAgentTool is the model-facing handoff_to_agent tool registered in the
// standard tool registry. It dispatches by ToolContext.Depth:
//   - depth == 0 → main backend (Engine.RunSubAgent): first-level delegation.
//   - depth > 0  → nested backend (SubAgentRunner.RunSubAgent): deeper nesting.
type SubAgentTool struct {
	main     SubAgentBackend
	nested   SubAgentBackend
	agents   func() []AgentInfo
	maxDepth int
}

// NewSubAgentTool constructs the tool. agents returns the current registered
// agent infos for the dynamic enum; maxDepth caps nesting (0 disables the depth check).
func NewSubAgentTool(main, nested SubAgentBackend, agents func() []AgentInfo, maxDepth int) *SubAgentTool {
	return &SubAgentTool{main: main, nested: nested, agents: agents, maxDepth: maxDepth}
}

func (t *SubAgentTool) Spec() ToolSpec {
	infos := []AgentInfo{{ID: "sub", Description: "Execute a well-defined subtask with specified tools"}}
	if t.agents != nil {
		if got := t.agents(); len(got) > 0 {
			infos = got
		}
	}
	enumJSON, err := json.Marshal(infos)
	if err != nil {
		enumJSON = []byte(`["sub"]`)
	}
	// Build the enum as objects so each role carries its description — the
	// delegating model reads these to decide which role fits the subtask.
	params := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"agent": {"type": "string", "enum": %s,
				"description": "Target agent (role). Roles are registered agents with a stable persona, default tool set, and optional model/turn config."},
			"goal": {"type": "string", "description": "What the agent should accomplish"},
			"context": {"type": "string", "description": "Relevant context for the sub-agent"},
			"tools": {"type": "array", "items": {"type": "string"},
				"description": "Tools the sub-agent is allowed to use (optional; defaults to the role's tool set)"},
			"constraints": {"type": "array", "items": {"type": "string"},
				"description": "Constraints for the sub-agent (optional)"},
			"expected_output": {"type": "string",
				"description": "What a successful result looks like — acceptance criteria (optional)"}
		},
		"required": ["agent", "goal"]
	}`, enumJSON)
	return ToolSpec{
		Name:        engine.HandoffToolName,
		Description: "Delegate a sub-task to a specialized agent (role). Sub-agents can research code, brainstorm solutions, or critically review decisions.",
		Parameters:  json.RawMessage(params),
	}
}

func (t *SubAgentTool) Run(ctx ToolContext, input json.RawMessage) (ToolResultEnvelope, error) {
	if t.maxDepth > 0 && ctx.Depth > t.maxDepth {
		return ToolResultEnvelope{
			Status: StatusError,
			Digest: fmt.Sprintf("Max nesting depth (%d) reached. Cannot delegate further.", t.maxDepth),
		}, nil
	}
	var params engine.HandoffToAgentParams
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResultEnvelope{
			Status: StatusError,
			Digest: fmt.Sprintf("invalid handoff params: %v", err),
		}, nil
	}
	var res engine.ToolResult
	var err error
	if ctx.Depth == 0 {
		res, err = t.main(ctx.Ctx, params, 0, ctx.UserLang)
	} else {
		res, err = t.nested(ctx.Ctx, params, ctx.Depth, ctx.UserLang)
	}
	if err != nil {
		return ToolResultEnvelope{Status: StatusError, Digest: err.Error()}, nil
	}
	return ToolResultEnvelope{
		ToolCallID:   res.ToolCallID,
		ToolName:     res.ToolName,
		Status:       res.Status,
		Digest:       res.Digest,
		ArtifactRef:  res.ArtifactRef,
		ExitCode:     res.ExitCode,
		FinishReason: res.FinishReason,
		Questions:    res.Questions,
	}, nil
}
