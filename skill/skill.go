// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that can be activated by the user via /<skillname> commands or by the model
// when relevant. Available skills are listed in the stable system prompt block
// so the model can decide which methodology to apply.
package skill

// Skill defines a methodology template that can be injected
// into the agent's context to guide its behavior.
// GateConfig defines a pre-implementation gate for a skill. When non-nil,
// the engine blocks edit/write calls until the gate is passed (user approval
// or NextSkills transition). This is data-driven: skills declare their gate
// type in TOML, and the engine reads it without hardcoding skill names.
type GateConfig struct {
	Type         string   `toml:"type"`          // "path_filter" or "block_all"
	AllowedPaths []string `toml:"allowed_paths"` // for "path_filter": paths allowed during gate
}

type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Loaded from TOML; retained as metadata (matching is LLM-semantic)
	NextSkills  []string // Skill names suggested after this skill completes, enabling auto-activation chains
	Gate        *GateConfig // Pre-implementation gate; nil = no gate

	// AutoActivateThreshold is loaded from TOML and retained as metadata.
	// Unused since keyword-based auto-activation was removed in favor of
	// semantic matching.
	AutoActivateThreshold *int
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
