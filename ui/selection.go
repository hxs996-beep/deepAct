package ui

type selPoint struct {
	Line int // index in renderBody full line array
	Col  int // visual column (0-based, ANSI-aware width)
}

type SelectionState struct {
	Active bool     // currently dragging
	Done   bool     // selection completed (highlight persists after release)
	Start  selPoint // mouse-down position
	End    selPoint // current / mouse-up position
}

// lineMeta maps a rendered line back to its source DisplayMessage.
// Built during renderBody() so extractSelectionText() can retrieve
// the original Markdown content (without ANSI codes) for clipboard copy.
type lineMeta struct {
	msgIdx  int // index in m.messages (-1 for non-message lines like logo/tool tree)
	lineOff int // line offset within that message's rendered output
}

// normalizeSelection returns (start, end) with start ≤ end.
// If start > end, they are swapped.
func normalizeSelection(s SelectionState) (selPoint, selPoint) {
	if s.Start.Line < s.End.Line {
		return s.Start, s.End
	}
	if s.Start.Line > s.End.Line {
		return s.End, s.Start
	}
	// Same line: compare columns
	if s.Start.Col <= s.End.Col {
		return s.Start, s.End
	}
	return s.End, s.Start
}

// screenToLine maps screen coordinates (row, col) to a selPoint
// in the renderBody full line array.
func (m Model) screenToLine(screenRow, screenCol int) selPoint {
	bodyHeight := m.height - m.footerHeight()
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	totalLines := m.cachedTotalLines
	maxScroll := 0
	if totalLines > bodyHeight {
		maxScroll = totalLines - bodyHeight
	}
	scrollOff := m.scrollOffset
	if scrollOff > maxScroll {
		scrollOff = maxScroll
	}
	if scrollOff < 0 {
		scrollOff = 0
	}

	endLine := totalLines - scrollOff
	startLine := endLine - bodyHeight

	lineIdx := startLine + screenRow
	if lineIdx < 0 {
		lineIdx = 0
	}
	if totalLines > 0 && lineIdx >= totalLines {
		lineIdx = totalLines - 1
	}

	return selPoint{Line: lineIdx, Col: screenCol}
}

// applySelectionHighlight applies ANSI reverse video to lines within the selection range.
func (m Model) applySelectionHighlight(lines []string) []string {
	if !m.selection.Active && !m.selection.Done {
		return lines
	}
	start, end := normalizeSelection(m.selection)
	for i := start.Line; i <= end.Line; i++ {
		if i < 0 || i >= len(lines) {
			continue
		}
		colStart := 0
		if i == start.Line {
			colStart = start.Col
		}
		colEnd := -1 // -1 = to end of line
		if i == end.Line && end.Col >= 0 {
			colEnd = end.Col
		}
		lines[i] = reverseHighlightLine(lines[i], colStart, colEnd)
	}
	return lines
}
