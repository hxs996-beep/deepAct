package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderMessage_LongCJKNoSpaces_Wraps verifies that long CJK text without
// spaces is hard-wrapped to the given width. Glamour's word wrap (reflow/wordwrap)
// treats CJK text without spaces as a single "word" and does NOT hard-break it
// when the word exceeds the line limit (wordwrap.go:143 condition
// `w.word.PrintableRuneWidth() < w.Limit`). Without post-processing, these lines
// exceed the terminal width and get truncated by Step 7's ansi.Truncate, causing
// the user to see cut-off text.
func TestRenderMessage_LongCJKNoSpaces_Wraps(t *testing.T) {
	// 120 CJK chars = 240 visual columns, no spaces anywhere
	longText := strings.Repeat("你好世界测试", 20)
	width := 40

	msg := DisplayMessage{Role: "assistant", Content: longText}
	lines := renderMessage(msg, width)

	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			t.Errorf("line %d visual width %d exceeds %d:\n%s", i, w, width, line)
		}
	}
}

// TestRenderMessage_LongCodeLine_Wraps verifies that long lines inside code
// blocks are wrapped. Glamour's codeblock.go does NOT apply wordwrap at all -
// code is rendered as-is through indent.NewWriterPipe. Long code lines exceed
// the terminal width and get truncated by Step 7.
func TestRenderMessage_LongCodeLine_Wraps(t *testing.T) {
	longCode := "```go\n" + strings.Repeat("x", 200) + "\n```"
	width := 40

	msg := DisplayMessage{Role: "assistant", Content: longCode}
	lines := renderMessage(msg, width)

	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			t.Errorf("line %d visual width %d exceeds %d:\n%s", i, w, width, line)
		}
	}
}

// TestRenderMessage_LongEnglishWord_Wraps verifies that a long English "word"
// (no spaces, like a URL or identifier) is hard-wrapped. Glamour's wordwrap
// only breaks at spaces/hyphens; a single token wider than the limit is emitted
// as-is.
func TestRenderMessage_LongEnglishWord_Wraps(t *testing.T) {
	longWord := strings.Repeat("a", 200)
	width := 40

	msg := DisplayMessage{Role: "assistant", Content: longWord}
	lines := renderMessage(msg, width)

	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			t.Errorf("line %d visual width %d exceeds %d:\n%s", i, w, width, line)
		}
	}
}
