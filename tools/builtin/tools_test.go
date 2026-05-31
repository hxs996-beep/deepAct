package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

func TestBashTool_SimpleCommand(t *testing.T) {
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "echo hello"})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if !strings.Contains(result.Digest, "hello") {
		t.Errorf("digest = %q, want 'hello'", result.Digest)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", result.ExitCode)
	}
}

func TestBashTool_FailingCommand(t *testing.T) {
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "false"})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected error for failing command")
	}
	if result.ExitCode == nil || *result.ExitCode == 0 {
		t.Errorf("exit_code should be non-zero, got %v", result.ExitCode)
	}
}

func TestBashTool_WorkDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "pwd", WorkDir: dir})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, filepath.Base(dir)) {
		t.Errorf("digest = %q, expected to contain %q", result.Digest, filepath.Base(dir))
	}
}

func TestBashTool_ContextWorkDir(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "pwd"})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, filepath.Base(dir)) {
		t.Errorf("digest = %q, expected to contain %q", result.Digest, filepath.Base(dir))
	}
}

func TestBashTool_BlockedCommand(t *testing.T) {
	tool := NewBashToolWithBlocklist(map[string]string{
		"rm -rf": "destructive operation",
	})
	input, _ := json.Marshal(bashInput{Command: "rm -rf /"})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected error for blocked command")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Digest, "blocked") {
		t.Errorf("digest = %q, want 'blocked'", result.Digest)
	}
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "sleep 10", Timeout: 1})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(result.Digest, "timed out") {
		t.Errorf("digest = %q, want 'timed out'", result.Digest)
	}
}

func TestBashTool_OutputTruncation(t *testing.T) {
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: "yes | head -n 5000"})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(result.Digest) > bashOutputLimit+100 {
		t.Errorf("digest length = %d, expected truncation near %d", len(result.Digest), bashOutputLimit)
	}
}

func TestBashTool_EmptyCommand(t *testing.T) {
	tool := NewBashTool()
	input, _ := json.Marshal(bashInput{Command: ""})

	result, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestGrepTool_PureGoFallback(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\nfunc world() {}\n"), 0o644)

	tool := NewGrepTool()
	input, _ := json.Marshal(grepInput{Pattern: "func", Path: dir})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if !strings.Contains(result.Digest, "func hello") {
		t.Errorf("digest missing 'func hello': %s", result.Digest[:min(200, len(result.Digest))])
	}
	if !strings.Contains(result.Digest, "func world") {
		t.Errorf("digest missing 'func world': %s", result.Digest[:min(200, len(result.Digest))])
	}
}

func TestGrepTool_NoMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\n"), 0o644)

	tool := NewGrepTool()
	input, _ := json.Marshal(grepInput{Pattern: "zzzznonexistent", Path: dir})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Digest, "zzzznonexistent") {
		t.Errorf("should have no matches")
	}
}

func TestGrepTool_IncludeFilter(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("target line\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("target line\n"), 0o644)

	tool := NewGrepTool()
	input, _ := json.Marshal(grepInput{Pattern: "target", Path: dir, Include: "*.go"})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, "a.go") {
		t.Errorf("expected a.go in results: %s", result.Digest)
	}
	if strings.Contains(result.Digest, "b.txt") {
		t.Errorf("b.txt should be filtered out: %s", result.Digest)
	}
}

func TestGlobTool_FindGoFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "util.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi"), 0o644)

	tool := NewGlobTool()
	input, _ := json.Marshal(globInput{Pattern: "**/*.go", Path: dir})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if !strings.Contains(result.Digest, "main.go") {
		t.Errorf("expected main.go in results: %s", result.Digest)
	}
	if !strings.Contains(result.Digest, "util.go") {
		t.Errorf("expected util.go in results: %s", result.Digest)
	}
	if strings.Contains(result.Digest, "readme.md") {
		t.Errorf("readme.md should not match **/*.go: %s", result.Digest)
	}
}

func TestGlobTool_NoMatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)

	tool := NewGlobTool()
	input, _ := json.Marshal(globInput{Pattern: "*.rs", Path: dir})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, "no matches") {
		t.Errorf("expected 'no matches', got: %s", result.Digest)
	}
}

func TestGlobTool_SkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "objects", "hidden.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("x"), 0o644)

	tool := NewGlobTool()
	input, _ := json.Marshal(globInput{Pattern: "**/*.go", Path: dir})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if strings.Contains(result.Digest, "hidden.go") {
		t.Errorf(".git files should be skipped: %s", result.Digest)
	}
	if !strings.Contains(result.Digest, "visible.go") {
		t.Errorf("expected visible.go: %s", result.Digest)
	}
}
