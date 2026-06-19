// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that can be activated by the user via /<skillname> commands or by the model
// when relevant. Available skills are listed in the stable system prompt block
// so the model can decide which methodology to apply.
package skill

import "strings"

// Skill defines a methodology template that can be injected
// into the agent's context to guide its behavior.
type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Intent keywords (kept for reference, not used for auto-matching)
	NextSkills  []string // Skill names suggested after this skill completes, enabling auto-activation chains
}

// Registry holds all available skills.
type Registry struct {
	skills []*Skill
}

// NewRegistry creates a skill registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a skill to the registry.
func (r *Registry) Register(s *Skill) {
	r.skills = append(r.skills, s)
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

// MatchByKeywords finds the best matching skill for the given input string.
// It checks each skill's Keywords list against the input (case-insensitive).
// Returns the skill with the most keyword matches, or nil if none match.
func (r *Registry) MatchByKeywords(input string) *Skill {
	if input == "" {
		return nil
	}
	inputLower := strings.ToLower(input)
	var best *Skill
	bestCount := 0
	for _, s := range r.skills {
		count := 0
		for _, kw := range s.Keywords {
			if kw == "" {
				continue
			}
			if strings.Contains(inputLower, strings.ToLower(kw)) {
				count++
			}
		}
		if count > 0 && count > bestCount {
			best = s
			bestCount = count
		}
	}
	return best
}
