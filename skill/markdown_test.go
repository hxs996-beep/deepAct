package skill

import (
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
	// Claude Code SKILL.md files may omit frontmatter entirely.
	// The parser should handle them gracefully, extracting
	// description from ## Description section if present.
	input := `# Smart Integration Test Runner

## Description
Run integration tests, analyze logs for errors, and automatically launch sub-agents to analyze and fix issues.

## Usage
/smart-test [test-pattern]

## Prompt
Run integration tests matching the pattern.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "" {
		t.Errorf("expected empty name, got %q", sf.Name)
	}
	if sf.Description != "Run integration tests, analyze logs for errors, and automatically launch sub-agents to analyze and fix issues." {
		t.Errorf("description = %q", sf.Description)
	}
	if !strings.Contains(sf.Content, "Smart Integration Test Runner") {
		t.Errorf("content missing body")
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

func TestParseMarkdownSkill_NewFields(t *testing.T) {
	input := `---
name: full-skill
description: "A skill with all fields"
allowed-tools:
  - read
  - grep
argument-hint: "[target]"
arguments:
  - target
when_to_use: "Use when you need to analyze code."
model: flash
effort: high
agent: sub
context: fork
version: "1.0"
user-invocable: false
disable-model-invocation: true
paths:
  - "src/**/*.go"
---

# Full Skill

Do the thing.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.Name != "full-skill" {
		t.Errorf("name = %q", sf.Name)
	}
	if len(sf.AllowedTools) != 2 || sf.AllowedTools[0] != "read" || sf.AllowedTools[1] != "grep" {
		t.Errorf("AllowedTools = %v", sf.AllowedTools)
	}
	if sf.ArgumentHint != "[target]" {
		t.Errorf("ArgumentHint = %q", sf.ArgumentHint)
	}
	if len(sf.Arguments) != 1 || sf.Arguments[0] != "target" {
		t.Errorf("Arguments = %v", sf.Arguments)
	}
	if sf.WhenToUse != "Use when you need to analyze code." {
		t.Errorf("WhenToUse = %q", sf.WhenToUse)
	}
	if sf.Model != "flash" {
		t.Errorf("Model = %q", sf.Model)
	}
	if sf.Effort != "high" {
		t.Errorf("Effort = %q", sf.Effort)
	}
	if sf.Agent != "sub" {
		t.Errorf("Agent = %q", sf.Agent)
	}
	if sf.Context != "fork" {
		t.Errorf("Context = %q", sf.Context)
	}
	if sf.Version != "1.0" {
		t.Errorf("Version = %q", sf.Version)
	}
	if sf.UserInvocable == nil || *sf.UserInvocable != false {
		t.Errorf("UserInvocable = %v, want false", sf.UserInvocable)
	}
	if !sf.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = false, want true")
	}
	if len(sf.Paths) != 1 || sf.Paths[0] != "src/**/*.go" {
		t.Errorf("Paths = %v", sf.Paths)
	}
}

func TestParseMarkdownSkill_MultiLineList(t *testing.T) {
	input := `---
name: list-test
keywords:
  - alpha
  - beta
  - gamma
next_skills:
  - skill-a
  - skill-b
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if len(sf.Keywords) != 3 || sf.Keywords[0] != "alpha" || sf.Keywords[2] != "gamma" {
		t.Errorf("keywords = %v", sf.Keywords)
	}
	if len(sf.NextSkills) != 2 || sf.NextSkills[0] != "skill-a" || sf.NextSkills[1] != "skill-b" {
		t.Errorf("next_skills = %v", sf.NextSkills)
	}
}

func TestParseMarkdownSkill_BooleanDefaults(t *testing.T) {
	// When user-invocable and disable-model-invocation are absent,
	// UserInvocable should be nil (defaults to true in SkillFromSkillFile)
	// and DisableModelInvocation should be false.
	input := `---
name: defaults-test
description: "No boolean fields"
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if sf.UserInvocable != nil {
		t.Errorf("UserInvocable = %v, want nil (default true)", sf.UserInvocable)
	}
	if sf.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = true, want false")
	}

	// Verify SkillFromSkillFile applies defaults
	s := SkillFromSkillFile(sf)
	if !s.UserInvocable {
		t.Errorf("Skill.UserInvocable = false, want true (default)")
	}
}

func TestParseMarkdownSkill_InlineListNewFields(t *testing.T) {
	input := `---
name: inline-test
allowed-tools: ["read", "grep", "bash"]
arguments: [arg1, arg2]
paths: ["src/*.go", "test/*.go"]
---

Body.
`
	sf, err := ParseMarkdownSkill([]byte(input))
	if err != nil {
		t.Fatalf("ParseMarkdownSkill failed: %v", err)
	}
	if len(sf.AllowedTools) != 3 || sf.AllowedTools[2] != "bash" {
		t.Errorf("AllowedTools = %v", sf.AllowedTools)
	}
	if len(sf.Arguments) != 2 || sf.Arguments[1] != "arg2" {
		t.Errorf("Arguments = %v", sf.Arguments)
	}
	if len(sf.Paths) != 2 || sf.Paths[0] != "src/*.go" {
		t.Errorf("Paths = %v", sf.Paths)
	}
}
