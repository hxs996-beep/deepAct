package skill

import (
	"testing"
)

func TestLoadExternalSkills(t *testing.T) {
	skills, err := LoadExternalSkills("D:\\java_project\\deepAct\\.deepact\\skills")
	if err != nil {
		t.Fatalf("LoadExternalSkills failed: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("no skills loaded")
	}
	for _, s := range skills {
		t.Logf("loaded skill: %s - %s", s.Name, s.Description)
	}
}
