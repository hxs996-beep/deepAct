package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTreeFixture builds a small project under a temp dir with representative
// real source, skip-listed noise, and nested depth.
func makeTreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{
		"go.mod",
		"main.go",
		"internal/engine/loop.go",
		"internal/engine/loop.extra.go",
		"pkg/util/helper.go",
		"pkg/util/helper.go.bak", // hidden by .gitignore-style noise not handled, but depth-capped
		".git/HEAD",
		"node_modules/dep/index.js",
		"dist/app.js",
		".DS_Store",
	} {
		path := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func TestBuildDirTree_IncludesTopLevelEntries(t *testing.T) {
	root := makeTreeFixture(t)
	tree := buildDirTree(root, treeMaxDepth, treeMaxEntries)
	for _, want := range []string{"go.mod", "main.go", "internal/", "pkg/"} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree missing %q:\n%s", want, tree)
		}
	}
}

func TestBuildDirTree_SkipsNoiseDirs(t *testing.T) {
	root := makeTreeFixture(t)
	tree := buildDirTree(root, treeMaxDepth, treeMaxEntries)
	for _, banned := range []string{".git/", "node_modules/", "dist/", ".DS_Store"} {
		if strings.Contains(tree, banned) {
			t.Errorf("tree should not contain %q:\n%s", banned, tree)
		}
	}
}

func TestBuildDirTree_RespectsMaxDepth(t *testing.T) {
	root := makeTreeFixture(t)
	// Depth 1: only top-level dirs and files, no children of subdirs.
	tree := buildDirTree(root, 1, treeMaxEntries)
	if strings.Contains(tree, "loop.go") {
		t.Errorf("depth=1 tree should not descend into internal/ to show loop.go:\n%s", tree)
	}
	if !strings.Contains(tree, "internal/") {
		t.Errorf("depth=1 tree should still list top-level dirs:\n%s", tree)
	}
}

func TestBuildDirTree_RespectsMaxEntries(t *testing.T) {
	root := makeTreeFixture(t)
	tree := buildDirTree(root, treeMaxDepth, 2)
	if !strings.Contains(tree, "truncated") {
		t.Errorf("expected truncation marker when entry cap is hit:\n%s", tree)
	}
}

func TestBuildDirTree_EmptyRoot(t *testing.T) {
	if got := buildDirTree("", treeMaxDepth, treeMaxEntries); got != "" {
		t.Errorf("empty root should produce empty tree, got %q", got)
	}
}

func TestBuildStableSessionContext_IncludesDirTree(t *testing.T) {
	root := makeTreeFixture(t)
	env := EnvironmentInfo{
		OS:      "linux",
		Arch:    "amd64",
		CWD:     root,
		Date:    "2026-01-01",
		DirTree: buildDirTree(root, treeMaxDepth, treeMaxEntries),
	}
	s := BuildStableSessionContext(env, "中文")
	for _, want := range []string{"# Block S", "## 代码库结构", "main.go", "internal/"} {
		if !strings.Contains(s, want) {
			t.Errorf("stable context missing %q:\n%s", want, s)
		}
	}

	en := BuildStableSessionContext(env, "en")
	for _, want := range []string{"## Codebase", "main.go"} {
		if !strings.Contains(en, want) {
			t.Errorf("english stable context missing %q:\n%s", want, en)
		}
	}
}
