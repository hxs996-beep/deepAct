package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// hunkHit records a hunk summary line's position so a mouse click on that line
// can be mapped back to the hunk.
type hunkHit struct {
	msgIdx   int // -1 = streaming (m.toolTree), >=0 = m.messages[msgIdx].ToolTree
	nodeIdx  int // index into the toolTree (the edit/write node)
	childIdx int // index into node.Children (the hunk)
}

// countHunkAddsDeletes counts added (+) and deleted (-) lines in a hunk body.
// Lines starting with "+++" / "---" (file headers) are not counted.
func countHunkAddsDeletes(hunk string) (adds, deletes int) {
	for _, line := range strings.Split(hunk, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				adds++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				deletes++
			}
		}
	}
	return adds, deletes
}

// hunkSummaryLine renders one collapsed hunk summary line:
//
//	[N] @@ -1,3 +1,3 @@    +2  -1    点击展开
func hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string {
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true)
	label := numStyle.Render(fmt.Sprintf("  [%d] ", idx+1))
	changes := addStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delStyle.Render(fmt.Sprintf("-%d", deletes))
	return label + hunkHeader + "    " + changes + "    " + hintStyle.Render("点击展开")
}

// renderDiffViewer renders a single hunk full-screen (occupying the whole body)
// when diffViewerActive. Returns the hunk's rendered lines (pre-scroll-slice).
func (m Model) renderDiffViewer(width int) []string {
	if !m.diffViewerActive {
		return nil
	}
	h := m.diffViewerHunk
	var tree []ToolNode
	if h.msgIdx < 0 {
		tree = m.toolTree
	} else if h.msgIdx < len(m.messages) {
		tree = m.messages[h.msgIdx].ToolTree
	}
	if len(tree) == 0 || h.nodeIdx < 0 || h.nodeIdx >= len(tree) {
		return nil
	}
	node := tree[h.nodeIdx]
	if h.childIdx < 0 || h.childIdx >= len(node.Children) {
		return nil
	}
	child := node.Children[h.childIdx]
	if child.DetailFull == "" {
		return nil
	}
	var lines []string
	escHint := lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true).Render("按 ESC 退出")
	lines = append(lines, "  "+escHint)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  %s  %s", node.Detail, child.Detail))
	lines = append(lines, "")
	lines = append(lines, renderDiffHunkBlock(child.DetailFull, width-4)...)
	return lines
}

// hunkSummaryRe matches a collapsed hunk summary line's leading "  [N] ".
var hunkSummaryRe = regexp.MustCompile(`^\s*\[(\d+)\]`)

// hitTestHunk checks whether the body line at bodyLineIdx is a hunk summary
// line and, if so, returns the corresponding hunkHit (without msgIdx — caller
// fills it). tree is the data source (m.toolTree for streaming, or a message's
// ToolTree snapshot for completed toolsummary).
func hitTestHunk(bodyLineIdx int, bodyPlain []string, tree []ToolNode) (hunkHit, bool) {
	if bodyLineIdx < 0 || bodyLineIdx >= len(bodyPlain) {
		return hunkHit{}, false
	}
	match := hunkSummaryRe.FindStringSubmatch(bodyPlain[bodyLineIdx])
	if match == nil {
		return hunkHit{}, false
	}
	var n int
	fmt.Sscanf(match[1], "%d", &n)
	// Walk tree in render order, counting only done edit/write hunks with
	// content — same filter renderDiffBlock/renderToolSummary uses.
	idx := 0
	for nodeIdx, node := range tree {
		if !node.Done || len(node.Children) == 0 {
			continue
		}
		if node.Name != "edit" && node.Name != "write" {
			continue
		}
		for childIdx, child := range node.Children {
			if child.DetailFull == "" {
				continue
			}
			idx++
			if idx == n {
				return hunkHit{nodeIdx: nodeIdx, childIdx: childIdx}, true
			}
		}
	}
	return hunkHit{}, false
}

// hunkLineEntry maps a body line index to the hunk it represents.
type hunkLineEntry struct {
	msgIdx  int  // -1 = streaming (m.toolTree), >=0 = m.messages[msgIdx]
	lineIdx int  // body-absolute line index
	hit     hunkHit
}

// buildHunkLineMap scans the actual body plain lines (the mouse-down snapshot,
// which already includes spinners/overlay/streaming/toolTree — everything
// renderBody emits) and maps each clickable hunk-summary line to its source.
//
// It walks bodyPlain in order, tracking which data source "owns" the current
// region: toolsummary messages are detected by their "● ... tools executed"
// header (renderToolSummary's first line), and the live toolTree block by its
// "▍ [~] Changes" header (renderDiffBlock's first line). Within a region, [N]
// lines map to that region's ToolTree via hitTestHunk.
func (m Model) buildHunkLineMap(bodyPlain []string) []hunkLineEntry {
	var entries []hunkLineEntry
	// Pending toolsummary messages (with a ToolTree snapshot) in body order,
	// consumed as their headers are encountered.
	var pendingMsgs []int
	for i, msg := range m.messages {
		if msg.Role == "toolsummary" && len(msg.ToolTree) > 0 {
			pendingMsgs = append(pendingMsgs, i)
		}
	}
	msgPtr := -1 // -1 = no toolsummary region active yet; header advances to 0
	liveActive := false

	for lineIdx, line := range bodyPlain {
		// Message header: "● N tools executed, ..." → start of a toolsummary region.
		if strings.HasPrefix(line, "●") && strings.Contains(line, "tools executed") {
			liveActive = false
			msgPtr++ // next pending toolsummary message
			continue
		}
		// Live toolTree block header: "▍ [~] Changes".
		if strings.Contains(line, "[~]") && strings.Contains(line, "Changes") {
			liveActive = true
			msgPtr = len(pendingMsgs) // no more messages after live block (live is last)
			continue
		}
		if !hunkSummaryRe.MatchString(line) {
			continue
		}
		// Determine the source tree for this [N] line.
		var msgIdx int
		var tree []ToolNode
		if liveActive {
			msgIdx = -1
			tree = m.toolTree
		} else if msgPtr >= 0 && msgPtr < len(pendingMsgs) {
			msgIdx = pendingMsgs[msgPtr]
			tree = m.messages[msgIdx].ToolTree
		} else {
			continue
		}
		if hit, ok := hitTestHunk(0, []string{line}, tree); ok {
			entries = append(entries, hunkLineEntry{
				msgIdx:  msgIdx,
				lineIdx: lineIdx,
				hit:     hit,
			})
		}
	}

	return entries
}
