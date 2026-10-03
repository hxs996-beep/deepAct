package engine

import (
	"context"
	"testing"
)

// handoffResultExecutor stands in for the real tool layer: every handoff call
// comes back with the same preset result and emits nothing itself, so any
// agent_start/agent_done seen by the test was emitted by executeTurn.
type handoffResultExecutor struct{ result ToolResult }

func (x *handoffResultExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		r := x.result
		r.ToolCallID = c.ID
		out = append(out, r)
	}
	return out
}

func (x *handoffResultExecutor) Specs() []ModelTool {
	return []ModelTool{{Type: "function", Function: ModelToolFunction{Name: HandoffToolName}}}
}

// TestExecuteTurn_AsyncDispatchEmitsNoTerminalEvents: 成功的异步派发不是结束。
// turn.go 既不能自己再发一次 agent_start（dispatchAsync 已经发过），也不能
// 立刻发 agent_done —— 否则后台还在跑的行会被标记完成（用户报告：5 个方向
// 在面板上变成 10 行，其中 5 行显示的是派发回执）。
func TestExecuteTurn_AsyncDispatchEmitsNoTerminalEvents(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "派发调研",
			ToolCalls: []ModelToolCall{{ID: "call_1", Type: "function", Function: ModelFunctionCall{
				Name:      HandoffToolName,
				Arguments: `{"agent":"researcher","goal":"调研","async":true,"step":"dir-1"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools: &handoffResultExecutor{result: ToolResult{
			ToolName:     HandoffToolName,
			Status:       "ok",
			Digest:       "Dispatched async job bg-1 (researcher): 调研. Use agent_poll(bg-1) to check the result.",
			FinishReason: HandoffReasonAsyncRunning,
		}},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "调研"}},
		config: EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	for _, ev := range events {
		if ev.Type == "agent_start" || ev.Type == "agent_done" {
			t.Errorf("turn.go must stay silent for a successful async dispatch, got %s: %+v", ev.Type, ev)
		}
	}
}

// TestExecuteTurn_SyncHandoffEmitsPairedRunID: 同步委派仍由 turn.go 发一对
// 事件，两者带同一个 RunID（= handoff tool call id）。
func TestExecuteTurn_SyncHandoffEmitsPairedRunID(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "同步委派",
			ToolCalls: []ModelToolCall{{ID: "call_9", Type: "function", Function: ModelFunctionCall{
				Name:      HandoffToolName,
				Arguments: `{"agent":"researcher","goal":"调研","step":"dir-9"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools: &handoffResultExecutor{result: ToolResult{
			ToolName: HandoffToolName, Status: "ok", Digest: "研究小结：结论",
		}},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "调研"}},
		config: EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var start, done *ProgressEvent
	for i := range events {
		switch events[i].Type {
		case "agent_start":
			start = &events[i]
		case "agent_done":
			done = &events[i]
		}
	}
	if start == nil || done == nil {
		t.Fatalf("sync handoff must emit both start and done, got %+v", events)
	}
	if start.RunID != "call_9" || done.RunID != "call_9" {
		t.Errorf("RunID mismatch: start=%q done=%q, want both call_9", start.RunID, done.RunID)
	}
	if done.Status != "ok" {
		t.Errorf("agent_done Status = %q, want ok", done.Status)
	}
	if done.Step != "dir-9" || start.Step != "dir-9" {
		t.Errorf("Step mismatch: start=%q done=%q, want both dir-9", start.Step, done.Step)
	}
}

// TestExecuteTurn_FailedAsyncDispatchEmitsErrorDone: 派发在子代理启动前就失败
// （名额满 / agent 不存在）时没有 agent_start，turn.go 必须发一条带 RunID 与
// error 状态的 done，让失败可见而不是静默消失。
func TestExecuteTurn_FailedAsyncDispatchEmitsErrorDone(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "派发",
			ToolCalls: []ModelToolCall{{ID: "call_7", Type: "function", Function: ModelFunctionCall{
				Name:      HandoffToolName,
				Arguments: `{"agent":"researcher","goal":"调研","async":true}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools: &handoffResultExecutor{result: ToolResult{
			ToolName: HandoffToolName,
			Status:   "error",
			Digest:   "background sub-agent limit reached (8 outstanding). Poll existing results with agent_poll first.",
		}},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "调研"}},
		config: EngineConfig{ModelName: "test-model", OnProgress: func(ev ProgressEvent) {
			events = append(events, ev)
		}},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}

	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	found := false
	for _, ev := range events {
		if ev.Type != "agent_done" {
			continue
		}
		found = true
		if ev.RunID != "call_7" {
			t.Errorf("agent_done RunID = %q, want call_7", ev.RunID)
		}
		if ev.Status != "error" {
			t.Errorf("agent_done Status = %q, want error", ev.Status)
		}
	}
	if !found {
		t.Fatalf("a failed async dispatch must still produce an agent_done, got %+v", events)
	}
}
