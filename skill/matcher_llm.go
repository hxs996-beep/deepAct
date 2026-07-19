package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MatchFunc is a callback that sends system+user messages to an LLM
// and returns the raw response text. Implementations should handle
// model selection, API calls, and error handling.
type MatchFunc func(ctx context.Context, systemMsg, userMsg string) (string, error)

// SemanticMatcher uses an LLM to semantically match a user message
// to the most relevant skill.
type SemanticMatcher struct {
	match     MatchFunc
	modelName string
	timeout   time.Duration
}

// NewSemanticMatcher creates a semantic matcher. If modelName is empty,
// the matcher is disabled (Match always returns nil). match is the LLM
// callback — typically wraps engine.ModelClient.Complete.
func NewSemanticMatcher(match MatchFunc, modelName string) *SemanticMatcher {
	return &SemanticMatcher{
		match:     match,
		modelName: modelName,
		timeout:   2 * time.Second,
	}
}

type matchResult struct {
	Skill *string `json:"skill"`
}

const semanticSystemPrompt = `You are a skill matching engine. Given a user's message and a list of available skills, select the ONE skill most relevant to the user's intent. Return ONLY JSON.

Rules:
- If a skill clearly matches the user's intent, return its name.
- If NO skill is relevant, return null.
- Consider both the skill name and description.
- The user message may be in Chinese or English.`

// Match runs semantic matching via LLM. Returns nil on any failure (timeout,
// bad response, unknown skill) — the caller should fall back gracefully.
func (m *SemanticMatcher) Match(ctx context.Context, userMsg string, skills []*Skill) *Skill {
	if m.modelName == "" || m.match == nil || len(skills) == 0 {
		return nil
	}

	// Build skills list for the prompt
	var skillList strings.Builder
	for _, s := range skills {
		skillList.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
	}

	userPrompt := fmt.Sprintf(
		"User message: %s\n\nAvailable skills:\n%s\nReturn: {\"skill\": \"<name>\"} or {\"skill\": null}",
		userMsg, skillList.String(),
	)

	// Apply timeout
	matchCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	raw, err := m.match(matchCtx, semanticSystemPrompt, userPrompt)
	if err != nil {
		return nil
	}

	var result matchResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	if result.Skill == nil {
		return nil
	}

	// Look up skill by name from the provided list
	name := *result.Skill
	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return s
		}
	}
	return nil
}
