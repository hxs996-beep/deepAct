// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that can be activated by the user via /<skillname> commands or by the model
// when relevant. Available skills are listed in the stable system prompt block
// so the model can decide which methodology to apply.
package skill

import (
	"sort"
	"strings"
)

// Skill defines a methodology template that can be injected
// into the agent's context to guide its behavior.
type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Keywords used by MatchTopSkills to suggest relevant skills
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

// MatchTopSkills returns the top N skills matching the given input string,
// sorted by keyword match count descending. Skills with zero matches are excluded.
// This is used to suggest relevant skills to the model in the runtime context,
// rather than auto-activating any skill.
func (r *Registry) MatchTopSkills(n int, input string) []*Skill {
	if n <= 0 || input == "" {
		return nil
	}
	inputLower := strings.ToLower(input)
	type scored struct {
		skill *Skill
		count int
	}
	var results []scored
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
		if count > 0 {
			results = append(results, scored{skill: s, count: count})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].count > results[j].count
	})
	if len(results) > n {
		results = results[:n]
	}
	out := make([]*Skill, len(results))
	for i, r := range results {
		out[i] = r.skill
	}
	return out
}

// MatchByKeywords finds the best matching skill for the given input string.
// Deprecated: Use MatchTopSkills instead.
func (r *Registry) MatchByKeywords(input string) *Skill {
	top := r.MatchTopSkills(1, input)
	if len(top) == 0 {
		return nil
	}
	return top[0]
}
