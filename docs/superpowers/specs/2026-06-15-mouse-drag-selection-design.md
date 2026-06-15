# Mouse Drag Selection Design

Date: 2026-06-15

## Overview

Implement mouse left-button drag-to-select in the conversation area. Selected text is automatically copied to the system clipboard on mouse release. This replaces the previous reliance on terminal-native text selection (Option+drag / Shift+drag).

## Requirements

1. **Direct drag select**: Mouse left-button press → drag → release selects text in the conversation area
2. **Auto-copy**: On mouse release, selected plain text (ANSI-stripped) is copied to the system clipboard
3. **Reverse highlight**: Selected region displayed with ANSI reverse video (`\x1b[7m`)
4. **Conversation area only**: Selection is limited to the scrollable message body; the input field is not selectable
5. **Plain text copy**: Clipboard content has all ANSI escape sequences stripped
6. **Scroll during drag**: Mouse wheel scrolling works while selecting; the selection endpoint follows the current mouse position
7. **Status bar feedback**: Brief "✓ Copied to clipboard" message after auto-copy

## Data Model

### Selection State

```go
type selPoint struct {
    Line int // index in renderBody full line array
    Col  int // visual column (0-based, ANSI-aware width)
}

type SelectionState struct {
    Active bool     // currently dragging
    Done   bool     // selection completed (highlight persists)
    Start  selPoint // mouse-down position
    End    selPoint // current / mouse-up position
}
```

Added to `Model`:
```go
selection SelectionState
```

### Normalization

`normalizeSelection(s SelectionState)` returns (start, end selPoint) with start ≤ end (swapped if needed).

## Mouse Event Handling

### State Machine

```
IDLE → MouseLeftDown → SELECTING
SELECTING → MouseMotion → update End, redraw
SELECTING → MouseLeftUp → DONE (copy + highlight)
SELECTING → MouseWheelUp/Down → scroll + update End
DONE → any key / click → IDLE (clear selection)
DONE → new message → IDLE (clear selection)
```

### Update() Changes

In `ui/model.go` `Update()`, extend the existing `tea.MouseMsg` handler:

```go
case tea.MouseMsg:
    switch msg.Button {
    case tea.MouseButtonWheelUp:
        // existing scroll logic (unchanged)
    case tea.MouseButtonWheelDown:
        // existing scroll logic (unchanged)
    case tea.MouseButtonLeft:  // NEW
        if msg.Action == tea.MouseActionPress {
            // Start selection
            m.selection = SelectionState{
                Active: true,
                Done:   false,
                Start:  m.screenToLine(msg.Y, msg.X),
                End:    m.screenToLine(msg.Y, msg.X),
            }
        } else if msg.Action == tea.MouseActionRelease {
            if m.selection.Active {
                m.selection.Active = false
                m.selection.Done = true
                m.selection.End = m.screenToLine(msg.Y, msg.X)
                m.copySelection()
            }
        }
    case tea.MouseActionMotion:  // NEW
        if m.selection.Active {
            m.selection.End = m.screenToLine(msg.Y, msg.X)
        }
    }
```

### Coordinate Mapping: screenToLine()

Maps screen coordinates (row, col) to renderBody line index:

```go
func (m Model) screenToLine(screenRow, screenCol int) selPoint {
    // screenRow is 0-based from terminal top
    // Body starts at row 0 (no header in current layout)
    // Visible window: lines[total-maxScroll-scrollOff-bodyHeight : total-maxScroll-scrollOff]
    bodyHeight := m.height - footerHeight
    totalLines := m.cachedTotalLines // stored after renderBody()
    maxScroll := max(0, totalLines - bodyHeight)
    scrollOff := clamp(m.scrollOffset, 0, maxScroll)

    // Bottom of visible window
    endLine := totalLines - scrollOff
    startLine := endLine - bodyHeight

    // Map screen row to content line
    lineIdx := startLine + screenRow
    lineIdx = clamp(lineIdx, 0, totalLines-1)

    return selPoint{Line: lineIdx, Col: screenCol}
}
```

**Note**: `cachedTotalLines` is set during `renderBody()` so `screenToLine()` can compute the mapping without re-rendering.

## Rendering

### Selection Highlight

After `renderBody()` produces `[]string` lines and before slicing the visible window, apply reverse highlight:

```go
func (m Model) applySelectionHighlight(lines []string) []string {
    if !m.selection.Active && !m.selection.Done {
        return lines
    }
    start, end := normalizeSelection(m.selection)
    for i := start.Line; i <= end.Line; i++ {
        if i < 0 || i >= len(lines) {
            continue
        }
        colStart, colEnd := 0, -1 // -1 = to end of line
        if i == start.Line {
            colStart = start.Col
        }
        if i == end.Line {
            colEnd = end.Col
        }
        lines[i] = reverseHighlightLine(lines[i], colStart, colEnd)
    }
    return lines
}
```

### reverseHighlightLine()

Applies ANSI reverse video to a visual column range within an ANSI-formatted string:

1. Walk the string character by character, tracking ANSI state and visual column position
2. At `colStart`, insert `\x1b[7m`
3. At `colEnd`, insert `\x1b[27m`
4. Preserve existing ANSI sequences (don't break color/bold codes)

### Rendering Pipeline (updated)

```go
func (m Model) View() string {
    // ... existing footer rendering ...
    lines := m.renderBody(width)
    m.cachedTotalLines = len(lines)        // NEW: cache for screenToLine
    lines = m.applySelectionHighlight(lines) // NEW
    // ... existing scroll window slicing ...
}
```

## Clipboard

### Copy on Release

```go
func (m *Model) copySelection() {
    text := m.extractSelectionText()
    if text == "" {
        return
    }
    if err := copyToClipboard(text); err == nil {
        m.clipboardFeedback = time.Now() // triggers status bar message
    }
}
```

### extractSelectionText()

Extracts plain text from the selected line range:

1. For each line in `[start.Line, end.Line]`, retrieve the original message content from `m.messages`
2. Map line indices back to `DisplayMessage` entries using a line-to-message index built during `renderBody()`
3. Use `ansi.Strip()` on the rendered text as fallback
4. Strip trailing whitespace per line, join with `\n`

### copyToClipboard()

```go
func copyToClipboard(text string) error {
    switch runtime.GOOS {
    case "darwin":
        return exec.Command("pbcopy").Input(strings.NewReader(text)).Run()
    case "linux":
        if _, err := exec.LookPath("wl-copy"); err == nil {
            return exec.Command("wl-copy", text).Run()
        }
        return exec.Command("xclip", "-selection", "clipboard").Input(strings.NewReader(text)).Run()
    }
    return fmt.Errorf("clipboard: unsupported platform %s", runtime.GOOS)
}
```

## Clearing Selection

Selection is cleared (set to zero-value `SelectionState`) on:

- Any key press (Escape, typing, etc.)
- Mouse click with no drag (start == end position on release)
- New message arriving from the engine
- Input submission

## Status Bar Feedback

After successful clipboard copy, show "✓ Copied to clipboard" in the status bar for 2 seconds:

```go
// In renderStatusBar():
if !m.clipboardFeedback.IsZero() && time.Since(m.clipboardFeedback) < 2*time.Second {
    // Show feedback message
}
```

No additional timer needed — the existing `TickMsg` (500ms interval) naturally refreshes the status bar, and the message disappears after 2 seconds.

## Line-to-Message Index

To map selected lines back to original message content for clean text extraction:

```go
type lineMeta struct {
    msgIdx  int    // index in m.messages
    lineOff int    // line offset within that message's rendered output
}
```

During `renderBody()`, build `[]lineMeta` parallel to the output `[]string`. This allows `extractSelectionText()` to:
1. Find which `DisplayMessage` each selected line belongs to
2. Extract the raw Markdown content (no ANSI) for that message
3. Map line ranges to the correct substring of the original content

## Removed Behavior

- Remove the "Option+drag" / "Shift+drag" hint from the status bar
- Mouse drag now exclusively selects text within the application

## Edge Cases

| Case | Behavior |
|------|----------|
| Drag starts outside body area | Clamp to first/last body line |
| Single click (no drag) | Clear any existing selection; no copy |
| Empty selection (start == end) | Same as single click |
| Drag while scrolled up | Wheel still scrolls; selection endpoint re-mapped after scroll |
| Selection spans multiple messages | Extract text from all messages in range, joined with newlines |
| Very long selection (>10K chars) | Copy all; no truncation |
| Clipboard tool missing (Linux) | Silently fail; show no feedback |
| Terminal doesn't support mouse motion | No selection possible; wheel still works |

## Files to Modify

| File | Changes |
|------|---------|
| `ui/model.go` | Add `SelectionState`, `clipboardFeedback`, `cachedTotalLines`, `lineMetas` fields; modify `Update()` mouse handler; add `screenToLine()`, `applySelectionHighlight()`, `copySelection()`, `extractSelectionText()`, `normalizeSelection()`; modify `View()` to apply highlight; modify `renderBody()` to build `lineMetas`; modify `renderStatusBar()` for feedback; remove Option/Shift+drag hint |
| `ui/clipboard.go` | New file: `copyToClipboard()`, `reverseHighlightLine()` |
| `ui/model_test.go` | Add tests for selection state machine, coordinate mapping, highlight rendering, clipboard copy |
| `ui/clipboard_test.go` | New file: tests for `reverseHighlightLine()`, `copyToClipboard()` |
