# Diff Viewer: Fix Copy & Visual Artifacts

Date: 2026-07-02

## Problem

Two bugs in the full-screen diff hunk viewer (`diffViewerActive`):

1. **Cannot select/copy text**: Mouse events in diff viewer only handle wheel scroll — all press/motion/release events are discarded at `model.go:243-257`.
2. **Visual artifacts on scroll**: When scrolling past one page, characters from other lines bleed into blank positions on the current line. Caused by Bubble Tea's incremental frame diff mis-rendering ANSI-styled lines that aren't padded to terminal width.

## Approach

Fix both bugs in the existing terminal ANSI rendering (approach A). If user tests and still sees issues, fall back to plain-text rendering (approach B).

## Design

### Fix 1: Enable text selection in diff viewer

Reuse the existing `SelectionState` mechanism for the diff viewer:

- **New method** `computeDiffViewerLayout()` — returns `(totalLines, bodyHeight, rendered, plain)` for the diff viewer content, mirroring `computeLayoutFull()`.
- **MousePress** in diff viewer: snapshot via `computeDiffViewerLayout()`, init `SelectionState`.
- **MouseMotion** in diff viewer: extend selection using snapshot, support edge auto-scroll.
- **MouseRelease** in diff viewer: if single-click, clear selection; if drag, copy to clipboard via `copySelection()`.
- **View()**: when `diffViewerActive && frozen`, show snapshot with highlight instead of live rendering.
- **ESC exit**: clear selection and trigger full repaint.

### Fix 2: Pad lines to prevent artifacts

- In `View()` Step 7, after `ansi.Truncate`, pad each line with spaces to `contentWidth` so no cell is left "empty" for Bubble Tea's frame diff to bleed into.
- On ESC exit from diff viewer, add `repaintCmd()` to ensure clean transition from ANSI-heavy diff view to normal view.

## Files Changed

- `ui/model.go`:
  - `Update()`: expand `diffViewerActive` mouse handler (~80 lines of selection logic)
  - `View()`: add frozen-snapshot sub-branch in diff viewer block; pad lines in Step 7
  - New method `computeDiffViewerLayout()`
  - ESC handler: add repaint + clear selection on diff viewer exit
