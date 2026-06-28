package engine

import (
	"strings"
	"testing"
)

func TestReadLoopState_NudgeThenBlock(t *testing.T) {
	s := NewReadLoopState()
	key := "read:a.go::symbol:Run"

	// 1st, 2nd: allow
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("1st: want allow, got %s (%s)", a.Type, a.Message)
	}
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("2nd: want allow, got %s (%s)", a.Type, a.Message)
	}
	// 3rd: nudge
	a := s.Check(key)
	if a.Type != GuardDiagnose {
		t.Fatalf("3rd: want diagnose(nudge), got %s (%s)", a.Type, a.Message)
	}
	if a.Message == "" {
		t.Fatal("3rd: nudge message empty")
	}
	// 4th: block
	a = s.Check(key)
	if a.Type != GuardBlock {
		t.Fatalf("4th: want block, got %s (%s)", a.Type, a.Message)
	}
}

func TestReadLoopState_DifferentScopeIndependent(t *testing.T) {
	s := NewReadLoopState()
	k1 := "read:a.go::symbol:Run"
	k2 := "read:a.go::L10-50"
	// k2 read 3 times → nudge, but must not inflate k1's count.
	if a := s.Check(k2); a.Type != GuardAllow {
		t.Fatalf("k2 1st: want allow, got %s", a.Type)
	}
	if a := s.Check(k2); a.Type != GuardAllow {
		t.Fatalf("k2 2nd: want allow, got %s", a.Type)
	}
	if a := s.Check(k2); a.Type != GuardDiagnose {
		t.Fatalf("k2 3rd: want diagnose, got %s", a.Type)
	}
	// k1 is independent: its 1st and 2nd must still be allow despite k2's counts.
	if a := s.Check(k1); a.Type != GuardAllow {
		t.Fatalf("k1 1st: want allow (independent of k2), got %s", a.Type)
	}
	if a := s.Check(k1); a.Type != GuardAllow {
		t.Fatalf("k1 2nd: want allow, got %s", a.Type)
	}
}

func TestReadLoopState_Reset(t *testing.T) {
	s := NewReadLoopState()
	key := "read:a.go::symbol:Run"
	s.Check(key)
	s.Check(key)
	s.Check(key) // nudge
	s.Reset()
	// After reset, 1st is allow again
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("after reset 1st: want allow, got %s", a.Type)
	}
}

func TestReadLoopState_NilSafe(t *testing.T) {
	var s *ReadLoopState
	if a := s.Check("read:a.go::"); a.Type != GuardAllow {
		t.Errorf("nil Check: want allow, got %s", a.Type)
	}
	s.Reset() // must not panic
}

func TestSplitReadKey(t *testing.T) {
	tests := []struct {
		in       string
		wantPath string
		wantScope string
	}{
		{"read:a.go::symbol:Run", "a.go", "symbol:Run"},
		{"read:a.go::", "a.go", ""},
		{"read:a.go", "a.go", ""},
		{"notread:x", "notread:x", ""},
	}
	for _, tt := range tests {
		p, s := splitReadKey(tt.in)
		if p != tt.wantPath || s != tt.wantScope {
			t.Errorf("splitReadKey(%q) = (%q,%q), want (%q,%q)", tt.in, p, s, tt.wantPath, tt.wantScope)
		}
	}
}

func TestBuildReadLoopMessages(t *testing.T) {
	key := "read:ui/model.go::symbol:Run"
	nudge := buildReadLoopNudge(key, true)
	block := buildReadLoopBlockMsg(key, true)
	if !strings.Contains(nudge, "ui/model.go") || !strings.Contains(nudge, "Run") {
		t.Errorf("nudge missing path/scope: %s", nudge)
	}
	if !strings.Contains(nudge, "不要再读取") {
		t.Errorf("nudge missing directive: %s", nudge)
	}
	if !strings.Contains(block, "ui/model.go") || !strings.Contains(block, "请澄清") {
		t.Errorf("block missing path/clarify: %s", block)
	}
	// English variants should not contain Chinese directives.
	enNudge := buildReadLoopNudge(key, false)
	if strings.Contains(enNudge, "不要再读取") {
		t.Errorf("en nudge should be English: %s", enNudge)
	}
}
