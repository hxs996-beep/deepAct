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

// TestIsHandoffFollowUpReason_Refusals: a refusal is not a delivery, so the
// parent must be told to continue (same as any other failed run).
func TestIsHandoffFollowUpReason_Refusals(t *testing.T) {
	if !isHandoffFollowUpReason(HandoffReasonFanoutLimit) {
		t.Error("fanout_limit must be a follow-up reason")
	}
}

// TestHandoffResultStatus_EveryReason locks the single status mapping: the ✓/✗
// the UI and the delegating model see is decided by FinishReason (with
// Blocked/BlockedBy as the refusal/interrupt axis), so no partial outcome can
// render as a success while its own digest says "partial result".
func TestHandoffResultStatus_EveryReason(t *testing.T) {
	cases := []struct {
		name   string
		result *HandoffResult
		want   string
	}{
		{"nil", nil, "error"},
		{"completed", &HandoffResult{FinishReason: HandoffReasonCompleted}, "ok"},
		{"no reason recorded", &HandoffResult{}, "ok"},
		{"awaiting_user", &HandoffResult{FinishReason: HandoffReasonAwaitingUser}, "ok"},
		{"async_running", &HandoffResult{FinishReason: HandoffReasonAsyncRunning}, "ok"},
		{"cancelled reason", &HandoffResult{FinishReason: HandoffReasonCancelled}, "cancelled"},
		{"blocked by cancel", &HandoffResult{BlockedBy: HandoffReasonCancelled}, "cancelled"},
		{"blocked with no reason", &HandoffResult{Blocked: true}, "error"},
		{"max_tokens", &HandoffResult{FinishReason: HandoffReasonMaxTokens}, "error"},
		{"no_result", &HandoffResult{FinishReason: HandoffReasonNoResult}, "error"},
		{"stalled_narration", &HandoffResult{FinishReason: HandoffReasonStalledNarration}, "error"},
		{"loop_detected", &HandoffResult{FinishReason: HandoffReasonLoopDetected}, "error"},
		{"budget_exceeded", &HandoffResult{FinishReason: HandoffReasonBudgetExceeded}, "error"},
		{"error", &HandoffResult{FinishReason: HandoffReasonError}, "error"},
		{"max_depth", &HandoffResult{FinishReason: HandoffReasonMaxDepth}, "error"},
		{"fanout_limit refusal", &HandoffResult{Blocked: true, FinishReason: HandoffReasonFanoutLimit}, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := handoffResultStatus(tc.result); got != tc.want {
				t.Errorf("handoffResultStatus = %q, want %q", got, tc.want)
			}
		})
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

// --- width / depth caps ------------------------------------------------------

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
