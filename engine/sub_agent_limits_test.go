package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// NOTE on NoNudge in these tests: a text-only reply NEVER completes a
// non-structured run — it is treated as narration and nudged, then ends as
// stalled_narration after 3 strikes (see the completion rules in runLoopFrom).
// These tests are about the delegation-tree invariants, not about completion
// semantics, so every handoff sets NoNudge to make the run's outcome
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
			got := handoffReasonHeading(tc.reason, "", false)
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
			res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", NoNudge: true})
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

	refused, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "third", NoNudge: true})
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
		res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", NoNudge: true})
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
	res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2, NoNudge: true})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinishReason != HandoffReasonMaxDepth {
		t.Fatalf("expected %q at depth 2 with max 1, got %q", HandoffReasonMaxDepth, res.FinishReason)
	}
	runner.SetMaxDepth(3)
	res, err = runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g", Depth: 2, NoNudge: true})
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

	first, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a", NoNudge: true})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.FinishReason != HandoffReasonCompleted {
		t.Fatalf("first run: got %q, want completed", first.FinishReason)
	}
	if got := runner.Spent(); got != 600 {
		t.Fatalf("Spent after one 600-token run = %d, want 600", got)
	}

	second, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b", NoNudge: true})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FinishReason != HandoffReasonCompleted {
		t.Fatalf("second run must still be admitted (600 < 1000): got %q", second.FinishReason)
	}

	third, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c", NoNudge: true})
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
	if _, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "a", NoNudge: true}); err != nil {
		t.Fatal(err)
	}
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "b", NoNudge: true}); res.FinishReason != HandoffReasonTreeBudgetExceeded {
		t.Fatalf("expected the cap to bite in Run 1, got %q", res.FinishReason)
	}

	runner.StartRun()
	if res, _ := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "c", NoNudge: true}); res.FinishReason != HandoffReasonCompleted {
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
			Agent: AgentSub, Goal: "g", NoNudge: true, TokenBudget: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.FinishReason != HandoffReasonCompleted {
			t.Fatalf("run %d: an unlimited Run budget must never refuse, got %q", i, res.FinishReason)
		}
	}
}
