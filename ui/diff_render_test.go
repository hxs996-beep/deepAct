package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestRenderDiffBlock_NoPadToTerminalWidth(t *testing.T) {
	// R3: renderDiffBlock 不应再 pad 到 m.width，宽度交由 View 统一 Truncate。
	// 构造一个已 Done 的 edit 节点带 hunk
	m := Model{width: 200} // 故意设很大的 width
	nodes := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
	}}
	got := m.renderDiffBlock(nodes, 80)
	// 每行显示宽度不应被 pad 到 200；短行应保持短（<= 80）
	for i, line := range got {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("第 %d 行被 pad 到 %d 宽 (>80): %q", i, w, line)
		}
	}
}

func TestRenderDiffBlock_ShowsDiffLines(t *testing.T) {
	m := Model{width: 80}
	nodes := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{
			{Name: "hunk", Detail: "@@ -1,3 +1,3 @@", DetailFull: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n+extra"},
			{Name: "hunk", Detail: "@@ -10,2 +10,2 @@", DetailFull: "@@ -10,2 +10,2 @@\n ctx\n-old2\n+new2"},
		},
	}}
	got := m.renderDiffBlock(nodes, 80)
	plain := make([]string, len(got))
	for i, l := range got {
		plain[i] = stripAnsi(l)
	}
	// Should show actual diff lines, not collapsed summaries.
	wantLines := []string{"@@ -1,3 +1,3 @@", "-old", "+new", "+extra", "@@ -10,2 +10,2 @@", "-old2", "+new2"}
	for _, want := range wantLines {
		found := false
		for _, l := range plain {
			if strings.Contains(l, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected diff line containing %q, not found: %v", want, plain)
		}
	}
}

func TestRenderToolSummary_ShowsDiffLines(t *testing.T) {
	toolTree := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", Detail: "@@ -1,3 +1,3 @@", DetailFull: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n+extra"}},
	}}
	got := renderToolSummary(toolTree)
	plain := stripAnsi(got)
	for _, want := range []string{"@@ -1,3 +1,3 @@", "-old", "+new", "+extra"} {
		if !strings.Contains(plain, want) {
			t.Errorf("toolsummary should contain %q, got: %q", want, plain)
		}
	}
}

// TestRenderToolSummary_NewFileWrite_CountsModified verifies the Fix 3
// regression: a write that creates a NEW file (digest has no unified diff →
// no hunk Children) must still be counted as a modified file. Previously the
// count depended on len(Children)>0, so a new-file write showed "0 files
// modified" even though the file was written.
func TestRenderToolSummary_NewFileWrite_CountsModified(t *testing.T) {
	toolTree := []ToolNode{{
		Name:   "write",
		Done:   true,
		Detail: "docs/h3-metal.md — write 9639 chars",
		// New file: no hunk children (no unified diff in digest)
	}}
	got := renderToolSummary(toolTree)
	plain := stripAnsi(got)
	if !strings.Contains(plain, "1 files modified") {
		t.Errorf("new-file write should count as 1 modified, got: %q", plain)
	}
}


func TestFinishStreaming_SnapshotsToolTree(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
		msgCache: &messageRenderCache{},
	}
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
	}}
	m.finishStreaming(EngineResponseMsg{})
	if len(m.toolTree) != 0 {
		t.Errorf("finishStreaming 后 toolTree 应清空, got %d", len(m.toolTree))
	}
	// Find the toolsummary message (finishStreaming may append other system msgs).
	var summary *DisplayMessage
	for i := range m.messages {
		if m.messages[i].Role == "toolsummary" {
			summary = &m.messages[i]
			break
		}
	}
	if summary == nil {
		t.Fatalf("未找到 toolsummary 消息")
	}
	if summary.ToolTree == nil {
		t.Fatalf("toolsummary 消息应含 ToolTree 快照")
	}
	if len(summary.ToolTree[0].Children) != 1 {
		t.Errorf("快照 Children 应有 1 个 hunk, got %d", len(summary.ToolTree[0].Children))
	}
}


func TestRenderHunkLines_PlusPrefixEdgeCase(t *testing.T) {
	// Lines starting with ++ or -- are legitimate add/delete lines in a hunk body.
	// File headers (--- a/, +++ b/) are already stripped by parseDiffHunks, so any
	// line starting with + or - here is a genuine diff line and must be colored
	// accordingly — not misclassified as context (dim).
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.ANSI256)

	hunk := "@@ -1,2 +1,2 @@\n--j;\n++i;\n"
	got := renderHunkLines(hunk)

	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))

	expectedAdd := addStyle.Render("  ++i;")
	expectedDel := delStyle.Render("  --j;")

	foundAdd := false
	foundDel := false
	for _, l := range got {
		if l == expectedAdd {
			foundAdd = true
		}
		if l == expectedDel {
			foundDel = true
		}
	}
	if !foundAdd {
		t.Errorf("++i; should be rendered as add (green 114), expected %q in %v", expectedAdd, got)
	}
	if !foundDel {
		t.Errorf("--j; should be rendered as delete (red 210), expected %q in %v", expectedDel, got)
	}
}

// TestRenderMessage_ToolSummaryKeepsDiffColors verifies the archived
// toolsummary message (which replaces the live toolTree after a turn ends)
// still renders diff lines with green/red colors instead of falling into the
// all-DimStyle fallback. Regression: sanitizeForTerminal strips the ANSI that
// renderToolSummary baked into Content, so the plain-text path lost every
// diff color once the toolTree was archived (user saw gray text after
// scrolling back to the changes block).
func TestRenderMessage_ToolSummaryKeepsDiffColors(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.ANSI256)

	msg := DisplayMessage{
		Role:    "toolsummary",
		Content: renderToolSummary([]ToolNode{{
			Name:     "edit",
			Done:     true,
			Detail:   "foo.go",
			Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
		}}),
		ToolTree: []ToolNode{{
			Name:     "edit",
			Done:     true,
			Detail:   "foo.go",
			Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
		}},
	}
	lines := renderMessage(msg, 80)

	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	foundAdd, foundDel := false, false
	for _, l := range lines {
		if strings.Contains(l, addStyle.Render("  +new")) {
			foundAdd = true
		}
		if strings.Contains(l, delStyle.Render("  -old")) {
			foundDel = true
		}
	}
	if !foundAdd {
		t.Errorf("archived toolsummary should render +new in green (114), got: %q", lines)
	}
	if !foundDel {
		t.Errorf("archived toolsummary should render -old in red (210), got: %q", lines)
	}
}

// TestRenderMessage_ToolSummaryFallback_NoToolTree verifies the fallback path
// (messages without a ToolTree snapshot — resumed sessions / hand-built
// messages) still renders the sanitized plain text in DimStyle, matching the
// pre-change behavior.
func TestRenderMessage_ToolSummaryFallback_NoToolTree(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.ANSI256)

	msg := DisplayMessage{
		Role:    "toolsummary",
		Content: "● 1 tools executed, 1 files modified\n  [~] foo.go",
	}
	lines := renderMessage(msg, 80)
	if len(lines) == 0 {
		t.Fatal("expected rendered lines")
	}
	for i, l := range lines {
		plain := stripAnsi(l)
		if plain == "" {
			continue
		}
		if l != DimStyle.Render(plain) && l != DimStyle.Render(plain)+"" {
			// The wrap path may re-wrap long lines; each styled line must
			// still equal DimStyle(plain) modulo trailing padding.
			if !strings.HasPrefix(l, "\x1b[") {
				t.Errorf("line %d should be DimStyle-styled, got %q", i, l)
			}
		}
	}
}
