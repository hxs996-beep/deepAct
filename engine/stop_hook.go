package engine

import (
	"context"
)

// StopHookContext carries the context a stop hook needs to decide whether
// the model's text-only response should end the loop or be nudged to continue.
type StopHookContext struct {
	RunToolCallCount   int    // total tool calls in this Run() so far
	LastContent        string // model's last text output
	FinishReason       string // finish reason (stop/length/etc)
	StopHookActive     bool   // true if this turn was triggered by a prior hook block
	StopHookRetryCount int    // consecutive hook-triggered continuations
	IsChinese          bool   // language preference for nudge message
	Goal               string // current Run's user goal (e.state.Goal)
	ToolCallSummary    string // brief summary of tools called this Run() (e.g. "grep×3, read×2")
	AnalysisMode       bool   // true when user intent is analysis-only; text output IS the report
}

// StopHookResult is what a stop hook returns.
type StopHookResult struct {
	Block     bool   // if true, inject Message and continue the loop
	Exhausted bool   // true if this hook didn't block because MaxRetries was reached
	Message   string // nudge message injected as a user message (when Block=true)
	Reason    string // block reason (for logging)
	// AwaitUser is true when the model's text-only reply is a question to
	// the user. The caller must stop the loop and present the reply as a
	// question — the model must never decide on the user's behalf.
	AwaitUser bool
}

// StopHook is checked when the model outputs text without tool calls.
// A blocking hook injects a nudge message and continues the agent loop
// instead of terminating. Modeled after Claude Code's stop hooks pattern.
// The framework is kept as an extension point: no built-in hooks are
// registered by default — a text-only reply (no tool calls) ends the turn
// directly (dsh-structured completion). External callers may register
// custom hooks via SetStopHooks.
type StopHook interface {
	Check(ctx context.Context, sc StopHookContext) StopHookResult
}

// SetStopHooks registers stop hooks checked when the model outputs text
// without tool calls. A blocking hook injects a nudge message and continues
// the agent loop instead of terminating. Empty (default) means every
// text-only reply ends the turn.
func (e *Engine) SetStopHooks(hooks []StopHook) {
	e.stopHooks = hooks
}

// runStopHooks executes registered stop hooks and returns the first blocking
// result. If no hook blocks, returns an empty result (loop may terminate).
func (e *Engine) runStopHooks(ctx context.Context, sc StopHookContext) StopHookResult {
	exhausted := false
	for _, hook := range e.stopHooks {
		result := hook.Check(ctx, sc)
		if result.Block {
			return result
		}
		// AwaitUser takes priority over continuing to later hooks: a question
		// to the user must stop the loop, not be swallowed by a later hook.
		if result.AwaitUser {
			return result
		}
		if result.Exhausted {
			exhausted = true
		}
	}
	return StopHookResult{Exhausted: exhausted}
}
