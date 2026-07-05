# Diff Viewer: Fix Copy & Visual Artifacts — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix two bugs in the full-screen diff viewer: enable text selection/copy, and eliminate visual artifacts when scrolling.

**Architecture:** Reuse existing `SelectionState` mechanism for the diff viewer; pad ANSI-styled lines to terminal width to prevent Bubble Tea frame diff artifacts.

**Tech Stack:** Go, Bubble Tea (charmbracelet), lipgloss, x/ansi

## Global Constraints

- Single file change: `ui/model.go`
- No new dependencies
- Existing tests must pass
- Minimal diff — do not refactor adjacent code

---

### Task 1: Fix diff viewer — enable selection, pad lines, clean exit

**Files:**
- Modify: `ui/model.go`

**Interfaces:**
- Produces: `computeDiffViewerLayout()` — `func (m Model) computeDiffViewerLayout() (totalLines, bodyHeight int, rendered, plain []string)`
- Consumes: existing `SelectionState`, `copySelection()`, `screenToLine()`, `repaintCmd()`, `stripAnsi()`, `ansi.Truncate`, `ansi.StringWidth`

- [ ] **Step 1: Add `computeDiffViewerLayout()` method**

Add after the existing `computeLayoutFull()` method (around line 726 of model.go):

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

- [ ] **Step 2: Compile — verify method signature**

Run: `cd /Users/admin/gitspace/deepact && go build ./...`
Expected: compiles successfully (method added but not yet called).

- [ ] **Step 3: Expand `Update()` diff viewer mouse handler**

Replace the `if m.diffViewerActive` block at lines 243-257 of model.go with the expanded version that supports wheel scroll + drag-to-select + copy.

The OLD block to replace:
```go
		if m.diffViewerActive {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.scrollOffset += m.height / 3
				return m, m.repaintCmd()
			case tea.MouseButtonWheelDown:
				m.scrollOffset -= m.height / 3
				if m.scrollOffset < 0 {
					m.scrollOffset = 0
				}
				return m, m.repaintCmd()
			}
			return m, nil
		}
```

The NEW block:
```go
		// Full-screen diff viewer: support wheel scroll + drag-to-select + copy.
		if m.diffViewerActive {
			// Mouse motion during drag: extend selection in diff viewer.
			if msg.Action == tea.MouseActionMotion {
				if m.selection.Active {
					sel := m.selection
					sel.End = screenToLine(msg.Y, msg.X, sel.Scroll, sel.BodyHeight, len(sel.Plain))
					m.lastMouseX = msg.X
					m.lastMouseY = msg.Y
					scrollEdge := 2
					newDir := 0
					if msg.Y < scrollEdge {
						newDir = -1
					} else if msg.Y >= sel.BodyHeight-scrollEdge {
						newDir = 1
					}
					m.selection = sel
					if newDir != 0 && m.autoScrollDir == 0 {
						m.autoScrollDir = newDir
						return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
							return autoScrollTickMsg{}
						})
					}
					m.autoScrollDir = newDir
				}
				return m, m.repaintCmd()
			}
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.scrollOffset += m.height / 3
				return m, m.repaintCmd()
			case tea.MouseButtonWheelDown:
				m.scrollOffset -= m.height / 3
				if m.scrollOffset < 0 {
					m.scrollOffset = 0
				}
				return m, m.repaintCmd()
			case tea.MouseButtonLeft:
				if msg.Action == tea.MouseActionPress {
					totalLines, bodyHeight, rendered, plain := m.computeDiffViewerLayout()
					maxScroll := 0
					if totalLines > bodyHeight {
						maxScroll = totalLines - bodyHeight
					}
					scroll := m.scrollOffset
					if scroll > maxScroll {
						scroll = maxScroll
					}
					if scroll < 0 {
						scroll = 0
					}
					pt := screenToLine(msg.Y, msg.X, scroll, bodyHeight, totalLines)
					m.selection = SelectionState{
						Active:     true,
						Done:       false,
						Start:      pt,
						End:        pt,
						Rendered:   rendered,
						Plain:      plain,
						BodyHeight: bodyHeight,
						Scroll:     scroll,
					}
					m.autoScrollDir = 0
					m.lastMouseX = msg.X
					m.lastMouseY = msg.Y
					return m, m.repaintCmd()
				} else if msg.Action == tea.MouseActionRelease {
					if m.selection.Active {
						sel := m.selection
						sel.End = screenToLine(msg.Y, msg.X, sel.Scroll, sel.BodyHeight, len(sel.Plain))
						sel.Active = false
						m.autoScrollDir = 0
						if sel.Start == sel.End {
							m.selection = SelectionState{}
						} else {
							sel.Done = true
							m.selection = sel
							_, err := copySelection(nil, m.selection)
							if err != nil {
								m.clipboardError = err.Error()
							} else {
								m.clipboardError = ""
							}
							m.clipboardFeedback = time.Now()
						}
					}
					return m, m.repaintCmd()
				}
			}
			return m, nil
		}
```

- [ ] **Step 4: Compile — verify Update() changes**

Run: `cd /Users/admin/gitspace/deepact && go build ./...`
Expected: compiles successfully.

- [ ] **Step 5: Update `View()` — two changes: frozen diff viewer selection + line padding**

**5a.** In the diff viewer block (around line 796), add frozen snapshot support:

OLD:
```go
	if m.diffViewerActive {
		// Full-screen diff viewer: render the selected hunk, scrollable.
		lines = m.renderDiffViewer(scrollContentWidth)
		total = len(lines)
		scrollOff = m.scrollOffset
		if total > bodyHeight {
```

NEW:
```go
	if m.diffViewerActive {
		// Full-screen diff viewer: render the selected hunk, scrollable.
		// When a selection is active, show the frozen snapshot with highlight
		// so the user sees exactly what they're dragging over.
		if frozen {
			sel := m.selection
			lines = append([]string(nil), sel.Rendered...)
			lines = m.applySelectionHighlight(lines)
			total = len(lines)
			scrollOff = sel.Scroll
			if total > bodyHeight {
				needScrollbar = true
				maxScroll = total - bodyHeight
				if scrollOff > maxScroll {
					scrollOff = maxScroll
				}
				if scrollOff < 0 {
					scrollOff = 0
				}
				end := total - scrollOff
				start := end - bodyHeight
				if start < 0 {
					start = 0
				}
				lines = lines[start:end]
			}
		} else {
			lines = m.renderDiffViewer(scrollContentWidth)
		total = len(lines)
		scrollOff = m.scrollOffset
		if total > bodyHeight {
```

And close the `else` block before `} else if frozen {`:

OLD:
```go
			lines = lines[start:end]
		}
	} else if frozen {
```

NEW:
```go
			lines = lines[start:end]
		}
		}
	} else if frozen {
```

**5b.** In Step 7 (around line 892), add padding after truncation:

OLD:
```go
	// ---- Step 7: Truncate all body lines to terminal width ----
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], contentWidth, "")
	}
```

NEW:
```go
	// ---- Step 7: Truncate all body lines to terminal width, then pad ----
	// Padding prevents Bubble Tea's incremental frame diff from leaving
	// stale characters from the previous frame in blank positions.
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], contentWidth, "")
		if w := ansi.StringWidth(lines[i]); w < contentWidth {
			lines[i] += strings.Repeat(" ", contentWidth-w)
		}
	}
```

- [ ] **Step 6: Fix ESC exit from diff viewer**

In `handleKey()`, around line 1015, modify the ESC handler:

OLD:
```go
		if m.diffViewerActive {
			m.diffViewerActive = false
			m.scrollOffset = 0
			return m, nil
		}
```

NEW:
```go
		if m.diffViewerActive {
			m.diffViewerActive = false
			m.scrollOffset = 0
			m.selection = SelectionState{}
			return m, m.repaintCmd()
		}
```

- [ ] **Step 7: Compile and run existing tests**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
cd /Users/admin/gitspace/deepact && go test ./ui/... -v -run "Diff|Hunk|Hit|ESC|Viewer"
```

Expected: all existing tests pass (no regression).

- [ ] **Step 8: Commit**

```bash
git add ui/model.go
git commit -m "fix(ui): enable text selection in diff viewer and fix scroll artifacts

- Add computeDiffViewerLayout() for diff viewer coordinate mapping
- Support mouse drag-to-select and copy in full-screen diff viewer
- Pad body lines to terminal width to prevent Bubble Tea frame diff artifacts
- Trigger full repaint on ESC exit from diff viewer"
```
