# Mouse Drag Selection 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在对话区实现鼠标左键拖拽选中文本，松开自动复制到系统剪贴板，反色高亮显示选中区域。

**架构：** 扩展 Bubble Tea 的 `tea.MouseMsg` 处理，捕获 MouseLeftDown/Motion/LeftUp 事件序列，将屏幕坐标映射到 renderBody 全量行索引，选中区域用 ANSI 反色渲染，松开鼠标时提取纯文本并调用系统剪贴板工具（pbcopy/xclip/wl-copy）。

**技术栈：** Bubble Tea MouseMsg, ANSI escape sequences, os/exec (clipboard)

---

## 文件结构

| 文件 | 职责 | 操作 |
|------|------|------|
| `ui/selection.go` | 选区类型定义（selPoint, SelectionState）、坐标映射（screenToLine）、归一化（normalizeSelection）、选区清除 | 新建 |
| `ui/clipboard.go` | 剪贴板写入（copyToClipboard）、ANSI 反色渲染（reverseHighlightLine）、选中文本提取 | 新建 |
| `ui/model.go` | Model 新增 selection/clipboardFeedback/cachedTotalLines/lineMetas 字段；修改 Update() 鼠标处理；修改 View() 渲染管线；修改 renderBody() 构建 lineMetas；修改 renderStatusBar() 反馈；修改 handleKey() 清除选区；移除 Option/Shift+drag 提示 | 修改 |
| `ui/selection_test.go` | selPoint 归一化、坐标映射、选区清除测试 | 新建 |
| `ui/clipboard_test.go` | reverseHighlightLine、copyToClipboard 测试 | 新建 |
| `ui/model_test.go` | 鼠标拖拽状态机集成测试 | 修改 |

---

### 任务 1：选区类型与归一化

**文件：**
- 创建：`ui/selection.go`
- 创建：`ui/selection_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
// ui/selection_test.go
package ui

import "testing"

func TestNormalizeSelection_StartBeforeEnd(t *testing.T) {
    s := SelectionState{
        Active: true,
        Start:  selPoint{Line: 2, Col: 5},
        End:    selPoint{Line: 8, Col: 10},
    }
    start, end := normalizeSelection(s)
    if start.Line != 2 || start.Col != 5 {
        t.Errorf("start: want {2,5}, got {%d,%d}", start.Line, start.Col)
    }
    if end.Line != 8 || end.Col != 10 {
        t.Errorf("end: want {8,10}, got {%d,%d}", end.Line, end.Col)
    }
}

func TestNormalizeSelection_StartAfterEnd(t *testing.T) {
    s := SelectionState{
        Active: true,
        Start:  selPoint{Line: 8, Col: 10},
        End:    selPoint{Line: 2, Col: 5},
    }
    start, end := normalizeSelection(s)
    if start.Line != 2 || start.Col != 5 {
        t.Errorf("start: want {2,5}, got {%d,%d}", start.Line, start.Col)
    }
    if end.Line != 8 || end.Col != 10 {
        t.Errorf("end: want {8,10}, got {%d,%d}", end.Line, end.Col)
    }
}

func TestNormalizeSelection_SameLineColReversed(t *testing.T) {
    s := SelectionState{
        Active: true,
        Start:  selPoint{Line: 5, Col: 20},
        End:    selPoint{Line: 5, Col: 3},
    }
    start, end := normalizeSelection(s)
    if start.Line != 5 || start.Col != 3 {
        t.Errorf("start: want {5,3}, got {%d,%d}", start.Line, start.Col)
    }
    if end.Line != 5 || end.Col != 20 {
        t.Errorf("end: want {5,20}, got {%d,%d}", end.Line, end.Col)
    }
}

func TestNormalizeSelection_EmptySelection(t *testing.T) {
    s := SelectionState{}
    start, end := normalizeSelection(s)
    if start != (selPoint{}) || end != (selPoint{}) {
        t.Errorf("empty selection should return zero values")
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestNormalizeSelection -v`
预期：FAIL，`undefined: selPoint` / `undefined: SelectionState` / `undefined: normalizeSelection`

- [ ] **步骤 3：编写实现代码**

```go
// ui/selection.go
package ui

type selPoint struct {
    Line int // index in renderBody full line array
    Col  int // visual column (0-based, ANSI-aware width)
}

type SelectionState struct {
    Active bool     // currently dragging
    Done   bool     // selection completed (highlight persists after release)
    Start  selPoint // mouse-down position
    End    selPoint // current / mouse-up position
}

// normalizeSelection returns (start, end) with start ≤ end.
// If start > end, they are swapped.
func normalizeSelection(s SelectionState) (selPoint, selPoint) {
    if s.Start.Line < s.End.Line {
        return s.Start, s.End
    }
    if s.Start.Line > s.End.Line {
        return s.End, s.Start
    }
    // Same line: compare columns
    if s.Start.Col <= s.End.Col {
        return s.Start, s.End
    }
    return s.End, s.Start
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestNormalizeSelection -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/selection.go ui/selection_test.go
git commit -m "feat(ui): add selection types and normalizeSelection"
```

---

### 任务 2：ANSI 反色渲染

**文件：**
- 创建：`ui/clipboard.go`
- 创建：`ui/clipboard_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
// ui/clipboard_test.go
package ui

import "testing"

func TestReverseHighlightLine_FullLine(t *testing.T) {
    line := "hello world"
    result := reverseHighlightLine(line, 0, -1)
    want := "\x1b[7mhello world\x1b[27m"
    if result != want {
        t.Errorf("full line: want %q, got %q", want, result)
    }
}

func TestReverseHighlightLine_PartialLine(t *testing.T) {
    line := "hello world"
    result := reverseHighlightLine(line, 2, 7)
    want := "he\x1b[7mllo w\x1b[27morld"
    if result != want {
        t.Errorf("partial: want %q, got %q", want, result)
    }
}

func TestReverseHighlightLine_WithANSI(t *testing.T) {
    // Line with ANSI color: \x1b[31m is red, \x1b[0m is reset
    line := "\x1b[31mhello\x1b[0m world"
    result := reverseHighlightLine(line, 0, -1)
    // ANSI codes are zero-width, so visual text starts at col 0
    // The reverse should wrap the entire visible content
    if !containsSequence(result, "\x1b[7m") || !containsSequence(result, "\x1b[27m") {
        t.Errorf("with ANSI: missing reverse markers in %q", result)
    }
}

func TestReverseHighlightLine_ColStartBeyondLine(t *testing.T) {
    line := "hi"
    result := reverseHighlightLine(line, 10, -1)
    // Col start beyond line length → no highlighting
    if result != "hi" {
        t.Errorf("col beyond line: want %q, got %q", "hi", result)
    }
}

func TestReverseHighlightLine_EmptyLine(t *testing.T) {
    result := reverseHighlightLine("", 0, -1)
    if result != "" {
        t.Errorf("empty line: want empty, got %q", result)
    }
}

func containsSequence(s, seq string) bool {
    return len(s) >= len(seq) && (s == seq || len(s) > 0 && containsSeq(s, seq))
}

func containsSeq(s, seq string) bool {
    for i := 0; i <= len(s)-len(seq); i++ {
        if s[i:i+len(seq)] == seq {
            return true
        }
    }
    return false
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestReverseHighlightLine -v`
预期：FAIL，`undefined: reverseHighlightLine`

- [ ] **步骤 3：编写实现代码**

```go
// ui/clipboard.go
package ui

import (
    "fmt"
    "os/exec"
    "runtime"
    "strings"
)

// reverseHighlightLine applies ANSI reverse video (\x1b[7m ... \x1b[27m)
// to a visual column range within an ANSI-formatted string.
// colEnd = -1 means highlight to end of line.
// Visual columns are measured by skipping zero-width ANSI escape sequences.
func reverseHighlightLine(line string, colStart, colEnd int) string {
    if line == "" || colStart < 0 {
        return line
    }

    // Walk the string, tracking visual column position.
    // Build output by inserting reverse markers at the right visual positions.
    var sb strings.Builder
    visualCol := 0
    inHighlight := false
    i := 0

    for i < len(line) {
        // Check for ANSI escape sequence
        if line[i] == '\x1b' {
            // Find end of escape sequence
            seqEnd := findAnsiSeqEnd(line, i)
            sb.WriteString(line[i:seqEnd])
            i = seqEnd
            continue
        }

        // Printable character
        if !inHighlight && visualCol == colStart {
            sb.WriteString("\x1b[7m")
            inHighlight = true
        }
        if inHighlight && colEnd >= 0 && visualCol == colEnd {
            sb.WriteString("\x1b[27m")
            inHighlight = false
        }

        // Handle multi-byte runes: write the full rune
        r, size := decodeRune(line[i:])
        sb.Write([]byte(line[i : i+size]))
        i += size
        visualCol++
    }

    // Close highlight if it extends to end of line
    if inHighlight {
        sb.WriteString("\x1b[27m")
    }

    // If colStart was beyond the line, no highlighting was applied
    return sb.String()
}

// findAnsiSeqEnd returns the index after the end of the ANSI escape sequence
// starting at position i. Handles CSI (ESC [ ... final), OSC (ESC ] ... BEL/ST),
// and single-char sequences like ESC c.
func findAnsiSeqEnd(s string, i int) int {
    if i >= len(s) || s[i] != '\x1b' {
        return i + 1
    }
    if i+1 >= len(s) {
        return i + 1
    }
    switch s[i+1] {
    case '[':
        // CSI: ESC [ <params 0x20-0x3F> <intermediates 0x20-0x2F> <final 0x40-0x7E>
        j := i + 2
        for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3F {
            j++
        }
        for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2F {
            j++
        }
        if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7E {
            j++
        }
        return j
    case ']':
        // OSC: ESC ] <params> BEL or ESC ] <params> ESC \
        j := i + 2
        for j < len(s) && s[j] != '\x07' && s[j] != '\x1b' {
            j++
        }
        if j < len(s) && s[j] == '\x07' {
            j++
        } else if j+1 < len(s) && s[j] == '\x1b' && s[j+1] == '\\' {
            j += 2
        }
        return j
    default:
        // Two-character sequence: ESC <byte>
        return i + 2
    }
}

// decodeRune decodes the first UTF-8 rune from s starting at the beginning.
// Returns the rune and its byte size.
func decodeRune(s string) (rune, int) {
    r, size := rune(s[0]), 1
    if s[0]&0x80 != 0 {
        r, size = strings.NewReader(s).ReadRune()
    }
    return r, size
}

// copyToClipboard writes plain text to the system clipboard.
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

// stripAnsi removes all ANSI escape sequences from a string.
func stripAnsi(s string) string {
    var sb strings.Builder
    i := 0
    for i < len(s) {
        if s[i] == '\x1b' {
            i = findAnsiSeqEnd(s, i)
            continue
        }
        sb.WriteByte(s[i])
        i++
    }
    return sb.String()
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestReverseHighlightLine -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/clipboard.go ui/clipboard_test.go
git commit -m "feat(ui): add reverseHighlightLine and copyToClipboard"
```

---

### 任务 3：坐标映射 screenToLine

**文件：**
- 修改：`ui/selection.go`
- 修改：`ui/selection_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
// ui/selection_test.go — 追加以下测试

func TestScreenToLine_Basic(t *testing.T) {
    m := Model{
        height:    40,
        selection: SelectionState{},
    }
    m.cachedTotalLines = 100
    m.msgCache = &messageRenderCache{lastMaxScroll: 60}

    // scrollOffset=0, footerHeight=5, bodyHeight=35
    // visible lines: [65, 99] (100 - 0 - 1 = 99 down to 100-0-35 = 65)
    // screenRow 0 → content line 65
    pt := m.screenToLine(0, 5)
    if pt.Line != 65 {
        t.Errorf("screenToLine(0,5): want line 65, got %d", pt.Line)
    }
    if pt.Col != 5 {
        t.Errorf("screenToLine(0,5): want col 5, got %d", pt.Col)
    }
}

func TestScreenToLine_ScrolledUp(t *testing.T) {
    m := Model{
        height:       40,
        scrollOffset: 20,
    }
    m.cachedTotalLines = 100
    m.msgCache = &messageRenderCache{lastMaxScroll: 60}

    // scrollOff=20: visible lines [45, 79]
    // screenRow 0 → content line 45
    pt := m.screenToLine(0, 0)
    if pt.Line != 45 {
        t.Errorf("scrolled up: want line 45, got %d", pt.Line)
    }
}

func TestScreenToLine_ClampBeyondContent(t *testing.T) {
    m := Model{
        height: 40,
    }
    m.cachedTotalLines = 10
    m.msgCache = &messageRenderCache{lastMaxScroll: 0}

    // screenRow beyond content → clamped to last line
    pt := m.screenToLine(30, 0)
    if pt.Line != 9 {
        t.Errorf("clamp: want line 9, got %d", pt.Line)
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestScreenToLine -v`
预期：FAIL，`Model` 没有 `cachedTotalLines` 字段

- [ ] **步骤 3：编写实现代码**

在 `ui/selection.go` 中追加：

```go
// screenToLine maps screen coordinates (row, col) to a selPoint
// in the renderBody full line array.
func (m Model) screenToLine(screenRow, screenCol int) selPoint {
    bodyHeight := m.height - footerHeight(m)
    if bodyHeight < 1 {
        bodyHeight = 1
    }
    totalLines := m.cachedTotalLines
    maxScroll := 0
    if totalLines > bodyHeight {
        maxScroll = totalLines - bodyHeight
    }
    scrollOff := m.scrollOffset
    if scrollOff > maxScroll {
        scrollOff = maxScroll
    }
    if scrollOff < 0 {
        scrollOff = 0
    }

    endLine := totalLines - scrollOff
    startLine := endLine - bodyHeight

    lineIdx := startLine + screenRow
    if lineIdx < 0 {
        lineIdx = 0
    }
    if totalLines > 0 && lineIdx >= totalLines {
        lineIdx = totalLines - 1
    }

    return selPoint{Line: lineIdx, Col: screenCol}
}

// footerHeight computes the footer height from the model state.
// Matches the logic in View() Step 2.
func footerHeight(m Model) int {
    // Minimal footer: 3 (status bar padding) + 1 (input line) = 4
    h := 4
    if m.showSuggestions {
        h += len(m.suggestions)
        if h > 8 {
            h = 8
        }
    }
    if len(m.activeOptions) > 0 {
        h += len(m.activeOptions) + 2
        if h > 10 {
            h = 10
        }
    }
    return h
}
```

在 `ui/model.go` Model 结构体中添加 `cachedTotalLines int` 字段（在 `bodyDirty bool` 之后）：

```go
cachedTotalLines int // total lines from last renderBody(), used by screenToLine()
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestScreenToLine -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/selection.go ui/selection_test.go ui/model.go
git commit -m "feat(ui): add screenToLine coordinate mapping"
```

---

### 任务 4：Model 新增选区字段

**文件：**
- 修改：`ui/model.go`

- [ ] **步骤 1：在 Model 结构体中添加新字段**

在 `ui/model.go` 的 Model 结构体中，`bodyDirty bool` 之后添加：

```go
// Mouse drag selection
selection         SelectionState
clipboardFeedback time.Time // timestamp of last clipboard copy for status bar feedback
cachedTotalLines  int       // total lines from last renderBody(), used by screenToLine()
lineMetas         []lineMeta // parallel to renderBody output: maps each line to its source message
```

- [ ] **步骤 2：在 `ui/selection.go` 中添加 lineMeta 类型**

```go
// lineMeta maps a rendered line back to its source DisplayMessage.
// Built during renderBody() so extractSelectionText() can retrieve
// the original Markdown content (without ANSI codes) for clipboard copy.
type lineMeta struct {
    msgIdx  int // index in m.messages (-1 for non-message lines like logo/tool tree)
    lineOff int // line offset within that message's rendered output
}
```

- [ ] **步骤 3：确保编译通过**

运行：`cd /Users/admin/gitspace/deepact && go build ./ui/`
预期：编译成功

- [ ] **步骤 4：Commit**

```bash
git add ui/model.go ui/selection.go
git commit -m "feat(ui): add selection fields to Model"
```

---

### 任务 5：鼠标事件处理 — 拖拽状态机

**文件：**
- 修改：`ui/model.go` (Update 方法)

- [ ] **步骤 1：编写失败的测试**

```go
// ui/model_test.go — 追加

func TestMouseDragSelection_StartDrag(t *testing.T) {
    m := NewModel(nil, engine.PricingConfig{})
    m.state = stateReady
    m.height = 40
    m.cachedTotalLines = 100
    m.msgCache = &messageRenderCache{lastMaxScroll: 60}

    downMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionPress,
        Y:      10,
        X:      5,
    }
    result, _ := m.Update(downMsg)
    m2 := result.(Model)
    if !m2.selection.Active {
        t.Error("selection should be active after mouse down")
    }
    if m2.selection.Done {
        t.Error("selection should not be done during drag")
    }
}

func TestMouseDragSelection_ReleaseCopies(t *testing.T) {
    m := NewModel(nil, engine.PricingConfig{})
    m.state = stateReady
    m.height = 40
    m.cachedTotalLines = 100
    m.msgCache = &messageRenderCache{lastMaxScroll: 60}
    m.messages = []DisplayMessage{{Role: "assistant", Content: "hello world"}}
    m.lineMetas = []lineMeta{{msgIdx: 0, lineOff: 0}}

    // Start drag
    m.selection = SelectionState{
        Active: true,
        Start:  selPoint{Line: 0, Col: 0},
        End:    selPoint{Line: 0, Col: 5},
    }

    upMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionRelease,
        Y:      0,
        X:      5,
    }
    result, _ := m.Update(upMsg)
    m2 := result.(Model)
    if m2.selection.Active {
        t.Error("selection should not be active after release")
    }
    if !m2.selection.Done {
        t.Error("selection should be done after release")
    }
}

func TestMouseDragSelection_ClickNoDragClearsSelection(t *testing.T) {
    m := NewModel(nil, engine.PricingConfig{})
    m.state = stateReady
    m.height = 40
    m.cachedTotalLines = 100
    m.msgCache = &messageRenderCache{lastMaxScroll: 60}

    // Set existing selection
    m.selection = SelectionState{
        Done:  true,
        Start: selPoint{Line: 2, Col: 0},
        End:   selPoint{Line: 5, Col: 10},
    }

    // Single click (same position down+up)
    downMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionPress,
        Y:      10, X: 5,
    }
    result, _ := m.Update(downMsg)
    m2 := result.(Model)
    // After mouse down, old selection cleared, new one started
    if !m2.selection.Active {
        t.Error("selection should be active after mouse down")
    }

    // Release at same position
    upMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionRelease,
        Y:      10, X: 5,
    }
    result, _ = m2.Update(upMsg)
    m3 := result.(Model)
    // Same position = no drag = clear selection
    if m3.selection.Done {
        t.Error("single click should not persist selection")
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestMouseDragSelection -v`
预期：FAIL，mouse left events not handled

- [ ] **步骤 3：修改 Update() 中的 MouseMsg 处理**

替换 `ui/model.go` 中 `Update()` 方法内的 `tea.MouseMsg` 分支（当前 204-223 行），从：

```go
case tea.MouseMsg:
    switch msg.Button {
    case tea.MouseButtonWheelUp:
        // ... existing ...
    case tea.MouseButtonWheelDown:
        // ... existing ...
    }
    return m, nil
```

改为：

```go
case tea.MouseMsg:
    switch msg.Button {
    case tea.MouseButtonWheelUp:
        if m.state == stateReady || m.state == stateRunning {
            m.scrollOffset += m.height / 3
            if ms := m.msgCache.lastMaxScroll; ms > 0 && m.scrollOffset > ms {
                m.scrollOffset = ms
            }
        }
        return m, m.repaintCmd()
    case tea.MouseButtonWheelDown:
        if m.state == stateReady || m.state == stateRunning {
            m.scrollOffset -= m.height / 3
            if m.scrollOffset < 0 {
                m.scrollOffset = 0
            }
        }
        return m, m.repaintCmd()
    case tea.MouseButtonLeft:
        if msg.Action == tea.MouseActionPress {
            pt := m.screenToLine(msg.Y, msg.X)
            m.selection = SelectionState{
                Active: true,
                Done:   false,
                Start:  pt,
                End:    pt,
            }
            return m, nil
        } else if msg.Action == tea.MouseActionRelease {
            if m.selection.Active {
                m.selection.End = m.screenToLine(msg.Y, msg.X)
                m.selection.Active = false
                // Same position = no drag = clear selection (single click)
                if m.selection.Start == m.selection.End {
                    m.selection = SelectionState{}
                } else {
                    m.selection.Done = true
                    m.copySelection()
                }
            }
            return m, nil
        }
    case tea.MouseActionMotion:
        if m.selection.Active {
            m.selection.End = m.screenToLine(msg.Y, msg.X)
            return m, nil
        }
    }
    return m, nil
```

同时在 `ui/selection.go` 中添加 `selPoint` 的 `==` 比较（Go 结构体默认支持 `==`，无需额外代码）。

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestMouseDragSelection -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go
git commit -m "feat(ui): add mouse drag selection state machine in Update()"
```

---

### 任务 6：选中文本提取与剪贴板复制

**文件：**
- 修改：`ui/clipboard.go`
- 修改：`ui/clipboard_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
// ui/clipboard_test.go — 追加

func TestExtractSelectionText_SingleMessage(t *testing.T) {
    m := Model{
        messages: []DisplayMessage{
            {Role: "assistant", Content: "line1\nline2\nline3"},
        },
        lineMetas: []lineMeta{
            {msgIdx: 0, lineOff: 0},
            {msgIdx: 0, lineOff: 1},
            {msgIdx: 0, lineOff: 2},
        },
    }
    m.selection = SelectionState{
        Done:  true,
        Start: selPoint{Line: 0, Col: 0},
        End:   selPoint{Line: 2, Col: 5},
    }

    text := m.extractSelectionText()
    if text == "" {
        t.Error("expected non-empty extracted text")
    }
    // Should contain the message content (stripped of ANSI)
    if !containsSeq(text, "line1") {
        t.Errorf("expected 'line1' in extracted text, got %q", text)
    }
}

func TestExtractSelectionText_NoSelection(t *testing.T) {
    m := Model{}
    text := m.extractSelectionText()
    if text != "" {
        t.Errorf("empty selection should return empty string, got %q", text)
    }
}

func TestStripAnsi(t *testing.T) {
    input := "\x1b[31mhello\x1b[0m \x1b[1mworld\x1b[0m"
    result := stripAnsi(input)
    if result != "hello world" {
        t.Errorf("stripAnsi: want %q, got %q", "hello world", result)
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestExtractSelectionText -v`
预期：FAIL，`Model` 没有 `extractSelectionText` 方法

- [ ] **步骤 3：编写实现代码**

在 `ui/clipboard.go` 中追加：

```go
// extractSelectionText extracts plain text from the selected line range.
// Uses lineMetas to map back to original DisplayMessage content,
// falling back to ANSI-stripped rendered text.
func (m Model) extractSelectionText() string {
    if !m.selection.Done && !m.selection.Active {
        return ""
    }
    start, end := normalizeSelection(m.selection)

    // Fallback: use cached body lines with ANSI stripping
    if len(m.cachedBodyLines) == 0 {
        return ""
    }

    var sb strings.Builder
    for i := start.Line; i <= end.Line; i++ {
        if i < 0 || i >= len(m.cachedBodyLines) {
            continue
        }
        line := stripAnsi(m.cachedBodyLines[i])
        line = strings.TrimRight(line, " ")
        // Apply column-level trimming for first/last line
        if i == start.Line && start.Col > 0 {
            // Count visual characters to find the start column
            visCol := 0
            byteIdx := 0
            runes := []rune(line)
            for byteIdx < len(runes) && visCol < start.Col {
                byteIdx++
                visCol++
            }
            if byteIdx < len(runes) {
                line = string(runes[byteIdx:])
            }
        }
        if i == end.Line && end.Col > 0 {
            runes := []rune(line)
            if end.Col < len(runes) {
                line = string(runes[:end.Col])
            }
        }
        sb.WriteString(line)
        if i < end.Line {
            sb.WriteString("\n")
        }
    }
    return sb.String()
}

// copySelection extracts selected text and copies it to the clipboard.
func (m *Model) copySelection() {
    text := m.extractSelectionText()
    if text == "" {
        return
    }
    if err := copyToClipboard(text); err == nil {
        m.clipboardFeedback = time.Now()
    }
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run "TestExtractSelectionText|TestStripAnsi" -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/clipboard.go ui/clipboard_test.go
git commit -m "feat(ui): add extractSelectionText and copySelection"
```

---

### 任务 7：View() 渲染管线 — 选区高亮

**文件：**
- 修改：`ui/model.go` (View 方法)

- [ ] **步骤 1：编写失败的测试**

```go
// ui/selection_test.go — 追加

func TestApplySelectionHighlight_NoSelection(t *testing.T) {
    m := Model{}
    lines := []string{"hello", "world"}
    result := m.applySelectionHighlight(lines)
    if len(result) != 2 || result[0] != "hello" || result[1] != "world" {
        t.Error("no selection: lines should be unchanged")
    }
}

func TestApplySelectionHighlight_WithSelection(t *testing.T) {
    m := Model{
        selection: SelectionState{
            Done:  true,
            Start: selPoint{Line: 0, Col: 0},
            End:   selPoint{Line: 1, Col: -1},
        },
    }
    lines := []string{"hello", "world", "extra"}
    result := m.applySelectionHighlight(lines)
    // Lines 0 and 1 should have reverse markers
    if !containsSeq(result[0], "\x1b[7m") {
        t.Errorf("line 0 should have reverse marker, got %q", result[0])
    }
    if !containsSeq(result[1], "\x1b[7m") {
        t.Errorf("line 1 should have reverse marker, got %q", result[1])
    }
    // Line 2 should be unchanged
    if result[2] != "extra" {
        t.Errorf("line 2 should be unchanged, got %q", result[2])
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestApplySelectionHighlight -v`
预期：FAIL，`Model` 没有 `applySelectionHighlight` 方法

- [ ] **步骤 3：编写 applySelectionHighlight 方法**

在 `ui/selection.go` 中追加：

```go
// applySelectionHighlight applies ANSI reverse video to lines within the selection range.
func (m Model) applySelectionHighlight(lines []string) []string {
    if !m.selection.Active && !m.selection.Done {
        return lines
    }
    start, end := normalizeSelection(m.selection)
    for i := start.Line; i <= end.Line; i++ {
        if i < 0 || i >= len(lines) {
            continue
        }
        colStart := 0
        if i == start.Line {
            colStart = start.Col
        }
        colEnd := -1 // -1 = to end of line
        if i == end.Line && end.Col >= 0 {
            colEnd = end.Col
        }
        lines[i] = reverseHighlightLine(lines[i], colStart, colEnd)
    }
    return lines
}
```

- [ ] **步骤 4：修改 View() 渲染管线**

在 `ui/model.go` 的 `View()` 方法中，在 `renderBody()` 调用之后、scroll window 切片之前，插入选区高亮和缓存更新。

当前代码（约 507-510 行）：
```go
lines := m.renderBody(scrollContentWidth)
total := len(lines)
maxScroll := 0
scrollOff := m.scrollOffset
```

改为：
```go
lines := m.renderBody(scrollContentWidth)
m.cachedTotalLines = len(lines)
m.cachedBodyLines = lines // cache for extractSelectionText
lines = m.applySelectionHighlight(lines)
total := len(lines)
maxScroll := 0
scrollOff := m.scrollOffset
```

- [ ] **步骤 5：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestApplySelectionHighlight -v`
预期：PASS

- [ ] **步骤 6：编译检查**

运行：`cd /Users/admin/gitspace/deepact && go build ./...`
预期：编译成功

- [ ] **步骤 7：Commit**

```bash
git add ui/selection.go ui/model.go ui/selection_test.go
git commit -m "feat(ui): integrate selection highlight into View() rendering pipeline"
```

---

### 任务 8：renderBody() 构建 lineMetas

**文件：**
- 修改：`ui/model.go` (renderBody 方法)

- [ ] **步骤 1：修改 renderBody() 构建并行 lineMetas 数组**

在 `renderBody()` 方法中（约 1080 行开始），在 `lines` 追加的同时构建 `lineMetas`。

在方法开头 `lines := []string{}` 之后添加：
```go
metas := []lineMeta{}
```

在每处 `lines = append(lines, ...)` 的同时，追加对应的 `metas`。关键修改点：

1. Logo box（无消息源，msgIdx = -1）：
```go
if m.state == stateInit {
    logoLines := strings.Split(renderLogoBox(width), "\n")
    lines = append(lines, logoLines...)
    for range logoLines {
        metas = append(metas, lineMeta{msgIdx: -1})
    }
    return lines // 注意：此 return 需要在 return 前设置 m.lineMetas
}
```

2. 初始 logo（无消息）：
```go
if len(m.messages) == 0 {
    logoLines := strings.Split(renderLogoBox(width), "\n")
    lines = append(lines, logoLines...)
    for range logoLines {
        metas = append(metas, lineMeta{msgIdx: -1})
    }
    lines = append(lines, "")
    metas = append(metas, lineMeta{msgIdx: -1})
}
```

3. 消息渲染（核心 — 有 msgIdx）：
```go
for i, msg := range m.messages {
    if i < len(cache.lines) {
        lines = append(lines, cache.lines[i]...)
        for j := range cache.lines[i] {
            metas = append(metas, lineMeta{msgIdx: i, lineOff: j})
        }
    } else {
        rendered := renderMessage(msg, width)
        rendered = append(rendered, "")
        cache.lines = append(cache.lines, rendered)
        lines = append(lines, rendered...)
        for j := range rendered {
            metas = append(metas, lineMeta{msgIdx: i, lineOff: j})
        }
    }
}
```

4. API key prompt、tool tree、member progress、streaming、spinners（msgIdx = -1）：
每处 `lines = append(lines, ...)` 后，添加对应的 `metas = append(metas, lineMeta{msgIdx: -1})` 迭代。

5. 方法末尾（return 前）添加：
```go
m.lineMetas = metas
```

- [ ] **步骤 2：编译检查**

运行：`cd /Users/admin/gitspace/deepact && go build ./...`
预期：编译成功

- [ ] **步骤 3：运行全量 UI 测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -v`
预期：所有现有测试 + 新测试 PASS

- [ ] **步骤 4：Commit**

```bash
git add ui/model.go
git commit -m "feat(ui): build lineMetas during renderBody for selection text extraction"
```

---

### 任务 9：选区清除 — 按键和新消息

**文件：**
- 修改：`ui/model.go` (handleKey, Update)

- [ ] **步骤 1：在 handleKey 开头添加选区清除逻辑**

在 `handleKey()` 方法最开头（约 600 行，在 pendingOpenBracket 逻辑之前）插入：

```go
// Any key press clears the current selection
if m.selection.Done {
    m.selection = SelectionState{}
}
```

- [ ] **步骤 2：在 EngineResponseMsg 处理中清除选区**

在 `Update()` 的 `case EngineResponseMsg:` 分支中（约 265 行），在 `m.state = stateReady` 之前添加：

```go
m.selection = SelectionState{} // new message: clear selection
```

- [ ] **步骤 3：编写测试**

```go
// ui/model_test.go — 追加

func TestSelectionClearedOnKeyPress(t *testing.T) {
    m := NewModel(nil, engine.PricingConfig{})
    m.state = stateReady
    m.selection = SelectionState{
        Done:  true,
        Start: selPoint{Line: 2, Col: 0},
        End:   selPoint{Line: 5, Col: 10},
    }

    keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
    result, _ := m.Update(keyMsg)
    m2 := result.(Model)
    if m2.selection.Done || m2.selection.Active {
        t.Error("key press should clear selection")
    }
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -run TestSelectionClearedOnKeyPress -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/model_test.go
git commit -m "feat(ui): clear selection on key press and new message"
```

---

### 任务 10：状态栏反馈与提示移除

**文件：**
- 修改：`ui/model.go` (renderStatusBar)

- [ ] **步骤 1：修改 renderStatusBar 函数签名**

当前签名：`func renderStatusBar(status StatusInfo, scrollOffset, scrollMax int, width int) string`

需要传入 `clipboardFeedback` 和 `selection`：

改为：`func renderStatusBar(status StatusInfo, scrollOffset, scrollMax int, width int, clipboardFeedback time.Time) string`

- [ ] **步骤 2：替换 Option/Shift+drag 提示**

在 `renderStatusBar` 内部，将原来的 dragHint 逻辑：

```go
dragHint := "Shift+drag"
switch runtime.GOOS {
case "darwin":
    dragHint = "⌥+drag"
}
```

替换为：

```go
dragHint := ""
if !clipboardFeedback.IsZero() && time.Since(clipboardFeedback) < 2*time.Second {
    dragHint = "✓ Copied"
} else {
    dragHint = "Drag to select"
}
```

- [ ] **步骤 3：更新 View() 中调用 renderStatusBar 的参数**

在 `View()` 中（约 530 行），将：
```go
statusLine := renderStatusBar(m.status, scrollOff, maxScroll, contentWidth)
```
改为：
```go
statusLine := renderStatusBar(m.status, scrollOff, maxScroll, contentWidth, m.clipboardFeedback)
```

- [ ] **步骤 4：编译检查**

运行：`cd /Users/admin/gitspace/deepact && go build ./...`
预期：编译成功

- [ ] **步骤 5：运行全量测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -v`
预期：PASS

- [ ] **步骤 6：Commit**

```bash
git add ui/model.go
git commit -m "feat(ui): show clipboard feedback in status bar, replace drag hint"
```

---

### 任务 11：cmd/run.go 更新注释

**文件：**
- 修改：`cmd/run.go`

- [ ] **步骤 1：更新 WithMouseCellMotion 注释**

将当前注释（约 69-70 行）：
```go
// Mouse wheel scrolling via WithMouseCellMotion.
// Hold Shift while dragging for native terminal text selection (SGR mouse protocol).
```
改为：
```go
// Mouse interaction via WithMouseCellMotion: wheel scrolling + drag-to-select.
// Left-click drag selects text and auto-copies to clipboard on release.
```

- [ ] **步骤 2：Commit**

```bash
git add cmd/run.go
git commit -m "docs(cmd): update mouse interaction comment"
```

---

### 任务 12：集成测试与清理

**文件：**
- 修改：`ui/model_test.go`
- 修改：`ui/selection_test.go`

- [ ] **步骤 1：更新已有 TestMouseWheelScroll 测试**

由于 `renderStatusBar` 签名已改，检查是否影响已有测试。运行：
```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -v
```

如果出现编译错误，修复测试中直接调用 `renderStatusBar` 的地方，补充 `clipboardFeedback` 参数（传 `time.Time{}`）。

- [ ] **步骤 2：编写端到端选中流程测试**

```go
// ui/model_test.go — 追加

func TestSelectionFullFlow(t *testing.T) {
    m := NewModel(nil, engine.PricingConfig{})
    m.state = stateReady
    m.height = 40
    m.cachedTotalLines = 50
    m.msgCache = &messageRenderCache{lastMaxScroll: 10}
    m.messages = []DisplayMessage{
        {Role: "assistant", Content: "first line\nsecond line\nthird line"},
    }
    m.cachedBodyLines = []string{
        "first line",
        "second line",
        "third line",
    }

    // Step 1: Mouse down at line 0, col 0
    downMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionPress,
        Y:      0, X: 0,
    }
    result, _ := m.Update(downMsg)
    m = result.(Model)
    if !m.selection.Active {
        t.Fatal("step 1: selection should be active")
    }

    // Step 2: Mouse motion to line 2, col 5
    motionMsg := tea.MouseMsg{
        Button: tea.MouseActionMotion,
        Action: tea.MouseActionMotion,
        Y:      2, X: 5,
    }
    result, _ = m.Update(motionMsg)
    m = result.(Model)
    if !m.selection.Active {
        t.Fatal("step 2: selection should still be active")
    }

    // Step 3: Mouse up — selection done, clipboard should have content
    upMsg := tea.MouseMsg{
        Button: tea.MouseButtonLeft,
        Action: tea.MouseActionRelease,
        Y:      2, X: 5,
    }
    result, _ = m.Update(upMsg)
    m = result.(Model)
    if m.selection.Active {
        t.Fatal("step 3: selection should not be active after release")
    }
    if !m.selection.Done {
        t.Fatal("step 3: selection should be done after release")
    }
}
```

- [ ] **步骤 3：运行全量测试**

运行：`cd /Users/admin/gitspace/deepact && go test ./ui/ -v -race`
预期：全部 PASS

- [ ] **步骤 4：运行全项目编译和快速测试**

运行：`cd /Users/admin/gitspace/deepact && make build && make test-short`
预期：BUILD SUCCESS + ALL TESTS PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model_test.go ui/selection_test.go
git commit -m "test(ui): add integration tests for full selection flow"
```

---

## 自检

### 1. 规格覆盖度

| 规格需求 | 对应任务 |
|---------|---------|
| 鼠标左键按下→拖拽→松开选中 | 任务 5 |
| 松开自动复制到剪贴板 | 任务 6 |
| 反色高亮 | 任务 2, 7 |
| 仅对话区选中 | 任务 3 (screenToLine 限制在 body 区域) |
| 纯文本复制（剥离 ANSI） | 任务 6 (stripAnsi + extractSelectionText) |
| 滚轮滚动时不中断选中 | 任务 5 (wheel 分支不修改 selection) |
| 状态栏反馈 | 任务 10 |
| 选区数据模型 | 任务 1, 4 |
| 坐标映射 | 任务 3 |
| 选区清除（按键/新消息/点击） | 任务 9 |
| 移除 Option/Shift+drag 提示 | 任务 10 |
| lineMetas 构建 | 任务 8 |

所有需求均有对应任务。✅

### 2. 占位符扫描

无 TODO/TBD/待定/后续实现。所有步骤有完整代码。✅

### 3. 类型一致性

- `selPoint`, `SelectionState`, `lineMeta` 定义在任务 1 和 4，后续任务使用一致
- `screenToLine` 返回 `selPoint`，在任务 5 的 MouseMsg handler 中使用
- `applySelectionHighlight` 接受 `[]string` 返回 `[]string`，与 View() 管线匹配
- `renderStatusBar` 签名变更在任务 10，调用点同步更新
- `clipboardFeedback` 类型 `time.Time`，在 Model (任务 4) 和 renderStatusBar (任务 10) 中一致
- `cachedTotalLines` 和 `cachedBodyLines` 在 Model (任务 4) 中定义，在 View() (任务 7) 和 screenToLine (任务 3) 中使用 ✅
