package llm

import (
	"testing"
)

func TestReasoningEchoManager_ObserveAndApply(t *testing.T) {
	mgr := NewReasoningEchoManager()

	mgr.ObserveAssistant(Message{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "thinking deeply...",
		ToolCalls:        []ToolCall{{ID: "call_1", Function: FunctionCall{Name: "bash"}}},
	})

	echo, ok := mgr.PendingEcho()
	if !ok {
		t.Fatal("expected pending echo")
	}
	if echo != "thinking deeply..." {
		t.Errorf("echo = %q, want %q", echo, "thinking deeply...")
	}

	msgs := []Message{
		{Role: "user", Content: "initial"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call_1"}}},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}

	msgs = mgr.ApplyEcho(msgs)

	if msgs[1].ReasoningContent != "thinking deeply..." {
		t.Errorf("applied reasoning = %q, want %q", msgs[1].ReasoningContent, "thinking deeply...")
	}

	_, ok = mgr.PendingEcho()
	if ok {
		t.Error("expected no pending echo after apply")
	}
}

func TestReasoningEchoManager_NoEchoWithoutToolCalls(t *testing.T) {
	mgr := NewReasoningEchoManager()

	mgr.ObserveAssistant(Message{
		Role:             "assistant",
		Content:          "just text",
		ReasoningContent: "some thinking",
		ToolCalls:        nil,
	})

	_, ok := mgr.PendingEcho()
	if ok {
		t.Error("should not need echo when no tool calls")
	}
}

func TestReasoningEchoManager_Clear(t *testing.T) {
	mgr := NewReasoningEchoManager()

	mgr.ObserveAssistant(Message{
		Role:             "assistant",
		ReasoningContent: "deep thoughts",
		ToolCalls:        []ToolCall{{ID: "call_x"}},
	})

	mgr.Clear()

	_, ok := mgr.PendingEcho()
	if ok {
		t.Error("expected no pending echo after Clear()")
	}
}

func TestReasoningEchoManager_SkipIfAlreadyPresent(t *testing.T) {
	mgr := NewReasoningEchoManager()

	mgr.ObserveAssistant(Message{
		Role:             "assistant",
		ReasoningContent: "new thinking",
		ToolCalls:        []ToolCall{{ID: "call_2"}},
	})

	msgs := []Message{
		{Role: "assistant", ReasoningContent: "already has reasoning", ToolCalls: []ToolCall{{ID: "call_2"}}},
		{Role: "tool", ToolCallID: "call_2", Content: "ok"},
	}

	msgs = mgr.ApplyEcho(msgs)

	if msgs[0].ReasoningContent != "already has reasoning" {
		t.Error("should not overwrite existing reasoning_content")
	}
}

func TestApplyEchoIfNeeded(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "do something"},
		{Role: "assistant", ReasoningContent: "I think...", ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "grep"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "found it"},
	}

	result := applyEchoIfNeeded(msgs)
	if result[1].ReasoningContent != "I think..." {
		t.Errorf("reasoning preserved = %q, want %q", result[1].ReasoningContent, "I think...")
	}
}

func TestApplyEchoIfNeeded_NoToolCalls(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ReasoningContent: "short thought"},
	}

	result := applyEchoIfNeeded(msgs)
	if result[1].ReasoningContent != "short thought" {
		t.Errorf("reasoning = %q, should be unchanged", result[1].ReasoningContent)
	}
}
