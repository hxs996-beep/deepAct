package engine

import (
	"strings"
	"testing"
)

func TestBuildRunSummary(t *testing.T) {
	tests := []struct {
		name          string
		history       []Message
		toolCallCount int
		zh            bool
		want          string
		wantContains  string // if set, check strings.Contains instead of exact match
		notWant       string // summary must NOT equal this
	}{
		{
			name: "last non-empty assistant content wins",
			history: []Message{
				{Role: "assistant", Content: ""},
				{Role: "assistant", Content: "已修改 2 个文件"},
				{Role: "assistant", Content: ""},
			},
			toolCallCount: 3,
			zh:            true,
			want:          "已修改 2 个文件",
		},
		{
			name: "fall back to reasoning when all content empty",
			history: []Message{
				{Role: "assistant", Content: "", ReasoningContent: "分析: bug 在第 42 行"},
				{Role: "assistant", Content: ""},
			},
			toolCallCount: 2,
			zh:            true,
			want:          "分析: bug 在第 42 行",
		},
		{
			name: "all empty -> diagnostic, not 完成",
			history: []Message{
				{Role: "assistant", Content: ""},
				{Role: "assistant", Content: "", ReasoningContent: ""},
			},
			toolCallCount: 5,
			zh:            true,
			notWant:       "完成",
			wantContains:  "5",
		},
		{
			name:          "empty history -> diagnostic, not Done",
			history:       nil,
			toolCallCount: 0,
			zh:            false,
			notWant:       "Done",
		},
		{
			name: "english diagnostic mentions tool calls",
			history: []Message{
				{Role: "assistant", Content: ""},
			},
			toolCallCount: 4,
			zh:            false,
			notWant:       "Done",
			wantContains:  "4",
		},
	}
	for _, tt := range tests {
		got := buildRunSummary(tt.history, tt.toolCallCount, tt.zh)
		switch {
		case tt.want != "" && got != tt.want:
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		case tt.notWant != "" && got == tt.notWant:
			t.Errorf("%s: got %q, must not equal %q", tt.name, got, tt.notWant)
		case tt.wantContains != "" && !strings.Contains(got, tt.wantContains):
			t.Errorf("%s: got %q, want to contain %q", tt.name, got, tt.wantContains)
		}
	}
}

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
