package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

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
// This strips every ANSI escape sequence (SGR coloring and positional alike)
// and drops control characters except newline and tab, so the content can never
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
	return b.String()
}
