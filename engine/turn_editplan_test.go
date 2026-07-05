package engine

import (
	"strings"
	"testing"
)

func TestReasoningForEditPlan(t *testing.T) {
	tests := []struct {
		name           string
		history        []Message
		currentContent string
		want           string
	}{
		{
			name:           "current content non-empty: use it as-is",
			currentContent: "我将修改 engine/turn.go 来修复拦截问题",
			history: []Message{
				{Role: "assistant", Content: "旧分析，应被忽略"},
			},
			want: "我将修改 engine/turn.go 来修复拦截问题",
		},
		{
			name:           "current empty: fall back to most recent assistant text",
			currentContent: "",
			history: []Message{
				{Role: "user", Content: "修复这个 bug"},
				{Role: "assistant", Content: "分析：问题在拦截门槛，方案是回退历史原因"},
				{Role: "user", Content: "OK"},
			},
			want: "分析：问题在拦截门槛，方案是回退历史原因",
		},
		{
			name:           "current empty: skip tool-call-only assistant msgs, take last non-empty",
			currentContent: "",
			history: []Message{
				{Role: "assistant", Content: "最早的分析"},
				{Role: "assistant", Content: ""},
				{Role: "assistant", Content: "最新的分析"},
			},
			want: "最新的分析",
		},
		{
			name:           "current empty, only empty assistant msgs: empty",
			currentContent: "",
			history: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: ""},
			},
			want: "",
		},
		{
			name:           "current empty, no history: empty",
			currentContent: "",
			history:        nil,
			want:           "",
		},
		{
			name:           "whitespace-only current content treated as empty, falls back",
			currentContent: "   \n  ",
			history: []Message{
				{Role: "assistant", Content: "实际分析"},
			},
			want: "实际分析",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reasoningForEditPlan(tt.history, tt.currentContent)
			if got != tt.want {
				t.Errorf("reasoningForEditPlan() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEditPlanSummary_EmptyReasoningNoAlarmingMessage(t *testing.T) {
	plan := &PendingEditPlan{Reasoning: ""}

	t.Run("zh", func(t *testing.T) {
		got := formatEditPlanSummary(plan, true, "/cwd")
		if strings.Contains(got, "AI 未提供修改原因") {
			t.Errorf("zh: should not show alarming 'AI 未提供修改原因', got: %s", got)
		}
		if !strings.Contains(got, "确认执行修改？") {
			t.Errorf("zh: should still ask for confirmation, got: %s", got)
		}
	})
	t.Run("en", func(t *testing.T) {
		got := formatEditPlanSummary(plan, false, "/cwd")
		if strings.Contains(got, "No reasoning provided") {
			t.Errorf("en: should not show alarming 'No reasoning provided', got: %s", got)
		}
		if !strings.Contains(got, "Proceed with the changes?") {
			t.Errorf("en: should still ask for confirmation, got: %s", got)
		}
	})
}

func TestFormatEditPlanSummary_WithReasoning(t *testing.T) {
	plan := &PendingEditPlan{Reasoning: "问题在 X，方案是改 Y"}
	got := formatEditPlanSummary(plan, true, "/cwd")
	if !strings.Contains(got, "问题在 X，方案是改 Y") {
		t.Errorf("should contain the reasoning verbatim, got: %s", got)
	}
	if !strings.Contains(got, "确认执行修改？") {
		t.Errorf("should ask for confirmation, got: %s", got)
	}
}
