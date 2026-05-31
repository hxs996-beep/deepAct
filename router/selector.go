package router

import "github.com/deepact/deepact/engine"

const (
	ModelFlash = "deepseek-v4-flash"
	ModelPro   = "deepseek-v4-pro"
)

type DefaultRouter struct {
	RiskThreshold float64
}

func NewRouter(threshold float64) *DefaultRouter {
	return &DefaultRouter{RiskThreshold: threshold}
}

func (r *DefaultRouter) SelectModel(ctx engine.RouteContext) engine.RouteDecision {
	if ctx.IsReadOnly {
		return engine.RouteDecision{Model: ModelFlash, Reasoning: "read-only turn"}
	}

	score := r.computeRiskScore(ctx)
	if score >= r.RiskThreshold {
		return engine.RouteDecision{Model: ModelPro, Reasoning: "high complexity"}
	}
	return engine.RouteDecision{Model: ModelFlash, Reasoning: "low complexity"}
}

func (r *DefaultRouter) computeRiskScore(ctx engine.RouteContext) float64 {
	score := 0.0

	if ctx.ToolFailureCount > 0 {
		score += float64(ctx.ToolFailureCount) * 0.15
		if score > 0.30 {
			score = 0.30
		}
	}

	if ctx.ConsecutiveFails >= 2 {
		score += 0.30
	}

	if ctx.EditScopeFiles > 3 {
		score += 0.20
	} else if ctx.EditScopeFiles > 0 {
		score += 0.10
	}

	return score
}
