package engine

import (
	"testing"
)

func TestDetectUserIntent_AnalyzeOnly(t *testing.T) {
	tests := []struct {
		msg    string
		goals  string
		intent UserIntent
	}{
		// Pure analysis (Chinese) — IntentAnalyze
		{msg: "分析一下当前UI为什么点击diff区域没有反应", goals: "添加登录页面功能", intent: IntentAnalyze},
		{msg: "为什么鼠标点击代码修改的diff区域没反应", goals: "添加登录页面功能", intent: IntentAnalyze},
		{msg: "解释一下这个函数的逻辑", goals: "修复登录页面bug", intent: IntentAnalyze},
		{msg: "看一下为什么编译失败", goals: "实现用户管理功能", intent: IntentAnalyze},
		{msg: "这个错误是什么原因导致的", goals: "添加支付功能", intent: IntentAnalyze},
		{msg: "帮我看看这段代码怎么回事", goals: "重构数据库层", intent: IntentAnalyze},

		// Analysis with modification command — IntentNewTopic (safe: reset PlanConfirmed)
		{msg: "分析一下然后修复这个问题", goals: "添加登录页面功能", intent: IntentNewTopic},
		{msg: "看看为什么编译失败，然后改一下", goals: "添加登录页面功能", intent: IntentNewTopic},
		{msg: "分析UI问题并做相应的修改", goals: "修复登录页面bug", intent: IntentNewTopic},

		// Context reference — IntentContinue
		{msg: "刚才那个修改再调整一下", goals: "添加登录页面功能", intent: IntentContinue},
		{msg: "继续上面的工作", goals: "添加登录页面功能", intent: IntentContinue},
		{msg: "这个也加一下", goals: "添加登录页面功能", intent: IntentContinue},
		{msg: "also add the logout button", goals: "add login page", intent: IntentContinue},

		// New topic (no overlap with goal) — IntentNewTopic
		{msg: "修改一下配置文件", goals: "添加登录页面功能", intent: IntentNewTopic},
		{msg: "重构数据库查询", goals: "添加登录页面功能", intent: IntentNewTopic},
		{msg: "这个颜色不太对，换成蓝色", goals: "添加用户认证功能", intent: IntentNewTopic},

		// Same topic (has overlap) — IntentContinue via isSameTopic fallback
		{msg: "登录页面再加一个记住密码的选项", goals: "添加登录页面功能", intent: IntentContinue},
		{msg: "把登录接口的超时时间改长一点", goals: "添加登录页面功能", intent: IntentContinue},
		{msg: "refactor the login form validation", goals: "add login page", intent: IntentContinue},

		// No prior goal (first message) — IntentContinue (no PlanConfirmed to reset)
		{msg: "hello", goals: "", intent: IntentContinue},
		{msg: "分析一下", goals: "", intent: IntentContinue},
	}

	for _, tt := range tests {
		e := &Engine{
			state: &TaskState{Goal: tt.goals},
		}
		got := e.detectUserIntent(tt.msg)
		if got != tt.intent {
			t.Errorf("detectUserIntent(%q, goal=%q) = %v, want %v", tt.msg, tt.goals, got, tt.intent)
		}
	}
}

func TestIsDangerousConfirmation(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		// Exact matches
		{"确认", true}, {"yes", true}, {"好的", true}, {"y", true},
		// Separator-compound
		{"对，改吧", true}, {"好的，执行", true}, {"ok, go", true},
		// Concatenated confirm words (no separator) — the bug that caused the
		// "确认执行修改？" infinite loop.
		{"确认执行", true}, {"继续执行", true}, {"继续", true}, {"执行吧", true},
		// Exact "修改" compounds (the prompt literally asks "确认执行修改？")
		{"确认执行修改", true}, {"确认修改", true}, {"执行修改", true},
		// Real instructions / feedback must NOT be treated as confirmation
		{"确认但先改下方案", false}, {"改一下方案再执行", false}, {"修改一下配置", false},
		{"修改", false}, {"你确认下对不对", false}, {"did", false}, {"你好", false},
	}
	for _, tt := range tests {
		if got := isDangerousConfirmation(tt.msg); got != tt.exp {
			t.Errorf("isDangerousConfirmation(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestDetectUserIntent_ConfirmationContinues(t *testing.T) {
	// A confirmation must be IntentContinue, never IntentNewTopic, or
	// PlanConfirmed gets reset and the edit-plan guard loops forever.
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}}
	for _, msg := range []string{"确认", "确认执行", "确认执行修改", "继续执行", "yes"} {
		if got := e.detectUserIntent(msg); got != IntentContinue {
			t.Errorf("detectUserIntent(%q) = %v, want IntentContinue", msg, got)
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

func TestHasContextReference(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"刚才那个再改一下", true},
		{"上面提到的", true},
		{"继续写代码", true},
		{"also add this", true},
		{"也加一个功能", true},
		{"开始一个新的任务", false},
		{"新的功能", false},
		{"解释一下这个函数", false}, // "这个" is descriptive, not a context reference
	}
	for _, tt := range tests {
		if got := hasContextReference(tt.msg); got != tt.exp {
			t.Errorf("hasContextReference(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestIsAnalysisOnly(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"分析一下这个问题", true},
		{"为什么点击没反应", true},
		{"解释一下这段代码", true},
		{"看看是什么原因", true},
		{"为什么鼠标点击代码修改的diff区域没反应", true}, // "修改" as noun in "代码修改" — not a mod command
		{"how does this work", true},
		{"explain the logic", true},
		{"分析一下然后修复", false},          // "然后修" is a modification phrase
		{"看看为什么报错并改掉", false},     // "并改" is a modification phrase
		{"修复这个bug", false},              // no analysis word
		{"添加一个新功能", false},            // no analysis word
		{"analyze and fix the bug", false}, // "fix the" is a modification phrase
	}
	for _, tt := range tests {
		if got := isAnalysisOnly(tt.msg); got != tt.exp {
			t.Errorf("isAnalysisOnly(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestExtractKeyTerms(t *testing.T) {
	terms := extractKeyTerms("添加登录页面功能和用户认证")
	if len(terms) == 0 {
		t.Error("expected at least some terms from Chinese text")
	}
	found := false
	for _, term := range terms {
		if term == "登录" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected '登录' in terms, got %v", terms)
	}

	// English
	terms = extractKeyTerms("add login page and user authentication")
	foundLogin := false
	for _, term := range terms {
		if term == "login" {
			foundLogin = true
			break
		}
	}
	if !foundLogin {
		t.Errorf("expected 'login' in English terms, got %v", terms)
	}
}

func TestIsSameTopic(t *testing.T) {
	tests := []struct {
		msg  string
		goal string
		same bool
	}{
		{"登录页面再加一个功能", "添加登录页面", true},
		{"重构数据库查询", "添加登录页面", false},
		{"fix the login form", "add login page", true},
		{"refactor database queries", "add login page", false},
		{"用户管理添加权限", "实现用户管理模块", true},
		{"修改配置文件", "实现用户管理模块", false},
	}
	for _, tt := range tests {
		if got := isSameTopic(tt.msg, tt.goal); got != tt.same {
			t.Errorf("isSameTopic(%q, %q) = %v, want %v", tt.msg, tt.goal, got, tt.same)
		}
	}
}
