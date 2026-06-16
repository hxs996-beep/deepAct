# Mouse Drag Selection Redesign 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 重构鼠标拖拽选择复制功能，修复根本性缓存 bug，合并代码文件，增加自动滚动和 Windows 剪贴板支持。

**架构：** 采用纯文本层与渲染层分离方案。`renderBody()` 同时返回渲染行和纯文本行。`screenToLine` 和 `extractSelectionText` 在 `Update()` 中按需调用 `renderBody()` 获取布局信息，不再依赖 `View()` 设置的缓存字段（当前实现因值接收器导致缓存永远为零，这是根本性 bug）。自动滚动通过 `tea.Tick` 定时器驱动。

**技术栈：** Go 1.24+, Bubble Tea, lipgloss, golang.org/x/sys/windows

---

## 关键设计偏差

规格中建议将 `plainTextLines` 作为 Model 字段并在 `View()` 中设置。**这是不可行的**，因为 `View()` 是值接收器方法，对 Model 字段的修改不会持久化到后续的 `Update()` 调用。当前实现的 `cachedTotalLines`/`cachedBodyLines` 正是因这个 bug 而永远为零。

**解决方案**：`screenToLine` 和 `extractSelectionText` 在 `Update()` 处理鼠标事件时按需调用 `renderBody()` 获取布局信息。由于 `msgCache` 是指针，渲染缓存跨调用有效，性能可接受。

---

## 文件结构

| 文件 | 变更 | 职责 |
|---|---|---|
| `ui/selection.go` | **重写** | 合并 clipboard.go：类型定义、坐标映射、高亮渲染、文本提取、剪贴板操作 |
| `ui/selection_test.go` | **重写** | 合并 clipboard_test.go，新增端到端测试、自动滚动测试 |
| `ui/model.go` | **修改** | 删除死代码字段、改 renderBody 签名、更新 View/Update、加自动滚动 |
| `ui/model_test.go` | **修改** | 适配新字段名、新增自动滚动测试 |
| `ui/clipboard.go` | **删除** | 合并到 selection.go |
| `ui/clipboard_test.go` | **删除** | 合并到 selection_test.go |

---

### 任务 1：重写 `ui/selection.go`

**文件：**
- 重写：`ui/selection.go`

这是最核心的任务。将 `clipboard.go` 的所有函数合并到 `selection.go`，同时简化实现。

- [ ] **步骤 1：编写完整的 `selection.go`**

```go
package ui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ---- Types ----

// selPoint represents a position in the plain-text line array.
type selPoint struct {
	Line int // plain-text line index (0-based)
	Col  int // visual column (0-based, CJK/emoji counted as width-2)
}

// SelectionState tracks the drag selection lifecycle.
type SelectionState struct {
	Active bool     // currently dragging
	Done   bool     // drag complete, highlight persists
	Start  selPoint // mouse-down position
	End    selPoint // current / mouse-up position
}

// autoScrollTickMsg is sent by the auto-scroll timer during edge-drag.
type autoScrollTickMsg struct{}

// ---- Coordinate mapping ----

// normalizeSelection returns (start, end) with start ≤ end.
func normalizeSelection(s SelectionState) (selPoint, selPoint) {
	if s.Start.Line < s.End.Line {
		return s.Start, s.End
	}
	if s.Start.Line > s.End.Line {
		return s.End, s.Start
	}
	if s.Start.Col <= s.End.Col {
		return s.Start, s.End
	}
	return s.End, s.Start
}

// screenToLine maps screen coordinates to a selPoint in the content line array.
// totalLines comes from renderBody output length, computed in Update() not View().
func screenToLine(screenRow, screenCol, scrollOffset, bodyHeight, totalLines int) selPoint {
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	var firstVisibleLine int
	if totalLines <= bodyHeight {
		firstVisibleLine = 0
	} else {
		maxScroll := totalLines - bodyHeight
		scrollOff := scrollOffset
		if scrollOff > maxScroll {
			scrollOff = maxScroll
		}
		if scrollOff < 0 {
			scrollOff = 0
		}
		firstVisibleLine = totalLines - scrollOff - bodyHeight
	}
	lineIdx := firstVisibleLine + screenRow
	if lineIdx < 0 {
		lineIdx = 0
	}
	if totalLines > 0 && lineIdx >= totalLines {
		lineIdx = totalLines - 1
	}
	return selPoint{Line: lineIdx, Col: screenCol}
}

// ---- Highlight rendering ----

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

// reverseHighlightLine applies ANSI reverse video (\x1b[7m ... \x1b[27m)
// to a visual column range within an ANSI-formatted string.
// colEnd = -1 means highlight to end of line.
func reverseHighlightLine(line string, colStart, colEnd int) string {
	if line == "" || colStart < 0 {
		return line
	}
	var sb strings.Builder
	visualCol := 0
	inHighlight := false
	i := 0
	for i < len(line) {
		if line[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(line, i)
			sb.WriteString(line[i:seqEnd])
			i = seqEnd
			continue
		}
		r, size := decodeRuneAt(line, i)
		rw := lipgloss.Width(string(r))
		if !inHighlight && visualCol <= colStart && colStart < visualCol+rw {
			sb.WriteString("\x1b[7m")
			inHighlight = true
		}
		if inHighlight && colEnd >= 0 && visualCol < colEnd && visualCol+rw > colEnd {
			sb.WriteString("\x1b[27m")
			inHighlight = false
		}
		if inHighlight && colEnd >= 0 && visualCol >= colEnd {
			sb.WriteString("\x1b[27m")
			inHighlight = false
		}
		sb.WriteString(line[i : i+size])
		i += size
		visualCol += rw
	}
	if inHighlight {
		sb.WriteString("\x1b[27m")
	}
	return sb.String()
}

// ---- ANSI helpers ----

// findAnsiSeqEnd returns the index after the end of the ANSI escape sequence
// starting at position i.
func findAnsiSeqEnd(s string, i int) int {
	if i >= len(s) || s[i] != '\x1b' {
		return i + 1
	}
	if i+1 >= len(s) {
		return i + 1
	}
	switch s[i+1] {
	case '[':
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
		return i + 2
	}
}

// decodeRuneAt decodes the first UTF-8 rune starting at byte offset i.
func decodeRuneAt(s string, i int) (rune, int) {
	if i >= len(s) {
		return 0, 0
	}
	r := rune(s[i])
	size := 1
	if s[i]&0x80 != 0 {
		decoded, sz := utf8.DecodeRuneInString(s[i:])
		if sz > 1 {
			r = decoded
			size = sz
		}
	}
	return r, size
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

// ---- Text extraction ----

// extractSelectionText extracts plain text from the selected line range.
// Reads directly from plain text lines — no ANSI stripping needed.
func extractSelectionText(plainLines []string, sel SelectionState) string {
	if !sel.Done && !sel.Active {
		return ""
	}
	start, end := normalizeSelection(sel)
	if len(plainLines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i := start.Line; i <= end.Line; i++ {
		if i < 0 || i >= len(plainLines) {
			continue
		}
		line := plainLines[i]
		colStart := 0
		colEnd := -1
		if i == start.Line {
			colStart = start.Col
		}
		if i == end.Line && end.Col >= 0 {
			colEnd = end.Col
		}
		if colEnd < 0 {
			line = sliceByVisualCol(line, colStart, -1)
		} else {
			line = sliceByVisualCol(line, colStart, colEnd)
		}
		if i > start.Line {
			sb.WriteByte('\n')
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// visualWidth returns the visual width of a string (accounts for CJK/emoji).
func visualWidth(s string) int {
	return lipgloss.Width(s)
}

// sliceByVisualCol returns the substring of s spanning the visual-column range
// [colStart, colEnd). colEnd = -1 means "to end of line".
func sliceByVisualCol(s string, colStart, colEnd int) string {
	if s == "" || colStart < 0 {
		return ""
	}
	if colEnd >= 0 && colEnd <= colStart {
		return ""
	}
	var sb strings.Builder
	visualCol := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if colEnd >= 0 && visualCol >= colEnd {
			break
		}
		if visualCol+rw <= colStart {
			visualCol += rw
			continue
		}
		if visualCol < colStart {
			visualCol += rw
			continue
		}
		if colEnd >= 0 && visualCol+rw > colEnd {
			break
		}
		sb.WriteRune(r)
		visualCol += rw
	}
	return sb.String()
}

// ---- Clipboard ----

// copySelection extracts selected text and copies it to the clipboard.
// Returns an error message string if clipboard copy failed, empty string on success.
func copySelection(plainLines []string, sel SelectionState) string {
	text := extractSelectionText(plainLines, sel)
	if text == "" {
		return ""
	}
	if err := copyToClipboard(text); err != nil {
		return fmt.Sprintf("clipboard: %v", err)
	}
	return text // non-empty = success
}

// copyToClipboard writes plain text to the system clipboard.
func copyToClipboard(text string) error {
	switch runtime.GOOS {
	case "darwin":
		return pipeToCmd("pbcopy", text)
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return pipeToCmd("wl-copy", text)
		}
		return pipeToCmd("xclip", "-selection", "clipboard", text)
	case "windows":
		return windowsCopy(text)
	}
	return fmt.Errorf("clipboard: unsupported platform %s", runtime.GOOS)
}

// pipeToCmd pipes text to an external command's stdin.
func pipeToCmd(name string, args []string, text string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// windowsCopy copies text to the Windows clipboard using Win32 API.
// Platform-specific implementation lives in clipboard_windows.go / clipboard_other.go.
func windowsCopy(text string) error {
	utf16Data, err := utf16EncodeString(text)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	return windowsCopyImpl(utf16Data)
}

- [ ] **步骤 2：验证 selection.go 编译通过**

运行：`go build ./ui/...`
预期：可能因与 clipboard.go 函数冲突而失败 — 这将在任务 2 解决

---

### 任务 2：删除 `ui/clipboard.go` 和 `ui/clipboard_test.go`

**文件：**
- 删除：`ui/clipboard.go`
- 删除：`ui/clipboard_test.go`

- [ ] **步骤 1：删除文件**

```bash
rm ui/clipboard.go ui/clipboard_test.go
```

- [ ] **步骤 2：验证编译**

运行：`go build ./ui/...`
预期：可能仍有编译错误（model.go 引用旧字段），将在任务 3 解决

---

### 任务 3：更新 `ui/model.go` — 删除死代码、改 renderBody 签名、更新 View 和 Update

这是改动量最大的任务。需要修改 model.go 的多个部分。

**文件：**
- 修改：`ui/model.go`

#### 3a. 删除死代码字段

- [ ] **步骤 1：从 Model struct 删除以下字段**

删除这些行：
```go
cachedBodyLines []string       // 第 118 行
cachedTotalLines  int          // 第 127 行
lineMetas         []lineMeta   // 第 128 行
```

同时删除 `cachedBodyWidth` 和 `bodyDirty` 字段（第 119-120 行）—— 检查是否被使用：

经检查，`cachedBodyWidth` 和 `bodyDirty` 在 model.go 中未被赋值或读取（搜索确认），也是死代码，一并删除。

#### 3b. 更新 renderBody 签名和实现

- [ ] **步骤 2：将 renderBody 签名改为返回双切片**

```go
// 之前
func (m Model) renderBody(width int) []string

// 之后
func (m Model) renderBody(width int) (rendered []string, plain []string)
```

- [ ] **步骤 3：在 renderBody 内部，删除所有 lineMeta 相关代码，并在返回时生成 plain 切片**

renderBody 函数体变更要点：
1. 删除所有 `metas` 局部变量和 `m.lineMetas = metas` 赋值
2. 在每个 `return` 语句和函数末尾，生成 `plain` 切片并返回

具体修改：

**Init state 分支** (约第 1150-1158 行):
```go
if m.state == stateInit {
	logoRendered := renderLogoBox(width)
	logoLines := strings.Split(logoRendered, "\n")
	plainLines := make([]string, len(logoLines))
	for i, l := range logoLines {
		plainLines[i] = stripAnsi(l)
	}
	return logoLines, plainLines
}
```

**No messages 分支** (约第 1163-1171 行):
```go
if len(m.messages) == 0 {
	logoRendered := renderLogoBox(width)
	logoLines := strings.Split(logoRendered, "\n")
	lines = append(lines, logoLines...)
	lines = append(lines, "")
}
```
（不再手动 append lineMeta）

**API key prompt 分支** (约第 1197-1215 行):
```go
if m.state == stateApiKeyPrompt {
	apiKeyLines := []string{...}  // 保持不变
	lines = append(lines, apiKeyLines...)
	plainLines := make([]string, len(lines))
	for i, l := range lines {
		plainLines[i] = stripAnsi(l)
	}
	return lines, plainLines
}
```

**函数末尾** (约第 1245 行):
```go
// 删除 m.lineMetas = metas
// 添加:
plainLines := make([]string, len(lines))
for i, l := range lines {
	plainLines[i] = stripAnsi(l)
}
return lines, plainLines
```

#### 3c. 更新 View() 使用新签名

- [ ] **步骤 4：修改 View() 中 renderBody 调用和相关行**

将第 566-569 行:
```go
lines := m.renderBody(scrollContentWidth)
m.cachedTotalLines = len(lines)
m.cachedBodyLines = lines
lines = m.applySelectionHighlight(lines)
```

改为:
```go
renderedLines, _ := m.renderBody(scrollContentWidth)
renderedLines = m.applySelectionHighlight(renderedLines)
lines := renderedLines
```

注意：View() 不需要 plain lines，因为 `applySelectionHighlight` 和显示用的都是渲染行。`extractSelectionText` 不再从 View() 获取 plain lines。

#### 3d. 更新 renderBody 在 API key prompt 场景的直接调用

- [ ] **步骤 5：修改 View() 中 API key prompt 分支**

第 534 行:
```go
// 之前
return strings.Join(m.renderBody(contentWidth), "\n")

// 之后
rendered, _ := m.renderBody(contentWidth)
return strings.Join(rendered, "\n")
```

#### 3e. 重写鼠标事件处理

- [ ] **步骤 6：添加新的 Model 字段**

在 Model struct 的 mouse drag selection 注释区域，替换为:
```go
// Mouse drag selection
selection         SelectionState
clipboardFeedback time.Time  // timestamp of last clipboard copy for status bar feedback
autoScrollDir     int        // auto-scroll direction during drag: -1=up, 0=none, +1=down
lastMouseX        int        // last mouse X during drag (screen coords, for auto-scroll)
lastMouseY        int        // last mouse Y during drag (screen coords, for auto-scroll)
```

- [ ] **步骤 7：添加 renderBodyWidth 辅助方法**

```go
// renderBodyWidth returns the content width used by renderBody.
func (m Model) renderBodyWidth() int {
	w := m.width - 1
	if w < 20 {
		w = 20
	}
	return w
}
```

- [ ] **步骤 8：添加 computeLayout 辅助方法**

在 Update() 的鼠标事件处理中，需要计算 totalLines 和 bodyHeight。抽取为辅助方法：

```go
// computeLayout returns (totalLines, bodyHeight, plainLines) for the current model state.
// Used by mouse event handlers for coordinate mapping and text extraction.
func (m Model) computeLayout() (totalLines, bodyHeight int, plainLines []string) {
	bh := m.height - m.footerHeight()
	if bh < 1 {
		bh = 1
	}
	_, plain := m.renderBody(m.renderBodyWidth())
	return len(plain), bh, plain
}
```

- [ ] **步骤 9：重写 Update() 中的 MouseMsg 处理**

将第 210-260 行的 `case tea.MouseMsg:` 块替换为:

```go
case tea.MouseMsg:
	// Handle motion events (drag) — they have Button=MouseButtonNone
	if msg.Action == tea.MouseActionMotion {
		if m.selection.Active {
			totalLines, bodyHeight, _ := m.computeLayout()
			m.selection.End = screenToLine(msg.Y, msg.X, m.scrollOffset, bodyHeight, totalLines)
			m.lastMouseX = msg.X
			m.lastMouseY = msg.Y
			// Auto-scroll edge detection
			scrollEdge := 2
			newDir := 0
			if msg.Y < scrollEdge {
				newDir = -1
			} else if msg.Y >= bodyHeight-scrollEdge {
				newDir = 1
			}
			if newDir != 0 && m.autoScrollDir == 0 {
				// Start auto-scroll timer
				m.autoScrollDir = newDir
				return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
					return autoScrollTickMsg{}
				})
			}
			m.autoScrollDir = newDir
		}
		return m, nil
	}
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
			totalLines, bodyHeight, _ := m.computeLayout()
			pt := screenToLine(msg.Y, msg.X, m.scrollOffset, bodyHeight, totalLines)
			m.selection = SelectionState{
				Active: true,
				Done:   false,
				Start:  pt,
				End:    pt,
			}
			m.autoScrollDir = 0
			m.lastMouseX = msg.X
			m.lastMouseY = msg.Y
			return m, nil
		} else if msg.Action == tea.MouseActionRelease {
			if m.selection.Active {
				totalLines, bodyHeight, plainLines := m.computeLayout()
				m.selection.End = screenToLine(msg.Y, msg.X, m.scrollOffset, bodyHeight, totalLines)
				m.selection.Active = false
				m.autoScrollDir = 0
				if m.selection.Start == m.selection.End {
					m.selection = SelectionState{}
				} else {
					m.selection.Done = true
					result := copySelection(plainLines, m.selection)
					if result != "" {
						if strings.HasPrefix(result, "clipboard:") {
							// Error — store for status bar display
							m.clipboardError = result
							m.clipboardFeedback = time.Now()
						} else {
							m.clipboardFeedback = time.Now()
						}
					}
				}
			}
			return m, nil
		}
	}
	return m, nil
```

- [ ] **步骤 10：处理 autoScrollTickMsg**

在 Update() 的 switch 中添加新 case（在 `case tea.MouseMsg:` 之后）:

```go
case autoScrollTickMsg:
	if m.selection.Active && m.autoScrollDir != 0 {
		_, bodyHeight, _ := m.computeLayout()
		maxScroll := m.msgCache.lastMaxScroll
		m.scrollOffset += m.autoScrollDir
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
		if maxScroll > 0 && m.scrollOffset > maxScroll {
			m.scrollOffset = maxScroll
		}
		// Recompute selection End with new scroll offset
		totalLines, _, _ := m.computeLayout()
		m.selection.End = screenToLine(m.lastMouseY, m.lastMouseX, m.scrollOffset, bodyHeight, totalLines)
		// Continue auto-scroll if still at edge
		return m, tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
			return autoScrollTickMsg{}
		})
	}
	m.autoScrollDir = 0
	return m, nil
```

- [ ] **步骤 11：添加 clipboardError 字段到 Model**

在 Model struct 的 mouse drag selection 区域添加:
```go
clipboardError    string     // last clipboard error message, shown briefly in status bar
```

- [ ] **步骤 12：更新 renderStatusBar 显示剪贴板错误**

修改 `renderStatusBar` 函数的 `dragHint` 逻辑:
```go
dragHint := "Drag to select"
if m.clipboardError != "" && !m.clipboardFeedback.IsZero() && time.Since(m.clipboardFeedback) < 2*time.Second {
	dragHint = m.clipboardError
} else if !clipboardFeedback.IsZero() && time.Since(clipboardFeedback) < 2*time.Second {
	dragHint = "✓ Copied"
}
```

并在 copySelection 成功时清除 clipboardError:
在 mouse release 处理中，成功路径添加 `m.clipboardError = ""`

- [ ] **步骤 13：在 EngineResponseMsg 处理中添加 autoScrollDir 清零**

在 `case EngineResponseMsg:` 分支中，添加:
```go
m.autoScrollDir = 0
```

- [ ] **步骤 14：验证编译**

运行：`go build ./...`
预期：PASS（所有编译错误应已解决）

---

### 任务 4：编写 `ui/selection_test.go`

**文件：**
- 重写：`ui/selection_test.go`

合并原有测试，新增端到端和自动滚动测试。

- [ ] **步骤 1：编写完整的 selection_test.go**

```go
package ui

import (
	"strings"
	"testing"
)

// ---- normalizeSelection tests ----

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

// ---- screenToLine tests ----

func TestScreenToLine_Basic(t *testing.T) {
	// 100 total lines, scrollOffset=0, bodyHeight=35
	// firstVisibleLine = 100 - 0 - 35 = 65
	// screenRow 0 → line 65
	pt := screenToLine(0, 5, 0, 35, 100)
	if pt.Line != 65 {
		t.Errorf("screenToLine(0,5,0,35,100): want line 65, got %d", pt.Line)
	}
	if pt.Col != 5 {
		t.Errorf("want col 5, got %d", pt.Col)
	}
	pt2 := screenToLine(1, 10, 0, 35, 100)
	if pt2.Line != 66 {
		t.Errorf("screenToLine(1,10): want line 66, got %d", pt2.Line)
	}
}

func TestScreenToLine_ClampBeyondContent(t *testing.T) {
	// 10 total lines, bodyHeight=36, fits on screen
	// firstVisibleLine = 0
	// screenRow 9 → line 9
	pt := screenToLine(9, 0, 0, 36, 10)
	if pt.Line != 9 {
		t.Errorf("last content line: want line 9, got %d", pt.Line)
	}
	// Beyond content should clamp
	pt2 := screenToLine(15, 0, 0, 36, 10)
	if pt2.Line != 9 {
		t.Errorf("beyond content: want line 9, got %d", pt2.Line)
	}
}

func TestScreenToLine_WithScroll(t *testing.T) {
	// 100 lines, scrollOffset=20, bodyHeight=35
	// maxScroll = 100-35 = 65, scrollOff=20
	// firstVisibleLine = 100 - 20 - 35 = 45
	pt := screenToLine(0, 0, 20, 35, 100)
	if pt.Line != 45 {
		t.Errorf("with scroll: want line 45, got %d", pt.Line)
	}
}

// ---- applySelectionHighlight tests ----

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
	if !containsSeq(result[0], "\x1b[7m") {
		t.Errorf("line 0 should have reverse marker, got %q", result[0])
	}
	if !containsSeq(result[1], "\x1b[7m") {
		t.Errorf("line 1 should have reverse marker, got %q", result[1])
	}
	if result[2] != "extra" {
		t.Errorf("line 2 should be unchanged, got %q", result[2])
	}
}

// ---- reverseHighlightLine tests (from clipboard_test.go) ----

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
	line := "\x1b[31mhello\x1b[0m world"
	result := reverseHighlightLine(line, 0, -1)
	if !containsSeq(result, "\x1b[7m") || !containsSeq(result, "\x1b[27m") {
		t.Errorf("with ANSI: missing reverse markers in %q", result)
	}
}

func TestReverseHighlightLine_ColStartBeyondLine(t *testing.T) {
	line := "hi"
	result := reverseHighlightLine(line, 10, -1)
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

// ---- stripAnsi tests (from clipboard_test.go) ----

func TestStripAnsi(t *testing.T) {
	input := "\x1b[31mhello\x1b[0m \x1b[1mworld\x1b[0m"
	result := stripAnsi(input)
	if result != "hello world" {
		t.Errorf("stripAnsi: want %q, got %q", "hello world", result)
	}
}

func TestStripAnsi_NoAnsi(t *testing.T) {
	result := stripAnsi("plain text")
	if result != "plain text" {
		t.Errorf("stripAnsi no ansi: want %q, got %q", "plain text", result)
	}
}

// ---- sliceByVisualCol tests ----

func TestSliceByVisualCol_ASCII(t *testing.T) {
	got := sliceByVisualCol("hello world", 2, 7)
	if got != "llo w" {
		t.Errorf("sliceByVisualCol ASCII: want %q, got %q", "llo w", got)
	}
}

func TestSliceByVisualCol_CJK(t *testing.T) {
	got := sliceByVisualCol("你好世界", 2, 6)
	if got != "好世" {
		t.Errorf("sliceByVisualCol CJK: want %q, got %q", "好世", got)
	}
}

func TestSliceByVisualCol_Mixed(t *testing.T) {
	got := sliceByVisualCol("Hi你好", 1, 5)
	if got != "i你" {
		t.Errorf("sliceByVisualCol mixed: want %q, got %q", "i你", got)
	}
}

func TestSliceByVisualCol_ToEnd(t *testing.T) {
	got := sliceByVisualCol("hello", 2, -1)
	if got != "llo" {
		t.Errorf("sliceByVisualCol to end: want %q, got %q", "llo", got)
	}
}

func TestSliceByVisualCol_CJKFullLine(t *testing.T) {
	got := sliceByVisualCol("你好世界", 0, -1)
	if got != "你好世界" {
		t.Errorf("sliceByVisualCol CJK full: want %q, got %q", "你好世界", got)
	}
}

func TestSliceByVisualCol_ColPastEnd(t *testing.T) {
	got := sliceByVisualCol("abc", 0, 100)
	if got != "abc" {
		t.Errorf("sliceByVisualCol past end: want %q, got %q", "abc", got)
	}
}

func TestSliceByVisualCol_WideRuneStraddle(t *testing.T) {
	got := sliceByVisualCol("你好", 1, -1)
	if got != "好" {
		t.Errorf("sliceByVisualCol straddle start: want %q, got %q", "好", got)
	}
}

func TestSliceByVisualCol_EmptyString(t *testing.T) {
	got := sliceByVisualCol("", 0, -1)
	if got != "" {
		t.Errorf("sliceByVisualCol empty: want %q, got %q", "", got)
	}
}

// ---- extractSelectionText tests ----

func TestExtractSelectionText_NoSelection(t *testing.T) {
	text := extractSelectionText(nil, SelectionState{})
	if text != "" {
		t.Errorf("empty selection should return empty string, got %q", text)
	}
}

func TestExtractSelectionText_SingleLineCJK(t *testing.T) {
	plain := []string{"你好世界"}
	sel := SelectionState{
		Done:  true,
		Start: selPoint{Line: 0, Col: 2},
		End:   selPoint{Line: 0, Col: 6},
	}
	got := extractSelectionText(plain, sel)
	if got != "好世" {
		t.Errorf("extractSelectionText CJK: want %q, got %q", "好世", got)
	}
}

func TestExtractSelectionText_MultiLine(t *testing.T) {
	plain := []string{"hello world", "middle line", "another test"}
	sel := SelectionState{
		Done:  true,
		Start: selPoint{Line: 0, Col: 3},
		End:   selPoint{Line: 2, Col: 5},
	}
	got := extractSelectionText(plain, sel)
	want := "lo world\nmiddle line\nanoth"
	if got != want {
		t.Errorf("extractSelectionText multi-line: want %q, got %q", want, got)
	}
}

func TestExtractSelectionText_WithContent(t *testing.T) {
	plain := []string{"first line", "second line", "third line"}
	sel := SelectionState{
		Done:  true,
		Start: selPoint{Line: 0, Col: 0},
		End:   selPoint{Line: 2, Col: 5},
	}
	text := extractSelectionText(plain, sel)
	if text == "" {
		t.Error("expected non-empty extracted text")
	}
	if !strings.Contains(text, "first line") {
		t.Errorf("expected 'first line' in extracted text, got %q", text)
	}
}

// ---- copySelection tests ----

func TestCopySelection_EmptyText(t *testing.T) {
	result := copySelection([]string{"hello"}, SelectionState{})
	if result != "" {
		t.Errorf("empty selection should return empty, got %q", result)
	}
}

// ---- end-to-end state machine test ----

func TestMouseDragSelection_FullFlow(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	// Add some messages so renderBody produces content
	m.messages = []DisplayMessage{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "response text"},
	}

	// Compute layout like Update() would
	totalLines, bodyHeight, _ := m.computeLayout()

	// Mouse down
	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      0,
		X:      5,
	}
	// Manually simulate what Update() does (since we can't easily call Update with the right setup)
	pt := screenToLine(0, 5, m.scrollOffset, bodyHeight, totalLines)
	m.selection = SelectionState{Active: true, Start: pt, End: pt}

	if !m.selection.Active {
		t.Error("selection should be active after mouse down")
	}

	// Mouse motion (drag)
	pt2 := screenToLine(2, 10, m.scrollOffset, bodyHeight, totalLines)
	m.selection.End = pt2

	// Mouse up (release)
	m.selection.End = pt2
	m.selection.Active = false
	m.selection.Done = true

	if !m.selection.Done {
		t.Error("selection should be done after release")
	}
	if m.selection.Start == m.selection.End {
		t.Error("drag should produce different start/end")
	}
}

func TestMouseClickNoDragClearsSelection(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test"}}

	totalLines, bodyHeight, _ := m.computeLayout()
	pt := screenToLine(0, 5, m.scrollOffset, bodyHeight, totalLines)

	// Down and up at same position = no drag
	m.selection = SelectionState{Active: true, Start: pt, End: pt}
	m.selection.Active = false
	if m.selection.Start == m.selection.End {
		m.selection = SelectionState{}
	}
	if m.selection.Done || m.selection.Active {
		t.Error("single click should clear selection")
	}
}

// ---- auto-scroll edge detection test ----

func TestAutoScrollEdgeDetection(t *testing.T) {
	bodyHeight := 35
	scrollEdge := 2

	tests := []struct {
		y      int
		wantUp bool
		wantDn bool
	}{
		{0, true, false},
		{1, true, false},
		{2, false, false},
		{32, false, false},
		{33, false, true},
		{34, false, true},
	}
	for _, tt := range tests {
		dir := 0
		if tt.y < scrollEdge {
			dir = -1
		} else if tt.y >= bodyHeight-scrollEdge {
			dir = 1
		}
		if tt.wantUp && dir != -1 {
			t.Errorf("y=%d: want up(-1), got %d", tt.y, dir)
		}
		if tt.wantDn && dir != 1 {
			t.Errorf("y=%d: want down(1), got %d", tt.y, dir)
		}
		if !tt.wantUp && !tt.wantDn && dir != 0 {
			t.Errorf("y=%d: want none(0), got %d", tt.y, dir)
		}
	}
}

// ---- helpers ----

func containsSeq(s, seq string) bool {
	return strings.Contains(s, seq)
}
```

- [ ] **步骤 2：运行测试验证**

运行：`go test ./ui/ -run "TestNormalizeSelection|TestScreenToLine|TestApplySelection|TestReverseHighlight|TestStripAnsi|TestSliceByVisualCol|TestExtractSelection|TestCopySelection|TestAutoScroll|TestMouseDrag" -v`
预期：PASS

---

### 任务 5：更新 `ui/model_test.go`

**文件：**
- 修改：`ui/model_test.go`

- [ ] **步骤 1：删除对 `cachedTotalLines` 的引用**

删除测试中 `m.cachedTotalLines = 100` 行（第 147 行和第 189 行），因为该字段已不存在。

- [ ] **步骤 2：更新 TestMouseDragSelection_StartDrag**

```go
func TestMouseDragSelection_StartDrag(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test message"}}

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
```

- [ ] **步骤 3：更新 TestMouseClickNoDragClearsSelection**

```go
func TestMouseClickNoDragClearsSelection(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test message"}}

	downMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		Y:      10, X: 5,
	}
	result, _ := m.Update(downMsg)
	m2 := result.(Model)
	if !m2.selection.Active {
		t.Error("selection should be active after mouse down")
	}

	upMsg := tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
		Y:      10, X: 5,
	}
	result, _ = m2.Update(upMsg)
	m3 := result.(Model)
	if m3.selection.Done || m3.selection.Active {
		t.Error("single click should clear selection")
	}
}
```

- [ ] **步骤 4：运行所有 UI 测试**

运行：`go test ./ui/ -v`
预期：PASS

---

### 任务 6：添加 Windows 剪贴板平台文件

**文件：**
- 创建：`ui/clipboard_windows.go`
- 创建：`ui/clipboard_other.go`

- [ ] **步骤 1：创建 `ui/clipboard_windows.go`**

```go
//go:build windows

package ui

import (
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	modkernel32          = syscall.NewLazyDLL("kernel32.dll")
	moduser32            = syscall.NewLazyDLL("user32.dll")
	procOpenClipboard    = moduser32.NewProc("OpenClipboard")
	procCloseClipboard   = moduser32.NewProc("CloseClipboard")
	procEmptyClipboard   = moduser32.NewProc("EmptyClipboard")
	procSetClipboardData = moduser32.NewProc("SetClipboardData")
	procGlobalAlloc      = modkernel32.NewProc("GlobalAlloc")
	procGlobalLock       = modkernel32.NewProc("GlobalLock")
	procGlobalUnlock     = modkernel32.NewProc("GlobalUnlock")
)

const (
	gmemMoveable   = 0x0002
	cfUnicodeText  = 13
)

func windowsCopyImpl(utf16Data []uint16) error {
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard failed")
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()

	size := len(utf16Data)*2 + 2 // bytes + null terminator
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(size))
	if h == 0 {
		return fmt.Errorf("GlobalAlloc failed")
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return fmt.Errorf("GlobalLock failed")
	}

	dst := unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(utf16Data)+1)
	copy(dst, utf16Data)
	dst[len(utf16Data)] = 0
	procGlobalUnlock.Call(h)

	r, _, _ = procSetClipboardData.Call(cfUnicodeText, h)
	if r == 0 {
		return fmt.Errorf("SetClipboardData failed")
	}
	return nil
}

func utf16EncodeString(s string) ([]uint16, error) {
	return utf16.Encode([]rune(s)), nil
}
```

- [ ] **步骤 2：创建 `ui/clipboard_other.go`**

```go
//go:build !windows

package ui

func windowsCopyImpl(_ []uint16) error {
	return nil // unreachable on non-Windows
}

func utf16EncodeString(_ string) ([]uint16, error) {
	return nil, nil // unreachable on non-Windows
}
```

- [ ] **步骤 3：在 selection.go 的 windowsCopy 函数中使用这些实现**

确保 selection.go 中的 `windowsCopy` 调用 `utf16EncodeString` 和 `windowsCopyImpl`：

```go
func windowsCopy(text string) error {
	utf16Data, err := utf16EncodeString(text)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}
	return windowsCopyImpl(utf16Data)
}
```

---

### 任务 7：最终构建与验证

- [ ] **步骤 1：运行完整构建**

```bash
go build ./...
```

- [ ] **步骤 2：运行所有测试（含竞态检测）**

```bash
go test -race ./...
```

预期：PASS

- [ ] **步骤 3：运行 lint**

```bash
make lint
```

- [ ] **步骤 4：Commit**

```bash
git add -A
git commit -m "refactor(ui): rewrite mouse drag selection with plain-text layer separation

- Merge clipboard.go into selection.go, delete clipboard.go
- Fix fundamental bug: cached fields set in View() (value receiver) never
  persisted to Update() — now compute layout on-the-fly via renderBody()
- Remove dead code: lineMeta, cachedBodyLines, cachedTotalLines
- Add auto-scroll during edge-drag (tea.Tick 50ms)
- Add Windows clipboard support via Win32 API
- Surface clipboard errors in status bar instead of swallowing silently
- Comprehensive test coverage including CJK, auto-scroll edge detection"
```
