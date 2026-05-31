package policy

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/deepact/deepact/engine"
)

type Checker struct {
	ambiguity   *AmbiguityDetector
	design      *DesignGuard
	modelClient engine.ModelClient
	modelName   string
}

func NewChecker(ambiguityThreshold float64) *Checker {
	return &Checker{
		ambiguity: NewAmbiguityDetector(ambiguityThreshold),
		design:    NewDesignGuard(),
	}
}

func (c *Checker) CheckAmbiguity(userMsg string, state *engine.TaskState) engine.AmbiguityResult {
	// Skip ambiguity check for follow-up messages when a goal is already established
	if state != nil && state.Goal != "" {
		return engine.AmbiguityResult{Score: 0.0}
	}
	if c.modelClient == nil || c.modelName == "" {
		return engine.AmbiguityResult{Score: 0.0}
	}
	return c.ambiguity.AnalyzeWithLLM(context.Background(), c.modelClient, c.modelName, userMsg)
}

func (c *Checker) SetModelClient(mc engine.ModelClient) {
	c.modelClient = mc
}

func (c *Checker) SetModelName(name string) {
	c.modelName = name
}

func (c *Checker) CheckDesign(plan string, ctxInfo string) engine.DesignReview {
	if c.modelClient == nil || plan == "" {
		return engine.DesignReview{Verdict: VerdictPass, Issues: nil}
	}

	prompt := c.design.BuildReviewPrompt(plan, ctxInfo)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	modelName := c.modelName
	if modelName == "" {
		modelName = "deepseek-v4-pro"
	}
	req := engine.ModelRequest{
		Model:       modelName,
		Messages:    []engine.ModelMessage{{Role: "user", Content: prompt}},
		Temperature: 0.0,
		JsonMode:    true,
	}

	resp, err := c.modelClient.Complete(ctx, req)
	if err != nil {
		return engine.DesignReview{Verdict: VerdictPass, Issues: nil}
	}

	review, err := parseDesignReview(resp.Message.Content)
	if err != nil {
		return engine.DesignReview{Verdict: VerdictPass, Issues: nil}
	}

	return review
}

// parseDesignReview extracts DesignReview from LLM JSON response,
// handling possible markdown code block wrapping.
func parseDesignReview(content string) (engine.DesignReview, error) {
	text := strings.TrimSpace(content)

	if strings.HasPrefix(text, "```") {
		end := strings.LastIndex(text, "```")
		if end > 3 {
			text = strings.TrimSpace(text[3:end])
			if idx := strings.Index(text, "\n"); idx >= 0 {
				text = strings.TrimSpace(text[idx:])
			}
		}
	}

	var review engine.DesignReview
	if err := json.Unmarshal([]byte(text), &review); err != nil {
		return engine.DesignReview{Verdict: VerdictPass, Issues: nil}, err
	}
	if review.Verdict == "" {
		review.Verdict = VerdictPass
	}
	return review, nil
}

func (c *Checker) CheckScope(action string, state *engine.TaskState) engine.ScopeResult {
	if state == nil {
		return engine.ScopeResult{Allowed: true}
	}
	if state.ConfirmedScope {
		return engine.ScopeResult{Allowed: true}
	}
	if !isDestructiveAction(action) {
		return engine.ScopeResult{Allowed: true}
	}
	if isActionInConfirmedFiles(action, state) {
		return engine.ScopeResult{Allowed: true}
	}
	return engine.ScopeResult{Allowed: false, Reasons: []string{"Scope not confirmed for destructive action"}}
}

func isDestructiveAction(action string) bool {
	value := strings.ToLower(action)
	return strings.Contains(value, "edit") || strings.Contains(value, "write") || strings.Contains(value, "bash")
}

func isActionInConfirmedFiles(action string, state *engine.TaskState) bool {
	if state == nil {
		return false
	}
	value := strings.ToLower(action)
	for _, file := range state.WorkingSet.Files {
		if file.Path != "" && strings.Contains(value, strings.ToLower(file.Path)) {
			return true
		}
	}
	for _, path := range state.ModifiedFiles {
		if path != "" && strings.Contains(value, strings.ToLower(path)) {
			return true
		}
	}
	return false
}
