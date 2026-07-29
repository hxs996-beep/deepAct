package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExternalSkills(t *testing.T) {
	// Directory format: <name>/SKILL.md
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test_skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
name: test_skill
description: "A test skill for unit testing"
keywords: ["test", "demo"]
---

You are a test agent.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "test_skill" {
		t.Errorf("name = %q", skills[0].Name)
	}
	if skills[0].Description != "A test skill for unit testing" {
		t.Errorf("description = %q", skills[0].Description)
	}
	if skills[0].BaseDir != skillDir {
		t.Errorf("BaseDir = %q, want %q", skills[0].BaseDir, skillDir)
	}
}

func TestLoadExternalSkills_SkipsReferencesDir(t *testing.T) {
	dir := t.TempDir()

	// Create a references/<skill-name>/ directory with non-skill files
	refDir := filepath.Join(dir, "references", "some-skill")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refDir, "table.md"), []byte("# reference data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a real skill in directory format
	skillDir := filepath.Join(dir, "real_skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
name: real_skill
description: "A real skill"
---

You are a real agent.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "real_skill" {
		t.Errorf("expected real_skill, got %s", skills[0].Name)
	}
}

func TestLoadExternalSkills_DirNameFallback(t *testing.T) {
	// SKILL.md without frontmatter; name should fall back to directory name.
	dir := t.TempDir()

	nestedDir := filepath.Join(dir, "smart-test")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillContent := `# Smart Test

## Description
A skill without frontmatter.

## Prompt
Do things.
`
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "smart-test" {
		t.Errorf("expected name 'smart-test' (dir fallback), got %q", skills[0].Name)
	}
	if skills[0].Description != "A skill without frontmatter." {
		t.Errorf("description = %q", skills[0].Description)
	}
}

func TestLoadExternalSkillsNested(t *testing.T) {
	// Nested SKILL.md with frontmatter (Claude Code layout)
	dir := t.TempDir()
	skillContent := `---
name: nested_skill
description: "A skill in a nested SKILL.md file"
---

You are a nested test agent.
`
	nestedDir := filepath.Join(dir, "nested_skill")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	var found *Skill
	for _, s := range skills {
		if s.Name == "nested_skill" {
			found = s
		}
	}
	if found == nil {
		t.Fatal("nested_skill not loaded")
	}
	if found.Description != "A skill in a nested SKILL.md file" {
		t.Errorf("unexpected description: %q", found.Description)
	}
	if found.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestLoadExternalSkills_NewFields(t *testing.T) {
	// Test that new Claude Code frontmatter fields are loaded.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "full-skill")
	os.MkdirAll(skillDir, 0o755)
	skillContent := `---
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

Do the thing.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if len(s.AllowedTools) != 2 || s.AllowedTools[0] != "read" || s.AllowedTools[1] != "grep" {
		t.Errorf("AllowedTools = %v", s.AllowedTools)
	}
	if s.ArgumentHint != "[target]" {
		t.Errorf("ArgumentHint = %q", s.ArgumentHint)
	}
	if len(s.Arguments) != 1 || s.Arguments[0] != "target" {
		t.Errorf("Arguments = %v", s.Arguments)
	}
	if s.WhenToUse != "Use when you need to analyze code." {
		t.Errorf("WhenToUse = %q", s.WhenToUse)
	}
	if s.Model != "flash" {
		t.Errorf("Model = %q", s.Model)
	}
	if s.Effort != "high" {
		t.Errorf("Effort = %q", s.Effort)
	}
	if s.Agent != "sub" {
		t.Errorf("Agent = %q", s.Agent)
	}
	if s.Context != "fork" {
		t.Errorf("Context = %q", s.Context)
	}
	if s.Version != "1.0" {
		t.Errorf("Version = %q", s.Version)
	}
	if s.UserInvocable != false {
		t.Errorf("UserInvocable = %v, want false", s.UserInvocable)
	}
	if !s.DisableModelInvocation {
		t.Errorf("DisableModelInvocation = false, want true")
	}
	if len(s.Paths) != 1 || s.Paths[0] != "src/**/*.go" {
		t.Errorf("Paths = %v", s.Paths)
	}
}
