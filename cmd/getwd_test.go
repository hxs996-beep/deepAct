package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("os.Getwd() = %s", wd)
	t.Logf("filepath.Join(wd, .deepact, skills) = %s", filepath.Join(wd, ".deepact", "skills"))

	// Check if the skills directory exists
	skillsDir := filepath.Join(wd, ".deepact", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatalf("os.ReadDir(%s) failed: %v", skillsDir, err)
	}
	for _, e := range entries {
		t.Logf("  found: %s", e.Name())
	}

	// Also check where LoadExternalSkillsFromPaths is called
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("os.UserHomeDir() = %s", home)
	userSkillsDir := filepath.Join(home, ".deepact", "skills")
	t.Logf("user skills dir: %s", userSkillsDir)
}
