package ui

import (
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

// TestForwardProgress_StateEventOutlivesBackpressure: progressCh 满（UI 正在渲染、
// 读取节奏落后）时，todo_update 这类状态事件不得像文本增量那样被丢弃。丢一条
// delta 只丢一段已经显示过的文字；丢一条 todo_update 会让步骤面板停在模型已经
// 覆盖掉的旧快照上——读起来就是"执行结束后不打勾"。
func TestForwardProgress_StateEventOutlivesBackpressure(t *testing.T) {
	ch := make(chan ProgressMsg, 1)
	ch <- ProgressMsg{Type: "content_delta", Detail: "占满缓冲，模拟背压"}
	r := &ProgressEngineRunner{progressCh: ch}

	done := make(chan struct{})
	go func() {
		r.forwardProgress(engine.ProgressEvent{
			Type:  "todo_update",
			Todos: []engine.TodoItem{{ID: "s1", Content: "步骤一", Status: "completed"}},
		})
		close(done)
	}()

	// 等到超过 delta 的丢弃阈值，再腾出缓冲。旧实现（一刀切 100ms）此时已经放弃投递。
	time.Sleep(3 * progressDeltaTimeout)
	<-ch

	select {
	case msg := <-ch:
		if msg.Type != "todo_update" || len(msg.Todos) != 1 || msg.Todos[0].Status != "completed" {
			t.Fatalf("got %+v, want the completed todo_update snapshot", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("todo_update was dropped because the UI was 100ms late")
	}
	<-done
}
