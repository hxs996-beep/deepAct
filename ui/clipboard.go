package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

// reverseHighlightLine applies ANSI reverse video (\x1b[7m ... \x1b[27m)
// to a visual column range within an ANSI-formatted string.
// colEnd = -1 means highlight to end of line.
// Visual columns are measured by skipping zero-width ANSI escape sequences.
func reverseHighlightLine(line string, colStart, colEnd int) string {
	if line == "" || colStart < 0 {
		return line
	}

	var sb strings.Builder
	visualCol := 0
	inHighlight := false
	i := 0

	for i < len(line) {
		// Check for ANSI escape sequence
		if line[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(line, i)
			sb.WriteString(line[i:seqEnd])
			i = seqEnd
			continue
		}

		// Check if we should start highlighting
		if !inHighlight && visualCol == colStart {
			sb.WriteString("\x1b[7m")
			inHighlight = true
		}
		// Check if we should stop highlighting
		if inHighlight && colEnd >= 0 && visualCol == colEnd {
			sb.WriteString("\x1b[27m")
			inHighlight = false
		}

		// Handle multi-byte runes
		_, size := decodeRuneAt(line, i)
		sb.WriteString(line[i : i+size])
		i += size
		visualCol++
	}

	// Close highlight if it extends to end of line
	if inHighlight {
		sb.WriteString("\x1b[27m")
	}

	return sb.String()
}

// findAnsiSeqEnd returns the index after the end of the ANSI escape sequence
// starting at position i. Handles CSI (ESC [ ... final), OSC (ESC ] ... BEL/ST),
// and single-char sequences like ESC c.
func findAnsiSeqEnd(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i + 1
	}
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[':
		// CSI: ESC [ <params 0x20-0x3F> <intermediates 0x20-0x2F> <final 0x40-0x7E>
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
		// OSC: ESC ] <params> BEL or ESC ] <params> ESC \
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
		// Two-character sequence: ESC <byte>
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
		// Multi-byte UTF-8 — use utf8.DecodeRuneInString
		decoded, sz := utf8.DecodeRuneInString(s[i:])
		if sz > 1 {
			r = decoded
			size = sz
		}
	}
	return r, size
}

// copyToClipboard writes plain text to the system clipboard.
func copyToClipboard(text string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command("wl-copy")
			cmd.Stdin = strings.NewReader(text)
			return cmd.Run()
		}
		cmd := exec.Command("xclip", "-selection", "clipboard")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return fmt.Errorf("clipboard: unsupported platform %s", runtime.GOOS)
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

// extractSelectionText extracts plain text from the selected line range.
// Uses cachedBodyLines with ANSI stripping.
func (m Model) extractSelectionText() string {
	if !m.selection.Done && !m.selection.Active {
		return ""
	}
	start, end := normalizeSelection(m.selection)

	if len(m.cachedBodyLines) == 0 {
		return ""
	}

	var sb strings.Builder
	for i := start.Line; i <= end.Line; i++ {
		if i < 0 || i >= len(m.cachedBodyLines) {
			continue
		}
		line := stripAnsi(m.cachedBodyLines[i])
		line = strings.TrimRight(line, " ")

		// Apply column-level trimming for first/last line
		if i == start.Line && start.Col > 0 {
			runes := []rune(line)
			if start.Col < len(runes) {
				line = string(runes[start.Col:])
			} else {
				line = ""
			}
		}
		if i == end.Line && end.Col > 0 {
			runes := []rune(line)
			if end.Col < len(runes) {
				line = string(runes[:end.Col])
			}
		}
		sb.WriteString(line)
		if i < end.Line {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// copySelection extracts selected text and copies it to the clipboard.
func (m *Model) copySelection() {
	text := m.extractSelectionText()
	if text == "" {
		return
	}
	if err := copyToClipboard(text); err == nil {
		m.clipboardFeedback = time.Now()
	}
}
