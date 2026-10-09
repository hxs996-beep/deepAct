package engine

import (
	"strings"
	"testing"
)

// TestGuardContextBudget_TrimsWhenOverBudget 验证发送前守卫会裁剪超预算的历史，
// 保留 system 头，且裁剪后的尾部不以孤儿 tool 响应开头。
func TestGuardContextBudget_TrimsWhenOverBudget(t *testing.T) {
	e := &Engine{
		config:     EngineConfig{MaxContextTokens: 100000, MaxOutputTokens: 1000},
		context:    &stubContextBuilder{},
		compressor: NewCompressionOrchestrator(nil, nil, "pro"),
	}
	e.history = []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q"},
		{Role: "assistant", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("b", 300000)},
		{Role: "user", Content: "next"},
	}
	budget := e.historyTokenBudget(nil)
	if estimateHistoryTokens(e.history) <= budget {
		t.Fatalf("test setup: history must exceed budget")
	}
	e.guardContextBudget(nil)
	if got := estimateHistoryTokens(e.history); got > budget {
		t.Fatalf("guard left history over budget: %d > %d", got, budget)
	}
	if len(e.history) == 0 || e.history[0].Role != "system" {
		t.Fatalf("system head must be preserved after trimming")
	}
	if len(e.history) > 1 && e.history[1].Role == "tool" {
		t.Fatalf("trimmed history must not begin with an orphaned tool response")
	}
}

// TestGuardContextBudget_TruncatesBodyWithSameMessageCount 覆盖"消息条数不变、
// 仅正文被截断"的路径（单轮工具输出把请求顶爆）。守卫必须按 token 估算判断
// 是否生效——按消息条数判断会漏掉这一路径、丢弃裁剪结果。
func TestGuardContextBudget_TruncatesBodyWithSameMessageCount(t *testing.T) {
	e := &Engine{
		config:     EngineConfig{MaxContextTokens: 100000, MaxOutputTokens: 1000},
		context:    &stubContextBuilder{},
		compressor: NewCompressionOrchestrator(nil, nil, "pro"),
	}
	e.history = []Message{
		{Role: "assistant", ToolCalls: []MessageToolCall{{ID: "c1", Name: "read"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("z", 600000)},
	}
	beforeLen := len(e.history)
	budget := e.historyTokenBudget(nil)
	e.guardContextBudget(nil)
	if got := estimateHistoryTokens(e.history); got > budget {
		t.Fatalf("guard left history over budget: %d > %d", got, budget)
	}
	if len(e.history) != beforeLen {
		t.Fatalf("expected body-only truncation to keep the message count, got %d want %d", len(e.history), beforeLen)
	}
	if e.history[len(e.history)-1].Role != "tool" {
		t.Fatalf("tool pairing must survive body truncation")
	}
}

// TestToolExposureSummary verifies the exposure line's numbers: the
// registry-vs-engine split and the budget share when a window is known.
func TestToolExposureSummary(t *testing.T) {
	tools := []ModelTool{
		{Function: ModelToolFunction{Name: "a"}},
		{Function: ModelToolFunction{Name: "b"}},
		{Function: ModelToolFunction{Name: "c"}},
	}
	count := func(msgs []ModelMessage) int { return len(msgs) }

	got := toolExposureSummary(tools, 1, 10, count)
	for _, want := range []string{"sent=3", "registry=1", "core=2", "tool_tokens=3", "~30% of 10 budget"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}

	// Without a known budget the share must be omitted, not reported as 0%.
	if got := toolExposureSummary(tools, 1, 0, count); strings.Contains(got, "budget") {
		t.Errorf("summary without budget must not mention budget: %q", got)
	}
}
