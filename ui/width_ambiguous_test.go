package ui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestRenderMessage_AmbiguousWidthChar_NoOverflow reproduces the rendering
// corruption reported by users: code-block lines containing ambiguous-width
// characters (← U+2190, → U+2192, — U+2014, · U+00B7) overflow the terminal.
//
// Root cause: ansi.StringWidth / lipgloss.Width count these runes as width 1,
// but iTerm2 renders them at width 2 (matching runewidth). Every pad/truncate
// decision that underestimates the width produces a line that is physically
// wider than the terminal, so the terminal wraps it. Bubble Tea then counts
// one logical line where the terminal shows two, drifting the cursor and
// causing repeated/garbled lines until a resize forces a full repaint.
func TestRenderMessage_AmbiguousWidthChar_NoOverflow(t *testing.T) {
	content := "```go\nfor _, t := range r.tools {   // ← Go map 遍历顺序随机\n" +
		"    return specs   // → 返回\n" +
		"    x := a — b   // 破折号\n" +
		"    y := a · b   // 间隔号\n```"
	width := 40

	msg := DisplayMessage{Role: "assistant", Content: content}
	lines := renderMessage(msg, width)

	for i, line := range lines {
		// Use runewidth (matches iTerm2 rendering) instead of lipgloss.Width,
		// which itself underestimates ambiguous-width runes and would mask the bug.
		w := runewidth.StringWidth(stripAnsi(line))
		if w > width {
			t.Errorf("line %d real width %d exceeds %d:\n%s", i, w, width, line)
		}
	}
}

// TestWrapLine_AmbiguousWidthChar_Wraps verifies the word-wrap break points
// also respect ambiguous-width runes: a long line containing ← must not emit a
// segment whose real (runewidth) width exceeds the limit.
func TestWrapLine_AmbiguousWidthChar_Wraps(t *testing.T) {
	line := "    for _, t := range r.tools {   // ← Go map 遍历顺序随机，每次 range 都可能不同"
	width := 40

	wrapped := wrapLine(line, width)
	if len(wrapped) < 2 {
		t.Fatalf("expected wrap into multiple lines, got %d: %v", len(wrapped), wrapped)
	}
	for i, seg := range wrapped {
		w := runewidth.StringWidth(stripAnsi(seg))
		if w > width {
			t.Errorf("segment %d real width %d exceeds %d:\n%s", i, w, width, seg)
		}
	}
}

// TestPadToWidth_AmbiguousWidthChar_NoOverflow verifies that padding a line to
// a target width using the conservative width measure never produces a line
// wider than the target (i.e. the pad count must not be inflated by
// underestimating ambiguous runes).
func TestPadToWidth_AmbiguousWidthChar_NoOverflow(t *testing.T) {
	line := "    for _, t := range r.tools {   // ← Go map 遍历顺序随机，每次 range 都可能不同"
	target := 80

	// Emulate View() Step 7: truncate then pad using the same conservative
	// measure that the production fix will use.
	truncated := truncateToWidth(line, target)
	w := displayWidth(truncated)
	if w < target {
		truncated += strings.Repeat(" ", target-w)
	}

	if got := runewidth.StringWidth(stripAnsi(truncated)); got > target {
		t.Errorf("padded line real width %d exceeds target %d", got, target)
	}
}

// TestTruncateToWidth_PreservesANSI verifies that truncation never splits an
// ANSI escape sequence. View() Step 7/11 truncate styled lines; splitting a
// sequence (e.g. keeping "\x1b[" but dropping the "31m" terminator) corrupts
// terminal parsing and can leak colors across rows.
func TestTruncateToWidth_PreservesANSI(t *testing.T) {
	styled := "\x1b[31m" + "    for _, t := range r.tools {   // ← 红色注释\n"
	truncated := truncateToWidth(styled, 40)

	// The opening SGR must survive intact.
	if !strings.Contains(truncated, "\x1b[31m") {
		t.Fatalf("opening SGR lost in truncation: %q", truncated)
	}
	// No stray ESC byte may remain after removing the complete opening
	// sequence — a split sequence would leave a partial ESC prefix behind.
	rest := strings.Replace(truncated, "\x1b[31m", "", 1)
	if strings.Contains(rest, "\x1b") {
		t.Errorf("truncation split an ANSI sequence: %q", truncated)
	}
	// Width must respect the limit (ANSI is zero-width).
	if got := runewidth.StringWidth(stripAnsi(truncated)); got > 40 {
		t.Errorf("truncated real width %d exceeds 40", got)
	}
}

// TestDisplayWidth_BoxDrawing_IsOneColumn guards the terminal rendering
// contract for Box Drawing / Block Elements (U+2500-U+259F): iTerm2 renders
// these at exactly 1 column regardless of East Asian Width classification,
// because they are pixel-art / border glyphs. The ambiguous-width fix pinned
// EastAsianWidth=true globally, which made runewidth count █ ╔ ═ ║ as 2
// columns — over-measuring logo lines, borders, and the input blue bar, then
// truncating them (logo pixel text misplaced / vanishing). These runes must be
// measured as 1 column while ambiguous runes (← → — ·) stay at 2.
func TestDisplayWidth_BoxDrawing_IsOneColumn(t *testing.T) {
	oneCol := []rune{'█', '╔', '═', '╗', '║', '╚', '╝', '│', '▍', '░'}
	for _, r := range oneCol {
		if w := runeWidth(r); w != 1 {
			t.Errorf("rune %q (U+%04X) width = %d, want 1 (Box Drawing/Block Element)", string(r), r, w)
		}
	}
	// Ambiguous runes keep the 2-column treatment that fixed the original bug.
	twoCol := []rune{'←', '→', '—', '·', '↑', '↓'}
	for _, r := range twoCol {
		if w := runeWidth(r); w != 2 {
			t.Errorf("ambiguous rune %q (U+%04X) width = %d, want 2", string(r), r, w)
		}
	}
	// The actual logo line must measure ≈ its rune count (59), not 2×.
	logoLine := "  ██████╗ ███████╗███████╗██████╗  █████╗  ██████╗████████╗"
	got := displayWidth(logoLine)
	if got != len([]rune(logoLine)) {
		t.Errorf("logo line displayWidth = %d, want %d (one column per box-drawing rune)", got, len([]rune(logoLine)))
	}
}
