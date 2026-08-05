package engine

import (
	"context"
	"testing"
	"time"
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

// multiTurnModel returns pre-configured chunk sets for each Stream call.
type multiTurnModel struct {
	turns   [][]ModelChunk
	callIdx int
}

func (m *multiTurnModel) Stream(_ context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	idx := m.callIdx
	m.callIdx++
	var chunks []ModelChunk
	if idx < len(m.turns) {
		chunks = m.turns[idx]
	} else {
		chunks = []ModelChunk{{Delta: "done", FinishReason: "stop", Usage: &ModelUsage{}}}
	}
	ch := make(chan ModelChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (m *multiTurnModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{FinishReason: "stop"}, nil
}

// steerContextBuilder is a minimal ContextBuilder for steer queue tests.
type steerContextBuilder struct{}

func (steerContextBuilder) Build(_ *TaskState, history []Message, _ []ToolResult) []ModelMessage {
	msgs := make([]ModelMessage, 0, len(history))
	for _, m := range history {
		msgs = append(msgs, ModelMessage{Role: m.Role, Content: m.Content})
	}
	return msgs
}

func (steerContextBuilder) EstimateTokens(msgs []ModelMessage) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content) / 4
	}
	return total
}

func (steerContextBuilder) SetActiveSkill(_, _ string) {}

func TestRun_DoneWithSteerQueue_AutoContinue(t *testing.T) {
	// Turn 1: model returns text-only (Done=true) -> steer queue has msg -> drain -> continue
	// Turn 2: model returns text-only (Done=true) -> steer queue empty -> break
	turn1Chunks := []ModelChunk{
		{Delta: "任务完成", FinishReason: "stop", Usage: &ModelUsage{}},
	}
	turn2Chunks := []ModelChunk{
		{Delta: "处理了补充信息", FinishReason: "stop", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{turn1Chunks, turn2Chunks}}
	e := &Engine{
		model:           model,
		tools:           stubToolExecutor{},
		context:         steerContextBuilder{},
		state:           &TaskState{TaskID: "test", ConfirmedScope: true},
		history:         []Message{{Role: "user", Content: "do something", Timestamp: time.Now()}},
		config:          EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:          &GuardSystem{scope: NewScopeGuard(true), loop: NewLoopGuard("", 6)},
		readLoop:        NewReadLoopState(),
		errorLoop:       NewErrorLoopState(0),
		activatedSkills: make(map[string]bool),
	}

	// Steer before Run - simulates UI calling Steer during a prior Blocked run.
	e.Steer("补充：也检查测试文件")

	resp, err := e.Run(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Run returned nil response")
	}

	// The steer message should be in history
	found := false
	for _, msg := range e.history {
		if msg.Content == "补充：也检查测试文件" {
			found = true
			break
		}
	}
	if !found {
		t.Error("steer message was not injected into history")
	}

	// The final summary should be from turn 2, not turn 1
	if resp.Summary == "任务完成" {
		t.Error("summary should be from the continued turn, not the initial Done turn")
	}
}
