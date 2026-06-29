package engine

import (
	"context"
	"testing"
)

// mockAgent implements Agent interface for testing.
type mockAgent struct {
	id   AgentID
	spec AgentSpec
}

func (m *mockAgent) ID() AgentID                      { return m.id }
func (m *mockAgent) Spec() AgentSpec                  { return m.spec }
func (m *mockAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) { return nil, nil }
func (m *mockAgent) SetOnProgress(fn ProgressFunc)    {}

func TestNewAgentRegistry(t *testing.T) {
	r := NewAgentRegistry()
	if r == nil {
		t.Fatal("expected non-nil AgentRegistry")
	}
	if r.agents == nil {
		t.Error("agents map should be initialized")
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := NewAgentRegistry()
	a := &mockAgent{id: "test-agent", spec: AgentSpec{Description: "for testing"}}
	r.Register(a)

	got, err := r.Get("test-agent")
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got.ID() != "test-agent" {
		t.Errorf("got agent ID %q, want %q", got.ID(), "test-agent")
	}
}

func TestGet_NotFound(t *testing.T) {
	r := NewAgentRegistry()
	_, err := r.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestRegister_Overwrite(t *testing.T) {
	r := NewAgentRegistry()
	a1 := &mockAgent{id: "same-id", spec: AgentSpec{Description: "Version 1"}}
	a2 := &mockAgent{id: "same-id", spec: AgentSpec{Description: "Version 2"}}
	r.Register(a1)
	r.Register(a2)

	got, err := r.Get("same-id")
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got.Spec().Description != "Version 2" {
		t.Errorf("expected overwritten value, got %q", got.Spec().Description)
	}
}

func TestAgentSpecs(t *testing.T) {
	r := NewAgentRegistry()
	r.Register(&mockAgent{id: "a", spec: AgentSpec{Description: "Agent A"}})
	r.Register(&mockAgent{id: "b", spec: AgentSpec{Description: "Agent B"}})

	specs := r.AgentSpecs()
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
}

func TestAgentSpecs_Empty(t *testing.T) {
	r := NewAgentRegistry()
	specs := r.AgentSpecs()
	if len(specs) != 0 {
		t.Errorf("expected 0 specs, got %d", len(specs))
	}
}

func TestForEach(t *testing.T) {
	r := NewAgentRegistry()
	r.Register(&mockAgent{id: "a"})
	r.Register(&mockAgent{id: "b"})

	visited := make(map[AgentID]bool)
	r.ForEach(func(a Agent) {
		visited[a.ID()] = true
	})

	if !visited["a"] || !visited["b"] {
		t.Errorf("ForEach did not visit all agents: %v", visited)
	}
}

func TestForEach_Empty(t *testing.T) {
	r := NewAgentRegistry()
	count := 0
	r.ForEach(func(a Agent) {
		count++
	})
	if count != 0 {
		t.Errorf("expected 0 callback invocations, got %d", count)
	}
}
