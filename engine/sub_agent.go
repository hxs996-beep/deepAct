package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	maxSubAgentIterations  = 99
	defaultSubAgentContext = 1_048_576 // ~1M — match main engine context window
)

// SubAgentRunner runs the generic sub-agent loop.
// It is shared by all agent types; specialists inject extra system prompt content.
type SubAgentRunner struct {
	workDir          string // project root for tool execution (resolves relative paths)
	sessionID        string // session identifier for tool context
	model            ModelClient
	tools            ToolExecutor
	registry         *AgentRegistry
	modelName        string // default (Pro) model
	flashModelName   string // Flash model for cheaper agents
	maxContextTokens int    // context window limit; 0 = use defaultSubAgentContext
	onProgress       ProgressFunc
}

// NewSubAgentRunner creates a runner with the given LLM client, tool executor, and agent registry.
func NewSubAgentRunner(model ModelClient, tools ToolExecutor, registry *AgentRegistry, modelName string) *SubAgentRunner {
	return &SubAgentRunner{
		model:     model,
		tools:     tools,
		registry:  registry,
		modelName: modelName,
	}
}

// SetFlashModel sets the Flash model name for agents that should use a cheaper model.
func (r *SubAgentRunner) SetFlashModel(name string) {
	r.flashModelName = name
}

// SetRegistry sets the agent registry on the runner after creation.
// Used to break circular dependencies during initialization.
func (r *SubAgentRunner) SetRegistry(reg *AgentRegistry) {
	r.registry = reg
}

// SetOnProgress sets the progress callback for sub-agent execution visibility.
func (r *SubAgentRunner) SetOnProgress(fn ProgressFunc) {
	r.onProgress = fn
}

// SetWorkDir sets the project root directory for tool execution.
func (r *SubAgentRunner) SetWorkDir(dir string) {
	r.workDir = dir
}

// SetSessionID sets the session identifier for tool execution context.
func (r *SubAgentRunner) SetSessionID(id string) {
	r.sessionID = id
}

// SetMaxContextTokens overrides the default context window limit for this runner.
func (r *SubAgentRunner) SetMaxContextTokens(tokens int) {
	r.maxContextTokens = tokens
}

// contextLimit returns the effective context window limit.
func (r *SubAgentRunner) contextLimit() int {
	if r.maxContextTokens > 0 {
		return r.maxContextTokens
	}
	return defaultSubAgentContext
}

// Run executes a generic sub-agent with the given handoff.
func (r *SubAgentRunner) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return r.runLoop(ctx, input, "", maxSubAgentIterations)
}

// runLoop is the core sub-agent execution loop.
// extraPrompt is additional system-level instructions injected for specialist agents.
// maxIterations caps the number of LLM turns for this agent.
// modelOverride, if non-empty, overrides the runner's default model for this run.
func (r *SubAgentRunner) runLoop(ctx context.Context, input Handoff, extraPrompt string, maxIterations int, modelOverride ...string) (*HandoffResult, error) {
	if input.Depth > maxSubAgentDepth {
		return &HandoffResult{
			Summary:   fmt.Sprintf("Max agent nesting depth (%d) exceeded. Cannot delegate further.", maxSubAgentDepth),
			Blocked:   true,
			BlockedBy: "max_depth",
		}, nil
	}

	// Stable system message — identical across all sub-agent calls → prefix cache hit
	// Variable content (goal/context/constraints/extra) goes in the first user message
	history := []ModelMessage{
		{Role: "system", Content: r.stableSystemPrompt()},
	}
	if userContent := r.buildVariablePrompt(input, extraPrompt); userContent != "" {
		history = append(history, ModelMessage{Role: "user", Content: userContent})
	}

	filteredTools := r.filterTools(input.Tools)

	modelName := r.modelName
	if len(modelOverride) > 0 && modelOverride[0] != "" {
		if modelOverride[0] == "flash" && r.flashModelName != "" {
			modelName = r.flashModelName
		} else {
			modelName = modelOverride[0]
		}
	}

	agentName := string(input.Agent)
	if agentName == "" {
		agentName = "sub"
	}
	limit := r.contextLimit()
	compressThreshold := limit * 80 / 100
	var totalUsage ModelUsage
	consecutiveIntermediate := 0
	lastOpKey := ""
	sameOpCount := 0
	maxSameOp := 5
	for iter := 0; iter < maxIterations; iter++ {
		select {
		case <-ctx.Done():
			return &HandoffResult{
				Summary:   "(cancelled)",
				Blocked:   true,
				BlockedBy: "cancelled",
				Usage:     &totalUsage,
			}, nil
		default:
		}
		// Compress history if approaching context limit (80% threshold)
		if estimatedTokens(history) > compressThreshold {
			history = compressSubHistory(history)
		}

		if r.onProgress != nil {
			r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: turn %d", agentName, iter)})
		}
		req := ModelRequest{
			Model:           modelName,
			Messages:        history,
			Tools:           filteredTools,
			MaxTokens:       4096,
			ThinkingEnabled: false, // sub-agents do structured tasks, don't need open-ended thinking
		}

		// Heartbeat — emit periodic progress during the blocking LLM call so the UI
		// doesn't appear frozen. Stops automatically when Complete returns.
		heartbeatDone := make(chan struct{})
		if r.onProgress != nil {
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: thinking...", agentName)})
					case <-heartbeatDone:
						return
					}
				}
			}()
		}

		// Derive a per-call deadline to prevent sub-agent from hanging indefinitely.
		// 120s is generous for a single LLM call (including thinking).
		callCtx, callCancel := context.WithTimeout(ctx, 120*time.Second)
		resp, err := r.model.Complete(callCtx, req)
		callCancel()
		close(heartbeatDone)
		if err != nil {
			// Don't crash the parent session — return a graceful degradation.
			summary := r.summarizeHistory(history, input.Goal)
			return &HandoffResult{
				Summary:   "(sub-agent error: " + err.Error() + ") \n" + summary,
				Blocked:   true,
				BlockedBy: "sub_agent_error",
				Usage:     &totalUsage,
			}, nil
		}

		// Update progress with what the agent is actually working on
		if r.onProgress != nil {
			if len(resp.Message.ToolCalls) > 0 {
				toolNames := make([]string, 0, len(resp.Message.ToolCalls))
				for _, tc := range resp.Message.ToolCalls {
					toolNames = append(toolNames, tc.Function.Name)
				}
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, strings.Join(toolNames, ", "))})
			} else if resp.Message.Content != "" {
				preview := firstLine(resp.Message.Content, 60)
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, preview)})
				// Stream full content for progressive display
				r.onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: resp.Message.Content})
			}
		}
		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
		totalUsage.CacheHitTokens += resp.Usage.CacheHitTokens

		msg := resp.Message

		// Strip intermediate thinking text from content when tool calls exist.
		// The model sometimes outputs intent text alongside structured tool calls;
		// this text is noise and should not pollute the sub-agent's history.
		if len(msg.ToolCalls) > 0 && isIntermediateText(msg.Content) {
			msg.Content = ""
		}

		history = append(history, msg)

		// No tool calls → agent may be done
		if len(msg.ToolCalls) == 0 {
			consecutiveIntermediate++
			if consecutiveIntermediate >= 3 {
				// Break — model keeps producing text without acting
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			// Give one more chance with a nudge
			history = append(history, ModelMessage{
				Role:    "user",
				Content: getNudgeMessage(input.Goal),
			})
			continue
		}
		consecutiveIntermediate = 0 // reset on tool calls — agent is making progress

		// Process tool calls
		calls := make([]ToolCallRequest, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			if tc.Function.Name == "" {
				continue
			}
			calls = append(calls, ToolCallRequest{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}

		if len(calls) == 0 {
			return r.buildResult(msg.Content, input.Goal), nil
		}

		// Per-file loop detection: same tool+file repeated N consecutive turns → block
		opKey := firstOpKey(calls)
		if opKey != "" {
			if opKey == lastOpKey {
				sameOpCount++
				if sameOpCount >= maxSameOp {
					summary := r.summarizeHistory(history, input.Goal)
					return &HandoffResult{Summary: summary, Usage: &totalUsage}, nil
				}
			} else {
				sameOpCount = 1
			}
			lastOpKey = opKey
		}

		for _, call := range calls {
			if r.onProgress != nil {
				r.onProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Input)})
			}
			if call.Name == HandoffToolName && input.Depth < maxSubAgentDepth {
				// Execute sub-sub-agent
				result := r.executeSubHandoff(ctx, call, input.Depth+1)
				if r.onProgress != nil {
					r.onProgress(ProgressEvent{Type: "tool_done", Name: "handoff", Detail: briefDigest(result.Digest)})
				}
				history = append(history, ModelMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    result.Digest,
				})
			} else if call.Name == HandoffToolName {
				history = append(history, ModelMessage{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Max nesting depth reached. Cannot delegate further.",
				})
			} else {
				env := ToolExecContext{WorkDir: r.workDir, SessionID: r.sessionID}
				results := r.tools.Execute(env, []ToolCallRequest{call})
				if len(results) > 0 {
					if r.onProgress != nil {
						r.onProgress(ProgressEvent{Type: "tool_done", Name: results[0].ToolName, Detail: briefDigest(results[0].Digest), FullDetail: results[0].Digest})
					}
					history = append(history, ModelMessage{
						Role:       "tool",
						ToolCallID: results[0].ToolCallID,
						Content:    results[0].Digest,
					})
				}
			}
		}
	}

	// Max iterations reached — extract whatever findings the agent accumulated
	summary := r.summarizeHistory(history, input.Goal)
	return &HandoffResult{
		Summary: summary,
	}, nil
}

// summarizeHistory extracts the last meaningful assistant output from history
// when the sub-agent runs out of iterations. Falls back to listing tool outputs.
// firstOpKey extracts "toolName:path" from the first path-bearing call for loop detection.
func firstOpKey(calls []ToolCallRequest) string {
	for _, c := range calls {
		if c.Name != "edit" && c.Name != "write" {
			continue
		}
		if path := extractPathFromArgs(c.Input); path != "" {
			return c.Name + ":" + path
		}
	}
	return ""
}

func firstLine(s string, max int) string {
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		s = s[:idx]
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func (r *SubAgentRunner) summarizeHistory(history []ModelMessage, goal string) string {
	// Walk backward to find a substantive assistant message.
	// Skip messages that are structurally non-substantive: too short, or a
	// single line ending with ":" (model talking to itself about next step).
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			content := history[i].Content
			trimmed := strings.TrimSpace(content)
			if len(trimmed) < 50 {
				continue
			}
			// Single line ending with ":" → model self-instruction, not output
			if !strings.Contains(trimmed, "\n") && strings.HasSuffix(trimmed, ":") {
				continue
			}
			return "(analysis timed out, partial result)\n" + content
		}
	}
	// Fallback: compile tool discoveries
	var sb strings.Builder
	sb.WriteString("(analysis timed out — listing discoveries)\n")
	for _, msg := range history {
		if msg.Role == "tool" && msg.Content != "" {
			digest := msg.Content
			if len(digest) > 200 {
				digest = digest[:200] + "..."
			}
			sb.WriteString("- ")
			sb.WriteString(digest)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// stableSystemPrompt returns the fixed system identity shared by all sub-agents.
// Identical across every sub-agent call in the session → enables prefix cache hits.
func (r *SubAgentRunner) stableSystemPrompt() string {
	return `You are a sub-agent executing a delegated task. Complete the goal and report your findings.

When you complete the task, provide a summary of what you did and list key findings/conclusions.
You can delegate sub-tasks using the '` + HandoffToolName + `' tool.`
}

// buildVariablePrompt assembles the per-call variable content (goal, context, constraints, extra).
// This is appended as the first user message, after the stable system message.
func (r *SubAgentRunner) buildVariablePrompt(input Handoff, extraPrompt string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Goal\n%s\n\n", input.Goal))
	if input.Context != "" {
		sb.WriteString(fmt.Sprintf("## Context\n%s\n\n", input.Context))
	}
	if len(input.Constraints) > 0 {
		sb.WriteString(fmt.Sprintf("## Constraints\n- %s\n\n", strings.Join(input.Constraints, "\n- ")))
	}
	if extraPrompt != "" {
		sb.WriteString(extraPrompt + "\n\n")
	}
	return sb.String()
}

// filterTools returns a tool spec list filtered to only the allowed tools.
// If allowList is empty, all tools are allowed.
func (r *SubAgentRunner) filterTools(allowList []string) []ModelTool {
	all := r.tools.Specs()
	// Always include the handoff tool
	result := []ModelTool{handoffToolSpec()}

	if len(allowList) == 0 {
		return append(result, all...)
	}

	allowSet := make(map[string]bool, len(allowList))
	for _, name := range allowList {
		allowSet[name] = true
	}
	for _, spec := range all {
		if allowSet[spec.Function.Name] {
			result = append(result, spec)
		}
	}
	return result
}

// executeSubHandoff handles a handoff_to_agent call from within a sub-agent.
func (r *SubAgentRunner) executeSubHandoff(ctx context.Context, call ToolCallRequest, depth int) ToolResult {
	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := r.registry.Get(AgentID(params.Agent))
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent not found: %s - %v", params.Agent, err),
		}
	}

	handoff := Handoff{
		Agent:       AgentID(params.Agent),
		Goal:        params.Goal,
		Context:     params.Context,
		Tools:       params.Tools,
		Constraints: params.Constraints,
		Depth:       depth,
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

	digest := formatHandoffResult(result)
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   HandoffToolName,
		Status:     "ok",
		Digest:     digest,
	}
}

// buildResult extracts conclusions from the agent's final text response.
func (r *SubAgentRunner) buildResult(content string, goal string) *HandoffResult {
	result := &HandoffResult{
		Summary:     content,
		Conclusions: make([]string, 0),
	}

	// Try to extract conclusions from bullet points in the final text
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			conclusion := strings.TrimPrefix(trimmed, "- ")
			conclusion = strings.TrimPrefix(conclusion, "* ")
			result.Conclusions = append(result.Conclusions, conclusion)
		}
	}

	// If goal is short (< 80 chars), include it as artifact reference
	if len(goal) < 80 {
		result.Artifacts = []string{fmt.Sprintf("goal: %s", goal)}
	}

	return result
}

// estimatedTokens returns a rough token count for a slice of model messages.
// Uses len/4 heuristic (no external estimator dependency).
func estimatedTokens(history []ModelMessage) int {
	total := 0
	for _, msg := range history {
		total += len(msg.Content) / 4
		total += len(msg.ReasoningContent) / 4
		for _, tc := range msg.ToolCalls {
			total += len(tc.ID) / 4
			total += len(tc.Function.Name) / 4
			total += len(tc.Function.Arguments) / 4
		}
		total += 10 // per-message overhead
	}
	return total
}

// compressSubHistory reduces history size when approaching context limits.
// Keeps system (index 0) and first user message (index 1) intact.
// For older turns (beyond the latest 3 assistant+tool groups), tool result
// content is truncated to a short summary.
func compressSubHistory(history []ModelMessage) []ModelMessage {
	if len(history) <= 4 {
		return history
	}

	// Reserve indices 0 (system) and 1 (first user) — always keep intact
	stable := history[:2]
	rest := history[2:]

	// Group rest into turns: [assistant, tool...] pairs.
	// Walk backward to find the last 3 complete turns (keep fresh).
	type turn struct {
		start int
		end   int
	}
	var turns []turn
	i := len(rest) - 1
	for i >= 0 {
		if rest[i].Role == "assistant" {
			// assistant marks the start of a turn; all tool messages after it
			// belong to this turn. Walk forward from assistant to find tool messages.
			end := i + 1
			for end < len(rest) && rest[end].Role == "tool" {
				end++
			}
			turns = append(turns, turn{start: i, end: end})
			i--
		} else {
			i--
		}
	}

	// Keep fresh: last 3 turns
	keepTurns := 3
	if keepTurns > len(turns) {
		keepTurns = len(turns)
	}
	// Fresh turns are the last ones in the list (most recent)
	fresh := turns[:keepTurns]

	// Build result: stable + compressed old turns + fresh turns
	result := make([]ModelMessage, 0, len(stable)+len(rest))
	result = append(result, stable...)

	// Compress turns NOT in fresh set
	// Map fresh turn indices to actual ranges
	freshRange := make(map[int]bool)
	for _, ft := range fresh {
		for j := ft.start; j < ft.end; j++ {
			freshRange[j] = true
		}
	}

	for idx := 0; idx < len(rest); idx++ {
		if freshRange[idx] {
			result = append(result, rest[idx])
		} else if rest[idx].Role == "tool" && len(rest[idx].Content) > 300 {
			// Truncate old tool results
			msg := rest[idx]
			msg.Content = rest[idx].Content[:300] + "\n... (compressed)"
			result = append(result, msg)
		} else {
			result = append(result, rest[idx])
		}
	}

	return result
}

// getNudgeMessage returns a language-appropriate nudge when the sub-agent
// keeps producing text without tool calls.
func getNudgeMessage(goal string) string {
	if msgIsChinese(goal) {
		return "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	}
	return "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
}
