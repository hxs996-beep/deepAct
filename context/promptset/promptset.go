// Package promptset provides language-specific prompt loading without
// dependencies on the engine package, avoiding circular imports.
package promptset

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed zh/system.md zh/examples.md zh/sub_agent.md
var promptFS embed.FS

// PromptSet holds all prompt components.
type PromptSet struct {
	System   string
	Examples string
	SubAgent string
}

// Get returns the single canonical (Chinese) prompt set.
// The system prompt instructs the model to respond in the user's language,
// so one prompt set suffices regardless of session language — no need to
// maintain parallel zh/en prompt sets.
func Get() PromptSet {
	return PromptSet{
		System:   read("zh/system.md"),
		Examples: read("zh/examples.md"),
		SubAgent: read("zh/sub_agent.md"),
	}
}

func read(path string) string {
	data, err := promptFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embedded prompt not found: %s", path))
	}
	return strings.TrimSpace(string(data))
}
