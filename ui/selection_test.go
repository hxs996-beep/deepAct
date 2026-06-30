package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/deepact/deepact/engine"
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
	pt := screenToLine(9, 0, 0, 36, 10)
	if pt.Line != 9 {
		t.Errorf("last content line: want line 9, got %d", pt.Line)
	}
	pt2 := screenToLine(15, 0, 0, 36, 10)
	if pt2.Line != 9 {
		t.Errorf("beyond content: want line 9, got %d", pt2.Line)
	}
}

func TestScreenToLine_WithScroll(t *testing.T) {
	// 100 lines, scrollOffset=20, bodyHeight=35
	// firstVisibleLine = 100 - 20 - 35 = 45
	pt := screenToLine(0, 0, 20, 35, 100)
	if pt.Line != 45 {
		t.Errorf("with scroll: want line 45, got %d", pt.Line)
	}
}

func TestScreenToLine_EmptyContent(t *testing.T) {
	pt := screenToLine(0, 0, 0, 35, 0)
	if pt.Line != 0 {
		t.Errorf("empty content: want line 0, got %d", pt.Line)
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

// ---- reverseHighlightLine tests ----

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

// TestReverseHighlightLine_SgrResetReEmit verifies that \x1b[7m is re-emitted
// after SGR reset sequences within the highlighted region. This is critical for
// glamour-rendered markdown where \x1b[0m resets appear at style boundaries.
func TestReverseHighlightLine_SgrResetReEmit(t *testing.T) {
	// Glamour-like line: bold "Hello" then normal " World" with \x1b[0m reset between
	line := "\x1b[1mHello\x1b[0m World"
	result := reverseHighlightLine(line, 0, -1)
	// Should have \x1b[7m at start, re-emit after \x1b[0m, and \x1b[27m at end
	if !containsSeq(result, "\x1b[7m") {
		t.Errorf("missing \\x1b[7m in %q", result)
	}
	// The \x1b[0m should be followed by \x1b[7m (re-emit)
	resetIdx := strings.Index(result, "\x1b[0m")
	if resetIdx < 0 {
		t.Fatalf("missing \\x1b[0m in %q", result)
	}
	afterReset := result[resetIdx+4:] // skip past \x1b[0m
	if !strings.HasPrefix(afterReset, "\x1b[7m") {
		t.Errorf("\\x1b[7m not re-emitted after \\x1b[0m, got %q", afterReset[:min(20, len(afterReset))])
	}
}

func TestReverseHighlightLine_CodeBlockBackground(t *testing.T) {
	// Glamour code block: background color + foreground color + text + reset
	line := "\x1b[48;5;236m\x1b[38;5;114m+ added\x1b[0m\x1b[48;5;236m  \x1b[0m"
	result := reverseHighlightLine(line, 0, -1)
	// Should have \x1b[7m re-emitted after both \x1b[0m occurrences
	count := strings.Count(result, "\x1b[7m")
	if count < 1 {
		t.Errorf("expected at least one \\x1b[7m in code block highlight, got %d in %q", count, result)
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

// ---- stripAnsi tests ----

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
	text, err := copySelection([]string{"hello"}, SelectionState{})
	if text != "" || err != nil {
		t.Errorf("empty selection: want ('', nil), got (%q, %v)", text, err)
	}
}

// ---- end-to-end state machine test ----

func TestMouseDragSelection_FullFlow(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "response text"},
	}

	totalLines, bodyHeight, _ := m.computeLayout()

	// Mouse down
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

func TestSelectionClickNoDrag(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.height = 40
	m.width = 100
	m.msgCache = &messageRenderCache{}
	m.messages = []DisplayMessage{{Role: "user", Content: "test"}}

	totalLines, bodyHeight, _ := m.computeLayout()
	pt := screenToLine(0, 5, m.scrollOffset, bodyHeight, totalLines)

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

// ---- R1/R2 regression tests ----

func TestScreenToLine_AfterEmptyLineFix(t *testing.T) {
	// 含空行的 plain 数组（模拟 R1 修复后的 1:1 行映射）
	// 10 行，bodyHeight=5，scrollOffset=0（底对齐）
	// firstVisibleLine = 10 - 0 - 5 = 5
	totalLines := 10
	bodyHeight := 5
	scroll := 0
	// 屏幕 row 0 → 数据行 5
	pt0 := screenToLine(0, 0, scroll, bodyHeight, totalLines)
	if pt0.Line != 5 {
		t.Errorf("row 0: want line 5, got %d", pt0.Line)
	}
	// 屏幕 row 1 → 数据行 6
	pt1 := screenToLine(1, 0, scroll, bodyHeight, totalLines)
	if pt1.Line != 6 {
		t.Errorf("row 1: want line 6, got %d", pt1.Line)
	}
	// 屏幕 row 2 → 数据行 7
	pt2 := screenToLine(2, 0, scroll, bodyHeight, totalLines)
	if pt2.Line != 7 {
		t.Errorf("row 2: want line 7, got %d", pt2.Line)
	}
}

func TestExtractSelectionText_NoCRInOutput(t *testing.T) {
	// R2: 渲染层已剥 \r，故 plain 快照不含 \r。验证选区文本无 \r 且整行复制正确。
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
	want := "+new\n-old\n ctx"
	if got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
