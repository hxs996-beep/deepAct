package ui

import (
	"strings"
	"testing"
)

// TestRenderStreaming_UsesPlainText verifies that renderStreaming uses fast
// plain-text rendering instead of glamour during active streaming. Plain-text
// rendering preserves raw ** markers (glamour would render them as styled text).
// This is intentional: glamour's expensive markdown parse on every token caused
// the progress channel to fill up and drop content_delta tokens, producing
// garbled streaming text. The final display (after streaming completes) uses
// glamour via renderMarkdown.
func TestRenderStreaming_UsesPlainText(t *testing.T) {
	md := "### Check: Build\n\n**Command run:**\n  go build ./...\n\n**Result: PASS**"
	lines := renderStreaming(md, 80)
	if len(lines) == 0 {
		t.Fatal("renderStreaming returned no lines for non-empty input")
	}
	joined := strings.Join(lines, "\n")
	// Plain-text rendering preserves raw ** markers (glamour would strip them).
	// This proves we're using the fast wrapText path, not glamour.
	if !strings.Contains(joined, "**Command") {
		t.Error("renderStreaming should preserve raw '**' markers in plain-text mode")
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
// consecutive blank lines are collapsed to a single blank line. The plain-text
// streaming path collapses 3+ newlines (\n\n\n) to \n\n, matching the behavior
// of the final glamour-rendered display.
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
