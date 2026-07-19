package skill

import (
	"fmt"
	"strings"
)

// ParseMarkdownSkill parses a Markdown skill file with YAML frontmatter
// into a SkillFile. The frontmatter is delimited by --- lines and may
// contain name, description, keywords, and next_skills fields.
// The Markdown body after the frontmatter becomes the Content.
//
// This format is used by open-source skill collections (e.g. obra/superpowers)
// whose skill files are SKILL.md rather than TOML.
func ParseMarkdownSkill(data []byte) (SkillFile, error) {
	// Normalize line endings to \n
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimLeft(text, "\n\t \uFEFF")

	if !strings.HasPrefix(text, "---") {
		return SkillFile{}, fmt.Errorf("markdown skill file must start with YAML frontmatter (---)")
	}

	// Skip the opening --- delimiter and any newline after it
	rest := text[3:]
	rest = strings.TrimLeft(rest, "\n")

	// Find the closing --- delimiter (first line that is exactly ---)
	lines := strings.Split(rest, "\n")
	closeLine := -1
	for i, line := range lines {
		if line == "---" {
			closeLine = i
			break
		}
	}
	if closeLine < 0 {
		return SkillFile{}, fmt.Errorf("markdown skill file missing closing --- frontmatter delimiter")
	}

	frontmatter := strings.Join(lines[:closeLine], "\n")
	body := strings.TrimLeft(strings.Join(lines[closeLine+1:], "\n"), "\n")

	// Parse frontmatter key-value pairs
	var sf SkillFile
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		value = stripYAMLQuotes(value)

		switch key {
		case "name":
			sf.Name = value
		case "description":
			sf.Description = value
		case "keywords":
			sf.Keywords = parseYAMLInlineList(value)
		case "next_skills":
			sf.NextSkills = parseYAMLInlineList(value)
		}
	}

	sf.Content = body
	return sf, nil
}

// IsMarkdownSkill returns true if the content indicates a Markdown skill
// file (starts with YAML frontmatter ---).
func IsMarkdownSkill(body []byte) bool {
	trimmed := strings.TrimLeft(string(body), "\r\n\t \uFEFF")
	return strings.HasPrefix(trimmed, "---")
}

// stripYAMLQuotes removes surrounding single or double quotes from a YAML value.
func stripYAMLQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseYAMLInlineList parses a YAML inline list like ["a", "b", "c"] or a, b, c.
func parseYAMLInlineList(s string) []string {
	s = strings.TrimSpace(s)
	s = stripYAMLQuotes(s)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = stripYAMLQuotes(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
