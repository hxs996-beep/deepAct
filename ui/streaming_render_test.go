package ui

import (
	"strings"
	"testing"
)

// TestRenderStreaming_UsesGlamour verifies that renderStreaming uses glamour
// markdown rendering instead of plain-text wrapText. Glamour renders ###
// headers and **bold** markers into styled text, removing the raw markers.
func TestRenderStreaming_UsesGlamour(t *testing.T) {
	md := "### Check: Build\n\n**Command run:**\n  go build ./...\n\n**Result: PASS**"
	lines := renderStreaming(md, 80)
	if len(lines) == 0 {
		t.Fatal("renderStreaming returned no lines for non-empty input")
	}
	joined := strings.Join(lines, "\n")
	// NOTE: The project's CustomDarkStyle/CustomLightStyle (styles.go) sets
	// Prefix: "### " for H3 headers, so glamour deliberately preserves the
	// "### " text. We cannot assert its absence. Instead, the ** bold marker
	// check below proves glamour rendering is active (wrapText would leave
	// ** as raw text).
	if strings.Contains(joined, "**Command") {
		t.Error("renderStreaming output contains raw '**' bold marker — glamour should have rendered it")
	}
}

// TestRenderStreaming_CacheHit verifies that calling renderStreaming twice
// with the same input returns the same result without re-rendering.
func TestRenderStreaming_CacheHit(t *testing.T) {
	md := "### Cache Test\n\nUnique content for cache test 12345."
	first := renderStreaming(md, 80)
	second := renderStreaming(md, 80)
	if len(first) != len(second) {
		t.Errorf("cache miss: first call returned %d lines, second returned %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("line %d differs between first and second call", i)
			break
		}
	}
}

// TestRenderStreaming_EmptyInput verifies that empty input returns an empty slice.
func TestRenderStreaming_EmptyInput(t *testing.T) {
	lines := renderStreaming("", 80)
	if len(lines) != 0 {
		t.Errorf("empty input should return empty slice, got %d lines", len(lines))
	}
}

// TestRenderStreaming_CollapsesBlankLinesInCodeBlock verifies that runs of
// consecutive blank lines are collapsed to a single blank line. Glamour renders
// code-block content literally, so blank lines inside a fenced block (e.g. the
// critic agent's ``` Check blocks, which often contain blank lines between
// **Command run:** / **Result:** fields) are preserved as-is — producing large
// visual gaps between paragraphs. The legacy wrapText path collapsed 3+
// newlines to \n\n; the glamour path must do the same to stay visually
// consistent with final display.
func TestRenderStreaming_CollapsesBlankLinesInCodeBlock(t *testing.T) {
	md := "```\n### Check: Build\n**Command run:**\n  go build\n\n\n**Result: PASS**\n```"
	lines := renderStreaming(md, 80)
	maxBlank, cur := 0, 0
	for _, l := range lines {
		if strings.TrimSpace(stripAnsi(l)) == "" {
			cur++
			if cur > maxBlank {
				maxBlank = cur
			}
		} else {
			cur = 0
		}
	}
	if maxBlank > 1 {
		t.Errorf("expected at most 1 consecutive blank line, got %d", maxBlank)
	}
}
