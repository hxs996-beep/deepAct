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
