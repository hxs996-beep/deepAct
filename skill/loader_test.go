package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadExternalSkills(t *testing.T) {
	// Create a temp directory with a sample skill file
	dir := t.TempDir()
	skillContent := `name = "test_skill"
description = "A test skill for unit testing"
keywords = ["test", "demo"]
content = "You are a test agent."
`
	os.WriteFile(filepath.Join(dir, "test.toml"), []byte(skillContent), 0o644)

	skills, err := LoadExternalSkills(dir)
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
