// Package skill provides a methodology skill system for guiding agent behavior.
//
// Skills are composable methodology templates (like "brainstorming", "debugging")
// that can be activated by the user via /<skillname> commands or by the model
// when relevant. Available skills are listed in the stable system prompt block
// so the model can decide which methodology to apply.
package skill

// GateConfig defines a pre-implementation gate for a skill. When non-nil,
// the engine blocks edit/write calls until the gate is passed (user approval
// or NextSkills transition). Gates are provided by gates.go defaults, not
// by skill files.
type GateConfig struct {
	Type         string   // "path_filter" or "block_all"
	AllowedPaths []string // for "path_filter": paths allowed during gate
}

type Skill struct {
	Name        string   // Unique identifier, e.g. "debugging"
	Description string   // Short description for matching
	Content     string   // Full skill instructions injected into prompt
	Keywords    []string // Retained as metadata (matching is LLM-semantic)
	NextSkills  []string // Skill names suggested after this skill completes
	Gate        *GateConfig // Pre-implementation gate; nil = no gate

	// AutoActivateThreshold is retained as metadata.
	// Unused since keyword-based auto-activation was removed in favor of
	// semantic matching.
	AutoActivateThreshold *int

	// Claude Code-compatible frontmatter fields. Parsed from YAML
	// frontmatter in SKILL.md files.

	AllowedTools           []string // allowed-tools: tool permission patterns
	ArgumentHint           string   // argument-hint: hint showing argument placeholders
	Arguments              []string // arguments: argument names for $name substitution
	WhenToUse              string   // when_to_use: when to auto-invoke, including trigger phrases
	Model                  string   // model: per-skill model override
	Effort                 string   // effort: reasoning effort level
	Agent                  string   // agent: agent type for execution
	Context                string   // context: "fork" for sub-agent, "" for inline
	Hooks                  string   // hooks: JSON-encoded hook configuration
	Paths                  []string // paths: conditional activation file patterns
	UserInvocable          bool     // user-invocable: can be invoked via / (default true)
	DisableModelInvocation bool     // disable-model-invocation: skip auto-activation
	Version                string   // version: skill version string
	Shell                  string   // shell: shell execution settings (JSON-encoded)
	BaseDir                string   // base directory of the skill (for ${SKILL_DIR})
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
