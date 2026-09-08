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

// TestRebuildHistory_BudgetFitsIsolatedAssistant 验证预算只够一条孤立 assistant
// 且剩余窗口内没有 user 消息时，保持越界点之后的位置，不引入超预算的越界消息。
// （对应 resume.go RebuildHistory 的注释分支："若剩余窗口内没有 user 消息...保持越界点之后的位置"）
func TestRebuildHistory_BudgetFitsIsolatedAssistant(t *testing.T) {
	events := []Event{
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"用户A"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答A"}`)},
	}
	// budget 只够 assistant 一条：向后累计到 user 时超预算 → overflow=0；
	// 窗口 msgs[1:] 内无 user → 保持从 1 开始，返回孤立 assistant。
	msgs := RebuildHistory(events, tokenCount("回答A"))
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1 (isolated assistant kept)", len(msgs))
	}
	if msgs[0].Role != "assistant" || msgs[0].Content != "回答A" {
		t.Errorf("msgs[0] = %+v, want assistant 回答A", msgs[0])
	}
}

// TestRebuildHistory_BudgetZero 验证 budget<=0 返回全部消息（不过滤）。
func TestRebuildHistory_BudgetZero(t *testing.T) {
	events := []Event{
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"问题"}`)},
	}
	msgs := RebuildHistory(events, 0)
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1 (budget<=0 keeps all)", len(msgs))
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

// fakeMemStore implements MemoryStore for testing lazy persistent-memory loading.
type fakeMemStore struct {
	snap *MemorySnapshot
}

func (f *fakeMemStore) Load() (*MemorySnapshot, error) { return f.snap, nil }
func (f *fakeMemStore) Save(s *MemorySnapshot) error   { return nil }
func (f *fakeMemStore) Clear() error                   { return nil }

// TestPersistentMemoryLoadedOnlyOnResume 验证方案 A 的核心语义：
// 新引擎（无 SetHistory / 无 /resume）绝不加载跨会话 memory ——
// 因此 Block B 的 memory_markers / decisions 为空，不残留上个任务内容。
func TestPersistentMemoryLoadedOnlyOnResume(t *testing.T) {
	snap := &MemorySnapshot{
		MemoryMarkers: []string{"上个任务的调研结论"},
		Decisions:     []Decision{{ID: "d-1", Text: "上个任务的决策"}},
		OpenQuestions: []string{"上个任务的问题"},
		Assumptions:   []string{"上个任务的假设"},
	}
	e := &Engine{
		state:  &TaskState{},
		memory: &fakeMemStore{snap: snap},
	}
	// 新会话：未调用 SetHistory，memory 必须保持未加载。
	if len(e.state.MemoryMarkers) != 0 || len(e.state.Decisions) != 0 {
		t.Fatalf("fresh session must not load persistent memory: markers=%v decisions=%v",
			e.state.MemoryMarkers, e.state.Decisions)
	}
	// /resume 路径：SetHistory 懒加载跨会话 memory 并合并进 TaskState。
	e.SetHistory([]Message{{Role: "user", Content: "恢复会话"}})
	if len(e.state.MemoryMarkers) != 1 || e.state.MemoryMarkers[0] != "上个任务的调研结论" {
		t.Errorf("resume should load markers, got %v", e.state.MemoryMarkers)
	}
	if len(e.state.Decisions) != 1 || e.state.Decisions[0].Text != "上个任务的决策" {
		t.Errorf("resume should load decisions, got %v", e.state.Decisions)
	}
	if len(e.state.OpenQuestions) != 1 || len(e.state.Assumptions) != 1 {
		t.Errorf("resume should load open questions and assumptions: %v %v",
			e.state.OpenQuestions, e.state.Assumptions)
	}
}

// TestPersistentMemoryLoadIdempotent 验证 loadPersistentMemory 幂等：
// 重复 SetHistory 不重复合并，避免 markers/decisions 翻倍。
func TestPersistentMemoryLoadIdempotent(t *testing.T) {
	snap := &MemorySnapshot{
		MemoryMarkers: []string{"结论A"},
		Decisions:     []Decision{{ID: "d-1", Text: "决策A"}},
	}
	e := &Engine{
		state:  &TaskState{},
		memory: &fakeMemStore{snap: snap},
	}
	e.SetHistory([]Message{{Role: "user", Content: "恢复"}})
	e.SetHistory([]Message{{Role: "user", Content: "再次恢复"}})
	if len(e.state.MemoryMarkers) != 1 {
		t.Errorf("markers duplicated after repeat SetHistory: %v", e.state.MemoryMarkers)
	}
	if len(e.state.Decisions) != 1 {
		t.Errorf("decisions duplicated after repeat SetHistory: %v", e.state.Decisions)
	}
}

// TestPersistentMemoryNilStoreSafe 验证 memory 为 nil（未注入）时
// SetHistory 不 panic，兼容无 memory 的测试环境。
func TestPersistentMemoryNilStoreSafe(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	e.SetHistory([]Message{{Role: "user", Content: "x"}})
	if len(e.state.MemoryMarkers) != 0 {
		t.Errorf("nil store must not load markers: %v", e.state.MemoryMarkers)
	}
}

// jsonRaw 构造测试用 JSON raw payload。
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}
