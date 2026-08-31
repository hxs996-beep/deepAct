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
