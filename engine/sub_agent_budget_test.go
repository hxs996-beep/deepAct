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

// budgetRunnerFor builds a runner whose engine-computed per-run budget is
// `budget` billable tokens. The budget is no longer settable per role or per
// call: it is derived from the run's own context window (2 × window), so a test
// picks the number by choosing the window.
func budgetRunnerFor(model ModelClient, exec ToolExecutor, budget int) *SubAgentRunner {
	r := budgetRunner(model, exec)
	r.maxContextTokens = budget / 2
	return r
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
	runner := budgetRunnerFor(model, readSpecExec(), 1000)

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研"})
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
	runner := budgetRunnerFor(model, readSpecExec(), 1000)

	_, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研"})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	nudges := 0
	nudgeIdx, lastToolIdx := -1, -1
	for i, msg := range model.lastReq.Messages {
		if msg.Role == "tool" {
			lastToolIdx = i
		}
		if strings.Contains(msg.Content, "tokens remain in this run's budget") {
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
			if strings.Contains(msg.Content, "tokens remain in this run's budget") {
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
	runner := budgetRunnerFor(model, readSpecExec(), 1000)

	// 100 tokens per turn, budget 1000: the nudge is armed at 800 (turn 8) and
	// injected into turn 9's request, so the submission lands at 900 — below the
	// cap. (Right at the cap the hard stop returns before the submit branch.)
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研", StructuredResult: true})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Fatalf("expected the run to converge through submit_result, got %q", result.FinishReason)
	}
	found := false
	for _, msg := range model.lastReq.Messages {
		if strings.Contains(msg.Content, "tokens remain in this run's budget") && strings.Contains(msg.Content, "submit_result") {
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
	runner := budgetRunnerFor(model, readSpecExec(), 1000)

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研"})
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
// count toward the budget; only the 10 miss tokens do. With a 16-token budget
// (a 8-token window) the run stops on turn 2 (20 >= 16) — if hits were counted,
// it would stop on turn 1.
func TestSubAgentBudget_CacheHitsFree(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{budgetTurnResponse(10, 0, 100000)}}
	runner := budgetRunnerFor(model, readSpecExec(), 16)

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研"})
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

// TestSubAgentBudget_DigestHeading: the budget_exceeded digest is
// reason-aware — budget wording, never "completed".
func TestSubAgentBudget_DigestHeading(t *testing.T) {
	digest := formatHandoffResult(&HandoffResult{
		Summary: "部分发现", FinishReason: HandoffReasonBudgetExceeded,
	})
	if !strings.Contains(digest, "exceeded its token budget") {
		t.Errorf("digest must carry the budget heading, got %q", digest)
	}
	if strings.Contains(digest, "Agent completed:") {
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

// TestSubAgentBudget_GuardExitCarriesUsage: a run that ends on a guard instead of
// a submission must carry the accumulated usage — the parent's cost accounting
// must not lose the run's last segment.
func TestSubAgentBudget_GuardExitCarriesUsage(t *testing.T) {
	narration := ModelResponse{
		Message:      ModelMessage{Role: "assistant", Content: "还在调研……"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
	model := &stubSeqModel{responses: []ModelResponse{narration}}
	runner := budgetRunner(model, readSpecExec())

	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentSub, Goal: "调研"})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if result.FinishReason != HandoffReasonStalledNarration {
		t.Fatalf("expected the narration guard (%q), got %q", HandoffReasonStalledNarration, result.FinishReason)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 90 {
		t.Errorf("expected Usage.TotalTokens=90 (3×30) on the guard path, got %+v", result.Usage)
	}
}

// TestSubAgentBudget_ConcurrentRuns: parallel runs share one runner; the
// budget resolution must be race-free (runner fields are read-only during a
// run). Run under -race.
func TestSubAgentBudget_ConcurrentRuns(t *testing.T) {
	exec := readSpecExec()
	runner := budgetRunner(&threadSafeSubmitModel{}, exec)
	runner.maxContextTokens = 5000

	const n = 8
	// Above the delegation-tree default (6) on purpose: this test is about the
	// per-run budget resolution being race-free, so it needs all n runs to be
	// admitted rather than refused by the width cap.
	runner.SetMaxConcurrentSubAgents(n)
	var wg sync.WaitGroup
	reasons := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := runner.Run(context.Background(), Handoff{
				Agent: AgentSub, Goal: "调研", StructuredResult: true})
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
