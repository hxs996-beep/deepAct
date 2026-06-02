// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that auto-trigger based on user intent detection. When matched, a skill's
// content is injected into the LLM context, shaping the agent's approach to
// the current task.
//
// This is inspired by Superpowers (obra/superpowers) skill system.
package skill

import "strings"

// Skill defines a methodology template that can be matched and injected
// into the agent's context to guide its behavior.
type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Intent keywords for matching
}

// MatchScore returns 0-1 how well the skill matches the user message.
func (s *Skill) MatchScore(msg string) float64 {
	msgLower := strings.ToLower(msg)
	matched := 0
	for _, kw := range s.Keywords {
		if strings.Contains(msgLower, kw) {
			matched++
		}
	}
	if matched == 0 || len(s.Keywords) == 0 {
		return 0
	}
	// Simple ratio: how many keywords matched out of total
	// A skill with 2+ keyword matches out of 3-5 is very likely relevant
	score := float64(matched) / float64(len(s.Keywords))
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// Registry holds all available skills and provides matching.
type Registry struct {
	skills    []*Skill
	threshold float64
}

// NewRegistry creates a skill registry with the given match threshold.
// Skills scoring >= threshold are considered matched.
func NewRegistry(threshold float64) *Registry {
	return &Registry{
		threshold: threshold,
	}
}

// Register adds a skill to the registry.
func (r *Registry) Register(s *Skill) {
	r.skills = append(r.skills, s)
}

// Match returns all skills whose MatchScore >= threshold for the given message.
func (r *Registry) Match(msg string) []*Skill {
	var matched []*Skill
	for _, s := range r.skills {
		if s.MatchScore(msg) >= r.threshold {
			matched = append(matched, s)
		}
	}
	return matched
}

// Get returns the skill by name, or nil.
func (r *Registry) Get(name string) *Skill {
	for _, s := range r.skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// All returns all registered skills.
func (r *Registry) All() []*Skill {
	return r.skills
}
