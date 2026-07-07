package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	// VerifyFailedSummary is set when a critic handoff returns FAIL verdict.
	// The caller (Engine.Run) must present this to the user and pause the agent loop.
	VerifyFailedSummary string
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
			Questions:    []string{fmt.Sprintf("API 请求失败，请检查网络连接和 API Key 后重试。\n\nAPI request failed. Please check your connection and API key, then try again.")},
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
				Questions:    []string{fmt.Sprintf("网络连接中断，请检查网络后重试。\n\nConnection interrupted. Please check your network and try again.")},
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

	// Reset consecutive failure counter — this LLM call succeeded.
	e.state.ConsecutiveFailures = 0

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

	// Layer 2b: Strip echoed internal prompt/context blocks (Block B, TASK
	// REMINDER, Environment, read-history hint, ...). DeepSeek sometimes echoes
	// these back; they must never reach the user or be written into history.
	content = stripInternalPromptEcho(content)

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
		// Run stop hooks — structured checks that decide whether the model's
		// text-only response should end the loop or be nudged to continue.
		// Replaces the former isIntermediateText pattern-matching approach
		// with behavioral signals (e.g. runToolCallCount).
		hookResult := e.runStopHooks(StopHookContext{
			RunToolCallCount:   e.runToolCallCount,
			LastContent:        content,
			FinishReason:       finish,
			StopHookActive:     e.stopHookActive,
			StopHookRetryCount: e.stopHookRetryCount,
			IsChinese:          e.isChinese,
		})
		if hookResult.Block {
			e.history = append(e.history, Message{
				Role: "user", Content: hookResult.Message, Timestamp: time.Now(),
			})
			e.stopHookActive = true
			e.stopHookRetryCount++
			turnLog.Printf("stop hook blocked: reason=%s retry=%d", hookResult.Reason, e.stopHookRetryCount)
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

	// 用可见回复作为方案原因展示，不混入内部思考。
	// ReasoningContent 是模型内部思考（通常是英文），不应展示给用户。
	// 当 Content 为空（DeepSeek 常裸发 edit/write 工具调用而不带正文）时，
	// 回退到历史中最近一条实质性 assistant 文本——即用户刚确认过的分析报告，
	// 避免显示误导性的“AI 未提供修改原因”，让确认闸门连贯。
	mergedReasoning := reasoningForEditPlan(e.history, assistant.Content)

	// Design-phase gating is delegated to the active skill's own <HARD-GATE>
	// text (injected into the stable context zone on activation) — the engine
	// does NOT add a code-level edit/write block here. A code guard cannot tell
	// "write implementation code" from "write a design doc", so it bluntly
	// blocked the skill's own design-doc writes (brainstorming step 6) and, when
	// mis-conditioned on ActiveSkillName=="", deadlocked every normal edit with
	// no escape hatch. When no skill is active, the edit-plan guard below is the
	// interception: it presents proposed edits for user confirmation with a
	// PlanConfirmed escape hatch — death-loop-safe.

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
	pendingActivateMsgs := e.processActivateSkillCalls(calls)

	e.history = append(e.history, assistant)

	// Add activate_skill tool messages AFTER the assistant message, so the
	// DeepSeek API sees the correct order: assistant(tool_calls) → tool.
	for _, msg := range pendingActivateMsgs {
		e.history = append(e.history, msg)
	}

	// Separate handoff calls from regular tool calls.
	// activate_skill is already handled by the intercept block above
	// (turn.go:363-416) — it must NOT enter regularCalls, or Execute will
	// produce a duplicate tool message ("tool not found: activate_skill")
	// with the same tool_call_id, violating the API contract.
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else if call.Name == ActivateSkillToolName {
			continue
		} else {
			regularCalls = append(regularCalls, call)
		}
	}

	// Execute handoff calls (sub-agents) — parallel when multiple, sequential when single.
	if len(handoffCalls) > 0 {
		results := e.executeHandoffsParallel(ctx, handoffCalls)
		msgs, criticFail := e.processHandoffResults(handoffCalls, results, regularCalls)
		for _, msg := range msgs {
			e.history = append(e.history, msg)
		}
		if criticFail != "" {
			return TurnResult{
				Done:                true,
				VerifyFailedSummary: criticFail,
			}, nil
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
		e.stopHookRetryCount = 0 // reset on tool calls — agent is making progress

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
	specs = append(specs, handoffToolSpec(e.isChinese))
	specs = append(specs, activateSkillToolSpec())
	return specs
}

// buildCriticFailSummary formats the critic FAIL report for user presentation.
func buildCriticFailSummary(digest string, zh bool) string {
	var sb strings.Builder
	if zh {
		sb.WriteString("## ⚠️ 对抗验证未通过\n\n")
		sb.WriteString("Critic 代理对当前实现进行了对抗性验证，发现以下问题：\n\n")
		sb.WriteString("---\n\n")
		sb.WriteString(digest)
		sb.WriteString("\n\n---\n\n")
		sb.WriteString("### 请选择下一步操作\n\n")
		sb.WriteString("- **修复**: 根据反馈继续修改代码，修改后重新验证\n")
		sb.WriteString("- **说明/澄清**: 如果你认为某条反馈是误报或需要补充上下文，请直接回复\n")
		sb.WriteString("- **跳过**: 忽略此验证结果，继续原方案\n")
		sb.WriteString("- **放弃**: 放弃当前方案，重新考虑")
	} else {
		sb.WriteString("## ⚠️ Adversarial Verification Failed\n\n")
		sb.WriteString("The critic agent performed adversarial verification on the current implementation and found issues:\n\n")
		sb.WriteString("---\n\n")
		sb.WriteString(digest)
		sb.WriteString("\n\n---\n\n")
		sb.WriteString("### Choose Next Action\n\n")
		sb.WriteString("- **Fix**: Continue modifying code based on feedback, then re-verify\n")
		sb.WriteString("- **Clarify**: If you think a finding is a false positive or needs context, reply directly\n")
		sb.WriteString("- **Skip**: Ignore this verification result and continue with the original plan\n")
		sb.WriteString("- **Abandon**: Abandon the current approach and reconsider")
	}
	return sb.String()
}

// parseCriticVerdict extracts the VERDICT line from a critic agent's output.
// Returns "PASS", "FAIL", "PARTIAL", or "" if no verdict found.
func parseCriticVerdict(digest string) string {
	for _, line := range strings.Split(digest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "VERDICT:") {
			v := strings.TrimSpace(strings.TrimPrefix(trimmed, "VERDICT:"))
			switch v {
			case "PASS", "FAIL", "PARTIAL":
				return v
			}
		}
	}
	return ""
}

// isCriticHandoff checks whether a handoff_to_agent call targets the critic agent.
func isCriticHandoff(input json.RawMessage) bool {
	var params HandoffToAgentParams
	if err := json.Unmarshal(input, &params); err != nil {
		return false
	}
	return params.Agent == string(AgentCritic)
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

// executeHandoffsParallel runs multiple handoff_to_agent calls concurrently.
// Each sub-agent runs in its own goroutine; results are collected and returned
// in the original call order. Progress events (agent_start/agent_done) are
// emitted with the actual agent name and goal, enabling the UI to display
// multiple sub-agents working simultaneously.
func (e *Engine) executeHandoffsParallel(ctx context.Context, calls []ToolCallRequest) []ToolResult {
	if len(calls) == 0 {
		return nil
	}

	type indexedResult struct {
		index  int
		result ToolResult
	}

	resultsCh := make(chan indexedResult, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c ToolCallRequest) {
			defer wg.Done()

			// Parse params for progress display
			var params HandoffToAgentParams
			if err := json.Unmarshal(c.Input, &params); err == nil {
				agentName := params.Agent
				if agentName == "" {
					agentName = "sub"
				}
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{
						Type:   "agent_start",
						Name:   agentName,
						Detail: params.Goal,
					})
				}
			}

			r := e.executeHandoff(ctx, c)

			// Parse again for agent_done event (use same name)
			var params2 HandoffToAgentParams
			if err := json.Unmarshal(c.Input, &params2); err == nil {
				agentName := params2.Agent
				if agentName == "" {
					agentName = "sub"
				}
				if e.config.OnProgress != nil {
					e.config.OnProgress(ProgressEvent{
						Type:   "agent_done",
						Name:   agentName,
						Detail: briefDigest(r.Digest),
					})
				}
			}

			resultsCh <- indexedResult{index: idx, result: r}
		}(i, call)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	// Collect results in original order
	ordered := make([]ToolResult, len(calls))
	for ir := range resultsCh {
		ordered[ir.index] = ir.result
	}

	return ordered
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

	// Read tool: annotate the scope so a full read is distinguishable from a
	// targeted read in the UI. Without this every read renders as a bare path,
	// so repeated full-file reads (a loop symptom) look identical to targeted
	// reads and can't be diagnosed. Formats:
	//   full file    -> "path (全文)"
	//   symbol       -> "path (symbol:Run)"
	//   offset/limit -> "path (L52-101)"  (end line computed to avoid the
	//                                       ambiguous "L52-50" offset/limit form)
	if toolName == "read" && path != "" {
		if sym, ok := m["symbol"].(string); ok && strings.TrimSpace(sym) != "" {
			return fmt.Sprintf("%s (symbol:%s)", relPath(path, cwd), strings.TrimSpace(sym))
		}
		offset, _ := m["offset"].(float64)
		limit, _ := m["limit"].(float64)
		if int(offset) == 0 && int(limit) == 0 {
			return relPath(path, cwd) + " (全文)"
		}
		start := int(offset)
		if start == 0 {
			start = 1
		}
		if int(limit) == 0 {
			return fmt.Sprintf("%s (L%d-)", relPath(path, cwd), start)
		}
		return fmt.Sprintf("%s (L%d-%d)", relPath(path, cwd), start, start+int(limit)-1)
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

// reasoningForEditPlan returns the reasoning text for an edit plan summary.
// When the current assistant content is non-empty, it is used directly.
// When empty (e.g. DeepSeek emits bare edit/write tool calls without a body),
// the function walks history backwards to find the most recent assistant
// message with non-empty content — typically the analysis report the user
// just confirmed. This prevents the misleading "AI 未提供修改原因" placeholder.
func reasoningForEditPlan(history []Message, currentContent string) string {
	if strings.TrimSpace(currentContent) != "" {
		return currentContent
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && strings.TrimSpace(history[i].Content) != "" {
			return history[i].Content
		}
	}
	return ""
}

// formatEditPlanSummary builds a user-facing summary of the agent's proposed changes.
// The summary shows the reasoning (WHY) first, then asks the user to confirm.
// On confirmation, the plan is executed directly with diffs shown progressively
// during tool execution.
func formatEditPlanSummary(plan *PendingEditPlan, zh bool, cwd string) string {
	var sb strings.Builder

	// Step 1: Show the reasoning — WHY these changes are proposed.
	if reasoning := plan.Reasoning; reasoning != "" {
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

// processActivateSkillCalls intercepts activate_skill tool calls from the
// assistant's response. For each call, it either activates the skill (success)
// or produces an error tool message (bad JSON, empty name, unknown skill).
// Every activate_skill call receives a tool response — this is critical because
// the DeepSeek API requires that every tool_call_id in an assistant message has
// a matching tool response. Without it, the next model call would be rejected
// and the session would be permanently stuck.
//
// The returned slice of Messages must be appended to history AFTER the
// assistant message to satisfy the API ordering:
// assistant(tool_calls) → tool(responses).
func (e *Engine) processActivateSkillCalls(calls []ToolCallRequest) []Message {
	var pendingActivateMsgs []Message
	for _, call := range calls {
		if call.Name != ActivateSkillToolName {
			continue
		}
		var params ActivateSkillParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			pendingActivateMsgs = append(pendingActivateMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid activate_skill arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if params.SkillName == "" {
			pendingActivateMsgs = append(pendingActivateMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: activate_skill requires a non-empty skill_name",
				Timestamp:  time.Now(),
			})
			continue
		}

		// Directly activate the skill — no user confirmation needed
		s := e.skills.Get(params.SkillName)
		if s == nil {
			pendingActivateMsgs = append(pendingActivateMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: skill %q not found", params.SkillName),
				Timestamp:  time.Now(),
			})
			continue
		}
		prevSkill := e.lastActivatedSkill
		e.activatedSkills[s.Name] = true
		e.lastActivatedSkill = s.Name
		e.state.ActiveSkillName = s.Name
		e.state.ActiveSkillContent = s.Content

		// Inject skill methodology into stable zone (persistent across turns)
		e.context.SetActiveSkill(s.Name, s.Content)

		chainInfo := ""
		if prevSkill != "" {
			chainInfo = fmt.Sprintf(" (chain: %s → %s)", prevSkill, s.Name)
		}
		skillMsg := fmt.Sprintf(
			"✅ Skill `%s` activated%s. Full methodology now in stable zone.",
			s.Name, chainInfo,
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
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("✅ Activated skill `%s`%s", s.Name, chainInfo),
			Timestamp:  time.Now(),
		})
	}
	return pendingActivateMsgs
}

// processHandoffResults builds tool response messages for handoff call results.
// Every handoff call receives a response — even cancelled ones — to prevent
// orphaned tool_call_ids that would cause the DeepSeek API to reject the next
// request. If a critic sub-agent returns FAIL, responses are also added for
// all remaining handoff calls and regular calls (which won't execute), and
// criticFail is set so the caller can return early with the failure summary.
func (e *Engine) processHandoffResults(handoffCalls []ToolCallRequest, results []ToolResult, regularCalls []ToolCallRequest) (messages []Message, criticFail string) {
	for i, call := range handoffCalls {
		result := results[i]

		// Hard gate: if critic returns FAIL, intercept and present to user.
		if isCriticHandoff(call.Input) && parseCriticVerdict(result.Digest) == "FAIL" {
			messages = append(messages, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()})

			// Add tool responses for remaining handoff calls so their
			// tool_call_ids are not orphaned (API requires every tool_call
			// to have a matching tool response).
			for j := i + 1; j < len(handoffCalls); j++ {
				r := results[j]
				content := r.Digest
				if content == "" {
					content = "Skipped: critic returned FAIL."
				}
				messages = append(messages, Message{Role: "tool", ToolCallID: r.ToolCallID, Content: content, Timestamp: time.Now()})
			}
			// Add placeholder responses for regular calls that won't execute.
			for _, rc := range regularCalls {
				messages = append(messages, Message{Role: "tool", ToolCallID: rc.ID, Content: "Skipped: critic returned FAIL.", Timestamp: time.Now()})
			}

			criticFail = buildCriticFailSummary(result.Digest, e.isChinese)
			return messages, criticFail
		}

		// Always add a tool response — even for cancelled sub-agents.
		// Without it, the tool_call_id is orphaned and the DeepSeek API
		// rejects the next request, permanently stalling the session.
		content := result.Digest
		if result.Status == "cancelled" {
			content = "Sub-agent cancelled."
		}
		messages = append(messages, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: content, Timestamp: time.Now()})
	}
	return messages, ""
}
