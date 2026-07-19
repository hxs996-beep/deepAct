package skill

import "context"

// SkillMatcher matches a user message to the most relevant skill.
// Returns nil if no skill meets the activation criteria.
//
// The sole implementation is SemanticMatcher (matcher_llm.go), which asks the
// flash model to pick the best skill from the user message + all skill
// descriptions. Keyword substring matching was removed — it produced too many
// false positives (e.g. "PR" lowercased to "pr" matched "prefix").
type SkillMatcher interface {
	Match(ctx context.Context, userMsg string, skills []*Skill) *Skill
}
