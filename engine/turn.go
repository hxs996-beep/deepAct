package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	dlog "github.com/deepact/deepact/internal/log"
)

var turnLog = dlog.New("[turn] ")

type TurnResult struct {
	Done         bool
	Blocked      bool
	BlockedBy    string
	Questions    []string
	FinishReason string
	LastOp       string // "toolName:path" for loop detection, empty if irrelevant
}

func (e *Engine) executeTurn(ctx context.Context) (TurnResult, error) {
	if e.state == nil {
		return TurnResult{}, fmt.Errorf("state is nil")
	}

	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "thinking", Name: "deepact", Detail: "analyzing..."})
	}

	if e.compressor != nil && e.context != nil {
		msgs := e.context.Build(e.state, e.history, nil)
		tokens := e.context.EstimateTokens(msgs)
		layer, should := e.compressor.ShouldCompress(tokens, e.config.MaxContextTokens)
		if should {
			compacted, err := e.compressor.Compress(layer, e.state, e.history)
			if err == nil {
				e.history = compacted
			}
		}
	}

	messages := e.context.Build(e.state, e.history, nil)

	// Append pinned messages (skill activations, etc.) at the very end
	// for highest recency attention. Clear after first use so subsequent
	// turns within the same Run() call don't repeat them.
	for _, pm := range e.pendingPinnedMessages {
		messages = append(messages, ModelMessage{Role: "user", Content: pm})
	}
	e.pendingPinnedMessages = nil

	// Route model selection: use flash for low-risk / read-only turns.
	modelName := e.selectModel()

	req := ModelRequest{
		Model:     modelName,
		Messages:  messages,
		Tools:     e.toolSpecsWithHandoff(),
		MaxTokens: 8192,
	}
	stream, err := e.model.Stream(ctx, req)
	if err != nil {
		turnLog.Printf("stream model err: %v", err)
		e.state.ConsecutiveFailures++
		// Graceful degradation: don't crash the session on transient API errors.
		// The caller (Run) will see Blocked=true and return to the user.
		return TurnResult{
			Blocked:      true,
			BlockedBy:    "model_error",
			Questions:    []string{fmt.Sprintf("LLM API error: %v. Please check your connection and API key, then try again.", err)},
			FinishReason: "model_error",
		}, nil
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []ModelToolCall
	var finish string
	var lastUsage *ModelUsage
	for chunk := range stream {
		if chunk.Err != nil {
			turnLog.Printf("stream chunk err: %v", chunk.Err)
			e.state.ConsecutiveFailures++
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "stream_error",
				Questions:    []string{fmt.Sprintf("Stream error: %v. The connection was interrupted. Please try again.", chunk.Err)},
				FinishReason: "stream_error",
			}, nil
		}
		if chunk.Delta != "" {
			contentBuilder.WriteString(chunk.Delta)
		}
		if chunk.ReasoningDelta != "" {
			reasoningBuilder.WriteString(chunk.ReasoningDelta)
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "reasoning_delta", Detail: chunk.ReasoningDelta})
			}
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
	}

	if lastUsage != nil && e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "usage", Usage: lastUsage, ModelName: modelName})
	}
	// Accumulate usage across turns for efficiency eval
	if lastUsage != nil {
		e.runUsageAccum.PromptTokens += lastUsage.PromptTokens
		e.runUsageAccum.CompletionTokens += lastUsage.CompletionTokens
		e.runUsageAccum.TotalTokens += lastUsage.TotalTokens
		e.runUsageAccum.CacheHitTokens += lastUsage.CacheHitTokens
		e.runUsageAccum.CacheMissTokens += lastUsage.CacheMissTokens
	}

	content := contentBuilder.String()
	reasoning := reasoningBuilder.String()

	// Layer 1: If no valid structured tool_calls, try parsing DSML into tool calls
	if !hasValidToolCalls(toolCalls) && hasDSMLToolCalls(content) {
		cleaned, dsmlCalls, ok := parseDSMLToolCalls(content)
		if ok {
			content = cleaned
			toolCalls = dsmlCalls
		}
	}
	if !hasValidToolCalls(toolCalls) && hasDSMLToolCalls(reasoning) {
		_, dsmlCalls, ok := parseDSMLToolCalls(reasoning)
		if ok {
			toolCalls = dsmlCalls
		}
	}

	// Layer 2: Unconditionally strip any remaining DSML tokens from content.
	// Even if structured tool_calls exist, DSML must never reach the user.
	content = stripDSMLTokens(content)

	// Layer 3: When tool calls exist, strip intermediate thinking text from content.
	// The model sometimes outputs intent text ("Let me...", "让我...") alongside
	// DSML tool calls. This text is noise — tool results provide execution context.
	if hasValidToolCalls(toolCalls) && isIntermediateText(content) {
		content = ""
	}

	assistant := Message{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		Timestamp:        time.Now(),
	}
	// Extract explicit memory markers from model output (both content and reasoning)
	// Dedup: skip markers already in MemoryMarkers to prevent the same finding
	// being repeated 20+ times across turns.
	if markers := extractRememberMarkers(content); len(markers) > 0 {
		e.state.MemoryMarkers = appendUniqMarkers(e.state.MemoryMarkers, markers...)
	}
	if markers := extractRememberMarkers(reasoning); len(markers) > 0 {
		e.state.MemoryMarkers = appendUniqMarkers(e.state.MemoryMarkers, markers...)
	}

	if !hasValidToolCalls(toolCalls) {
		if assistant.Content == "" && assistant.ReasoningContent == "" {
			turnLog.Printf("skipping empty assistant message (no content, no reasoning, no tool_calls)")
			return TurnResult{Done: true, FinishReason: finish}, nil
		}
		e.history = append(e.history, assistant)
		if finish == "length" {
			e.history = append(e.history, Message{Role: "user", Content: "继续", Timestamp: time.Now()})
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		return TurnResult{Done: true, FinishReason: finish}, nil
	}

	assistant.ToolCalls = make([]MessageToolCall, 0, len(toolCalls))
	calls := make([]ToolCallRequest, 0, len(toolCalls))
	for _, call := range toolCalls {
		if call.Function.Name == "" {
			continue
		}
		assistant.ToolCalls = append(assistant.ToolCalls, MessageToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
		args := json.RawMessage(call.Function.Arguments)
		calls = append(calls, ToolCallRequest{ID: call.ID, Name: call.Function.Name, Input: args})
	}
	if len(calls) == 0 {
		e.history = append(e.history, assistant)
		return TurnResult{Done: true, FinishReason: finish}, nil
	}

	// Edit plan guard: before executing any edit/write calls for the first time
	// in this Run(), block and present the agent's understanding + proposed changes
	// to the user for approval.
	if e.pendingEditPlan == nil && !e.state.PlanConfirmed {
		var editCalls []ToolCallRequest
		for _, call := range calls {
			if call.Name == "edit" || call.Name == "write" {
				editCalls = append(editCalls, call)
			}
		}
		if len(editCalls) > 0 {
			// Merge reasoning from both content and thinking (reasoning_content).
			// When thinking mode is on, the actual analysis lives in ReasoningContent,
			// and Content is stripped as intermediate text. We need BOTH to give
			// the user a meaningful explanation of WHY changes are proposed.
			mergedReasoning := assistant.Content
			if assistant.ReasoningContent != "" {
				if mergedReasoning != "" {
					mergedReasoning = assistant.ReasoningContent + "\n" + mergedReasoning
				} else {
					mergedReasoning = assistant.ReasoningContent
				}
			}
			plan := &PendingEditPlan{
				Reasoning: mergedReasoning,
				Calls:     calls, // store ALL calls (read, edit, write, bash, handoff, etc.)
				State:     cloneTaskState(e.state),
			}
			for _, c := range editCalls {
				action := buildEditAction(c)
				plan.Edits = append(plan.Edits, action)
			}
			e.pendingEditPlan = plan

			// Build a rich plan summary for the user
			zh := msgIsChinese(e.history[0].Content) // check first user message language
			planSummary := formatEditPlanSummary(plan, zh)

			// Add assistant (with tool_calls) first, then tool messages to close IDs.
			e.history = append(e.history, assistant)
			for _, c := range calls {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Blocked: " + planSummary,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{
				Blocked:    true,
				BlockedBy:  GuardAskUser,
				Questions:  []string{planSummary},
				FinishReason: finish,
			}, nil
		}
	}

	for _, call := range calls {
		// Check loop guard: same (tool, path) repeated → block to prevent cycles
		if e.guards.loop != nil {
			loopAction := e.guards.loop.Check(call)
			if loopAction.Type != GuardAllow {
				e.history = append(e.history, assistant)
				for _, c := range calls {
					e.history = append(e.history, Message{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    "Blocked: " + loopAction.Message,
						Timestamp:  time.Now(),
					})
				}
				return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
			}
		}

		scopeAction := e.guards.scope.CheckTool(call, e.state)
		if scopeAction.Type != GuardAllow {
			// If blocked due to dangerous bash command, store it as pending user confirmation
			if call.Name == "bash" {
				e.state.PendingDangerousCmd = e.guards.scope.DangerousPending()
			}
			// Add assistant (with tool_calls) first, then tool messages to close IDs.
			// DeepSeek requires: assistant(tool_calls) must be followed by tool messages.
			e.history = append(e.history, assistant)
			for _, c := range calls {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Blocked: " + scopeAction.Message,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{Blocked: true, BlockedBy: scopeAction.Type, Questions: []string{scopeAction.Message}}, nil
		}
	}

	// Check for activate_skill tool call — intercept and auto-activate if in skill chain
	for _, call := range calls {
		if call.Name == ActivateSkillToolName {
			var params ActivateSkillParams
			if err := json.Unmarshal(call.Input, &params); err != nil {
				continue
			}
			if params.SkillName == "" {
				continue
			}

			// Check if this skill is in the auto-activation chain (NextSkills of lastActivatedSkill)
			isChainTransition := false
			if e.lastActivatedSkill != "" {
				if lastSkill := e.skills.Get(e.lastActivatedSkill); lastSkill != nil {
					for _, ns := range lastSkill.NextSkills {
						if strings.EqualFold(ns, params.SkillName) {
							isChainTransition = true
							break
						}
					}
				}
			}

			if isChainTransition {
				// Auto-activate: skill chain transition, no user confirmation needed
				s := e.skills.Get(params.SkillName)
				if s != nil {
					prevSkill := e.lastActivatedSkill
					e.activatedSkills[s.Name] = true
					e.lastActivatedSkill = s.Name
					e.state.ActiveSkillName = s.Name
					e.state.ActiveSkillContent = s.Content
					skillMsg := fmt.Sprintf(
						"[SKILL ACTIVATED: %s] (auto, chain: %s → %s)\n\n%s",
						s.Name, prevSkill, s.Name, s.Content,
					)
					// Store as pending pinned message to inject at end of this turn
					e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
					e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content)
					if e.config.OnProgress != nil {
						e.config.OnProgress(ProgressEvent{
							Type:   "skill_activated",
							Name:   s.Name,
							Detail: s.Description + " (auto chain)",
						})
					}
					// Record in history so the chain transition is visible
					e.history = append(e.history, Message{
						Role:      "tool",
						ToolCallID: call.ID,
						Content:   fmt.Sprintf("✅ Auto-activated skill `%s` (chain: %s → %s)", s.Name, e.lastActivatedSkill, s.Name),
						Timestamp: time.Now(),
					})
				}
				continue
			}

			// Not in chain — require user confirmation
			e.state.PendingActivateSkill = params.SkillName
			e.history = append(e.history, assistant)
			for _, c := range calls {
				reasoning := params.Reasoning
				if reasoning == "" {
					reasoning = fmt.Sprintf("建议激活 skill `%s`", params.SkillName)
				}
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Suggestion: " + reasoning,
					Timestamp:  time.Now(),
				})
			}
			zh := msgIsChinese(e.history[0].Content)
			var question string
			if zh {
				question = fmt.Sprintf("💡 模型建议激活 skill **`%s`**。%s\n\n是否确认？", params.SkillName, params.Reasoning)
			} else {
				question = fmt.Sprintf("💡 The model suggests activating skill **`%s`**. %s\n\nConfirm?", params.SkillName, params.Reasoning)
			}
			return TurnResult{Blocked: true, BlockedBy: "activate_skill", Questions: []string{question}}, nil
		}
	}

	e.history = append(e.history, assistant)

	// Separate handoff calls from regular tool calls
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else {
			regularCalls = append(regularCalls, call)
		}
	}

	// Execute handoff calls (sub-agents)
	for _, call := range handoffCalls {
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: "handoff", Detail: summarizeArgs(call.Input)})
		}
		result := e.executeHandoff(ctx, call)
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: "handoff", Detail: briefDigest(result.Digest)})
		}
		toolMessage := Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()}
		e.history = append(e.history, toolMessage)
	}

	// Execute regular tool calls.
	// Split into read-only (batch for speed) and destructive (sequential for progressive UX).
	if len(regularCalls) > 0 {
		var readOnlyCalls, destructiveCalls []ToolCallRequest
		for _, call := range regularCalls {
			if call.Name == "edit" || call.Name == "write" {
				destructiveCalls = append(destructiveCalls, call)
			} else {
				readOnlyCalls = append(readOnlyCalls, call)
			}
		}

		// Batch execute read-only tools (grep, glob, read, lsp, bash — no diffs, fast)
		if len(readOnlyCalls) > 0 {
			for _, call := range readOnlyCalls {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Input)})
				}
			}
			roResults := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, readOnlyCalls)
			for _, result := range roResults {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
				}
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
		}

		// Sequential execute destructive tools (edit/write — show each diff progressively)
		for _, call := range destructiveCalls {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Input)})
			}
			results := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, []ToolCallRequest{call})
			if len(results) > 0 {
				result := results[0]
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
				}
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
		}

		allCalls := append(readOnlyCalls, destructiveCalls...)
		allResults := make([]ToolResult, 0)
		for i := len(e.history) - len(regularCalls); i < len(e.history); i++ {
			if i >= 0 && e.history[i].Role == "tool" {
				allResults = append(allResults, ToolResult{ToolCallID: e.history[i].ToolCallID, Digest: e.history[i].Content})
			}
		}
		e.updateTaskStateFromTools(allCalls, allResults)
		e.runToolCallCount += len(regularCalls)
	}

	result := TurnResult{Done: false, FinishReason: finish}
	// Record the first operation for loop detection.
	// For destructive tools (edit/write), include content hash so different edits
	// on the same file are recognized as distinct operations.
	// For read operations, use only tool:path — content hash is unreliable (read
	// may lack content-bearing fields or vary in offset/limit/symbol), and repeatedly
	// reading the same file is itself a sign of a stuck agent.
	for _, c := range regularCalls {
		path := extractPathFromArgs(c.Input)
		if path == "" {
			continue
		}
		if c.Name == "read" {
			result.LastOp = c.Name + ":" + path
		} else {
			result.LastOp = c.Name + ":" + path + "#" + contentSignature(c.Input)
		}
		break
	}
	return result, nil
}

// selectModel always returns the configured primary model.
// Model routing is disabled to ensure a stable model field across turns,
// which is required for DeepSeek's per-model prefix cache to work.
func (e *Engine) selectModel() string {
	return e.config.ModelName
}

// toolSpecsWithHandoff returns the tool specs list with the handoff_to_agent and activate_skill tools appended.
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, handoffToolSpec())
	specs = append(specs, activateSkillToolSpec())
	return specs
}

// executeHandoff processes a handoff_to_agent tool call from the main agent loop.
func (e *Engine) executeHandoff(ctx context.Context, call ToolCallRequest) ToolResult {
	if e.agents == nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     "no agent registry configured",
		}
	}

	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := e.agents.Get(AgentID(params.Agent))
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
		Depth:       0, // main engine starts at depth 0
	}

	// Inject matched skill content into sub-agent context
	if e.matchedSkillsContent != "" {
		if handoff.Context != "" {
			handoff.Context = e.matchedSkillsContent + "\n\n" + handoff.Context
		} else {
			handoff.Context = e.matchedSkillsContent
		}
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

	if result.Usage != nil && e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "usage", Usage: result.Usage})
	}

	digest := formatHandoffResult(result)
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   HandoffToolName,
		Status:     "ok",
		Digest:     digest,
	}
}

func summarizeArgs(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if cmd, ok := m["command"].(string); ok {
		return cmd
	}

	// LSP tool: show operation + query or file position.
	if op, ok := m["operation"].(string); ok {
		if query, ok := m["query"].(string); ok {
			return fmt.Sprintf("%s %q", op, query)
		}
		if fp, ok := m["file_path"].(string); ok {
			line, _ := m["line"].(float64)
			chr, _ := m["character"].(float64)
			if line > 0 {
				return fmt.Sprintf("%s %s:%d:%d", op, shortPath(fp), int(line), int(chr))
			}
			return fmt.Sprintf("%s %s", op, shortPath(fp))
		}
		return op
	}

	// Extract path first — all file-oriented tools have it.
	path := ""
	if p, ok := m["path"].(string); ok {
		path = p
	}
	if path == "" {
		if pattern, ok := m["pattern"].(string); ok {
			return pattern
		}
		return ""
	}

	// Edit tool: show change preview from old_string/new_string.
	if oldStr, ok := m["old_string"].(string); ok {
		newStr, _ := m["new_string"].(string)
		oldLen := len(oldStr)
		newLen := len(newStr)
		if oldLen > 0 && newLen > 0 && oldLen != newLen {
			return fmt.Sprintf("%s — replace %d → %d chars", path, oldLen, newLen)
		}
		if oldLen > 0 {
			return fmt.Sprintf("%s — replace %d chars", path, oldLen)
		}
		return path
	}

	// Write tool: show content length preview.
	if content, ok := m["content"].(string); ok {
		if len(content) > 0 {
			return fmt.Sprintf("%s — write %d chars", path, len(content))
		}
		return path
	}

	return path
}

// shortPath shortens a file path for display — shows last two components.
func shortPath(p string) string {
	if p == "" {
		return p
	}
	base := filepath.Base(p)
	dir := filepath.Dir(p)
	parent := filepath.Base(dir)
	if parent != "" && parent != "." {
		return parent + "/" + base
	}
	return base
}

func briefDigest(digest string) string {
	if len(digest) == 0 {
		return ""
	}
	lines := strings.SplitN(digest, "\n", 2)
	first := lines[0]
	if len(first) > 80 {
		first = first[:80] + "..."
	}
	lineCount := strings.Count(digest, "\n")
	if lineCount > 1 {
		return fmt.Sprintf("%s (%d lines)", first, lineCount)
	}
	return first
}

func (e *Engine) updateTaskStateFromTools(calls []ToolCallRequest, results []ToolResult) {
	if e.state == nil {
		return
	}
	for i, call := range calls {
		path := extractPathFromArgs(call.Input)
		if path == "" {
			continue
		}
		switch call.Name {
		case "edit", "write":
			if !containsString(e.state.ModifiedFiles, path) {
				e.state.ModifiedFiles = append(e.state.ModifiedFiles, path)
			}
			e.state.EditScopeFiles = len(e.state.ModifiedFiles)
			addToWorkingSet(e.state, path, "modified")
		case "read":
			addToWorkingSet(e.state, path, "read")
		case "grep", "glob":
			if i < len(results) && results[i].Status == "ok" {
				addToWorkingSet(e.state, path, "searched")
			}
		}
	}
}

func (e *Engine) updateGoalFromFirstMessage(userMsg string) {
	if e.state != nil && e.state.Goal == "" {
		e.state.Goal = userMsg
	}
}

// extractPathFromArgs extracts a file path from tool call arguments.
func extractPathFromArgs(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok {
		return p
	}
	if p, ok := m["file_path"].(string); ok {
		return p
	}
	if p, ok := m["filePath"].(string); ok {
		return p
	}
	return ""
}

// contentSignature returns a short hash of the tool call's input arguments,
// used to distinguish operations with different content on the same file.
// The hash is derived from content-bearing fields (pattern, old_string, content)
// rather than the full JSON, so trivial changes like path or filename don't
// collapse the signature.
func contentSignature(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	// Collect content-bearing fields only
	var parts []string
	for _, key := range []string{"pattern", "old_string", "new_string", "content", "command", "symbol"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				parts = append(parts, key+"="+s)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	combined := strings.Join(parts, "&")
	h := sha256.Sum256([]byte(combined))
	return fmt.Sprintf("%x", h[:4])
}

// appendUniqMarkers appends markers that aren't already in the list,
// preventing the same finding from being accumulated 20+ times.
func appendUniqMarkers(existing []string, markers ...string) []string {
	for _, m := range markers {
		found := false
		for _, e := range existing {
			if e == m {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, m)
		}
	}
	return existing
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// truncateStr truncates a string to the given max length, appending "..." if truncated.
func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func addToWorkingSet(state *TaskState, path string, notes string) {
	for i, f := range state.WorkingSet.Files {
		if f.Path == path {
			state.WorkingSet.Files[i].Notes = notes
			return
		}
	}
	state.WorkingSet.Files = append(state.WorkingSet.Files, FileRef{Path: path, Notes: notes})
}

// buildEditAction extracts a human-readable description of a proposed edit from a tool call.
func buildEditAction(call ToolCallRequest) PendingEditAction {
	var m map[string]interface{}
	if err := json.Unmarshal(call.Input, &m); err != nil {
		return PendingEditAction{Tool: call.Name, Path: "?", Summary: "? (parse error)"}
	}

	path, _ := m["path"].(string)
	if path == "" {
		if p, ok := m["file_path"].(string); ok {
			path = p
		}
	}

	action := PendingEditAction{Tool: call.Name, Path: path}

	if call.Name == "edit" {
		oldStr, _ := m["old_string"].(string)
		newStr, _ := m["new_string"].(string)
		action.OldText = oldStr
		action.NewText = newStr
		if oldStr != "" && newStr != "" {
			action.Summary = "修改文件内容"
		} else if pattern, ok := m["pattern"].(string); ok {
			replacement, _ := m["replacement"].(string)
			action.Summary = fmt.Sprintf("替换匹配 %q → %q", truncateStr(pattern, 50), truncateStr(replacement, 50))
		} else {
			action.Summary = "编辑文件"
		}
	} else {
		// write tool
		if content, ok := m["content"].(string); ok {
			action.NewText = content
			action.Summary = fmt.Sprintf("写入 %d 个字符", len(content))
		} else {
			action.Summary = "写入文件"
		}
	}
	return action
}

// formatEditPlanSummary builds a user-facing summary of the agent's proposed changes.
// The summary always shows the reasoning (WHY) first, then a file list, then asks
// the user to confirm. Reasoning is never truncated — the user needs the full
// explanation to make an informed decision.
func formatEditPlanSummary(plan *PendingEditPlan, zh bool) string {
	var sb strings.Builder

	// Step 1: Show the reasoning — WHY these changes are proposed.
	reasoning := plan.Reasoning
	if reasoning == "" {
		if zh {
			sb.WriteString("（AI 未提供修改原因）\n")
		} else {
			sb.WriteString("(No reasoning provided)\n")
		}
	} else {
		sb.WriteString(reasoning)
		sb.WriteString("\n")
	}

	// Step 2: Show the file list — WHAT will be changed.
	if zh {
		sb.WriteString("\n📋 计划修改以下文件：\n")
	} else {
		sb.WriteString("\n📋 Planned changes:\n")
	}
	for _, edit := range plan.Edits {
		sb.WriteString(fmt.Sprintf("  • `%s` — %s\n", edit.Path, edit.Summary))
	}

	// Step 3: Ask for confirmation.
	if zh {
		sb.WriteString("\n确认执行以上修改？")
	} else {
		sb.WriteString("\nConfirm these changes?")
	}
	return sb.String()
}

// cloneTaskState creates a shallow-but-safe copy of TaskState for snapshotting.
func cloneTaskState(s *TaskState) *TaskState {
	if s == nil {
		return nil
	}
	data, _ := json.Marshal(s)
	var clone TaskState
	json.Unmarshal(data, &clone)
	return &clone
}
