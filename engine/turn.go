package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	dlog "github.com/deepact/deepact/internal/log"
)

var turnLog = dlog.New("[turn] ")

// maxSegmentRunes caps a single buffered paragraph before it is force-flushed
// as a content_delta event. Without a blank-line boundary the UI would sit
// idle while the model streams a long unbroken paragraph; splitting into
// ~60-rune chunks keeps progress visible and each chunk stays a complete
// readable CJK/ASCII span (never cutting a multi-byte rune).
const maxSegmentRunes = 60

type TurnResult struct {
	Done         bool
	Blocked      bool
	BlockedBy    string
	Questions    []string
	FinishReason string
	// CompletionSummary holds the summary from the task_complete tool call,
	// set when the model explicitly signals task completion.
	CompletionSummary string
	// MadeProgress is true when this turn produced a progress signal: a
	// successful edit/write/revert/bash call, a handoff, a novel read (a
	// (path, scope) not yet read this Run), a novel search (grep/glob with a
	// new pattern/path key), or a novel lsp query. Repeated reads, repeated
	// searches and repeated lsp queries, todo_write, ask_user and narration
	// are NOT progress. The progressLoop breaker uses it to detect "N turns
	// without progress" loops while leaving legitimate investigation — reading,
	// searching or inspecting new content — alone.
	MadeProgress bool
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
			compacted, err := e.compressor.Compress(ctx, layer, e.state, e.history)
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
	// Remind the model about outstanding background async sub-agent tasks.
	e.injectBackgroundJobsSummary()
	for _, pm := range e.pendingPinnedMessages {
		messages = append(messages, ModelMessage{Role: "user", Content: pm})
	}
	e.pendingPinnedMessages = nil

	// Route model selection: use flash for low-risk / read-only turns.
	modelName := e.selectModel()

	req := ModelRequest{
		Model:           modelName,
		Messages:        messages,
		Tools:           e.toolSpecsWithHandoff(),
		MaxTokens:       e.maxOutputTokens(),
		ReasoningEffort: e.config.ReasoningEffort,
	}
	turnLog.Printf("turn %d start: model=%s msgs=%d ctx_build=%s", e.state.TurnNumber, modelName, len(messages), ctxBuildDur)
	streamStart := time.Now()
	stream, err := e.model.Stream(ctx, req)
	if err != nil {
		turnLog.Printf("stream model err: %v", err)
		e.state.ConsecutiveFailures++
		// Rate limits: the llm layer already retried with Retry-After backoff.
		// If it still failed, wait one more quiet window and re-attempt once —
		// the user should not be forced to type "continue" for a transient 429.
		if errors.Is(err, ErrModelRateLimit) {
			turnLog.Printf("rate limit persisted after llm retries; sleeping 60s then one final retry")
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "retry", Detail: "Rate limit hit — waiting 60s before retrying…"})
			}
			select {
			case <-time.After(60 * time.Second):
			case <-ctx.Done():
				return TurnResult{Blocked: true, BlockedBy: "model_error", FinishReason: "cancelled"}, nil
			}
			stream, err = e.model.Stream(ctx, req)
			if err != nil {
				turnLog.Printf("final retry after rate limit still failed: %v", err)
				return TurnResult{
					Blocked:      true,
					BlockedBy:    "model_error",
					Questions:    []string{fmt.Sprintf("模型 API 持续限流，请稍后重试。\n\n具体原因：%v\n\nThe model API is rate-limited. Please retry later.\n\nReason: %v", err, err)},
					FinishReason: "model_error",
				}, nil
			}
		} else if errors.Is(err, ErrModelInsufficientBal) {
			// Insufficient balance is persistent: retrying is pointless. Surface
			// a clear, actionable message instead of a generic network error.
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "model_error",
				Questions:    []string{"模型 API 余额不足，请充值后重试。\n\nInsufficient model API balance. Please top up and retry."},
				FinishReason: "model_error",
			}, nil
		} else {
			// Other errors (network, 5xx, etc.): llm layer retried; surface it.
			// 透传底层真实错误（HTTP 状态码/响应体/重试日志），避免把所有失败
			// 都呈现为"断网"，掩盖限流（429）等真实原因。
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "model_error",
				Questions:    []string{fmt.Sprintf("模型 API 请求失败，任务已中断。请检查网络与 API Key 配置后重新发起。\n\n具体原因：%v\n\nModel API request failed and the task was interrupted. Check your network and API key, then retry.\n\nReason: %v", err, err)},
				FinishReason: "model_error",
			}, nil
		}
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []ModelToolCall
	var finish string
	var lastUsage *ModelUsage
	var seg string // buffered content_delta text awaiting a paragraph boundary
	flushUpTo := func(n int) {
		if n <= 0 || e.config.OnProgress == nil {
			return
		}
		e.config.OnProgress(ProgressEvent{Type: "content_delta", Detail: seg[:n]})
		seg = seg[n:]
	}
	// steerStreamCancelled is set when the user's steer message interrupts
	// the stream mid-flight. The in-flight stream is cancelled so the
	// blocked Stream() call returns; the partially generated content is
	// dropped (never executed as tools, never shown as a conclusion).
	steerStreamCancelled := false
	cancelStream := func() {
		if !steerStreamCancelled {
			steerStreamCancelled = true
		}
	}
	// handleSteerInterrupt is the common exit for a soft interruption: it
	// persists any fully-received assistant content (with tool calls but
	// WITHOUT executing them) and returns a non-Done TurnResult so the Run
	// loop drains the steer queue and starts a fresh turn.
	handleSteerInterrupt := func() TurnResult {
		flushUpTo(len(seg))
		if contentBuilder.Len() > 0 || reasoningBuilder.Len() > 0 || len(toolCalls) > 0 {
			assistant := Message{
				Role:             "assistant",
				Content:          contentBuilder.String(),
				ReasoningContent: reasoningBuilder.String(),
				Timestamp:        time.Now(),
			}
			if len(toolCalls) > 0 {
				assistant.ToolCalls = make([]MessageToolCall, 0, len(toolCalls))
				for _, call := range toolCalls {
					if call.Function.Name == "" {
						continue
					}
					assistant.ToolCalls = append(assistant.ToolCalls, MessageToolCall{
						ID:        call.ID,
						Name:      call.Function.Name,
						Arguments: call.Function.Arguments,
					})
				}
			}
			e.history = append(e.history, assistant)
		}
		turnLog.Printf("turn %d: steer interrupted stream (content=%d reasoning=%d tool_calls=%d)",
			e.state.TurnNumber, contentBuilder.Len(), reasoningBuilder.Len(), len(toolCalls))
		// Done=false: the Run loop will drain the steer queue and
		// continue with a fresh turn that sees the new direction.
		return TurnResult{Done: false, FinishReason: "steer_interrupt"}
	}
	// checkSteerMidStream returns true when a steer message arrived while
	// the model was streaming. Soft interruption: stop consuming the
	// stream, let the Run loop inject the message and start a fresh turn
	// that sees the user's new direction immediately.
	checkSteerMidStream := func() bool {
		if e.steerQueueLen() == 0 {
			return false
		}
		// Cancelling the stream makes the blocked Stream() read return.
		// The loop below will see steerStreamCancelled and exit.
		cancelStream()
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "steer_interrupt", Detail: "steer"})
		}
		return true
	}
	for chunk := range stream {
		// Soft interruption: a user steer message arrived mid-stream.
		// Stop consuming, keep any fully-received tool_calls but do NOT
		// execute them, and return so the Run loop drains the queue.
		if steerStreamCancelled {
			return handleSteerInterrupt(), nil
		}
		// Non-blocking steer check between chunks: a user message queued
		// mid-stream triggers the soft interruption above at the top of the
		// next iteration. This is cheap (mutex + len) and runs per chunk.
		if checkSteerMidStream() {
			continue
		}
		if chunk.Err != nil {
			turnLog.Printf("stream chunk err: %v", chunk.Err)
			e.state.ConsecutiveFailures++
			// 透传底层真实错误：HTTP 状态码/响应体、超时类型、重试日志。
			// 429 限流、5xx、网络波动都各归其位，不再一律提示"断网"。
			// 402 余额不足：持久性错误，明确提示充值。
			if errors.Is(chunk.Err, ErrModelInsufficientBal) {
				return TurnResult{
					Blocked:      true,
					BlockedBy:    "stream_error",
					Questions:    []string{"模型 API 余额不足，请充值后重试。\n\nInsufficient model API balance. Please top up and retry."},
					FinishReason: "stream_error",
				}, nil
			}
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "stream_error",
				Questions:    []string{fmt.Sprintf("模型响应中断，自动重试已耗尽，任务终止。请检查网络后重新发起。\n\n具体原因：%v\n\nModel response was interrupted after automatic retries were exhausted. Check your network and retry.\n\nReason: %v", chunk.Err, chunk.Err)},
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
			seg += chunk.Delta
			// 段落切分：发射最后一个空行分隔符之前的完整段落（含 \n\n）。
			// 段落完整才输出，UI 每次渲染的都是语义完整的文本，半截
			// markdown（** / `）和 CJK 字符截断问题从根上消失。
			if idx := strings.LastIndex(seg, "\n\n"); idx >= 0 {
				flushUpTo(idx + 2)
			} else if utf8.RuneCountInString(seg) >= maxSegmentRunes {
				// 单段超长无空行：强制切段，避免屏幕长时间无反馈。
				flushUpTo(len(seg))
			}
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
	// Post-stream check: a steer message may arrive in the window between
	// the last chunk and stream close. Interrupting here still avoids
	// executing tools the user has redirected away from.
	if steerStreamCancelled {
		return handleSteerInterrupt(), nil
	}
	flushUpTo(len(seg)) // 流结束：发掉残余段

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

	// Layer 4 (max-token truncation): when the output cap cut the stream
	// (finish_reason == "length"), the response may end with a partially
	// streamed tool call whose arguments never closed. Never execute it —
	// drop every tool call and fall through to the text branch, whose
	// finish=="length" path resumes with "继续" (mirrors the harness
	// assembler's max-tokens truncation: tool calls are dropped, text kept).
	if finish == "length" {
		toolCalls = nil
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
		if finish == "length" {
			// Output-cap truncation: the model did not finish its turn. Keep
			// whatever text streamed, then resume it with a pinned "继续" so
			// the partial reply is never mistaken for a conclusion. The pinned
			// message is seen by the model next turn but is NOT persisted as a
			// fake user message in history.
			if assistant.Content != "" || assistant.ReasoningContent != "" {
				e.history = append(e.history, assistant)
			}
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, "继续")
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		if assistant.Content == "" && assistant.ReasoningContent == "" {
			turnLog.Printf("skipping empty assistant message (no content, no reasoning, no tool_calls)")
			return TurnResult{Done: true, FinishReason: finish}, nil
		}
		e.history = append(e.history, assistant)
		// Run stop hooks — structured checks that decide whether the model's
		// text-only response should end the loop or be nudged to continue.
		// Replaces the former isIntermediateText pattern-matching approach
		// with behavioral signals (e.g. runToolCallCount).
		hookResult := e.runStopHooks(ctx, StopHookContext{
			RunToolCallCount:   e.runToolCallCount,
			LastContent:        content,
			FinishReason:       finish,
			StopHookActive:     e.stopHookActive,
			StopHookRetryCount: e.stopHookRetryCount,
			IsChinese:          e.isChinese,
			Goal:               e.state.Goal,
		})
		// The model asked the user a question. Stop the loop and present the
		// question instead of nudging the model to continue — the model must
		// never decide on the user's behalf.
		if hookResult.AwaitUser {
			turnLog.Printf("turn %d: awaiting user (question detected, blocked)", e.state.TurnNumber)
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "awaiting_user",
				Questions:    []string{content},
				FinishReason: finish,
			}, nil
		}
		if hookResult.Block {
			// Nudge is injected as a pinned message: the model sees it on the
			// next turn to act, but it does NOT persist as a fake user message
			// in history (avoids context pollution / false user intent).
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, hookResult.Message)
			e.stopHookActive = true
			e.stopHookRetryCount++
			turnLog.Printf("stop hook blocked: reason=%s retry=%d", hookResult.Reason, e.stopHookRetryCount)
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		// If a stop hook returned Exhausted=true, MaxRetries was reached.
		// The model kept narrating instead of acting. Return Blocked (not
		// Done) so the user sees a diagnostic message instead of mistaking
		// the narration for a completed conclusion. When the hook didn't
		// block because the content is a genuine conclusion (not
		// exhaustion), Exhausted is false and we correctly return Done.
		if hookResult.Exhausted {
			msg := "Agent 在叙述循环中卡住了：已多次引导模型直接执行操作，但模型持续输出中间计划而非调用工具。请提供更具体的指令或缩小任务范围。"
			if !e.isChinese {
				msg = "The agent is stuck in a narration loop: guided nudges were tried but the model kept describing steps without executing them. Please provide a more specific instruction or narrow the task scope."
			}
			turnLog.Printf("stop hook exhausted after %d retries", e.stopHookRetryCount)
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "stalled_narration_exhausted",
				Questions:    []string{msg},
				FinishReason: finish,
			}, nil
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
	toolNames := make([]string, 0, len(calls))
	for _, c := range calls {
		toolNames = append(toolNames, c.Name)
	}
	turnLog.Printf("executeTurn tool branch: tools=%v finish=%s", toolNames, finish)
	if len(calls) == 0 {
		e.history = append(e.history, assistant)
		return TurnResult{Done: true, FinishReason: finish}, nil
	}

	// Intercept task_complete: explicit completion signal from the LLM.
	// When the model calls this tool, the turn ends immediately with the
	// provided summary. This is the primary completion mechanism - no stop
	// hooks, no guards, no keyword matching. The LLM explicitly signals "done".
	for _, call := range calls {
		if call.Name == TaskCompleteToolName {
			var params TaskCompleteParams
			_ = json.Unmarshal(call.Input, &params)
			e.history = append(e.history, assistant)
			for _, c := range calls {
				content := "Skipped: task_complete was called."
				if c.ID == call.ID {
					content = "Task completed."
				}
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    content,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{Done: true, CompletionSummary: params.Summary, FinishReason: finish}, nil
		}
	}

	// Skill HARD-GATE removed (2026-09-08): the engine no longer blocks
	// edit/write calls while a skill with a pre-implementation gate is active.
	// The skill's own methodology prompt is the authority — the harness stays
	// thin. This eliminates the "agent spins in the red phase without acting"
	// deadlock: an agent under TDD/systematic-debugging trying to write its
	// failing test was HARD-GATE-blocked and could only loop on todo_write/read.

	for _, call := range calls {
		scopeAction := e.guards.scope.CheckTool(call)
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

	// Check for load_skill tool call — intercept and return the skill's full content.
	// Collect tool messages in a separate slice and add them AFTER the assistant
	// message to satisfy DeepSeek API requirement: assistant(tool_calls) must be
	// followed by tool messages responding to each tool_call_id.
	pendingLoadMsgs := e.processLoadSkillCalls(calls)
	pendingPlanMsgs := e.processPlanTaskCalls(calls)
	pendingTodoMsgs := e.processTodoWriteCalls(calls)
	pendingAskUserMsgs := e.processAskUserCalls(calls)
	pendingAskUserMsgs = append(pendingAskUserMsgs, e.processAgentResumeCalls(ctx, calls)...)
	pendingPollMsgs := e.processAgentPollCalls(calls)

	e.history = append(e.history, assistant)

	// Add load_skill tool messages AFTER the assistant message, so the
	// DeepSeek API sees the correct order: assistant(tool_calls) → tool.
	for _, msg := range pendingLoadMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingPlanMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingTodoMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingAskUserMsgs {
		e.history = append(e.history, msg)
	}
	for _, msg := range pendingPollMsgs {
		e.history = append(e.history, msg)
	}

	// Separate handoff calls from regular tool calls.
	// load_skill is already handled by the intercept block above
	// (turn.go:363-416) — it must NOT enter regularCalls, or Execute will
	// produce a duplicate tool message ("tool not found: load_skill")
	// with the same tool_call_id, violating the API contract.
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else if call.Name == LoadSkillToolName {
			continue
		} else if call.Name == TodoWriteToolName {
			continue
		} else if call.Name == AskUserToolName {
			continue
		} else if call.Name == PlanTaskToolName {
			continue
		} else if call.Name == AgentPollToolName {
			continue
		} else if call.Name == AgentResumeToolName {
			continue
		} else {
			regularCalls = append(regularCalls, call)
		}
	}

	// Execute handoff calls through the registered SubAgentTool.
	if len(handoffCalls) > 0 {
		userLang := ""
		if e.isChinese {
			userLang = "中文"
		}
		// agent_start events for UI.
		for _, call := range handoffCalls {
			var params HandoffToAgentParams
			if err := json.Unmarshal(call.Input, &params); err == nil && e.config.OnProgress != nil {
				name := params.Agent
				if name == "" {
					name = "sub"
				}
				e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: name, Detail: params.Goal})
			}
		}
		execCtx := ToolExecContext{
			WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber,
			Ctx: ctx, Depth: 0, UserLang: userLang,
		}
		results := e.tools.Execute(execCtx, handoffCalls)
		// agent_done events for UI. Execute preserves order, so results[i]
		// corresponds to handoffCalls[i]; parse the real agent name from the
		// call input (r.ToolName is always "handoff_to_agent").
		for i, r := range results {
			if e.config.OnProgress != nil {
				name := "sub"
				if i < len(handoffCalls) {
					var params HandoffToAgentParams
					if err := json.Unmarshal(handoffCalls[i].Input, &params); err == nil && params.Agent != "" {
						name = params.Agent
					}
				}
				e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: name, Detail: briefDigest(r.Digest)})
			}
		}
		msgs := e.processHandoffResults(handoffCalls, results)
		for _, msg := range msgs {
			e.history = append(e.history, msg)
		}
	}

	// Execute regular tool calls.
	// Split into read-only (batch for speed) and destructive (sequential for progressive UX).
	// statusByID records each call's outcome status.
	statusByID := make(map[string]string, len(regularCalls))
	if len(regularCalls) > 0 {
		toolsStart := time.Now()
		var readOnlyCalls, destructiveCalls []ToolCallRequest
		callByID := make(map[string]ToolCallRequest, len(regularCalls))
		for _, call := range regularCalls {
			callByID[call.ID] = call
			if call.Name == "edit" || call.Name == "write" {
				destructiveCalls = append(destructiveCalls, call)
			} else {
				readOnlyCalls = append(readOnlyCalls, call)
			}
		}
		// rawResults keeps the un-annotated tool digests for task-state
		// extraction, while history carries the repeat annotation.
		rawResults := make([]ToolResult, 0, len(regularCalls))

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
				rawResults = append(rawResults, ToolResult{ToolCallID: result.ToolCallID, Digest: result.Digest})
				content := e.annotateRepeats(callByID[result.ToolCallID], result.Digest)
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: content, Timestamp: time.Now()})
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
				rawResults = append(rawResults, ToolResult{ToolCallID: result.ToolCallID, Digest: result.Digest})
				content := e.annotateRepeats(call, result.Digest)
				e.history = append(e.history, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: content, Timestamp: time.Now()})
			}
		}

		allCalls := append(readOnlyCalls, destructiveCalls...)
		e.updateTaskStateFromTools(allCalls, rawResults)
		e.runToolCallCount += len(regularCalls)
		e.stopHookRetryCount = 0 // reset on tool calls — agent is making progress

		turnLog.Printf("turn %d tools done: dur=%s calls=%d (ro=%d destructive=%d)",
			e.state.TurnNumber, time.Since(toolsStart), len(regularCalls), len(readOnlyCalls), len(destructiveCalls))
	}

	result := TurnResult{Done: false, FinishReason: finish}
	// ask_user: 模型需要用户输入。有 options → 本 turn 的报告文本即最终结论，
	// 立即结束 Run 并将报告全文作为 CompletionSummary。UI 收到干净的报告 +
	// Options（弹窗选择）。无 options → 同时置 Blocked + awaiting_user，经
	// loop.go 的 Blocked 分支（优先于 Done）呈现问题，用户自由输入。
	if req := e.peekAskUser(); req != nil {
		result.Done = true
		result.CompletionSummary = content
		// No options → present the question via the awaiting_user Blocked path
		// (loop.go processes Blocked before Done). The engine must never decide
		// on the user's behalf; use the structured Question, not narration text.
		if len(req.Options) == 0 {
			result.Blocked = true
			result.BlockedBy = "awaiting_user"
			result.Questions = []string{req.Question}
		}
	}
	// Progress signal for the progressLoop breaker: a successful destructive
	// call (edit/write/revert/bash), a handoff, a NOVEL read (a (path, scope)
	// not yet read this Run), a NOVEL search (grep/glob with a new
	// pattern/path key), or a NOVEL lsp query counts as progress. Repeated
	// reads, searches and lsp queries do not. This distinguishes legitimate
	// investigation — reading, searching or inspecting new content advances
	// the task — from a "N turns narrate but never act" loop.
	if e.progressKeys == nil {
		e.progressKeys = make(map[string]bool)
	}
	for _, c := range regularCalls {
		switch c.Name {
		case "edit", "write", "revert", "bash":
			if statusByID[c.ID] == "ok" {
				result.MadeProgress = true
			}
		case "read":
			if path := extractPathFromArgs(c.Input, e.config.WorkDir); path != "" {
				key := "read:" + path + "::" + extractReadScope(c.Input)
				if !e.progressKeys[key] {
					e.progressKeys[key] = true
					result.MadeProgress = true
				}
			}
		case "read_multi":
			for _, tgt := range parseReadMultiTargets(c.Input) {
				if tgt.Path == "" {
					continue
				}
				key := "read:" + normalizePath(tgt.Path, e.config.WorkDir) + "::" + readMultiTargetScope(tgt)
				if !e.progressKeys[key] {
					e.progressKeys[key] = true
					result.MadeProgress = true
				}
			}
		case "grep", "glob":
			// 新信息获取（新 pattern/路径的搜索）＝推进理解＝进展。
			key := searchKey(c, e.config.WorkDir)
			if key != "" && !e.progressKeys[key] {
				e.progressKeys[key] = true
				result.MadeProgress = true
			}
		case "lsp":
			// 同理：新的 lsp 查询（新 operation/文件/符号/位置）＝获取新信息＝
			// 进展；重复同一查询不算。
			key := lspKey(c, e.config.WorkDir)
			if key != "" && !e.progressKeys[key] {
				e.progressKeys[key] = true
				result.MadeProgress = true
			}
		}
		// 不 break：所有 read key 必须无条件记录，即使本轮已因 edit 等
		// 置位 MadeProgress——否则后续轮次重复读同一 (path, scope) 会被
		// 误判为"新读"而错误重置 progress 计数。
	}
	if !result.MadeProgress && len(handoffCalls) > 0 {
		result.MadeProgress = true
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

// toolSpecsWithHandoff returns the tool specs list with the load_skill and
// task tools appended. handoff_to_agent is provided by the registered
// SubAgentTool (via e.tools.Specs()); it is not appended here to avoid a
// duplicated spec.
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, loadSkillToolSpec())
	specs = append(specs, planTaskToolSpec(e.isChinese))
	specs = append(specs, taskCompleteToolSpec(e.isChinese))
	specs = append(specs, todoWriteToolSpec())
	specs = append(specs, askUserToolSpec(e.isChinese))
	specs = append(specs, agentPollToolSpec(e.isChinese))
	specs = append(specs, agentResumeToolSpec(e.isChinese))
	return specs
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

	// read_multi: show per-target paths with scope annotation, comma-joined.
	// Without this, read_multi falls through to fallbackSummary which returns
	// just "read_multi" - the user sees the tool name but not which files.
	if toolName == "read_multi" {
		targets := parseReadMultiTargets(input)
		if len(targets) == 0 {
			return fallbackSummary(toolName, m)
		}
		parts := make([]string, 0, len(targets))
		for _, tgt := range targets {
			s := relPath(tgt.Path, cwd)
			if tgt.Symbol != "" {
				s += " (symbol:" + tgt.Symbol + ")"
			} else if tgt.Offset > 0 || tgt.Limit > 0 {
				start := tgt.Offset
				if start == 0 {
					start = 1
				}
				if tgt.Limit == 0 {
					s += fmt.Sprintf(" (L%d-)", start)
				} else {
					s += fmt.Sprintf(" (L%d-%d)", start, start+tgt.Limit-1)
				}
			}
			parts = append(parts, s)
		}
		result := strings.Join(parts, ", ")
		if len(result) > 100 {
			result = result[:97] + "..."
		}
		return result
	}

	// Extract path first — all file-oriented tools have it.
	path := ""
	if p, ok := m["path"].(string); ok {
		path = p
	}

	// Skill/agent tools — show the human-relevant target, not an empty line.
	switch toolName {
	case "todo_write":
		if todos, ok := m["todos"].([]interface{}); ok {
			return fmt.Sprintf("update todos: %d 项", len(todos))
		}
		return "update todos"
	case "plan_task":
		return "deep plan"
	case "skill_install", "load_skill":
		// skill_install uses "name"; load_skill uses "skill_name".
		if n, ok := m["name"].(string); ok && n != "" {
			return "install skill: " + n
		}
		if n, ok := m["skill_name"].(string); ok && n != "" {
			return "load skill: " + n
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

	// Grep/glob: show pattern first, path as short suffix, include glob as
	// scope annotation so a filtered search is distinguishable in the UI.
	if toolName == "grep" || toolName == "glob" {
		include := ""
		if inc, ok := m["include"].(string); ok && strings.TrimSpace(inc) != "" {
			include = " (" + strings.TrimSpace(inc) + ")"
		}
		if pattern, ok := m["pattern"].(string); ok {
			if path != "" {
				return fmt.Sprintf("%s in %s%s", pattern, relPath(path, cwd), include)
			}
			return pattern + include
		}
		if path != "" {
			return relPath(path, cwd) + include
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

// searchKey builds a per-search progress key: "grep:<pattern>:<path>",
// "glob:<pattern>:<path>". pattern is the primary dimension; path is
// appended so searching different files is distinct. Empty key when neither
// pattern nor path is present.
func searchKey(c ToolCallRequest, workDir string) string {
	var m map[string]interface{}
	if len(c.Input) == 0 || json.Unmarshal(c.Input, &m) != nil {
		return ""
	}
	pattern, _ := m["pattern"].(string)
	path := extractPathFromArgs(c.Input, workDir)
	if pattern == "" && path == "" {
		return ""
	}
	return c.Name + ":" + pattern + ":" + path
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

func addToWorkingSet(state *TaskState, path string, notes string) {
	for i, f := range state.WorkingSet.Files {
		if f.Path == path {
			state.WorkingSet.Files[i].Notes = notes
			return
		}
	}
	state.WorkingSet.Files = append(state.WorkingSet.Files, FileRef{Path: path, Notes: notes})
}

// processLoadSkillCalls intercepts load_skill tool calls from the
// assistant's response. For each call, it either returns the skill's full
// content as the tool result (success) or produces an error tool message
// (bad JSON, empty name, unknown skill). Every load_skill call receives a
// tool response — this is critical because the DeepSeek API requires that
// every tool_call_id in an assistant message has a matching tool response.
// Without it, the next model call would be rejected and the session would
// be permanently stuck.
//
// The returned slice of Messages must be appended to history AFTER the
// assistant message to satisfy the API ordering:
// assistant(tool_calls) → tool(responses).
func (e *Engine) processLoadSkillCalls(calls []ToolCallRequest) []Message {
	var pendingLoadMsgs []Message
	for _, call := range calls {
		if call.Name != LoadSkillToolName {
			continue
		}
		var params LoadSkillParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid load_skill arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if params.SkillName == "" {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: load_skill requires a non-empty skill_name",
				Timestamp:  time.Now(),
			})
			continue
		}

		s := e.skills.Get(params.SkillName)
		if s == nil {
			pendingLoadMsgs = append(pendingLoadMsgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: skill %q not found", params.SkillName),
				Timestamp:  time.Now(),
			})
			continue
		}

		// Load semantics: return the full skill content as this turn's
		// tool result. No engine state, no persistent stable-zone injection.
		pendingLoadMsgs = append(pendingLoadMsgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("[SKILL — %s]\n\n%s", s.Name, s.Content),
			Timestamp:  time.Now(),
		})
	}
	return pendingLoadMsgs
}

// processPlanTaskCalls intercepts plan_task tool calls from the assistant's
// response. For each call it returns the built-in deep-planning methodology
// as the tool result (pure capability injection — no engine state, no gate).
// Every plan_task call receives a tool response, satisfying the DeepSeek API
// requirement that every tool_call_id in an assistant message has a matching
// tool response.
func (e *Engine) processPlanTaskCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != PlanTaskToolName {
			continue
		}
		var params map[string]json.RawMessage
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid plan_task arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("[PLAN_METHODOLOGY]\n\n%s", planMethodology(e.isChinese)),
			Timestamp:  time.Now(),
		})
	}
	return msgs
}

// processTodoWriteCalls intercepts todo_write tool calls from the assistant's
// response. Each call carries a FULL snapshot of the step list; the engine
// validates it and forwards it to the UI as a "todo_update" progress event.
// Every call receives a tool response message (satisfying the DeepSeek API
// requirement that every tool_call_id has a matching tool response).
func (e *Engine) processTodoWriteCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != TodoWriteToolName {
			continue
		}
		var params struct {
			Todos []TodoItem `json:"todos"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid todo_write arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		valid := true
		for _, t := range params.Todos {
			if strings.TrimSpace(t.Content) == "" {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Error: todo_write requires non-empty content for each item",
					Timestamp:  time.Now(),
				})
				valid = false
				break
			}
			if t.Status != "pending" && t.Status != "in_progress" && t.Status != "completed" {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    fmt.Sprintf("Error: invalid todo status %q (must be pending, in_progress, or completed)", t.Status),
					Timestamp:  time.Now(),
				})
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "todo_update", Todos: params.Todos})
		}
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("✓ 已更新 %d 项 todo", len(params.Todos)),
			Timestamp:  time.Now(),
		})
	}
	return msgs
}

// processAgentPollCalls intercepts agent_poll tool calls from the assistant's
// response. Each call queries one background async sub-agent task by job_id.
// A running task returns "running"; a done task returns its result and is
// removed from the table; an unknown job_id returns an error. Every call
// receives a tool response (satisfying the DeepSeek API requirement that
// every tool_call_id has a matching tool response).
func (e *Engine) processAgentPollCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != AgentPollToolName {
			continue
		}
		var params struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil || strings.TrimSpace(params.JobID) == "" {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: agent_poll requires a non-empty job_id",
				Timestamp:  time.Now(),
			})
			continue
		}
		e.initBackgroundTasks()
		e.bgMu.Lock()
		task, ok := e.bgTasks[params.JobID]
		if !ok {
			e.bgMu.Unlock()
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: agent_poll job %q not found (already consumed or run ended).", params.JobID),
				Timestamp:  time.Now(),
			})
			continue
		}
		if e.suspendedJobExpiredLocked(task) {
			delete(e.bgTasks, params.JobID)
			e.bgMu.Unlock()
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Job %s has expired (suspended for over %s) and can no longer be resumed. Re-delegate the task if you still need it.", params.JobID, suspendedJobTTL),
				Timestamp:  time.Now(),
			})
			continue
		}
		if task.state == bgStateAwaitingUser {
			// Suspended: nothing is running, so the channel would always read as
			// "still running". Report the question and how to answer it.
			e.bgMu.Unlock()
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    formatSuspendedPoll(params.JobID, task),
				Timestamp:  time.Now(),
			})
			continue
		}
		e.bgMu.Unlock()

		select {
		case result := <-task.result:
			// Done: remove from table and return the result.
			e.bgMu.Lock()
			delete(e.bgTasks, params.JobID)
			e.bgMu.Unlock()
			if result == nil {
				result = &HandoffResult{Summary: "(no result)"}
			}
			content := fmt.Sprintf("Job %s done.\n%s", params.JobID, formatHandoffResult(result, e.isChinese))
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    content,
				Timestamp:  time.Now(),
			})
		default:
			// Still running.
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Job %s (%s) is still running. Goal: %s. Poll again later.", params.JobID, task.agent, task.goal),
				Timestamp:  time.Now(),
			})
		}
	}
	return msgs
}

// injectBackgroundJobsSummary appends a pinned message listing outstanding
// background async sub-agent tasks so the model knows it has jobs running and
// can continue working while remembering to poll them. No-op when none exist.
// The pinned message is consumed next turn (pendingPinnedMessages semantics)
// and never persists into history.
func (e *Engine) injectBackgroundJobsSummary() {
	e.bgMu.Lock()
	n := len(e.bgTasks)
	if n == 0 {
		e.bgMu.Unlock()
		return
	}
	var b strings.Builder
	b.WriteString("[Background jobs] ")
	first := true
	for id, t := range e.bgTasks {
		if !first {
			b.WriteString("; ")
		}
		first = false
		switch {
		case t.state == bgStateAwaitingUser && t.suspended != nil && t.suspended.Question != "":
			fmt.Fprintf(&b, "%s (%s): %s — waiting for user input (answer with agent_resume(%s, …))", id, t.agent, t.goal, id)
		case t.state == bgStateAwaitingUser:
			fmt.Fprintf(&b, "%s (%s): %s — waiting for a sub-agent result", id, t.agent, t.goal)
		default:
			fmt.Fprintf(&b, "%s (%s): %s — running", id, t.agent, t.goal)
		}
	}
	e.bgMu.Unlock()
	b.WriteString(". Continue your work; use agent_poll(job_id) to fetch results.")
	e.pendingPinnedMessages = append(e.pendingPinnedMessages, b.String())
}

// processAskUserCalls intercepts ask_user tool calls from the assistant's
// response. Each call declares a question (and optionally 2-6 candidate
// answers) the user must respond to; the engine stores it for presentation.
// Every call receives a tool response message (satisfying the DeepSeek API
// requirement that every tool_call_id has a matching tool response).
func (e *Engine) processAskUserCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != AskUserToolName {
			continue
		}
		var params struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
		}
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    fmt.Sprintf("Error: invalid ask_user arguments: %v", err),
				Timestamp:  time.Now(),
			})
			continue
		}
		if strings.TrimSpace(params.Question) == "" {
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    "Error: ask_user requires a non-empty question",
				Timestamp:  time.Now(),
			})
			continue
		}
		if len(params.Options) > 0 {
			if len(params.Options) < 2 || len(params.Options) > 6 {
				msgs = append(msgs, Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    "Error: ask_user options require 2 to 6 items. Provide 2-6 candidate answers or omit options for an open-ended question.",
					Timestamp:  time.Now(),
				})
				continue
			}
			valid := true
			for _, o := range params.Options {
				if strings.TrimSpace(o) == "" {
					msgs = append(msgs, Message{
						Role:       "tool",
						ToolCallID: call.ID,
						Content:    "Error: ask_user requires non-empty option strings",
						Timestamp:  time.Now(),
					})
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
		}
		e.pushAskUser(&AskUserRequest{
			Question: params.Question,
			Options:  append([]string(nil), params.Options...),
		})
		msgs = append(msgs, Message{
			Role:       "tool",
			ToolCallID: call.ID,
			Content:    "✓ 已记录问题，等待用户回答。",
			Timestamp:  time.Now(),
		})
	}
	return msgs
}

// processHandoffResults builds tool response messages for handoff call results.
// Every handoff call receives a response — even cancelled ones — to prevent
// orphaned tool_call_ids that would cause the DeepSeek API to reject the next
// request.
func (e *Engine) processHandoffResults(handoffCalls []ToolCallRequest, results []ToolResult) []Message {
	messages := make([]Message, 0, len(handoffCalls))
	for i := range handoffCalls {
		result := results[i]

		// Always add a tool response — even for cancelled sub-agents.
		// Without it, the tool_call_id is orphaned and the DeepSeek API
		// rejects the next request, permanently stalling the session.
		content := result.Digest
		if result.Status == "cancelled" || result.FinishReason == HandoffReasonCancelled {
			content = "Sub-agent cancelled."
		}
		messages = append(messages, Message{Role: "tool", ToolCallID: result.ToolCallID, Content: content, Timestamp: time.Now()})

		// C6: a sub-agent that ended WITHOUT delivering a result must not dump
		// a partial digest on the user and wait for "继续". Pin a follow-up so
		// the parent auto-continues within the same Run: it reads the partial
		// result, fills gaps, and either completes or re-delegates.
		if isHandoffFollowUpReason(result.FinishReason) {
			e.pendingPinnedMessages = append(e.pendingPinnedMessages, buildHandoffFollowUp(result.FinishReason, e.isChinese))
		}
	}
	return messages
}

// isHandoffFollowUpReason reports whether a handoff's FinishReason is a
// failure that needs the parent to continue. completed and cancelled are not:
// the former delivered a real result, the latter was a user/context decision.
func isHandoffFollowUpReason(reason string) bool {
	switch reason {
	case "", HandoffReasonCompleted, HandoffReasonCancelled, HandoffReasonAsyncRunning:
		return false
	default:
		return true
	}
}

// buildHandoffFollowUp returns the pinned instruction that tells the parent
// to continue after a sub-agent failed to deliver a complete result.
func buildHandoffFollowUp(reason string, zh bool) string {
	if reason == HandoffReasonAwaitingUser {
		if zh {
			return "以上子代理需要用户输入。若用户这条消息正是在回答它，请用 agent_resume(<句柄>, 回答) 让它带着原有上下文继续；否则基于它的部分结果自行继续或重新委派。"
		}
		return "The sub-agent above is waiting for user input. If the user's latest message answers it, call agent_resume(<handle>, <answer>) so it continues with its original context; otherwise continue from its partial findings or re-delegate."
	}
	if zh {
		return "以上子代理未给出完整结论。请基于其部分结果继续处理：补充缺失信息后给出最终结论，或重新委派合适的子代理。不要停在这里等待用户输入。"
	}
	return "The sub-agent above did not deliver a complete result. Continue from its partial findings: complete the remaining work and give your final conclusion, or re-delegate to a more suitable sub-agent. Do not stop and wait for user input."
}
