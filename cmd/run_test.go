package cmd

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// TestBuildSkillsBlock_MustLoadDirective locks the strong "MUST" directive at
// the top of the catalog: a possible match (1%) means load first via load_skill,
// and it is not optional.
func TestBuildSkillsBlock_MustLoadDirective(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&skill.Skill{Name: "x", Description: "y"})

	got := buildSkillsBlock(reg.All())

	if !strings.Contains(got, "This is not optional") {
		t.Errorf("catalog must carry a non-optional MUST directive, got %q", got)
	}
	mu := strings.Index(got, "Mandatory")
	li := strings.Index(got, "- **")
	if mu < 0 || li < 0 || mu > li {
		t.Errorf("MUST directive must precede the skill list: must@%d list@%d", mu, li)
	}
	if !strings.Contains(got, "load_skill") {
		t.Errorf("catalog must name the load_skill tool, got %q", got)
	}
}

// TestBuildSkillsBlock_DescriptionCapped locks the catalog size guard: a long
// description is truncated to the cap on rune boundaries (never splitting a
// multi-byte character).
func TestBuildSkillsBlock_DescriptionCapped(t *testing.T) {
	long := strings.Repeat("长描述", 400) // 1200 runes, well over the cap
	reg := skill.NewRegistry()
	reg.Register(&skill.Skill{Name: "big", Description: long})

	got := buildSkillsBlock(reg.All())

	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "- **big**") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("catalog entry for 'big' not found in %q", got)
	}
	desc := strings.TrimPrefix(line, "- **big**: ")
	// when_to_use is appended after the description; take the part before it.
	if i := strings.Index(desc, " Use when "); i >= 0 {
		desc = desc[:i]
	}
	if n := len([]rune(desc)); n > skillsCatalogDescriptionMaxLen {
		t.Errorf("description runes = %d, want <= %d", n, skillsCatalogDescriptionMaxLen)
	}
	if !strings.HasSuffix(desc, "...") {
		t.Errorf("truncated description must end with ..., got %q", desc)
	}
	if !utf8.ValidString(desc) {
		t.Errorf("truncation must not split a rune, got invalid UTF-8")
	}
}
