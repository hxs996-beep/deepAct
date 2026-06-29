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
	// LastOpError is true when the operation recorded in LastOp returned an
	// error status this turn. Used by ErrorLoopState to detect repeated
	// failing operations that defeat the content-hash-based loop guards.
	LastOpError bool
}

func (e *Engine) executeTurn(ctx context.Context) (TurnResult, error) {
	if e.state == nil {
		return TurnResult{}, fmt.Errorf("state is nil")
	}

	turnStart := time.Now()
	defer func() {
		turnLog.Printf("turn %d total=%s", e.state.TurnNumber, time.Since(turnStart))
	}()

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

	ctxBuildStart := time.Now()
	messages := e.context.Build(e.state, e.history, nil)
	ctxBuildDur := time.Since(ctxBuildStart)

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
		MaxTokens: e.maxOutputTokens(),
	}
	turnLog.Printf("turn %d start: model=%s msgs=%d ctx_build=%s", e.state.TurnNumber, modelName, len(messages), ctxBuildDur)
	streamStart := time.Now()
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
		if chunk.RetryProgress != "" {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "retry", Detail: chunk.RetryProgress})
			}
			continue
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
	streamDur := time.Since(streamStart)
	turnLog.Printf("turn %d model stream done: dur=%s finish=%s tool_calls=%d usage prompt=%d completion=%d cache_hit=%d cache_miss=%d",
		e.state.TurnNumber, streamDur, finish, len(toolCalls),
		usageOrZero(lastUsage, func(u *ModelUsage) int { return u.PromptTokens }),
		usageOrZero(lastUsage, func(u *ModelUsage) int { return u.CompletionTokens }),
		usageOrZero(lastUsage, func(u *ModelUsage) int { return u.CacheHitTokens }),
		usageOrZero(lastUsage, func(u *ModelUsage) int { return u.CacheMissTokens }))

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

	// Merge reasoning from both content and thinking (reasoning_content).
	// Used by both conclusion verification and the edit plan guard below.
	mergedReasoning := assistant.Content
	if assistant.ReasoningContent != "" {
		if mergedReasoning != "" {
			mergedReasoning = assistant.ReasoningContent + "\n" + mergedReasoning
		} else {
			mergedReasoning = assistant.ReasoningContent
		}
	}

	// Conclusion verification gate: before presenting any edit plan, run an
	// independent contrarian sub-agent to verify the agent's reasoning is
	// actually supported by code evidence. Only triggers on the first edit
	// proposal in each Run() and only when VerifyConclusions is enabled.
	if e.config.VerifyConclusions && !e.verificationPassed && !e.state.PlanConfirmed {
		var hasEditCall bool
		for _, call := range calls {
			if call.Name == "edit" || call.Name == "write" {
				hasEditCall = true
				break
			}
		}
		if hasEditCall && mergedReasoning != "" {
			vr := e.runConclusionVerification(ctx, mergedReasoning)
			if vr != nil && vr.Confidence < e.confidenceThreshold() {
				// Block: conclusions lack sufficient code evidence.
				// Present verifier findings + questions to the user instead of edits.
				e.verificationPassed = false

				verdict := "不支持"
				verdictIcon := "❌"
				if vr.Supported {
					verdict = "部分支持"
					verdictIcon = "⚠️"
				}

				zh := e.isChinese
				var msg strings.Builder
				if zh {
					msg.WriteString(fmt.Sprintf("## 🔍 结论验证未通过\n\n"))
					msg.WriteString(fmt.Sprintf("系统检测到 AI 的分析结论缺乏充分的代码依据（置信度: %d/100, 结论%s）。\n\n", vr.Confidence, verdict))
					msg.WriteString(fmt.Sprintf("%s **结论验证报告**\n\n", verdictIcon))
					if len(vr.Issues) > 0 {
						msg.WriteString("**未找到代码依据的结论：**\n")
						for _, issue := range vr.Issues {
							msg.WriteString(fmt.Sprintf("- %s\n", issue))
						}
						msg.WriteString("\n")
					}
					if len(vr.Questions) > 0 {
						msg.WriteString("**需要你澄清的问题：**\n")
						for _, q := range vr.Questions {
							msg.WriteString(fmt.Sprintf("- %s\n", q))
						}
						msg.WriteString("\n")
					}
					msg.WriteString("请回答以上问题或提供更多信息，AI 将基于更完整的信息重新分析。")
				} else {
					msg.WriteString(fmt.Sprintf("## 🔍 Conclusion Verification Failed\n\n"))
					msg.WriteString(fmt.Sprintf("The AI's conclusions lack sufficient code evidence (confidence: %d/100, conclusion %s).\n\n", vr.Confidence, verdict))
					msg.WriteString(fmt.Sprintf("%s **Verification Report**\n\n", verdictIcon))
					if len(vr.Issues) > 0 {
						msg.WriteString("**Unsupported claims:**\n")
						for _, issue := range vr.Issues {
							msg.WriteString(fmt.Sprintf("- %s\n", issue))
						}
						msg.WriteString("\n")
					}
					if len(vr.Questions) > 0 {
						msg.WriteString("**Clarifying questions:**\n")
						for _, q := range vr.Questions {
							msg.WriteString(fmt.Sprintf("- %s\n", q))
						}
						msg.WriteString("\n")
					}
					msg.WriteString("Please answer the questions or provide more context. The AI will re-analyze with better information.")
				}

				// Add assistant message then tool messages to close IDs.
				e.history = append(e.history, assistant)
				for _, c := range calls {
					e.history = append(e.history, Message{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    "Blocked: conclusions insufficiently supported by code evidence.",
						Timestamp:  time.Now(),
					})
				}
				return TurnResult{
					Blocked:      true,
					BlockedBy:    "verification_failed",
					Questions:    []string{msg.String()},
					FinishReason: finish,
				}, nil
			}
			// Verification passed (or degraded to pass on error).
			e.verificationPassed = true
		}
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
			zh := e.isChinese
			planSummary := formatEditPlanSummary(plan, zh, e.config.WorkDir)

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
		// Check loop guard: same (tool, path) repeated → block to prevent cycles.
		if e.guards.loop != nil {
			var loopAction GuardAction
			if call.Name == "read_multi" {
				// read_multi bypasses the single-call key (extractToolKey returns ""
				// for unknown tools); check each sub-target as a synthetic read so
				// repeated fan-out reads of the same (path, scope) are still caught.
				for _, tgt := range parseReadMultiTargets(call.Input) {
					synthInput, _ := json.Marshal(map[string]interface{}{
						"path": tgt.Path, "symbol": tgt.Symbol,
						"offset": tgt.Offset, "limit": tgt.Limit,
					})
					synth := ToolCallRequest{ID: call.ID, Name: "read", Input: synthInput}
					a := e.guards.loop.Check(synth)
					if a.Type != GuardAllow {
						loopAction = GuardAction{
							Type:    a.Type,
							Message: fmt.Sprintf("read_multi target %s: %s", tgt.Path, a.Message),
						}
						break
					}
				}
			} else {
				loopAction = e.guards.loop.Check(call)
			}
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

	// Check for activate_skill tool call — intercept and auto-activate if in skill chain.
	// Collect tool messages in a separate slice and add them AFTER the assistant
	// message to satisfy DeepSeek API requirement: assistant(tool_calls) must be
	// followed by tool messages responding to each tool_call_id.
	var pendingActivateMsgs []Message
	for _, call := range calls {
		if call.Name == ActivateSkillToolName {
			var params ActivateSkillParams
			if err := json.Unmarshal(call.Input, &params); err != nil {
				continue
			}
			if params.SkillName == "" {
				continue
			}

			// Directly activate the skill — no user confirmation needed
			s := e.skills.Get(params.SkillName)
			if s == nil {
				continue
			}
			prevSkill := e.lastActivatedSkill
			e.activatedSkills[s.Name] = true
			e.lastActivatedSkill = s.Name
			e.state.ActiveSkillName = s.Name
			e.state.ActiveSkillContent = s.Content
			chainInfo := ""
			if prevSkill != "" {
				chainInfo = fmt.Sprintf(" (chain: %s → %s)", prevSkill, s.Name)
			}
			skillMsg := fmt.Sprintf(
				"[SKILL ACTIVATED: %s]%s\n\n%s",
				s.Name, chainInfo, s.Content,
			)
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, skillMsg)
			e.matchedSkillsContent = fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content)
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{
					Type:   "skill_activated",
					Name:   s.Name,
					Detail: s.Description + chainInfo,
				})
			}
			pendingActivateMsgs = append(pendingActivateMsgs, Message{
				Role:      "tool",
				ToolCallID: call.ID,
				Content:   fmt.Sprintf("✅ Activated skill `%s`%s", s.Name, chainInfo),
				Timestamp: time.Now(),
			})
		}
	}

	e.history = append(e.history, assistant)

	// Add activate_skill tool messages AFTER the assistant message, so the
	// DeepSeek API sees the correct order: assistant(tool_calls) → tool.
	for _, msg := range pendingActivateMsgs {
		e.history = append(e.history, msg)
	}

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
			e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: "handoff", Detail: summarizeArgs("handoff", call.Input, e.config.WorkDir)})
		}
		result := e.executeHandoff(ctx, call)
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: "handoff", Detail: briefDigest(result.Digest)})
		}
		if result.Status != "cancelled" {
			toolMessage := Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()}
			e.history = append(e.history, toolMessage)
		}
	}

	// Execute regular tool calls.
	// Split into read-only (batch for speed) and destructive (sequential for progressive UX).
	// statusByID records each call's outcome status so the loop-detection block
	// below can tell whether the first op errored (feeds ErrorLoopState).
	statusByID := make(map[string]string, len(regularCalls))
	if len(regularCalls) > 0 {
		toolsStart := time.Now()
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
					e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, e.config.WorkDir)})
				}
			}
			roResults := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, readOnlyCalls)
			for _, result := range roResults {
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
				}
				statusByID[result.ToolCallID] = result.Status
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})
			}
		}

		// Sequential execute destructive tools (edit/write — show each diff progressively)
		for _, call := range destructiveCalls {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Name, call.Input, e.config.WorkDir)})
			}
			results := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, []ToolCallRequest{call})
			if len(results) > 0 {
				result := results[0]
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
				}
				statusByID[result.ToolCallID] = result.Status
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

		// Infer TDD phase from tool calls when TDD skill is active
		if e.state != nil && e.state.ActiveSkillName == "test-driven-development" {
			e.inferTDDPhase(allCalls, allResults)
		}
		turnLog.Printf("turn %d tools done: dur=%s calls=%d (ro=%d destructive=%d)",
			e.state.TurnNumber, time.Since(toolsStart), len(regularCalls), len(readOnlyCalls), len(destructiveCalls))
	}

	result := TurnResult{Done: false, FinishReason: finish}
	// Record the first operation for loop detection.
	// For destructive tools (edit/write), include content hash so different edits
	// on the same file are recognized as distinct operations.
	// For read operations, include a human-readable scope (symbol/offset/limit) so
	// reading different sections of the same file produces distinct LastOps and is
	// not counted as a loop. Repeated reads of the SAME scope are still caught.
	// Key form is aligned with LoopGuard's read key ("read:path::scope").
	for _, c := range regularCalls {
		path := extractPathFromArgs(c.Input, e.config.WorkDir)
		if path == "" {
			continue
		}
		if c.Name == "read" {
			result.LastOp = c.Name + ":" + path + "::" + extractReadScope(c.Input)
		} else {
			result.LastOp = c.Name + ":" + path + "#" + contentSignature(c.Input)
		}
		// Record whether this op errored so ErrorLoopState can catch repeated
		// failures on the same (tool, path) that defeat content-hash guards.
		result.LastOpError = statusByID[c.ID] == "error"
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

// maxOutputTokens returns the per-turn completion cap, falling back to the
// default when the config doesn't override it.
func (e *Engine) maxOutputTokens() int {
	if e.config.MaxOutputTokens > 0 {
		return e.config.MaxOutputTokens
	}
	return DefaultMaxOutputTokens
}

// usageOrZero safely extracts an int field from a possibly-nil *ModelUsage,
// for timing/log lines that report token counts.
func usageOrZero(u *ModelUsage, get func(*ModelUsage) int) int {
	if u == nil {
		return 0
	}
	return get(u)
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

	userLang := ""
	if e.isChinese {
		userLang = "中文"
	}
	handoff := Handoff{
		Agent:         AgentID(params.Agent),
		Goal:          params.Goal,
		Context:       params.Context,
		Tools:         params.Tools,
		Constraints:   params.Constraints,
		Depth:         0, // main engine starts at depth 0
		UserLanguage:  userLang,
	}

	// Inject matched skill content into sub-agent context
	if e.matchedSkillsContent != "" {
		if handoff.Context != "" {
			handoff.Context = e.matchedSkillsContent + "\n\n" + handoff.Context
		} else {
			handoff.Context = e.matchedSkillsContent
		}
	}

	// Inject main agent's working context (known files, findings, modifications)
	// as a starting point for the sub-agent. The sub-agent should re-examine these
	// from its own perspective to find blind spots the main agent missed.
	if e.state != nil {
		var agentCtx strings.Builder

		if len(e.state.WorkingSet.Files) > 0 {
			agentCtx.WriteString("\n## Main Agent Context (Review Starting Point)\n")
			agentCtx.WriteString("The main agent examined these files. Re-examine them from your own perspective:\n")
			for _, f := range e.state.WorkingSet.Files {
				agentCtx.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Notes))
			}
		}

		if len(e.state.MemoryMarkers) > 0 {
			agentCtx.WriteString("\nKey findings from the main agent (review for blind spots):\n")
			for _, m := range e.state.MemoryMarkers {
				agentCtx.WriteString(fmt.Sprintf("  ⚡ %s\n", m))
			}
		}

		if len(e.state.ModifiedFiles) > 0 {
			agentCtx.WriteString("\nFiles modified so far:\n")
			for _, f := range e.state.ModifiedFiles {
				agentCtx.WriteString(fmt.Sprintf("- %s\n", f))
			}
		}

		extra := agentCtx.String()
		if extra != "" {
			if handoff.Context != "" {
				handoff.Context = handoff.Context + extra
			} else {
				handoff.Context = extra
			}
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

	if result.Usage != nil {
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "usage", Usage: result.Usage})
		}
		e.accumulateUsage(result.Usage)
	}

	status := "ok"
	if result.BlockedBy == "cancelled" {
		status = "cancelled"
	}
	digest := formatHandoffResult(result, e.isChinese)
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   HandoffToolName,
		Status:     status,
		Digest:     digest,
	}
}

func summarizeArgs(toolName string, input json.RawMessage, cwd string) string {
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
				return fmt.Sprintf("%s %s:%d:%d", op, relPath(fp, cwd), int(line), int(chr))
			}
			return fmt.Sprintf("%s %s", op, relPath(fp, cwd))
		}
		return op
	}

	// Extract path first — all file-oriented tools have it.
	path := ""
	if p, ok := m["path"].(string); ok {
		path = p
	}

	// Skill/agent tools — show the human-relevant target, not an empty line.
	switch toolName {
	case "skill_install", "activate_skill":
		// skill_install uses "name"; activate_skill uses "skill_name".
		if n, ok := m["name"].(string); ok && n != "" {
			return "install skill: " + n
		}
		if n, ok := m["skill_name"].(string); ok && n != "" {
			return "activate skill: " + n
		}
	case "handoff_to_agent":
		agent, _ := m["agent"].(string)
		goal, _ := m["goal"].(string)
		if goal != "" && len(goal) > 60 {
			goal = goal[:60] + "..."
		}
		switch {
		case agent != "" && goal != "":
			return "→ " + agent + ": " + goal
		case agent != "":
			return "→ " + agent
		case goal != "":
			return goal
		}
	}

	// Grep/glob: show pattern first, path as short suffix.
	if toolName == "grep" || toolName == "glob" {
		if pattern, ok := m["pattern"].(string); ok {
			if path != "" {
				return fmt.Sprintf("%s in %s", pattern, relPath(path, cwd))
			}
			return pattern
		}
		if path != "" {
			return relPath(path, cwd)
		}
		return fallbackSummary(toolName, m)
	}

	if path == "" {
		if pattern, ok := m["pattern"].(string); ok && strings.TrimSpace(pattern) != "" {
			return pattern
		}
		if name, ok := m["name"].(string); ok && strings.TrimSpace(name) != "" {
			return name
		}
		return fallbackSummary(toolName, m)
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

	if path != "" {
		return relPath(path, cwd)
	}
	// Last resort: never return an empty summary — an empty Detail renders as
	// a bare icon with no context (e.g. "[*]  ✓"), which tells the user nothing.
	return fallbackSummary(toolName, m)
}

// fallbackSummary builds a non-empty one-line summary for tools whose argument
// shape isn't handled by the specialized branches above (MCP tools, custom
// tools, future built-ins). It picks the most informative string field and,
// if none exists, falls back to the tool name itself so the UI always shows
// something meaningful.
func fallbackSummary(toolName string, m map[string]interface{}) string {
	// Prefer fields that commonly carry the "what": query, pattern, command,
	// name, url, description — in that order.
	for _, key := range []string{"query", "pattern", "command", "url", "source_url", "description", "text", "value"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			s = strings.TrimSpace(s)
			if len(s) > 80 {
				s = s[:80] + "..."
			}
			return s
		}
	}
	// Any first non-empty string field beats nothing.
	for k, v := range m {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			s = strings.TrimSpace(s)
			if len(s) > 80 {
				s = s[:80] + "..."
			}
			return k + ": " + s
		}
	}
	// Truly nothing to show — at least name the tool.
	if toolName != "" {
		return toolName
	}
	return "—"
}

// relPath shortens a file path for display — shows relative path from cwd
// (project root). Falls back to last-two-components if the path isn't under cwd
// or if cwd is empty.
func relPath(p, cwd string) string {
	if p == "" {
		return p
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	// Fallback: show last two components.
	base := filepath.Base(p)
	dir := filepath.Dir(p)
	parent := filepath.Base(dir)
	if parent != "" && parent != "." {
		return parent + "/" + base
	}
	return base
}

// shortPath shortens a file path for display — shows last two components.
// Deprecated: use relPath(p, cwd) for project-root-relative display.
func shortPath(p string) string {
	return relPath(p, "")
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
		switch call.Name {
		case "read_multi":
			// read_multi has no top-level "path"; extractPathFromArgs returns "".
			// Parse the self-describing metadata in the result digest to record
			// each sub-target's (path, scope) into ReadHistory, and add each to
			// the working set as "read".
			if i < len(results) {
				for _, rec := range parseReadMultiDigestScopes(results[i].Digest) {
					addToWorkingSet(e.state, rec.Path, "read")
					e.state.ReadHistory = append(e.state.ReadHistory, rec)
				}
			}
			continue
		}
		path := extractPathFromArgs(call.Input, e.config.WorkDir)
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
			e.state.ReadHistory = append(e.state.ReadHistory, ReadRecord{
				Path:  path,
				Scope: extractReadScope(call.Input),
			})
		case "grep", "glob":
			if i < len(results) && results[i].Status == "ok" {
				addToWorkingSet(e.state, path, "searched")
			}
		}
	}
}

// parseReadMultiDigestScopes extracts per-target ReadRecords from a read_multi
// result's self-describing metadata comment:
//
//	<!-- read_multi targets: path1::scope1 | path2::scope2 | ... -->
//
// Returns nil if the metadata line is absent or malformed (best-effort: missing
// metadata just skips ReadHistory bookkeeping; the loop guard still backstops).
func parseReadMultiDigestScopes(digest string) []ReadRecord {
	lineEnd := strings.Index(digest, "\n")
	if lineEnd < 0 {
		lineEnd = len(digest)
	}
	firstLine := digest[:lineEnd]
	const marker = "<!-- read_multi targets:"
	start := strings.Index(firstLine, marker)
	if start < 0 {
		return nil
	}
	rest := firstLine[start+len(marker):]
	end := strings.Index(rest, "-->")
	if end < 0 {
		return nil
	}
	body := strings.TrimSpace(rest[:end])
	if body == "" {
		return nil
	}
	var recs []ReadRecord
	for _, part := range strings.Split(body, " | ") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "::", 2)
		if len(kv) != 2 {
			continue
		}
		recs = append(recs, ReadRecord{Path: kv[0], Scope: kv[1]})
	}
	return recs
}

func (e *Engine) updateGoalFromFirstMessage(userMsg string) {
	if e.state != nil && e.state.Goal == "" {
		e.state.Goal = userMsg
	}
}

// extractPathFromArgs extracts a file path from tool call arguments and
// normalizes it against workDir so the same physical file yields one path
// regardless of how the model addressed it (relative/absolute/file_path).
// Used for loop-detection keys and file-set tracking.
func extractPathFromArgs(input json.RawMessage, workDir string) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok {
		return normalizePath(p, workDir)
	}
	if p, ok := m["file_path"].(string); ok {
		return normalizePath(p, workDir)
	}
	if p, ok := m["filePath"].(string); ok {
		return normalizePath(p, workDir)
	}
	return ""
}

// extractReadScope derives a human-readable scope string from a read tool call's
// arguments: "" for a bare full-file read, "symbol:<name>" for a symbol read,
// "L<offset>-<limit>" for an offset/limit range (offset defaults to 1 when only
// limit is given; limit is omitted when only offset is given). Used as the scope
// component of the loop-detection key and the ReadRecord.
func extractReadScope(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if symbol, ok := m["symbol"].(string); ok && symbol != "" {
		return "symbol:" + symbol
	}
	offset, _ := m["offset"].(float64)
	limit, _ := m["limit"].(float64)
	if int(offset) == 0 && int(limit) == 0 {
		return ""
	}
	start := int(offset)
	if start == 0 {
		start = 1
	}
	if int(limit) == 0 {
		return fmt.Sprintf("L%d-", start)
	}
	return fmt.Sprintf("L%d-%d", start, int(limit))
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
	} else {
		// write tool
		if content, ok := m["content"].(string); ok {
			action.NewText = content
		}
	}
	// Summary is intentionally empty — formatEditPlanSummary shows reasoning + path list.
	return action
}

// formatEditPlanSummary builds a user-facing summary of the agent's proposed changes.
// The summary shows the reasoning (WHY) first, then a file list, then asks
// the user to confirm. On confirmation, the plan is executed directly
// with diffs shown progressively during tool execution.
func formatEditPlanSummary(plan *PendingEditPlan, zh bool, cwd string) string {
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
	// Step 2: Ask for confirmation.
	if zh {
		sb.WriteString("\n确认执行修改？")
	} else {
		sb.WriteString("\nProceed with the changes?")
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

// isTestFile returns true if the file path looks like a test file.
func isTestFile(path string) bool {
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	// Common test file patterns: *_test.go, *_spec.*, test_*, *.test.*
	if strings.HasSuffix(name, "_test") {
		return true
	}
	if strings.HasSuffix(name, "_spec") {
		return true
	}
	if strings.HasPrefix(name, "test_") {
		return true
	}
	if strings.Contains(base, ".test.") {
		return true
	}
	return false
}

// extractCmd extracts the command string from a bash tool call input.
func extractCmd(input json.RawMessage) string {
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
	return ""
}

// isTestCommand returns true if the bash command is running tests.
func isTestCommand(cmd string) bool {
	if cmd == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(cmd))
	testPatterns := []string{
		"go test",
		"npm test",
		"npm run test",
		"yarn test",
		"yarn run test",
		"pnpm test",
		"pnpm run test",
		"pytest",
		"python -m pytest",
		"python3 -m pytest",
		"cargo test",
		"jest",
		"vitest",
		"mocha",
		"rspec",
		"bundle exec rspec",
		"rake test",
		"mix test",
		"dotnet test",
		"npx jest",
		"npx vitest",
		"make test",
	}
	for _, p := range testPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// inferTDDPhase examines the tool calls and results from the current turn
// and updates the engine's TDD phase state machine.
//
// State machine:
//
//	"" → RED (write/edit test file)
//	RED → RED_VERIFY (bash with test command)
//	RED_VERIFY → GREEN (write/edit non-test file after test failed)
//	RED_VERIFY → RED (test passed unexpectedly, fix test)
//	GREEN → GREEN_VERIFY (bash with test command)
//	GREEN_VERIFY → REFACTOR (write/edit after test passed)
//	GREEN_VERIFY → GREEN (test failed, fix implementation)
//	REFACTOR → RED (write/edit test file)
//
// Phases and their meanings:
//   - red:        Writing a failing test
//   - red_verify: Running the test to confirm it fails
//   - green:      Writing minimal implementation code
//   - green_verify: Running the test to confirm it passes
//   - refactor:   Cleaning up while keeping tests green
func (e *Engine) inferTDDPhase(calls []ToolCallRequest, results []ToolResult) {
	resultMap := make(map[string]ToolResult)
	for _, r := range results {
		resultMap[r.ToolCallID] = r
	}

	var newPhase string
	var newDetail string

	for _, call := range calls {
		result := resultMap[call.ID]

		switch call.Name {
		case "write", "edit":
			path := extractPathFromArgs(call.Input, e.config.WorkDir)
			if isTestFile(path) {
				newPhase = "red"
				newDetail = "编写测试..."
			} else if e.tddPhase == "red_verify" || e.tddPhase == "green_verify" || e.tddPhase == "refactor" || e.tddPhase == "green" {
				// Non-test file edit during implementation or refactor phase
				if e.tddPhase == "green_verify" || e.tddPhase == "refactor" {
					newPhase = "refactor"
					newDetail = "清理代码..."
				} else if newPhase != "red" {
					newPhase = "green"
					newDetail = "编写实现..."
				}
			} else if newPhase != "red" && newPhase != "green" {
				// First non-test edit without prior phase — could be GREEN
				newPhase = "green"
				newDetail = "编写实现..."
			}

		case "bash":
			cmd := extractCmd(call.Input)
			if isTestCommand(cmd) {
				switch e.tddPhase {
				case "red", "red_verify":
					// Running test during RED phase — checking for expected failure
					if result.ExitCode != nil && *result.ExitCode == 0 {
						newPhase = "red_verify"
						newDetail = "⚠️ 测试通过（预期失败）"
					} else {
						newPhase = "red_verify"
						newDetail = "✅ 测试失败（符合预期）"
					}
				case "green", "green_verify":
					if result.ExitCode != nil && *result.ExitCode == 0 {
						newPhase = "green_verify"
						newDetail = "✅ 测试通过"
					} else {
						newPhase = "green_verify"
						newDetail = "❌ 测试失败"
					}
				case "refactor":
					if result.ExitCode != nil && *result.ExitCode == 0 {
						newPhase = "refactor"
						newDetail = "✅ 测试通过（重构后）"
					} else {
						newPhase = "refactor"
						newDetail = "❌ 重构导致测试失败"
					}
				default:
					// Test run without prior phase — determine from exit code
					if result.ExitCode != nil && *result.ExitCode == 0 {
						newPhase = "green_verify"
						newDetail = "✅ 测试通过"
					} else {
						newPhase = "red_verify"
						newDetail = "✅ 测试失败（符合预期）"
					}
				}
			}
		}
	}

	// Only emit event and update state if phase actually changed
	if newPhase != "" && newPhase != e.tddPhase {
		e.tddPhase = newPhase
		e.tddPhaseDetail = newDetail
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type:   "tdd_phase",
				Name:   newPhase,
				Detail: newDetail,
			})
		}
	} else if newPhase != "" && newPhase == e.tddPhase && newDetail != e.tddPhaseDetail {
		// Same phase but detail changed (e.g., verify outcome updated)
		e.tddPhaseDetail = newDetail
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{
				Type:   "tdd_phase",
				Name:   newPhase,
				Detail: newDetail,
			})
		}
	}
}
