package engine

import (
	"encoding/json"
	"testing"
)

// TestExtractToolKey_NormalizesPathVariations verifies the loop-detection key
// is invariant to how the model addresses the same physical file: relative
// path, "./" prefix, and absolute file_path must all collapse to ONE key.
// Without normalization the model splits its read count across path forms and
// never trips the guard — the root cause of repeated reads during exploration.
func TestExtractToolKey_NormalizesPathVariations(t *testing.T) {
	workDir := "/Users/x/deepact"
	cases := []struct {
		label string
		call  ToolCallRequest
	}{
		{"relative", ToolCallRequest{Name: "read", Input: json.RawMessage(`{"path":"ui/model.go"}`)}},
		{"dot-relative", ToolCallRequest{Name: "read", Input: json.RawMessage(`{"path":"./ui/model.go"}`)}},
		{"abs-file_path", ToolCallRequest{Name: "read", Input: json.RawMessage(`{"file_path":"/Users/x/deepact/ui/model.go"}`)}},
		{"abs-path", ToolCallRequest{Name: "read", Input: json.RawMessage(`{"path":"/Users/x/deepact/ui/model.go"}`)}},
	}
	keys := map[string]string{}
	for _, c := range cases {
		keys[c.label] = extractToolKey(c.call, workDir)
	}
	distinct := map[string]bool{}
	for _, k := range keys {
		distinct[k] = true
	}
	if len(distinct) != 1 {
		t.Fatalf("expected 1 normalized key for the same file, got %d: %+v", len(distinct), keys)
	}
}

// TestLoopGuard_NormalizedPathsAccumulate confirms that after normalization,
// reads of the same file via different path forms accumulate into one count
// and trip the guard at maxRepeats — the behavior that was broken before.
func TestLoopGuard_NormalizedPathsAccumulate(t *testing.T) {
	workDir := "/Users/x/deepact"
	g := NewLoopGuard(workDir, 4)
	calls := []ToolCallRequest{
		{Name: "read", Input: json.RawMessage(`{"path":"ui/model.go"}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"./ui/model.go"}`)},
		{Name: "read", Input: json.RawMessage(`{"file_path":"/Users/x/deepact/ui/model.go"}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"/Users/x/deepact/ui/model.go"}`)}, // 4th → block
	}
	for i, c := range calls {
		a := g.Check(c)
		if i < 3 && a.Type != GuardAllow {
			t.Fatalf("call %d: want allow, got %s (%s)", i, a.Type, a.Message)
		}
		if i == 3 && a.Type != GuardBlock {
			t.Fatalf("call %d: want block (accumulated across path forms), got %s", i, a.Type)
		}
	}
}

// TestNormalizePath covers the canonicalization helper directly.
func TestNormalizePath(t *testing.T) {
	workDir := "/Users/x/deepact"
	tests := []struct{ in, want string }{
		{"ui/model.go", "/Users/x/deepact/ui/model.go"},
		{"./ui/model.go", "/Users/x/deepact/ui/model.go"},
		{"ui/../ui/model.go", "/Users/x/deepact/ui/model.go"},
		{"/Users/x/deepact/ui/model.go", "/Users/x/deepact/ui/model.go"},
		{"", ""},
		{"  ", ""},
	}
	for _, tt := range tests {
		if got := normalizePath(tt.in, workDir); got != tt.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Empty workDir: keep relative cleaned (cannot absolutize without a base).
	if got := normalizePath("ui/model.go", ""); got != "ui/model.go" {
		t.Errorf("empty workDir: got %q, want ui/model.go", got)
	}
}
