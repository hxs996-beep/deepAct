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

// FallbackMatcher tries keyword matching first, then falls back to semantic matching.
// This ensures zero-cost keyword matches are prioritized, and LLM calls are only
// made when necessary.
type FallbackMatcher struct {
	keyword  *KeywordMatcher
	semantic *SemanticMatcher
}

// NewFallbackMatcher creates a fallback matcher. If semantic is nil, only
// keyword matching is used (semantic is skipped).
func NewFallbackMatcher(keyword *KeywordMatcher, semantic *SemanticMatcher) *FallbackMatcher {
	return &FallbackMatcher{keyword: keyword, semantic: semantic}
}

// Match tries keyword matching first. If no match, falls back to semantic.
func (m *FallbackMatcher) Match(ctx context.Context, userMsg string, skills []*Skill) *Skill {
	// Step 1: try keyword matching (fast, free)
	if s := m.keyword.Match(ctx, userMsg, skills); s != nil {
		return s
	}
	// Step 2: fall back to semantic matching (slow, cheap)
	if m.semantic != nil {
		return m.semantic.Match(ctx, userMsg, skills)
	}
	return nil
}
