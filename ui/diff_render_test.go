package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestRenderDiffBlock_NoPadToTerminalWidth(t *testing.T) {
	// R3: renderDiffBlock 不应再 pad 到 m.width，宽度交由 View 统一 Truncate。
	// 构造一个已 Done 的 edit 节点带 hunk
	m := Model{width: 200} // 故意设很大的 width
	nodes := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
	}}
	got := m.renderDiffBlock(nodes, 80)
	// 每行显示宽度不应被 pad 到 200；短行应保持短（<= 80）
	for i, line := range got {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("第 %d 行被 pad 到 %d 宽 (>80): %q", i, w, line)
		}
	}
}

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
	plain := make([]string, len(got))
	for i, l := range got {
		plain[i] = stripAnsi(l)
	}
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
	for i, l := range plain {
		if strings.Contains(l, "old") || strings.Contains(l, "new") || strings.Contains(l, "extra") {
			t.Errorf("第 %d 行泄漏了 hunk 内容（应折叠）: %q", i, l)
		}
	}
	// 应有两个 [N] 摘要行
	summaryCount := 0
	for _, l := range plain {
		if strings.Contains(l, "[1]") || strings.Contains(l, "[2]") {
			summaryCount++
		}
	}
	if summaryCount != 2 {
		t.Errorf("摘要行数: want 2, got %d (%v)", summaryCount, plain)
	}
}

func TestRenderDiffViewer_RendersHunkFullscreen(t *testing.T) {
	m := Model{width: 80, height: 24}
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", Detail: "@@ -1,2 +1,2 @@", DetailFull: "@@ -1,2 +1,2 @@\n-old\n+new"}},
	}}
	m.diffViewerActive = true
	m.diffViewerHunk = hunkHit{msgIdx: -1, nodeIdx: 0, childIdx: 0}
	lines := m.renderDiffViewer(78)
	if len(lines) == 0 {
		t.Fatal("renderDiffViewer 返回空")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripAnsi(joined), "+new") {
		t.Errorf("全屏 viewer 缺少 +new: %q", joined)
	}
	if !strings.Contains(stripAnsi(joined), "-old") {
		t.Errorf("全屏 viewer 缺少 -old: %q", joined)
	}
}

func TestHitTestHunk(t *testing.T) {
	m := Model{width: 80}
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{
			{Name: "hunk", Detail: "@@ -1,2 +1,2 @@", DetailFull: "@@ -1,2 +1,2 @@\n-old\n+new"},
			{Name: "hunk", Detail: "@@ -10,2 +10,2 @@", DetailFull: "@@ -10,2 +10,2 @@\n-old2\n+new2"},
		},
	}}
	// Render the collapsed block; strip ANSI to get plain summary lines.
	blockLines := m.renderDiffBlock(m.toolTree, 80)
	bodyPlain := make([]string, len(blockLines))
	for i, l := range blockLines {
		bodyPlain[i] = stripAnsi(l)
	}
	// Find the [1] and [2] summary lines.
	idx1, idx2 := -1, -1
	for i, l := range bodyPlain {
		if strings.Contains(l, "[1]") {
			idx1 = i
		}
		if strings.Contains(l, "[2]") {
			idx2 = i
		}
	}
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("未找到摘要行: %v", bodyPlain)
	}
	hit1, ok := hitTestHunk(idx1, bodyPlain, m.toolTree)
	if !ok {
		t.Errorf("hitTestHunk([1]) 未命中")
	} else if hit1.childIdx != 0 {
		t.Errorf("hit1.childIdx: want 0, got %d", hit1.childIdx)
	}
	hit2, ok := hitTestHunk(idx2, bodyPlain, m.toolTree)
	if !ok {
		t.Errorf("hitTestHunk([2]) 未命中")
	} else if hit2.childIdx != 1 {
		t.Errorf("hit2.childIdx: want 1, got %d", hit2.childIdx)
	}
	// A non-summary line should not hit.
	if _, ok := hitTestHunk(0, bodyPlain, m.toolTree); ok {
		t.Errorf("非摘要行不应命中")
	}
}

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

func TestRenderToolSummary_CollapsesHunks(t *testing.T) {
	toolTree := []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", Detail: "@@ -1,3 +1,3 @@", DetailFull: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n+extra"}},
	}}
	got := renderToolSummary(toolTree)
	if strings.Contains(got, "old") || strings.Contains(got, "new") || strings.Contains(got, "extra") {
		t.Errorf("toolsummary 泄漏了 hunk 内容（应折叠）: %q", got)
	}
	if !strings.Contains(got, "[1]") || !strings.Contains(got, "+2") || !strings.Contains(got, "-1") {
		t.Errorf("toolsummary 缺少摘要 [1] +2 -1: %q", got)
	}
}

func TestRenderDiffViewer_FromMessageSnapshot(t *testing.T) {
	m := Model{width: 80, height: 24}
	m.messages = []DisplayMessage{{
		Role:    "toolsummary",
		Content: "summary",
		ToolTree: []ToolNode{{
			Name:     "edit",
			Done:     true,
			Detail:   "foo.go",
			Children: []ToolNode{{Name: "hunk", Detail: "@@ -1,2 +1,2 @@", DetailFull: "@@ -1,2 +1,2 @@\n-old\n+new"}},
		}},
	}}
	m.diffViewerActive = true
	m.diffViewerHunk = hunkHit{msgIdx: 0, nodeIdx: 0, childIdx: 0}
	lines := m.renderDiffViewer(78)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripAnsi(joined), "+new") {
		t.Errorf("viewer 从消息快照取 hunk 失败，缺少 +new: %q", joined)
	}
}

func TestFinishStreaming_SnapshotsToolTree(t *testing.T) {
	m := &Model{
		width:  80,
		height: 24,
		state:  stateReady,
		msgCache: &messageRenderCache{},
	}
	m.toolTree = []ToolNode{{
		Name:     "edit",
		Done:     true,
		Detail:   "foo.go",
		Children: []ToolNode{{Name: "hunk", DetailFull: "@@ -1,1 +1,1 @@\n-old\n+new"}},
	}}
	m.finishStreaming(EngineResponseMsg{})
	if len(m.toolTree) != 0 {
		t.Errorf("finishStreaming 后 toolTree 应清空, got %d", len(m.toolTree))
	}
	// Find the toolsummary message (finishStreaming may append other system msgs).
	var summary *DisplayMessage
	for i := range m.messages {
		if m.messages[i].Role == "toolsummary" {
			summary = &m.messages[i]
			break
		}
	}
	if summary == nil {
		t.Fatalf("未找到 toolsummary 消息")
	}
	if summary.ToolTree == nil {
		t.Fatalf("toolsummary 消息应含 ToolTree 快照")
	}
	if len(summary.ToolTree[0].Children) != 1 {
		t.Errorf("快照 Children 应有 1 个 hunk, got %d", len(summary.ToolTree[0].Children))
	}
}
