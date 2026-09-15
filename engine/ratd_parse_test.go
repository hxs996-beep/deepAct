package engine

import (
	"strings"
	"testing"
)

func TestParseRATDCommand_Valid(t *testing.T) {
	cmd := parseRATDCommand("/ratd 实现队列")
	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	if cmd.Goal != "实现队列" {
		t.Fatalf("expected goal 实现队列, got %q", cmd.Goal)
	}
}

func TestParseRATDCommand_NoGoal(t *testing.T) {
	if cmd := parseRATDCommand("/ratd"); cmd != nil {
		t.Fatalf("expected nil for /ratd without goal, got %+v", cmd)
	}
}

func TestParseRATDCommand_NotRATD(t *testing.T) {
	if cmd := parseRATDCommand("/debate x"); cmd != nil {
		t.Fatalf("expected nil for /debate x, got %+v", cmd)
	}
	if cmd := parseRATDCommand("普通消息"); cmd != nil {
		t.Fatalf("expected nil for plain message, got %+v", cmd)
	}
	if cmd := parseRATDCommand(""); cmd != nil {
		t.Fatalf("expected nil for empty message, got %+v", cmd)
	}
	if cmd := parseRATDCommand("/"); cmd != nil {
		t.Fatalf("expected nil for bare slash, got %+v", cmd)
	}
	if cmd := parseRATDCommand("   "); cmd != nil {
		t.Fatalf("expected nil for whitespace-only message, got %+v", cmd)
	}
}

func TestParseRATDCommand_Uppercase(t *testing.T) {
	cmd := parseRATDCommand("/RATD 实现队列")
	if cmd == nil {
		t.Fatal("expected non-nil command for uppercase /RATD")
	}
	if cmd.Goal != "实现队列" {
		t.Fatalf("expected goal 实现队列, got %q", cmd.Goal)
	}
}

func TestParseRATDCommand_Multiline(t *testing.T) {
	cmd := parseRATDCommand("/ratd 目标\n第二行")
	if cmd == nil {
		t.Fatal("expected non-nil command for multiline input")
	}
	if cmd.Goal != "目标" {
		t.Fatalf("expected goal 目标 (first line only), got %q", cmd.Goal)
	}
}

func TestBuildProposerGoal_IncludesFailingContext(t *testing.T) {
	state := &RATDState{
		Tests: []RATDTest{
			{TargetFile: "queue.go", Severity: "high", TestCode: "func TestQueue() {}"},
		},
		LastSandbox:        &RATDSandboxResult{ExitCode: 1, Output: "panic: nil pointer"},
		ActionableFeedback: "修复 nil 指针：初始化队列头节点",
	}
	out := buildProposerGoal("实现队列", state, true)
	if !strings.Contains(out, "queue.go") {
		t.Fatalf("expected output to contain TargetFile queue.go, got:\n%s", out)
	}
	if !strings.Contains(out, "沙箱输出") {
		t.Fatalf("expected output to contain zh 沙箱输出, got:\n%s", out)
	}
	if !strings.Contains(out, "修复 nil 指针：初始化队列头节点") {
		t.Fatalf("expected output to contain ActionableFeedback, got:\n%s", out)
	}
}

func TestBuildProposerGoal_FirstRoundClean(t *testing.T) {
	out := buildProposerGoal("实现队列", nil, true)
	if strings.Contains(out, "Test Suite") {
		t.Fatalf("expected no Test Suite on first round, got:\n%s", out)
	}
	if strings.Contains(out, "Sandbox Output") {
		t.Fatalf("expected no Sandbox Output on first round, got:\n%s", out)
	}
}

func TestBuildArbitratorGoal_ShowsExitCode(t *testing.T) {
	sandbox := &RATDSandboxResult{ExitCode: 1, TimedOut: false, Output: "FAIL"}
	out := buildArbitratorGoal("需求", []RATDSourceFile{{Path: "a.go", Content: "code"}}, RATDTest{TargetFile: "a_test.go", Severity: "high", TestCode: "t", Rationale: "r"}, sandbox, true)
	if !strings.Contains(out, "exit code: 1") {
		t.Fatalf("expected output to contain exit code: 1, got:\n%s", out)
	}
}

func TestBuildRedTeamGoal_ExcludesDesignNotes(t *testing.T) {
	out := buildRedTeamGoal("需求", []RATDSourceFile{{Path: "a.go", Content: "code"}}, true)
	if !strings.Contains(out, "a.go") {
		t.Fatalf("expected output to contain a.go, got:\n%s", out)
	}
	if strings.Contains(out, "design_notes") {
		t.Fatalf("expected output to exclude design_notes (context isolation), got:\n%s", out)
	}
}
