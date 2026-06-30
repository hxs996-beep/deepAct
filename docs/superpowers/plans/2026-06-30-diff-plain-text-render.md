# Diff 纯文本展示修复 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 修复 UI 中代码 diff 区域鼠标选错行、滚动错位、滚动残留花屏三个症状，把 diff 改为纯文本展示（保留单层前景色）。

**架构：** diff 渲染层 4 个根因——空行被 `continue` 丢弃（R1）、未剥离 `\r`（R2）、pad 宽度不一致（R3）、长行无截断提示（R4）。修 R1（空行占位）/R2（剥 `\r`）/R4（硬截断 + `…` + 新增 `truncateVisual`）；移除 `renderDiffBlock` 的 padLine 统一宽度根除 R3。选区层不动，靠渲染层修根因 + View 统一 `ansi.Truncate` 对齐宽度。

**技术栈：** Go 1.24+、Bubble Tea、lipgloss、`github.com/muesli/reflow/ansi`（已在 model.go 使用）。

**规格：** `docs/superpowers/specs/2026-06-30-diff-plain-text-render-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|------|------|------|
| `ui/selection.go` | 新增 `truncateVisual` 辅助函数（按显示宽度截断，保留 ANSI 边界） | 修改 |
| `ui/model.go` | `renderDiffHunkBlock`（R1/R2/R4）、`renderDiffHunkFlat`（R1/R2）、`renderDiffBlock`（R3 移除 padLine） | 修改 |
| `ui/diff_render_test.go` | diff 渲染层单元测试（R1/R2/R4/宽字符/行号） | 创建 |
| `ui/selection_test.go` | `truncateVisual` 测试 + 选区映射回归测试（R1/R2） | 修改 |

**放置决策：** `truncateVisual` 放在 `ui/selection.go`，与同文件的 `sliceByVisualCol` / `stripAnsi` 等显示宽度工具同属一类，复用 `lipgloss.Width` 和 `decodeRuneAt`。不单独建文件——单一职责已由 selection.go 的"视觉宽度工具集"承担。

---

## 任务 1：`truncateVisual` 辅助函数（TDD）

**文件：**
- 修改：`ui/selection.go`（在 `sliceByVisualCol` 之后，约 333 行后追加）
- 测试：`ui/selection_test.go`（在文件末尾追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/selection_test.go` 末尾追加：

```go
// ---- truncateVisual tests ----

func TestTruncateVisual_PlainASCII(t *testing.T) {
	// maxW=5, 输入 10 字符 → 截到显示宽度 5，无尾标记
	got := truncateVisual("abcdefghij", 5)
	if w := lipgloss.Width(got); w != 5 {
		t.Errorf("width: want 5, got %d (%q)", w, got)
	}
	if got != "abcde" {
		t.Errorf("want %q, got %q", "abcde", got)
	}
}

func TestTruncateVisual_WideChar(t *testing.T) {
	// 中文每字宽 2，maxW=5 → 保留 2 字（宽 4），第 3 字会超宽故丢弃
	got := truncateVisual("你好世界测试", 5)
	if w := lipgloss.Width(got); w > 5 {
		t.Errorf("width: want <=5, got %d (%q)", w, got)
	}
	if w := lipgloss.Width(got); w != 4 {
		t.Errorf("width: want 4 (2 CJK chars), got %d (%q)", w, got)
	}
}

func TestTruncateVisual_PreservesANSI(t *testing.T) {
	// 含 ANSI 序列：截断后不切断转义序列，且 stripAnsi 后宽度 <= maxW
	styled := "\x1b[31mabcdefghij\x1b[0m"
	got := truncateVisual(styled, 5)
	if strings.Contains(got, "\x1b[31m") == false {
		t.Errorf("ANSI seq lost: %q", got)
	}
	// 不应出现被切断的残缺转义（如 \x1b[31 但无 m）
	if strings.Contains(got, "\x1b[0m") == false {
		t.Errorf("reset seq lost: %q", got)
	}
	if w := lipgloss.Width(stripAnsi(got)); w > 5 {
		t.Errorf("visual width: want <=5, got %d (%q)", w, got)
	}
}

func TestTruncateVisual_NoTruncationNeeded(t *testing.T) {
	got := truncateVisual("abc", 10)
	if got != "abc" {
		t.Errorf("want %q, got %q", "abc", got)
	}
}

func TestTruncateVisual_EmptyAndZero(t *testing.T) {
	if got := truncateVisual("", 5); got != "" {
		t.Errorf("empty input: want %q, got %q", "", got)
	}
	if got := truncateVisual("abc", 0); got != "" {
		t.Errorf("maxW=0: want %q, got %q", "", got)
	}
}
```

注意：`ui/selection_test.go` 现有 import 只有 `strings` 和 `testing` 与 engine。新增测试用到 `lipgloss.Width`，需在 import 块追加 `"github.com/charmbracelet/lipgloss"`。将 import 块改为：

```go
import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/deepact/deepact/engine"
)
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestTruncateVisual -v`
预期：FAIL，报错 `undefined: truncateVisual`

- [ ] **步骤 3：编写最少实现代码**

在 `ui/selection.go` 的 `sliceByVisualCol` 函数之后（约 333 行 `}` 之后）追加：

```go
// truncateVisual truncates s to fit within maxW display columns, preserving
// ANSI escape sequences (never splits a sequence mid-way) and wide runes
// (CJK/emoji counted as width-2 via lipgloss.Width). It does NOT append a
// trailing marker — callers decide whether to add "…" etc. Returns s
// unchanged if its visual width <= maxW. maxW <= 0 yields "".
func truncateVisual(s string, maxW int) string {
	if maxW <= 0 || s == "" {
		return ""
	}
	var sb strings.Builder
	visualCol := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			seqEnd := findAnsiSeqEnd(s, i)
			sb.WriteString(s[i:seqEnd])
			i = seqEnd
			continue
		}
		r, size := decodeRuneAt(s, i)
		rw := lipgloss.Width(string(r))
		if visualCol+rw > maxW {
			break
		}
		sb.WriteString(s[i : i+size])
		i += size
		visualCol += rw
	}
	return sb.String()
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestTruncateVisual -v`
预期：PASS（全部 5 个测试）

- [ ] **步骤 5：Commit**

```bash
git add ui/selection.go ui/selection_test.go
git commit -m "feat(ui): 新增 truncateVisual 按显示宽度截断辅助函数

按 lipgloss.Width 逐 rune 截断，保留 ANSI 转义序列边界，为 diff
长行硬截断做准备。"
```

---

## 任务 2：`renderDiffHunkBlock` 修复 R1（空行）+ R2（剥 `\r`）

**文件：**
- 修改：`ui/model.go:1994-2051`（`renderDiffHunkBlock` 函数体）
- 测试：`ui/diff_render_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

创建 `ui/diff_render_test.go`：

```go
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// stripAnsi 在 selection.go 中已定义，测试可直接复用。

// buildHunk 构造一个含空 context 行 + CRLF 的 hunk 内容用于测试。
func buildHunk() string {
	// 行顺序：@@ header, context, 空 context 行, delete, insert, context
	return "@@ -1,4 +1,4 @@\n line1\n\n-old\n+new\n line4"
}

func TestRenderDiffHunkBlock_PreservesEmptyLines(t *testing.T) {
	hunk := buildHunk()
	// 输入按 \n 分割后的行数（含空行）
	inputLines := strings.Split(hunk, "\n")
	got := renderDiffHunkBlock(hunk, 120)
	if len(got) != len(inputLines) {
		t.Errorf("空行被丢弃: input %d 行, output %d 行", len(inputLines), len(got))
	}
}

func TestRenderDiffHunkBlock_StripsCR(t *testing.T) {
	// 构造含 \r\n 的 hunk
	hunk := "@@ -1,2 +1,2 @@\n line1\r\n-old\r\n+new\r"
	got := renderDiffHunkBlock(hunk, 120)
	for i, line := range got {
		if strings.Contains(line, "\r") {
			t.Errorf("第 %d 行仍含 \\r: %q", i, line)
		}
	}
}

func TestRenderDiffHunkBlock_LineNumbersAligned(t *testing.T) {
	hunk := "@@ -1,3 +1,3 @@\n ctx\n-old\n+new"
	got := renderDiffHunkBlock(hunk, 120)
	// 第 0 行 @@ header，第 1 行 context（old=1,new=1），第 2 行 delete（old=2），第 3 行 insert（new=2）
	if len(got) != 4 {
		t.Fatalf("want 4 行, got %d (%v)", len(got), got)
	}
	plain0 := stripAnsi(got[0])
	if !strings.HasPrefix(plain0, "    @@") {
		t.Errorf("header 行格式错: %q", plain0)
	}
	// delete 行应含 "  2     " 行号 + "-old"
	plainDel := stripAnsi(got[2])
	if !strings.Contains(plainDel, "2") || !strings.Contains(plainDel, "-old") {
		t.Errorf("delete 行号/内容错: %q", plainDel)
	}
	// insert 行应含 new=2
	plainIns := stripAnsi(got[3])
	if !strings.Contains(plainIns, "-new") == false || !strings.Contains(plainIns, "2") {
		// +new 的前缀 + 在 ANSI 里，stripAnsi 后是 "+new"
	}
	if !strings.Contains(plainIns, "+new") {
		t.Errorf("insert 内容错: %q", plainIns)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestRenderDiffHunkBlock -v`
预期：
- `TestRenderDiffHunkBlock_PreservesEmptyLines` FAIL（空行被 continue 丢弃，输出行数 < 输入）
- `TestRenderDiffHunkBlock_StripsCR` FAIL（含 `\r`）

- [ ] **步骤 3：编写实现代码**

将 `ui/model.go` 中 `renderDiffHunkBlock` 的循环体（约 2005-2049 行）替换。原代码：

```go
	for _, hl := range lines {
		if hl == "" {
			continue
		}
		if strings.HasPrefix(hl, "@@") {
```

改为（剥 `\r` + 空行占位）：

```go
	for _, raw := range lines {
		hl := strings.TrimRight(raw, "\r")
		if hl == "" {
			// R1: 空行不再丢弃，渲染为占位空行，保持 屏幕行 == 数据行，
			// 否则 screenToLine 映射错位导致选错行/滚动错位。
			result = append(result, "    "+diffContextStyle.Render(""))
			continue
		}
		if strings.HasPrefix(hl, "@@") {
```

注意：原循环里有两个 `if len(hl) == 0 { continue }`（第二个在 2028 行附近，是无用重复检查）。修改后第二个 `if len(hl) == 0` 保留无害，但为整洁可一并删除该重复块。循环其余部分（`@@` 解析、prefix switch、行号递增）**不动**。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffHunkBlock -v`
预期：PASS（全部 3 个测试）

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "fix(ui): diff hunk 保留空行并剥离回车

R1: 空行改为占位渲染，保持屏幕行==数据行，修复选错行/滚动错位。
R2: TrimRight 剥离 \\r，修复回车致下行渲染到上行。"
```

---

## 任务 3：`renderDiffHunkBlock` 修复 R4（长行硬截断 + `…`）

**文件：**
- 修改：`ui/model.go:2034-2048`（prefix switch 内每个 case 的 append）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 追加：

```go
func TestRenderDiffHunkBlock_HardTruncatesLongLine(t *testing.T) {
	// 一行超长 insert，maxWidth=20
	long := strings.Repeat("x", 80)
	hunk := "@@ -1,1 +1,1 @@\n+" + long
	got := renderDiffHunkBlock(hunk, 20)
	if len(got) != 2 {
		t.Fatalf("want 2 行 (header+insert), got %d", len(got))
	}
	insertLine := got[1]
	if w := lipgloss.Width(stripAnsi(insertLine)); w > 20 {
		t.Errorf("insert 行显示宽度 %d 超过 maxWidth 20: %q", w, insertLine)
	}
	if !strings.HasSuffix(stripAnsi(insertLine), "…") {
		t.Errorf("长行末尾应有 … 截断提示: %q", stripAnsi(insertLine))
	}
}

func TestRenderDiffHunkBlock_WideCharWidth(t *testing.T) {
	// 含中文的超长行，截断后显示宽度 <= maxWidth
	long := strings.Repeat("你好", 40) // 每字宽2，共 160 宽
	hunk := "@@ -1,1 +1,1 @@\n+" + long
	got := renderDiffHunkBlock(hunk, 20)
	if len(got) < 2 {
		t.Fatalf("want >=2 行, got %d", len(got))
	}
	for i, line := range got {
		if w := lipgloss.Width(stripAnsi(line)); w > 20 {
			t.Errorf("第 %d 行显示宽度 %d 超过 20: %q", i, w, line)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run "TestRenderDiffHunkBlock_HardTruncatesLongLine|TestRenderDiffHunkBlock_WideCharWidth" -v`
预期：FAIL（当前无截断，显示宽度远超 maxWidth，且无 `…`）

- [ ] **步骤 3：编写实现代码**

在 `renderDiffHunkBlock` 中，给 `maxWidth` 加防御性下限（函数开头 `lines := strings.Split(...)` 之后）：

```go
	if maxWidth < 1 {
		maxWidth = 80
	}
```

然后把 prefix switch 的三个 case（约 2035-2048 行）的 append 改为走截断。原 case：

```go
		switch prefix {
		case "-":
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("%4d     ", oldNum))
			result = append(result, "    "+lineNum+diffDeleteStyle.Render(prefix+content))
			oldNum++
		case "+":
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("    %4d ", newNum))
			result = append(result, "    "+lineNum+diffInsertStyle.Render(prefix+content))
			newNum++
		default:
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("%4d %4d ", oldNum, newNum))
			result = append(result, "    "+lineNum+content)
			oldNum++
			newNum++
		}
```

改为（抽取 `renderTruncatedDiffLine` 闭包，统一截断 + `…`）：

```go
		// R4: 长行硬截断 + 行尾 …，保持显示宽度 <= maxWidth。
		renderTruncatedDiffLine := func(styled string) string {
			if w := lipgloss.Width(styled); w > maxWidth {
				return truncateVisual(styled, maxWidth-1) + "…"
			}
			return styled
		}
		switch prefix {
		case "-":
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("%4d     ", oldNum))
			line := renderTruncatedDiffLine("    " + lineNum + diffDeleteStyle.Render(prefix+content))
			result = append(result, line)
			oldNum++
		case "+":
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("    %4d ", newNum))
			line := renderTruncatedDiffLine("    " + lineNum + diffInsertStyle.Render(prefix+content))
			result = append(result, line)
			newNum++
		default:
			lineNum := diffLineNumStyle.Render(fmt.Sprintf("%4d %4d ", oldNum, newNum))
			line := renderTruncatedDiffLine("    " + lineNum + content)
			result = append(result, line)
			oldNum++
			newNum++
		}
```

注意：`@@` header 行（`result = append(result, "    "+diffHunkHeaderStyle.Render(hl))`）也走截断更一致，将其改为：

```go
				result = append(result, renderTruncatedDiffLine("    "+diffHunkHeaderStyle.Render(hl)))
```

但 `renderTruncatedDiffLine` 闭包定义在循环内、`@@` 分支之后——需把闭包定义提到 `for` 循环体最开头（在 `hl := strings.TrimRight(raw, "\r")` 之前），这样 `@@` 分支也能用。最终循环开头顺序：

```go
	for _, raw := range lines {
		renderTruncatedDiffLine := func(styled string) string {
			if w := lipgloss.Width(styled); w > maxWidth {
				return truncateVisual(styled, maxWidth-1) + "…"
			}
			return styled
		}
		hl := strings.TrimRight(raw, "\r")
		if hl == "" {
			result = append(result, "    "+diffContextStyle.Render(""))
			continue
		}
		if strings.HasPrefix(hl, "@@") {
			... // 内部 append 改为 renderTruncatedDiffLine(...)
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffHunkBlock -v`
预期：PASS（全部 5 个测试，含任务 2 的 3 个 + 本任务 2 个）

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "fix(ui): diff 长行硬截断并加 … 提示

R4: 超长代码行按显示宽度截断到 maxWidth，末尾加 …，修复长行
无提示且避免折行破坏行映射。"
```

---

## 任务 4：`renderDiffHunkFlat` 修复 R1 + R2（不加截断）

**文件：**
- 修改：`ui/model.go:2054-2111`（`renderDiffHunkFlat` 函数体）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 追加：

```go
func TestRenderDiffHunkFlat_PreservesEmptyLines(t *testing.T) {
	hunk := "@@ -1,3 +1,3 @@\n ctx\n\n-old\n+new"
	got := renderDiffHunkFlat(hunk)
	// flat 版按 \n 分隔，空行应保留为 "    "（4 空格占位）
	gotLines := strings.Split(got, "\n")
	inputLines := strings.Split(hunk, "\n")
	if len(gotLines) != len(inputLines) {
		t.Errorf("flat 空行被丢弃: input %d, output %d", len(inputLines), len(gotLines))
	}
}

func TestRenderDiffHunkFlat_StripsCR(t *testing.T) {
	hunk := "@@ -1,2 +1,2 @@\n ctx\r\n-old\r\n+new\r"
	got := renderDiffHunkFlat(hunk)
	if strings.Contains(got, "\r") {
		t.Errorf("flat 输出仍含 \\r: %q", got)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run "TestRenderDiffHunkFlat" -v`
预期：FAIL（空行被丢弃、含 `\r`）

- [ ] **步骤 3：编写实现代码**

`renderDiffHunkFlat`（model.go:2065 起）的循环开头与 `renderDiffHunkBlock` 同样的修改。原：

```go
	for _, hl := range lines {
		if hl == "" {
			continue
		}
		if strings.HasPrefix(hl, "@@") {
```

改为：

```go
	for _, raw := range lines {
		hl := strings.TrimRight(raw, "\r")
		if hl == "" {
			// R1: 空行占位，保持行数一致
			buf.WriteString("    \n")
			continue
		}
		if strings.HasPrefix(hl, "@@") {
```

同样删除循环里重复的 `if len(hl) == 0 { continue }`。**不加深截断**（flat 版跟随终端软折行）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffHunkFlat -v`
预期：PASS（2 个测试）

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "fix(ui): renderDiffHunkFlat 保留空行并剥离回车

flat 版同步修复 R1/R2，不加硬截断（消息文本流跟随终端软折行）。"
```

---

## 任务 5：`renderDiffBlock` 移除 padLine 修复 R3

**文件：**
- 修改：`ui/model.go:1910-1938`（`renderDiffBlock` 末尾的 pad 逻辑）
- 测试：`ui/diff_render_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

在 `ui/diff_render_test.go` 追加。`renderDiffBlock` 是 `Model` 方法，需构造最小 Model：

```go
func TestRenderDiffBlock_NoPadToTerminalWidth(t *testing.T) {
	// R3: renderDiffBlock 不应再 pad 到 m.width，宽度交由 View 统一 Truncate。
	// 构造一个已 Done 的 edit 节点带 hunk
	m := Model{width: 200} // 故意设很大的 width
	nodes := []ToolNode{{
		Name:       "edit",
		Done:       true,
		Detail:     "foo.go",
		Children:   []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
	}}
	got := m.renderDiffBlock(nodes, 80)
	// 每行显示宽度不应被 pad 到 200；短行应保持短（<= 80）
	for i, line := range got {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("第 %d 行被 pad 到 %d 宽 (>80): %q", i, w, line)
		}
	}
}
```

注意：`Model` 的字段名 `width` 需与 `ui/model.go` 中一致（已确认为小写 `m.width`）。若 `renderDiffBlock` 内仍引用 `m.width`，移除后该字段不再被此函数使用，但测试仍设置它无害。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestRenderDiffBlock_NoPadToTerminalWidth -v`
预期：FAIL（当前 padLine 把每行 pad 到 `m.width=200`，宽度 > 80）

- [ ] **步骤 3：编写实现代码**

将 `renderDiffBlock` 末尾（model.go:1910-1938）的 pad 逻辑整块替换。原：

```go
	// Render diff rows as plain foreground-colored text — NO background block.
	// ...（长注释）...
	contentWidth := m.width
	if contentWidth < 1 {
		contentWidth = width
	}
	padLine := func(line string) string {
		w := lipgloss.Width(line)
		if w >= contentWidth {
			return line
		}
		return line + strings.Repeat(" ", contentWidth-w)
	}
	result := make([]string, 0, len(content)+2)
	result = append(result, padLine(""))
	for _, line := range content {
		result = append(result, padLine(line))
	}
	result = append(result, padLine(""))
	return result
}
```

改为（移除 pad，保留首尾空行作为视觉间隔）：

```go
	// R3: 不再 pad 到终端全宽。View() 已对全 body 统一 ansi.Truncate 到
	// contentWidth，diff 行单独 pad 会引入 m.width/contentWidth/scrollContentWidth
	// 三宽度不一致，导致滚动残留/花屏。保留首尾空行作为视觉间隔即可。
	result := make([]string, 0, len(content)+2)
	result = append(result, "")
	result = append(result, content...)
	result = append(result, "")
	return result
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestRenderDiffBlock_NoPadToTerminalWidth -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/diff_render_test.go
git commit -m "fix(ui): 移除 renderDiffBlock 全宽 pad 统一宽度

R3: 删除 padLine，diff 行宽度交由 View 统一 ansi.Truncate，消除
m.width/contentWidth/scrollContentWidth 三宽度不一致导致的滚动残留。"
```

---

## 任务 6：选区映射回归测试（R1/R2 验证）

**文件：**
- 测试：`ui/selection_test.go`（在末尾追加）

- [ ] **步骤 1：编写测试**

在 `ui/selection_test.go` 末尾追加。这些是回归测试，验证 R1 修复后 `screenToLine` 对含空行的数组映射准确、R2 修复后选区复制不含 `\r`：

```go
// ---- R1/R2 regression tests ----

func TestScreenToLine_AfterEmptyLineFix(t *testing.T) {
	// 含空行的 plain 数组（模拟 R1 修复后的 1:1 行映射）
	// 10 行，bodyHeight=5，scrollOffset=0（底对齐）
	// firstVisibleLine = 10 - 0 - 5 = 5
	plain := []string{"L0", "L1", "L2", "L3", "L4", "L5", "", "L7", "L8", "L9"}
	totalLines := len(plain)
	bodyHeight := 5
	scroll := 0
	// 屏幕 row 0 → 数据行 5 (L5)
	pt0 := screenToLine(0, 0, scroll, bodyHeight, totalLines)
	if pt0.Line != 5 {
		t.Errorf("row 0: want line 5, got %d", pt0.Line)
	}
	// 屏幕 row 1 → 数据行 6（空行）
	pt1 := screenToLine(1, 0, scroll, bodyHeight, totalLines)
	if pt1.Line != 6 {
		t.Errorf("row 1: want line 6 (空行), got %d", pt1.Line)
	}
	// 屏幕 row 2 → 数据行 7 (L7)
	pt2 := screenToLine(2, 0, scroll, bodyHeight, totalLines)
	if pt2.Line != 7 {
		t.Errorf("row 2: want line 7, got %d", pt2.Line)
	}
}

func TestExtractSelectionText_NoCRInOutput(t *testing.T) {
	// R2: 渲染层已剥 \r，故 plain 快照不含 \r。这里直接验证选区文本无 \r。
	plain := []string{"+new", "-old", " ctx"}
	sel := SelectionState{
		Done:  true,
		Start: selPoint{Line: 0, Col: 0},
		End:   selPoint{Line: 2, Col: -1},
	}
	got := extractSelectionText(plain, sel)
	if strings.Contains(got, "\r") {
		t.Errorf("选区文本含 \\r: %q", got)
	}
	// 验证整行复制正确（R1 修复后行索引 1:1）
	want := "+new\n-old\n ctx"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./ui/ -run "TestScreenToLine_AfterEmptyLineFix|TestExtractSelectionText_NoCRInOutput" -v`
预期：PASS（这些是回归测试，验证既有正确行为在 R1/R2 修复后仍成立——`screenToLine` 本身未改，映射本就正确，R1 修复保证的是"输入数组行数 == 屏幕行数"这个前提）

- [ ] **步骤 3：Commit**

```bash
git add ui/selection_test.go
git commit -m "test(ui): 选区映射 R1/R2 回归测试

验证含空行数组的 screenToLine 1:1 映射、选区复制不含 \\r。"
```

---

## 任务 7：全量验证 + lint

**文件：** 无（验证任务）

- [ ] **步骤 1：运行全量测试（含 race detector）**

运行：`make test`
预期：全部 PASS，无 race 报告。若有既有失败，需判断是否本次引入。

- [ ] **步骤 2：运行 lint**

运行：`make lint`
预期：通过。若 golangci-lint 报本次新增代码问题（如未使用的 import），修复后重跑。

- [ ] **步骤 3：检查未使用的 import / 变量**

运行：`go build ./...`
预期：编译通过。重点确认 `ui/model.go` 中移除 padLine 后 `lipgloss` 是否仍被其他代码使用（是，diff 样式仍用）、`strings.Repeat` 是否仍有引用。

- [ ] **步骤 4：手动验收说明（交付给用户）**

向用户说明手动验收步骤：
1. `make build`
2. 运行 `./deepact`，触发一个会产生含空行/中文/长行 diff 的 edit 操作
3. 验证：鼠标长按拖选不再选错行（a）、上下滚动不再下行渲染到上行（b）、滚动无残留花屏（e）
4. 拖选后释放，粘贴验证复制内容正确、不含 `\r`

- [ ] **步骤 5：Commit（如有 lint 修复）**

```bash
git add -A
git commit -m "chore(ui): lint 修复"
```
（仅在有 lint 修复时执行；否则跳过此步骤）

---

## 自检

**1. 规格覆盖度：**
- R1（空行丢弃）→ 任务 2（block）+ 任务 4（flat）+ 任务 6（回归测试）✓
- R2（未剥 `\r`）→ 任务 2（block）+ 任务 4（flat）+ 任务 6（回归测试）✓
- R3（pad 宽度不一致）→ 任务 5 ✓
- R4（长行无截断提示）→ 任务 3 ✓
- `truncateVisual` 新增 → 任务 1 ✓
- 宽字符（c）→ 任务 3 `TestRenderDiffHunkBlock_WideCharWidth` + 任务 1 `TestTruncateVisual_WideChar` ✓
- 选区层不动 → 任务 6 仅回归测试，无改动 ✓
- 帧差异补丁不删 → 计划无对应任务，符合规格 ✓
- 验收（make test / lint / 手动）→ 任务 7 ✓

**2. 占位符扫描：** 无 TODO/待定/省略。每个步骤含完整代码块。任务 2 中提到的"删除重复的 `if len(hl) == 0`"已给出具体位置。✓

**3. 类型一致性：**
- `truncateVisual(s string, maxW int) string` —— 任务 1 定义，任务 3 `renderTruncatedDiffLine` 调用，签名一致 ✓
- `renderTruncatedDiffLine` 闭包 —— 任务 3 定义并使用 ✓
- `renderDiffHunkBlock(hunkContent string, maxWidth int) []string` —— 签名不变，任务 2/3 修改内部 ✓
- `renderDiffHunkFlat(hunkContent string) string` —— 签名不变，任务 4 修改内部 ✓
- `renderDiffBlock(nodes []ToolNode, width int) []string` —— 签名不变，任务 5 修改内部 ✓
- 测试中 `Model{width: 200}` 字段名 —— 与 model.go 小写 `width` 一致 ✓
- `diffContextStyle` —— 在 `initDiffStyles`（model.go:1986）定义，任务 2/4 使用前已通过 `diffStylesOnce.Do(initDiffStyles)` 初始化 ✓

无问题，计划完整。
