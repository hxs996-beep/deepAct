package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// TestReadTool_UnchangedHintOnSecondRead verifies that a read of a file whose
// mtime is unchanged since the previous read carries a "do not re-read" hint.
// Repeated identical reads give the model no new information and are a hallmark
// of the narration+read loop; the hint steers the model to act on what it
// already has instead of re-reading.
func TestReadTool_UnchangedHintOnSecondRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]string{"path": path})

	first, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("first Run error: %v", err)
	}
	if first.Status != tools.StatusOK {
		t.Fatalf("first status = %q, digest: %s", first.Status, first.Digest)
	}
	if strings.Contains(first.Digest, "unchanged since your last read") {
		t.Errorf("first read should not carry the unchanged hint: %s", first.Digest)
	}

	second, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("second Run error: %v", err)
	}
	if !strings.Contains(second.Digest, "unchanged since your last read") {
		t.Errorf("second read should carry the unchanged hint: %s", second.Digest)
	}
}

// TestReadTool_UnchangedHintOnOffsetRead: the hint also applies to offset/limit
// reads, so re-reading the same range is reported as carrying no new information.
func TestReadTool_UnchangedHintOnOffsetRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ranged.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewReadTool()
	input, _ := json.Marshal(map[string]any{"path": path, "offset": 1, "limit": 2})

	first, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("first Run error: %v", err)
	}
	if strings.Contains(first.Digest, "unchanged since your last read") {
		t.Errorf("first ranged read should not carry the unchanged hint: %s", first.Digest)
	}

	second, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("second Run error: %v", err)
	}
	if !strings.Contains(second.Digest, "unchanged since your last read") {
		t.Errorf("second ranged read should carry the unchanged hint: %s", second.Digest)
	}
}
