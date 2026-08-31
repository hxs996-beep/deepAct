package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessHandoffResults_FailedReasonPinsFollowUp 锁定 C6：
// 子代理以失败 reason（no_result/max_tokens/loop_detected/stalled_narration）
// 结束时，父代理必须 pin 一条 follow-up 指令（下一回合自动看到并继续处理），
// 而不是把残缺结论直接抛给用户、等用户输入"继续"。
// 当前实现不 pin 任何 follow-up → 红灯。
func TestProcessHandoffResults_FailedReasonPinsFollowUp(t *testing.T) {
	e := &Engine{isChinese: true}
	handoffCalls := []ToolCallRequest{{
		ID: "h1", Name: HandoffToolName,
		Input: json.RawMessage(`{"agent":"sub","goal":"审查实现"}`),
	}}
	results := []ToolResult{{
		ToolCallID:   "h1",
		ToolName:     HandoffToolName,
		Status:       "ok",
		Digest:       "子代理未提交结果（下方为部分答案）：\n只找到一半证据",
		FinishReason: HandoffReasonNoResult,
	}}

	msgs, criticFail := e.processHandoffResults(handoffCalls, results, nil)
	if criticFail != "" {
		t.Fatalf("unexpected criticFail: %q", criticFail)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(msgs))
	}
	// digest 不得宣称完成
	if strings.Contains(msgs[0].Content, "Agent completed") {
		t.Errorf("failed handoff digest must not claim completion: %q", msgs[0].Content)
	}
	// 必须 pin follow-up，让父代理同一 Run 内自动继续处理
	if len(e.pendingPinnedMessages) == 0 {
		t.Errorf("expected a pinned follow-up so the parent auto-continues, got none")
	}
}

// TestProcessHandoffResults_CompletedNoFollowUp 锁定 C6 的反面：
// 子代理正常完成（completed）时不得 pin follow-up——那是正常路径，不需要父代理干预。
func TestProcessHandoffResults_CompletedNoFollowUp(t *testing.T) {
	e := &Engine{isChinese: true}
	handoffCalls := []ToolCallRequest{{
		ID: "h1", Name: HandoffToolName,
		Input: json.RawMessage(`{"agent":"sub","goal":"审查实现"}`),
	}}
	results := []ToolResult{{
		ToolCallID:   "h1",
		ToolName:     HandoffToolName,
		Status:       "ok",
		Digest:       "Agent completed: 结论",
		FinishReason: HandoffReasonCompleted,
	}}

	msgs, _ := e.processHandoffResults(handoffCalls, results, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(msgs))
	}
	if len(e.pendingPinnedMessages) != 0 {
		t.Errorf("expected NO follow-up on completed handoff, got %v", e.pendingPinnedMessages)
	}
}

// TestProcessHandoffResults_CancelledNoFollowUp 锁定 C6 的边界：
// 子代理被取消（cancelled）时同样不 pin follow-up——取消是用户/上下文决定，不是失败。
func TestProcessHandoffResults_CancelledNoFollowUp(t *testing.T) {
	e := &Engine{isChinese: true}
	handoffCalls := []ToolCallRequest{{
		ID: "h1", Name: HandoffToolName,
		Input: json.RawMessage(`{"agent":"sub","goal":"审查实现"}`),
	}}
	results := []ToolResult{{
		ToolCallID:   "h1",
		ToolName:     HandoffToolName,
		Status:       "cancelled",
		Digest:       "Sub-agent cancelled.",
		FinishReason: HandoffReasonCancelled,
	}}

	msgs, _ := e.processHandoffResults(handoffCalls, results, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(msgs))
	}
	if len(e.pendingPinnedMessages) != 0 {
		t.Errorf("expected NO follow-up on cancelled handoff, got %v", e.pendingPinnedMessages)
	}
}
