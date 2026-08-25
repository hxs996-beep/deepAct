package context

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lightweight codebase snapshot constants. The tree is built once at startup
// and cached in the stable session block (Block S), so it must stay compact to
// bound prompt cost while still giving the model global orientation.
const (
	treeMaxDepth   = 3   // directory depth below the project root to descend into
	treeMaxEntries = 300 // hard cap on rendered entries
)

// treeSkipDirs are directories never rendered — VCS metadata, generated output,
// dependencies, and agent tooling. Keeping these out makes the snapshot useful
// and small; the model can always grep/glob into them on demand.
var treeSkipDirs = map[string]bool{
	".git": true, ".idea": true, ".vscode": true, ".claude": true,
	".superpowers": true, ".omo": true, ".deepact": true, ".cache": true,
	"node_modules": true, "vendor": true,
	"dist": true, "build": true, "target": true, "out": true,
	"bin": true, "coverage": true, "__pycache__": true,
	".next": true, ".nuxt": true, ".terraform": true,
}

// treeSkipFiles are individual files never rendered (OS noise, secrets).
var treeSkipFiles = map[string]bool{
	".DS_Store": true, ".env": true, ".local": true,
}

// buildDirTree renders a compact directory tree snapshot rooted at root. It is
// language-agnostic (unlike the removed Go-only RepoMap) and bounded by depth
// and entry count so it stays a small, stable prefix-cache-friendly block.
func buildDirTree(root string, maxDepth, maxEntries int) string {
	if root == "" {
		return ""
	}
	rootName := filepath.Base(root)
	if rootName == "" || rootName == "." || rootName == "/" || rootName == string(os.PathSeparator) {
		rootName = root
	}

	var b strings.Builder
	b.WriteString(rootName + "/\n")
	count := 0

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission errors or vanished entries: prune and continue.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		name := d.Name()

		if d.IsDir() {
			if treeSkipDirs[name] {
				return filepath.SkipDir
			}
			depth := strings.Count(rel, string(os.PathSeparator)) + 1
			if depth > maxDepth {
				return filepath.SkipDir
			}
			if count >= maxEntries {
				return filepath.SkipDir
			}
			indent := strings.Repeat("  ", depth-1)
			b.WriteString(indent + name + "/\n")
			count++
			return nil
		}

		// Regular file.
		if treeSkipFiles[name] {
			return nil
		}
		if count >= maxEntries {
			return nil
		}
		depth := strings.Count(rel, string(os.PathSeparator)) + 1
		if depth > maxDepth+1 {
			// Nested file beyond the rendering depth: listed as itself but we
			// only descend into directories, so a file can never exceed the
			// directory depth cap plus one.
			return nil
		}
		indent := strings.Repeat("  ", depth-1)
		b.WriteString(indent + name + "\n")
		count++
		return nil
	})

	if count >= maxEntries {
		b.WriteString("  ... (truncated, " + strconv.Itoa(count) + " entries shown)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
