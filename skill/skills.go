package skill

// RegisterBuiltinSkills is a no-op. All skills are loaded from external TOML files
// in .deepact/skills/. Built-in Go skills were removed in favor of richer external
// TOML definitions (brainstorming, systematic-debugging, verification-before-completion, etc.).
func RegisterBuiltinSkills(r *Registry) {
	// no-op: built-in skills removed, all skills come from TOML files
}
