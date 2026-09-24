package builtin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

// TestArtifactTool_ReadsStoredRef locks the escape hatch: a ref reported by a
// truncated tool result must be readable, otherwise "full output in artifact"
// is a dead end.
func TestArtifactTool_ReadsStoredRef(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.New(dir)
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	ref, err := store.Store([]byte("line1\nline2\nline3\n"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	tool := NewArtifactTool()
	input, _ := json.Marshal(map[string]any{"ref": ref})
	res, err := tool.Run(tools.ToolContext{ArtifactDir: dir}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", res.Status, res.Digest)
	}
	for _, want := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(res.Digest, want) {
			t.Errorf("digest missing %q: %s", want, res.Digest)
		}
	}
}

// TestArtifactTool_OffsetLimit: large artifacts can be paged through.
func TestArtifactTool_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := artifact.New(dir)
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	ref, err := store.Store([]byte("a\nb\nc\nd\n"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	tool := NewArtifactTool()
	input, _ := json.Marshal(map[string]any{"ref": ref, "offset": 2, "limit": 2})
	res, err := tool.Run(tools.ToolContext{ArtifactDir: dir}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := strings.TrimSpace(res.Digest); got != "b\nc" {
		t.Errorf("digest = %q, want %q", got, "b\nc")
	}
}

// TestArtifactTool_MissingRef: an unknown ref reports a clear error instead of
// returning empty content.
func TestArtifactTool_MissingRef(t *testing.T) {
	dir := t.TempDir()
	tool := NewArtifactTool()
	input, _ := json.Marshal(map[string]any{"ref": "sha256:0000000000000000000000000000000000000000000000000000000000000000"})
	res, err := tool.Run(tools.ToolContext{ArtifactDir: dir}, input)
	if res.Status != tools.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if !strings.Contains(res.Digest, "not found") {
		t.Errorf("digest should say the artifact was not found, got %q", res.Digest)
	}
	_ = err
}

// TestArtifactTool_EmptyRef: ref is required.
func TestArtifactTool_EmptyRef(t *testing.T) {
	tool := NewArtifactTool()
	input, _ := json.Marshal(map[string]any{"ref": "  "})
	res, _ := tool.Run(tools.ToolContext{ArtifactDir: t.TempDir()}, input)
	if res.Status != tools.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
}
