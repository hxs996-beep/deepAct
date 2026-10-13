package ui

import (
	"strings"
	"unicode/utf8"
)

// ---- Input-box drag selection ----
//
// The body selection (selection.go) covers the scrollable transcript, which is
// mapped through the renderBody snapshot. The input box is drawn in the footer
// and is fixed on screen, so it needs its own mapping: screen (row, column) to
// a rune index in the input buffer.

// inputCaret is the cell drawn for the input cursor. It occupies one column in
// the drawn text but is not a buffer rune, so every index translation between
// screen columns and buffer positions has to account for it.
const inputCaret = "█"

const (
	// inputPrefix is drawn before the first row of input text.
	inputPrefix = "  > "
	// inputContinuationPrefix indents every wrapped row by the same width as
	// inputPrefix, so all text rows start in the same column.
	inputContinuationPrefix = "    "
	// inputTextIndent is the column input text starts at: the blue bar plus the
	// prefix.
	inputTextIndent = 1 + len(inputPrefix)
)

// InputSelectionState tracks a drag selection inside the input box. Start and
// End are rune indices into the input buffer (caret excluded), so a selection
// is expressed in the same units the clipboard extraction slices with — the same
// Start/End vocabulary SelectionState uses for the body.
type InputSelectionState struct {
	Active bool
	Done   bool
	Start  int // buffer rune index at mouse-down
	End    int // buffer rune index at the current mouse position
}

// bodyBottom returns the first screen row below the transcript: the top of the
// footer (popups, input box and status bar). Rows at or below it are not body
// content.
func (m Model) bodyBottom() int {
	return bodyBottomFor(m.height, m.footerHeight())
}

// bodyBottomFor maps a screen height and footer height to the body bottom: the
// footer is pinned to the last rows, but never leaves the body empty.
func bodyBottomFor(height, footer int) int {
	bodyHeight := height - footer
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	return bodyHeight
}

// inputBoxRange returns the screen row range [top, top+height) covered by the
// input box. Rows above it are body content and popups; rows below it are the
// status bar. ok is false when no input box is drawn (the API-key prompt takes
// over the whole screen).
func (m Model) inputBoxRange() (top, height int, ok bool) {
	if m.state == stateApiKeyPrompt {
		return 0, 0, false
	}
	height = renderedHeight(renderInputLine(m))
	if height < 1 {
		height = 1
	}
	footer := m.footerHeight()
	bodyHeight := bodyBottomFor(m.height, footer)
	// The popups (resume / options / suggestions) are drawn between the body
	// and the input box: they push the input box down without being part of it.
	popups := footer - statusBarHeight - height
	if popups < 0 {
		popups = 0
	}
	return bodyHeight + popups, height, true
}

// inputRuneAt maps an absolute screen position to a rune index in the input
// buffer. Positions outside the input box clamp to its text.
func (m Model) inputRuneAt(screenRow, screenCol int) int {
	top, _, ok := m.inputBoxRange()
	if !ok {
		return 0
	}
	return m.inputRowRuneAt(screenRow-top, screenCol)
}

// inputRowRuneAt maps a row relative to the input box top (0 = the separator row
// above the text, 1.. = the wrapped text rows) plus a screen column to a rune
// index in the input buffer. The row offsets come from wrapInputRows, the same
// ones renderInputLine draws with.
func (m Model) inputRowRuneAt(row, screenCol int) int {
	rows, starts := wrapInputRows(m.inputTextWithCaret(), m.inputInnerWidth())
	idx := row - 1
	if idx < 0 {
		return m.caretToBufferIndex(0)
	}
	if idx >= len(rows) {
		// A drag below the last text row: clamp to the end of the input.
		last := len(rows) - 1
		return m.caretToBufferIndex(starts[last] + utf8.RuneCountInString(rows[last]))
	}
	col := screenCol - inputTextIndent
	if col < 0 {
		col = 0
	}
	return m.caretToBufferIndex(starts[idx] + runeIndexAtVisualCol(rows[idx], col))
}

// inputTextWithCaret is the text drawn in the input box: the buffer text with
// the caret cell inserted at the cursor.
func (m Model) inputTextWithCaret() string {
	runes := []rune(m.inputBuf.Value())
	cursor := m.inputBuf.Cursor()
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor < 0 {
		cursor = 0
	}
	return string(runes[:cursor]) + inputCaret + string(runes[cursor:])
}

// inputInnerWidth is the wrap width of the input text: the box width less the
// drawn bar and prefix (inputTextIndent) and one column of slack. It mirrors the
// width renderInputLine wraps with.
func (m Model) inputInnerWidth() int {
	w := m.width - inputTextIndent - 1
	if w < 20 {
		w = 20
	}
	return w
}

// caretToBufferIndex converts an index in the drawn text (caret included) to an
// index in the buffer (caret excluded), clamped to the buffer length.
func (m Model) caretToBufferIndex(idx int) int {
	if idx > m.inputBuf.Cursor() {
		idx--
	}
	if idx < 0 {
		idx = 0
	}
	if n := m.inputBuf.Len(); idx > n {
		idx = n
	}
	return idx
}

// bufferToCaretIndex is the inverse of caretToBufferIndex: indices after the
// cursor shift by the caret's own cell.
func (m Model) bufferToCaretIndex(idx int) int {
	if idx > m.inputBuf.Cursor() {
		return idx + 1
	}
	return idx
}

// runeIndexAtVisualCol returns the number of runes of s drawn before visual
// column col (col is relative to the start of s). ANSI escape sequences occupy
// no column but do count as runes, matching the rune indices of the drawn row.
func runeIndexAtVisualCol(s string, col int) int {
	if col <= 0 {
		return 0
	}
	n := 0
	visual := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(s, i)
			n += utf8.RuneCountInString(s[i:seqEnd])
			i = seqEnd
			continue
		}
		r, size := decodeRuneAt(s, i)
		rw := runeWidth(r)
		if visual+rw > col {
			return n
		}
		visual += rw
		n++
		i += size
	}
	return n
}

// normalizeInputSelection returns the selected buffer rune range [from, to),
// clamped to the buffer so callers can slice with it directly. ok is false when
// no non-empty range is selected.
func (m Model) normalizeInputSelection(sel InputSelectionState) (from, to int, ok bool) {
	if !sel.Active && !sel.Done {
		return 0, 0, false
	}
	from, to = sel.Start, sel.End
	if from > to {
		from, to = to, from
	}
	n := m.inputBuf.Len()
	if from < 0 {
		from = 0
	}
	if to > n {
		to = n
	}
	if from >= to {
		return 0, 0, false
	}
	return from, to, true
}

// inputSelectionText returns the plain text of the current input selection.
// A styled paste indicator is stripped down to the text it shows, so copying
// across one never leaks escape sequences into the clipboard.
func (m Model) inputSelectionText() string {
	from, to, ok := m.normalizeInputSelection(m.inputSel)
	if !ok {
		return ""
	}
	return stripAnsi(string([]rune(m.inputBuf.Value())[from:to]))
}

// highlightInputRow wraps the selected part of one drawn text row in reverse
// video. rowStart is the rune index (caret space) the row begins at.
func (m Model) highlightInputRow(row string, rowStart int) string {
	from, to, ok := m.normalizeInputSelection(m.inputSel)
	if !ok {
		return row
	}
	lo := m.bufferToCaretIndex(from) - rowStart
	hi := m.bufferToCaretIndex(to) - rowStart
	runes := utf8.RuneCountInString(row)
	if lo < 0 {
		lo = 0
	}
	if hi > runes {
		hi = runes
	}
	if lo >= runes || hi <= lo {
		return row
	}
	return highlightRuneSpan(row, lo, hi)
}

// highlightRuneSpan wraps the rune span [from, to) of s in reverse video.
// Indices count every rune of s, ANSI sequences included, so they line up with
// the rune offsets of the drawn row. The span is closed with \x1b[27m (reverse
// off) rather than a full SGR reset, so the surrounding input-block styling
// survives.
func highlightRuneSpan(s string, from, to int) string {
	if from >= to {
		return s
	}
	var sb strings.Builder
	open := false
	idx := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(s, i)
			sb.WriteString(s[i:seqEnd])
			i = seqEnd
			continue
		}
		if !open && idx == from {
			sb.WriteString("\x1b[7m")
			open = true
		}
		if open && idx == to {
			sb.WriteString("\x1b[27m")
			open = false
		}
		_, size := decodeRuneAt(s, i)
		sb.WriteString(s[i : i+size])
		i += size
		idx++
	}
	if open {
		sb.WriteString("\x1b[27m")
	}
	return sb.String()
}
