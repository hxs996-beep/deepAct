package engine

import "testing"

// --- /collab command parsing ---

func TestParseCollabCommand_Valid(t *testing.T) {
	cmd := parseCollabCommand("/collab 实现一个缓存层")
	if cmd == nil {
		t.Fatal("expected non-nil CollabCommand")
	}
	if cmd.Goal != "实现一个缓存层" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "实现一个缓存层")
	}
}

func TestParseCollabCommand_NotCollab(t *testing.T) {
	cases := []string{
		"/debate 实现一个功能",
		"/team 实现一个功能",
		"/skills",
		"普通用户消息",
		"",
		"/",
	}
	for _, c := range cases {
		cmd := parseCollabCommand(c)
		if cmd != nil {
			t.Errorf("expected nil for %q, got %+v", c, cmd)
		}
	}
}
