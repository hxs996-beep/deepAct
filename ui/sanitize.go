package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// tabStopWidth is the terminal tab stop: a TAB advances the cursor to the next
// multiple of this many columns (the traditional terminal default).
const tabStopWidth = 8

// expandTabs replaces every TAB with the spaces a terminal would advance, so the
// measured width (displayWidth) matches what the terminal actually renders.
//
// A TAB is not a fixed-width glyph: the terminal jumps to the next tab stop, so
// its advance depends on the current column, and runeWidth('\t') is 0. Left in
// place, a TAB-indented line (a Go code block, a diff hunk of a TAB-indented
// file, script output) is measured far narrower than it renders; the renderer
// then pads/truncates it to a width it visibly exceeds, the terminal soft-wraps
// it, Bubble Tea counts one line where the terminal shows two, and the cursor
// drifts — garbling every following row, including the input box and the status
// line (see displayWidth's doc).
//
// The column is tracked with runeVisualWidth so the expansion honours both
// ambiguous-width runes and box-drawing glyphs. ANSI escape sequences are
// skipped (zero-width), so this is safe on already-styled lines too.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + tabStopWidth)
	col := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			end := findAnsiSeqEnd(s, i)
			b.WriteString(s[i:end])
			i = end
			continue
		}
		r, size := decodeRuneAt(s, i)
		switch r {
		case '\n':
			b.WriteString(s[i : i+size])
			col = 0
		case '\t':
			n := tabStopWidth - col%tabStopWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		default:
			b.WriteString(s[i : i+size])
			col += runeVisualWidth(r, size)
		}
		i += size
	}
	return b.String()
}

// sanitizeForTerminal renders content safe to write to the terminal directly.
//
// Streaming/message content can carry bytes that, if emitted verbatim, move the
// terminal's cursor or clear the screen — because the terminal treats them as
// control codes, not display characters. A bare '\r' (carriage return — common
// in CRLF script/command output and progress bars) resets the cursor to column
// 0, and cursor-positioning ANSI sequences (e.g. ESC[H, ESC[2J, ESC[?25l) move
// or hide it. The app then draws the next frame at a cursor the terminal no
// longer agrees on, producing duplicate/jumbled rows and a drifting input box.
//
// This strips every ANSI escape sequence (SGR coloring and positional alike),
// drops control characters (newline is kept), and expands TAB to spaces at
// 8-column tab stops (see expandTabs) — a TAB is likewise invisible to the width
// measure and would otherwise overflow the terminal. So the content can never
// corrupt the terminal while the app still emits its own styling separately.
// Call it on CONTENT before it reaches the render/wrap path, never on the final
// styled View().
func sanitizeForTerminal(s string) string {
	if s == "" {
		return ""
	}
	// Remove all ANSI escape sequences (CSI/OSC/SGR). Charmbracelet's
	// x/ansi.Strip is a well-tested scanner for exactly these.
	s = ansi.Strip(s)
	// Normalize CRLF to a single newline; a bare CR is dropped (it would reset
	// the cursor to column 0).
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\t':
			b.WriteRune(r)
		default:
			// Drop other control characters (NUL, BEL, backspace, VT, FF, DEL...).
			if r < 0x20 || r == 0x7f {
				continue
			}
			b.WriteRune(r)
		}
	}
	return expandTabs(b.String())
}
