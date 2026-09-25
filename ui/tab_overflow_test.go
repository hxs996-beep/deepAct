package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// terminalCellWidth returns the number of terminal columns a rendered line
// really occupies, expanding TABs to 8-column tab stops. It is the ground truth
// the renderer's line counter assumes: a line wider than the terminal soft-wraps,
// Bubble Tea counts one line where the terminal shows two, and every following
// row (input box, status line) drifts. displayWidth cannot express this because a
// TAB's advance depends on the running column, not just the rune.
func terminalCellWidth(s string) int {
	s = stripAnsi(s)
	col := 0
	for _, r := range s {
		if r == '\t' {
			col += 8 - col%8
			continue
		}
		col += runeWidth(r)
	}
	return col
}

// TestExpandTabs_TabStops: a TAB advances to the next multiple of 8 columns, so
// its width depends on the current column (NOT a fixed 8 spaces).
func TestExpandTabs_TabStops(t *testing.T) {
	sp := strings.Repeat
	cases := []struct{ name, in, want string }{
		{"col0", "\tfoo", sp(" ", 8) + "foo"},
		{"two-tabs-col0", "\t\tfoo", sp(" ", 16) + "foo"},
		{"after-one-col", "a\tb", "a" + sp(" ", 7) + "b"},
		{"after-two-cols", "ab\tc", "ab" + sp(" ", 6) + "c"},
		{"no-tabs", "no tabs here", "no tabs here"},
		{"reset-per-line", "\tline2\tx", sp(" ", 8) + "line2" + sp(" ", 3) + "x"},
	}
	for _, c := range cases {
		if got := expandTabs(c.in); got != c.want {
			t.Errorf("%s: expandTabs(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestRenderMessage_TabIndentedCode_NoOverflow reproduces the reported bug: a Go
// code block indented with literal TABs (the shape of the content that corrupted
// the layout). runeWidth('\t') == 0, so such a line was measured as width-2 while
// the terminal renders it far wider -> soft wrap -> renderer line-count drift.
func TestRenderMessage_TabIndentedCode_NoOverflow(t *testing.T) {
	content := "```go\n" +
		"\t\te.bgTasks[id] = &bgTask{id: id, agent: agent, goal: goal, state: bgStateAwaitingUser,\n" +
		"\t\t\t// BOTH fall through to default → the result is dropped and poll reports\n" +
		"\tIter int\n" +
		"```"
	width := 100

	lines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, width)
	for i, l := range lines {
		if w := terminalCellWidth(l); w > width {
			t.Errorf("line %d occupies %d terminal columns (> %d):\n%q", i, w, width, l)
		}
	}
}

// TestRenderHunkLines_TabIndentedDiff_NoOverflow asserts the diff path never
// emits an over-wide row: renderToolSummaryStyled renders the ToolTree's raw hunk
// content (a TAB-indented file's diff lines) and wrapLines then measures it. The
// measurement must count a TAB at its real terminal advance, not runeWidth's 0.
func TestRenderHunkLines_TabIndentedDiff_NoOverflow(t *testing.T) {
	hunk := "@@ -1,2 +1,2 @@\n" +
		"-\t\told := x\n" +
		"+\t\tnew := y\n"
	width := 20

	lines := wrapLines(renderHunkLines(hunk), width)
	for i, l := range lines {
		if w := terminalCellWidth(l); w > width {
			t.Errorf("diff line %d occupies %d terminal columns (> %d):\n%q", i, w, width, l)
		}
	}
}

// TestView_TabInUncoveredPaths_NoOverflow asserts the frame-level invariant: no
// row View() emits may contain a TAB. A TAB advances the terminal to the next
// tab stop while the width measure counts it as 0, so such a row overflows,
// soft-wraps, and drifts Bubble Tea's line counter (garbled rows, broken input
// box / status line).
//
// The sub-agent panel and the todo overlay reach the frame via renderSubAgentPanel
// / renderOverlayStatus (splitLipglossBlock), and the footer is assembled after
// the body — none of them pass through sanitizeForTerminal or wrapLines. Today
// lipgloss's width-constrained Render already expands TABs for those paths;
// View's assembly (Step 7 body / Step 11 footer) is the backstop that keeps the
// invariant true regardless of that behaviour.
func TestView_TabInUncoveredPaths_NoOverflow(t *testing.T) {
	m := Model{
		width:    100,
		height:   30,
		state:    stateReady,
		ready:    true,
		msgCache: &messageRenderCache{},
		inputBuf: NewInputBuffer(),
		messages: []DisplayMessage{{Role: "assistant", Content: "done"}},
		subAgents: []SubAgentStatus{
			{ID: "1", Agent: "sub", Status: "running", Goal: "\t\tsearch the cache layer"},
			{ID: "2", Agent: "sub", Status: "done", Summary: "\t\tfound it"},
		},
		todoItems: []engine.TodoItem{
			{Content: "\t\trewrite the compressor", Status: "in_progress"},
		},
	}

	rows := strings.Split(m.View(), "\n")
	seen := false
	for i, l := range rows {
		if w := terminalCellWidth(l); w > m.width {
			t.Errorf("row %d occupies %d terminal columns (> %d):\n%q", i, w, m.width, l)
		}
		if strings.Contains(stripAnsi(l), "search the cache layer") {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("test setup: sub-agent goal row not present in the frame (%d rows)", len(rows))
	}
}
