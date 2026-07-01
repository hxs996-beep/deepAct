# Fix: "Done" + 文件列表但无结论 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 修复分析任务后 UI 显示 "Done" + 已读文件列表但无实际结论的问题

**Architecture:** 三处小改动：(1) `buildRunSummary` 增加质量门槛拒绝空壳 summary，(2) `renderToolSummary` 文本 "Done" → "N tools executed"，(3) `renderMessage` 中 toolsummary role 用 DimStyle 弱化渲染

**Tech Stack:** Go, 无新增依赖

## Global Constraints

- 最小改动，不重构相邻代码
- 不修改公开接口
- 无新增依赖

## File Structure

| 文件 | 职责 |
|------|------|
| `engine/loop.go` | `buildRunSummary` 质量门槛 + 新增 `isSubstantiveSummary` |
| `engine/loop_summary_test.go` | `isSubstantiveSummary` 测试用例 |
| `ui/model.go` | `renderToolSummary` 文本 + `renderMessage` toolsummary 样式 |

---

### Task 1: `isSubstantiveSummary` + `buildRunSummary` 质量门槛

**Files:**
- Modify: `engine/loop.go:788-813`
- Modify: `engine/loop_summary_test.go`

**Interfaces:**
- Produces: `func isSubstantiveSummary(summary string) bool` — 判断 summary 是否有实质内容
- Modifies: `func buildRunSummary(history []Message, toolCallCount int, zh bool) string` — 在 stripDSMLTokens 后调用 isSubstantiveSummary

- [ ] **Step 1: 写 `isSubstantiveSummary` 测试**

```go
func TestIsSubstantiveSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    bool
	}{
		// 长度门槛：英文 < 20 且无中文 → 不通过
		{name: "single word Done", summary: "Done", want: false},
		{name: "short ok", summary: "OK", want: false},
		{name: "single Chinese char", summary: "完成", want: false},

		// 空壳词精确匹配
		{name: "chinese done", summary: "完成", want: false},
		{name: "english done", summary: "Done.", want: false},
		{name: "im done", summary: "I'm done.", want: false},

		// 文件列表回声：≥50% 行为路径模式
		{name: "file list echo", summary: "Done\n- /a/b/c.go\n- /d/e/f.go", want: false},
		{name: "tool icon echo", summary: "[<>] /a/b/c.go\n[<>] /d/e/f.go", want: false},

		// 通过：足够长度的实质内容
		{name: "substantive en", summary: "The root cause is a race condition in the lock acquisition.", want: true},
		{name: "substantive zh", summary: "根因是三个机制叠加导致的，详见下文分析。", want: true},
		{name: "mixed content ok", summary: "分析结果如下：\n\n1. 问题在 loop.go", want: true},

		// 边界：正好在阈值上
		{name: "barely substantive en", summary: "This is the analysis.", want: true},   // 22 chars
		{name: "barely short en", summary: "Done with analysis", want: false},            // 19 chars, 空壳词匹配
		{name: "barely substantive zh", summary: "根因如上所述。", want: true},               // 6 个中文字符
		{name: "empty string", summary: "", want: true}, // 空字符串不拦截，由调用方处理
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSubstantiveSummary(tt.summary)
			if got != tt.want {
				t.Errorf("isSubstantiveSummary(%q) = %v, want %v", tt.summary, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./engine/ -run TestIsSubstantiveSummary -count=1
```
Expected: FAIL — `isSubstantiveSummary` 未定义

- [ ] **Step 3: 实现 `isSubstantiveSummary`**

在 `engine/loop.go` 中，`buildRunSummary` 函数之后添加：

```go
// isSubstantiveSummary checks whether a summary string contains meaningful
// analysis content, as opposed to a bare "Done"/"完成" or an echo of the
// internal read_history block. Returns false for empty-shell summaries that
// should be replaced by the diagnostic fallback.
func isSubstantiveSummary(summary string) bool {
	if summary == "" {
		return true // empty is not "unsubstantive" — caller decides fallback
	}

	trimmed := strings.TrimSpace(summary)

	// Rule 1: length threshold.
	// English text under 20 chars with no CJK → too short to be meaningful.
	// Chinese text under 10 chars → too short.
	hasCJK := false
	for _, r := range trimmed {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
			break
		}
	}
	if hasCJK {
		if len([]rune(trimmed)) < 10 {
			return false
		}
	} else {
		if len(trimmed) < 20 {
			return false
		}
	}

	// Rule 2: bare shell words — exact or nearly exact match.
	shellWords := []string{"done", "完成", "ok", "好的", "i'm done", "im done", "done."}
	lower := strings.ToLower(trimmed)
	for _, w := range shellWords {
		if lower == w {
			return false
		}
	}

	// Rule 3: file-list echo detection.
	// If ≥50% of non-empty lines start with a path-like pattern, treat as echo.
	lines := strings.Split(trimmed, "\n")
	pathLike := 0
	total := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		total++
		// Match lines starting with common path/icon patterns:
		//   - /path/to/file
		//   [<>] path
		//   [@] path
		//   [?] path
		//   [~] path
		//   [>_] path
		if strings.HasPrefix(t, "- /") || strings.HasPrefix(t, "[<>]") ||
			strings.HasPrefix(t, "[@]") || strings.HasPrefix(t, "[?]") ||
			strings.HasPrefix(t, "[~]") || strings.HasPrefix(t, "[>_]") {
			pathLike++
		}
	}
	if total > 0 && pathLike*2 >= total {
		return false
	}

	return true
}
```

需要在文件顶部 import 中添加 `"unicode"`（检查是否已存在）。

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./engine/ -run TestIsSubstantiveSummary -count=1 -v
```
Expected: PASS — 所有 `isSubstantiveSummary` 用例通过

- [ ] **Step 5: 修改 `buildRunSummary` 调用质量门槛**

在 `engine/loop.go:804` 行 `summary = stripDSMLTokens(summary)` 之后，`if summary != ""` 之前，插入：

```go
	summary = stripDSMLTokens(summary)
	if summary != "" && !isSubstantiveSummary(summary) {
		summary = ""
	}
	if summary != "" {
		return summary
	}
```

即第 804-807 行变为：

```go
	summary = stripDSMLTokens(summary)
	if summary != "" && !isSubstantiveSummary(summary) {
		summary = ""
	}
	if summary != "" {
		return summary
	}
```

- [ ] **Step 6: 运行全部 engine 测试**

```bash
go test ./engine/ -count=1 -v
```
Expected: PASS — 包括现有 `TestBuildRunSummary` 和新测试

- [ ] **Step 7: Commit**

```bash
git add engine/loop.go engine/loop_summary_test.go
git commit -m "feat: add isSubstantiveSummary quality gate to buildRunSummary"
```

---

### Task 2: UI 文本和样式 (C2 + C3)

**Files:**
- Modify: `ui/model.go:1604-1614` (renderMessage toolsummary)
- Modify: `ui/model.go:1954` (renderToolSummary)

**Interfaces:**
- Consumes: nothing from Task 1 (独立改动)
- Produces: nothing for later tasks

- [ ] **Step 1: 修改 `renderToolSummary` 文本**

`ui/model.go:1954`，将：
```go
b.WriteString(fmt.Sprintf("● Done (%d tools, %d files modified)\n", len(toolTree), modified))
```
改为：
```go
b.WriteString(fmt.Sprintf("● %d tools executed, %d files modified\n", len(toolTree), modified))
```

- [ ] **Step 2: 修改 `renderMessage` toolsummary 样式**

`ui/model.go:1612`，将：
```go
styled[i] = ToolTreeStyle.Render(line)
```
改为：
```go
styled[i] = DimStyle.Render(line)
```

- [ ] **Step 3: 运行 UI 测试确认无回归**

```bash
go test ./ui/ -count=1 -v
```
Expected: PASS

- [ ] **Step 4: 编译确认**

```bash
go build ./...
```
Expected: 无错误

- [ ] **Step 5: Commit**

```bash
git add ui/model.go
git commit -m "fix: replace Done with neutral tool summary text; dim toolsummary style"
```
