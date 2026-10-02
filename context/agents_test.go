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

// TestLoadAgentsMD_AncestorChain verifies AGENTS.md files are collected from
// the project root (located via the .git marker) down to cwd, so nested /
// monorepo conventions are not lost.
func TestLoadAgentsMD_AncestorChain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "a", "b")
	nested := []struct{ dir, content string }{
		{root, "# 根规则"},
		{filepath.Join(root, "a"), "# a 规则"},
		{cwd, "# b 规则"},
	}
	for _, n := range nested {
		if err := os.MkdirAll(n.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(n.dir, "AGENTS.md"), []byte(n.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files := LoadAgentsMD(cwd)
	wantSources := []string{"AGENTS.md", "a/AGENTS.md", "a/b/AGENTS.md"}
	if len(files) != len(wantSources) {
		t.Fatalf("len(files) = %d, want %d (%+v)", len(files), len(wantSources), files)
	}
	for i, want := range wantSources {
		if files[i].Source != want {
			t.Errorf("files[%d].Source = %q, want %q", i, files[i].Source, want)
		}
	}
	// 根在前（低优先），cwd 在后（高优先）
	if !strings.Contains(files[len(files)-1].Content, "b 规则") {
		t.Errorf("last file = %+v, want cwd content last", files[len(files)-1])
	}
}

// TestLoadAgentsMD_NoMarkerFallsBackToCwd is a regression guard: without a
// project marker we must not silently adopt an arbitrary ancestor's
// conventions — only cwd itself is loaded.
func TestLoadAgentsMD_NoMarkerFallsBackToCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	cwd := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# 祖先规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# cwd 规则"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(cwd)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want only the cwd file", files)
	}
	if !strings.Contains(files[0].Content, "cwd 规则") {
		t.Errorf("files[0].Content = %q, want cwd content", files[0].Content)
	}
}

// TestLoadAgentsMD_OverrideWins verifies AGENTS.override.md replaces
// AGENTS.md in the same directory.
func TestLoadAgentsMD_OverrideWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# 默认规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.override.md"), []byte("# 覆盖规则"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(cwd)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1 (%+v)", len(files), files)
	}
	if files[0].Source != "AGENTS.override.md" {
		t.Errorf("Source = %q, want AGENTS.override.md", files[0].Source)
	}
	if !strings.Contains(files[0].Content, "覆盖规则") {
		t.Errorf("Content = %q, want override content", files[0].Content)
	}
}

// TestLoadAgentsMD_EmptyOverrideFallsBack is a regression guard: a blank
// override file must not erase the directory's instructions.
func TestLoadAgentsMD_EmptyOverrideFallsBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# 默认规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.override.md"), []byte("  \n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(cwd)
	if len(files) != 1 || files[0].Source != "AGENTS.md" {
		t.Fatalf("files = %+v, want fallback to AGENTS.md", files)
	}
	if !strings.Contains(files[0].Content, "默认规则") {
		t.Errorf("Content = %q, want default content", files[0].Content)
	}
}

// TestLoadAgentsMD_BudgetDropsDistantFiles verifies the byte budget evicts the
// files furthest from cwd first, keeping the closest (highest priority) ones.
func TestLoadAgentsMD_BudgetDropsDistantFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 单份即超过预算（每字 3 字节）。
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(strings.Repeat("根", agentsMDMaxBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# 最近规则"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(cwd)
	if len(files) != 1 || files[0].Source != "sub/AGENTS.md" {
		t.Fatalf("files = %+v, want only the nearest file kept", files)
	}
}

// TestLoadAgentsMD_NearestFileAlwaysKept is a regression guard: priority beats
// the budget — the file closest to cwd is never dropped, even oversized.
func TestLoadAgentsMD_NearestFileAlwaysKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(strings.Repeat("x", agentsMDMaxBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadAgentsMD(cwd)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1 (nearest file must never be dropped)", len(files))
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
