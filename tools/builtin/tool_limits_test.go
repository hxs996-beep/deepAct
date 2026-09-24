package builtin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// --- glob: 截断必须告知 ---

func TestGlobTool_MaxResultsReportsTruncation(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	tool := NewGlobTool()
	input, _ := json.Marshal(map[string]any{"pattern": "*.go", "path": dir, "max_results": 2})
	res, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Digest, "[showing 2 of 4 matches") {
		t.Errorf("truncation must be reported, got %q", res.Digest)
	}
}

func TestGlobTool_NoTruncationNoticeWhenComplete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewGlobTool()
	input, _ := json.Marshal(map[string]any{"pattern": "*.go", "path": dir})
	res, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Digest, "showing") {
		t.Errorf("complete result must not carry a truncation notice, got %q", res.Digest)
	}
}

// --- fetch: 文本类型放行、二进制拒绝 ---

func TestFetchTool_AllowsJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer ts.Close()

	tool := NewFetchTool()
	input, _ := json.Marshal(map[string]any{"url": ts.URL})
	res, err := tool.Run(tools.ToolContext{}, input)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", res.Status, res.Digest)
	}
	if !strings.Contains(res.Digest, `"hello"`) {
		t.Errorf("digest should carry the JSON body, got %q", res.Digest)
	}
}

func TestFetchTool_RejectsBinary(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02})
	}))
	defer ts.Close()

	tool := NewFetchTool()
	input, _ := json.Marshal(map[string]any{"url": ts.URL})
	res, _ := tool.Run(tools.ToolContext{}, input)
	if res.Status != tools.StatusError {
		t.Errorf("status = %q, want error for binary content", res.Status)
	}
	if !strings.Contains(res.Digest, "unsupported content type") {
		t.Errorf("digest = %q", res.Digest)
	}
}

// --- bash 截断：ref 必须可读（逃生口端到端） ---

// TestBashTool_TruncationRefIsReadable locks the escape hatch end to end: a
// truncated bash result reports a ref, and that ref can actually be read back
// with the artifact tool. Without this, "full output in artifact" is a dead end.
func TestBashTool_TruncationRefIsReadable(t *testing.T) {
	dir := t.TempDir()
	tool := NewBashTool()
	input, _ := json.Marshal(map[string]any{"command": "seq 1 20000"})
	res, err := tool.Run(tools.ToolContext{WorkDir: dir, ArtifactDir: dir}, input)
	if err != nil {
		t.Fatalf("bash Run: %v", err)
	}
	if !strings.Contains(res.Digest, "full output in artifact: sha256:") {
		t.Fatalf("truncated output must carry a readable ref, got %q", res.Digest)
	}
	if res.ArtifactRef == "" {
		t.Fatal("ArtifactRef must be set for truncated output")
	}

	art := NewArtifactTool()
	ainput, _ := json.Marshal(map[string]any{"ref": res.ArtifactRef, "limit": 3})
	ares, err := art.Run(tools.ToolContext{ArtifactDir: dir}, ainput)
	if err != nil {
		t.Fatalf("artifact Run: %v", err)
	}
	if ares.Status != tools.StatusOK {
		t.Fatalf("artifact status = %q, digest: %s", ares.Status, ares.Digest)
	}
	if !strings.Contains(ares.Digest, "1") {
		t.Errorf("artifact content should carry the truncated output, got %q", ares.Digest)
	}
}
