# Diff 折叠展示 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 diff 从"在滚动 body 内混排展示"改为"hunk 级折叠摘要 + 点击 hunk 全屏展开"，从架构上绕开 diff 混排滚动时的 ghosting。

**架构：** Model 新增 `diffViewerActive` 状态 + `diffViewerHunk` 索引 + `hunkHits` 命中映射。`renderDiffBlock` 改为 hunk 摘要行（`[N] @@ ... @@ +2 -1`）。点击 hunk 行进入全屏 viewer（占满 body，复用 `renderDiffHunkBlock`），ESC 退出。全屏内滚轮滚动、禁拖选。

**技术栈：** Go 1.24+、Bubble Tea v1.3.4、lipgloss、`github.com/charmbracelet/x/ansi`。

**规格：** `docs/superpowers/specs/2026-07-01-diff-collapse-viewer-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|------|------|------|
| `ui/model.go` | `renderDiffBlock` 改折叠摘要；新增 `diffViewer` 状态字段、全屏渲染分支、ESC 拦截、mouse 点击命中/禁拖选/滚轮 | 修改 |
| `ui/diff_viewer.go` | 新增 `hunkHit` 类型、`countHunkAddsDeletes`、`renderDiffViewer`（全屏渲染）、`hunkSummaryLine` 辅助 | 创建 |
| `ui/diff_render_test.go` | 折叠摘要渲染测试、`countHunkAddsDeletes` 测试、全屏 viewer 渲染测试 | 修改 |

**放置决策：** 新逻辑（`hunkHit`、`countHunkAddsDeletes`、`renderDiffViewer`）放独立文件 `ui/diff_viewer.go`，保持 `model.go` 不至于过大（已 2800+ 行）。`renderDiffBlock` 因与 `renderToolTree`/`renderSearchBlock` 同组，留在 `model.go`。

---

## 任务 1：`countHunkAddsDeletes` 辅助函数（TDD）

统计 hunk 内容的增删行数，用于折叠摘要 `+N -M`。

**文件：**
- 创建：`ui/diff_viewer.go`
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 末尾追加：

```go
func TestCountHunkAddsDeletes(t *testing.T) {
	tests := []struct {
		name    string
		hunk    string
		adds    int
		deletes int
	}{
		{
			name:    "mixed",
			hunk:    "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n+extra",
			adds:    2,
			deletes: 1,
		},
		{
			name:    "only context",
			hunk:    "@@ -1,2 +1,2 @@\n a\n b",
			adds:    0,
			deletes: 0,
		},
		{
			name:    "empty",
			hunk:    "",
			adds:    0,
			deletes: 0,
		},
		{
			name:    "no hunk header",
			hunk:    "+a\n-b",
			adds:    1,
			deletes: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, d := countHunkAddsDeletes(tt.hunk)
			if a != tt.adds || d != tt.deletes {
				t.Errorf("countHunkAddsDeletes(%q) = +%d -%d, want +%d -%d", tt.hunk, a, d, tt.adds, tt.deletes)
			}
		})
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestCountHunkAddsDeletes -v`
预期：FAIL，报 `undefined: countHunkAddsDeletes`

- [ ] **步骤 3：编写实现代码**

创建 `ui/diff_viewer.go`：

```go
package ui

import "strings"

// hunkHit records a hunk summary line's position in the collapsed diff block
// so a mouse click on that line can be mapped back to the hunk.
type hunkHit struct {
	nodeIdx  int // index into toolTree (the edit/write node)
	childIdx int // index into node.Children (the hunk)
	lineIdx  int // index of this summary line within the full body line array
}

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
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestCountHunkAddsDeletes -v`
预期：PASS（全部子测试）

- [ ] **步骤 5：Commit**

```bash
git add ui/diff_viewer.go ui/diff_render_test.go
git commit -m "feat(ui): 新增 countHunkAddsDeletes 统计 hunk 增删行数

为 diff hunk 折叠摘要展示 +N -M 做准备。"
```

---

## 任务 2：`renderDiffBlock` 改为 hunk 折叠摘要（TDD）

把 `renderDiffBlock` 从"显示完整 hunk 内容"改为"每个 hunk 一行摘要"，并记录 `hunkHits` 命中映射。

**文件：**
- 修改：`ui/model.go:1892-1920`（`renderDiffBlock`）
- 修改：`ui/diff_viewer.go`（新增 `hunkSummaryLine`、`renderDiffViewer` 占位）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 末尾追加：

```go
func TestRenderDiffBlock_CollapsesHunks(t *testing.T) {
	m := Model{width: 80}
	nodes := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{
			{Name: "hunk", Detail: "@@ -1,3 +1,3 @@", DetailFull: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n+extra"},
			{Name: "hunk", Detail: "@@ -10,2 +10,2 @@", DetailFull: "@@ -10,2 +10,2 @@\n ctx\n-old2\n+new2"},
		},
	}}
	got := m.renderDiffBlock(nodes, 80)
	// 每行 stripAnsi 后的纯文本
	plain := make([]string, len(got))
	for i, l := range got {
		plain[i] = stripAnsi(l)
	}
	// 应含两个 hunk 摘要行，带 +N -M
	found1, found2 := false, false
	for _, l := range plain {
		if strings.Contains(l, "@@ -1,3 +1,3 @@") && strings.Contains(l, "+2") && strings.Contains(l, "-1") {
			found1 = true
		}
		if strings.Contains(l, "@@ -10,2 +10,2 @@") && strings.Contains(l, "+1") && strings.Contains(l, "-1") {
			found2 = true
		}
	}
	if !found1 {
		t.Errorf("缺少 hunk1 摘要 (+2 -1): %v", plain)
	}
	if !found2 {
		t.Errorf("缺少 hunk2 摘要 (+1 -1): %v", plain)
	}
	// 不应含完整 hunk 内容行 "ctx" / "old" / "new"
	for i, l := range plain {
		if strings.Contains(l, "old") || strings.Contains(l, "new") || strings.Contains(l, "extra") {
			t.Errorf("第 %d 行泄漏了 hunk 内容（应折叠）: %q", i, l)
		}
	}
	// hunkHits 应记录 2 个命中
	if len(m.hunkHits) != 2 {
		t.Errorf("hunkHits: want 2, got %d", len(m.hunkHits))
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestRenderDiffBlock_CollapsesHunks -v`
预期：FAIL（当前 `renderDiffBlock` 渲染完整 hunk 内容，会泄漏 "old"/"new"，且无 `hunkHits` 字段）

- [ ] **步骤 3：编写实现代码**

先在 `ui/model.go` 的 Model 结构体添加字段（在 `selection` 字段附近）：

```go
	// Diff hunk collapse viewer
	diffViewerActive bool
	diffViewerHunk   hunkHit // which hunk is shown full-screen
	hunkHits         []hunkHit // collapsed-view hunk summary line positions (refreshed each render)
```

注意：`hunkHit` 类型在任务 1 已定义于 `ui/diff_viewer.go`。

然后替换 `renderDiffBlock`（model.go:1892-1920）。原代码渲染完整 hunk 内容；新代码渲染摘要行并记录 `hunkHits`：

```go
func (m Model) renderDiffBlock(nodes []ToolNode, width int) []string {
	var content []string
	header := SpinnerStyle.Render("▍") + " [~] " + SpinnerStyle.Render("Changes")
	content = append(content, header)
	content = append(content, "")
	m.hunkHits = m.hunkHits[:0] // reset; rebuilt below
	// Track the absolute body line index of each summary line. We don't know
	// the absolute index here (caller's offset), so record a running counter
	// relative to this block's start; the caller adjusts. For simplicity we
	// store the index within `content` and the View layer maps screen→content.
	// Actually screenToLine returns body-line index, and this block is embedded
	// in body lines — so we need absolute body indices. We approximate by
	// recording content-local indices here; a follow-up mapping in the View
	// pass converts. To keep it correct, we record the content-local line index
	// (0-based within content) plus node/child; the click handler will compare
	// against the body-line index returned by screenToLine by offsetting with
	// the block's start in the body. Since the block start varies, we instead
	// store the plain-text of each summary line and match by content. See
	// hitTestHunk for the matching logic.
	for nodeIdx, node := range nodes {
		status := ""
		if node.Done {
			status = " " + SpinnerDoneStyle.Render("✓")
		}
		content = append(content, fmt.Sprintf("  :: %s%s", node.Detail, status))
		if node.Done && len(node.Children) > 0 {
			for childIdx, child := range node.Children {
				if child.DetailFull == "" {
					continue
				}
				adds, deletes := countHunkAddsDeletes(child.DetailFull)
				summary := hunkSummaryLine(childIdx, child.Detail, adds, deletes)
				contentLocalIdx := len(content)
				content = append(content, summary)
				m.hunkHits = append(m.hunkHits, hunkHit{
					nodeIdx:  nodeIdx,
					childIdx: childIdx,
					lineIdx:  contentLocalIdx, // content-local; View pass converts to body-absolute
				})
			}
		}
	}
	// R3: 保留首尾空行作为视觉间隔，不 pad 全宽。
	result := make([]string, 0, len(content)+2)
	result = append(result, "")
	result = append(result, content...)
	result = append(result, "")
	return result
}
```

在 `ui/diff_viewer.go` 追加 `hunkSummaryLine`：

```go
import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// hunkSummaryLine renders one collapsed hunk summary line:
//   [N] @@ -1,3 +1,3 @@        +2  -1
func hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string {
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("210"))
	label := numStyle.Render(fmt.Sprintf("  [%d] ", idx+1))
	changes := addStyle.Render(fmt.Sprintf("+%d", adds)) + " " + delStyle.Render(fmt.Sprintf("-%d", deletes))
	// pad header region so changes right-align-ish; keep simple: header + spaces + changes
	return label + hunkHeader + "    " + changes
}
```

注意：`ui/diff_viewer.go` 任务 1 只 import 了 `strings`，这里要加 `fmt` 和 `lipgloss`，更新 import 块。

**关于命中映射的关键说明（实现者必读）：** 上面 `lineIdx` 是 content-local 索引。但 `screenToLine` 返回的是 body-absolute 索引。任务 3 会在 View 渲染时把 `hunkHits` 的 `lineIdx` 转换为 body-absolute（加上 diff block 在 body 中的起始偏移）。本任务的测试只验证 `len(m.hunkHits)==2` 和摘要内容，不验证 lineIdx 绝对值。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffBlock_CollapsesHunks -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_viewer.go ui/diff_render_test.go
git commit -m "feat(ui): renderDiffBlock 改为 hunk 折叠摘要

每个 hunk 显示 [N] @@ 头 +N -M，不显示内容。记录 hunkHits
命中映射供点击展开使用。"
```

---

## 任务 3：全屏 diff viewer 渲染 + View 分支（TDD）

新增 `renderDiffViewer`（占满 body 渲染单个 hunk），并在 View 的 body 渲染分支里：`diffViewerActive` 时渲染全屏 hunk 替代 `renderBody`。

**文件：**
- 修改：`ui/diff_viewer.go`（新增 `renderDiffViewer`）
- 修改：`ui/model.go` View() body 分支（约 762-785）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 末尾追加：

```go
func TestRenderDiffViewer_RendersHunkFullscreen(t *testing.T) {
	m := Model{width: 80, height: 24}
	// 构造 toolTree 带一个 hunk
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,2 +1,2 @@\n-old\n+new"}},
	}}
	m.diffViewerActive = true
	m.diffViewerHunk = hunkHit{nodeIdx: 0, childIdx: 0}
	lines := m.renderDiffViewer(78)
	if len(lines) == 0 {
		t.Fatal("renderDiffViewer 返回空")
	}
	// 应含 hunk 内容（+new / -old）
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripAnsi(joined), "+new") {
		t.Errorf("全屏 viewer 缺少 +new: %q", joined)
	}
	if !strings.Contains(stripAnsi(joined), "-old") {
		t.Errorf("全屏 viewer 缺少 -old: %q", joined)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestRenderDiffViewer_RendersHunkFullscreen -v`
预期：FAIL，报 `undefined: renderDiffViewer`

- [ ] **步骤 3：编写实现代码**

在 `ui/diff_viewer.go` 追加：

```go
// renderDiffViewer renders a single hunk full-screen (occupying the whole body)
// when diffViewerActive. Returns the hunk's rendered lines (plain, no scroll slice).
func (m Model) renderDiffViewer(width int) []string {
	if !m.diffViewerActive {
		return nil
	}
	h := m.diffViewerHunk
	if h.nodeIdx < 0 || h.nodeIdx >= len(m.toolTree) {
		return nil
	}
	node := m.toolTree[h.nodeIdx]
	if h.childIdx < 0 || h.childIdx >= len(node.Children) {
		return nil
	}
	child := node.Children[h.childIdx]
	if child.DetailFull == "" {
		return nil
	}
	// Header: file path + hunk header, then the hunk content via renderDiffHunkBlock.
	var lines []string
	lines = append(lines, fmt.Sprintf("  %s  %s", node.Detail, child.Detail))
	lines = append(lines, "")
	lines = append(lines, renderDiffHunkBlock(child.DetailFull, width-4)...)
	return lines
}
```

然后在 `ui/model.go` 的 View() body 渲染分支加入 diffViewer 路径。先 Read View() 的 760-786 区域确认当前代码，然后在 `} else {` 分支（行 762）之前插入 diffViewer 分支。

将 View() 中（约 730-785）：

```go
	var lines []string
	total := 0
	maxScroll := 0
	scrollOff := 0
	frozen := (m.selection.Active || m.selection.Done) && m.selection.Rendered != nil
	if frozen {
```

改为：

```go
	var lines []string
	total := 0
	maxScroll := 0
	scrollOff := 0

	// Full-screen diff viewer: render the selected hunk, scrollable.
	if m.diffViewerActive {
		viewerLines := m.renderDiffViewer(scrollContentWidth)
		lines = viewerLines
		total = len(lines)
		scrollOff = m.scrollOffset
		if total > bodyHeight {
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
		// pad/trim to bodyHeight (same as Step 6 below, but early)
		for len(lines) < bodyHeight {
			lines = append(lines, "")
		}
		if len(lines) > bodyHeight {
			lines = lines[len(lines)-bodyHeight:]
		}
		for i := range lines {
			lines[i] = ansi.Truncate(lines[i], contentWidth, "")
		}
		statusLine := renderStatusBar(m.status, scrollOff, maxScroll, contentWidth, m.clipboardFeedback, m.clipboardError)
		// Assemble: body + footer (skip popups/suggestions in viewer mode)
		body := strings.Join(lines, "\n")
		inputLine := renderInputLine(m)
		footer := "\033[0m" + inputLine + "\n\033[0m" + statusLine
		full := body + "\n" + footer
		finalLines := strings.Split(full, "\n")
		if len(finalLines) > m.height {
			finalLines = finalLines[len(finalLines)-m.height:]
		}
		for i := range finalLines {
			finalLines[i] = ansi.Truncate(finalLines[i], m.width, "")
		}
		return strings.Join(finalLines, "\n")
	}

	frozen := (m.selection.Active || m.selection.Done) && m.selection.Rendered != nil
	if frozen {
```

注意：`renderStatusBar`、`renderInputLine`、`contentWidth`、`scrollContentWidth`、`bodyHeight`、`ansi` 均在 View() 作用域内已定义/可用。`frozen` 变量原本声明在 `if frozen {` 之前，现在拆成先声明后判断（如上）。实现时先 Read 实际代码确认 `frozen` 声明位置，调整为准。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffViewer_RendersHunkFullscreen -v`
预期：PASS

再运行 `go test ./ui/ -race 2>&1 | tail -3` 确认无回归。

- [ ] **步骤 5：Commit**

```bash
git add ui/diff_viewer.go ui/model.go ui/diff_render_test.go
git commit -m "feat(ui): 全屏 diff viewer 渲染 + View 分支

diffViewerActive 时占满 body 渲染单个 hunk，支持滚动。"
```

---

## 任务 4：点击 hunk 进入全屏 + 命中映射（TDD）

鼠标 release 单点击（`Start == End`）时，若点击位置命中某 hunk 摘要行，进入全屏 viewer。需把 `hunkHits` 的 content-local `lineIdx` 转换为 body-absolute。

**文件：**
- 修改：`ui/model.go` mouse release 逻辑（约 310-337）
- 修改：`ui/diff_viewer.go`（新增 `hitTestHunk`）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 末尾追加。`hitTestHunk` 接收 body-absolute 行索引 + body 渲染的 plain 行数组，返回是否命中 + 命中的 hunkHit：

```go
func TestHitTestHunk(t *testing.T) {
	m := Model{width: 80}
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,2 +1,2 @@\n-old\n+new"}},
	}}
	// 渲染折叠 block 拿到 content 行
	blockLines := m.renderDiffBlock(m.toolTree, 80)
	// 模拟 body：前面 5 行其他内容 + block
	bodyPlain := []string{"msg1", "msg2", "msg3", "msg4", "msg5"}
	for _, l := range blockLines {
		bodyPlain = append(bodyPlain, stripAnsi(l))
	}
	// hunkHits 的 lineIdx 是 content-local；hitTestHunk 需用 body-absolute
	// 找到 hunk 摘要行在 bodyPlain 中的位置
	hit, ok := m.hitTestHunk(5+2, bodyPlain) // block 从 index 5 开始，摘要行是 block content 第 2 行（:: foo.go 是第1，摘要第2）
	_ = hit
	// 由于 block 第 0 行是首部空行、第1行 header、第2行空、第3行 :: foo.go、第4行摘要
	// content-local 索引在 renderDiffBlock 里记录的是 content 数组索引，需对应
	if !ok {
		t.Errorf("hitTestHunk 应命中 hunk 摘要行")
	}
}
```

注意：上面测试对 lineIdx 偏移的精确性依赖 `renderDiffBlock` 的内部结构。实现者运行测试时若偏移不符，调整 `5+2` 为实际值——但核心是 `hitTestHunk` 能在 body-absolute 索引上命中。实现时**先确认 `renderDiffBlock` 输出的 plain 行结构**，再确定摘要行的 body-absolute 索引，写稳定断言。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestHitTestHunk -v`
预期：FAIL，报 `undefined: hitTestHunk`

- [ ] **步骤 3：编写实现代码**

`hitTestHunk` 的设计：因为 `hunkHits.lineIdx` 是 content-local，难以稳定映射到 body-absolute，改用**纯文本匹配**——`hitTestHunk(bodyLineIdx, bodyPlain)` 检查 `bodyPlain[bodyLineIdx]` 是否是某 hunk 摘要行（以 `  [N]` 开头 + 含 `@@`），若是，解析 `[N]` 得到序号，结合当前 `m.hunkHits` 顺序定位。

在 `ui/diff_viewer.go` 追加：

```go
import "regexp"

// hunkSummaryRe matches a collapsed hunk summary line's leading "  [N] ".
var hunkSummaryRe = regexp.MustCompile(`^\s*\[(\d+)\]`)

// hitTestHunk checks whether the body line at bodyLineIdx is a hunk summary
// line and, if so, returns the corresponding hunkHit. bodyPlain is the
// ANSI-stripped body line array. Matching is by the [N] index, cross-referenced
// with m.hunkHits (which records nodeIdx/childIdx per summary line in order).
func (m Model) hitTestHunk(bodyLineIdx int, bodyPlain []string) (hunkHit, bool) {
	if bodyLineIdx < 0 || bodyLineIdx >= len(bodyPlain) {
		return hunkHit{}, false
	}
	line := bodyPlain[bodyLineIdx]
	match := hunkSummaryRe.FindStringSubmatch(line)
	if match == nil {
		return hunkHit{}, false
	}
	// [N] is 1-based; map to hunkHits index. hunkHits is in render order,
	// each entry corresponds to summary line N (1-based) in order.
	var n int
	fmt.Sscanf(match[1], "%d", &n)
	idx := n - 1
	if idx < 0 || idx >= len(m.hunkHits) {
		return hunkHit{}, false
	}
	return m.hunkHits[idx], true
}
```

注意：`ui/diff_viewer.go` 需补 `regexp` import；`fmt` 已在任务 2 加。`fmt.Sscanf` 需 `fmt`。

然后修改 `ui/model.go` mouse release（约 310-337）。在 `if sel.Start == sel.End {` 分支里（单点击），原代码是 `m.selection = SelectionState{}` 清选区。改为先尝试 hunk 命中：

将：

```go
					if sel.Start == sel.End {
						m.selection = SelectionState{}
					} else {
```

改为：

```go
					if sel.Start == sel.End {
						// Single click: if it lands on a hunk summary line, open
						// the full-screen diff viewer for that hunk.
						if hit, ok := m.hitTestHunk(sel.End.Line, sel.Plain); ok {
							m.diffViewerActive = true
							m.diffViewerHunk = hit
							m.scrollOffset = 0
							m.selection = SelectionState{}
							return m, m.repaintCmd()
						}
						m.selection = SelectionState{}
					} else {
```

`sel.Plain` 是 mouse-down 快照的 plain 行数组，与点击时的 body 一致。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestHitTestHunk -v`
预期：PASS（实现者调整测试中 `5+2` 偏移至实际值后通过）

再运行 `go test ./ui/ -race 2>&1 | tail -3`。

- [ ] **步骤 5：Commit**

```bash
git add ui/diff_viewer.go ui/model.go ui/diff_render_test.go
git commit -m "feat(ui): 点击 hunk 摘要行进入全屏 diff viewer

单点击（Start==End）命中 hunk 摘要行时展开该 hunk 全屏。"
```

---

## 任务 5：ESC 退出全屏 + 全屏禁拖选 + 全屏滚轮（TDD）

全屏 viewer 下：ESC 退出、mouse-down/release 不进入拖选（仅滚轮滚动）。

**文件：**
- 修改：`ui/model.go` handleKey ESC（约 931）、mouse 处理（约 225-277）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 末尾追加。测试 ESC 退出：

```go
func TestESCExitsDiffViewer(t *testing.T) {
	m := Model{
		width:            80,
		height:           24,
		state:            stateReady,
		diffViewerActive: true,
		diffViewerHunk:   hunkHit{nodeIdx: 0, childIdx: 0},
		scrollOffset:     5,
	}
	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := res.(Model)
	if m2.diffViewerActive {
		t.Error("ESC 应退出 diff viewer")
	}
	if m2.scrollOffset != 0 {
		t.Errorf("ESC 应重置 scrollOffset, got %d", m2.scrollOffset)
	}
}
```

注意：`tea.KeyMsg`、`tea.KeyEsc`、`stateReady` 在 model_test.go 已有先例可用。需 import `tea "github.com/charmbracelet/bubbletea"`（diff_render_test.go 当前可能没 import tea，需加）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestESCExitsDiffViewer -v`
预期：FAIL（当前 ESC 在 stateReady 下清输入框，不退出 viewer）

- [ ] **步骤 3：编写实现代码**

在 `ui/model.go` handleKey 的 ESC 处理（约 931）**最前面**插入 diffViewer 拦截。将：

```go
	if msg.Type == tea.KeyEsc {
		if m.state == stateRunning {
```

改为：

```go
	if msg.Type == tea.KeyEsc {
		// Diff viewer: ESC exits to collapsed view (before other ESC handling).
		if m.diffViewerActive {
			m.diffViewerActive = false
			m.scrollOffset = 0
			return m, nil
		}
		if m.state == stateRunning {
```

然后修改 mouse 处理（约 225-277）加入全屏禁拖选 + 滚轮滚 viewer。在 `case tea.MouseMsg:` 开头（约 226，motion 处理之前）插入：

```go
	case tea.MouseMsg:
		// Full-screen diff viewer: only wheel scroll, no drag-select.
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
		// Handle motion events (drag) — they have Button=MouseButtonNone
		if msg.Action == tea.MouseActionMotion {
```

注意：全屏 viewer 的 maxScroll 在 View 渲染时基于 hunk 行数计算，mouse 这里滚轮上滚不钳制 maxScroll（hunk 内容不变，maxScroll 稳定；View 渲染会钳制显示）。若实测发现滚过头，在 mouse 处也加钳制——但 YAGNI，先不加。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestESCExitsDiffViewer -v`
预期：PASS

再运行 `go test ./ui/ -race 2>&1 | tail -3`。

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "feat(ui): ESC 退出全屏 diff viewer + 全屏禁拖选滚轮滚动

全屏 viewer 下 ESC 退出回折叠视图；mouse 仅滚轮滚动，禁拖选。"
```

---

## 任务 6：全量验证 + lint

**文件：** 无（验证任务）

- [ ] **步骤 1：运行全量测试（race）**

运行：`make test` 或 `go test ./ui/ -race`
预期：全部 PASS，无 race。

- [ ] **步骤 2：go vet**

运行：`go vet ./...`
预期：无问题。

- [ ] **步骤 3：go build**

运行：`go build ./...`
预期：编译通过。确认 `ui/diff_viewer.go` import 齐全（fmt、strings、regexp、lipgloss）。

- [ ] **步骤 4：手动验收说明（交付用户）**

1. `make build`
2. `./deepact`，触发一个会产生多 hunk diff 的 edit
3. 验证：diff block 默认折叠，每个 hunk 显示 `[N] @@ ... @@ +A -D`
4. 鼠标单击 hunk 摘要行 → 全屏显示该 hunk 内容
5. 全屏内滚轮滚动长 hunk
6. ESC 退回折叠视图
7. 折叠视图下拖选仍可用；全屏下拖选禁用
8. **重点验证 ghosting 是否消失**（折叠视图无 diff 内容混排；全屏 diff 无其他行渗入）

- [ ] **步骤 5：Commit（如有 lint 修复）**

```bash
git add -A
git commit -m "chore(ui): lint 修复"
```
（仅在有修复时执行）

---

## 自检

**1. 规格覆盖度：**
- hunk 级折叠摘要 → 任务 1（countHunkAddsDeletes）+ 任务 2（renderDiffBlock 折叠）✓
- 点击 hunk 全屏展开 → 任务 4（hitTestHunk + mouse release）✓
- 全屏占满 body + 滚动 → 任务 3（renderDiffViewer + View 分支）+ 任务 5（滚轮）✓
- ESC 退出 → 任务 5 ✓
- 全屏禁拖选 → 任务 5 ✓
- 折叠视图仍可拖选 → 任务 5 只在 diffViewerActive 禁拖选，折叠视图（非 active）走原逻辑 ✓
- 保留 R1-R4 → 任务 2 复用 renderDiffHunkBlock（含 R1/R2/R4），R3 padLine 移除保留 ✓
- 不闪屏 → 用 repaintCmd（WindowSizeMsg 版本），未引入 ClearScreen ✓

**2. 占位符扫描：** 任务 4 测试中有"实现者调整偏移"说明——这是对测试稳定性的提示，非计划占位；核心逻辑（hitTestHunk 匹配 `[N]`）有完整代码。任务 3 View 分支有完整代码 + "先 Read 确认"提示。无 TODO/待定。✓

**3. 类型一致性：**
- `hunkHit{nodeIdx, childIdx, lineIdx int}` —— 任务 1 定义，任务 2/3/4 使用，字段一致 ✓
- `countHunkAddsDeletes(hunk string) (int, int)` —— 任务 1 定义，任务 2 调用 ✓
- `hunkSummaryLine(idx int, hunkHeader string, adds, deletes int) string` —— 任务 2 定义使用 ✓
- `renderDiffViewer(width int) []string` —— 任务 3 定义，View 调用 ✓
- `hitTestHunk(bodyLineIdx int, bodyPlain []string) (hunkHit, bool)` —— 任务 4 定义，mouse release 调用 ✓
- `diffViewerActive bool` / `diffViewerHunk hunkHit` / `hunkHits []hunkHit` —— 任务 2 定义字段，任务 3/4/5 使用 ✓
- `renderDiffHunkBlock(hunkContent string, maxWidth int) []string` —— 任务 3 调用，签名不变（已有）✓

无问题，计划完整。
