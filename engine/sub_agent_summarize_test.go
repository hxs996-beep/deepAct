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

// TestSummarizeHistory_SkipsIntentStatement locks the "/collab output shows
// an intent statement as the conclusion" bug. The user's collab report showed
// "(analysis timed out, partial result) 让我深入了解 codex 的 skills 模型..."
// — the "让我..." line is an INTENT statement ("let me look into..."), not a
// conclusion. summarizeHistory walked backward and returned it as the last
// substantive assistant message. It must skip intent statements (using the
// existing isIntermediateText heuristic) and fall back to a real finding, so
// the collab stage shows substance instead of a plan-to-do-more line.
func TestSummarizeHistory_SkipsIntentStatement(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "调研 codex 与 deepact 的区别"},
		{Role: "assistant", Content: "核心区别已定位：codex 使用原生 worktree 隔离与 hooks 事件机制，deepact 使用 stop hooks 与 guard 系统。这是实质性结论。"},
		{Role: "tool", Content: "read codex/hooks/..."},
		{Role: "assistant", Content: "让我深入了解 codex 的 skills 模型（与 deepact 的 skill 差异）、worktree、execpolicy、hooks、memory、realtime 等核心模块。"},
	}
	got := r.summarizeHistory(history, "调研 codex 与 deepact 的区别")

	if strings.Contains(got, "让我深入了解") {
		t.Errorf("summarizeHistory must skip intent statements, got %q", got)
	}
	if !strings.Contains(got, "核心区别已定位") {
		t.Errorf("summarizeHistory should fall back to the real substantive message, got %q", got)
	}
}

// TestSummarizeHistory_SkipsPlanStatementVariants locks the remaining
// "plan statement as conclusion" cases the user reproduced in /collab:
//   - "现在读取关键的实际设计（agent-graph-store、skills 接口、guardian
//     审查流程、execpolicy），以及 deepact 的核心 loop/turn/guards，来完成对比。"  (recon)
//   - "Now let me look at the multi_agents_v2 spawn tool ..."  (design)
//   - "I now understand deepact's engine architecture well. Let me examine the
//     codex side — the key crates mentioned: ..."  (dev)
// All three describe what the agent PLANS to do next, not a finding.
// summarizeHistory must skip them and fall back to a real conclusion, or to
// the readable no-result message when none exists.
func TestSummarizeHistory_SkipsPlanStatementVariants(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "研究 codex 与当前项目的区别"},
		{Role: "assistant", Content: "codex 使用 agent-graph-store 管理状态，deepact 使用 TaskState；这是已定位的核心差异。"},
		{Role: "tool", Content: "read codex/skills/..."},
		{Role: "assistant", Content: "现在读取关键的实际设计（agent-graph-store、skills 接口、guardian 审查流程、execpolicy），以及 deepact 的核心 loop/turn/guards，来完成对比。"},
		{Role: "tool", Content: "read codex/hooks/..."},
		{Role: "assistant", Content: "Now let me look at the multi_agents_v2 spawn tool (codex's sub-agent spawning) and the network_approval to understand the key design differences, plus deepact's session/store.go."},
		{Role: "tool", Content: "read codex/multi_agents_v2/..."},
		{Role: "assistant", Content: "I now understand deepact's engine architecture well. Let me examine the codex side — the key crates mentioned: agent-graph-store, skills, guardian, execpolicy, multi_agents_v2."},
	}
	got := r.summarizeHistory(history, "研究 codex 与当前项目的区别")

	for _, plan := range []string{"现在读取", "Now let me", "Let me examine", "I now understand"} {
		if strings.Contains(got, plan) {
			t.Errorf("summarizeHistory must skip plan statement %q, got %q", plan, got)
		}
	}
	if !strings.Contains(got, "agent-graph-store") {
		t.Errorf("summarizeHistory should fall back to the real finding, got %q", got)
	}
}
