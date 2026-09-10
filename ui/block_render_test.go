package ui

import (
	"strings"
	"testing"
)

// 方案 D 目标：assistant 消息按 markdown 顶层块渲染，杜绝 glamour 把相邻
// 普通文本行合并为段落、折行时把下一行内容拼到上一行末尾。
//
// 复现场景（用户报告）：无空行分隔的 "•  " 行（U+2022 不是 markdown 列表
// 语法），glamour 视为同一段落，行超宽折行时把 "• grep ..." 拼到上一行
// 末尾（"验收） • grep 验收..."）。
func TestRenderAssistant_NoParagraphConcatenation(t *testing.T) {
	l1 := "  •  go build  报  pendingConfirmOptions  未使用（虽 Go 允许未使用结构体字段，但残留旧符号违背\"无残留引用\"验收）"
	l2 := "  • grep 验收（预期无输出）失败，执行者卡在验收步骤"
	content := l1 + "\n" + l2
	lead := string([]rune(strings.TrimSpace(l2))[:6]) // "• grep"

	for _, w := range []int{110, 114, 125, 133} {
		lines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, w)
		for i, l := range lines {
			trim := strings.TrimLeft(stripAnsi(l), " ")
			if strings.Contains(trim, lead) && !strings.HasPrefix(trim, lead) {
				t.Errorf("width=%d line %d: 下一行片段 %q 被拼到本行行尾: %q",
					w, i, lead, strings.TrimRight(stripAnsi(l), " "))
			}
		}
	}
}

// 泛化场景（不枚举字符）：相邻普通文本行（无空行分隔、含空格、超宽折行）
// 不得被 glamour 合并为段落、把下一行内容拼到上一行末尾。这与 // 注释行、
// • 列表行是同一规律——无 markdown 块语义的连续行必须各自独立。
func TestRenderAssistant_AdjacentLinesNotMerged(t *testing.T) {
	l1 := "line one with lots of words that will wrap at spaces when the line is wider than the terminal width threshold used for display"
	l2 := "line two with more words that must never be appended to the end of line one wrapped continuation"
	content := l1 + "\n" + l2
	lead := string([]rune(strings.TrimSpace(l2))[:6]) // "line t"

	for _, w := range []int{60, 80, 100, 110} {
		lines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, w)
		for i, l := range lines {
			trim := strings.TrimLeft(stripAnsi(l), " ")
			if strings.Contains(trim, lead) && !strings.HasPrefix(trim, lead) {
				t.Errorf("width=%d line %d: 第二行片段 %q 被拼到本行行尾: %q",
					w, i, lead, strings.TrimRight(stripAnsi(l), " "))
			}
		}
	}
}

// 表格必须整体作为一个块渲染：markdown 表格（header + separator + rows）
// 若被拆成独立行，glamour 对单行 "| A | B |" 无法识别为表格，输出会丢失
// 框线 │ 与对齐。方案 D 的块切分必须保留表格完整性。
func TestRenderAssistant_TableKeepsBoxDrawing(t *testing.T) {
	content := "| A | B |\n|---|---|\n| 1 | 2 |"
	for _, w := range []int{40, 60, 80} {
		lines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, w)
		joined := stripAnsi(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "│") {
			t.Errorf("width=%d 表格框线 │ 缺失，表格被拆散: %q", w, joined)
		}
		if !strings.Contains(joined, "A") || !strings.Contains(joined, "1") {
			t.Errorf("width=%d 表格内容缺失: %q", w, joined)
		}
	}
}

// 代码围栏必须整体作为一个块：围栏内是代码，markdown 语法（#、**）不得被
// 解释渲染。若围栏被拆成独立行，glamour 会把 # 当标题、** 当加粗处理，
// 破坏代码内容（# 被消费、** 被消费）。
func TestRenderAssistant_CodeFenceMarkdownNotInterpreted(t *testing.T) {
	content := "```\n# not a heading\n**not bold**\n```"
	for _, w := range []int{40, 60, 80} {
		lines := renderMessage(DisplayMessage{Role: "assistant", Content: content}, w)
		joined := stripAnsi(strings.Join(lines, "\n"))
		if !strings.Contains(joined, "# not a heading") {
			t.Errorf("width=%d 围栏内 # 标题被渲染，应保持字面: %q", w, joined)
		}
		if !strings.Contains(joined, "**not bold**") {
			t.Errorf("width=%d 围栏内 ** 加粗被渲染，应保持字面: %q", w, joined)
		}
	}
}
