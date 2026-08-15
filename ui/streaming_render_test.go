package ui

import (
	"strings"
	"testing"
)

// TestRenderStreaming_FormatsMarkdown verifies that renderStreaming runs
// streamed text through the same glamour markdown rendering as finalized
// messages (defensive: no "unformatted output" path remains). Markdown syntax
// markers are consumed by glamour; visible text content is preserved.
func TestRenderStreaming_FormatsMarkdown(t *testing.T) {
	md := "### Check: Build\n\n**Command run:**\n  go build ./...\n\n**Result: PASS**"
	lines := renderStreaming(md, 80)
	if len(lines) == 0 {
		t.Fatal("renderStreaming returned no lines for non-empty input")
	}
	plain := stripAnsi(strings.Join(lines, "\n"))
	if strings.Contains(plain, "**") {
		t.Error("renderStreaming should consume '**' bold markers via glamour")
	}
	if !strings.Contains(plain, "Check: Build") {
		t.Error("renderStreaming should preserve header text content")
	}
	if !strings.Contains(plain, "Command run:") {
		t.Error("renderStreaming should preserve bold text content")
	}
	if !strings.Contains(plain, "Result: PASS") {
		t.Error("renderStreaming should preserve result content")
	}
}

// TestRenderStreaming_PreservesCodeBlockContent verifies that code inside
// fenced code blocks is preserved (with glamour syntax highlighting) while the
// ``` fence markers are consumed.
func TestRenderStreaming_PreservesCodeBlockContent(t *testing.T) {
	md := "```go\nfunc main() {\n    fmt.Println(\"hello\")\n}\n```"
	lines := renderStreaming(md, 80)
	plain := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "func main()") {
		t.Error("renderStreaming should preserve code block content")
	}
	if !strings.Contains(plain, "fmt.Println") {
		t.Error("renderStreaming should preserve code block content")
	}
	if strings.Contains(plain, "```") {
		t.Error("renderStreaming should consume code fence markers")
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

// TestRenderStreaming_CodeBlockBlankLines verifies that glamour-rendered code
// blocks do not produce excessive blank-line gaps in the streaming display.
func TestRenderStreaming_CodeBlockBlankLines(t *testing.T) {
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
	if maxBlank > 2 {
		t.Errorf("expected at most 2 consecutive blank lines, got %d", maxBlank)
	}
}
