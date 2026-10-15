package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// interruptBlockingModel blocks inside Stream until its ctx is cancelled and
// then reports the cancellation as a stream error — the shape of a real Esc
// press: the llm layer turns the context error into Chunk{Err}.
type interruptBlockingModel struct {
	started chan struct{}
}

func (m *interruptBlockingModel) Stream(ctx context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	ch := make(chan ModelChunk, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- ModelChunk{Err: ctx.Err()}
	}()
	return ch, nil
}

func (m *interruptBlockingModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{}, nil
}

// newInterruptTestEngine 构造一个 Run 所需的最小 Engine（字段集合与
// progress_loop_test.go 的用例一致）。
func newInterruptTestEngine(model ModelClient) *Engine {
	return &Engine{
		model:   model,
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}
}

// countInterruptMarkers 统计 history 中带中断标记的消息条数。
func countInterruptMarkers(e *Engine) int {
	n := 0
	for _, m := range e.history {
		if strings.Contains(m.Content, "<turn_aborted>") {
			n++
		}
	}
	return n
}

// TestRun_CancelledRunAppendsInterruptMarker：ctx 已取消的 Run 必须在 history
// 末尾留下一条 user 角色的中断标记，且只留一条。
func TestRun_CancelledRunAppendsInterruptMarker(t *testing.T) {
	e := newInterruptTestEngine(&stubStreamModel{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := e.Run(ctx, "跑一个长任务")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp == nil || resp.BlockedBy != "cancelled" {
		t.Fatalf("resp = %+v, want BlockedBy=cancelled", resp)
	}
	if got := countInterruptMarkers(e); got != 1 {
		t.Fatalf("interrupt markers in history = %d, want 1 (history=%+v)", got, e.history)
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("last message role = %q, want user", last.Role)
	}
	if !strings.Contains(last.Content, "interrupted the previous turn") {
		t.Errorf("last message = %q, want the interrupt guidance text", last.Content)
	}
}

// TestRun_InterruptMidStreamAppendsMarker：运行中（流式生成期间）被取消同样
// 留下标记——这是真实 Esc 的形态。
func TestRun_InterruptMidStreamAppendsMarker(t *testing.T) {
	model := &interruptBlockingModel{started: make(chan struct{}, 1)}
	e := newInterruptTestEngine(model)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Run(ctx, "跑一个长任务")
	}()

	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model stream was never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if got := countInterruptMarkers(e); got != 1 {
		t.Fatalf("interrupt markers in history = %d, want 1 (history=%+v)", got, e.history)
	}
}

// TestRun_InterruptMarkerPersistedToSession：标记必须随同一 Run 的落盘路径写入
// session。这条锁住 defer 的注册顺序（LIFO → noteUserInterrupt 先于
// persistHistory 执行）；顺序写反时标记既不进请求也不进 session。
func TestRun_InterruptMarkerPersistedToSession(t *testing.T) {
	store := &mockSessionStore{}
	e := newInterruptTestEngine(&stubStreamModel{})
	e.session = store
	e.config.SessionID = "sess-1"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx, "跑一个长任务"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	for _, ev := range store.events {
		if ev.Type != EventTypeMessage {
			continue
		}
		var m Message
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if strings.Contains(m.Content, "<turn_aborted>") {
			return
		}
	}
	t.Fatalf("no <turn_aborted> message event persisted; events=%+v", store.events)
}
