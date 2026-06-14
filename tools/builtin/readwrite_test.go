package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

func TestReadTool_BasicFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("line one\nline two\nline three\n"), 0o644)

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"path": path})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}
	if !strings.Contains(result.Digest, "1: line one") {
		t.Errorf("expected line-numbered output, got: %s", result.Digest[:min(100, len(result.Digest))])
	}
	if !strings.Contains(result.Digest, "3: line three") {
		t.Errorf("expected line 3, got: %s", result.Digest[:min(100, len(result.Digest))])
	}
}

func TestReadTool_OffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = strings.Repeat("x", 10)
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"path": path, "offset": 10, "limit": 5})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	outputLines := strings.Split(strings.TrimSpace(result.Digest), "\n")
	// The digest includes a trailing lspHint (---\nNeed to find...),
	// so we check the content portion separately.
	contentLines := 0
	for _, l := range outputLines {
		if l == "" || l == "---" || strings.HasPrefix(l, "Need to find") {
			break
		}
		contentLines++
	}
	if contentLines != 5 {
		t.Errorf("got %d content lines, want 5 (offset=10, limit=5)", contentLines)
	}
	if !strings.HasPrefix(outputLines[0], "10: ") {
		t.Errorf("first line = %q, want prefix '10: '", outputLines[0])
	}
}

func TestReadTool_BinaryDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	data := make([]byte, 100)
	data[50] = 0
	os.WriteFile(path, data, 0o644)

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"path": path})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for binary file")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestReadTool_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	tool := NewReadTool()
	input, _ := json.Marshal(map[string]interface{}{"path": filepath.Join(dir, "nonexistent.txt")})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestWriteTool_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "new.txt")

	tool := NewWriteTool()
	input, _ := json.Marshal(map[string]interface{}{"path": path, "content": "hello world"})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q, digest: %s", result.Status, result.Digest)
	}

	content, _ := os.ReadFile(path)
	if string(content) != "hello world" {
		t.Errorf("content = %q, want %q", string(content), "hello world")
	}
}

func TestWriteTool_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	os.WriteFile(path, []byte("old content"), 0o644)

	tool := NewWriteTool()
	input, _ := json.Marshal(map[string]interface{}{"path": path, "content": "new content"})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Errorf("status = %q", result.Status)
	}

	content, _ := os.ReadFile(path)
	if string(content) != "new content" {
		t.Errorf("content = %q", string(content))
	}
}
