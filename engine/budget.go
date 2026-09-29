package engine

import "unicode/utf8"

// This file holds the context-budget primitives shared by compression and the
// pre-send context guard. They use a dependency-free 1 token ≈ 3 characters
// heuristic (matching llm.TokenEstimator's default) so the compressor, the
// guard and the reactive context-length retry all measure context on one scale.

const (
	// trimBodyFloor is the smallest a message body may be shrunk to by the hard
	// budget guard. It bounds the truncation loop while still making progress.
	trimBodyFloor = 2000
	// trimBodyMarker is appended to a body shortened by the guard so the model
	// knows content was dropped.
	trimBodyMarker = "\n…[truncated to fit the model context window]"
)

// approxCharsTokens converts a character count to tokens (1 token ≈ 3 chars).
func approxCharsTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return chars/3 + 1
}

// messageChars counts the characters a Message contributes to the request.
func messageChars(m Message) int {
	n := len(m.Content) + len(m.ReasoningContent)
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
	}
	return n
}

// modelMessageChars counts the characters a ModelMessage contributes.
func modelMessageChars(m ModelMessage) int {
	n := len(m.Content) + len(m.ReasoningContent)
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// messageTokens estimates one Message's token footprint.
func messageTokens(m Message) int { return approxCharsTokens(messageChars(m)) }

// modelMessageTokens estimates one ModelMessage's token footprint.
func modelMessageTokens(m ModelMessage) int { return approxCharsTokens(modelMessageChars(m)) }

// estimateHistoryTokens estimates a []Message history's total token footprint.
func estimateHistoryTokens(history []Message) int {
	total := 0
	for _, m := range history {
		total += messageTokens(m)
	}
	return total
}

// estimateModelHistoryTokens estimates a []ModelMessage history's footprint.
func estimateModelHistoryTokens(history []ModelMessage) int {
	total := 0
	for _, m := range history {
		total += modelMessageTokens(m)
	}
	return total
}

// estimateToolSpecTokens estimates the token cost of the tool schemas sent with
// a request. They are not part of the message list, so a pre-send budget check
// must count them separately. A nil estimator yields 0.
func estimateToolSpecTokens(estimate func([]ModelMessage) int, tools []ModelTool) int {
	if len(tools) == 0 || estimate == nil {
		return 0
	}
	msgs := make([]ModelMessage, 0, len(tools))
	for _, t := range tools {
		msgs = append(msgs, ModelMessage{
			Content: t.Function.Name + t.Function.Description + string(t.Function.Parameters),
		})
	}
	return estimate(msgs)
}

// promptBlocksTokens estimates the fixed prompt blocks (system prompt, stable
// session context, skills) prepended to every request.
func promptBlocksTokens(estimate func([]ModelMessage) int, blocks []string) int {
	if len(blocks) == 0 || estimate == nil {
		return 0
	}
	msgs := make([]ModelMessage, 0, len(blocks))
	for _, b := range blocks {
		msgs = append(msgs, ModelMessage{Content: b})
	}
	return estimate(msgs)
}

// systemHeadLen returns how many leading messages must survive a trim. A
// leading system message is pinned: the sub-agent history starts with its
// system prompt, and dropping it would change the agent's identity.
func systemHeadLen(history []Message) int {
	if len(history) > 0 && history[0].Role == "system" {
		return 1
	}
	return 0
}

func systemHeadModelLen(history []ModelMessage) int {
	if len(history) > 0 && history[0].Role == "system" {
		return 1
	}
	return 0
}

// trimHistoryToBudget returns the newest suffix of history whose estimated token
// footprint fits budget. Oldest messages are dropped until it fits; the cut is
// advanced so the tail never begins with an orphaned tool response (which would
// break the assistant(tool_calls) → tool contract). A leading system message is
// preserved. When dropping alone cannot reach the budget (a single oversized
// message), the largest tool/assistant bodies are truncated in place — pairing
// holds because bodies shrink, messages are not removed.
func trimHistoryToBudget(history []Message, budget int) []Message {
	if budget <= 0 || len(history) == 0 {
		return history
	}
	head := systemHeadLen(history)
	start := head
	used := estimateHistoryTokens(history)
	for start < len(history) && used > budget {
		used -= messageTokens(history[start])
		start++
		for start < len(history) && history[start].Role == "tool" {
			used -= messageTokens(history[start])
			start++
		}
	}
	if start >= len(history) {
		start = len(history) - 1
		for start > head && history[start].Role == "tool" {
			start--
		}
		if history[start].Role == "tool" {
			return history // cannot trim without orphaning a tool response
		}
	}
	result := make([]Message, 0, head+len(history)-start)
	result = append(result, history[:head]...)
	result = append(result, history[start:]...)
	if estimateHistoryTokens(result) <= budget {
		return result
	}
	return truncateHistoryBodies(result, budget)
}

// trimModelHistoryToBudget is the ModelMessage variant of trimHistoryToBudget.
func trimModelHistoryToBudget(history []ModelMessage, budget int) []ModelMessage {
	if budget <= 0 || len(history) == 0 {
		return history
	}
	head := systemHeadModelLen(history)
	start := head
	used := estimateModelHistoryTokens(history)
	for start < len(history) && used > budget {
		used -= modelMessageTokens(history[start])
		start++
		for start < len(history) && history[start].Role == "tool" {
			used -= modelMessageTokens(history[start])
			start++
		}
	}
	if start >= len(history) {
		start = len(history) - 1
		for start > head && history[start].Role == "tool" {
			start--
		}
		if history[start].Role == "tool" {
			return history
		}
	}
	result := make([]ModelMessage, 0, head+len(history)-start)
	result = append(result, history[:head]...)
	result = append(result, history[start:]...)
	if estimateModelHistoryTokens(result) <= budget {
		return result
	}
	return truncateModelHistoryBodies(result, budget)
}

// truncateHistoryBodies halves the largest shrinkable body until the history
// fits budget or no body can shrink further.
func truncateHistoryBodies(history []Message, budget int) []Message {
	out := append([]Message(nil), history...)
	for iter := 0; iter < 128 && estimateHistoryTokens(out) > budget; iter++ {
		idx := largestShrinkableMessage(out)
		if idx < 0 {
			break
		}
		target := len(out[idx].Content) / 2
		if target < trimBodyFloor {
			target = trimBodyFloor
		}
		out[idx].Content = truncateRunes(out[idx].Content, target) + trimBodyMarker
	}
	return out
}

// truncateModelHistoryBodies is the ModelMessage variant of truncateHistoryBodies.
func truncateModelHistoryBodies(history []ModelMessage, budget int) []ModelMessage {
	out := append([]ModelMessage(nil), history...)
	for iter := 0; iter < 128 && estimateModelHistoryTokens(out) > budget; iter++ {
		idx := largestShrinkableModelMessage(out)
		if idx < 0 {
			break
		}
		target := len(out[idx].Content) / 2
		if target < trimBodyFloor {
			target = trimBodyFloor
		}
		out[idx].Content = truncateRunes(out[idx].Content, target) + trimBodyMarker
	}
	return out
}

func largestShrinkableMessage(history []Message) int {
	idx := -1
	for i := range history {
		if history[i].Role != "tool" && history[i].Role != "assistant" {
			continue
		}
		if len(history[i].Content) <= trimBodyFloor {
			continue
		}
		if idx < 0 || len(history[i].Content) > len(history[idx].Content) {
			idx = i
		}
	}
	return idx
}

func largestShrinkableModelMessage(history []ModelMessage) int {
	idx := -1
	for i := range history {
		if history[i].Role != "tool" && history[i].Role != "assistant" {
			continue
		}
		if len(history[i].Content) <= trimBodyFloor {
			continue
		}
		if idx < 0 || len(history[i].Content) > len(history[idx].Content) {
			idx = i
		}
	}
	return idx
}

// truncateRunes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	if n < 0 {
		return ""
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// inputBudget returns the tokens available to the message list after reserving
// the completion and a safety margin for estimation error / framing overhead.
func (e *Engine) inputBudget() int {
	window := e.config.MaxContextTokens
	if window <= 0 {
		return 0
	}
	b := window - e.maxOutputTokens() - window/20
	if b < 0 {
		return 0
	}
	return b
}

// historyTokenBudget returns the share of the input budget left for history
// after the fixed prompt blocks and the tool schemas, which are sent with every
// request but are not part of history.
func (e *Engine) historyTokenBudget(tools []ModelTool) int {
	if e.context == nil {
		return 0
	}
	b := e.inputBudget()
	if b <= 0 {
		return 0
	}
	b -= promptBlocksTokens(e.context.EstimateTokens, e.context.InjectedBlocks())
	b -= estimateToolSpecTokens(e.context.EstimateTokens, tools)
	if b < 0 {
		return 0
	}
	return b
}

// guardContextBudget is the last-resort pre-send guard: compression normally
// keeps the request far below the window, but when it cannot (failure cooldown,
// or a single turn's tool batch grew faster than the per-turn check) the oldest
// history is dropped now so the request cannot exceed the model window.
func (e *Engine) guardContextBudget(tools []ModelTool) {
	if e.compressor == nil || e.context == nil || e.config.MaxContextTokens <= 0 {
		return
	}
	budget := e.historyTokenBudget(tools)
	before := estimateHistoryTokens(e.history)
	if budget <= 0 || before <= budget {
		return
	}
	trimmed := trimHistoryToBudget(e.history, budget)
	// Compare token estimates, not lengths: the guard may only truncate the
	// largest bodies (message count unchanged) when a single turn's tool output
	// is what pushed the request over.
	if after := estimateHistoryTokens(trimmed); after < before {
		turnLog.Printf("context guard: history %d -> %d messages, %d -> %d tokens (budget=%d)",
			len(e.history), len(trimmed), before, after, budget)
		e.history = trimmed
	}
}

// forceTrimHistory is the reactive counterpart of guardContextBudget: a provider
// 400 proved the estimate was too low, so trim to half the budget for headroom.
func (e *Engine) forceTrimHistory(tools []ModelTool) {
	if e.compressor == nil || e.context == nil {
		return
	}
	budget := e.historyTokenBudget(tools)
	if budget <= 0 {
		return
	}
	e.history = trimHistoryToBudget(e.history, budget/2)
}

// inputBudget returns the sub-agent's usable input budget: its context window
// minus the reserved completion and a safety margin.
func (r *SubAgentRunner) inputBudget() int {
	limit := r.contextLimit()
	if limit <= 0 {
		return 0
	}
	b := limit - r.outputTokenCap() - limit/20
	if b < 0 {
		return 0
	}
	return b
}

// historyTokenBudget is the sub-agent counterpart of Engine.historyTokenBudget.
// Its history starts with the system prompt, so only the tool schemas need to be
// subtracted separately.
func (r *SubAgentRunner) historyTokenBudget(tools []ModelTool) int {
	b := r.inputBudget()
	if b <= 0 {
		return 0
	}
	var est func([]ModelMessage) int
	if r.compressor != nil {
		est = r.compressor.EstimateTokens
	}
	b -= estimateToolSpecTokens(est, tools)
	if b < 0 {
		return 0
	}
	return b
}
