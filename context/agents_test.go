package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAgentsMD_Cascade verifies user-level + project-level both load,
// project-level placed after user-level (higher priority / closer to task).
func TestLoadAgentsMD_Cascade(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)

	userContent := "# 全局规则\n不要提交 .env"
	projContent := "# 项目规则\n用标准库"

	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte(projContent), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(proj)
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	// 用户级在前，项目级在后
	if files[0].Source != "~/.deepact/AGENTS.md" {
		t.Errorf("files[0].Source = %q, want ~/.deepact/AGENTS.md", files[0].Source)
	}
	if !strings.Contains(files[0].Content, "全局规则") {
		t.Errorf("files[0].Content = %q, want contains 全局规则", files[0].Content)
	}
	if files[1].Source != "AGENTS.md" {
		t.Errorf("files[1].Source = %q, want AGENTS.md", files[1].Source)
	}
	if !strings.Contains(files[1].Content, "项目规则") {
		t.Errorf("files[1].Content = %q, want contains 项目规则", files[1].Content)
	}
}

// TestLoadAgentsMD_UserMissing verifies project-level alone works.
func TestLoadAgentsMD_UserMissing(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# 项目规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(proj)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want 1 project-only file", files)
	}
}

// TestLoadAgentsMD_ProjectMissing verifies user-level alone works.
func TestLoadAgentsMD_ProjectMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte("# 全局规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(t.TempDir())
	if len(files) != 1 || files[0].Source != "~/.deepact/AGENTS.md" {
		t.Fatalf("files = %+v, want 1 user-only file", files)
	}
}

// TestLoadAgentsMD_None verifies empty result when neither exists.
func TestLoadAgentsMD_None(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	files := LoadAgentsMD(t.TempDir())
	if len(files) != 0 {
		t.Fatalf("len(files) = %d, want 0", len(files))
	}
}

// TestLoadAgentsMD_EmptyFile verifies empty files are skipped.
func TestLoadAgentsMD_EmptyFile(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".deepact"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".deepact", "AGENTS.md"), []byte("   \n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# 项目规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := LoadAgentsMD(proj)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want only project file (empty user skipped)", files)
	}
}

// TestRenderAgentsBlock verifies rendering shape: empty input -> empty output.
func TestRenderAgentsBlock(t *testing.T) {
	if got := RenderAgentsBlock(nil); got != "" {
		t.Errorf("RenderAgentsBlock(nil) = %q, want empty", got)
	}
	files := []AgentsFile{
		{Source: "~/.deepact/AGENTS.md", Content: "全局规则"},
		{Source: "AGENTS.md", Content: "项目规则"},
	}
	got := RenderAgentsBlock(files)
	for _, want := range []string{"Project Conventions", "全局规则", "项目规则", "AGENTS.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAgentsBlock() missing %q in:\n%s", want, got)
		}
	}
}

// TestSetAgentsBlock verifies agents block is stored and cleared.
func TestSetAgentsBlock(t *testing.T) {
	a := NewContextAssembler(t.TempDir(), nil)
	a.SetAgentsBlock("\n## Project Conventions (AGENTS.md)\n\n# 规则\n")
	if a.agentsBlock == "" {
		t.Fatal("agentsBlock not stored")
	}
	// 空内容清空
	a.SetAgentsBlock("")
	if a.agentsBlock != "" {
		t.Fatalf("agentsBlock = %q, want empty", a.agentsBlock)
	}
}
