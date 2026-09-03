package engine

import (
	"context"
	"strings"
	"testing"
)

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

// --- Collab pipeline stages ---

func TestHandleCollabArena_RunsAllStages(t *testing.T) {
	e := newCollabTestEngine(t)
	e.state.Collab = &CollabState{
		Goal:  "实现一个缓存层",
		Phase: CollabReconPhase,
	}
	resp, err := e.collabHall.handleCollabArena(context.Background())
	if err != nil {
		t.Fatalf("handleCollabArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Collab.Phase != CollabAwaitingConfirmation {
		t.Errorf("Phase = %v, want CollabAwaitingConfirmation", e.state.Collab.Phase)
	}
	if len(e.state.Collab.Stages) != 4 {
		t.Fatalf("got %d stages, want 4", len(e.state.Collab.Stages))
	}
	// 各阶段都有产出（mockPromptRunner 返回固定文本"采用微服务架构"）
	for _, s := range e.state.Collab.Stages {
		if !strings.Contains(s.Content, "采用微服务架构") {
			t.Errorf("stage %q should contain mock output, got %q", s.Name, s.Content)
		}
	}
}

// newCollabTestEngine creates a minimal engine for /collab testing.
func newCollabTestEngine(t *testing.T) *Engine {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(&mockPromptRunner{
		mockSimpleAgent: mockSimpleAgent{
			id:       AgentSub,
			response: "## 产出\n采用微服务架构。",
		},
	})
	e := &Engine{
		agents:          reg,
		state:           &TaskState{TaskID: "test-collab"},
		config:          EngineConfig{},
		activatedSkills: make(map[string]bool),
	}
	e.collabHall = NewCollabHall(e)
	return e
}
