package engine

import (
	"encoding/json"
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
