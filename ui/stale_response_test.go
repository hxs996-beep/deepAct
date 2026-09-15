package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestStaleCancelledResponseDropped 复现"运行中自己弹出 任务已取消。"的根因：
// 用户 Esc 取消 Run A 后立即提交 Run B（m.cancelled 被 submitInput 重置为
// false），Run A 的 cancelled EngineResponseMsg 随后才到达。旧的全局
// m.cancelled 守卫已失效——RunSeq 不匹配必须把这条陈旧响应直接丢弃，
// 不能让它结束 Run B 或显示"任务已取消。"。
func TestStaleCancelledResponseDropped(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateRunning,
		runSeq:   2,     // 当前 Run B 的 seq
		cancelled: false, // Run B 启动后 cancelled 被重置
		msgCache: &messageRenderCache{},
	}

	got, cmd := m.Update(EngineResponseMsg{
		Response: &engine.EngineResponse{
			Summary:      "任务已取消。",
			Blocked:      true,
			BlockedBy:    "cancelled",
			FinishReason: "cancelled",
		},
		RunSeq: 1, // Run A 的陈旧响应
	})
	if cmd != nil {
		t.Fatalf("stale response must not schedule any command, got %v", cmd)
	}
	gm := got.(Model)
	// 陈旧响应不得改变当前 run 的状态
	if gm.state != stateRunning {
		t.Fatalf("state = %v, want stateRunning (stale response must be dropped)", gm.state)
	}
	// 陈旧响应不得把取消文本写入消息流
	for _, msg := range gm.messages {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "任务已取消") {
			t.Fatalf("stale cancelled response leaked into messages: %+v", gm.messages)
		}
	}
}

// TestCurrentRunResponseProcessed 对照：RunSeq 匹配的响应必须正常结束 run。
func TestCurrentRunResponseProcessed(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateRunning,
		runSeq:   2,
		cancelled: false,
		msgCache: &messageRenderCache{},
	}

	got, cmd := m.Update(EngineResponseMsg{
		Response: &engine.EngineResponse{Summary: "正常完成"},
		RunSeq:   2,
	})
	if cmd == nil {
		t.Fatal("matching response must schedule a command (repaint)")
	}
	gm := got.(Model)
	if gm.state != stateReady {
		t.Fatalf("state = %v, want stateReady (matching response must be processed)", gm.state)
	}
	found := false
	for _, msg := range gm.messages {
		if msg.Role == "assistant" && msg.Content == "正常完成" {
			found = true
		}
	}
	if !found {
		t.Fatalf("matching response summary not displayed: %+v", gm.messages)
	}
}

// TestRunSeqIncrementsOnSubmit 验证每次提交都会推进 runSeq，确保连续 run
// 的响应可被区分。
func TestRunSeqIncrementsOnSubmit(t *testing.T) {
	m := NewModel(&recordingRunner{}, engine.PricingConfig{})
	if m.runSeq != 0 {
		t.Fatalf("initial runSeq = %d, want 0", m.runSeq)
	}
	m.state = stateReady
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("第一个问题")

	got, _ := m.submitInput()
	m1 := got.(Model)
	if m1.runSeq != 1 {
		t.Fatalf("runSeq after first submit = %d, want 1", m1.runSeq)
	}
	if m1.state != stateRunning {
		t.Fatalf("state = %v, want stateRunning", m1.state)
	}
}
