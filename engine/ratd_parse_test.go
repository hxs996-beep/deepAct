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
