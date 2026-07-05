# Remove Diff Viewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove "点击展开" text and click-to-expand full-screen diff viewer from the diff display, keep collapsed hunk summary lines, clean up all resulting dead code.

**Architecture:** The full-screen diff viewer is an interaction layer (mouse handling, rendering, ESC exit) built on top of collapsed hunk summary lines. Removing it means deleting all code exclusively serving the viewer across 3 files. Hunk summary lines remain as static display with `+N -N` stats.

**Tech Stack:** Go, bubbletea (TUI framework), lipgloss (styling)

## Global Constraints

- Go project; TUI using bubbletea/lipgloss
- Follow existing code patterns; no new dependencies
- `sync` import in model.go stays — `mdRendererMu sync.Mutex` (L1829) still uses it
- `regexp` import in diff_viewer.go removed — `hunkSummaryRe` is the only user
- `tea` import in diff_render_test.go removed — `TestESCExitsDiffViewer` is the only user

## File Structure

- `ui/diff_viewer.go` — hunk summary utilities. After all tasks: only `countHunkAddsDeletes()` and modified `hunkSummaryLine()` remain.
- `ui/model.go` — TUI model. Remove viewer state fields, mouse/ESC/render interaction, `computeDiffViewerLayout()`, and cascading dead code (`renderDiffHunkBlock()` + diff styles).
- `ui/diff_render_test.go` — diff render tests. Remove all viewer tests, `renderDiffHunkBlock` tests, `buildHunk()` helper. Add one new test for "点击展开" removal.

---

### Task 1: Remove "点击展开" text from hunkSummaryLine

**Files:**
- Modify: `ui/diff_viewer.go:42-51`
- Test: `ui/diff_render_test.go` (add new test)

**Interfaces:**
- Consumes: `hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string` (existing)
- Produces: same signature; output no longer contains "点击展开". Downstream callers `renderDiffBlock` and `renderToolSummary` are unaffected — they don't check for this text.

- [ ] **Step 1: Write the failing test**

Add to `ui/diff_render_test.go` (after `TestCountHunkAddsDeletes`):

```go
func TestHunkSummaryLine_NoExpandHint(t *testing.T) {
	line := hunkSummaryLine(0, "@@ -1,3 +1,3 @@", 2, 1)
	plain := stripAnsi(line)
	if strings.Contains(plain, "点击展开") {
		t.Errorf("summary line should not contain 点击展开: %q", plain)
	}
	if !strings.Contains(plain, "+2") || !strings.Contains(plain, "-1") {
		t.Errorf("summary line should contain +2 -1: %q", plain)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ui/ -run TestHunkSummaryLine_NoExpandHint -v`
Expected: FAIL — `hunkSummaryLine` output still contains "点击展开"

- [ ] **Step 3: Modify hunkSummaryLine to remove the hint text**

Replace `ui/diff_viewer.go:42-51` — remove `hintStyle` variable and the `"    " + hintStyle.Render("点击展开")` suffix from the return. Update the doc comment example:

```go
// hunkSummaryLine renders one collapsed hunk summary line:
//
//	[N] @@ -1,3 +1,3 @@    +2  -1
func hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string {
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	label := numStyle.Render(fmt.Sprintf("  [%d] ", idx+1))
	changes := addStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delStyle.Render(fmt.Sprintf("-%d", deletes))
	return label + hunkHeader + "    " + changes
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ui/ -run TestHunkSummaryLine_NoExpandHint -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add ui/diff_viewer.go ui/diff_render_test.go
git commit -m "refactor(ui): remove 点击展开 hint from hunk summary line"
```

---

### Task 2: Remove full-screen diff viewer code

**Files:**
- Modify: `ui/model.go` (6 edit points + 2 comment updates)
- Modify: `ui/diff_viewer.go` (remove 6 symbols + 1 import)
- Modify: `ui/diff_render_test.go` (remove 4 tests + 1 import)

**Interfaces:**
- Consumes: modified `hunkSummaryLine()` from Task 1
- Produces: removal of `diffViewerActive`, `diffViewerHunk` fields; `renderDiffViewer()`, `computeDiffViewerLayout()`, `buildHunkLineMap()`, `hitTestHunk()`, `hunkSummaryRe`, `hunkHit` type, `hunkLineEntry` type. Normal mouse selection/copy/scroll behavior is unaffected.

**model.go edits:**

- [ ] **Step 1: Remove viewer state fields**

Delete these 3 lines from the Model struct (currently L148-150):

```go
	// Diff hunk collapse viewer
	diffViewerActive bool    // full-screen hunk viewer is open
	diffViewerHunk   hunkHit // which hunk is shown full-screen
```

- [ ] **Step 2: Remove mouse handler viewer block**

In `Update()`, inside `case tea.MouseMsg:`, delete the entire `if m.diffViewerActive { ... }` block — from the comment `// Full-screen diff viewer: support wheel scroll + drag-to-select + copy.` through the closing `}` and `return m, nil` (currently L243-333).

After removal, `case tea.MouseMsg:` flows directly to:

```go
		// Handle motion events (drag) — they have Button=MouseButtonNone
```

- [ ] **Step 3: Remove click-to-open-viewer branch**

In the normal `MouseActionRelease` handler (inside `case tea.MouseButtonLeft:`), find the `if sel.Start == sel.End {` branch. Replace:

```go
						if sel.Start == sel.End {
							// Single click: if it lands on a hunk summary line, open
							// the full-screen diff viewer for that hunk.
							for _, e := range m.buildHunkLineMap(sel.Plain) {
								if e.lineIdx == sel.End.Line {
									hit := e.hit
									hit.msgIdx = e.msgIdx
									m.diffViewerActive = true
									m.diffViewerHunk = hit
									m.scrollOffset = 0
									m.selection = SelectionState{}
									return m, m.repaintCmd()
								}
							}
							m.selection = SelectionState{}
						} else {
```

with:

```go
						if sel.Start == sel.End {
							m.selection = SelectionState{}
						} else {
```

- [ ] **Step 4: Remove ESC handler for diff viewer**

In `handleKey()`, inside `if msg.Type == tea.KeyEsc {`, delete the viewer exit block. Replace:

```go
	if msg.Type == tea.KeyEsc {
		// Diff viewer: ESC exits to collapsed view first.
		if m.diffViewerActive {
			m.diffViewerActive = false
			m.scrollOffset = 0
			m.selection = SelectionState{}
			return m, m.repaintCmd()
		}
		if m.state == stateRunning {
```

with:

```go
	if msg.Type == tea.KeyEsc {
		if m.state == stateRunning {
```

- [ ] **Step 5: Remove render branch for diff viewer**

In the body render function, find `frozen := ...` followed by `if m.diffViewerActive {`. Delete the entire `if m.diffViewerActive { ... } else ` prefix (currently L887-933), so that `} else if frozen {` becomes `if frozen {`.

Before:

```go
	frozen := (m.selection.Active || m.selection.Done) && m.selection.Rendered != nil
	if m.diffViewerActive {
		// Full-screen diff viewer: render the selected hunk, scrollable.
		// When a selection is active, show the frozen snapshot with highlight
		// so the user sees exactly what they're dragging over.
		if frozen {
			... (frozen snapshot rendering) ...
		} else {
			lines = m.renderDiffViewer(scrollContentWidth)
			... (scroll rendering) ...
		}
	} else if frozen {
```

After:

```go
	frozen := (m.selection.Active || m.selection.Done) && m.selection.Rendered != nil
	if frozen {
```

- [ ] **Step 6: Remove computeDiffViewerLayout function**

Delete the entire function (currently L804-817) plus its preceding blank line:

```go
// computeDiffViewerLayout returns the diff viewer's rendered content for
// mouse coordinate mapping and selection snapshots.
func (m Model) computeDiffViewerLayout() (totalLines, bodyHeight int, rendered, plain []string) {
	bh := m.height - m.footerHeight()
	if bh < 1 {
		bh = 1
	}
	rendered = m.renderDiffViewer(m.renderBodyWidth())
	plain = make([]string, len(rendered))
	for i, l := range rendered {
		plain[i] = stripAnsi(l)
	}
	return len(plain), bh, rendered, plain
}
```

- [ ] **Step 7: Update hunkSeq comments**

There are two occurrences of this comment (in `renderDiffBlock` and `renderToolSummary`). Replace:

```go
	hunkSeq := 0 // global 1-based hunk number across all files (matches hitTestHunk)
```

with:

```go
	hunkSeq := 0 // global 1-based hunk number across all files
```

**diff_viewer.go edits:**

- [ ] **Step 8: Remove viewer functions and types**

Delete everything except `countHunkAddsDeletes()` and `hunkSummaryLine()`. Specifically remove:
- `hunkHit` type (L11-17)
- `renderDiffViewer()` function (L53-85)
- `hunkSummaryRe` var (L87-88)
- `hitTestHunk()` function (L90-125)
- `hunkLineEntry` type (L127-132)
- `buildHunkLineMap()` function (L134-194)

After removal, `diff_viewer.go` should contain only:

```go
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// countHunkAddsDeletes counts added (+) and deleted (-) lines in a hunk body.
// Lines starting with "+++" / "---" (file headers) are not counted.
func countHunkAddsDeletes(hunk string) (adds, deletes int) {
	for _, line := range strings.Split(hunk, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				adds++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				deletes++
			}
		}
	}
	return adds, deletes
}

// hunkSummaryLine renders one collapsed hunk summary line:
//
//	[N] @@ -1,3 +1,3 @@    +2  -1
func hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string {
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	label := numStyle.Render(fmt.Sprintf("  [%d] ", idx+1))
	changes := addStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delStyle.Render(fmt.Sprintf("-%d", deletes))
	return label + hunkHeader + "    " + changes
}
```

Note: `regexp` import is removed — `hunkSummaryRe` was its only user.

**diff_render_test.go edits:**

- [ ] **Step 9: Remove viewer tests**

Delete these 4 test functions:
- `TestRenderDiffViewer_RendersHunkFullscreen` (L198-219)
- `TestHitTestHunk` (L221-267)
- `TestESCExitsDiffViewer` (L269-286)
- `TestRenderDiffViewer_FromMessageSnapshot` (L304-323)

- [ ] **Step 10: Remove tea import**

Replace the import block:

```go
import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)
```

with:

```go
import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)
```

- [ ] **Step 11: Build and test**

Run: `go build ./ui/`
Expected: compiles with no errors

Run: `go test ./ui/ -v`
Expected: all remaining tests PASS:
- `TestRenderDiffHunkBlock_*` (5 tests — still valid, renderDiffHunkBlock not yet removed)
- `TestRenderDiffBlock_NoPadToTerminalWidth`
- `TestRenderDiffBlock_CollapsesHunks`
- `TestCountHunkAddsDeletes`
- `TestHunkSummaryLine_NoExpandHint` (from Task 1)
- `TestRenderToolSummary_CollapsesHunks`
- `TestFinishStreaming_SnapshotsToolTree`

Run: `go vet ./ui/`
Expected: no warnings

- [ ] **Step 12: Commit**

```bash
git add ui/model.go ui/diff_viewer.go ui/diff_render_test.go
git commit -m "refactor(ui): remove full-screen diff viewer interaction"
```

---

### Task 3: Remove cascading dead code (renderDiffHunkBlock + diff styles)

After Task 2, `renderDiffHunkBlock()` has no production callers — its only caller was `renderDiffViewer()` which was removed. The diff style variables and `initDiffStyles()` are only used by `renderDiffHunkBlock()`.

**Files:**
- Modify: `ui/model.go:2185-2243`
- Modify: `ui/diff_render_test.go` (remove 5 tests + `buildHunk` helper)

**Interfaces:**
- Consumes: Task 2 removed `renderDiffViewer()` — the only production caller of `renderDiffHunkBlock()`
- Produces: removal of `renderDiffHunkBlock()`, `diffDeleteStyle`, `diffInsertStyle`, `diffHunkHeaderStyle`, `diffStylesOnce`, `initDiffStyles()`. No downstream impact — nothing references these symbols.

**model.go edits:**

- [ ] **Step 1: Remove renderDiffHunkBlock and diff styles**

Delete L2185-2243 (the comment, var block, `initDiffStyles()`, and `renderDiffHunkBlock()`):

```go
// diff styles cached for performance
var (
	diffDeleteStyle     lipgloss.Style
	diffInsertStyle     lipgloss.Style
	diffHunkHeaderStyle lipgloss.Style
	diffStylesOnce      sync.Once
)

func initDiffStyles() {
	diffDeleteStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("210")) // light red text, no background
	diffInsertStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("114")) // light green text, no background
	diffHunkHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("178")) // yellow
}

// renderDiffHunkBlock renders a unified diff hunk as plain-text lines with
// minimal color: red for deletions, green for insertions, yellow for headers.
// No line numbers or complex ANSI styling — just the raw diff with +/- coloring.
func renderDiffHunkBlock(hunkContent string, maxWidth int) []string {
	... (entire function body) ...
}
```

After removal, `isDiffContent()` (L2245) directly follows the preceding function's closing `}`.

**diff_render_test.go edits:**

- [ ] **Step 2: Remove renderDiffHunkBlock tests and buildHunk helper**

Delete:
- `buildHunk()` helper (L11-15) — only used by `TestRenderDiffHunkBlock_PreservesEmptyLines`
- `TestRenderDiffHunkBlock_PreservesEmptyLines` (L17-25)
- `TestRenderDiffHunkBlock_StripsCR` (L27-36)
- `TestRenderDiffHunkBlock_LineNumbersAligned` (L38-59)
- `TestRenderDiffHunkBlock_PreservesRawContent` (L61-73)
- `TestRenderDiffHunkBlock_WideCharPreserved` (L75-87)

- [ ] **Step 3: Build and test**

Run: `go build ./ui/`
Expected: compiles with no errors

Run: `go test ./ui/ -v`
Expected: all remaining tests PASS:
- `TestRenderDiffBlock_NoPadToTerminalWidth`
- `TestRenderDiffBlock_CollapsesHunks`
- `TestCountHunkAddsDeletes`
- `TestHunkSummaryLine_NoExpandHint`
- `TestRenderToolSummary_CollapsesHunks`
- `TestFinishStreaming_SnapshotsToolTree`

Run: `go vet ./ui/`
Expected: no warnings

- [ ] **Step 4: Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "refactor(ui): remove dead renderDiffHunkBlock and diff styles"
```
