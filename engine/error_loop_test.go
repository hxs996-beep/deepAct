package engine

import (
	"testing"
)

func TestErrorLoopState_BlocksAfterRepeatedErrors(t *testing.T) {
	s := NewErrorLoopState(3)
	key := "edit:llm/deepseek.go"

	// 1st, 2nd error: allow (give the model a chance to self-correct).
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("1st error: want allow, got %s (%s)", a.Type, a.Message)
	}
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("2nd error: want allow, got %s (%s)", a.Type, a.Message)
	}
	// 3rd error: block — the same (tool, path) keeps failing without progress.
	a := s.Check(key, true)
	if a.Type != GuardBlock {
		t.Fatalf("3rd error: want block, got %s (%s)", a.Type, a.Message)
	}
	if a.Message == "" {
		t.Fatal("3rd error: block message empty")
	}
}

func TestErrorLoopState_SuccessResetsStreak(t *testing.T) {
	s := NewErrorLoopState(3)
	key := "edit:llm/deepseek.go"

	s.Check(key, true)
	s.Check(key, true) // 2 errors
	// A success on the same op breaks the streak.
	if a := s.Check(key, false); a.Type != GuardAllow {
		t.Fatalf("success: want allow, got %s", a.Type)
	}
	// After reset, two more errors must not yet block (count restarted).
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("post-success 1st error: want allow, got %s", a.Type)
	}
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("post-success 2nd error: want allow, got %s", a.Type)
	}
	// Third error after reset → block.
	if a := s.Check(key, true); a.Type != GuardBlock {
		t.Fatalf("post-success 3rd error: want block, got %s", a.Type)
	}
}

func TestErrorLoopState_DifferentOpsIndependent(t *testing.T) {
	s := NewErrorLoopState(3)
	k1 := "edit:a.go"
	k2 := "edit:b.go"
	// k1 fails twice — must not inflate k2's streak.
	s.Check(k1, true)
	s.Check(k1, true)
	if a := s.Check(k2, true); a.Type != GuardAllow {
		t.Fatalf("k2 1st error: want allow, got %s", a.Type)
	}
	if a := s.Check(k2, true); a.Type != GuardAllow {
		t.Fatalf("k2 2nd error: want allow, got %s", a.Type)
	}
	if a := s.Check(k2, true); a.Type != GuardBlock {
		t.Fatalf("k2 3rd error: want block, got %s", a.Type)
	}
	// k1 still at 2 — one more error blocks.
	if a := s.Check(k1, true); a.Type != GuardBlock {
		t.Fatalf("k1 3rd error: want block, got %s", a.Type)
	}
}

func TestErrorLoopState_NilSafe(t *testing.T) {
	var s *ErrorLoopState
	if a := s.Check("edit:x", true); a.Type != GuardAllow {
		t.Errorf("nil Check: want allow, got %s", a.Type)
	}
	s.Reset() // must not panic
}

func TestErrorLoopState_Reset(t *testing.T) {
	s := NewErrorLoopState(3)
	key := "write:a.go"
	s.Check(key, true)
	s.Check(key, true)
	s.Reset()
	// After reset, two errors must not block.
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("after reset 1st: want allow, got %s", a.Type)
	}
	if a := s.Check(key, true); a.Type != GuardAllow {
		t.Fatalf("after reset 2nd: want allow, got %s", a.Type)
	}
}

func TestCoarseOp(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"edit:llm/deepseek.go#abc123", "edit:llm/deepseek.go"},
		{"write:a.go#sig", "write:a.go"},
		{"read:a.go::symbol:Run", "read:a.go"},
		{"bash:foo.sh#", "bash:foo.sh"},
		{"grep:src", "grep:src"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := coarseOp(tt.in); got != tt.want {
			t.Errorf("coarseOp(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
