package cmd

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/skill"
)

func TestBuildSkillsBlock_LoadSemanticsWording(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&skill.Skill{Name: "brainstorming", Description: "design before code"})
	reg.Register(&skill.Skill{Name: "hidden", Description: "no model", DisableModelInvocation: true})

	got := buildSkillsBlock(reg.All())

	if !strings.Contains(got, "load_skill") {
		t.Errorf("catalog must mention load_skill tool, got %q", got)
	}
	if strings.Contains(got, "BLOCKING REQUIREMENT") {
		t.Errorf("old activate semantics wording must be gone, got %q", got)
	}
	if !strings.Contains(got, "brainstorming") {
		t.Errorf("catalog must list invocable skill, got %q", got)
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("DisableModelInvocation skill must not appear in catalog, got %q", got)
	}
}

func TestBuildSkillsBlock_Empty(t *testing.T) {
	if got := buildSkillsBlock(nil); got != "" {
		t.Errorf("expected empty block for no skills, got %q", got)
	}
}
