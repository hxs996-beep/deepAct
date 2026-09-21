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
