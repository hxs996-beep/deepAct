package engine

import "fmt"

// StopHookContext carries the context a stop hook needs to decide whether
// the model's text-only response should end the loop or be nudged to continue.
type StopHookContext struct {
	RunToolCallCount   int    // total tool calls in this Run() so far
	LastContent        string // model's last text output
	FinishReason       string // finish reason (stop/length/etc)
	StopHookActive     bool   // true if this turn was triggered by a prior hook block
	StopHookRetryCount int    // consecutive hook-triggered continuations
	IsChinese          bool   // language preference for nudge message
}

// StopHookResult is what a stop hook returns.
type StopHookResult struct {
	Block     bool   // if true, inject Message and continue the loop
	Exhausted bool   // true if this hook didn't block because MaxRetries was reached
	Message   string // nudge message injected as a user message (when Block=true)
	Reason    string // block reason (for logging)
}

// StopHook is checked when the model outputs text without tool calls.
// A blocking hook injects a nudge message and continues the agent loop
// instead of terminating. Modeled after Claude Code's stop hooks pattern.
type StopHook interface {
	Check(ctx StopHookContext) StopHookResult
}

// ZeroToolCallHook blocks loop exit when the model has not called any tools
// this Run(). A text-only response with zero prior tool calls cannot be a
// final conclusion — the model is narrating intent without acting.
// Blocks up to MaxRetries times (default 3), then allows exit.
type ZeroToolCallHook struct {
	MaxRetries int
}

func (h *ZeroToolCallHook) Check(ctx StopHookContext) StopHookResult {
	if ctx.RunToolCallCount > 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if ctx.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	msg := "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	if !ctx.IsChinese {
		msg = "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
	}
	if ctx.StopHookRetryCount > 0 && ctx.LastContent != "" {
		snippet := truncateStr(ctx.LastContent, 60)
		msg = fmt.Sprintf("你刚才说了\"%s\"却没有执行。请立即调用工具执行这个操作，不要再描述计划。", snippet)
		if !ctx.IsChinese {
			msg = fmt.Sprintf("You said \"%s\" but didn't act on it. Call a tool to perform this now - don't just describe the plan.", snippet)
		}
	}
	return StopHookResult{Block: true, Message: msg, Reason: "zero_tool_calls"}
}

// StalledNarrationHook blocks loop exit when the model, after already calling
// tools this Run() (RunToolCallCount > 0), emits a text-only response that
// reads as a forward-looking next-step plan rather than a final conclusion —
// the mid-task stall. ZeroToolCallHook covers the RunToolCallCount == 0 case;
// the two are mutually exclusive per turn. Blocks up to MaxRetries times
// (default 2), then allows exit, so a misread conclusion costs at most a few
// nudges and the shared counter resets on any tool call.
type StalledNarrationHook struct {
	MaxRetries int
}

func (h *StalledNarrationHook) Check(ctx StopHookContext) StopHookResult {
	if ctx.RunToolCallCount == 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	if ctx.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	if !looksLikeNextStepNarration(ctx.LastContent) {
		return StopHookResult{}
	}
	msg := "你在描述下一步却没有实际执行。请直接调用工具继续执行，不要只描述计划；全部完成后再给出最终结论。"
	if !ctx.IsChinese {
		msg = "You described the next step without doing it. Call a tool to perform it now — don't just describe a plan — then give your final conclusions once the goal is complete."
	}
	if ctx.StopHookRetryCount > 0 && ctx.LastContent != "" {
		snippet := truncateStr(ctx.LastContent, 60)
		msg = fmt.Sprintf("你又描述了下一步\"%s\"却仍未执行。请立即调用工具，不要再叙述计划。", snippet)
		if !ctx.IsChinese {
			msg = fmt.Sprintf("You again described a step (\"%s\") without doing it. Call a tool now - stop narrating and act.", snippet)
		}
	}
	return StopHookResult{Block: true, Message: msg, Reason: "stalled_narration"}
}

// SetStopHooks registers stop hooks checked when the model outputs text
// without tool calls. A blocking hook injects a nudge message and continues
// the agent loop instead of terminating.
func (e *Engine) SetStopHooks(hooks []StopHook) {
	e.stopHooks = hooks
}

// runStopHooks executes registered stop hooks and returns the first blocking
// result. If no hook blocks, returns an empty result (loop may terminate).
func (e *Engine) runStopHooks(ctx StopHookContext) StopHookResult {
	exhausted := false
	for _, hook := range e.stopHooks {
		result := hook.Check(ctx)
		if result.Block {
			return result
		}
		if result.Exhausted {
			exhausted = true
		}
	}
	return StopHookResult{Exhausted: exhausted}
}
