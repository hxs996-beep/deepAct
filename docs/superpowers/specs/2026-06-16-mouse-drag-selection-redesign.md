# Mouse Drag-to-Select Redesign

## Background

The original implementation (+728 lines across 7 files) had several fundamental issues:

1. **`lineMetas` dead code** — built in `renderBody()` but never consumed
2. **`cachedBodyLines` pollution** — highlight ANSI markers baked into cache
3. **No Windows clipboard support**
4. **No auto-scroll during drag**
5. **Fragile coordinate mapping** — `screenToLine` depends on multiple layout calculations
6. **Missing end-to-end tests** for the `Update()` state machine
7. **Silent clipboard errors**

This redesign uses a "plain text layer + visual layer separation" approach to solve these problems at the root.

## Design

### 1. Data Model

```go
// selPoint represents a position in the plain-text line array
type selPoint struct {
    Line int // plain-text line index (0-based)
    Col  int // visual column (0-based, CJK/emoji counted as width-2)
}

// SelectionState tracks the drag selection lifecycle
type SelectionState struct {
    Active bool     // currently dragging
    Done   bool     // drag complete, highlight persists
    Start  selPoint // mouse-down position
    End    selPoint // current / mouse-up position
}
```

Model field changes:

```go
// Add
plainTextLines    []string       // plain text per line, no ANSI, 1:1 with rendered lines

// Keep
selection         SelectionState
clipboardFeedback time.Time

// Remove
cachedBodyLines   []string       // replaced by plainTextLines
cachedTotalLines  int            // use len(plainTextLines)
lineMetas         []lineMeta     // dead code
```

### 2. Rendering Pipeline

`renderBody` returns both rendered and plain-text lines:

```go
func (m Model) renderBody(scrollContentWidth int) (rendered []string, plain []string)
```

Inside `renderBody`, each rendered line is paired with `stripAnsi(renderedLine)` to produce the corresponding plain line. Both slices always have equal length and 1:1 correspondence.

`View()` flow:

1. `renderedLines, plainLines = m.renderBody(...)`
2. `m.plainTextLines = plainLines`
3. `renderedLines = m.applySelectionHighlight(renderedLines)` — does NOT mutate cache
4. Slice and display `renderedLines`

### 3. Coordinate Mapping & Mouse Events

`screenToLine` maps screen coordinates to `plainTextLines` indices:

```go
func (m Model) screenToLine(screenRow, screenCol int) selPoint
```

- `screenRow - scrollOffset` → content line index (also `plainTextLines` index)
- `screenCol` → visual column (0-based)
- Clamp line index to `[0, len(m.plainTextLines)-1]`

Mouse event handling:

| Event | Behavior |
|---|---|
| Left button press | `screenToLine` → `SelectionState{Active: true, Start: pt, End: pt}` |
| Motion (while dragging) | `screenToLine` → update `End`; check for auto-scroll trigger |
| Left button release | `screenToLine` → update `End`, set `Active=false`; if `Start != End`, set `Done=true` and `copySelection()` |
| Single click (Start == End) | Clear selection |
| Any key press | Clear Done selection |

### 4. Auto-Scroll During Drag

When the mouse is near the visible area edge (within 2 rows of top/bottom) during a drag:

- Near top → `scrollOffset--` (min 0)
- Near bottom → `scrollOffset++` (max `totalLines - bodyHeight`)
- Use a `tea.Tick` timer (e.g., 50ms) to drive continuous scrolling while the mouse stays at the edge
- After scroll, `End` is recalculated naturally via `screenToLine` with the new scroll offset

The tick is started when a drag enters the edge zone and stopped when the drag ends or moves away from the edge.

### 5. Text Extraction & Clipboard

Simplified `extractSelectionText` — reads directly from `plainTextLines`, no `stripAnsi` needed:

```go
func (m Model) extractSelectionText() string {
    start, end := normalizeSelection(m.selection)
    var buf strings.Builder
    for i := start.Line; i <= end.Line; i++ {
        if i < 0 || i >= len(m.plainTextLines) {
            continue
        }
        line := m.plainTextLines[i]
        colStart, colEnd := 0, visualWidth(line)
        if i == start.Line { colStart = start.Col }
        if i == end.Line   { colEnd = end.Col }
        if i > start.Line  { buf.WriteByte('\n') }
        buf.WriteString(sliceByVisualCol(line, colStart, colEnd))
    }
    return buf.String()
}
```

Cross-platform `copyToClipboard`:

| OS | Method |
|---|---|
| macOS | `pbcopy` |
| Linux (Wayland) | `wl-copy` |
| Linux (X11) | `xclip -selection clipboard` |
| Windows | Win32 API via `golang.org/x/sys/windows` |

A `pipeToCmd` helper extracts the common `exec.Command` + stdin pipe pattern for Unix platforms. `windowsCopy` uses `OpenClipboard` → `EmptyClipboard` → `SetClipboardData(CF_UNICODETEXT)` → `CloseClipboard`.

Clipboard errors are surfaced: show a brief status bar message on failure instead of swallowing silently.

### 6. File Organization

Everything goes into `ui/selection.go`:

| Symbol | Origin |
|---|---|
| `selPoint` | original `selection.go` |
| `SelectionState` | original `selection.go` |
| `normalizeSelection` | original `selection.go` |
| `screenToLine` | original `selection.go`, simplified |
| `applySelectionHighlight` | original `selection.go` |
| `reverseHighlightLine` | original `clipboard.go` |
| `extractSelectionText` | original `clipboard.go`, simplified |
| `sliceByVisualCol` | original `clipboard.go` |
| `copyToClipboard` | original `clipboard.go`, +Windows |
| `windowsCopy` | new |
| `pipeToCmd` | new, extracted from `copyToClipboard` |
| `stripAnsi` | original `clipboard.go`, now used only in `renderBody` |
| `findAnsiSeqEnd` | original `clipboard.go`, helper for `stripAnsi` |
| `decodeRuneAt` | original `clipboard.go`, helper for `reverseHighlightLine` |

Deleted files: `ui/clipboard.go`, `ui/clipboard_test.go`

### 7. Test Plan

`ui/selection_test.go` consolidates all tests:

- **Retained**: normalizeSelection, screenToLine, sliceByVisualCol (CJK/ASCII/mixed/straddle), extractSelectionText (single-line CJK, multi-line), reverseHighlightLine (full/partial/ANSI/empty), stripAnsi
- **Added**: `Update()` state machine end-to-end (press → drag → release → Done + clipboard called), auto-scroll trigger edge detection, Windows clipboard mock test
- **Removed**: duplicate tests from `clipboard_test.go`, dead `lineMeta` related tests

### 8. Code Deletion Summary

- `lineMeta` struct and all code building/populating it in `renderBody`
- `m.lineMetas` field
- `m.cachedBodyLines` field
- `m.cachedTotalLines` field
- `ui/clipboard.go` (merged into `selection.go`)
- `ui/clipboard_test.go` (merged into `selection_test.go`)
- `renderStatusBar` parameter change: `clipboardFeedback` accessed via `m.clipboardFeedback` directly
