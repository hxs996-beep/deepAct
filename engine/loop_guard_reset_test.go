package engine

import (
	"context"
	"encoding/json"
	"testing"
)

// TestRun_ResetsLoopGuardOnNewRun reproduces the "stuck mid-task" bug:
// LoopGuard's read counts accumulated across Runs (the engine is reused via
// once.Do in the runner, and Reset() was never called). A user retrying or
// revisiting a task re-reads the same core files; cross-Run accumulation
// reached maxRepeats=6 and falsely blocked normal reads as a "loop". Each
// new Run must reset read-loop tracking so reads are evaluated fresh within
// the Run (where ReadLoopState still catches true 4th-same-read loops).
func TestRun_ResetsLoopGuardOnNewRun(t *testing.T) {
	e := &Engine{
		model:    &stubStreamModel{chunks: []ModelChunk{{Delta: "已完成。", FinishReason: "stop"}}},
		context:  &stubContextBuilder{},
		tools:    stubToolExecutor{},
		state:    &TaskState{TurnNumber: 0, Goal: ""},
		history:  []Message{},
		config:   EngineConfig{ModelName: "test-model"},
		guards:   &GuardSystem{loop: NewLoopGuard("", 6)},
		readLoop: NewReadLoopState(),
	}
	call := ToolCallRequest{Name: "read", Input: json.RawMessage(`{"file_path":"loop.go"}`)}
	// Preload: 6 reads reach the block threshold (maxRepeats=6).
	for i := 0; i < 6; i++ {
		e.guards.loop.Check(call)
	}
	// 7th check blocks before Run.
	if a := e.guards.loop.Check(call); a.Type != GuardBlock {
		t.Fatalf("preload: expected block after 6 reads (count=7), got %v", a.Type)
	}
	// Run must reset LoopGuard.
	if _, err := e.Run(context.Background(), "新任务：分析代码"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// After Run, first check should Allow (count reset).
	action := e.guards.loop.Check(call)
	if action.Type != GuardAllow {
		t.Errorf("after Run, LoopGuard should be reset (Allow), got %v (msg=%s)", action.Type, action.Message)
	}
}

// TestRun_ResetsReadLoopStateOnNewRun verifies the same reset for
// ReadLoopState (the per-(path,scope) read counter used in the loop body).
func TestRun_ResetsReadLoopStateOnNewRun(t *testing.T) {
	e := &Engine{
		model:    &stubStreamModel{chunks: []ModelChunk{{Delta: "已完成。", FinishReason: "stop"}}},
		context:  &stubContextBuilder{},
		tools:    stubToolExecutor{},
		state:    &TaskState{TurnNumber: 0, Goal: ""},
		history:  []Message{},
		config:   EngineConfig{ModelName: "test-model"},
		guards:   &GuardSystem{loop: NewLoopGuard("", 6)},
		readLoop: NewReadLoopState(),
	}
	// Preload ReadLoopState to the block tier (4th same key blocks).
	key := "read:loop.go::"
	for i := 0; i < 3; i++ {
		e.readLoop.Check(key)
	}
	if a := e.readLoop.Check(key); a.Type != GuardBlock {
		t.Fatalf("preload: expected block on 4th read, got %v", a.Type)
	}
	if _, err := e.Run(context.Background(), "新任务"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	// After Run, first check should Allow (count reset).
	if a := e.readLoop.Check(key); a.Type != GuardAllow {
		t.Errorf("after Run, ReadLoopState should be reset (Allow), got %v", a.Type)
	}
}
