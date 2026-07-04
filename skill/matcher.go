package skill

import "context"

// SkillMatcher matches a user message to the most relevant skill.
// Returns nil if no skill meets the activation criteria.
type SkillMatcher interface {
	Match(ctx context.Context, userMsg string, skills []*Skill) *Skill
}

// KeywordMatcher uses keyword substring matching with AutoActivateThreshold.
// It wraps Registry.MatchTopSkillsWithScores and applies the threshold check.
type KeywordMatcher struct {
	reg *Registry
}

// NewKeywordMatcher creates a KeywordMatcher using the given registry.
func NewKeywordMatcher(reg *Registry) *KeywordMatcher {
	return &KeywordMatcher{reg: reg}
}

// Match returns the highest-scoring skill whose keyword match count meets its
// AutoActivateThreshold. Returns nil if no skill qualifies.
func (m *KeywordMatcher) Match(_ context.Context, userMsg string, skills []*Skill) *Skill {
	matches := m.reg.MatchTopSkillsWithScores(3, userMsg)
	for _, match := range matches {
		if match.Skill.AutoActivateThreshold != nil && match.Score >= *match.Skill.AutoActivateThreshold {
			return match.Skill
		}
	}
	return nil
}
