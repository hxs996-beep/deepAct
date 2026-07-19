# ANSI 感知自动换行 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 DeepAct CLI 中 LLM 输出超宽不换行、静默截断的问题，使含 ANSI 颜色码的长行也能正确按视觉宽度换行。

**Architecture:** 新增 `wrapLineAnsi` 函数作为 ANSI 感知的换行核心，修改 `wrapLine` 和 `wrapLines` 将 ANSI 行委托给它，对 lipgloss `Width()` 渲染的面板（sub-agent、member、TDD）进行后处理换行。Step 7 保留不动。

**Tech Stack:** Go, lipgloss, charmbracelet/x/ansi, 表格驱动测试

## Global Constraints

- 不修改 Step 7（`model.go:1008-1016`）的截断逻辑
- 不修改 `renderMarkdown`（glamour 已有换行）
- 所有新增函数必须有单元测试
- 遵循项目现有测试模式（表格驱动，`ui/model_test.go`）

---

### Task 1: 添加 `wrapLineAnsi` 函数

**Files:**
- Modify: `ui/model.go` — 在 `wrapLine` 之后（L3023 之后）添加函数
- Modify: `ui/model_test.go` — 添加测试

**Interfaces:**
- Produces: `func wrapLineAnsi(line string, width int) []string`

- [ ] **Step 1: 编写测试用例（表格驱动）**

在 `ui/model_test.go` 末尾添加：

```go
func TestWrapLineAnsi_PlainASCII(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{
			name:  "short line no wrap",
			line:  "hello",
			width: 10,
			want:  []string{"hello"},
		},
		{
			name:  "exact width no wrap",
			line:  "hello world",
			width: 11,
			want:  []string{"hello world"},
		},
		{
			name:  "wrap at space",
			line:  "hello world foo bar",
			width: 12,
			want:  []string{"hello world", "foo bar"},
		},
		{
			name:  "hard break when no space",
			line:  "abcdefghijklmnop",
			width: 5,
			want:  []string{"abcde", "fghij", "klmno", "p"},
		},
		{
			name:  "empty line",
			line:  "",
			width: 10,
			want:  []string{""},
		},
		{
			name:  "width zero",
			line:  "hello world",
			width: 0,
			want:  []string{"hello world"},
		},
		{
			name:  "width negative",
			line:  "hello world",
			width: -1,
			want:  []string{"hello world"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) = %q, want %q", tt.line, tt.width, got, tt.want)
			}
		})
	}
}

func TestWrapLineAnsi_WithSGR(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{
			name:  "SGR wrapped at space",
			line:  "\x1b[31mhello world foo bar\x1b[0m",
			width: 12,
			want:  []string{"\x1b[31mhello world\x1b[0m", "\x1b[31mfoo bar\x1b[0m"},
		},
		{
			name:  "SGR hard break no space",
			line:  "\x1b[31mabcdefghij\x1b[0m",
			width: 5,
			want:  []string{"\x1b[31mabcde\x1b[0m", "\x1b[31mfghij\x1b[0m"},
		},
		{
			name:  "short line with SGR no wrap",
			line:  "\x1b[32mhello\x1b[0m",
			width: 10,
			want:  []string{"\x1b[32mhello\x1b[0m"},
		},
		{
			name:  "multiple SGR sequences",
			line:  "\x1b[1m\x1b[31mbold red text long enough to wrap\x1b[0m",
			width: 16,
			want:  []string{"\x1b[1m\x1b[31mbold red text\x1b[0m", "\x1b[1m\x1b[31mlong enough to\x1b[0m", "\x1b[1m\x1b[31mwrap\x1b[0m"},
		},
		{
			name:  "SGR in middle of text",
			line:  "normal \x1b[31mred text here\x1b[0m normal",
			width: 14,
			want:  []string{"normal \x1b[31mred\x1b[0m", "\x1b[31mtext here\x1b[0m", "normal"},
		},
		{
			name:  "non-SGR ANSI not replayed",
			line:  "\x1b[2Jhello world long text",
			width: 12,
			want:  []string{"hello world", "long text"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) =\n  got:  %q\n  want: %q", tt.line, tt.width, got, tt.want)
			}
			// Verify: every output line's visual width ≤ width
			for i, l := range got {
				if w := lipgloss.Width(l); w > tt.width && tt.width > 0 {
					t.Errorf("line %d visual width %d exceeds limit %d: %q", i, w, tt.width, l)
				}
			}
		})
	}
}

func TestWrapLineAnsi_WideChars(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{
			name:  "CJK wrap",
			line:  "你好世界你好世界",
			width: 8,
			want:  []string{"你好世界", "你好世界"},
		},
		{
			name:  "CJK mixed with ASCII",
			line:  "hello你好world世界",
			width: 10,
			want:  []string{"hello你好", "world世界"},
		},
		{
			name:  "CJK with SGR",
			line:  "\x1b[32m你好世界你好世界\x1b[0m",
			width: 8,
			want:  []string{"\x1b[32m你好世界\x1b[0m", "\x1b[32m你好世界\x1b[0m"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineAnsi(tt.line, tt.width)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapLineAnsi(%q, %d) =\n  got:  %q\n  want: %q", tt.line, tt.width, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -run "TestWrapLineAnsi" -v 2>&1 | head -20
```

预期：`undefined: wrapLineAnsi` 或类似编译错误

- [ ] **Step 3: 实现 `wrapLineAnsi`**

在 `ui/model.go` 的 `wrapLine` 函数之后（L3023 之后）添加：

```go
// isSGR checks whether an ANSI escape sequence is an SGR (Select Graphic
// Rendition) sequence, i.e. it sets text style attributes like colors.
// SGR sequences match the pattern \x1b[...m where ... is one or more
// semicolon-separated numbers.
func isSGR(seq string) bool {
	// SGR: CSI ... m  where CSI is \x1b[
	if len(seq) < 4 || seq[len(seq)-1] != 'm' {
		return false
	}
	// seq starts with \x1b[, check the parameter part
	params := seq[2 : len(seq)-1]
	if params == "" {
		return true // \x1b[m is SGR reset
	}
	for _, c := range params {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// wrapLineAnsi wraps a line that may contain ANSI escape sequences to fit
// within the given visual width. It preserves all escape sequences intact,
// re-emits active SGR sequences at the start of continuation lines, and
// emits SGR reset at the end of each wrapped line to prevent color bleeding.
//
// Word-wrap: prefers breaking at spaces (U+0020, U+3000). Falls back to
// hard-break at width boundary when no space is found within the segment.
func wrapLineAnsi(line string, width int) []string {
	if width <= 0 || line == "" {
		return []string{line}
	}
	if lipgloss.Width(line) <= width {
		return []string{line}
	}

	var lines []string
	var curLine strings.Builder
	var activeSGRs []string // active SGR sequences to replay on continuation
	visualCol := 0
	lastSpaceIdx := -1           // byte index in curLine of last space in current segment
	curLineSinceLastSpace := 0   // visual columns since last space (for hard-break fallback)
	inEscape := false
	var escBuf strings.Builder
	segmentStart := 0 // byte offset in original line for current segment

	// flushLine appends curLine content as a completed line, resetting state.
	flushLine := func() {
		s := curLine.String()
		// Append SGR reset if the line has active styles
		if len(activeSGRs) > 0 {
			s += "\x1b[0m"
		}
		lines = append(lines, s)
		curLine.Reset()
		// Replay active SGRs at start of next line
		for _, sgr := range activeSGRs {
			curLine.WriteString(sgr)
		}
		visualCol = 0
		lastSpaceIdx = -1
		curLineSinceLastSpace = 0
	}

	runes := []rune(line)
	i := 0
	for i < len(runes) {
		r := runes[i]

		// Handle ANSI escape sequences
		if r == '\x1b' {
			// Capture the full escape sequence
			escBuf.Reset()
			escBuf.WriteRune(r)
			i++
			for i < len(runes) {
				r2 := runes[i]
				escBuf.WriteRune(r2)
				i++
				if (r2 >= 'a' && r2 <= 'z') || (r2 >= 'A' && r2 <= 'Z') {
					break
				}
			}
			seq := escBuf.String()
			curLine.WriteString(seq)

			if isSGR(seq) {
				// Track SGR for replay. SGR reset (\x1b[0m or \x1b[m) clears the stack.
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					activeSGRs = nil
				} else {
					activeSGRs = append(activeSGRs, seq)
				}
			}
			inEscape = false
			continue
		}

		rw := lipgloss.Width(string(r))

		// Check if adding this rune would exceed width
		if visualCol+rw > width {
			if lastSpaceIdx >= 0 {
				// Word-wrap: break at last space
				// Need to rewind curLine to just after lastSpaceIdx
				rewind := curLineSinceLastSpace
				// Trim curLine back to the space position
				curLineStr := curLine.String()
				trimmed := curLineStr[:lastSpaceIdx]
				// The part after the space becomes the start of the next line
				overflow := curLineStr[lastSpaceIdx:]
				// Remove leading space from overflow
				if len(overflow) > 0 && (overflow[0] == ' ' || overflow[0] == '　') {
					overflow = overflow[1:]
				}
				curLine.Reset()
				curLine.WriteString(trimmed)
				flushLine()
				// Start new line with overflow + current rune
				for _, sgr := range activeSGRs {
					curLine.WriteString(sgr)
				}
				curLine.WriteString(overflow)
				curLine.WriteRune(r)
				// Recalculate visual state for the new line
				visualCol = lipgloss.Width(stripAnsi(overflow)) + rw
				lastSpaceIdx = -1
				curLineSinceLastSpace = visualCol
				i++
				continue
			}
			// Hard break: flush current line, start new one with this rune
			flushLine()
			curLine.WriteRune(r)
			visualCol = rw
			if r == ' ' || r == '　' {
				lastSpaceIdx = len(string([]rune(curLine.String()))) - 1
				curLineSinceLastSpace = 0
			} else {
				curLineSinceLastSpace = rw
			}
			i++
			continue
		}

		// Normal: add rune to current line
		curLine.WriteRune(r)
		visualCol += rw
		curLineSinceLastSpace += rw
		if r == ' ' || r == '　' {
			lastSpaceIdx = len(string([]rune(curLine.String()))) - 1
			curLineSinceLastSpace = 0
		}
		i++
	}

	// Flush remaining content
	if curLine.Len() > 0 {
		curLineStr := curLine.String()
		// Strip leading SGR replays if this is the only line (no wrap happened)
		if len(lines) == 0 {
			lines = append(lines, line)
		} else {
			lines = append(lines, curLineStr)
		}
	}

	return lines
}
```

注意：上述实现中的 `stripAnsi` 在 `ui/selection.go` 中定义，需确认在 model.go 中可访问（同一 package `ui`，可直接调用）。

- [ ] **Step 4: 运行测试确认通过**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -run "TestWrapLineAnsi" -v
```

预期：全部 PASS

- [ ] **Step 5: 提交**

```bash
git add ui/model.go ui/model_test.go
git commit -m "feat(ui): add wrapLineAnsi for ANSI-aware word wrapping"
```

---

### Task 2: 修改 `wrapLine` 委托 ANSI 行

**Files:**
- Modify: `ui/model.go:2981-3023` — `wrapLine` 函数

**Interfaces:**
- Consumes: `wrapLineAnsi(line, width) []string` (from Task 1)
- Produces: `wrapLine(line, width) []string` — 行为不变，但对含 ANSI 的行现在正确换行

- [ ] **Step 1: 修改 `wrapLine`**

在 `ui/model.go` 的 `wrapLine` 函数开头（L2982 之后）添加 ANSI 检测：

```go
func wrapLine(line string, width int) []string {
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	// Delegate ANSI-containing lines to wrapLineAnsi for safe wrapping
	if strings.Contains(line, "\x1b[") {
		return wrapLineAnsi(line, width)
	}
	runes := []rune(line)
	// ... rest unchanged
```

- [ ] **Step 2: 运行已有测试确认无回归**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -v -count=1 2>&1 | tail -30
```

预期：所有已有测试 PASS

- [ ] **Step 3: 提交**

```bash
git add ui/model.go
git commit -m "feat(ui): delegate ANSI lines in wrapLine to wrapLineAnsi"
```

---

### Task 3: 移除 `wrapLines` 中的 ANSI 跳过逻辑

**Files:**
- Modify: `ui/model.go:3025-3044` — `wrapLines` 函数

**Interfaces:**
- Consumes: `wrapLine(line, width) []string` (from Task 2, now ANSI-capable)
- Produces: `wrapLines(lines, width) []string` — 不再跳过 ANSI 行

- [ ] **Step 1: 移除 ANSI 跳过分支**

`ui/model.go:3025-3044`，将整个 `wrapLines` 函数简化为：

```go
func wrapLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	result := []string{}
	for _, line := range lines {
		if lipgloss.Width(line) <= width {
			result = append(result, line)
		} else {
			result = append(result, wrapLine(line, width)...)
		}
	}
	return result
}
```

原来的逻辑中，对于短行和超宽行都有处理，但现在 `wrapLine` 内部已经做了 `Width <= width` 的判断并委托 ANSI 行给 `wrapLineAnsi`，所以 `wrapLines` 只需直接调用 `wrapLine` 即可。

- [ ] **Step 2: 运行测试确认无回归**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -v -count=1 2>&1 | tail -30
```

- [ ] **Step 3: 提交**

```bash
git add ui/model.go
git commit -m "fix(ui): remove ANSI skip in wrapLines, delegate to wrapLine"
```

---

### Task 4: 后处理 lipgloss 渲染的面板行

**Files:**
- Modify: `ui/model.go:2369-2370` — `renderSubAgentPanel`
- Modify: `ui/model.go:2423-2424` — `renderMemberProgress`
- Modify: `ui/model.go:2496-2497` — `renderTDDStatus`

**Interfaces:**
- Consumes: `wrapLineAnsi(line, width) []string` (from Task 1)
- Produces: 三个渲染函数输出不再有超宽行

- [ ] **Step 1: 修改三个面板渲染函数**

这三个函数都使用 `ExecBlockStyle.Width(maxWidth).Render(...)` 后 `strings.Split` 成行。lipgloss 的 `Width()` 会截断超宽行。改为渲染后对每行调用 `wrapLineAnsi` 展开。

`renderSubAgentPanel`（L2369-2370），将：

```go
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	return strings.Split(rendered, "\n")
```

改为：

```go
	rendered := ExecBlockStyle.Width(width).Render(strings.Join(content, "\n"))
	rawLines := strings.Split(rendered, "\n")
	var result []string
	for _, l := range rawLines {
		result = append(result, wrapLineAnsi(l, width)...)
	}
	return result
```

`renderMemberProgress`（L2423-2424），同样修改。

`renderTDDStatus`（L2496-2497），同样修改。

- [ ] **Step 2: 运行测试确认无回归**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ -v -count=1 2>&1 | tail -30
```

- [ ] **Step 3: 提交**

```bash
git add ui/model.go
git commit -m "fix(ui): wrap overflow lines from lipgloss-rendered panels"
```

---

### Task 5: 编译验证 + 最终检查

**Files:**
- 无新增修改

- [ ] **Step 1: 完整编译**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```

- [ ] **Step 2: 运行全部测试**

```bash
cd /Users/admin/gitspace/deepact && go test ./ui/ ./engine/ -v -count=1 2>&1 | tail -40
```

- [ ] **Step 3: 检查 vet 和静态分析**

```bash
cd /Users/admin/gitspace/deepact && go vet ./ui/ ./engine/
```
