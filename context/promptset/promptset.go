// Package promptset provides the single canonical (English) prompt set without
// dependencies on the engine package, avoiding circular imports.
//
// DeepAct is a coding agent preset: there is one prompt set, and generality
// across other kinds of work is left to the model's own judgement (plus any
// project/user-provided context), not to a domain taxonomy.
package promptset

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed en/system.md en/examples.md en/sub_agent.md
var promptFS embed.FS

// PromptSet holds all prompt components.
type PromptSet struct {
	System   string
	Examples string
	SubAgent string
}

// Get returns the single canonical (English) prompt set.
// The system prompt instructs the model to respond in the user's language,
// so one prompt set suffices regardless of session language — there is no
// parallel zh/en prompt set and no per-session prompt-language branch.
func Get() PromptSet {
	return PromptSet{
		System:   read("en/system.md"),
		Examples: read("en/examples.md"),
		SubAgent: read("en/sub_agent.md"),
	}
}

func read(path string) string {
	data, err := promptFS.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("embedded prompt not found: %s", path))
	}
	return strings.TrimSpace(string(data))
}
