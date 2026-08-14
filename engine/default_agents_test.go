package engine

import (
	"strings"
	"testing"
)

// TestCriticSpec_NoBashTool ensures the critic no longer exposes the bash tool:
// build/test verification is already done by the main agent, and the critic's
// verdict comes from static review only. This removes the path where the critic
// launches long-running go build/go test commands that exceed the 30s bash
// timeout and exhaust its iteration budget without producing a verdict.
func TestCriticSpec_NoBashTool(t *testing.T) {
	reg := NewDefaultRegistry(nil)
	critic, err := reg.Get(AgentCritic)
	if err != nil {
		t.Fatalf("critic not registered: %v", err)
	}
	spec := critic.Spec()
	for _, name := range spec.ToolNames {
		if name == "bash" {
			t.Errorf("critic ToolNames must not include bash, got %v", spec.ToolNames)
		}
	}
}

// TestCriticPrompt_NoBuildTestBaseline ensures the critic prompt no longer
// instructs the agent to run build/test/lint commands — those are verified by
// the main agent, and repeating them wastes the critic's iteration budget
// (the root cause of the "critic timed out without a verdict" symptom).
// The prompt explicitly FORBIDS running them; the words themselves are allowed
// only inside the prohibition, never as an instruction.
func TestCriticPrompt_NoBuildTestBaseline(t *testing.T) {
	for name, prompt := range map[string]string{
		"en": criticPromptEn,
		"zh": criticPromptZh,
	} {
		for _, forbidden := range []string{
			"Run the build",
			"运行构建",
			"运行项目测试",
			"Run the project's test suite",
			"Command run",
			"确定性检查",
			"Deterministic Checks",
		} {
			if strings.Contains(prompt, forbidden) {
				t.Errorf("criticPrompt[%s] must not instruct build/test verification, found %q", name, forbidden)
			}
		}
		// The prohibition itself must be present (positive contract).
		if !strings.Contains(prompt, "Do NOT run build/test") &&
			!strings.Contains(prompt, "不要运行 build/test") {
			t.Errorf("criticPrompt[%s] must explicitly forbid running build/test commands", name)
		}
	}
}

// TestCriticPrompt_RetainsVerdictFormat ensures the new prompt still exposes
// the VERDICT: PASS/FAIL/PARTIAL contract that the hard gate
// (parseCriticVerdict == "FAIL") depends on.
func TestCriticPrompt_RetainsVerdictFormat(t *testing.T) {
	for name, prompt := range map[string]string{
		"en": criticPromptEn,
		"zh": criticPromptZh,
	} {
		for _, verdict := range []string{"VERDICT: PASS", "VERDICT: FAIL", "VERDICT: PARTIAL"} {
			if !strings.Contains(prompt, verdict) {
				t.Errorf("criticPrompt[%s] must retain %q contract", name, verdict)
			}
		}
	}
}

// TestCriticPrompt_ReviewsRequirementsAgainstChanges ensures the new prompt
// directs the critic to compare the changed files against the original
// requirements (static review) rather than executing commands.
func TestCriticPrompt_ReviewsRequirementsAgainstChanges(t *testing.T) {
	for name, prompt := range map[string]string{
		"en": criticPromptEn,
		"zh": criticPromptZh,
	} {
		for _, required := range []string{"read", "grep"} {
			if !strings.Contains(prompt, required) {
				t.Errorf("criticPrompt[%s] should mention %q as an allowed evidence tool", name, required)
			}
		}
	}
}
