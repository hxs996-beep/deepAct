package engine

import (
	"encoding/json"
	"testing"
)

func TestTaskCompleteToolSpec(t *testing.T) {
	spec := taskCompleteToolSpec(true)
	if spec.Function.Name != TaskCompleteToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, TaskCompleteToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
	var params struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal parameters: %v", err)
	}
	if len(params.Required) != 1 || params.Required[0] != "summary" {
		t.Errorf("required = %v, want [summary]", params.Required)
	}
}

func TestTaskCompleteToolSpec_English(t *testing.T) {
	spec := taskCompleteToolSpec(false)
	if spec.Function.Name != TaskCompleteToolName {
		t.Errorf("name = %q, want %q", spec.Function.Name, TaskCompleteToolName)
	}
	if spec.Function.Description == "" {
		t.Error("description should not be empty")
	}
}

func TestTurnResult_CompletionSummaryField(t *testing.T) {
	tr := TurnResult{Done: true, CompletionSummary: "test summary"}
	if tr.CompletionSummary != "test summary" {
		t.Errorf("CompletionSummary = %q, want %q", tr.CompletionSummary, "test summary")
	}
	var zero TurnResult
	if zero.CompletionSummary != "" {
		t.Errorf("zero-value CompletionSummary = %q, want empty", zero.CompletionSummary)
	}
}
