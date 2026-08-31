package engine

import (
	"strings"
	"testing"
)

// TestHandoffToolSpec_ExposesExpectedOutput 锁定 A3 的委派契约增强：
// handoff_to_agent 工具 schema 必须暴露 expected_output 字段，让父代理能给
// 子代理交代"做成什么样算好"。当前 schema 没有该字段 → 红灯。
func TestHandoffToolSpec_ExposesExpectedOutput(t *testing.T) {
	spec := handoffToolSpec(true)
	if !strings.Contains(string(spec.Function.Parameters), "expected_output") {
		t.Errorf("handoff schema must expose expected_output, got %s", spec.Function.Parameters)
	}
}

// TestBuildVolatilePrompt_InjectsExpectedOutput 锁定 A3 的落地：
// 父代理填写的 expected_output 必须注入子代理的 volatile prompt（任务交代
// 的一部分），子代理才知道验收标准。当前 buildVolatilePrompt 未处理该字段 → 红灯。
func TestBuildVolatilePrompt_InjectsExpectedOutput(t *testing.T) {
	r := &SubAgentRunner{}
	prompt := r.buildVolatilePrompt(Handoff{
		Goal:           "审查改动",
		ExpectedOutput: "输出格式：JSON {\"问题\": [], \"结论\": \"PASS|FAIL|PARTIAL\"}",
	})
	if !strings.Contains(prompt, "Expected Output") {
		t.Errorf("volatile prompt must carry an expected-output section, got %q", prompt)
	}
	if !strings.Contains(prompt, "PASS|FAIL|PARTIAL") {
		t.Errorf("volatile prompt must contain the specified expected output, got %q", prompt)
	}
}
