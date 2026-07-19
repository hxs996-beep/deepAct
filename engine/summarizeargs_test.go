package engine

import (
	"encoding/json"
	"testing"
)

func TestSummarizeArgs(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		want     string
		// wantContains: when set, assert the result contains this substring
		// (used for cases where the exact output is less important than it
		// being non-empty and informative).
		wantContains string
		wantNonEmpty bool
	}{
		{
			name:         "activate_skill shows skill name",
			toolName:     "activate_skill",
			input:        map[string]interface{}{"skill_name": "brainstorming", "reasoning": "explore design"},
			wantContains: "activate skill: brainstorming",
		},
		{
			name:         "skill_install shows skill name",
			toolName:     "skill_install",
			input:        map[string]interface{}{"name": "code-review"},
			wantContains: "install skill: code-review",
		},
		{
			name:         "handoff_to_agent shows agent and goal",
			toolName:     "handoff_to_agent",
			input:        map[string]interface{}{"agent": "architect", "goal": "design the API"},
			wantContains: "→ architect: design the API",
		},
		{
			name:         "handoff_to_agent truncates long goal",
			toolName:     "handoff_to_agent",
			input:        map[string]interface{}{"agent": "coder", "goal": string(make([]byte, 200))},
			wantContains: "→ coder:",
		},
		{
			name: "bash command",
			input: map[string]interface{}{
				"command": "go test ./...",
			},
			toolName: "bash",
			want:     "go test ./...",
		},
		{
			name:     "read full file annotated",
			toolName: "read",
			input:    map[string]interface{}{"path": "/a/b/c.go"},
			want:     "b/c.go (全文)",
		},
		{
			name:         "read with symbol shows scope",
			toolName:     "read",
			input:        map[string]interface{}{"path": "/a/b/c.go", "symbol": "Run"},
			wantContains: "b/c.go (symbol:Run)",
		},
		{
			name:         "read with offset/limit shows end line",
			toolName:     "read",
			input:        map[string]interface{}{"path": "/a/b/c.go", "offset": float64(52), "limit": float64(50)},
			wantContains: "b/c.go (L52-101)",
		},
		{
			name:         "read with offset only shows open-ended range",
			toolName:     "read",
			input:        map[string]interface{}{"path": "/a/b/c.go", "offset": float64(200)},
			wantContains: "b/c.go (L200-)",
		},
		{
			name:         "grep with include filter shows file scope",
			toolName:     "grep",
			input:        map[string]interface{}{"pattern": "foo", "include": "*.go"},
			want:         "foo (*.go)",
		},
		{
			name:         "grep with path and include shows both",
			toolName:     "grep",
			input:        map[string]interface{}{"pattern": "foo", "path": "/a/src", "include": "*.go"},
			wantContains: "foo in a/src (*.go)",
		},
		{
			name:         "read_multi shows target paths",
			toolName:     "read_multi",
			input: map[string]interface{}{
				"targets": []map[string]interface{}{
					{"path": "/a/b.go"},
					{"path": "/a/c.go"},
				},
			},
			wantContains: "b.go",
		},
		{
			name:         "read_multi with symbol shows scope",
			toolName:     "read_multi",
			input: map[string]interface{}{
				"targets": []map[string]interface{}{
					{"path": "/a/b.go", "symbol": "Run"},
				},
			},
			wantContains: "b.go (symbol:Run)",
		},
		{
			name:         "read_multi does not show bare tool name",
			toolName:     "read_multi",
			input: map[string]interface{}{
				"targets": []map[string]interface{}{
					{"path": "/a/b.go"},
				},
			},
			wantContains: "b.go",
		},
		{
			name:         "unknown MCP tool falls back to a labeled string field",
			toolName:     "mcp_github_create_issue",
			input:        map[string]interface{}{"title": "Fix bug", "body": "..."},
			wantContains: ": ", // e.g. "title: Fix bug" or "body: ..."
			wantNonEmpty: true,
		},
		{
			name:         "unknown tool with only non-string fields still non-empty",
			toolName:     "weird_tool",
			input:        map[string]interface{}{"count": float64(3)},
			wantNonEmpty: true,
		},
		{
			name:         "empty input falls back to tool name",
			toolName:     "mystery_tool",
			input:        map[string]interface{}{},
			want:         "mystery_tool",
			wantNonEmpty: true,
		},
		{
			name:         "nil-ish input (no fields) falls back to tool name",
			toolName:     "mystery_tool",
			input:        map[string]interface{}{},
			want:         "mystery_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := summarizeArgs(tt.toolName, raw, "")

			if tt.want != "" && got != tt.want {
				t.Fatalf("summarizeArgs(%q) = %q, want %q", tt.toolName, got, tt.want)
			}
			if tt.wantContains != "" && !containsSubstr(got, tt.wantContains) {
				t.Fatalf("summarizeArgs(%q) = %q, want it to contain %q", tt.toolName, got, tt.wantContains)
			}
			if tt.wantNonEmpty && got == "" {
				t.Fatalf("summarizeArgs(%q) = empty, want non-empty", tt.toolName)
			}
		})
	}
}

// TestSummarizeArgsNeverEmpty is the core regression guard: every tool call
// must produce a non-empty Detail so the UI never shows a bare "[*]  ✓" node.
func TestSummarizeArgsNeverEmpty(t *testing.T) {
	tools := []string{
		"activate_skill", "skill_install", "handoff_to_agent",
		"bash", "read", "read_multi", "grep", "glob", "edit", "write", "lsp",
		"mcp_unknown_tool", "custom_tool", "",
	}
	inputs := []map[string]interface{}{
		{},
		{"name": "x"},
		{"skill_name": "y"},
		{"agent": "z", "goal": "g"},
		{"path": "/p/q.go"},
		{"command": "ls"},
		{"count": float64(1)},
	}
	for _, tool := range tools {
		for _, in := range inputs {
			raw, _ := json.Marshal(in)
			got := summarizeArgs(tool, raw, "")
			if got == "" {
				t.Errorf("summarizeArgs(%q, %v) returned empty — UI would show a bare icon", tool, in)
			}
		}
	}
}

func containsSubstr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
