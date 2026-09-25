package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Token-budget gate tests. The budget counts CacheMissTokens +
// CompletionTokens — cache hits are free — and is checked every turn right
// after usage accumulation. The Miss-only hard-stop test locks the
// CacheMissTokens accumulation itself: if the accumulation line is ever
// dropped, spent degenerates to CompletionTokens and that test goes red.

// budgetTurnResponse builds a scripted response that keeps the run looping
// with a read tool call while consuming the given usage per turn.
func budgetTurnResponse(miss, completion, hit int) ModelResponse {
	return ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "r", Type: "function",
			Function: ModelFunctionCall{Name: "read", Arguments: `{"path":"x.go"}`},
		}}},
		FinishReason: "tool_calls",
		Usage:        ModelUsage{CacheMissTokens: miss, CompletionTokens: completion, CacheHitTokens: hit, TotalTokens: miss + completion + hit},
	}
}

func budgetRunner(model ModelClient, exec ToolExecutor) *SubAgentRunner {
	return &SubAgentRunner{model: model, tools: exec, modelName: "test"}
}

func readSpecExec() *scriptToolExecutor {
	return &scriptToolExecutor{specs: []ModelTool{specNamed("read")}}
}

// TestSubAgentBudget_HardStopCountsMissOnly: a run whose every turn burns
// 400 cache-miss tokens against budget=1000 stops on the 3rd turn with
// reason=budget_exceeded and a partial summary. Miss-only: CompletionTokens
// is zero throughout — if CacheMissTokens were not accumulated, spent would
// stay 0 and this test would never terminate at the budget.
func TestSubAgentBudget_HardStopCountsMissOnly(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(400, 0, 0)}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: 1000, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonBudgetExceeded {
		t.Fatalf("expected %q, got %q", HandoffReasonBudgetExceeded, result.FinishReason)
	}
	if model.calls != 3 {
		t.Errorf("expected stop at the 3rd turn (400+400+400 >= 1000), got %d calls", model.calls)
	}
	if result.Summary == "" {
		t.Error("expected a partial summary from the accumulated history")
	}
	if result.Usage == nil || result.Usage.CacheMissTokens != 1200 {
		t.Errorf("expected Usage.CacheMissTokens=1200 on the result, got %+v", result.Usage)
	}
}

// TestSubAgentBudget_NudgeOnceBeforeStop: crossing 80% injects the wrap-up
// nudge exactly once, before the hard stop, and the nudge names the remaining
// amount (information the model can act on).
func TestSubAgentBudget_NudgeOnceBeforeStop(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(400, 0, 0)}}
	runner := budgetRunner(model, readSpecExec())

	_, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: 1000, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	nudges := 0
	nudgeIdx, lastToolIdx := -1, -1
	for i, msg := range model.lastReq.Messages {
		if msg.Role == "tool" {
			lastToolIdx = i
		}
		if strings.Contains(msg.Content, "预算只剩") {
			nudges++
			nudgeIdx = i
			if !strings.Contains(msg.Content, "200") {
				t.Errorf("nudge must name the remaining amount (200), got %q", msg.Content)
			}
		}
	}
	// lastReq is the 3rd request: 80% was crossed after turn 2 (800 >= 800),
	// so exactly one nudge is visible there.
	if nudges != 1 {
		t.Errorf("expected exactly 1 nudge in the final request, got %d", nudges)
	}
	// It must come after turn 2's tool results — never interleaved between the
	// assistant tool_calls message and its tool response (the API requires tool
	// responses to follow the message that requested them).
	if nudgeIdx < lastToolIdx {
		t.Errorf("budget nudge (msg %d) must not precede the last tool result (msg %d)", nudgeIdx, lastToolIdx)
	}
}

// tokenNudgeProbeModel burns 100 cache-miss tokens per turn with read calls
// (varying paths, so per-file loop detection never fires) until the token-budget
// wrap-up nudge appears, then submits a structured result. It models a
// structured run that must converge through submit_result — plain text never
// completes it.
type tokenNudgeProbeModel struct {
	calls   int
	nudged  bool
	lastReq ModelRequest
}

func (m *tokenNudgeProbeModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *tokenNudgeProbeModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.calls++
	m.lastReq = req
	if !m.nudged {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "预算只剩") {
				m.nudged = true
				break
			}
		}
	}
	usage := ModelUsage{CacheMissTokens: 100, TotalTokens: 100}
	if m.nudged && toolsContain(req.Tools, SubmitResultToolName) {
		return &ModelResponse{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName, Arguments: `{"summary":"预算提示后收敛。"}`},
			}}},
			FinishReason: "tool_calls",
			Usage:        usage,
		}, nil
	}
	return &ModelResponse{
		Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
			ID: "c1", Type: "function",
			Function: ModelFunctionCall{Name: "read", Arguments: fmt.Sprintf(`{"path":"x%d.go"}`, m.calls)},
		}}},
		FinishReason: "tool_calls",
		Usage:        usage,
	}, nil
}

// TestSubAgentBudget_NudgeStructuredNamesSubmitResult: the 80% nudge of a
// structured run must direct the model to submit_result. Without it the model
// replies with plain text, which never completes a structured run — burning a
// turn (or three, via the submit_result nudge streak) out of the budget the
// guard just told it to wrap up within.
func TestSubAgentBudget_NudgeStructuredNamesSubmitResult(t *testing.T) {
	model := &tokenNudgeProbeModel{}
	runner := budgetRunner(model, readSpecExec())

	// 100 tokens per turn, budget 1000: the nudge is armed at 800 (turn 8) and
	// injected into turn 9's request, so the submission lands at 900 — below the
	// cap. (Right at the cap the hard stop returns before the submit branch.)
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: 1000, StructuredResult: true, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Fatalf("expected the run to converge through submit_result, got %q", result.FinishReason)
	}
	found := false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, "预算只剩") && strings.Contains(msg.Content, "submit_result") {
			found = true
		}
	}
	if !found {
		t.Errorf("structured token-budget nudge must name submit_result, last request messages: %q", model.lastReq.Messages)
	}
}

// TestSubAgentBudget_CompletionTokensCount: with zero cache-miss, the
// completion tokens alone drive the budget.
func TestSubAgentBudget_CompletionTokensCount(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(0, 600, 0)}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: 1000, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonBudgetExceeded {
		t.Fatalf("expected %q at 600+600 >= 1000, got %q", HandoffReasonBudgetExceeded, result.FinishReason)
	}
	if model.calls != 2 {
		t.Errorf("expected stop at the 2nd turn, got %d calls", model.calls)
	}
}

// TestSubAgentBudget_CacheHitsFree: 100k cache-hit tokens per turn must not
// count toward the budget; only the 10 miss tokens do. With budget=15 the
// run stops on turn 2 (20 >= 15) — if hits were counted, it would stop on
// turn 1.
func TestSubAgentBudget_CacheHitsFree(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(10, 0, 100000)}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: 15, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonBudgetExceeded {
		t.Fatalf("expected %q, got %q", HandoffReasonBudgetExceeded, result.FinishReason)
	}
	if model.calls != 2 {
		t.Errorf("hits must be free: stop at turn 2 (10+10>=15), got %d calls", model.calls)
	}
}

// TestSubAgentBudget_ThreeStateResolution: tokenBudgetFor resolves the
// three-state semantics at every level — 0 inherits downward, -1 is explicit
// unlimited (never falls through to a default), >0 is an explicit cap.
func TestSubAgentBudget_ThreeStateResolution(t *testing.T) {
	// Handoff level: explicit values win; -1 means unlimited (0 = no check).
	runner := &SubAgentRunner{modelName: "test"}
	if got := tokenBudgetFor(Handoff{TokenBudget: 500}, runner); got != 500 {
		t.Errorf("explicit handoff budget: got %d, want 500", got)
	}
	if got := tokenBudgetFor(Handoff{TokenBudget: -1}, runner); got != 0 {
		t.Errorf("handoff -1 must resolve to unlimited (0), got %d", got)
	}
	// Runner level: same three states when the handoff inherits (0).
	if got := tokenBudgetFor(Handoff{}, &SubAgentRunner{tokenBudget: -1}); got != 0 {
		t.Errorf("runner -1 must resolve to unlimited (0), got %d", got)
	}
	if got := tokenBudgetFor(Handoff{}, &SubAgentRunner{tokenBudget: 700}); got != 700 {
		t.Errorf("runner explicit: got %d, want 700", got)
	}
	// Default: 2× the effective context window.
	r := &SubAgentRunner{maxContextTokens: 100}
	if got := tokenBudgetFor(Handoff{}, r); got != 200 {
		t.Errorf("default budget must be 2× context window, got %d, want 200", got)
	}
}

// TestSubAgentBudget_SpecMerge: specSubAgent merges the role budget into the
// handoff (0 = not set), and an explicit handoff value — including -1 — is
// never overwritten by the spec.
func TestSubAgentBudget_SpecMerge(t *testing.T) {
	spec := AgentSpec{ID: AgentSub, TokenBudget: 500}

	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: spec}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if capture.input.TokenBudget != 500 {
		t.Errorf("spec budget must merge into handoff, got %d", capture.input.TokenBudget)
	}

	capture = &captureSubRunner{}
	a = &specSubAgent{runner: capture, spec: spec}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", TokenBudget: -1}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if capture.input.TokenBudget != -1 {
		t.Errorf("handoff -1 must survive the spec merge, got %d", capture.input.TokenBudget)
	}

	// A spec-level -1 flows into the handoff unchanged (explicit unlimited
	// can never be silently replaced by a default).
	capture = &captureSubRunner{}
	a = &specSubAgent{runner: capture, spec: AgentSpec{ID: AgentSub, TokenBudget: -1}}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if capture.input.TokenBudget != -1 {
		t.Errorf("spec -1 must merge as -1, got %d", capture.input.TokenBudget)
	}
}

// TestSubAgentBudget_ExplicitUnlimitedRunsPastBudget: Handoff{TokenBudget:-1}
// must disable the check end-to-end — a run burning 1e9 miss tokens per turn
// still ends via max_iterations, never budget_exceeded. Locks the N1 fix: a
// ">0 only" resolution would silently re-enable the runner default.
func TestSubAgentBudget_ExplicitUnlimitedRunsPastBudget(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(1_000_000_000, 0, 0)}}
	runner := budgetRunner(model, readSpecExec())
	runner.SetSubAgentTokenBudget(500)

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", TokenBudget: -1, MaxIterations: 3, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason == HandoffReasonBudgetExceeded {
		t.Fatal("TokenBudget:-1 is explicit unlimited; the budget gate must not fire")
	}
	if result.FinishReason != HandoffReasonMaxIterations {
		t.Fatalf("expected max_iterations, got %q", result.FinishReason)
	}
}

// TestSubAgentBudget_DigestHeading: the budget_exceeded digest is
// reason-aware — budget wording, never "completed".
func TestSubAgentBudget_DigestHeading(t *testing.T) {
	digest := formatHandoffResult(&HandoffResult{
		Summary: "部分发现", FinishReason: HandoffReasonBudgetExceeded,
	}, true)
	if !strings.Contains(digest, "超出 token 预算") {
		t.Errorf("digest must carry the budget heading, got %q", digest)
	}
	if strings.Contains(digest, "完成") && strings.Contains(digest, "子代理完成任务") {
		t.Errorf("budget_exceeded digest must not present as completed, got %q", digest)
	}
}

// TestSubAgentBudget_FollowUpPinned: budget_exceeded is a failure reason —
// the parent pins the auto-continue follow-up (exclusion-table semantics).
func TestSubAgentBudget_FollowUpPinned(t *testing.T) {
	if !isHandoffFollowUpReason(HandoffReasonBudgetExceeded) {
		t.Error("budget_exceeded must be a follow-up reason (partial findings + re-delegate)")
	}
}

// TestSubAgentBudget_MaxIterationsCarriesUsage: the max-iterations fallback
// must carry the accumulated usage — the parent's cost accounting must not
// lose the run's last segment.
func TestSubAgentBudget_MaxIterationsCarriesUsage(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(10, 20, 0)}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", MaxIterations: 3, UserLanguage: "中文",
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonMaxIterations {
		t.Fatalf("expected max_iterations, got %q", result.FinishReason)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 90 {
		t.Errorf("expected Usage.TotalTokens=90 (3×30) on the fallback path, got %+v", result.Usage)
	}
}

// TestSubAgentBudget_ConcurrentRuns: parallel runs share one runner; the
// budget resolution must be race-free (runner fields are read-only during a
// run). Run under -race.
func TestSubAgentBudget_ConcurrentRuns(t *testing.T) {
	exec := readSpecExec()
	runner := budgetRunner(&threadSafeSubmitModel{}, exec)
	runner.SetSubAgentTokenBudget(10_000)

	const n = 8
	var wg sync.WaitGroup
	reasons := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := runner.Run(context.Background(), Handoff{
				Agent: AgentSub, Goal: "调研", StructuredResult: true, UserLanguage: "中文",
			})
			if err != nil {
				reasons[i] = "error: " + err.Error()
				return
			}
			reasons[i] = result.FinishReason
		}(i)
	}
	wg.Wait()
	for i, r := range reasons {
		if r != HandoffReasonCompleted {
			t.Errorf("run %d: expected %q, got %q", i, HandoffReasonCompleted, r)
		}
	}
}
