# Unify Streaming Rendering to Glamour Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace plain-text `wrapText` rendering in `renderStreaming` with glamour markdown rendering, eliminating the blank-line gap between streaming and final display.

**Architecture:** Add a package-level cache `(content, width) → lines` guarded by a mutex. `renderStreaming` checks the cache first, then calls `renderMarkdown` (glamour), falling back to the existing `wrapText` path if glamour is unavailable.

**Tech Stack:** Go, lipgloss, glamour, Bubble Tea

## Global Constraints

- Single file change: `ui/model.go` (function `renderStreaming` at line 2032)
- `sync` is already imported in `ui/model.go`
- `renderMarkdown` (line 1682) and `wrapText` (line 2703) already exist — reuse as-is
- No changes to `Model` struct, `Update`, `renderBody`, or any engine code

### Task 1: Unify renderStreaming to use glamour with cache

**Files:**
- Create: `ui/streaming_render_test.go`
- Modify: `ui/model.go:2032-2044` (replace `renderStreaming` function, add cache variable)
- Test: `ui/streaming_render_test.go`

**Interfaces:**
- Consumes: `renderMarkdown(string, int) string` (model.go:1682), `wrapText(string, int) []string` (model.go:2703), `AssistantMsgStyle` (styles.go:73), `sync.Mutex`
- Produces: `renderStreaming(string, int) []string` — same signature, same caller (`renderBody` at model.go:1490), new rendering behavior

- [ ] **Step 1: Write the failing test**

Create `ui/streaming_render_test.go`:

```go
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
	if strings.Contains(joined, "### ") {
		t.Error("renderStreaming output contains raw '###' header marker — glamour should have rendered it")
	}
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestRenderStreaming -v`
Expected: FAIL — `TestRenderStreaming_UsesGlamour` fails because the current `renderStreaming` uses `wrapText` which leaves raw `###` and `**` markers in the output. `TestRenderStreaming_CacheHit` and `TestRenderStreaming_EmptyInput` may pass (cache doesn't exist yet but behavior happens to match).

- [ ] **Step 3: Write minimal implementation**

In `ui/model.go`, replace the existing `renderStreaming` function (lines 2032-2044) with the cache variable and new function body. The old code to replace:

```go
func renderStreaming(streaming string, width int) []string {
	if streaming == "" {
		return []string{}
	}
	// Collapse 3+ consecutive newlines to \n\n: raw LLM markdown often
	// contains excessive blank lines that glamour (used for final display)
	// normalizes, but wrapText renders literally — producing visual gaps.
	normalized := streaming
	for strings.Contains(normalized, "\n\n\n") {
		normalized = strings.ReplaceAll(normalized, "\n\n\n", "\n\n")
	}
	return wrapText(AssistantMsgStyle.Render(normalized), width)
}
```

Replace with:

```go
var (
	streamRenderCacheMu sync.Mutex
	streamRenderCache   struct {
		content string
		width   int
		lines   []string
	}
)

func renderStreaming(streaming string, width int) []string {
	if streaming == "" {
		return []string{}
	}
	streamRenderCacheMu.Lock()
	defer streamRenderCacheMu.Unlock()

	// Cache hit — content and width unchanged since last render.
	if streamRenderCache.content == streaming && streamRenderCache.width == width {
		return streamRenderCache.lines
	}

	// Primary path: glamour markdown rendering (same as final display).
	rendered := renderMarkdown(streaming, width)
	if rendered != streaming {
		// Glamour succeeded — split into lines.
		lines := strings.Split(rendered, "\n")
		streamRenderCache.content = streaming
		streamRenderCache.width = width
		streamRenderCache.lines = lines
		return lines
	}

	// Fallback: glamour unavailable or failed — use legacy plain-text rendering.
	normalized := streaming
	for strings.Contains(normalized, "\n\n\n") {
		normalized = strings.ReplaceAll(normalized, "\n\n\n", "\n\n")
	}
	lines := wrapText(AssistantMsgStyle.Render(normalized), width)
	streamRenderCache.content = streaming
	streamRenderCache.width = width
	streamRenderCache.lines = lines
	return lines
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestRenderStreaming -v`
Expected: PASS — all three tests pass. Glamour renders `###` and `**` markers into styled text; cache returns identical results on second call; empty input returns empty slice.

- [ ] **Step 5: Run all UI tests to verify no regression**

Run: `cd /Users/admin/gitspace/deepact && go test ./ui/ -v`
Expected: PASS — all existing tests pass. No other code calls `renderStreaming` with different expectations.

- [ ] **Step 6: Commit**

```bash
cd /Users/admin/gitspace/deepact && git add ui/streaming_render_test.go ui/model.go && git commit -m "feat: unify streaming rendering to glamour

Replace wrapText with renderMarkdown in renderStreaming so streaming
display matches final display. Add package-level cache to avoid
re-rendering on every 100ms tick. Fallback to legacy wrapText path
when glamour is unavailable."
```
