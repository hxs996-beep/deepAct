package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// runHandoff is the shared delegation core used by both backends.
// opts carries per-caller differences: how to resolve the agent registry,
// how to inject parent context, whether to accumulate usage, and how to
// localize output.
type handoffOptions struct {
	// resolve returns the target agent, or an error digest if unavailable.
	resolve func(AgentID) (Agent, error)
	// injectParentCtx optionally appends parent working context to params.Context.
	injectParentCtx func(params *HandoffToAgentParams)
	// accumulate reports sub-agent usage into the parent's counter.
	accumulate func(*ModelUsage)
	// zh localizes the result digest.
	zh bool
	// depth is the depth of the new sub-agent run (0 = first level).
	depth int
	// userLang is the session language ("中文" or "").
	userLang string
}

func runHandoff(ctx context.Context, call ToolCallRequest, opts handoffOptions) ToolResult {
	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := opts.resolve(AgentID(params.Agent))
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent not found: %s - %v", params.Agent, err),
		}
	}

	if opts.injectParentCtx != nil {
		opts.injectParentCtx(&params)
	}

	handoff := Handoff{
		Agent:          AgentID(params.Agent),
		Goal:           params.Goal,
		Context:        params.Context,
		Tools:          params.Tools,
		Constraints:    params.Constraints,
		ExpectedOutput: params.ExpectedOutput,
		Depth:          opts.depth,
		UserLanguage:   opts.userLang,
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent error: %v", err),
		}
	}

	if result.Usage != nil && opts.accumulate != nil {
		opts.accumulate(result.Usage)
	}

	status := "ok"
	if result.BlockedBy == "cancelled" {
		status = "cancelled"
	}
	return ToolResult{
		ToolCallID:   call.ID,
		ToolName:     HandoffToolName,
		Status:       status,
		Digest:       formatHandoffResult(result, opts.zh),
		FinishReason: result.FinishReason,
		Questions:    result.Questions,
	}
}

// RunSubAgent implements the main-agent backend for SubAgentTool: depth 0.
func (e *Engine) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang string) (ToolResult, error) {
	if e.agents == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	userLang = ""
	if e.isChinese {
		userLang = "中文"
	}
	res := runHandoff(ctx, call, handoffOptions{
		resolve: func(id AgentID) (Agent, error) { return e.agents.Get(id) },
		injectParentCtx: func(p *HandoffToAgentParams) {
			if e.state != nil {
				*p = injectMainAgentContext(*p, e.state)
			}
		},
		accumulate: e.accumulateUsage,
		zh:         e.isChinese,
		depth:      depth,
		userLang:   userLang,
	})
	// Bubble up sub-agent questions into the pending ask_user seam so the
	// existing awaiting_user / Options UI path presents them.
	if len(res.Questions) > 0 {
		e.pendingAskUser = &AskUserRequest{Question: res.Questions[0]}
	}
	return res, nil
}

// RunSubAgent implements the nested-agent backend for SubAgentTool: depth > 0.
func (r *SubAgentRunner) RunSubAgent(ctx context.Context, params HandoffToAgentParams, depth int, userLang string) (ToolResult, error) {
	if r.registry == nil {
		return ToolResult{Status: "error", Digest: "no agent registry configured"}, nil
	}
	call := ToolCallRequest{Name: HandoffToolName, Input: mustJSON(params)}
	res := runHandoff(ctx, call, handoffOptions{
		resolve: func(id AgentID) (Agent, error) { return r.registry.Get(id) },
		accumulate: nil,
		zh:       zhFromLang(userLang),
		depth:    depth,
		userLang: userLang,
	})
	return res, nil
}

// injectMainAgentContext appends the main agent's working set, memory markers,
// and modified files to the handoff context (moved from Engine.executeHandoff).
func injectMainAgentContext(params HandoffToAgentParams, state *TaskState) HandoffToAgentParams {
	extra := mainAgentContextText(state)
	if extra == "" {
		return params
	}
	if params.Context != "" {
		params.Context = params.Context + extra
	} else {
		params.Context = extra
	}
	return params
}

// mainAgentContextText builds the working-set/markers/modified-files block.
func mainAgentContextText(state *TaskState) string {
	var sb strings.Builder
	if len(state.WorkingSet.Files) > 0 {
		sb.WriteString("\n## Main Agent Context (Review Starting Point)\n")
		sb.WriteString("The main agent examined these files. Re-examine them from your own perspective:\n")
		for _, f := range state.WorkingSet.Files {
			sb.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Notes))
		}
	}
	if len(state.MemoryMarkers) > 0 {
		sb.WriteString("\nKey findings from the main agent (review for blind spots):\n")
		for _, m := range state.MemoryMarkers {
			sb.WriteString(fmt.Sprintf("  • %s\n", m))
		}
	}
	if len(state.ModifiedFiles) > 0 {
		sb.WriteString("\nFiles modified so far:\n")
		for _, f := range state.ModifiedFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
	}
	return sb.String()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
