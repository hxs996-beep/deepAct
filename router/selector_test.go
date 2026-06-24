package router

import (
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestNewRouter(t *testing.T) {
	r := NewRouter(0.5)
	if r == nil {
		t.Fatal("expected non-nil Router")
	}
	if r.RiskThreshold != 0.5 {
		t.Errorf("RiskThreshold = %f, want 0.5", r.RiskThreshold)
	}
}

func TestNewRouter_Defaults(t *testing.T) {
	r := NewRouter(0.3)
	if r.ModelName != "deepseek-v4-flash" {
		t.Errorf("ModelName = %q, want deepseek-v4-flash", r.ModelName)
	}
	if r.FlashModelName != "deepseek-v4-flash" {
		t.Errorf("FlashModelName = %q, want deepseek-v4-flash", r.FlashModelName)
	}
}

func TestSelectModel_ReadOnly(t *testing.T) {
	r := NewRouter(0.5)
	r.FlashModelName = "flash-model"
	ctx := engine.RouteContext{IsReadOnly: true}
	decision := r.SelectModel(ctx)
	if decision.Model != "flash-model" {
		t.Errorf("read-only should use flash model, got %q", decision.Model)
	}
	if decision.Reasoning != "read-only turn" {
		t.Errorf("reasoning = %q, want 'read-only turn'", decision.Reasoning)
	}
}

func TestSelectModel_HighRisk(t *testing.T) {
	r := NewRouter(0.3)
	r.ModelName = "pro-model"
	r.FlashModelName = "flash-model"

	ctx := engine.RouteContext{
		ToolFailureCount: 3, // 3*0.15=0.45 >= 0.3
	}
	decision := r.SelectModel(ctx)
	if decision.Model != "pro-model" {
		t.Errorf("high risk should use pro model, got %q", decision.Model)
	}
	if decision.Reasoning != "high complexity" {
		t.Errorf("reasoning = %q, want 'high complexity'", decision.Reasoning)
	}
}

func TestSelectModel_LowRisk(t *testing.T) {
	r := NewRouter(0.5)
	r.FlashModelName = "flash-model"

	ctx := engine.RouteContext{}
	decision := r.SelectModel(ctx)
	if decision.Model != "flash-model" {
		t.Errorf("low risk should use flash model, got %q", decision.Model)
	}
	if decision.Reasoning != "low complexity" {
		t.Errorf("reasoning = %q, want 'low complexity'", decision.Reasoning)
	}
}

func TestComputeRiskScore_NoFailures(t *testing.T) {
	r := NewRouter(0.5)
	ctx := engine.RouteContext{}
	score := r.computeRiskScore(ctx)
	if score != 0.0 {
		t.Errorf("expected 0, got %f", score)
	}
}

func TestComputeRiskScore_ToolFailures(t *testing.T) {
	r := NewRouter(0.5)

	tests := []struct {
		failures int
		want     float64
	}{
		{0, 0.0},
		{1, 0.15},
		{2, 0.30},
		{3, 0.30}, // capped at toolFailureCap
		{10, 0.30},
	}
	for _, tt := range tests {
		ctx := engine.RouteContext{ToolFailureCount: tt.failures}
		got := r.computeRiskScore(ctx)
		if got != tt.want {
			t.Errorf("ToolFailureCount=%d: got %f, want %f", tt.failures, got, tt.want)
		}
	}
}

func TestComputeRiskScore_ConsecutiveFails(t *testing.T) {
	r := NewRouter(0.5)

	tests := []struct {
		fails int
		want  float64
	}{
		{0, 0.0},
		{1, 0.0},
		{2, 0.30}, // consecutiveFailScore added
		{3, 0.30},
	}
	for _, tt := range tests {
		ctx := engine.RouteContext{ConsecutiveFails: tt.fails}
		got := r.computeRiskScore(ctx)
		if got != tt.want {
			t.Errorf("ConsecutiveFails=%d: got %f, want %f", tt.fails, got, tt.want)
		}
	}
}

func TestComputeRiskScore_Combined(t *testing.T) {
	r := NewRouter(0.5)
	ctx := engine.RouteContext{
		ToolFailureCount: 2,     // 0.30 (capped)
		ConsecutiveFails: 2,     // 0.30
	}
	score := r.computeRiskScore(ctx)
	want := 0.30 + 0.30 // 0.60
	if score != want {
		t.Errorf("combined score: got %f, want %f", score, want)
	}
}
