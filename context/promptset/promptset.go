// Package promptset provides language-specific prompt loading without
// dependencies on the engine package, avoiding circular imports.
package promptset

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed en/system.md en/examples.md en/sub_agent.md
//go:embed zh/system.md zh/examples.md zh/sub_agent.md
var promptFS embed.FS

// PromptSet holds all prompt components for a single language.
type PromptSet struct {
	System    string
	Examples  string
	SubAgent  string
}

// Get returns the prompt set for the given user language.
// If userLang is "中文", returns the Chinese set; otherwise returns English.
func Get(userLang string) PromptSet {
	if userLang == "中文" {
		return PromptSet{
			System:   read("zh/system.md"),
			Examples: read("zh/examples.md"),
			SubAgent: read("zh/sub_agent.md"),
		}
	}
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
