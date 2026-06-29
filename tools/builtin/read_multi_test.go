package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

func TestReadMulti_ThreeTargetsOrdered(t *testing.T) {
	dir := t.TempDir()
	// target 1: full read
	fullPath := filepath.Join(dir, "full.txt")
	os.WriteFile(fullPath, []byte("a\nb\nc\n"), 0o644)
	// target 2: offset/limit
	rangePath := filepath.Join(dir, "range.txt")
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	os.WriteFile(rangePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	// target 3: go symbol
	goPath := filepath.Join(dir, "sym.go")
	os.WriteFile(goPath, []byte("package main\n\n// Run does stuff\nfunc Run() {\n  return\n}\n"), 0o644)

	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{
		"targets": []map[string]interface{}{
			{"path": fullPath},
			{"path": rangePath, "offset": 5, "limit": 3},
			{"path": goPath, "symbol": "Run"},
		},
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", result.Status, result.Digest)
	}
	// Headers appear in input order.
	idxFull := strings.Index(result.Digest, "=== [1] "+fullPath+" (full) ===")
	idxRange := strings.Index(result.Digest, "=== [2] "+rangePath+" [L5-7] ===")
	idxSym := strings.Index(result.Digest, "=== [3] "+goPath+" [symbol:Run] ===")
	if idxFull < 0 || idxRange < 0 || idxSym < 0 {
		t.Fatalf("missing/incorrect header in digest:\n%s", result.Digest)
	}
	if !(idxFull < idxRange && idxRange < idxSym) {
		t.Fatalf("targets not in input order: %d %d %d", idxFull, idxRange, idxSym)
	}
	// Metadata comment at top.
	if !strings.HasPrefix(result.Digest, "<!-- read_multi targets:") {
		t.Fatalf("missing metadata comment at top:\n%s", result.Digest[:80])
	}
	// Symbol body present.
	if !strings.Contains(result.Digest, "symbol Run") {
		t.Fatalf("symbol body missing in digest:\n%s", result.Digest)
	}
}

func TestReadMulti_PartialFailure(t *testing.T) {
	dir := t.TempDir()
	okPath := filepath.Join(dir, "ok.txt")
	os.WriteFile(okPath, []byte("hello\n"), 0o644)
	goPath := filepath.Join(dir, "sym.go")
	os.WriteFile(goPath, []byte("package main\n"), 0o644)

	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{
		"targets": []map[string]interface{}{
			{"path": okPath},
			{"path": goPath, "symbol": "Missing"},
		},
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v (batch must not fail on partial error)", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, want ok for partial failure", result.Status)
	}
	if !strings.Contains(result.Digest, "hello") {
		t.Fatalf("ok target content missing:\n%s", result.Digest)
	}
	// ERROR must be attached to target [2] (the missing symbol): it appears
	// after the [2] header, not after the [1] header.
	idx2 := strings.Index(result.Digest, "=== [2] ")
	errIdx := strings.Index(result.Digest, "ERROR:")
	if errIdx < 0 {
		t.Fatalf("missing ERROR marker for failed target:\n%s", result.Digest)
	}
	if idx2 < 0 || errIdx <= idx2 {
		t.Fatalf("ERROR should appear after [2] header, got idx2=%d errIdx=%d\n%s", idx2, errIdx, result.Digest)
	}
}

func TestReadMulti_OffsetOnlyScope(t *testing.T) {
	// offset without limit → scope "L{N}-end" (read to EOF from offset).
	dir := t.TempDir()
	rangePath := filepath.Join(dir, "range.txt")
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	os.WriteFile(rangePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{
		"targets": []map[string]interface{}{
			{"path": rangePath, "offset": 10},
		},
	})

	result, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, "=== [1] "+rangePath+" [L10-end] ===") {
		t.Fatalf("missing L10-end header:\n%s", result.Digest)
	}
	if !strings.Contains(result.Digest, rangePath+"::L10-end") {
		t.Fatalf("missing L10-end scope in metadata:\n%s", result.Digest)
	}
}

func TestReadMulti_TooManyTargets(t *testing.T) {
	dir := t.TempDir()
	targets := make([]map[string]interface{}, 9)
	for i := range targets {
		targets[i] = map[string]interface{}{"path": filepath.Join(dir, "x.txt")}
	}
	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{"targets": targets})
	_, err := tool.Run(tools.ToolContext{WorkDir: dir}, input)
	if err == nil {
		t.Fatal("expected error for >8 targets")
	}
}

func TestReadMulti_EmptyTargets(t *testing.T) {
	tool := NewReadMultiTool()
	input, _ := json.Marshal(map[string]interface{}{"targets": []map[string]interface{}{}})
	_, err := tool.Run(tools.ToolContext{}, input)
	if err == nil {
		t.Fatal("expected error for empty targets")
	}
}
