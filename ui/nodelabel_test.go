package ui

import "testing"

func TestNodeDetailLabelFallback(t *testing.T) {
	tests := []struct {
		name string
		node ToolNode
		want string
	}{
		{"uses detail when present", ToolNode{Name: "bash", Detail: "go build"}, "go build"},
		{"falls back to name when detail empty", ToolNode{Name: "activate_skill", Detail: ""}, "activate_skill"},
		{"falls back to name when detail whitespace", ToolNode{Name: "handoff_to_agent", Detail: "   "}, "handoff_to_agent"},
		{"em-dash when both empty", ToolNode{Name: "", Detail: ""}, "—"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeDetailLabel(tt.node); got != tt.want {
				t.Fatalf("nodeDetailLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRenderExecBlockNeverBareIcon: even with an empty-Detail tool, the
// rendered exec block must contain the tool name so a node is never just "[*]  ✓".
func TestRenderExecBlockNeverBareIcon(t *testing.T) {
	lines := renderExecBlock([]ToolNode{
		{Name: "activate_skill", Detail: "", Icon: "[*]", Done: true},
	}, 80)
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	if !contains(joined, "activate_skill") {
		t.Fatalf("exec block should contain tool name for empty-Detail node, got:\n%s", joined)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
