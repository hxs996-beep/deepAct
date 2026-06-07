package router

import "github.com/deepact/deepact/engine"

// Risk scoring weights — configurable constants for router behavior.
const (
	toolFailureWeight      = 0.15  // per failure
	toolFailureCap         = 0.30  // max contribution from tool failures
	consecutiveFailScore   = 0.30  // add if consecutive fails >= threshold
	consecutiveFailTrigger = 2     // trigger count for consecutive fail penalty
)

type DefaultRouter struct {
	RiskThreshold  float64
	ModelName      string // Pro model (for complex tasks)
	FlashModelName string // Flash model (for simple/read-only tasks)
}

func NewRouter(threshold float64) *DefaultRouter {
	return &DefaultRouter{
		RiskThreshold:  threshold,
		ModelName:      "deepseek-v4-flash",
		FlashModelName: "deepseek-v4-flash",
	}
}

func (r *DefaultRouter) SelectModel(ctx engine.RouteContext) engine.RouteDecision {
	if ctx.IsReadOnly {
		return engine.RouteDecision{Model: r.FlashModelName, Reasoning: "read-only turn"}
	}

	score := r.computeRiskScore(ctx)
	if score >= r.RiskThreshold {
		return engine.RouteDecision{Model: r.ModelName, Reasoning: "high complexity"}
	}
	return engine.RouteDecision{Model: r.FlashModelName, Reasoning: "low complexity"}
}

func (r *DefaultRouter) computeRiskScore(ctx engine.RouteContext) float64 {
	score := 0.0

	if ctx.ToolFailureCount > 0 {
		score += float64(ctx.ToolFailureCount) * toolFailureWeight
		if score > toolFailureCap {
			score = toolFailureCap
		}
	}

	if ctx.ConsecutiveFails >= consecutiveFailTrigger {
		score += consecutiveFailScore
	}

	return score
}
