package engine

import (
	"encoding/json"
	"testing"
)

func TestExtractToolKey_ReadScope(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare read", `{"path":"a.go"}`, "read:a.go::"},
		{"symbol", `{"path":"a.go","symbol":"Run"}`, "read:a.go::symbol:Run"},
		{"offset+limit", `{"path":"a.go","offset":10,"limit":50}`, "read:a.go::L10-50"},
		{"no path", `{"symbol":"Run"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolKey(ToolCallRequest{Name: "read", Input: json.RawMessage(tt.input)})
			if got != tt.want {
				t.Errorf("extractToolKey(read, %s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractToolKey_EditAndWriteUnchanged(t *testing.T) {
	// edit/write still use content hash — ensure refactor didn't break them.
	k := extractToolKey(ToolCallRequest{Name: "edit", Input: json.RawMessage(`{"path":"a.go","old_string":"x","new_string":"y"}`)})
	if k == "" || k == "read:" {
		t.Errorf("edit key unexpected: %q", k)
	}
	if k2 := extractToolKey(ToolCallRequest{Name: "grep", Input: json.RawMessage(`{"path":"a.go"}`)}); k2 != "" {
		t.Errorf("grep should not be tracked, got %q", k2)
	}
}
