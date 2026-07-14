package engine

import (
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
