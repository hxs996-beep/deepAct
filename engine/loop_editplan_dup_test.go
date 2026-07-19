package engine

import (
	"context"
	"strings"
	"testing"
)

// TestRun_EditPlanBlocked_NoSummaryDuplication is a regression test for the
// bug where the edit-plan guard's confirmation prompt showed the reasoning
// text twice. Root cause: the Blocked path in Run() (loop.go) set BOTH
// Summary (from buildRunSummary, which finds the assistant's Content in
// history) and Questions (the plan summary = reasoning + "确认执行修改？").
// The UI concatenates Summary + "\n\n" + Questions, so the reasoning
// appeared twice.
//
// Fix: when e.pendingEditPlan != nil (edit-plan guard), Summary must be
// empty because Questions already contains the full plan summary.
func TestRun_EditPlanBlocked_NoSummaryDuplication(t *testing.T) {
	reasoning := "回滚两处未授权改动—— loop.go 和 turn.go 中的 ModifiedFiles 重置逻辑。"

	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{
				Delta:   reasoning,
				ToolCalls: []ModelToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ModelFunctionCall{
						Name:      "edit",
						Arguments: `{"path":"loop.go","old_string":"x","new_string":"y"}`,
					},
				}},
				FinishReason: "tool_calls",
			},
		}},
		context:  &stubContextBuilder{},
		tools:    stubToolExecutor{},
		state:    &TaskState{TurnNumber: 0},
		history:  []Message{},
		config:   EngineConfig{ModelName: "test-model"},
		guards:   &GuardSystem{}, // nil fields are nil-safe
	}

	resp, err := e.Run(context.Background(), "修复这个 bug")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if !resp.Blocked {
		t.Fatalf("expected Blocked=true, got Blocked=false")
	}

	// The plan summary (Questions[0]) should contain the reasoning.
	if len(resp.Questions) == 0 {
		t.Fatalf("expected Questions to be non-empty")
	}
	planSummary := resp.Questions[0]
	if !strings.Contains(planSummary, reasoning) {
		t.Errorf("Questions[0] should contain reasoning, got: %s", planSummary)
	}

	// Summary must be empty: Questions already carries the full plan summary
	// (reasoning + confirmation prompt). Setting Summary to the same reasoning
	// causes the UI to show it twice (Summary + "\n\n" + Questions).
	if resp.Summary != "" {
		// Check for duplication: if Summary contains the reasoning and
		// Questions also contains it, the UI would show it twice.
		if strings.Contains(resp.Summary, reasoning) {
			t.Errorf("DUPLICATION: Summary (%q) contains the same reasoning as Questions (%q). "+
				"The UI concatenates Summary + Questions, so the reasoning appears twice.",
				resp.Summary, planSummary)
		}
	}
}
