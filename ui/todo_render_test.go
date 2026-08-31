package ui

import (
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

// TestRenderTodoList_ThreeStates verifies the three plain-text status markers:
// [ ] pending, [~] in_progress, [✓] completed. No emoji.
func TestRenderTodoList_ThreeStates(t *testing.T) {
	items := []engine.TodoItem{
		{Content: "红灯 - 编写失败的测试", Status: "completed"},
		{Content: "绿灯 - 编写最小实现", Status: "in_progress"},
		{Content: "重构 - 清理代码", Status: "pending"},
	}
	lines := renderTodoList(items, 80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty rendered lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "[✓]") {
		t.Errorf("missing [✓] marker for completed item:\n%s", joined)
	}
	if strings.Contains(joined, "[x]") {
		t.Errorf("completed item must not use [x] (reads as failure):\n%s", joined)
	}
	if !strings.Contains(joined, "[~]") {
		t.Errorf("missing [~] marker for in_progress item:\n%s", joined)
	}
	if !strings.Contains(joined, "[ ]") {
		t.Errorf("missing [ ] marker for pending item:\n%s", joined)
	}
	if strings.Contains(joined, "🔴") || strings.Contains(joined, "🟢") || strings.Contains(joined, "✅") {
		t.Errorf("todo list must not contain emoji:\n%s", joined)
	}
	if !strings.Contains(joined, "红灯 - 编写失败的测试") {
		t.Errorf("missing step content:\n%s", joined)
	}
}

// TestRenderTodoList_Empty verifies an empty list renders nothing.
func TestRenderTodoList_Empty(t *testing.T) {
	if got := renderTodoList(nil, 80); got != nil {
		t.Errorf("expected nil for empty todo list, got %v", got)
	}
}
