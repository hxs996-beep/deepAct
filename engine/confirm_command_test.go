package engine

import (
	"context"
	"strings"
	"testing"
)

func TestParseConfirmCommand(t *testing.T) {
	cases := []struct {
		msg string
		n   int
		ok  bool
	}{
		{"/confirm 1", 1, true},
		{"/confirm 2", 2, true},
		{"/confirm 3", 3, true},
		{"/confirm", 0, false},   // 无编号不命中
		{"/confirm 0", 0, false}, // 编号从 1 起
		{"确认", 0, false},         // 非 /confirm 前缀
		{"请按方案A执行", 0, false},
	}
	for _, c := range cases {
		n, ok := parseConfirmCommand(c.msg)
		if n != c.n || ok != c.ok {
			t.Errorf("parseConfirmCommand(%q) = (%d,%v), want (%d,%v)", c.msg, n, ok, c.n, c.ok)
		}
	}
}

// /confirm 1 确定性确认：置 AnalysisReportConfirmed、清 AnalysisMode。
func TestHandleConfirmCommand_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after /confirm 1")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after /confirm 1")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "已确认") {
		t.Errorf("history user message should be rewritten as confirmation, got %q", last)
	}
}

// /confirm N (N>=2) 是反馈语义：不确认执行，AnalysisMode 保持，清除 nudge。
func TestHandleConfirmCommand_FeedbackVariant(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:   []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should stay false for /confirm N (N>=2)")
	}
	if !e.state.AnalysisMode {
		t.Error("AnalysisMode should stay true for /confirm N (N>=2)")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after /confirm N")
	}
}

// detectUserIntent 对 /confirm 前缀走确定性 fast-path，不调用 intentJudge。
func TestDetectUserIntent_ConfirmCommandFastPath(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze} // 即使 judge 判 analyze 也不该被调用
	e := &Engine{state: &TaskState{Goal: "g"}, intentJudge: judge}

	if got := e.detectUserIntent(context.Background(), "/confirm 1"); got != IntentContinue {
		t.Errorf("detectUserIntent(/confirm 1) = %v, want IntentContinue", got)
	}
	if judge.called {
		t.Error("intentJudge must NOT be called for /confirm (deterministic channel)")
	}
}
