package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deepact/deepact/context/promptset"
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
	maxOutputTokens  int    // per-turn completion cap; 0 = use DefaultMaxOutputTokens
	onProgress       ProgressFunc
	compressor       *CompressionOrchestrator
	subAgentBaseURL  string // separate API endpoint for cache isolation; empty = use main agent's
	langPackZh       string // Chinese language pack (Go/Python rules in zh)
	langPackEn       string // English language pack (Go/Python rules in en)
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

// SetMaxOutputTokens overrides the per-turn completion cap for sub-agents.
func (r *SubAgentRunner) SetMaxOutputTokens(tokens int) {
	r.maxOutputTokens = tokens
}

func (r *SubAgentRunner) outputTokenCap() int {
	if r.maxOutputTokens > 0 {
		return r.maxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// SetCompressor sets the CompressionOrchestrator for layered compression (same as main agent).
// When set, replaces the simple compressSubHistory with the full 4-layer strategy.
func (r *SubAgentRunner) SetCompressor(c *CompressionOrchestrator) {
	r.compressor = c
}

// SetSubAgentBaseURL sets a separate API base URL for sub-agents. When set, sub-agents
// use this URL instead of the main agent's, giving them their own DeepSeek prefix cache
// partition. Empty string (default) means sub-agents share the main agent's endpoint.
func (r *SubAgentRunner) SetSubAgentBaseURL(url string) {
	r.subAgentBaseURL = url
}

// SetLangPacks sets both language variants of the language-specific rules.
// Called once at startup from cmd/run.go after language detection.
func (r *SubAgentRunner) SetLangPacks(zh, en string) {
	r.langPackZh = zh
	r.langPackEn = en
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
	iters := maxSubAgentIterations
	if input.MaxIterations > 0 {
		iters = input.MaxIterations
	}
	return r.runLoop(ctx, input, "", iters)
}

// RunWithPrompt runs a sub-agent with an extra system-level instruction prompt
// prepended to the volatile content. This is used by roundtable member agents
// that need a role-specific instruction (e.g. "你是一位安全工程师...") injected
// as a high-priority user message after the stable system prompt.
func (r *SubAgentRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	iters := maxSubAgentIterations
	if input.MaxIterations > 0 {
		iters = input.MaxIterations
	}
	return r.runLoop(ctx, input, extraPrompt, iters)
}

// runLoop is the core sub-agent execution loop.
// extraPrompt is additional system-level instructions injected for specialist agents.
// maxIterations caps the number of LLM turns for this agent.
// modelOverride, if non-empty, overrides the runner's default model for this run.
// subAgentStreamer guards sub-agent stream_delta emission. Sub-agents use
// non-streaming Complete, so resp.Message.Content is the full response text.
// Emitting it as a stream_delta on every runLoop iteration causes the UI to
// accumulate duplicate blocks (m.streaming += ...), producing repeated text
// and blank-line gaps. maybeEmit emits only the first non-empty content; the
// final answer is still surfaced by the main engine's Summary at run end.
type subAgentStreamer struct {
	streamed bool
}

// maybeEmit emits content as a stream_delta the first time it is called with
// non-empty content and a non-nil onProgress; subsequent calls are no-ops.
func (s *subAgentStreamer) maybeEmit(onProgress ProgressFunc, agentName, content string) {
	if s.streamed || content == "" || onProgress == nil {
		return
	}
	onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: content})
	s.streamed = true
}

func (r *SubAgentRunner) runLoop(ctx context.Context, input Handoff, extraPrompt string, maxIterations int, modelOverride ...string) (*HandoffResult, error) {
	if input.Depth > maxSubAgentDepth {
		return &HandoffResult{
			Summary:   fmt.Sprintf("Max agent nesting depth (%d) exceeded. Cannot delegate further.", maxSubAgentDepth),
			Blocked:   true,
			BlockedBy: "max_depth",
		}, nil
	}

	// Fork model client to get an independent ReasoningEchoManager.
	// Prevents the sub-agent's reasoning_content from leaking into the main agent's
	// next request via the shared mux-protected manager on DeepSeekClient.
	model := r.model
	if f, ok := r.model.(interface{ Fork() ModelClient }); ok {
		model = f.Fork()
	}
	// If a separate sub-agent base URL is configured, fork again with a different
	// endpoint for cache isolation. This gives sub-agents their own DeepSeek prefix
	// cache partition so their calls don't pollute the main agent's cached prefix.
	if r.subAgentBaseURL != "" {
		if f, ok := model.(interface{ ForkWithBaseURL(string) ModelClient }); ok {
			model = f.ForkWithBaseURL(r.subAgentBaseURL)
		}
	}

	// Stable system message — identical across all sub-agent calls → prefix cache hit
	// Stable agent-type instructions (extraPrompt) — identical per agent type → prefix cache hit
	// Volatile content (goal/context/constraints) — changes per call → cache miss (unavoidable)
	history := []ModelMessage{
		{Role: "system", Content: r.stableSystemPrompt(input.UserLanguage)},
	}
	if extraPrompt != "" {
		history = append(history, ModelMessage{Role: "user", Content: extraPrompt})
	}
	if volatileContent := r.buildVolatilePrompt(input); volatileContent != "" {
		history = append(history, ModelMessage{Role: "user", Content: volatileContent})
	}

	filteredTools := r.filterTools(input.Tools, input.UserLanguage)

	modelName := r.modelName
	isFlashAgent := false // 标记 agent 是否被分配为 Flash（用于失败升级回退）
	if len(modelOverride) > 0 && modelOverride[0] != "" {
		if modelOverride[0] == "flash" && r.flashModelName != "" {
			modelName = r.flashModelName
			isFlashAgent = true
		} else {
			modelName = modelOverride[0]
		}
	}

	agentName := string(input.Agent)
	if agentName == "" {
		agentName = "sub"
	}
	limit := r.contextLimit()
	compressThreshold := limit * 95 / 100
	var totalUsage ModelUsage
	consecutiveIntermediate := 0
	lastOpKey := ""
	sameOpCount := 0
	maxSameOp := 5
	streamer := subAgentStreamer{}
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
		// Compress history using layered strategy (same as main agent) when compressor is set.
		// Falls back to simple truncation if compressor is nil.
		if r.compressor != nil {
			tokens := r.compressor.EstimateTokens(history)
			if tokens > 0 {
				layer, should := r.compressor.ShouldCompress(tokens, limit)
				if should {
					if compressed, err := r.compressor.CompressModelMessages(layer, input.Goal, history); err == nil {
						history = compressed
					}
				}
			}
		} else if estimatedTokens(history) > compressThreshold {
			history = compressSubHistory(history)
		}

		if r.onProgress != nil {
			r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: turn %d", agentName, iter)})
		}
		req := ModelRequest{
			Model:           modelName,
			Messages:        history,
			Tools:           filteredTools,
			MaxTokens:       r.outputTokenCap(),
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
		resp, err := model.Complete(callCtx, req)
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
				// Stream full content for progressive display -- but only once
				// per runLoop. Subsequent text-only rounds re-emit the same
				// full body; without this guard the UI accumulates duplicates.
				streamer.maybeEmit(r.onProgress, agentName, resp.Message.Content)
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
			if input.NoNudge {
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			consecutiveIntermediate++
			if consecutiveIntermediate >= 3 {
				// Break — model keeps producing text without acting
				result := r.buildResult(msg.Content, input.Goal)
				result.Usage = &totalUsage
				return result, nil
			}
			// 失败回退升级：Flash agent 连续输出文本无 tool call → 升级到 Pro 重试
			if consecutiveIntermediate >= 2 && isFlashAgent && r.modelName != "" && modelName != r.modelName {
				modelName = r.modelName // 升级到 Pro
				history = append(history, ModelMessage{
					Role:    "user",
					Content: "The Flash model is having difficulty producing structured output. Escalating to Pro model. Please complete the task now.",
				})
				consecutiveIntermediate = 0
				continue
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
		opKey := firstOpKey(calls, r.workDir)
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
				r.onProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, r.workDir)})
			}
			if call.Name == HandoffToolName && input.Depth < maxSubAgentDepth {
				// Execute sub-sub-agent
				result := r.executeSubHandoff(ctx, call, input.Depth+1, input.UserLanguage)
				if r.onProgress != nil {
					r.onProgress(ProgressEvent{Type: "tool_done", Name: "handoff", Detail: briefDigest(result.Digest)})
				}
				if result.Status != "cancelled" {
					history = append(history, ModelMessage{
						Role:       "tool",
						ToolCallID: call.ID,
						Content:    result.Digest,
					})
				}
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
		TimedOut: true,
	}, nil
}

// summarizeHistory extracts the last meaningful assistant output from history
// when the sub-agent runs out of iterations. Falls back to listing tool outputs.
// firstOpKey extracts "toolName:path" from the first path-bearing call for loop detection.
func firstOpKey(calls []ToolCallRequest, workDir string) string {
	for _, c := range calls {
		if c.Name != "edit" && c.Name != "write" {
			continue
		}
		if path := extractPathFromArgs(c.Input, workDir); path != "" {
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
			sb.WriteString("- ")
			sb.WriteString(msg.Content)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// stableSystemPrompt returns the full system prompt shared by all sub-agents.
// Combines the main system prompt (rules, examples, language pack) with the sub-agent role suffix.
// Identical across every sub-agent call in the session → enables prefix cache hits.
func (r *SubAgentRunner) stableSystemPrompt(userLang string) string {
	prompts := promptset.Get()
	langPack := r.langPackEn
	if userLang == "中文" {
		langPack = r.langPackZh
	}
	base := prompts.System + "\n\n" + prompts.Examples
	if langPack != "" {
		base += "\n\n" + pickPrompt(userLang == "中文", "# Language Pack\n", "# 语言包\n") + langPack
	}
	return base + "\n\n" + prompts.SubAgent
}

// buildVolatilePrompt assembles the per-call variable content (goal, context, constraints).
// This is appended as the last user message, after stable system + agent-specific prompts.
func (r *SubAgentRunner) buildVolatilePrompt(input Handoff) string {
	zh := zhFromLang(input.UserLanguage)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Goal", "## 目标"), input.Goal))
	if input.Context != "" {
		sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Context", "## 上下文"), input.Context))
	}
	if len(input.Constraints) > 0 {
		sb.WriteString(fmt.Sprintf("%s\n- %s\n\n", pickPrompt(zh, "## Constraints", "## 约束"), strings.Join(input.Constraints, "\n- ")))
	}
	return sb.String()
}

// buildVariablePrompt assembles the per-call variable content (goal, context, constraints, extra).
// This is appended as the first user message, after the stable system message.
func (r *SubAgentRunner) buildVariablePrompt(input Handoff, extraPrompt string) string {
	zh := zhFromLang(input.UserLanguage)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Goal", "## 目标"), input.Goal))
	if input.Context != "" {
		sb.WriteString(fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Context", "## 上下文"), input.Context))
	}
	if len(input.Constraints) > 0 {
		sb.WriteString(fmt.Sprintf("%s\n- %s\n\n", pickPrompt(zh, "## Constraints", "## 约束"), strings.Join(input.Constraints, "\n- ")))
	}
	if extraPrompt != "" {
		sb.WriteString(extraPrompt + "\n\n")
	}
	return sb.String()
}

// filterTools returns a tool spec list filtered to only the allowed tools.
// If allowList is empty, all tools are allowed.
// userLang controls the language of the handoff tool description ("中文" = Chinese).
func (r *SubAgentRunner) filterTools(allowList []string, userLang string) []ModelTool {
	all := r.tools.Specs()
	// Always include the handoff tool
	result := []ModelTool{handoffToolSpec(zhFromLang(userLang))}

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
func (r *SubAgentRunner) executeSubHandoff(ctx context.Context, call ToolCallRequest, depth int, userLang string) ToolResult {
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
		Agent:        AgentID(params.Agent),
		Goal:         params.Goal,
		Context:      params.Context,
		Tools:        params.Tools,
		Constraints:  params.Constraints,
		Depth:        depth,
		UserLanguage: userLang,
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

	status := "ok"
	if result.BlockedBy == "cancelled" {
		status = "cancelled"
	}
	digest := formatHandoffResult(result, zhFromLang(userLang))
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   HandoffToolName,
		Status:     status,
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

	// Keep fresh: last 20 turns
	keepTurns := 20
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
		} else if rest[idx].Role == "tool" {
			result = append(result, rest[idx])
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
