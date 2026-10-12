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
	if !strings.Contains(p, "预期输出") {
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
	for _, want := range []string{"你没有选择", "不得用以下理由跳过技能匹配"} {
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
	for _, want := range []string{"todo_write", "子代理已返回", "收尾前先发一份"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt must carry the live step-reporting rule (missing %q)", want)
		}
	}
}
