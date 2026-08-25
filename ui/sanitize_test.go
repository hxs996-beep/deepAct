package ui

import (
	"strings"
	"testing"
)

func TestSanitizeForTerminal_ControlChars(t *testing.T) {
	// Bare CR resets the cursor — must be dropped. CRLF collapses to a newline.
	if got := sanitizeForTerminal("a\rb"); got != "ab" {
		t.Errorf("bare CR should be dropped, got %q", got)
	}
	if got := sanitizeForTerminal("a\r\nb"); got != "a\nb" {
		t.Errorf("CRLF should collapse to a newline, got %q", got)
	}
	// Newline and tab are preserved.
	if got := sanitizeForTerminal("a\nb\tc"); got != "a\nb\tc" {
		t.Errorf("newline/tab should be kept, got %q", got)
	}
	// Other control chars (BEL, NUL, backspace) dropped.
	if got := sanitizeForTerminal("a\x07b\x00c\x08d"); got != "abcd" {
		t.Errorf("control chars should be dropped, got %q", got)
	}
}

func TestSanitizeForTerminal_ANSI(t *testing.T) {
	// SGR color and cursor-movement escapes are both stripped. The dangerous
	// part is the cursor/clear sequences, but stripping all ANSI is the safe
	// boundary for arbitrary content.
	for _, in := range []string{
		"\x1b[31mred\x1b[0m",
		"\x1b[Hhome",
		"\x1b[2Jclear",
		"\x1b[?25lhide",
		"\x1b[1;3Hmove",
	} {
		if got := sanitizeForTerminal(in); strings.Contains(got, "\x1b") {
			t.Errorf("ANSI escape should be stripped from %q, got %q", in, got)
		}
	}
	// Text survives, ANSI gone.
	if got := sanitizeForTerminal("\x1b[31mOK\x1b[0m done"); got != "OK done" {
		t.Errorf("stripped text = %q, want 'OK done'", got)
	}
}

func TestSanitizeForTerminal_Plain(t *testing.T) {
	// Ordinary text passes through unchanged.
	in := "普通文本 and code\n[ok]"
	if got := sanitizeForTerminal(in); got != in {
		t.Errorf("plain text changed: got %q", got)
	}
	if got := sanitizeForTerminal(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
}

func TestSanitizeForTerminal_CRLFScript(t *testing.T) {
	// A CRLF script with ANSI colors should render as clean newline-separated
	// lines with no escape bytes — the terminal can never be moved by it.
	in := "#! /bin/sh\r\n\x1b[32mecho ok\x1b[0m\r\nexit 0\r\n"
	out := sanitizeForTerminal(in)
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\r") {
		t.Errorf("script output still carries escape/cr bytes: %q", out)
	}
	if out != "#! /bin/sh\necho ok\nexit 0\n" {
		t.Errorf("CRLF script sanitized to: %q", out)
	}
}
