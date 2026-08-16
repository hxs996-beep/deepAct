package ui

import (
	"strings"
	"testing"
)

// TestRenderStreaming_IncompleteMarkdown_NoRawMarkers reproduces the bug where
// streaming frames (markdown arriving mid-stream, e.g. an unclosed ** bold or
// ` inline-code token) leak raw markers to the display — the user copies them
// into a file and gets literal **/` instead of formatted text. The streaming
// path must consume markers unconditionally, regardless of whether the
// markdown is complete yet.
func TestRenderStreaming_IncompleteMarkdown_NoRawMarkers(t *testing.T) {
	fragments := []string{
		"`--ref-video-audio`",       // complete inline code
		" 吃两个**参数",                // UNCLOSED bold
		"**（`main.c:4",               // unclosed bold + unclosed code
		"`main.c:434-445`）：`VIDEO`", // completes + new code
	}
	acc := ""
	for i, f := range fragments {
		acc += f
		for _, w := range []int{60, 80, 100} {
			lines := renderStreaming(acc, w)
			for _, l := range lines {
				plain := stripAnsi(l)
				if strings.Contains(plain, "**") || strings.Contains(plain, "`") {
					t.Errorf("fragment %d width=%d leaked raw marker: %q", i, w, plain)
				}
			}
		}
	}
}

// TestRenderStreaming_NoAnsiOutput verifies the streaming path emits PLAIN
// text (no SGR sequences). ANSI + CJK wide chars on Bubble Tea's incremental
// line diff drift on terminals, swapping/stale-ing characters (Ref2VA ->
// 2RefVA, AUDIO -> UDAIO). Streaming must stay ANSI-free so every tick
// re-renders cleanly and the user can safely copy the text.
func TestRenderStreaming_NoAnsiOutput(t *testing.T) {
	src := "--ref-video-audio 属于 Ref2VA（引用有序）路径。\n\n## 语法\n\n`--ref-video-audio` 吃两个**参数**（`main.c:434-445`）：`VIDEO` + `AUDIO`。"
	for _, w := range []int{60, 80, 100} {
		lines := renderStreaming(src, w)
		for i, l := range lines {
			if strings.Contains(l, "\x1b[") {
				t.Errorf("width=%d line %d contains ANSI (must be plain text): %q", w, i, l)
			}
		}
	}
}
