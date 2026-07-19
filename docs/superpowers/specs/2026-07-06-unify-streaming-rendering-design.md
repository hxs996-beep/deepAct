# Design: Unify Streaming Rendering to Glamour

## Problem

When the critic sub-agent is dispatched, its output is streamed to the UI via `stream_delta` events. The streaming display uses `renderStreaming` → `wrapText` (plain text line-by-line wrapping), while the final display uses `renderMessage` → `renderMarkdown` (glamour markdown renderer).

The critic's output is highly structured markdown (multiple `### Check:` sections, `**Command run:**`, `**Output observed:**`, `**Result:**`), with `\n\n` between fields. `wrapText` preserves these blank lines literally; glamour normalizes them. This produces a visually sparse streaming display with excessive blank lines that contrast sharply with the compact final display.

The existing partial fix (collapsing 3+ consecutive newlines to 2) only addresses extreme cases — `\n\n` (1 blank line) between every field is still preserved, accumulating into "lots of blank lines" across multiple checks.

## Solution

Replace `wrapText` with `renderMarkdown` in `renderStreaming`, unifying streaming and final display to the same glamour rendering path. Add a package-level cache to avoid re-rendering on every 100ms tick.

## Scope

Single file: `ui/model.go`. Only the `renderStreaming` function changes. No changes to `Model` struct, `Update`, `renderBody`, `sub_agent.go`, `turn.go`, or any engine code.

## Design

### `renderStreaming` replacement

Current implementation:
```go
func renderStreaming(streaming string, width int) []string {
    if streaming == "" {
        return []string{}
    }
    normalized := streaming
    for strings.Contains(normalized, "\n\n\n") {
        normalized = strings.ReplaceAll(normalized, "\n\n\n", "\n\n")
    }
    return wrapText(AssistantMsgStyle.Render(normalized), width)
}
```

New implementation:
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

### Cache behavior

- **Cache key**: `(content string, width int)`. Invalidated automatically when either changes.
- **Lifecycle**: No explicit invalidation needed. When `m.streaming` is cleared (set to `""`), `renderStreaming` is not called (the `if m.streaming != ""` guard in `renderBody` prevents it). On the next `stream_delta`, new content arrives and the cache key changes, triggering a fresh render.
- **Window resize**: Width change invalidates the cache. Next `renderStreaming` call re-renders with the new width.
- **Concurrency**: Protected by `streamRenderCacheMu` mutex. Lock order is `streamRenderCacheMu` → `mdRendererMu` (acquired inside `renderMarkdown` → `getMarkdownRenderer`). No reverse ordering exists, so no deadlock risk.

### Fallback strategy

`renderMarkdown` returns the original content string if:
1. The glamour renderer is nil (initialization failed)
2. `r.Render()` returns an error

The new code detects this by checking `rendered != streaming`. If they're equal, glamour failed, and the code falls back to the existing `wrapText` + 3+ newline collapse logic. This guarantees the display is never worse than the current behavior.

### What does NOT change

- `Model` struct: no new fields (cache is package-level)
- `Update` function: no changes (pre-rendering is internal to `renderStreaming`)
- `renderBody`: no changes (still calls `renderStreaming(m.streaming, width)`)
- All `m.streaming = ""` clearing points: no changes (cache auto-invalidates)
- `maybeEmit` / `sub_agent.go` / `turn.go`: no changes
- `renderMessage` / `renderMarkdown` / `getMarkdownRenderer`: no changes (reused as-is)

### Visual result

Streaming display will match final display exactly — same glamour styling (headers, bold, code blocks), same blank line normalization. The visual gap between streaming and final display is eliminated.

## Testing

- Existing `sub_agent_stream_test.go` tests `maybeEmit` (engine layer) — unaffected.
- Manual verification: trigger a critic dispatch, confirm streaming display matches final summary display (no excessive blank lines).
- Verify fallback: if glamour renderer is nil, streaming still renders (legacy path).
- Verify cache: no perceptible lag during streaming (glamour called once per content change, not per tick).
