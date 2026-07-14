package engine

import (
	"context"
	"fmt"
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
	Goal               string // current Run's user goal (e.state.Goal) for LLM judgment
	ToolCallSummary    string // brief summary of tools called this Run() (e.g. "grep×3, read×2")
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
	Check(ctx context.Context, sc StopHookContext) StopHookResult
}

// ZeroToolCallHook blocks loop exit when the model has not called any tools
// this Run(). A text-only response with zero prior tool calls cannot be a
// final conclusion - the model is narrating intent without acting.
// Blocks up to MaxRetries times (default 3), then allows exit.
type ZeroToolCallHook struct {
	MaxRetries int
}

func (h *ZeroToolCallHook) Check(_ context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount > 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if sc.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	msg := "请直接使用工具执行下一步，完成目标后给出最终结论。不要只描述计划。"
	if !sc.IsChinese {
		msg = "Use tools to take the next action. Complete the goal and give your final conclusions. Do not just describe a plan."
	}
	if sc.StopHookRetryCount > 0 && sc.LastContent != "" {
		snippet := truncateStr(sc.LastContent, 60)
		msg = fmt.Sprintf("你刚才说了\"%s\"却没有执行。请立即调用工具执行这个操作，不要再描述计划。", snippet)
		if !sc.IsChinese {
			msg = fmt.Sprintf("You said \"%s\" but didn't act on it. Call a tool to perform this now - don't just describe the plan.", snippet)
		}
	}
	return StopHookResult{Block: true, Message: msg, Reason: "zero_tool_calls"}
}

// StalledNarrationHook blocks loop exit when the model, after already calling
// tools this Run() (RunToolCallCount > 0), emits a text-only response. It uses
// an LLM ConclusionJudge to decide whether the text is a final conclusion;
// non-conclusions are nudged to continue. ZeroToolCallHook covers the
// RunToolCallCount == 0 case; the two are mutually exclusive per turn. Blocks
// up to MaxRetries times (default 2), then signals Exhausted so the caller
// returns Blocked instead of mistaking narration for completion. The shared
// counter resets on any tool call.
type StalledNarrationHook struct {
	MaxRetries int
	Classifier ConclusionJudge
}

func (h *StalledNarrationHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount == 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	if sc.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	// Heuristic pre-check: if the text ends with a clear next-step intention
	// (e.g., "让我精读这些关键区域。"), block without calling the LLM
	// classifier. This catches obvious intermediate narration that the
	// flash-model classifier might miss (false positive on partial answers
	// that look like they address the goal).
	// Hard guard: future-intent markers (需要/接下来/将/准备...) hard-classify
	// text as intermediate, BEFORE the classifier is called. Catches declarative
	// intermediate findings ("综上，需要在 turn.go 加入校验") that slip past
	// hasTrailingNextStepIntent (no leading action verb) and would otherwise
	// hit the classifier - which may false-positive or fail outright.
	if hasFutureIntent(sc.LastContent) {
		turnLog.Printf("stop hook: future-intent marker detected, blocking without classifier")
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "future_intent"}
	}
	if hasTrailingNextStepIntent(sc.LastContent) {
		turnLog.Printf("stop hook heuristic: next-step intent detected, blocking without classifier")
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "heuristic_next_step"}
	}
	// Defensive: a StalledNarrationHook registered without a Classifier
	// (nil ConclusionJudge) must not crash the loop. Skip the check and let
	// the turn terminate normally; the wiring bug is surfaced via the log.
	if h.Classifier == nil {
		turnLog.Printf("StalledNarrationHook: nil Classifier (wiring bug), skipping stop-hook check")
		return StopHookResult{}
	}
	isConclusion, err := h.Classifier.IsConclusion(ctx, ConclusionCheck{
		Goal:            sc.Goal,
		Text:            sc.LastContent,
		ToolCallSummary: sc.ToolCallSummary,
	})
	turnLog.Printf("stop hook classifier: conclusion=%v err=%v retry=%d marker=%v content=%.60s",
		isConclusion, err, sc.StopHookRetryCount, hasCompletionMarker(sc.LastContent), sc.LastContent)
	if err != nil {
		turnLog.Printf("conclusion classifier error: %v (falling back to completion-marker check)", err)
		// Classifier unavailable: fall back to the deterministic completion-marker
		// check rather than always blocking. A text with a strong completion
		// marker is allowed to exit; otherwise block conservatively.
		if hasCompletionMarker(sc.LastContent) {
			turnLog.Printf("stop hook: allow exit (classifier error + completion marker)")
			return StopHookResult{}
		}
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "classifier_error"}
	}
	if isConclusion {
		// Conservative guard against flash-classifier false positives on
		// declarative intermediate findings ("问题出在 X，建议 Y") that read
		// as partial answers. Trust the verdict only when the text carries an
		// explicit completion/summary marker, OR when we have already nudged
		// once this stall (retry > 0) - giving the model a second chance to
		// either restate clearly or resume acting. Otherwise block one more
		// round so a partial answer is not mistaken for completion.
		if hasCompletionMarker(sc.LastContent) {
			turnLog.Printf("stop hook: allow exit (classifier=true + completion marker)")
			return StopHookResult{}
		}
		if sc.StopHookRetryCount > 0 {
			turnLog.Printf("stop hook: allow exit (classifier=true + retry>0 second chance)")
			return StopHookResult{}
		}
		turnLog.Printf("stop hook: classifier verdict unconfirmed (no completion marker), blocking conservatively")
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "classifier_unconfirmed"}
	}
	return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "stalled_narration"}
}

// stalledNudgeMsg builds the bilingual nudge; on retry it quotes a snippet of
// the model's own words to make the nudge concrete.
func stalledNudgeMsg(sc StopHookContext) string {
	msg := "你在描述下一步却没有实际执行。请直接调用工具继续执行，不要只描述计划；全部完成后再给出最终结论。"
	if !sc.IsChinese {
		msg = "You described the next step without doing it. Call a tool to perform it now - don't just describe a plan - then give your final conclusions once the goal is complete."
	}
	if sc.StopHookRetryCount > 0 && sc.LastContent != "" {
		snippet := truncateStr(sc.LastContent, 60)
		msg = fmt.Sprintf("你又描述了下一步\"%s\"却仍未执行。请立即调用工具，不要再叙述计划。", snippet)
		if !sc.IsChinese {
			msg = fmt.Sprintf("You again described a step (\"%s\") without doing it. Call a tool now - stop narrating and act.", snippet)
		}
	}
	return msg
}

// SetStopHooks registers stop hooks checked when the model outputs text
// without tool calls. A blocking hook injects a nudge message and continues
// the agent loop instead of terminating.
func (e *Engine) SetStopHooks(hooks []StopHook) {
	e.stopHooks = hooks
}

// NewConclusionClassifier constructs a ConclusionClassifier bound to the
// engine's model, flash model name, and language preference. Used by callers
// (e.g. cmd/exec.go) to wire StalledNarrationHook without exposing e.model.
func (e *Engine) NewConclusionClassifier() *ConclusionClassifier {
	return NewConclusionClassifier(e.model, e.config.FlashModelName, e.isChinese)
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
		if result.Exhausted {
			exhausted = true
		}
	}
	return StopHookResult{Exhausted: exhausted}
}
