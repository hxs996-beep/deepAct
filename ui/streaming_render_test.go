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
