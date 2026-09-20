package skill

import (
	"strings"
	"testing"
)

func TestBuiltinSkills_Parsable(t *testing.T) {
	skills, err := BuiltinSkills()
	if err != nil {
		t.Fatalf("BuiltinSkills() error: %v", err)
	}
	want := map[string]bool{"ratd": false, "collab": false}
	for _, s := range skills {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected builtin skill %q", s.Name)
		}
		want[s.Name] = true
		if s.Description == "" {
			t.Errorf("skill %q missing description", s.Name)
		}
		if s.WhenToUse == "" {
			t.Errorf("skill %q missing when_to_use", s.Name)
		}
		if !isValidSkillName(s.Name) {
			t.Errorf("skill %q has invalid name", s.Name)
		}
		if !strings.Contains(s.Content, "handoff_to_agent") {
			t.Errorf("skill %q content should mention handoff_to_agent", s.Name)
		}
		if !strings.Contains(s.Content, "task_complete") {
			t.Errorf("skill %q content should mention task_complete", s.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("builtin skill %q missing", name)
		}
	}
}

// isValidSkillName 复制 engine/loop.go 的校验逻辑（skill 包不依赖 engine，避免循环依赖）。
func isValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > 30 {
		return false
	}
	for _, r := range name {
		if r != '-' && r != '_' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
