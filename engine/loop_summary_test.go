package engine

import (
	"strings"
	"testing"
)

func TestBuildRunSummary(t *testing.T) {
	tests := []struct {
		name          string
		history       []Message
		startIdx      int
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
			startIdx:      0,
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
			startIdx:      0,
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
			startIdx:      0,
			toolCallCount: 5,
			zh:            true,
			notWant:       "完成",
			wantContains:  "5",
		},
		{
			name:          "empty history -> diagnostic, not Done",
			history:       nil,
			startIdx:      0,
			toolCallCount: 0,
			zh:            false,
			notWant:       "Done",
		},
		{
			name: "english diagnostic mentions tool calls",
			history: []Message{
				{Role: "assistant", Content: ""},
			},
			startIdx:      0,
			toolCallCount: 4,
			zh:            false,
			notWant:       "Done",
			wantContains:  "4",
		},
		// --- 新增：轮次边界 ---
		{
			// 本轮只有空 Content 的 assistant（裸工具调用被剥空）+ tool 结果，
			// 旧轮旁白在 startIdx 之前 → 必须返回诊断串，不能回吐旧旁白。
			name: "run boundary: stale narration from prior run not returned",
			history: []Message{
				{Role: "assistant", Content: "关键路径在 sub_agent.go 产生 stream_delta"}, // idx 0：上一轮
				{Role: "user", Content: "继续"},                                              // idx 1
				{Role: "assistant", Content: ""},                                             // idx 2：本轮，裸工具调用剥空
				{Role: "tool", Content: "file contents"},                                    // idx 3
				{Role: "assistant", Content: ""},                                             // idx 4：本轮，剥空
			},
			startIdx:      2,
			toolCallCount: 1,
			zh:            true,
			notWant:       "关键路径在 sub_agent.go 产生 stream_delta",
			wantContains:  "1",
		},
		{
			// 本轮有正文 → 本轮正文胜出，不取上一轮旁白。
			name: "run boundary: this run's content wins over prior run",
			history: []Message{
				{Role: "assistant", Content: "旧旁白"},        // idx 0：上一轮
				{Role: "user", Content: "继续"},               // idx 1
				{Role: "assistant", Content: "本轮新分析结果"}, // idx 2：本轮
			},
			startIdx:      2,
			toolCallCount: 0,
			zh:            true,
			want:          "本轮新分析结果",
		},
		{
			// 本轮无 assistant 消息（仅 tool）→ 诊断串，不回吐旧旁白。
			name: "run boundary: no assistant in this run -> diagnostic",
			history: []Message{
				{Role: "assistant", Content: "旧旁白"},  // idx 0：上一轮
				{Role: "user", Content: "继续"},         // idx 1
				{Role: "tool", Content: "only tool"},    // idx 2：本轮
			},
			startIdx:      2,
			toolCallCount: 1,
			zh:            true,
			notWant:       "旧旁白",
			wantContains:  "1",
		},
		{
			// 长度阈值删除后，短结论原样返回（旧行为会判为"无实质"并丢弃）。
			name: "short conclusion kept verbatim",
			history: []Message{
				{Role: "assistant", Content: "根因在 a.go:42"},
			},
			startIdx:      0,
			toolCallCount: 2,
			zh:            true,
			want:          "根因在 a.go:42",
		},
	}
	for _, tt := range tests {
		got := buildRunSummary(tt.history, tt.startIdx, tt.toolCallCount, tt.zh)
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
