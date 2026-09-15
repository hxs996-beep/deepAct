package engine

import (
	"fmt"
	"strings"
	"time"
)

const (
	ratdMaxRounds      = 3
	ratdSandboxTimeout = 120 * time.Second
	ratdRoleIterations = 10
)

// RATDCommand represents a parsed /ratd command.
type RATDCommand struct {
	Goal string
}

// parseRATDCommand checks if userMsg is a /ratd command.
func parseRATDCommand(userMsg string) *RATDCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "ratd" {
		return nil
	}
	goal := strings.Join(parts[1:], " ")
	if goal == "" {
		return nil
	}
	return &RATDCommand{Goal: goal}
}

// ratdRolePrompt returns the compact role system prompt for a harness role.
func ratdRolePrompt(role string, zh bool) string {
	switch role {
	case "proposer":
		return pickPrompt(zh,
			"You are a Proposer — a rigorous senior engineer. Write code that fully satisfies the requirement and passes all physical sandbox tests. When given failing tests, analyze the traceback and fix ONLY the source logic — never modify the RedTeam's test files. Output ONLY the CodePayload JSON (language, source_files[{path,content}], design_notes). Do not add prose.",
			"你是「方案生成者」——精益求精的高级工程师。编写完全符合需求的代码，并确保能通过所有物理沙箱测试。若收到失败测试，分析报错并只修源代码逻辑——绝不修改红队的测试文件。只输出 CodePayload JSON（language, source_files[{path,content}], design_notes）。不要添加额外文字。")
	case "redteam":
		return pickPrompt(zh,
			"You are a RedTeam agent — paranoid, rigorous. Your ONLY job is to break the Proposer's code with hard, executable test files. Rules: 1) NEVER output natural-language suggestions or code comments — your only valid output is a test file that crashes or exposes a logic/perf flaw. 2) Focus on: concurrency deadlocks, race conditions, boundary overflows, memory/resource leaks, unhandled errors, large-data stalls. 3) If after deep analysis the code is flawless under the requirement, return ONLY the JSON {\"no_issues_found\": true}. Output ONLY the TestPayload JSON (test_category, severity, target_file, test_code, assertion_rationale).",
			"你是「红队」——偏执、严苛的顶尖红队专家。你唯一任务是打破 Proposer 提交的代码。规则：1) 严禁输出任何自然语言建议或代码点评——你的唯一有效输出是【必定能让当前代码崩溃或暴露逻辑/性能漏洞的测试文件】。2) 重点审查：并发死锁、竞态条件、边界溢出、内存/资源泄漏、未处理异常、大数据量卡死。3) 若深思熟虑后确定代码在现有需求下无懈可击，仅返回 JSON {\"no_issues_found\": true}。只输出 TestPayload JSON（test_category, severity, target_file, test_code, assertion_rationale）。")
	case "arbitrator":
		return pickPrompt(zh,
			"You are an Arbitrator — the judge of test legality. When a sandbox run fails, decide whether the failing test is a REAL bug in the code (ACCEPT_TEST) or an invalid/overreach/unreasonable test (REJECT_TEST). Consider: does the test violate the requirement's input contract? Does it reach into private internals the spec forbids? Is it itself syntactically broken? Output ONLY the ArbitrationResult JSON (decision: ACCEPT_TEST|REJECT_TEST, rejected_reason, actionable_feedback).",
			"你是「仲裁者」——测试合法性的裁判。当沙箱运行失败时，判断失败测试是代码的真 Bug（ACCEPT_TEST）还是无效/越权/不合理的测试（REJECT_TEST）。考虑：测试是否违背需求约定的输入范式？是否越权访问规格禁止的私有内部？测试本身是否语法错误？只输出 ArbitrationResult JSON（decision: ACCEPT_TEST|REJECT_TEST, rejected_reason, actionable_feedback）。")
	}
	return ""
}

// buildProposerGoal builds the task goal for the Proposer. On first run (no
// failing context) it is just the requirement; on refactor it includes the
// failing tests, sandbox output, and arbitrator feedback.
func buildProposerGoal(goal string, state *RATDState, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(pickPrompt(zh,
		"## Task\nProduce the code for the requirement below. Output a single CodePayload JSON.\n\n## Requirement\n%s\n\n## Output Format\n{\"language\":\"go|python|rust|java|typescript|cpp\",\"source_files\":[{\"path\":\"<relative path>\",\"content\":\"<full source>\"}],\"design_notes\":\"<brief>\"}",
		"## 任务\n为下面的需求产出代码。输出单个 CodePayload JSON。\n\n## 需求\n%s\n\n## 输出格式\n{\"language\":\"go|python|rust|java|typescript|cpp\",\"source_files\":[{\"path\":\"<相对路径>\",\"content\":\"<完整源码>\"}],\"design_notes\":\"<简述>\"}"), goal))
	if state != nil && len(state.Tests) > 0 {
		sb.WriteString(pickPrompt(zh, "\n\n## Test Suite (make ALL tests pass — the newest tests are the currently failing ones)\n", "\n\n## 测试套件（请让所有测试通过——最新追加的测试即当前待修复项）\n"))
		for _, t := range state.Tests {
			sb.WriteString(fmt.Sprintf("### %s (%s)\n%s\n\n", t.TargetFile, t.Severity, t.TestCode))
		}
	}
	if state != nil && state.LastSandbox != nil && state.LastSandbox.Output != "" {
		sb.WriteString(pickPrompt(zh, "\n## Sandbox Output\n", "\n## 沙箱输出\n"))
		sb.WriteString(state.LastSandbox.Output + "\n\n")
	}
	sb.WriteString(pickPrompt(zh, "\nDo NOT modify the RedTeam test files. Fix source files only.", "\n不要修改红队测试文件，只修源代码。"))
	return sb.String()
}

// buildRedTeamGoal builds the RedTeam goal: requirement + current source files.
// design_notes is deliberately EXCLUDED (context isolation — no anchoring).
func buildRedTeamGoal(goal string, files []RATDSourceFile, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(pickPrompt(zh,
		"## Task\nAttack the code below and output a TestPayload JSON. If the code is flawless under the requirement, output {\"no_issues_found\": true}.\n\n## Requirement\n%s\n\n## Current Code\n",
		"## 任务\n攻击下面的代码并输出 TestPayload JSON。若代码在需求下无懈可击，输出 {\"no_issues_found\": true}。\n\n## 需求\n%s\n\n## 当前代码\n"), goal))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("### File: %s\n%s\n\n", f.Path, f.Content))
	}
	return sb.String()
}

// buildArbitratorGoal builds the Arbitrator goal: requirement + code + failing
// test + sandbox output.
func buildArbitratorGoal(goal string, files []RATDSourceFile, test RATDTest, sandbox *RATDSandboxResult, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(pickPrompt(zh,
		"## Task\nJudge whether the failing test below is a REAL bug (ACCEPT_TEST) or an invalid test (REJECT_TEST). Output a single ArbitrationResult JSON.\n\n## Requirement\n%s\n\n## Code\n",
		"## 任务\n判断下面的失败测试是代码真 Bug（ACCEPT_TEST）还是无效测试（REJECT_TEST）。输出单个 ArbitrationResult JSON。\n\n## 需求\n%s\n\n## 代码\n"), goal))
	for _, f := range files {
		sb.WriteString(fmt.Sprintf("### File: %s\n%s\n\n", f.Path, f.Content))
	}
	sb.WriteString(fmt.Sprintf("## Failing Test\n### %s (%s)\n%s\n\n## Rationale\n%s\n\n",
		test.TargetFile, test.Severity, test.TestCode, test.Rationale))
	if sandbox != nil {
		sb.WriteString("## Sandbox Output\n" + sandbox.Output + "\n\n")
	}
	return sb.String()
}
