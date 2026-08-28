package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// mockSessionStore 记录 AppendEvent 调用，供持久化测试断言。
type mockSessionStore struct {
	events []Event
}

func (m *mockSessionStore) AppendEvent(e Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *mockSessionStore) LoadEvents(sessionID string) ([]Event, error) {
	return m.events, nil
}

// TestPersistHistoryWritesMessageEvents 验证 persistHistory 把 history 落盘为
// message 事件：user/assistant 全文、tool 摘要、reasoning_content 不落盘。
func TestPersistHistoryWritesMessageEvents(t *testing.T) {
	store := &mockSessionStore{}
	e := &Engine{
		session: store,
		config:  EngineConfig{SessionID: "sess-1", WorkDir: "/proj"},
		state:   &TaskState{},
		history: []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好，需要什么帮助？", ReasoningContent: "思考中"},
			{Role: "tool", ToolCallID: "call-1", Content: "第一行\n第二行\n第三行\n"},
		},
	}
	e.persistHistory()

	var msgs []Message
	for _, ev := range store.events {
		if ev.Type != EventTypeMessage {
			t.Fatalf("event type = %q, want %q", ev.Type, EventTypeMessage)
		}
		if ev.WorkDir != "/proj" {
			t.Errorf("ev.WorkDir = %q, want /proj", ev.WorkDir)
		}
		var m Message
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "你好" {
		t.Errorf("msgs[0] = %+v, want user 你好", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "你好，需要什么帮助？" {
		t.Errorf("msgs[1] = %+v, want full assistant content", msgs[1])
	}
	if msgs[1].ReasoningContent != "" {
		t.Errorf("msgs[1].ReasoningContent = %q, want empty", msgs[1].ReasoningContent)
	}
	if msgs[2].Role != "tool" || !strings.Contains(msgs[2].Content, "第一行") || !strings.Contains(msgs[2].Content, "3 lines") {
		t.Errorf("msgs[2] = %+v, want brief digest (first line + line count)", msgs[2])
	}
}

// TestPersistHistoryResumesFromPersistedCount 验证第二次 persistHistory 只写新增消息。
func TestPersistHistoryResumesFromPersistedCount(t *testing.T) {
	store := &mockSessionStore{}
	e := &Engine{
		session: store,
		config:  EngineConfig{SessionID: "sess-1"},
		state:   &TaskState{},
		history: []Message{{Role: "user", Content: "第一条"}},
	}
	e.persistHistory()
	if len(store.events) != 1 {
		t.Fatalf("first persist: %d events, want 1", len(store.events))
	}
	e.history = append(e.history, Message{Role: "assistant", Content: "回复"})
	e.persistHistory()
	if len(store.events) != 2 {
		t.Fatalf("second persist: %d events, want 2 (only new)", len(store.events))
	}
}

// TestPersistHistoryResetAfterCompaction 验证 history 被压缩替换（persistedCount
// 大于新 history 长度）时，persistHistory 重置为 0 并从 0 重写，不越界。
func TestPersistHistoryResetAfterCompaction(t *testing.T) {
	store := &mockSessionStore{}
	e := &Engine{
		session: store,
		config:  EngineConfig{SessionID: "sess-1"},
		state:   &TaskState{},
		history: []Message{{Role: "user", Content: "第一条"}},
	}
	e.persistHistory()
	if len(store.events) != 1 {
		t.Fatalf("first persist: %d events, want 1", len(store.events))
	}
	// 模拟压缩：history 被替换为更短的摘要。persistedCount=1 > len=0，
	// 触发越界重置分支将 persistedCount 置 0；随后遍历空 history 无事件写入。
	e.history = []Message{} // 压缩后 history 为空
	e.persistHistory()
	if len(store.events) != 1 {
		t.Fatalf("after compaction: %d events, want 1 (no rewrite for empty history)", len(store.events))
	}
}
