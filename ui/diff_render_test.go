package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// buildHunk 构造一个含空 context 行的 hunk 内容用于测试。
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
	// delete 行应含行号 2 + "-old"
	plainDel := stripAnsi(got[2])
	if !strings.Contains(plainDel, "2") || !strings.Contains(plainDel, "-old") {
		t.Errorf("delete 行号/内容错: %q", plainDel)
	}
	// insert 行应含 +new
	plainIns := stripAnsi(got[3])
	if !strings.Contains(plainIns, "+new") {
		t.Errorf("insert 内容错: %q", plainIns)
	}
}

func TestRenderDiffHunkBlock_HardTruncatesLongLine(t *testing.T) {
	// 一行超长 insert，maxWidth=20
	long := strings.Repeat("x", 80)
	hunk := "@@ -1,1 +1,1 @@\n+" + long
	got := renderDiffHunkBlock(hunk, 20)
	if len(got) != 2 {
		t.Fatalf("want 2 行 (header+insert), got %d", len(got))
	}
	insertLine := got[1]
	if w := ansi.StringWidth(stripAnsi(insertLine)); w > 20 {
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
		if w := ansi.StringWidth(stripAnsi(line)); w > 20 {
			t.Errorf("第 %d 行显示宽度 %d 超过 20: %q", i, w, line)
		}
	}
}
