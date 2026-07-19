package engine

import (
	"context"
	"testing"
)

// stubIntentJudge is a controllable IntentJudge stub for detectUserIntent tests.
type stubIntentJudge struct {
	intent UserIntent
	err    error
	called bool
	last   IntentCheck
}

func (s *stubIntentJudge) Classify(_ context.Context, check IntentCheck) (UserIntent, error) {
	s.called = true
	s.last = check
	if s.err != nil {
		return 0, s.err
	}
	return s.intent, nil
}

func TestDetectUserIntent_NoGoal_Continue(t *testing.T) {
	e := &Engine{state: &TaskState{Goal: ""}}
	got := e.detectUserIntent(context.Background(), "分析一下")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue for empty goal, got %v", got)
	}
}

func TestDetectUserIntent_ConfirmationFastPath(t *testing.T) {
	// isDangerousConfirmation is a deterministic fast-path; judge must NOT be called.
	judge := &stubIntentJudge{intent: IntentNewTopic} // would be wrong if called
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	for _, msg := range []string{"确认", "确认执行", "确认执行修改", "继续执行", "yes"} {
		judge.called = false
		got := e.detectUserIntent(context.Background(), msg)
		if got != IntentContinue {
			t.Errorf("detectUserIntent(%q) = %v, want IntentContinue", msg, got)
		}
		if judge.called {
			t.Errorf("detectUserIntent(%q) should not call judge (confirmation fast-path)", msg)
		}
	}
}

func TestDetectUserIntent_NilJudge_Continue(t *testing.T) {
	// nil intentJudge (wiring bug) falls back conservatively to IntentContinue.
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue for nil judge, got %v", got)
	}
}

func TestDetectUserIntent_JudgeAnalyze(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "为什么点击没反应")
	if got != IntentAnalyze {
		t.Errorf("expected IntentAnalyze, got %v", got)
	}
	if !judge.called {
		t.Error("expected judge to be called")
	}
	if judge.last.Goal != "添加登录页面功能" {
		t.Errorf("expected goal passed to judge, got %q", judge.last.Goal)
	}
}

func TestDetectUserIntent_JudgeContinue(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentContinue}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "刚才那个再调整一下")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue, got %v", got)
	}
}

func TestDetectUserIntent_JudgeNewTopic(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentNewTopic}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentNewTopic {
		t.Errorf("expected IntentNewTopic, got %v", got)
	}
}

func TestDetectUserIntent_JudgeError_Continue(t *testing.T) {
	judge := &stubIntentJudge{err: errBoom}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentContinue {
		t.Errorf("expected conservative IntentContinue on judge error, got %v", got)
	}
}

func TestIsDangerousConfirmation(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"确认", true}, {"yes", true}, {"好的", true}, {"y", true},
		{"对，改吧", true}, {"好的，执行", true}, {"ok, go", true},
		{"确认执行", true}, {"继续执行", true}, {"继续", true}, {"执行吧", true},
		{"确认执行修改", true}, {"确认修改", true}, {"执行修改", true},
		{"确认但先改下方案", false}, {"改一下方案再执行", false}, {"修改一下配置", false},
		{"修改", false}, {"你确认下对不对", false}, {"did", false}, {"你好", false},
	}
	for _, tt := range tests {
		if got := isDangerousConfirmation(tt.msg); got != tt.exp {
			t.Errorf("isDangerousConfirmation(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestIsClearCommand(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"/clear", true},
		{"/clear ", true},
		{"/clear all the things", true},
		{"/Clear", false},
		{"clear", false},
		{"/clear  extra", true},
	}
	for _, tt := range tests {
		if got := isClearCommand(tt.msg); got != tt.exp {
			t.Errorf("isClearCommand(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}
