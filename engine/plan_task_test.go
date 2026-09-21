package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPlanMethodology_NonEmptyBothLanguages(t *testing.T) {
	zh := planMethodology(true)
	en := planMethodology(false)
	if strings.TrimSpace(zh) == "" {
		t.Error("zh methodology should not be empty")
	}
	if strings.TrimSpace(en) == "" {
		t.Error("en methodology should not be empty")
	}
	if zh == en {
		t.Error("zh and en methodologies should differ")
	}
	if !strings.Contains(zh, "吃透背景") || !strings.Contains(zh, "多假设") {
		t.Error("zh methodology should cover core steps (吃透背景 / 多假设)")
	}
	if !strings.Contains(en, "background") || !strings.Contains(en, "hypotheses") {
		t.Error("en methodology should cover core steps (background / hypotheses)")
	}
}

func TestPlanTaskToolSpec(t *testing.T) {
	spec := planTaskToolSpec(true)
	if spec.Function.Name != PlanTaskToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, PlanTaskToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if params.Type != "object" {
		t.Errorf("type = %q, want object", params.Type)
	}
	if len(params.Required) != 0 {
		t.Errorf("required = %v, want empty (no required params)", params.Required)
	}
	if len(params.Properties) != 0 {
		t.Errorf("properties = %v, want empty (no params)", params.Properties)
	}
}

func TestProcessPlanTaskCalls_InjectMethodology(t *testing.T) {
	e := &Engine{state: &TaskState{}, config: EngineConfig{}, isChinese: true}
	calls := []ToolCallRequest{
		{ID: "call_ok", Name: PlanTaskToolName, Input: json.RawMessage(`{}`)},
	}
	msgs := e.processPlanTaskCalls(calls)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_ok" {
		t.Errorf("ToolCallID = %q, want call_ok", msgs[0].ToolCallID)
	}
	if msgs[0].Role != "tool" {
		t.Errorf("Role = %q, want tool", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "[PLAN_METHODOLOGY") {
		t.Errorf("Content = %q, want [PLAN_METHODOLOGY marker", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "深度分析方法论") {
		t.Errorf("Content = %q, want zh methodology", msgs[0].Content)
	}
}

func TestProcessPlanTaskCalls_BadJSON(t *testing.T) {
	e := &Engine{state: &TaskState{}, config: EngineConfig{}}
	calls := []ToolCallRequest{
		{ID: "call_bad", Name: PlanTaskToolName, Input: json.RawMessage(`{invalid`)},
	}
	msgs := e.processPlanTaskCalls(calls)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "invalid plan_task arguments") {
		t.Errorf("Content = %q, want invalid arguments error", msgs[0].Content)
	}
}

func TestProcessPlanTaskCalls_Mixed(t *testing.T) {
	e := &Engine{state: &TaskState{}, config: EngineConfig{}}
	calls := []ToolCallRequest{
		{ID: "call_plan", Name: PlanTaskToolName, Input: json.RawMessage(`{}`)},
		{ID: "call_read", Name: "read", Input: json.RawMessage(`{"path":"a.go"}`)},
	}
	msgs := e.processPlanTaskCalls(calls)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tool response (only plan_task), got %d", len(msgs))
	}
	if msgs[0].ToolCallID != "call_plan" {
		t.Errorf("ToolCallID = %q, want call_plan", msgs[0].ToolCallID)
	}
}

// recorderExecutor mimics tools/registry.go's Execute for turn-level
// classification tests: unknown tool names (which includes plan_task if it
// ever leaked into regularCalls) produce a "tool not found: <name>" result,
// exactly like the production executor.
type recorderExecutor struct {
	calls []ToolCallRequest
}

func (r *recorderExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	r.calls = append(r.calls, calls...)
	results := make([]ToolResult, 0, len(calls))
	for _, call := range calls {
		results = append(results, ToolResult{
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Status:     "error",
			Digest:     fmt.Sprintf("tool not found: %s", call.Name),
		})
	}
	return results
}

func (r *recorderExecutor) Specs() []ModelTool { return nil }

// TestPlanTaskNotInRegularCalls verifies the executeTurn classification loop
// (turn.go: separate handoff calls from regular tool calls): plan_task is
// intercepted by processPlanTaskCalls and must NOT enter regularCalls.
// Otherwise the tool executor would produce a duplicate "tool not found:
// plan_task" message for the same tool_call_id, violating the DeepSeek API
// contract — the same failure mode load_skill guards against.
func TestPlanTaskNotInRegularCalls(t *testing.T) {
	recorder := &recorderExecutor{}
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "先深度规划，再执行。", ToolCalls: []ModelToolCall{
				{ID: "call_plan", Type: "function", Function: ModelFunctionCall{
					Name:      PlanTaskToolName,
					Arguments: `{}`,
				}},
				{ID: "call_read", Type: "function", Function: ModelFunctionCall{
					Name:      "read",
					Arguments: `{"path":"a.go"}`,
				}},
			}, FinishReason: "tool_calls"},
		}},
		context: &stubContextBuilder{},
		tools:   recorder,
		state:   &TaskState{TurnNumber: 1, Goal: "规划并执行"},
		history: []Message{{Role: "user", Content: "规划并执行"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Fatalf("expected Done=false (regular read call executed), got Done=true")
	}

	// plan_task must never reach the tool executor's regularCalls path.
	for _, call := range recorder.calls {
		if call.Name == PlanTaskToolName {
			t.Errorf("plan_task leaked into regularCalls: executed by tools with ID %q", call.ID)
		}
	}

	// The only executed tool must be the regular read call.
	if len(recorder.calls) != 1 || recorder.calls[0].Name != "read" || recorder.calls[0].ID != "call_read" {
		t.Fatalf("expected exactly 1 regular call (read/call_read), got %+v", recorder.calls)
	}

	// No duplicate "tool not found: plan_task" message may appear in history.
	// The plan_task tool_call_id must be answered exactly once, with the
	// injected methodology, and the read call once with its executor result.
	planMsgs, readMsgs, notFound := 0, 0, 0
	for _, msg := range e.history {
		if msg.Role != "tool" {
			continue
		}
		switch msg.ToolCallID {
		case "call_plan":
			planMsgs++
			if !strings.Contains(msg.Content, "[PLAN_METHODOLOGY") {
				t.Errorf("plan_task tool message = %q, want [PLAN_METHODOLOGY marker", msg.Content)
			}
		case "call_read":
			readMsgs++
		}
		if strings.Contains(msg.Content, "tool not found: plan_task") {
			notFound++
		}
	}
	if planMsgs != 1 {
		t.Errorf("expected exactly 1 tool message for plan_task, got %d", planMsgs)
	}
	if readMsgs != 1 {
		t.Errorf("expected exactly 1 tool message for read, got %d", readMsgs)
	}
	if notFound != 0 {
		t.Errorf("expected no 'tool not found: plan_task' in history, got %d", notFound)
	}
}
