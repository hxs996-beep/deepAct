package engine

import (
	"testing"
)

func TestStripInternalPromptEcho(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no internal blocks -> unchanged",
			in:   "我已经分析了代码，发现 bug 在第 42 行。",
			want: "我已经分析了代码，发现 bug 在第 42 行。",
		},
		{
			name: "strip environment block echo",
			in:   "## Environment\n- OS: darwin\n- Arch: arm64\n\n真实的分析结论在这里。",
			want: "真实的分析结论在这里。",
		},
		{
			name: "strip task reminder block echo",
			in:   "[TASK REMINDER]\nGoal: 修复bug\nModified: turn.go\n\n修复完成。",
			want: "修复完成。",
		},
		{
			name: "strip block B echo",
			in:   "# Block B: Runtime Context\n## Task State\n{\"turn\":3}\n\n结论。",
			want: "结论。",
		},
		{
			name: "strip legacy angle-bracket task reminder tags",
			in:   "<TASK REMINDER>\n目标：修复\n</TASK REMINDER>\n\ndone.",
			want: "done.",
		},
		{
			name: "strip read-history exact header echo",
			in:   "Files already read\n- main.go\n\n分析完成。",
			want: "分析完成。",
		},
		{
			name: "strip read-history colon header echo",
			in:   "Files already read:\n- main.go\n\n分析完成。",
			want: "分析完成。",
		},
		{
			name: "do NOT strip natural sentence with read-history phrase",
			in:   "已读文件显示代码结构如下：\n\nmain.go 中定义了...",
			want: "已读文件显示代码结构如下：\n\nmain.go 中定义了...",
		},
		{
			name: "do NOT strip natural English sentence with read-history phrase",
			in:   "Files already read show the following structure:\n\nmain.go defines...",
			want: "Files already read show the following structure:\n\nmain.go defines...",
		},
		{
			name: "strip exact 已读文件 header followed by content",
			in:   "已读文件\n- main.go\n\n分析结果。",
			want: "分析结果。",
		},
		{
			name: "pure internal echo -> empty",
			in:   "## Environment\n- OS: darwin\n",
			want: "",
		},
		{
			name: "multiple internal blocks with real text between",
			in:   "## Environment\n- OS: darwin\n\n中间结论。\n[TASK REMINDER]\nGoal: x\n\n结尾。",
			want: "中间结论。\n结尾。",
		},
		{
			name: "strip reminder on tool usage echo",
			in:   "## Reminder on tool usage\n- 先读文件\n- 验证 API 存在\n\n分析完成。",
			want: "分析完成。",
		},
		{
			name: "strip blocks with leading whitespace",
			in:   "  ## Recent Actions (to prevent re-reading)\n  1. Read main.go\n\n  第3行链接错了。",
			want: "第3行链接错了。",
		},
		{
			name: "strip mixed whitespace and real content",
			in:   "  ## Environment\n  - OS: darwin\n\n  ## Reminder on tool usage\n  - 先读文件\n\n真实输出。",
			want: "真实输出。",
		},
	}
	for _, tt := range tests {
		got := stripInternalPromptEcho(tt.in)
		if got != tt.want {
			t.Errorf("%s:\n got = %q\nwant = %q", tt.name, got, tt.want)
		}
	}
}
