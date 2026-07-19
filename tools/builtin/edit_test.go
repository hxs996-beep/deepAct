package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/deepact/deepact/tools"
)

func TestEditTool_ExactReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func hello() {\n\treturn \"hello\"\n}\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:      path,
		OldString: "return \"hello\"",
		NewString: "return \"world\"",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, want ok. digest: %s", result.Status, result.Digest)
	}

	content, _ := os.ReadFile(path)
	if got := string(content); got != "func hello() {\n\treturn \"world\"\n}\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestEditTool_FuzzyMatchTrailingWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line one  \nline two\nline three\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:      path,
		OldString: "line one\nline two",
		NewString: "LINE ONE\nLINE TWO",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}

	content, _ := os.ReadFile(path)
	if got := string(content); got != "LINE ONE\nLINE TWO\nline three\n" {
		t.Errorf("file content = %q", got)
	}
}

// TestEditTool_FilePathAlias verifies the edit tool accepts `file_path` as an
// alias for `path`. DeepSeek models sometimes emit `file_path` (Anthropic
// convention) instead of the declared `path` key; without alias support the
// tool errors with "path is required" and the model retries in a loop.
func TestEditTool_FilePathAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func hello() {\n\treturn \"hello\"\n}\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(map[string]string{
		"file_path":  path,
		"old_string": "return \"hello\"",
		"new_string": "return \"world\"",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, want ok. digest: %s", result.Status, result.Digest)
	}
	content, _ := os.ReadFile(path)
	if got := string(content); got != "func hello() {\n\treturn \"world\"\n}\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestEditTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:      path,
		OldString: "nonexistent string",
		NewString: "replacement",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestEditTool_MultipleMatchesBlocked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("foo\nbar\nfoo\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:      path,
		OldString: "foo",
		NewString: "baz",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestEditTool_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("foo\nbar\nfoo\n"), 0o644)

	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:       path,
		OldString:  "foo",
		NewString:  "baz",
		ReplaceAll: true,
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q", result.Status)
	}

	content, _ := os.ReadFile(path)
	if got := string(content); got != "baz\nbar\nbaz\n" {
		t.Errorf("file content = %q", got)
	}
}

func TestEditTool_FileNotExists(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditTool()
	input, _ := json.Marshal(editInput{
		Path:      filepath.Join(dir, "nonexistent.txt"),
		OldString: "a",
		NewString: "b",
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}
