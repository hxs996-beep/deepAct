package engine

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
	Block   bool   // if true, inject Message and continue the loop
	Message string // nudge message injected as a user message (when Block=true)
	Reason  string // block reason (for logging)
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
		return StopHookResult{}
	}
	msg := "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	if !ctx.IsChinese {
		msg = "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
	}
	return StopHookResult{Block: true, Message: msg, Reason: "zero_tool_calls"}
}
