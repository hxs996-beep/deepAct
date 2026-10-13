package engine

import (
	"context"
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

// TestProcessTodoWriteCalls_DuplicateIDRejected: id 是分组键，重复会让两个步骤
// 争夺同一批子代理，必须在入口 fail loud。
func TestProcessTodoWriteCalls_DuplicateIDRejected(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) {
		events = append(events, ev)
	}}}

	msgs := e.processTodoWriteCalls([]ToolCallRequest{{
		ID: "call_dup", Name: TodoWriteToolName,
		Input: json.RawMessage(`{"todos":[
			{"id":"dir-1","content":"方向1","status":"in_progress"},
			{"id":"dir-1","content":"方向2","status":"pending"}
		]}`),
	}})

	if len(msgs) != 1 {
		t.Fatalf("msgs len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "duplicate") {
		t.Errorf("message = %q, want a duplicate-id error", msgs[0].Content)
	}
	for _, ev := range events {
		if ev.Type == "todo_update" {
			t.Error("a rejected snapshot must not reach the UI")
		}
	}
}

// TestExecuteTurn_TodoUpdateSurvivesTaskComplete: "标全部 completed + task_complete"
// 是最常见的收尾形态。todo_write 的拦截必须早于 task_complete 的早退，否则最后
// 一份快照永远到不了 UI——步骤面板会停在旧状态，正是"执行结束后不打勾"。
func TestExecuteTurn_TodoUpdateSurvivesTaskComplete(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "全部完成",
			ToolCalls: []ModelToolCall{
				{ID: "c_todo", Type: "function", Function: ModelFunctionCall{
					Name:      TodoWriteToolName,
					Arguments: `{"todos":[{"content":"步骤一","status":"completed"}]}`,
				}},
				{ID: "c_done", Type: "function", Function: ModelFunctionCall{
					Name: TaskCompleteToolName, Arguments: `{"summary":"done"}`,
				}},
			},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "做完"}},
		config: EngineConfig{
			ModelName:  "test-model",
			OnProgress: func(ev ProgressEvent) { events = append(events, ev) },
		},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Fatalf("task_complete must end the turn, got %+v", result)
	}

	var todos []ProgressEvent
	for _, ev := range events {
		if ev.Type == "todo_update" {
			todos = append(todos, ev)
		}
	}
	if len(todos) != 1 {
		t.Fatalf("todo_update must reach the UI even when the same message calls task_complete; events = %+v", events)
	}
	if got := todos[0].Todos[0].Status; got != "completed" {
		t.Errorf("final snapshot status = %q, want completed", got)
	}
}

// TestExecuteTurn_TodoUpdateSurvivesBlockedCall: guard 拦截（危险 bash）同样在
// todo_write 拦截之前早退。同一轮携带的步骤快照不得因此丢失。
func TestExecuteTurn_TodoUpdateSurvivesBlockedCall(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "更新步骤后执行危险命令",
			ToolCalls: []ModelToolCall{
				{ID: "c_todo", Type: "function", Function: ModelFunctionCall{
					Name:      TodoWriteToolName,
					Arguments: `{"todos":[{"content":"步骤一","status":"completed"}]}`,
				}},
				{ID: "c_bash", Type: "function", Function: ModelFunctionCall{
					Name: "bash", Arguments: `{"command":"rm -rf /"}`,
				}},
			},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "危险操作"}},
		config: EngineConfig{
			ModelName:  "test-model",
			OnProgress: func(ev ProgressEvent) { events = append(events, ev) },
		},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Blocked {
		t.Fatalf("dangerous bash must be blocked, got %+v", result)
	}

	var todos []ProgressEvent
	for _, ev := range events {
		if ev.Type == "todo_update" {
			todos = append(todos, ev)
		}
	}
	if len(todos) != 1 {
		t.Fatalf("todo_update must reach the UI even when the same message is blocked; events = %+v", events)
	}
}

// TestProcessTodoWriteCalls_UniqueIDsAccepted: 正常路径必须原样透传 id。
func TestProcessTodoWriteCalls_UniqueIDsAccepted(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{config: EngineConfig{OnProgress: func(ev ProgressEvent) {
		events = append(events, ev)
	}}}

	e.processTodoWriteCalls([]ToolCallRequest{{
		ID: "call_ok", Name: TodoWriteToolName,
		Input: json.RawMessage(`{"todos":[
			{"id":"dir-1","content":"方向1","status":"in_progress"},
			{"content":"汇总","status":"pending"}
		]}`),
	}})

	if len(events) != 1 || events[0].Type != "todo_update" {
		t.Fatalf("events = %+v, want one todo_update", events)
	}
	if got := events[0].Todos[0].ID; got != "dir-1" {
		t.Errorf("Todos[0].ID = %q, want dir-1", got)
	}
}

// taskCompleteTodoChunks is the收尾 shape that the nudge exists for: the model
// declares completion in the same message where it reports a step as still
// running.
func taskCompleteTodoChunks(todos string) []ModelChunk {
	return []ModelChunk{{
		Delta: "收尾",
		ToolCalls: []ModelToolCall{
			{ID: "c_todo", Type: "function", Function: ModelFunctionCall{
				Name: TodoWriteToolName, Arguments: todos,
			}},
			{ID: "c_done", Type: "function", Function: ModelFunctionCall{
				Name: TaskCompleteToolName, Arguments: `{"summary":"done"}`,
			}},
		},
		FinishReason: "tool_calls",
	}}
}

// toolResponses indexes the tool results in history by tool_call_id.
func toolResponses(history []Message) map[string]string {
	out := map[string]string{}
	for _, m := range history {
		if m.Role == "tool" {
			out[m.ToolCallID] = m.Content
		}
	}
	return out
}

// TestExecuteTurn_TaskCompleteDeferredWhileStepInProgress: 收尾时面板仍把某步标为
// in_progress，与"任务已完成"自相矛盾——用户看到的就是永远不打勾的步骤。引擎给
// 模型一次自纠机会：把 task_complete 的 tool 回执写成"先补最终快照"，不结束 run。
// 通道就是回执本身，引擎不替模型编造任何状态。
func TestExecuteTurn_TaskCompleteDeferredWhileStepInProgress(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: taskCompleteTodoChunks(
			`{"todos":[{"content":"步骤一","status":"in_progress"},{"content":"步骤二","status":"pending"}]}`)},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 0},
		history:   []Message{{Role: "user", Content: "做完"}},
		config:    EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) { events = append(events, ev) }},
		guards:    &GuardSystem{scope: NewScopeGuard()},
		}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Fatalf("completion must be deferred while a step is still in_progress, got %+v", result)
	}
	if !e.todoNudgeSent {
		t.Error("the one-shot nudge must be marked as spent")
	}

	responses := toolResponses(e.history)
	if got := responses["c_done"]; got == "" || got == "Task completed." {
		t.Errorf("task_complete response = %q, want the final-snapshot reminder", got)
	} else if !strings.Contains(got, "in_progress") || !strings.Contains(got, "completed") {
		t.Errorf("reminder must name the statuses to fix, got %q", got)
	}
	if responses["c_todo"] == "" {
		t.Error("todo_write must still be answered (one response per tool_call_id)")
	}
	var todos int
	for _, ev := range events {
		if ev.Type == "todo_update" {
			todos++
		}
	}
	if todos != 1 {
		t.Errorf("todo_update events = %d, want 1 (the snapshot still reaches the UI)", todos)
	}
}

// TestExecuteTurn_TaskCompleteNotDeferredTwice: 只给一次机会。第二次 task_complete
// 必须收尾——否则模型可以把 run 拖成无限循环。
func TestExecuteTurn_TaskCompleteNotDeferredTwice(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: taskCompleteTodoChunks(
			`{"todos":[{"content":"步骤一","status":"in_progress"}]}`)},
		context:       &stubContextBuilder{},
		tools:         stubToolExecutor{},
		state:         &TaskState{TurnNumber: 0},
		history:       []Message{{Role: "user", Content: "做完"}},
		config:        EngineConfig{ModelName: "test-model"},
		guards:        &GuardSystem{scope: NewScopeGuard()},
		todoNudgeSent: true, // 本 run 的提醒已经用掉
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Fatalf("a spent nudge must not defer again, got %+v", result)
	}
	if got := toolResponses(e.history)["c_done"]; got != "Task completed." {
		t.Errorf("task_complete response = %q, want the normal completion ack", got)
	}
}

// TestExecuteTurn_TaskCompleteNotDeferredWhenPanelSettled: 打勾齐全、或模型明确留白
// 的 pending、或整轮没用过 todo_write，都不得触发提醒（否则每次收尾都白烧一轮）。
func TestExecuteTurn_TaskCompleteNotDeferredWhenPanelSettled(t *testing.T) {
	cases := []struct {
		name   string
		chunks []ModelChunk
	}{
		{"all completed", taskCompleteTodoChunks(`{"todos":[{"content":"步骤一","status":"completed"}]}`)},
		{"pending left on purpose", taskCompleteTodoChunks(`{"todos":[{"content":"步骤一","status":"completed"},{"content":"步骤二","status":"pending"}]}`)},
		{"no snapshot this run", []ModelChunk{{
			Delta: "收尾",
			ToolCalls: []ModelToolCall{{ID: "c_done", Type: "function", Function: ModelFunctionCall{
				Name: TaskCompleteToolName, Arguments: `{"summary":"done"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{
				model:   &stubStreamModel{chunks: tc.chunks},
				context: &stubContextBuilder{},
				tools:   stubToolExecutor{},
				state:   &TaskState{TurnNumber: 0},
				history: []Message{{Role: "user", Content: "做完"}},
				config:  EngineConfig{ModelName: "test-model"},
				guards:  &GuardSystem{scope: NewScopeGuard()},
			}
			result, err := e.executeTurn(context.Background())
			if err != nil {
				t.Fatalf("executeTurn error: %v", err)
			}
			if !result.Done {
				t.Fatalf("a settled panel must end the run immediately, got %+v", result)
			}
			if e.todoNudgeSent {
				t.Error("no nudge may be spent when the panel is settled")
			}
		})
	}
}
