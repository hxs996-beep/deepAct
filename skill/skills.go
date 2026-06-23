package skill

// RegisterBuiltinSkills loads all skills embedded in the binary.
// These are the default skill set, shipped with every DeepAct install.
// User-installed skills in ~/.deepact/skills/ override these by name.
func RegisterBuiltinSkills(r *Registry) {
	embedded, err := LoadEmbeddedSkills()
	if err != nil {
		// This should never happen — embedded skills are compiled in.
		return
	}
	for _, s := range embedded {
		r.Register(s)
	}
}
