package ui

import "os"

// init runs before styles.go's init (because "e" < "s" in file name order).
// It sets environment variables to prevent terminal color query sequences
// (OSC 10/11/4, CSI c, etc.) from leaking into stdin. When termenv,
// lipgloss, or glamour try to detect terminal capabilities, they send
// escape sequences like \x1b]11;?\x07 and the response bytes leak into
// the Bubble Tea input stream as visible residue (e.g. "]11;rgb:...").
//
// By pre-setting these vars, we short-circuit the capability detection
// and prevent queries from being sent in the first place.
func init() {
	if os.Getenv("COLORFGBG") == "" {
		os.Setenv("COLORFGBG", "15;0")
	}
	if os.Getenv("COLORTERM") == "" {
		os.Setenv("COLORTERM", "truecolor")
	}
	// Prevent terminfo/terminfo from querying terminal capabilities
	if v := os.Getenv("TERM"); v == "" || v == "dumb" {
		// Ensure a modern terminfo entry so libs don't send DA queries
		os.Setenv("TERM", "xterm-256color")
	}
}
