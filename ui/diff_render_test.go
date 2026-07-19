package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

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

func TestHunkSummaryLine_NoExpandHint(t *testing.T) {
	line := hunkSummaryLine(0, "@@ -1,3 +1,3 @@", 2, 1)
	plain := stripAnsi(line)
	if strings.Contains(plain, "点击展开") {
		t.Errorf("summary line should not contain 点击展开: %q", plain)
	}
	if !strings.Contains(plain, "+2") || !strings.Contains(plain, "-1") {
		t.Errorf("summary line should contain +2 -1: %q", plain)
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

func TestFinishStreaming_SnapshotsToolTree(t *testing.T) {
	m := &Model{
		width:    80,
		height:   24,
		state:    stateReady,
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
