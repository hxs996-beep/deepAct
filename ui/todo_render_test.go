package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/deepact/deepact/engine"
)

// TestRenderStepsPanel_ThreeStates verifies the three plain-text status markers:
// [ ] pending, [~] in_progress, [✓] completed. No emoji.
func TestRenderStepsPanel_ThreeStates(t *testing.T) {
	items := []engine.TodoItem{
		{Content: "红灯 - 编写失败的测试", Status: "completed"},
		{Content: "绿灯 - 编写最小实现", Status: "in_progress"},
		{Content: "重构 - 清理代码", Status: "pending"},
	}
	lines := renderStepsPanel(items, nil, 80, true)
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

// TestRenderStepsPanel_Empty verifies an empty panel renders nothing.
func TestRenderStepsPanel_Empty(t *testing.T) {
	if got := renderStepsPanel(nil, nil, 80, true); got != nil {
		t.Errorf("expected nil for an empty panel, got %v", got)
	}
}

// TestRenderStepsPanel_NestsByStepID: 子代理按 SubAgentStatus.Step 缩进到对应
// 步骤下；step 为空或已失配的进游离组，标题复用 Sub-Agents。
func TestRenderStepsPanel_NestsByStepID(t *testing.T) {
	items := []engine.TodoItem{
		{ID: "plan", Content: "规划阶段", Status: "completed"},
		{ID: "dir-1", Content: "方向1 调研", Status: "in_progress"},
		{Content: "汇总交叉分析", Status: "pending"},
	}
	agents := []SubAgentStatus{
		{RunID: "bg-1", Agent: "researcher", Goal: "调研方向1", Step: "dir-1", Status: "running"},
		{RunID: "bg-2", Agent: "critic", Goal: "审查", Status: "running"},
		{RunID: "bg-3", Agent: "researcher", Goal: "失配", Step: "gone", Status: "done", Summary: "小结"},
	}
	lines := renderStepsPanel(items, agents, 80, true)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(stripAnsi(l), " ")
	}
	joined := strings.Join(plain, "\n")

	idx := func(sub string) int {
		for i, l := range plain {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("line %q not found in:\n%s", sub, joined)
		return -1
	}
	// bg-1 挂在方向1 之下：顺序必须是 方向1 → bg-1 → 汇总。
	if !(idx("方向1 调研") < idx("bg-1") && idx("bg-1") < idx("汇总交叉分析")) {
		t.Errorf("bg-1 must nest between its step and the next step:\n%s", joined)
	}
	// 游离组在步骤列表之后，且保留独立标题。
	if !(idx("汇总交叉分析") < idx("Sub-Agents") && idx("Sub-Agents") < idx("bg-2") && idx("bg-2") < idx("bg-3")) {
		t.Errorf("unlinked agents must follow the steps under the Sub-Agents heading:\n%s", joined)
	}
	if !strings.Contains(joined, "[::] Steps") {
		t.Errorf("missing Steps heading:\n%s", joined)
	}
}

// TestRenderStepsPanel_OrphanOnly: 没有步骤列表时退化为原有的子代理面板。
func TestRenderStepsPanel_OrphanOnly(t *testing.T) {
	lines := renderStepsPanel(nil, []SubAgentStatus{
		{RunID: "bg-1", Agent: "sub", Goal: "批判性审查", Status: "running"},
	}, 80, true)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(stripAnsi(l), " ")
	}
	joined := strings.Join(plain, "\n")
	if strings.Contains(joined, "[::] Steps") {
		t.Errorf("no step list must render no Steps heading:\n%s", joined)
	}
	if !strings.Contains(joined, "Sub-Agents") || !strings.Contains(joined, "[s]") {
		t.Errorf("orphan agents must keep the Sub-Agents heading:\n%s", joined)
	}
}

// TestRenderStepsPanel_MultilineSummaryStaysOneRow: done 行的 Summary 是子代理
// 的原始结论（多行 markdown）。它必须折叠成单行，否则一行子代理会渲染成多行，
// 内层样式的 reset 会截断 ExecBlockStyle 背景，续行看起来与相邻行不同。
func TestRenderStepsPanel_MultilineSummaryStaysOneRow(t *testing.T) {
	summary := "# dir-6 研究小结：持久化检索\n\n## 总体结论\n\ndsh 在本方向的核心机制是 **缓存**。"
	lines := renderStepsPanel(nil, []SubAgentStatus{
		{RunID: "bg-1", Agent: "researcher", Status: "done", Summary: summary},
	}, 200, true)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimRight(stripAnsi(l), " ")
	}
	joined := strings.Join(plain, "\n")

	var rows []string
	for _, l := range plain {
		if strings.Contains(l, "bg-1") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("multi-line summary must render as exactly one bg-1 row, got %d:\n%s", len(rows), joined)
	}
	row := rows[0]
	if !strings.Contains(row, "dir-6 研究小结") || !strings.Contains(row, "总体结论") {
		t.Errorf("collapsed summary must sit on the agent row:\n%s", row)
	}
	for _, l := range plain {
		if strings.Contains(l, "##") || strings.Contains(l, "**") {
			t.Errorf("markdown markers leaked into the panel:\n%s", joined)
		}
	}
}

// TestRenderStepsPanel_ErrorStatus: error/cancelled 用 ✗，不再是 ✓。
func TestRenderStepsPanel_ErrorStatus(t *testing.T) {
	lines := renderStepsPanel(nil, []SubAgentStatus{
		{RunID: "bg-9", Agent: "researcher", Status: "error", Summary: "limit reached"},
	}, 80, true)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "✗") {
		t.Errorf("failed run must render ✗:\n%s", joined)
	}
	if strings.Contains(joined, "✓") {
		t.Errorf("failed run must not render ✓:\n%s", joined)
	}
}

// TestTodoLine_InProgressIsOnlyActiveWhileRunning: 活动色（SpinnerStyle）表示"正在
// 跑"。run 结束后没有任何步骤在推进，同一个 in_progress 项必须退成暗色——否则
// 收尾那一刻的残留快照会被读成"卡在第一步还在跑"。
func TestTodoLine_InProgressIsOnlyActiveWhileRunning(t *testing.T) {
	// Style assertions need colors on: tests run without a TTY, so lipgloss
	// otherwise renders every style as plain text (same convention as
	// TestPasteModeValueNoBareReset).
	lipgloss.SetColorProfile(termenv.ANSI256)

	it := engine.TodoItem{ID: "s1", Content: "步骤一", Status: "in_progress"}

	running := todoLine(it, true)
	if !strings.Contains(running, SpinnerStyle.Render(it.Content)) {
		t.Errorf("running in_progress must use the active style, got %q", running)
	}

	idle := todoLine(it, false)
	if strings.Contains(idle, SpinnerStyle.Render(it.Content)) {
		t.Errorf("an idle panel must not style in_progress as active, got %q", idle)
	}
	if !strings.Contains(idle, DimStyle.Render(it.Content)) {
		t.Errorf("idle in_progress must fall back to the dim style, got %q", idle)
	}
	if !strings.Contains(stripAnsi(idle), "[~]") {
		t.Errorf("status marker must stay [~] (the model's own status), got %q", idle)
	}
}

// TestRenderStepsPanel_FrozenSnapshotIsLabelled: run 已结束时，只要面板上还有未打勾
// 的步骤，就必须标明这是最后一次快照——否则用户看到的就是"一直卡在第一个步骤"。
// 全部打勾的面板无需解释。
func TestRenderStepsPanel_FrozenSnapshotIsLabelled(t *testing.T) {
	render := func(items []engine.TodoItem, running bool) string {
		return stripAnsi(strings.Join(renderStepsPanel(items, nil, 80, running), "\n"))
	}
	unfinished := []engine.TodoItem{
		{ID: "s1", Content: "步骤一", Status: "in_progress"},
		{ID: "s2", Content: "步骤二", Status: "pending"},
	}

	if got := render(unfinished, true); strings.Contains(got, "最后一次快照") {
		t.Errorf("a panel whose run is still going must not claim to be frozen:\n%s", got)
	}
	got := render(unfinished, false)
	if !strings.Contains(got, "最后一次快照") {
		t.Errorf("a frozen panel with unfinished steps must say so:\n%s", got)
	}

	settled := []engine.TodoItem{{ID: "s1", Content: "步骤一", Status: "completed"}}
	if got := render(settled, false); strings.Contains(got, "最后一次快照") {
		t.Errorf("an all-completed panel needs no snapshot note:\n%s", got)
	}
}
