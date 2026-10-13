package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// NOTE on TextResult in these tests: a text-only reply NEVER completes a
// non-structured run — it is treated as narration and nudged, then ends as
// stalled_narration after 3 strikes (see the completion rules in runLoopFrom).
// These tests are about the delegation-tree invariants, not about completion
// semantics, so every handoff sets TextResult to make the run's outcome
// deterministic and its token math exact.

// A refused delegation is NOT a partial result: nothing ran. These two reasons
// carry their own headings so the parent reads "refused, continue" rather than
// the "partial result" wording every run-that-stopped-halfway reason uses.
func TestHandoffReasonHeading_RefusalReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{HandoffReasonFanoutLimit, "refused"},
		{HandoffReasonTreeBudgetExceeded, "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			got := handoffReasonHeading(tc.reason, "")
			if !strings.Contains(strings.ToLower(got), tc.want) {
				t.Errorf("heading for %q must say %q, got %q", tc.reason, tc.want, got)
			}
			// A refusal must never claim a partial result: that wording tells
			// the parent to salvage an outcome that does not exist.
			if strings.Contains(strings.ToLower(got), "partial") {
				t.Errorf("heading for %q must not say \"partial\", got %q", tc.reason, got)
			}
		})
	}
}

// A refused delegation needs the parent to continue, exactly like a run that
// hit its turn cap — so both reasons must be follow-up reasons.
func TestIsHandoffFollowUpReason_Refusals(t *testing.T) {
	if !isHandoffFollowUpReason(HandoffReasonFanoutLimit) {
		t.Error("fanout_limit must be a follow-up reason")
	}
	if !isHandoffFollowUpReason(HandoffReasonTreeBudgetExceeded) {
		t.Error("tree_budget_exceeded must be a follow-up reason")
	}
}

// --- test scaffolding -------------------------------------------------------

// gateModel blocks inside Complete until released, so a run holds its in-flight
// slot deterministically instead of racing on scheduling.
type gateModel struct {
	release chan struct{}
	entered chan struct{} // one token per Complete entry
}

func (m *gateModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *gateModel) Complete(ctx context.Context, _ ModelRequest) (*ModelResponse, error) {
	m.entered <- struct{}{}
	select {
	case <-m.release:
		return &ModelResponse{Message: ModelMessage{Role: "assistant", Content: "已给出结论。"}, FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// --- width ------------------------------------------------------------------

// TestSubAgentWidth_RefusesBeyondLimit: with the limit held by two runs parked
// inside the model call, a third delegation is REFUSED (not queued) with the
// refusal reason and an educational summary naming the limit.
func TestSubAgentWidth_RefusesBeyondLimit(t *testing.T) {
	model := &gateModel{release: make(chan struct{}), entered: make(chan struct{}, 4)}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetMaxConcurrentSubAgents(2)

	var wg sync.WaitGroup
	admitted := make([]*HandoffResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", TextResult: true})
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			admitted[i] = res
		}(i)
	}
	// Both admitted runs are now INSIDE Complete, i.e. holding their slots.
	<-model.entered
	<-model.entered

	refused, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "third", TextResult: true})
	if err != nil {
		t.Fatalf("refused run returned an error instead of a result: %v", err)
	}
	if refused.FinishReason != HandoffReasonFanoutLimit {
		t.Fatalf("expected %q, got %q", HandoffReasonFanoutLimit, refused.FinishReason)
	}
	if !strings.Contains(refused.Summary, "2") {
		t.Errorf("refusal must name the limit, got %q", refused.Summary)
	}

	close(model.release)
	wg.Wait()
	for i, res := range admitted {
		if res.FinishReason != HandoffReasonCompleted {
			t.Errorf("admitted run %d: got %q, want completed", i, res.FinishReason)
		}
	}
}

// TestSubAgentWidth_SlotReleasedAfterRun: a finished run frees its slot, so the
// next delegation is admitted. Without the deferred release the counter would
// stay at the limit and the second call would be refused.
func TestSubAgentWidth_SlotReleasedAfterRun(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "结论一"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetMaxConcurrentSubAgents(1)

	for i := 0; i < 3; i++ {
		res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", TextResult: true})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("run %d: got %q, want completed (slot not released?)", i, res.FinishReason)
		}
	}
}

// --- depth ------------------------------------------------------------------

// TestSubAgentDepth_Configurable: the nesting cap is configurable and 0 keeps
// the historical default (2), so existing behavior is unchanged unless the user
// opts in.
func TestSubAgentDepth_Configurable(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	if got := runner.MaxDepth(); got != 2 {
		t.Fatalf("default MaxDepth = %d, want 2", got)
	}
	runner.SetMaxDepth(1)
	res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2, TextResult: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinishReason != HandoffReasonMaxDepth {
		t.Fatalf("expected %q at depth 2 with max 1, got %q", HandoffReasonMaxDepth, res.FinishReason)
	}
	runner.SetMaxDepth(3)
	res, err = runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2, TextResult: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinishReason == HandoffReasonMaxDepth {
		t.Fatalf("depth 2 must be allowed when max is 3, got %q", res.FinishReason)
	}
}

// --- Run-level token aggregate ---------------------------------------------

// TestSubAgentTreeBudget_RefusesAfterSpent: the aggregate spans runs, not just
// one run — after the tree has spent its Run budget, the next delegation is
// refused with the refusal reason, and the spend is reported on the runner.
func TestSubAgentTreeBudget_RefusesAfterSpent(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 600, TotalTokens: 600},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(1000)
	runner.StartRun()

	first, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a", TextResult: true})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.FinishReason != HandoffReasonCompleted {
		t.Fatalf("first run: got %q, want completed", first.FinishReason)
	}
	if got := runner.Spent(); got != 600 {
		t.Fatalf("Spent after one 600-token run = %d, want 600", got)
	}

	second, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b", TextResult: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FinishReason != HandoffReasonCompleted {
		t.Fatalf("second run must still be admitted (600 < 1000): got %q", second.FinishReason)
	}

	third, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c", TextResult: true})
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if third.FinishReason != HandoffReasonTreeBudgetExceeded {
		t.Fatalf("third run: got %q, want %q", third.FinishReason, HandoffReasonTreeBudgetExceeded)
	}
	if third.Summary == "" {
		t.Error("refusal must explain itself")
	}
}

// TestSubAgentTreeBudget_StartRunResets: the aggregate is per-Run, so a new Run
// gets a fresh budget (otherwise the cap would silently become a session cap).
func TestSubAgentTreeBudget_StartRunResets(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 1500, TotalTokens: 1500},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(1000)

	runner.StartRun()
	if _, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a", TextResult: true}); err != nil {
		t.Fatal(err)
	}
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b", TextResult: true}); res.FinishReason != HandoffReasonTreeBudgetExceeded {
		t.Fatalf("expected the cap to bite in Run 1, got %q", res.FinishReason)
	}

	runner.StartRun()
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c", TextResult: true}); res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("a new Run must start with a fresh budget, got %q", res.FinishReason)
	}
}

// TestSubAgentTreeBudget_Unlimited: -1 is an explicit opt-out and must not be
// replaced by the default. The per-run budget is also set to unlimited here,
// because a 9M-token run would otherwise trip the per-run cap (2× the context
// window) first and this test would prove nothing about the Run-level cap.
// Four runs are needed for the pin to bite if -1 ever fell back to the default
// (20 context windows ≈ 21M): 4 × 9M crosses it on the fourth.
func TestSubAgentTreeBudget_Unlimited(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: "结论"},
		FinishReason: "stop",
		Usage:        ModelUsage{CacheMissTokens: 9_000_000, TotalTokens: 9_000_000},
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	runner.SetRunTokenBudget(-1)
	runner.StartRun()
	for i := 0; i < 4; i++ {
		res, err := runner.Run(context.Background(), Handoff{
			Agent: AgentSub, Goal: "g", TextResult: true, TokenBudget: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("run %d: an unlimited Run budget must never refuse, got %q", i, res.FinishReason)
		}
	}
}

// --- engine wiring ----------------------------------------------------------

// budgetStub is a scripted SubAgentBudget, so the Engine-side reset and notice
// can be tested without a live runner.
type budgetStub struct {
	spent      int64
	budget     int64
	starts     int
	noticeLeft int // how many ClaimBudgetNotice calls still return true
}

func (b *budgetStub) StartRun()    { b.starts++; b.spent = 0; b.noticeLeft = 1 }
func (b *budgetStub) Spent() int64 { return b.spent }
func (b *budgetStub) Budget() int64 {
	return b.budget
}
func (b *budgetStub) ClaimBudgetNotice() bool {
	if b.noticeLeft <= 0 {
		return false
	}
	b.noticeLeft--
	return true
}

// TestEngineRun_ResetsSubAgentBudget: every Run must start with a clean
// delegation-tree aggregate, or the cap silently becomes a session cap.
func TestEngineRun_ResetsSubAgentBudget(t *testing.T) {
	budget := &budgetStub{budget: 1000, spent: 999, noticeLeft: 0}
	e := newTestEngineWithBudget(t, budget)
	if _, err := e.Run(context.Background(), "你好"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if budget.starts == 0 {
		t.Error("Run must call SubAgentBudget.StartRun")
	}
}

// TestEngineRun_SubAgentBudgetNoticeOnce: past 80% the pinned notice fires
// exactly once and names both numbers.
func TestEngineRun_SubAgentBudgetNoticeOnce(t *testing.T) {
	budget := &budgetStub{budget: 1000, spent: 900, noticeLeft: 1}
	e := newTestEngineWithBudget(t, budget)

	e.injectSubAgentBudgetNotice()
	if len(e.pendingPinnedMessages) != 1 {
		t.Fatalf("expected exactly one pinned notice, got %d", len(e.pendingPinnedMessages))
	}
	got := e.pendingPinnedMessages[0]
	if !strings.Contains(got, "900") || !strings.Contains(got, "1000") {
		t.Errorf("notice must name spent and budget, got %q", got)
	}
	e.pendingPinnedMessages = nil

	// Second call: already claimed this Run → no second banner.
	e.injectSubAgentBudgetNotice()
	if len(e.pendingPinnedMessages) != 0 {
		t.Errorf("the near-limit notice must be one-shot, got %d", len(e.pendingPinnedMessages))
	}
}

// TestEngineRun_SubAgentBudgetNoticeSilent: below the threshold, when the cap
// is unlimited, and when no budget surface is wired, the notice stays silent.
func TestEngineRun_SubAgentBudgetNoticeSilent(t *testing.T) {
	cases := []struct {
		name   string
		budget SubAgentBudget
	}{
		{"below threshold", &budgetStub{budget: 1000, spent: 100, noticeLeft: 1}},
		{"unlimited", &budgetStub{budget: 0, spent: 100_000, noticeLeft: 1}},
		{"not wired", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{subBudget: tc.budget}
			e.injectSubAgentBudgetNotice()
			if len(e.pendingPinnedMessages) != 0 {
				t.Errorf("expected no notice, got %v", e.pendingPinnedMessages)
			}
		})
	}
}

// newTestEngineWithBudget builds a minimal Engine whose single turn answers with
// plain text, with the given delegation-budget surface wired. The field set
// mirrors the other Engine literals in this package (there is no shared helper):
// Run dereferences context, state, config and guards, so all four are required.
func newTestEngineWithBudget(t *testing.T, budget SubAgentBudget) *Engine {
	t.Helper()
	return &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成。", FinishReason: "stop"},
		}},
		tools:     stubToolExecutor{},
		context:   steerContextBuilder{},
		state:     &TaskState{TaskID: "test"},
		config:    EngineConfig{ModelName: "test-model"},
		guards:    &GuardSystem{scope: NewScopeGuard()},
		subBudget: budget,
	}
}

// TestSubAgentWidth_SixWayFanoutStillFits: the default width (6) is chosen to
// match the 2-6 directions a /collab round fans out, so a six-way parallel
// delegation must complete with NO refusal. This is the regression that keeps
// the invariant from breaking the built-in research skill.
func TestSubAgentWidth_SixWayFanoutStillFits(t *testing.T) {
	model := &gateModel{release: make(chan struct{}), entered: make(chan struct{}, 8)}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	var wg sync.WaitGroup
	results := make([]*HandoffResult, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runner.Run(context.Background(), Handoff{Agent: AgentResearcher, Goal: "方向", TextResult: true})
			if err != nil {
				t.Errorf("run %d: %v", i, err)
				return
			}
			results[i] = res
		}(i)
	}
	for i := 0; i < 6; i++ {
		<-model.entered
	}
	close(model.release)
	wg.Wait()
	for i, res := range results {
		if res == nil || res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("six-way fan-out must fit the default width; run %d: %+v", i, res)
		}
	}
}
