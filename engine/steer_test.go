package engine

import (
	"testing"
)

func TestSteer_AndDrain(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("补充信息1")
	e.Steer("补充信息2")

	injected := e.drainSteerQueue()
	if !injected {
		t.Fatal("drainSteerQueue should return true when queue is non-empty")
	}

	if len(e.history) != 2 {
		t.Fatalf("expected 2 messages in history, got %d", len(e.history))
	}
	if e.history[0].Content != "补充信息1" {
		t.Errorf("first message = %q, want %q", e.history[0].Content, "补充信息1")
	}
	if e.history[1].Content != "补充信息2" {
		t.Errorf("second message = %q, want %q", e.history[1].Content, "补充信息2")
	}
	if e.history[0].Role != "user" {
		t.Errorf("first message role = %q, want %q", e.history[0].Role, "user")
	}
}

func TestDrainSteerQueue_Empty(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("drainSteerQueue should return false when queue is empty")
	}
	if len(e.history) != 0 {
		t.Fatalf("history should be empty, got %d messages", len(e.history))
	}
}

func TestSteer_EmptyString(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("")
	e.Steer("   ")

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("drainSteerQueue should return false when only empty strings were queued")
	}
}

func TestDrainSteerQueue_ClearsQueue(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("msg1")
	e.drainSteerQueue()

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("second drain should return false - queue was already cleared")
	}
}

func TestSteer_EmitsProgressEvent(t *testing.T) {
	var eventTypes []string
	e := &Engine{
		state: &TaskState{},
		config: EngineConfig{
			OnProgress: func(event ProgressEvent) {
				eventTypes = append(eventTypes, event.Type)
			},
		},
	}

	e.Steer("test message")

	if len(eventTypes) != 1 || eventTypes[0] != "steer_queued" {
		t.Fatalf("expected [steer_queued], got %v", eventTypes)
	}

	e.drainSteerQueue()

	if len(eventTypes) != 2 || eventTypes[1] != "steer_injected" {
		t.Fatalf("expected [steer_queued, steer_injected], got %v", eventTypes)
	}
}

func TestClearSessionState_ClearsSteerQueue(t *testing.T) {
	e := &Engine{
		state:           &TaskState{},
		history:         make([]Message, 0),
		activatedSkills: make(map[string]bool),
	}

	e.Steer("queued message")
	e.clearSessionState()

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("steer queue should be empty after clearSessionState")
	}
}
