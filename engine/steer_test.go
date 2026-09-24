package engine

import (
	"context"
	"strings"
	"sync"
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
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("queued message")
	e.clearSessionState()

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("steer queue should be empty after clearSessionState")
	}
}

// steerBlockingModel streams one chunk then waits for release before
// closing the channel. It simulates a model stream that is still in
// flight (blocked) when the user steers.
type steerBlockingModel struct {
	mu      sync.Mutex
	callIdx int
	// release is closed by the test to let the first stream finish
	release chan struct{}
}

func (m *steerBlockingModel) Stream(_ context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	m.mu.Lock()
	idx := m.callIdx
	m.callIdx++
	m.mu.Unlock()
	ch := make(chan ModelChunk)
	go func() {
		if idx == 0 {
			// First call: emit one chunk, then block until release.
			ch <- ModelChunk{Delta: "正在读A"}
			<-m.release
		} else {
			// Second call: complete immediately with the new direction.
			ch <- ModelChunk{Delta: "已转向去查看B文件的具体内容", FinishReason: "stop", Usage: &ModelUsage{}}
		}
		close(ch)
	}()
	return ch, nil
}

func (m *steerBlockingModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{FinishReason: "stop"}, nil
}

// TestRun_SteerInterruptsMidStream verifies the soft interruption: a steer
// message arriving while the model streams causes the current turn to be
// abandoned (no tools executed) and the Run loop to continue with the
// injected message on the next turn.
func TestRun_SteerInterruptsMidStream(t *testing.T) {
	model := &steerBlockingModel{release: make(chan struct{})}
	tools := &recordingToolExecutor{}
	e := &Engine{
		model:   model,
		tools:   tools,
		context: steerContextBuilder{},
		state:   &TaskState{TaskID: "test"},
		history: []Message{{Role: "user", Content: "读A", Timestamp: time.Now()}},
		config:  EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}

	// Run in a goroutine; it blocks on the first stream until we release.
	respCh := make(chan *EngineResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := e.Run(context.Background(), "读A")
		respCh <- resp
		errCh <- err
	}()

	// Wait until the first stream chunk has been consumed (turn started).
	deadline := time.Now().Add(5 * time.Second)
	for {
		model.mu.Lock()
		callCount := model.callIdx
		model.mu.Unlock()
		if callCount >= 1 && len(tools.executed) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for first stream chunk")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Steer while the stream is still in flight.
	e.Steer("去看B，别读A了")

	// Release the first stream; the soft interrupt should abandon it.
	close(model.release)

	resp := <-respCh
	if err := <-errCh; err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Run returned nil response")
	}

	// The steer message must be injected into history.
	found := false
	for _, msg := range e.history {
		if msg.Content == "去看B，别读A了" {
			found = true
			break
		}
	}
	if !found {
		t.Error("steer message was not injected into history")
	}

	// The interrupted first turn's tools must NOT have executed: the user
	// redirected before the plan ran.
	if len(tools.executed) != 0 {
		t.Errorf("tools executed during interrupted turn: %v", tools.executed)
	}

	// The Run must have continued and finished on turn 2 (which saw the
	// steer message and produced the new direction).
	if !strings.Contains(resp.Summary, "已转向去查看B文件的具体内容") {
		t.Errorf("final summary = %q, want continuation with '已转向去查看B文件的具体内容'", resp.Summary)
	}

	// The final assistant message should be the second turn's reply, not a
	// half-streamed first-turn fragment.
	var lastAssistant string
	for _, msg := range e.history {
		if msg.Role == "assistant" {
			lastAssistant = msg.Content
		}
	}
	if !strings.Contains(lastAssistant, "已转向去查看B文件的具体内容") {
		t.Errorf("last assistant content = %q, want '已转向去查看B文件的具体内容'", lastAssistant)
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
		model:   model,
		tools:   stubToolExecutor{},
		context: steerContextBuilder{},
		state:   &TaskState{TaskID: "test"},
		history: []Message{{Role: "user", Content: "do something", Timestamp: time.Now()}},
		config:  EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}

	// A steer queued before Run() is drained up front, before the first turn
	// (loop.go:295) — it never reaches the Done branch and therefore cannot
	// exercise auto-continue. Queue it during turn 1's post-processing instead.
	e.SetStopHooks([]StopHook{&steerOnceHook{steer: func() { e.Steer("补充：也检查测试文件") }}})

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

	// Auto-continue must actually have run a second turn, and the final summary
	// must be turn 2's text — not turn 1's "任务完成".
	if model.callIdx != 2 {
		t.Errorf("expected 2 turns (Done-branch auto-continue), got %d", model.callIdx)
	}
	if resp.Summary != "处理了补充信息" {
		t.Errorf("summary = %q, want the continued turn's text %q", resp.Summary, "处理了补充信息")
	}
}

// steerOnceHook queues a steer message exactly once, at the end of a turn's
// post-processing (after the stream has been fully consumed). That is the seam
// where the Run loop's Done branch picks the message up and continues with a
// fresh turn.
type steerOnceHook struct {
	once  sync.Once
	steer func()
}

func (h *steerOnceHook) Check(_ context.Context, _ StopHookContext) StopHookResult {
	h.once.Do(h.steer)
	return StopHookResult{}
}
