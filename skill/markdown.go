package skill

import (
	"fmt"
	"strings"
)

// ParseMarkdownSkill parses a Markdown skill file with YAML frontmatter
// into a SkillFile. The frontmatter is delimited by --- lines and may
// contain all Claude Code-compatible fields.
// The Markdown body after the frontmatter becomes the Content.
//
// This format is used by open-source skill collections (e.g. obra/superpowers)
// and Claude Code, whose skill files are SKILL.md.
func ParseMarkdownSkill(data []byte) (SkillFile, error) {
	// Normalize line endings to \n
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimLeft(text, "\n\t \uFEFF")

	if !strings.HasPrefix(text, "---") {
		// No YAML frontmatter - treat as plain markdown (Claude Code
		// SKILL.md files may omit frontmatter entirely).
		return parsePlainMarkdownSkill(text), nil
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
	fmLines := strings.Split(frontmatter, "\n")

	// Fields that accept YAML lists (inline or multi-line)
	listFields := map[string]bool{
		"keywords":      true,
		"next_skills":   true,
		"allowed-tools": true,
		"arguments":     true,
		"paths":         true,
	}

	for i := 0; i < len(fmLines); i++ {
		line := strings.TrimSpace(fmLines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// Handle list fields (multi-line or inline)
		if listFields[key] {
			if value == "" {
				// Multi-line YAML list: collect following "- item" lines
				var items []string
				for j := i + 1; j < len(fmLines); j++ {
					nextLine := strings.TrimSpace(fmLines[j])
					if strings.HasPrefix(nextLine, "- ") {
						item := strings.TrimSpace(nextLine[2:])
						item = stripYAMLQuotes(item)
						items = append(items, item)
						i = j
					} else if nextLine == "" {
						continue
					} else {
						break
					}
				}
				assignListField(&sf, key, items)
			} else {
				assignListField(&sf, key, parseYAMLInlineList(value))
			}
			continue
		}

		value = stripYAMLQuotes(value)
		assignField(&sf, key, value)
	}

	sf.Content = body
	return sf, nil
}

// parsePlainMarkdownSkill parses a markdown file without YAML frontmatter.
// It extracts the description from a "## Description" section if present
// and uses the entire file as the skill content. The skill name is left
// empty; the caller (loader) uses the directory name as fallback.
func parsePlainMarkdownSkill(text string) SkillFile {
	var sf SkillFile
	sf.Content = text

	lines := strings.Split(text, "\n")
	inDesc := false
	var descLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			heading := strings.TrimSpace(trimmed[3:])
			if inDesc {
				break // end of description section
			}
			if strings.EqualFold(heading, "Description") {
				inDesc = true
			}
		} else if inDesc {
			descLines = append(descLines, line)
		}
	}
	if len(descLines) > 0 {
		sf.Description = strings.TrimSpace(strings.Join(descLines, "\n"))
	}

	return sf
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

// assignField assigns a scalar value to the matching SkillFile field.
func assignField(sf *SkillFile, key, value string) {
	switch key {
	case "name":
		sf.Name = value
	case "description":
		sf.Description = value
	case "when_to_use":
		sf.WhenToUse = value
	case "model":
		sf.Model = value
	case "effort":
		sf.Effort = value
	case "agent":
		sf.Agent = value
	case "context":
		sf.Context = value
	case "hooks":
		sf.Hooks = value
	case "version":
		sf.Version = value
	case "shell":
		sf.Shell = value
	case "argument-hint":
		sf.ArgumentHint = value
	case "user-invocable":
		b := parseYAMLBool(value)
		sf.UserInvocable = &b
	case "disable-model-invocation":
		sf.DisableModelInvocation = parseYAMLBool(value)
	}
}

// assignListField assigns a list value to the matching SkillFile field.
func assignListField(sf *SkillFile, key string, items []string) {
	switch key {
	case "keywords":
		sf.Keywords = items
	case "next_skills":
		sf.NextSkills = items
	case "allowed-tools":
		sf.AllowedTools = items
	case "arguments":
		sf.Arguments = items
	case "paths":
		sf.Paths = items
	}
}

// parseYAMLBool parses a YAML boolean value.
func parseYAMLBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "true" || s == "yes" || s == "1"
}
