package engine

import (
	"encoding/json"
	"testing"
)

// TestRebuildHistory 验证 RebuildHistory：过滤 message 事件、跳过 tool、
// 剥离 ToolCalls、按 budget 裁剪（user 边界）。
func TestRebuildHistory(t *testing.T) {
	events := []Event{
		{Type: "user_message", Payload: jsonRaw(`"旧事件被忽略"`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"问题1"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答1"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"tool","tool_call_id":"c1","content":"工具结果"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答2","tool_calls":[{"id":"c1","name":"grep","arguments":"{}"}]}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"问题3"}`)},
	}
	msgs := RebuildHistory(events, 1<<30) // 大预算：全保留
	// 期望：user/assistant 文本流，tool 被跳过，tool_calls 被剥离
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d, want 4 (user,assistant,assistant,user)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "问题1" {
		t.Errorf("msgs[0] = %+v, want user 问题1", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "回答1" {
		t.Errorf("msgs[1] = %+v, want assistant 回答1", msgs[1])
	}
	if msgs[2].Role != "assistant" || msgs[2].Content != "回答2" {
		t.Errorf("msgs[2] = %+v, want assistant 回答2", msgs[2])
	}
	if len(msgs[2].ToolCalls) != 0 {
		t.Errorf("msgs[2].ToolCalls = %+v, want stripped", msgs[2].ToolCalls)
	}
	if msgs[3].Role != "user" || msgs[3].Content != "问题3" {
		t.Errorf("msgs[3] = %+v, want user 问题3", msgs[3])
	}
}

// TestRebuildHistoryTrimsToBudget 验证 budget 裁剪且在 user 边界切割。
func TestRebuildHistoryTrimsToBudget(t *testing.T) {
	events := []Event{
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"用户A"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答A"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"用户B"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答B"}`)},
	}
	// budget 小到只能容纳最后 2 条 → 裁剪应从 user（用户B）开始，保留 2 条
	msgs := RebuildHistory(events, tokenCount("用户B")+tokenCount("回答B"))
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2 (cut at user boundary)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "用户B" {
		t.Errorf("msgs[0] = %+v, want user 用户B (cut at user boundary)", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "回答B" {
		t.Errorf("msgs[1] = %+v, want assistant 回答B", msgs[1])
	}
}

// TestRebuildHistoryEmpty 验证无 message 事件返回空。
func TestRebuildHistoryEmpty(t *testing.T) {
	events := []Event{{Type: "user_message", Payload: jsonRaw(`"x"`)}}
	if msgs := RebuildHistory(events, 16384); len(msgs) != 0 {
		t.Fatalf("len(msgs) = %d, want 0", len(msgs))
	}
}

// TestSetSessionIDAndHistory 验证 SetSessionID / SetHistory 状态迁移。
func TestSetSessionIDAndHistory(t *testing.T) {
	e := &Engine{config: EngineConfig{SessionID: "old"}, state: &TaskState{TaskID: "old"}}
	e.SetSessionID("new-id")
	if e.config.SessionID != "new-id" || e.state.TaskID != "new-id" {
		t.Errorf("SetSessionID not applied: config=%s state=%s", e.config.SessionID, e.state.TaskID)
	}
	hist := []Message{{Role: "user", Content: "预载"}}
	e.SetHistory(hist)
	if len(e.history) != 1 || e.history[0].Content != "预载" {
		t.Errorf("SetHistory not applied: %+v", e.history)
	}
	if e.persistedCount != 1 {
		t.Errorf("persistedCount = %d, want 1 (preloaded history not re-persisted)", e.persistedCount)
	}
}

// jsonRaw 构造测试用 JSON raw payload。
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}
