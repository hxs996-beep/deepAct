package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProcessTodoWriteCalls_Valid verifies a valid todo_write call forwards
// the full snapshot to OnProgress AND produces a tool response message
// (preventing orphaned tool_call_ids).
func TestProcessTodoWriteCalls_Valid(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_todo", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [
				{"content": "红灯 - 编写失败的测试", "status": "in_progress"},
				{"content": "重构 - 清理代码", "status": "pending"}
			]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d — tool_call %q is orphaned", len(msgs), "call_todo")
	}
	if msgs[0].ToolCallID != "call_todo" || msgs[0].Role != "tool" {
		t.Errorf("response = %+v, want ToolCallID=call_todo Role=tool", msgs[0])
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 progress event, got %d", len(got))
	}
	if got[0].Type != "todo_update" {
		t.Errorf("event Type = %q, want todo_update", got[0].Type)
	}
	if len(got[0].Todos) != 2 {
		t.Fatalf("event Todos len = %d, want 2", len(got[0].Todos))
	}
	if got[0].Todos[0].Content != "红灯 - 编写失败的测试" || got[0].Todos[0].Status != "in_progress" {
		t.Errorf("Todos[0] = %+v, want content=红灯... status=in_progress", got[0].Todos[0])
	}
}

// TestProcessTodoWriteCalls_InvalidStatus verifies an invalid status produces
// an error response and NO progress event.
func TestProcessTodoWriteCalls_InvalidStatus(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_bad", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [{"content": "step", "status": "almost_done"}]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "invalid todo status") {
		t.Errorf("Content = %q, want invalid todo status error", msgs[0].Content)
	}
	if len(got) != 0 {
		t.Errorf("expected no progress event on invalid status, got %d", len(got))
	}
}

// TestProcessTodoWriteCalls_EmptyContent verifies empty content is rejected.
func TestProcessTodoWriteCalls_EmptyContent(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	calls := []ToolCallRequest{
		{ID: "call_empty", Name: TodoWriteToolName, Input: json.RawMessage(`{
			"todos": [{"content": "  ", "status": "pending"}]
		}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "non-empty content") {
		t.Errorf("Content = %q, want non-empty content error", msgs[0].Content)
	}
}

// TestProcessTodoWriteCalls_BadJSON verifies malformed input gets an error
// response and no event.
func TestProcessTodoWriteCalls_BadJSON(t *testing.T) {
	var got []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) { got = append(got, ev) }}}

	calls := []ToolCallRequest{
		{ID: "call_badjson", Name: TodoWriteToolName, Input: json.RawMessage(`{invalid}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "invalid todo_write arguments") {
		t.Errorf("Content = %q, want invalid arguments error", msgs[0].Content)
	}
	if len(got) != 0 {
		t.Errorf("expected no progress event on bad JSON, got %d", len(got))
	}
}

// TestProcessTodoWriteCalls_Mixed verifies only todo_write calls get responses
// from this function (regular calls are handled elsewhere).
func TestProcessTodoWriteCalls_Mixed(t *testing.T) {
	e := &Engine{config: EngineConfig{}}

	calls := []ToolCallRequest{
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"x.go"}`)},
		{ID: "call_todo", Name: TodoWriteToolName, Input: json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`)},
	}

	msgs := e.processTodoWriteCalls(calls)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response (todo_write only), got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_todo" {
		t.Errorf("ToolCallID = %q, want call_todo", msgs[0].ToolCallID)
	}
}
