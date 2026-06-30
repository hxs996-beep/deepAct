package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ---- Types ----

// selPoint represents a position in the plain-text line array.
type selPoint struct {
	Line int // plain-text line index (0-based)
	Col  int // visual column (0-based, CJK/emoji counted as width-2)
}

// SelectionState tracks the drag selection lifecycle.
type SelectionState struct {
	Active bool     // currently dragging
	Done   bool     // drag complete, highlight persists
	Start  selPoint // mouse-down position
	End    selPoint // current / mouse-up position

	// Snapshot of the body content captured at mouse-down. Selection indices
	// (Start/End.Line) reference these arrays, so highlight and clipboard
	// extraction stay anchored to what the user actually clicked — even while
	// streaming output appends lines and shifts the live view underneath.
	// Rendered is the styled line array (pre-highlight); Plain is the
	// ANSI-stripped counterpart used for clipboard extraction.
	Rendered   []string
	Plain      []string
	BodyHeight int
	Scroll     int // snapshot-local scroll offset (adjusted by auto-scroll)
}

// autoScrollTickMsg is sent by the auto-scroll timer during edge-drag.
type autoScrollTickMsg struct{}

// ---- Coordinate mapping ----

// normalizeSelection returns (start, end) with start ≤ end.
func normalizeSelection(s SelectionState) (selPoint, selPoint) {
	if s.Start.Line < s.End.Line {
		return s.Start, s.End
	}
	if s.Start.Line > s.End.Line {
		return s.End, s.Start
	}
	if s.Start.Col <= s.End.Col {
		return s.Start, s.End
	}
	return s.End, s.Start
}

// screenToLine maps screen coordinates to a selPoint in the content line array.
// totalLines, bodyHeight, and scrollOffset are computed in Update() — not from View() cache.
func screenToLine(screenRow, screenCol, scrollOffset, bodyHeight, totalLines int) selPoint {
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	var firstVisibleLine int
	if totalLines <= bodyHeight {
		firstVisibleLine = 0
	} else {
		maxScroll := totalLines - bodyHeight
		scrollOff := scrollOffset
		if scrollOff > maxScroll {
			scrollOff = maxScroll
		}
		if scrollOff < 0 {
			scrollOff = 0
		}
		firstVisibleLine = totalLines - scrollOff - bodyHeight
	}
	lineIdx := firstVisibleLine + screenRow
	if lineIdx < 0 {
		lineIdx = 0
	}
	if totalLines > 0 && lineIdx >= totalLines {
		lineIdx = totalLines - 1
	}
	return selPoint{Line: lineIdx, Col: screenCol}
}

// ---- Highlight rendering ----

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

// reverseHighlightLine applies ANSI reverse video (\x1b[7m ... \x1b[27m)
// to a visual column range within an ANSI-formatted string.
// colEnd = -1 means highlight to end of line.
//
// When the highlight region contains SGR reset sequences (\x1b[0m or \x1b[m),
// the reverse video attribute is also reset. This function re-emits \x1b[7m
// after each such reset within the highlighted region, so the visual highlight
// is not broken by glamour's style changes.
func reverseHighlightLine(line string, colStart, colEnd int) string {
	if line == "" || colStart < 0 {
		return line
	}
	var sb strings.Builder
	visualCol := 0
	inHighlight := false
	i := 0
	for i < len(line) {
		if line[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(line, i)
			seq := line[i:seqEnd]
			sb.WriteString(seq)
			// If we're inside the highlight region and this sequence is an SGR reset,
			// re-emit \x1b[7m to keep the reverse video active.
			if inHighlight && isSgrReset(seq) {
				sb.WriteString("\x1b[7m")
			}
			i = seqEnd
			continue
		}
		r, size := decodeRuneAt(line, i)
		rw := lipgloss.Width(string(r))
		if !inHighlight && visualCol <= colStart && colStart < visualCol+rw {
			sb.WriteString("\x1b[7m")
			inHighlight = true
		}
		if inHighlight && colEnd >= 0 && visualCol < colEnd && visualCol+rw > colEnd {
			sb.WriteString("\x1b[27m")
			inHighlight = false
		}
		if inHighlight && colEnd >= 0 && visualCol >= colEnd {
			sb.WriteString("\x1b[27m")
			inHighlight = false
		}
		sb.WriteString(line[i : i+size])
		i += size
		visualCol += rw
	}
	if inHighlight {
		sb.WriteString("\x1b[27m")
	}
	return sb.String()
}

// isSgrReset checks if an ANSI sequence is an SGR reset that would clear
// the reverse video attribute. Matches \x1b[0m, \x1b[m, and \x1b[0;...m
// (any SGR that starts with 0, which resets all attributes).
func isSgrReset(seq string) bool {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != '[' {
		return false
	}
	// Find the final byte (should be a letter 0x40-0x7E)
	for j := len(seq) - 1; j >= 2; j-- {
		if seq[j] >= 0x40 && seq[j] <= 0x7E {
			if seq[j] != 'm' {
				return false // Not an SGR sequence
			}
			// Check if the first parameter is 0 (reset)
			params := seq[2:j]
			if len(params) == 0 {
				return true // \x1b[m is equivalent to \x1b[0m
			}
			// Check if starts with "0" followed by ';' or end
			if params[0] == '0' && (len(params) == 1 || params[1] == ';') {
				return true
			}
			return false
		}
	}
	return false
}

// ---- ANSI helpers ----

// findAnsiSeqEnd returns the index after the end of the ANSI escape sequence
// starting at position i.
func findAnsiSeqEnd(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i + 1
	}
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2F {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
			j++
		}
		return j
	case ']':
		j := i + 2
		for j < len(s) && s[j] != '\x07' && s[j] != '\x1b' {
			j++
		}
		if j < len(s) && s[j] == '\x07' {
			j++
		} else if j+1 < len(s) && s[j] == '\x1b' && s[j+1] == '\\' {
			j += 2
		}
		return j
	default:
		return i + 2
	}
}

// decodeRuneAt decodes the first UTF-8 rune starting at byte offset i.
func decodeRuneAt(s string, i int) (rune, int) {
	if i >= len(s) {
		return 0, 0
	}
	r := rune(s[i])
	size := 1
	if s[i]&0x80 != 0 {
		decoded, sz := utf8.DecodeRuneInString(s[i:])
		if sz > 1 {
			r = decoded
			size = sz
		}
	}
	return r, size
}

// stripAnsi removes all ANSI escape sequences from a string.
func stripAnsi(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			i = findAnsiSeqEnd(s, i)
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// ---- Text extraction ----

// extractSelectionText extracts plain text from the selected line range.
// plainLines is the ANSI-stripped line array (1:1 with rendered lines).
func extractSelectionText(plainLines []string, sel SelectionState) string {
	if !sel.Done && !sel.Active {
		return ""
	}
	start, end := normalizeSelection(sel)
	if len(plainLines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := start.Line; i <= end.Line; i++ {
		if i < 0 || i >= len(plainLines) {
			continue
		}
		line := plainLines[i]
		colStart := 0
		colEnd := -1
		if i == start.Line {
			colStart = start.Col
		}
		if i == end.Line && end.Col >= 0 {
			colEnd = end.Col
		}
		line = sliceByVisualCol(line, colStart, colEnd)
		if i > start.Line {
			sb.WriteByte('\n')
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// sliceByVisualCol returns the substring of s spanning the visual-column range
// [colStart, colEnd). colEnd = -1 means "to end of line".
func sliceByVisualCol(s string, colStart, colEnd int) string {
	if s == "" || colStart < 0 {
		return ""
	}
	if colEnd >= 0 && colEnd <= colStart {
		return ""
	}
	var sb strings.Builder
	visualCol := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if colEnd >= 0 && visualCol >= colEnd {
			break
		}
		if visualCol+rw <= colStart {
			visualCol += rw
			continue
		}
		if visualCol < colStart {
			visualCol += rw
			continue
		}
		if colEnd >= 0 && visualCol+rw > colEnd {
			break
		}
		sb.WriteRune(r)
		visualCol += rw
	}
	return sb.String()
}

// truncateVisual truncates s to fit within maxW display columns, preserving
// ANSI escape sequences (never splits a sequence mid-way) and wide runes
// (CJK/emoji counted as width-2 via lipgloss.Width). Trailing ANSI sequences
// that appear after the visual truncation point are preserved so that closing
// sequences like \x1b[0m are not lost. It does NOT append a trailing marker —
// callers decide whether to add "…" etc. Returns s unchanged if its visual
// width <= maxW. maxW <= 0 yields "".
func truncateVisual(s string, maxW int) string {
	if maxW <= 0 || s == "" {
		return ""
	}
	var sb strings.Builder
	visualCol := 0
	i := 0
	truncated := false
	for i < len(s) {
		if s[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(s, i)
			sb.WriteString(s[i:seqEnd])
			i = seqEnd
			continue
		}
		r, size := decodeRuneAt(s, i)
		rw := lipgloss.Width(string(r))
		if truncated || visualCol+rw > maxW {
			truncated = true
			i += size
			continue
		}
		sb.WriteString(s[i : i+size])
		i += size
		visualCol += rw
	}
	return sb.String()
}

// ---- Clipboard ----

// copySelection extracts selected text and copies it to the clipboard.
// Returns ("", nil) on empty text, (text, nil) on success, ("", err) on failure.
// When the selection carries a body snapshot (sel.Plain), extraction uses the
// snapshot so the copied text matches what was highlighted at mouse-down
// instead of the live (possibly shifted) view.
func copySelection(plainLines []string, sel SelectionState) (string, error) {
	plain := plainLines
	if sel.Plain != nil {
		plain = sel.Plain
	}
	text := extractSelectionText(plain, sel)
	if text == "" {
		return "", nil
	}
	if err := copyToClipboard(text); err != nil {
		return "", err
	}
	return text, nil
}

// copyToClipboard writes plain text to the system clipboard.
func copyToClipboard(text string) error {
	switch runtime.GOOS {
	case "darwin":
		return pipeToCmd("pbcopy", nil, text)
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return pipeToCmd("wl-copy", nil, text)
		}
		return pipeToCmd("xclip", []string{"-selection", "clipboard"}, text)
	case "windows":
		return windowsCopy(text)
	}
	return fmt.Errorf("clipboard: unsupported platform %s", runtime.GOOS)
}

// pipeToCmd pipes text to an external command's stdin.
func pipeToCmd(name string, args []string, text string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// windowsCopy copies text to the Windows clipboard using Win32 API.
// Platform-specific implementation lives in clipboard_windows.go / clipboard_other.go.
func windowsCopy(text string) error {
	utf16Data, err := utf16EncodeString(text)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	return windowsCopyImpl(utf16Data)
}
