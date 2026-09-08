package engine

import (
	"strings"
	"testing"
)

// TestSummarizeHistory_NoFinalText_ReadableFallback locks the "/collab output
// unreadable" bug: when a sub-agent hits the per-call timeout, the loop guard,
// or the iteration cap without producing a usable final text, summarizeHistory
// must return a concise readable failure line — NOT dump the first line of
// every raw tool result (file paths, code lines, LSP hits), which flooded
// /collab's stage outputs and summary with garbage like
// "(analysis timed out — 12 tool calls; showing first discoveries)\n• 1240: }".
func TestSummarizeHistory_NoFinalText_ReadableFallback(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "分析这个 bug"},
		{Role: "assistant", Content: ""},
		{Role: "tool", Content: "clipboard_other.go"},
		{Role: "tool", Content: "ui/model.go:152: showSuggestions bool"},
		{Role: "tool", Content: "1240: }"},
	}
	got := r.summarizeHistory(history, "分析这个 bug")

	if strings.TrimSpace(got) == "" {
		t.Fatal("fallback must return a non-empty readable message")
	}
	for _, bad := range []string{
		"showing first discoveries",
		"no partial discoveries",
		"clipboard_other.go",
		"showSuggestions",
		"1240: }",
		"\n- ",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("fallback must not dump raw tool output, got %q containing %q", got, bad)
		}
	}
	// Readable failure must describe what happened: interrupted without a
	// final conclusion, mentioning how many tool calls ran.
	if !strings.Contains(got, "3") {
		t.Errorf("fallback should state the tool-call count readably, got %q", got)
	}
	if !strings.Contains(got, "未") && !strings.Contains(got, "no final") {
		t.Errorf("fallback should describe the failure readably, got %q", got)
	}
}

// TestSummarizeHistory_NoTools_ReadableFallback: when the agent was
// interrupted before any tool call, the fallback must still be a readable
// message, not the raw dump phrasing.
func TestSummarizeHistory_NoTools_ReadableFallback(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "目标"},
		{Role: "assistant", Content: ""},
	}
	got := r.summarizeHistory(history, "目标")
	if strings.Contains(got, "no partial discoveries") {
		t.Errorf("fallback must not use the raw dump phrasing, got %q", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("fallback must return a non-empty readable message")
	}
}

// TestSummarizeHistory_SubstantiveAssistant_KeptVerbatim: the substantive
// final-text path must be preserved — a real final message (>=50 chars, not a
// self-instruction line) is still the summary.
func TestSummarizeHistory_SubstantiveAssistant_KeptVerbatim(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "分析"},
		{Role: "assistant", Content: "经过排查，根因位于 engine/sub_agent.go 的 summarizeHistory 兜底分支：它把每次工具调用的原始输出首行原样倒出，导致 /collab 界面输出无法阅读。"},
		{Role: "tool", Content: "ui/model.go:152"},
	}
	got := r.summarizeHistory(history, "分析")
	if !strings.Contains(got, "summarizeHistory") {
		t.Errorf("substantive assistant message must be kept as the summary, got %q", got)
	}
}
