package promptset

import (
	"strings"
	"testing"
)

// TestSubAgentPrompt_MentionsExpectedOutput 锁定 A3 的 prompt 落地：
// 子代理的输出契约必须指引它遵循父代理委派时交代的"预期输出"（expected_output）
// 验收标准——否则子代理不知道交付物长什么样，只能靠猜。
// 当前 sub_agent.md 输出契约未提及预期输出 → 红灯。
func TestSubAgentPrompt_MentionsExpectedOutput(t *testing.T) {
	p := Get().SubAgent
	if !strings.Contains(p, "Expected Output") {
		t.Errorf("sub-agent prompt must instruct following the parent's expected-output acceptance criteria, got %q", p)
	}
}

// TestSubAgentPrompt_StructuredJSONTakesPrecedence 锁定 harness 格式契约：
// 子代理系统提示的输出契约是"叙述式小节"，与 Decomposer/Proposer 等角色
// 提示的"只输出 JSON"冲突时，模型倾向于遵循 system 层的叙述格式（这是
// /collab decompose 报 "no JSON object found" 的源头之一）。输出契约必须
// 显式声明：当父代理角色指令要求结构化 JSON 时，JSON 优先于通用叙述格式。
func TestSubAgentPrompt_StructuredJSONTakesPrecedence(t *testing.T) {
	p := Get().SubAgent
	if !strings.Contains(p, "JSON") {
		t.Errorf("sub-agent prompt must mention structured JSON output precedence, got %q", p)
	}
}

// TestSystemPrompt_MandatorySkillRule locks the mandatory skill rule: a possible
// match means you MUST load, it is not optional, and the rationalization escape
// hatches are listed.
func TestSystemPrompt_MandatorySkillRule(t *testing.T) {
	p := Get().System
	for _, want := range []string{"you have no choice but to use it", "Do not skip skill matching"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt must carry the mandatory skill rule (missing %q)", want)
		}
	}
}

// TestSystemPrompt_TodoProgressIsLive locks the step-reporting cadence: status
// changes must be reported in the turn they happen (a delegated step counts as
// finished when its sub-agent returns), and a final all-completed snapshot must
// precede the completion call. Without it the model batches updates until the
// end — or skips the final snapshot entirely — and the panel looks stuck on the
// first step even though the run is done.
func TestSystemPrompt_TodoProgressIsLive(t *testing.T) {
	p := Get().System
	for _, want := range []string{"todo_write", "the sub-agent returned", "Before wrapping up"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt must carry the live step-reporting rule (missing %q)", want)
		}
	}
}

// TestSystemPrompt_RepliesInUserLanguage locks the reply-language rule. Each
// anchor below is the smallest fragment that only the clause it guards can
// produce, so removing that clause goes red. The bare "Reply in the user's
// language" phrasing existed before this fix as well, so on its own it guards
// nothing — it is kept only to pin the rule's existence. The comment claims no
// more than that: a partial rewrite of a summary sentence is out of reach.
func TestSystemPrompt_RepliesInUserLanguage(t *testing.T) {
	p := Get().System
	for _, want := range []string{
		"Reply in the user's language",
		"not from injected context blocks",
		"`todo_write` step text",
		"`ask_user` questions and options",
		"sub-agent reports you relay",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt must carry the reply-language rule (missing %q)", want)
		}
	}
	// The session-language lock mechanism was removed in 76f90f2, so a claim
	// about it must not come back — including a reworded one. The leading space
	// keeps "blocked" out of the match.
	if strings.Contains(p, " locked") {
		t.Error("system prompt must not claim a session-language lock: that mechanism was removed, so the claim is false")
	}
}

// TestSubAgentPrompt_RepliesInUserLanguage locks the sub-agent language rule:
// sub-agent streams render in the UI and their summaries are relayed to the user
// by the parent, so they must follow the user's language rather than the English
// prompt and tool output they work with. Both anchors are fragments only this
// rule can produce — sub_agent.md carries no other language text.
func TestSubAgentPrompt_RepliesInUserLanguage(t *testing.T) {
	p := Get().SubAgent
	for _, want := range []string{
		"Write your report in the user's language",
		"not the language of this prompt or of tool output",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("sub-agent prompt must carry the reply-language rule (missing %q)", want)
		}
	}
}
