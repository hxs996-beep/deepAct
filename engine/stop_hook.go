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
type StopHook interface {
	Check(ctx context.Context, sc StopHookContext) StopHookResult
}

// ZeroToolCallHook blocks loop exit when the model has not called any tools
// this Run(). A text-only response with zero prior tool calls cannot be a
// final conclusion - the model is narrating intent without acting.
// Blocks up to MaxRetries times (default 3), then allows exit.
// Verdict is a three-way judge (conclusion/question/intermediate):
//   - VerdictQuestion returns AwaitUser=true instead of nudging — the model
//     is asking the user a decision and the loop must stop and wait. Takes
//     priority over everything, including AnalysisMode.
//   - VerdictConclusion allows exit: a clear final answer with zero tool
//     calls (e.g. a pure-reasoning answer) is complete and must not be
//     nudged into acting.
//   - VerdictIntermediate falls through to the nudge below, unless
//     AnalysisMode is set (text-only output IS the report — allow exit).
type ZeroToolCallHook struct {
	MaxRetries int
	Verdict    VerdictJudge
}

func (h *ZeroToolCallHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount > 0 {
		return StopHookResult{}
	}
	// A question to the user takes priority over nudging: the model must
	// never decide on the user's behalf. Verdict judge is semantic (LLM),
	// keyword-free — missed questions are destructive, so we never guess
	// from keywords here.
	if h.Verdict != nil {
		v, err := h.Verdict.Classify(ctx, ConclusionCheck{
			Goal: sc.Goal,
			Text: sc.LastContent,
		})
		if err == nil {
			switch v {
			case VerdictQuestion:
				turnLog.Printf("zero-tool-call hook: question detected, awaiting user (retry=%d)", sc.StopHookRetryCount)
				return StopHookResult{AwaitUser: true, Reason: "question_to_user"}
			case VerdictConclusion:
				// A clear conclusion with zero tool calls (pure-reasoning
				// answer) is complete — nudging would block a full answer.
				return StopHookResult{}
			}
			// VerdictIntermediate. In analysis mode, text-only output IS the
			// report — allow exit without nudging (mirrors StalledNarrationHook).
			if sc.AnalysisMode {
				return StopHookResult{}
			}
		}
	}
	// No Verdict wired, verdict error, or intermediate non-analysis:
	// in analysis mode, text-only output is the report — allow exit.
	if sc.AnalysisMode {
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
	// Verdict is the semantic three-way judge (conclusion/question/
	// intermediate). When set, it takes priority over the binary Classifier:
	// a question verdict returns AwaitUser=true (stop and wait for the user)
	// instead of nudging the model to continue. On judge error, falls back
	// to the binary Classifier (existing behavior).
	Verdict VerdictJudge
}

func (h *StalledNarrationHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount == 0 {
		return StopHookResult{}
	}
	// A question to the user takes priority over everything, including
	// AnalysisMode: the model must never decide on the user's behalf.
	if h.Verdict != nil {
		v, err := h.Verdict.Classify(ctx, ConclusionCheck{
			Goal:            sc.Goal,
			Text:            sc.LastContent,
			ToolCallSummary: sc.ToolCallSummary,
		})
		if err == nil {
			switch v {
			case VerdictQuestion:
				turnLog.Printf("stalled-narration hook: question detected, awaiting user (retry=%d)", sc.StopHookRetryCount)
				return StopHookResult{AwaitUser: true, Reason: "question_to_user"}
			case VerdictConclusion:
				return StopHookResult{}
			}
			// VerdictIntermediate. In analysis mode, partial/intermediate text
			// is still part of the report — allow exit without nudging. Outside
			// analysis mode, nudge to continue (same as the binary classifier's
			// "not a conclusion" branch below).
			if sc.AnalysisMode {
				return StopHookResult{}
			}
			maxRetries := h.MaxRetries
			if maxRetries <= 0 {
				maxRetries = 2
			}
			if sc.StopHookRetryCount >= maxRetries {
				return StopHookResult{Exhausted: true}
			}
			return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "stalled_narration"}
		}
		turnLog.Printf("stalled-narration hook: verdict judge error %v (falling back to binary classifier)", err)
	}
	// Analysis mode: text-only output after tool calls is the analysis report.
	// Question detection already ran above (Verdict judge), so reaching here
	// means the reply is not a question. Allow exit without content inspection.
	if sc.AnalysisMode {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	if sc.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	// Defensive: a StalledNarrationHook registered without a Classifier
	// (nil ConclusionJudge) must not crash the loop. Skip the check and let
	// the turn terminate normally; the wiring bug is surfaced via the log.
	if h.Classifier == nil {
		turnLog.Printf("StalledNarrationHook: nil Classifier (wiring bug), skipping stop-hook check")
		return StopHookResult{}
	}
	// Deterministic pre-check before the binary LLM call (fall-back path:
	// no Verdict wired, or the Verdict judge errored above). A clear
	// next-step plan ("让我/接下来/深入读取...") is never a conclusion —
	// block without the cost and latency of a flash-model call. Question
	// detection already ran in the Verdict branch above, so a hit here is
	// intermediate narration, never a user question.
	if hasTrailingNextStepIntent(sc.LastContent) {
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "stalled_narration"}
	}
	isConclusion, err := h.Classifier.IsConclusion(ctx, ConclusionCheck{
		Goal:            sc.Goal,
		Text:            sc.LastContent,
		ToolCallSummary: sc.ToolCallSummary,
	})
	turnLog.Printf("stop hook classifier: conclusion=%v err=%v retry=%d content=%.60s",
		isConclusion, err, sc.StopHookRetryCount, sc.LastContent)
	if err != nil {
		// Classifier unavailable: allow exit rather than blocking. Blocking on
		// classifier failure forces the agent to continue when it should stop,
		// causing more harm (e.g. implementing without user approval) than a
		// potentially premature exit (user can simply say "continue").
		turnLog.Printf("conclusion classifier error: %v (allowing exit)", err)
		return StopHookResult{}
	}
	if isConclusion {
		return StopHookResult{}
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
// the agent loop instead of terminating. Also extracts the VerdictJudge from
// registered hooks so the tool branch's self-answering guard can reuse the
// same semantic question detector (e.verdictJudge).
func (e *Engine) SetStopHooks(hooks []StopHook) {
	e.stopHooks = hooks
	for _, h := range hooks {
		switch v := h.(type) {
		case *ZeroToolCallHook:
			if v.Verdict != nil {
				e.verdictJudge = v.Verdict
			}
		case *StalledNarrationHook:
			if v.Verdict != nil {
				e.verdictJudge = v.Verdict
			}
		}
	}
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
