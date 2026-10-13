package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// runRememberPreference invokes the tool and fails on a tool-internal error.
func runRememberPreference(t *testing.T, workDir, pref string) tools.ToolResultEnvelope {
	t.Helper()
	input, err := json.Marshal(map[string]string{"preference": pref})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	env, err := NewRememberPreferenceTool().Run(tools.ToolContext{WorkDir: workDir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	return env
}

func TestRememberPreference_CreatesFileWithSection(t *testing.T) {
	dir := t.TempDir()
	env := runRememberPreference(t, dir, "回复用中文")
	if env.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest = %s", env.Status, env.Digest)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	got := string(data)
	for _, want := range []string{preferenceSectionHeading, "- 回复用中文"} {
		if !strings.Contains(got, want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, got)
		}
	}
}

func TestRememberPreference_AppendsToExistingSection(t *testing.T) {
	dir := t.TempDir()
	original := "# 项目规则\n\n" + preferenceSectionHeading + "\n\n- 已有偏好\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runRememberPreference(t, dir, "新偏好")

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "- 已有偏好") || !strings.Contains(got, "- 新偏好") {
		t.Errorf("both bullets should remain:\n%s", got)
	}
	if strings.Count(got, preferenceSectionHeading) != 1 {
		t.Errorf("section should not be duplicated:\n%s", got)
	}
}

// TestRememberPreference_MigratesLegacyHeading: a section written by an older
// build (legacy Chinese heading) must be renamed in place instead of gaining a
// second, English section — the loader injects AGENTS.md verbatim into the prompt,
// so two sections would split one preference list into two.
func TestRememberPreference_MigratesLegacyHeading(t *testing.T) {
	dir := t.TempDir()
	original := "# 项目规则\n\n" + legacyPreferenceHeadings[0] + "\n\n- 已有偏好\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runRememberPreference(t, dir, "新偏好")

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	got := string(data)
	if strings.Contains(got, legacyPreferenceHeadings[0]) {
		t.Errorf("legacy heading should be renamed:\n%s", got)
	}
	if n := strings.Count(got, preferenceSectionHeading); n != 1 {
		t.Errorf("section should appear once, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "- 已有偏好") || !strings.Contains(got, "- 新偏好") {
		t.Errorf("both bullets should remain:\n%s", got)
	}
}

// TestRememberPreference_MigratesHeadingWhenBulletAlreadyRecorded: the rename
// must reach the file even on the dedup path, where no new bullet is added.
func TestRememberPreference_MigratesHeadingWhenBulletAlreadyRecorded(t *testing.T) {
	dir := t.TempDir()
	original := legacyPreferenceHeadings[0] + "\n\n- 回复用中文\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	env := runRememberPreference(t, dir, "回复用中文")
	if env.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest = %s", env.Status, env.Digest)
	}

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	got := string(data)
	if strings.Contains(got, legacyPreferenceHeadings[0]) {
		t.Errorf("legacy heading should be renamed even when the bullet already exists:\n%s", got)
	}
	if !strings.Contains(got, preferenceSectionHeading) {
		t.Errorf("AGENTS.md missing %q:\n%s", preferenceSectionHeading, got)
	}
	if n := strings.Count(got, "- 回复用中文"); n != 1 {
		t.Errorf("bullet count = %d, want 1:\n%s", n, got)
	}
}

func TestRememberPreference_DedupNoop(t *testing.T) {
	dir := t.TempDir()
	runRememberPreference(t, dir, "回复用中文")
	// Whitespace-only differences must dedup to the same bullet.
	env := runRememberPreference(t, dir, "  回复用中文  ")
	if env.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest = %s", env.Status, env.Digest)
	}
	if !strings.Contains(env.Digest, "already recorded") {
		t.Errorf("digest = %q, want already recorded", env.Digest)
	}
	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if n := strings.Count(string(data), "- 回复用中文"); n != 1 {
		t.Errorf("bullet count = %d, want 1:\n%s", n, data)
	}
}

func TestRememberPreference_WritesProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	runRememberPreference(t, sub, "偏好X")

	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Errorf("expected AGENTS.md at project root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md should not be written to cwd when a project root exists")
	}
}

func TestRememberPreference_EmptyRejected(t *testing.T) {
	dir := t.TempDir()
	input, err := json.Marshal(map[string]string{"preference": "   "})
	if err != nil {
		t.Fatal(err)
	}
	env, err := NewRememberPreferenceTool().Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for empty preference")
	}
	if env.Status != tools.StatusError {
		t.Errorf("status = %q, want error", env.Status)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(statErr) {
		t.Errorf("no file should be created for empty preference")
	}
}
