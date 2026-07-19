package engine

import (
	"context"
	"strings"
	"testing"
)

func TestHandleAnalysisNudgeConfirmation(t *testing.T) {
	// Case 1: user confirms the analysis report
	e := &Engine{
		state:                 &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		pendingAnalysisNudge:  true,
		isChinese:             true,
		history:               []Message{{Role: "user", Content: "确认"}},
	}
	handled := e.handleAnalysisNudgeConfirmation("确认")
	if !handled {
		t.Error("should return true when nudge is pending")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after confirmation")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after confirmation")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after confirmation")
	}

	// Case 2: user gives feedback (not a confirmation)
	e2 := &Engine{
		state:                &TaskState{AnalysisMode: true, AnalysisReportConfirmed: false},
		pendingAnalysisNudge: true,
		isChinese:            true,
		history:              []Message{{Role: "user", Content: "不对"}},
	}
	handled2 := e2.handleAnalysisNudgeConfirmation("不对")
	if !handled2 {
		t.Error("should return true when nudge is pending (feedback)")
	}
	if e2.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should remain false after feedback")
	}
	if e2.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after feedback")
	}

	// Case 3: no nudge pending - should return false
	e3 := &Engine{state: &TaskState{}}
	handled3 := e3.handleAnalysisNudgeConfirmation("确认")
	if handled3 {
		t.Error("should return false when no nudge is pending")
	}
}

// TestExecuteTurn_AnalysisGateDegradation_BatchesEdits verifies that when
// the analysis report gate degrades (analysisNudgeCount >= 2), it does NOT
// silently fall through to the edit plan guard. Instead, it sets
// AnalysisReportConfirmed=true, clears pendingAnalysisNudge, and sends a
// one-time message telling the LLM to batch ALL planned edits. This prevents
// the LLM from submitting edits one at a time after being blocked twice.
func TestExecuteTurn_AnalysisGateDegradation_BatchesEdits(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{
				Delta: "再次尝试修改",
				ToolCalls: []ModelToolCall{
					{ID: "call_1", Type: "function", Function: ModelFunctionCall{
						Name:      "edit",
						Arguments: `{"path":"engine/types.go","old_string":"old","new_string":"new"}`,
					}},
				},
				FinishReason: "tool_calls",
			},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 5},
		history:            []Message{{Role: "user", Content: "修改代码"}},
		config:             EngineConfig{ModelName: "test-model"},
		isChinese:          true,
		runToolCallCount:   3,
		analysisNudgeCount: 2,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (degradation handler should block), got Done=true")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after degradation handler fires")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after degradation handler fires")
	}
	// Verify the blocked message tells the LLM to batch all edits
	found := false
	for _, msg := range e.history {
		if msg.Role == "tool" && strings.Contains(msg.Content, "一次性提交所有计划的修改") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected blocked message to contain batch-edits instruction")
	}
}
