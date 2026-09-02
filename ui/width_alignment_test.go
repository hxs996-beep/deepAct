package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/deepact/deepact/engine"
)

// 本文件护栏：全项目宽度口径必须与 ui 侧唯一的 termWidthCond 对齐
// （EastAsianWidth=true → ambiguous 宽度字符按 2 列；Box Drawing U+2500-0x259F
// 钉 1 列）。iTerm2 等 CJK 环境把 — • → “ ” ← · 等歧义宽度字符渲染为 2 列；
// 任何残留 lipgloss.Width / ansi.StringWidth / len()（字节）计宽的路径都会
// 产生列漂移 → 点击 A 行高亮 B 行、滚动重复行（见根因分析报告）。

// ---- selection.go：高亮列映射口径 ----

// TestReverseHighlightLine_AmbiguousStartsHighlightAtTermColumn 验证高亮起点
// 按终端物理列（ambiguous=2）定位：列 4 必须落在第二个 em dash 上。
// 修复前 lipgloss.Width 把 — 计 1 列，列 4 会落在空格上。
func TestReverseHighlightLine_AmbiguousStartsHighlightAtTermColumn(t *testing.T) {
	// 终端列: A=0, ' '=1, —=2..3, —=4..5, ' '=6, B=7
	line := "A —— B"
	got := reverseHighlightLine(line, 4, -1)
	if strings.Contains(got, "\x1b[7m ") {
		t.Fatalf("highlight must not start on a space at terminal col 4, got %q", got)
	}
	if !strings.Contains(got, "\x1b[7m—") {
		t.Fatalf("highlight should start on em dash at terminal col 4, got %q", got)
	}
}

// TestSliceByVisualCol_AmbiguousTerminalColumns 验证列切片按终端口径。
// 终端列 [2,6) 恰好是两个 em dash。
func TestSliceByVisualCol_AmbiguousTerminalColumns(t *testing.T) {
	line := "A —— B" // 终端列: A0 ' '1 —2..3 —4..5 ' '6 B7
	got := sliceByVisualCol(line, 2, 6)
	if got != "——" {
		t.Fatalf("slice [2,6) should be exactly the two em dashes, got %q", got)
	}
}

// TestTruncateVisual_AmbiguousCountsTwoColumns 验证 truncateVisual 中
// ambiguous 按 2 列计：4 列预算只容得下 A + 一个 em dash。
func TestTruncateVisual_AmbiguousCountsTwoColumns(t *testing.T) {
	got := truncateVisual("A——B", 4) // A=1 —=2 —=2 B=1
	if got != "A—" {
		t.Fatalf("truncate at 4 terminal columns should keep 'A—', got %q", got)
	}
}

// ---- model.go：truncateAnsi 计宽口径 ----

// TestTruncateAnsi_CountsWideRunesAsTwoColumns 验证 truncateAnsi 按
// EastAsian 口径计宽：6 列预算保留 3 个 CJK 字符（修复前每 rune 计 1，
// 会保留 6 个字符实际占 12 列而溢出）。
func TestTruncateAnsi_CountsWideRunesAsTwoColumns(t *testing.T) {
	s := "\x1b[31m一二三四五\x1b[0m" // 10 terminal columns visible
	got := truncateAnsi(s, 6)
	if plain := stripAnsi(got); plain != "一二三" {
		t.Fatalf("6 columns should keep 3 CJK runes, got %q", plain)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("trailing reset sequence must survive truncation, got %q", got)
	}
}

// ---- model.go：renderThinkingBox 截断口径 ----

// TestRenderThinkingBox_AmbiguousTruncateMatchesTermWidth 验证超宽截断按终端
// 口径执行并保留 "…" 标记（修复前 ansi.Truncate 把 — 计 1 列，截出 45 个 —
// 实际渲染 90 列溢出）。该函数当前无调用方（被 renderMemberProgress 取代），
// 测试护栏其口径以防复用时回归。
func TestRenderThinkingBox_AmbiguousTruncateMatchesTermWidth(t *testing.T) {
	width := 51 // blockWidth = width - 6 = 45
	activity := strings.Repeat("—", 30)
	lines := renderThinkingBox(activity, width)
	if len(lines) == 0 {
		t.Fatal("renderThinkingBox returned no lines")
	}
	plain := stripAnsi(lines[0]) // [style pad]"  " + display
	idx := strings.Index(plain, "…")
	if idx < 0 {
		t.Fatalf("truncation must keep an ellipsis marker, got %q", plain)
	}
	// display 预算是 blockWidth-2（为 "…" 留 2 列）；剥离样式 pad 后核对。
	display := strings.TrimLeft(plain[:idx], " ")
	if w := displayWidth(display); w > width-6-2 {
		t.Fatalf("truncated display exceeds blockWidth-2: displayWidth=%d, want <= %d, got %q", w, width-6-2, display)
	}
}

// ---- model.go：footerHeight 与 View footer 成员一致性 ----

// TestFooterHeight_IncludesResumePopup 验证 footerHeight() 与 View 一致计入
// resume 弹窗（View Step2 计入，修复前 footerHeight() 遗漏 → 点击映射整体
// 平移）。
func TestFooterHeight_IncludesResumePopup(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.width = 120
	m.height = 40
	ready := m.footerHeight()

	m.state = stateResume
	m.resumeSessions = []SessionSummary{{
		ID:        "s1",
		UpdatedAt: time.Now(),
		FirstMsg:  "hello world",
	}}
	withResume := m.footerHeight()

	if withResume <= ready {
		t.Fatalf("footerHeight must include resume popup: ready=%d, resume=%d", ready, withResume)
	}
}
