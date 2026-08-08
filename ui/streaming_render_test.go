package ui

import (
	"strings"
	"testing"
)

// TestRenderStreaming_StripsMarkdownMarkers verifies that renderStreaming
// strips common markdown syntax markers (**, ###, `) for readability during
// active streaming. The final display (after streaming completes) uses
// glamour via renderMarkdown for full formatting.
func TestRenderStreaming_StripsMarkdownMarkers(t *testing.T) {
	md := "### Check: Build\n\n**Command run:**\n  go build ./...\n\n**Result: PASS**"
	lines := renderStreaming(md, 80)
	if len(lines) == 0 {
		t.Fatal("renderStreaming returned no lines for non-empty input")
	}
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "**") {
		t.Error("renderStreaming should strip '**' markers for streaming display")
	}
	if strings.Contains(joined, "###") {
		t.Error("renderStreaming should strip '###' header markers for streaming display")
	}
	if !strings.Contains(joined, "Check: Build") {
		t.Error("renderStreaming should preserve header text content")
	}
	if !strings.Contains(joined, "Command run:") {
		t.Error("renderStreaming should preserve bold text content")
	}
}

// TestRenderStreaming_PreservesCodeBlockContent verifies that code inside
// fenced code blocks is preserved while the ``` fence markers are removed.
func TestRenderStreaming_PreservesCodeBlockContent(t *testing.T) {
	md := "```go\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```"
	lines := renderStreaming(md, 80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "func main()") {
		t.Error("renderStreaming should preserve code block content")
	}
	if !strings.Contains(joined, "fmt.Println") {
		t.Error("renderStreaming should preserve code block content")
	}
	if strings.Contains(joined, "```") {
		t.Error("renderStreaming should strip code fence markers")
	}
}

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

func TestRenderStreaming_EmptyInput(t *testing.T) {
	lines := renderStreaming("", 80)
	if len(lines) != 0 {
		t.Errorf("empty input should return empty slice, got %d lines", len(lines))
	}
}

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
