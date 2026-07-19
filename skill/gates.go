package skill

// defaultGateConfigs maps well-known skill names to their gate configurations.
// Open-source skill TOMLs (downloaded from GitHub etc.) don't include a [gate]
// section - this registry provides defaults so the engine can enforce gates
// without modifying those files. If a skill's TOML does include [gate], it
// overrides the default here.
var defaultGateConfigs = map[string]*GateConfig{
	"brainstorming": {
		Type:         "path_filter",
		AllowedPaths: []string{"docs/"},
	},
	"systematic-debugging": {
		Type: "block_all",
	},
}

// DefaultGateFor returns the default gate config for a well-known skill name,
// or nil if no default is registered. This allows the engine to provide gate
// configs for open-source skills whose TOMLs don't include a [gate] section.
func DefaultGateFor(name string) *GateConfig {
	return defaultGateConfigs[name]
}
