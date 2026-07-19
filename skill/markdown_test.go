package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMarkdownSkill(t *testing.T) {
	input := `---
name: brainstorming
description: "You MUST use this before any creative work."
keywords: ["test", "demo"]
---

# Brainstorming Ideas Into Designs

Help turn ideas into fully formed designs and specs.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "brainstorming" {
		t.Errorf("name = %q, want %q", sf.Name, "brainstorming")
	}
	if sf.Description != "You MUST use this before any creative work." {
		t.Errorf("description = %q", sf.Description)
	}
	if len(sf.Keywords) != 2 || sf.Keywords[0] != "test" || sf.Keywords[1] != "demo" {
		t.Errorf("keywords = %v", sf.Keywords)
	}
	if !strings.Contains(sf.Content, "Brainstorming Ideas") {
		t.Errorf("content does not contain expected body, got: %q", sf.Content)
	}
}

func TestParseMarkdownSkill_UnquotedValues(t *testing.T) {
	input := `---
name: debugging
description: A debugging skill
---

Debug here.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "debugging" {
		t.Errorf("name = %q", sf.Name)
	}
	if sf.Description != "A debugging skill" {
		t.Errorf("description = %q", sf.Description)
	}
	if !strings.Contains(sf.Content, "Debug here") {
		t.Errorf("content missing body, got: %q", sf.Content)
	}
}

func TestParseMarkdownSkill_NoFrontmatter(t *testing.T) {
	_, err := ParseMarkdownSkill([]byte("# Just markdown\nNo frontmatter."))
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseMarkdownSkill_MissingClosingDelimiter(t *testing.T) {
	input := "---\nname: test\ndescription: \"no closing\"\n"
	_, err := ParseMarkdownSkill([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing closing --- delimiter")
	}
}

func TestParseMarkdownSkill_EmptyFrontmatter(t *testing.T) {
	input := "---\n---\n\n# Body only\n"
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "" {
		t.Errorf("expected empty name, got %q", sf.Name)
	}
	if !strings.Contains(sf.Content, "Body only") {
		t.Errorf("content missing body")
	}
}

func TestParseMarkdownSkill_CRLF(t *testing.T) {
	input := "---\r\nname: test\r\ndescription: \"CRLF skill\"\r\n---\r\n\r\n# Body\r\n"
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "test" {
		t.Errorf("name = %q", sf.Name)
	}
	if sf.Description != "CRLF skill" {
		t.Errorf("description = %q", sf.Description)
	}
	if !strings.Contains(sf.Content, "Body") {
		t.Errorf("content missing body")
	}
}

func TestLoadExternalSkills_Markdown(t *testing.T) {
	dir := t.TempDir()
	skillContent := `---
name: md_skill
description: "A markdown skill"
---

# Markdown Skill

Do things.
`
	os.WriteFile(filepath.Join(dir, "md_skill.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "md_skill" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "A markdown skill" {
		t.Errorf("description = %q", s.Description)
	}
	if !strings.Contains(s.Content, "Markdown Skill") {
		t.Errorf("content missing body")
	}
}

func TestLoadExternalSkills_MixedFormats(t *testing.T) {
	dir := t.TempDir()

	tomlContent := `name = "toml_skill"
description = "A TOML skill"
content = "TOML body."
`
	os.WriteFile(filepath.Join(dir, "toml_skill.toml"), []byte(tomlContent), 0o644)

	mdContent := `---
name: md_skill
description: "A markdown skill"
---

# MD body.
`
	os.WriteFile(filepath.Join(dir, "md_skill.md"), []byte(mdContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	if !names["toml_skill"] || !names["md_skill"] {
		t.Errorf("expected both toml_skill and md_skill, got %v", names)
	}
}

func TestIsMarkdownSkill(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"frontmatter", "---\nname: test\n---\nbody", true},
		{"leading_whitespace", "\n\n---\nname: test", true},
		{"plain_toml", `name = "test"`, false},
		{"markdown_no_frontmatter", "# Just a heading", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMarkdownSkill([]byte(tt.body)); got != tt.want {
				t.Errorf("IsMarkdownSkill() = %v, want %v", got, tt.want)
			}
		})
	}
}
