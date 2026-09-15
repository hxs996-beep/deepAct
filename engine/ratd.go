package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ratdMaxRounds            = 3
	ratdSandboxTimeout       = 120 * time.Second
	ratdRoleIterations       = 10
	ratdArbitratorIterations = 5
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
// failing tests, sandbox output, and, when set, the Arbitrator's actionable
// feedback for the fix round.
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
	if state != nil && strings.TrimSpace(state.ActionableFeedback) != "" {
		sb.WriteString(pickPrompt(zh, "\n## Arbitrator Feedback\n", "\n## 仲裁者反馈\n"))
		sb.WriteString(state.ActionableFeedback + "\n\n")
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
		sb.WriteString(pickPrompt(zh, "## Sandbox Output\n", "## 沙箱输出\n"))
		sb.WriteString(fmt.Sprintf("exit code: %d", sandbox.ExitCode))
		if sandbox.TimedOut {
			sb.WriteString(pickPrompt(zh, " (timed out)", "（超时）"))
		}
		sb.WriteString("\n")
		if sandbox.Output != "" {
			sb.WriteString(sandbox.Output + "\n\n")
		}
	}
	return sb.String()
}

// codePayloadJSON mirrors the Proposer's output contract.
type codePayloadJSON struct {
	Language    string           `json:"language"`
	SourceFiles []RATDSourceFile `json:"source_files"`
	DesignNotes string           `json:"design_notes"`
}

// parseCodePayload parses a Proposer CodePayload. Errors when JSON is invalid
// or no source files were produced.
func parseCodePayload(content string) (language string, files []RATDSourceFile, designNotes string, err error) {
	var p codePayloadJSON
	if err = json.Unmarshal([]byte(content), &p); err != nil {
		return "", nil, "", fmt.Errorf("invalid CodePayload: %w", err)
	}
	if len(p.SourceFiles) == 0 {
		return "", nil, "", fmt.Errorf("CodePayload has no source_files")
	}
	return p.Language, p.SourceFiles, p.DesignNotes, nil
}

// testPayloadJSON mirrors the RedTeam's output contract. NoIssuesFound is a
// pointer so an absent field is distinguishable from an explicit false.
type testPayloadJSON struct {
	NoIssuesFound *bool  `json:"no_issues_found"`
	Category      string `json:"test_category"`
	Severity      string `json:"severity"`
	TargetFile    string `json:"target_file"`
	TestCode      string `json:"test_code"`
	Rationale     string `json:"assertion_rationale"`
}

// parseTestPayload parses a RedTeam TestPayload. noIssues=true when the payload
// is {"no_issues_found": true} with no test fields; a payload that declares
// no_issues_found=true while also carrying test_code/target_file is ambiguous
// and rejected. Otherwise returns the parsed test. Errors on invalid JSON or
// ambiguous payloads.
func parseTestPayload(content string) (test *RATDTest, noIssues bool, err error) {
	var p testPayloadJSON
	if err = json.Unmarshal([]byte(content), &p); err != nil {
		return nil, false, fmt.Errorf("invalid TestPayload: %w", err)
	}
	if p.NoIssuesFound != nil && *p.NoIssuesFound {
		if strings.TrimSpace(p.TestCode) == "" && strings.TrimSpace(p.TargetFile) == "" {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("TestPayload 同时声明 no_issues_found=true 与测试字段，含混")
	}
	t := &RATDTest{
		Category:   p.Category,
		Severity:   p.Severity,
		TargetFile: p.TargetFile,
		TestCode:   p.TestCode,
		Rationale:  p.Rationale,
	}
	if strings.TrimSpace(t.TestCode) == "" || strings.TrimSpace(t.TargetFile) == "" {
		return nil, false, fmt.Errorf("TestPayload missing test_code/target_file")
	}
	return t, false, nil
}

// parseArbitration parses an Arbitrator ArbitrationResult.
func parseArbitration(content string) (decision string, feedback string, err error) {
	var a struct {
		Decision string `json:"decision"`
		Reason   string `json:"rejected_reason"`
		Feedback string `json:"actionable_feedback"`
	}
	if err = json.Unmarshal([]byte(content), &a); err != nil {
		return "", "", fmt.Errorf("invalid ArbitrationResult: %w", err)
	}
	if a.Decision != "ACCEPT_TEST" && a.Decision != "REJECT_TEST" {
		return "", "", fmt.Errorf("invalid arbitration decision %q", a.Decision)
	}
	return a.Decision, a.Feedback, nil
}

// resolveSafePath validates that relPath stays inside workDir (rejecting empty
// paths, absolute paths, ../ escapes, and symlink traversal out of the
// workdir) and returns the resolved absolute path. When createDirs is true,
// parent directories are created first. Leaf symlinks are refused so writes
// and removals never follow a link out of the workdir.
func resolveSafePath(workDir, relPath string, createDirs bool) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path %q escapes workdir", relPath)
	}
	p := filepath.Join(workDir, filepath.FromSlash(relPath))
	rel, err := filepath.Rel(workDir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes workdir", relPath)
	}
	dir := filepath.Dir(p)
	if createDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	workDirReal, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir %s: %w", workDir, err)
	}
	dirReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve dir %s: %w", dir, err)
	}
	relReal, err := filepath.Rel(workDirReal, dirReal)
	if err != nil || relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes workdir via symlink", relPath)
	}
	out := filepath.Join(dirReal, filepath.Base(p))
	if fi, err := os.Lstat(out); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to write through symlink %q", relPath)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", relPath, err)
	}
	return out, nil
}

// safeWriteFile writes content to relPath under workDir after validating that
// the path stays within workDir. It creates parent directories as needed.
func safeWriteFile(workDir, relPath, content string) error {
	out, err := resolveSafePath(workDir, relPath, true)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	return nil
}

// safeRemoveFile removes relPath under workDir after validating that the path
// stays within workDir. Missing files are not an error.
func safeRemoveFile(workDir, relPath string) error {
	out, err := resolveSafePath(workDir, relPath, false)
	if err != nil {
		return err
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", relPath, err)
	}
	return nil
}

// writeSourceFiles writes all Proposer files under workDir. Paths are
// validated to stay within workDir (rejects absolute paths, ../ escapes, empty
// paths, and symlink traversal out of the workdir).
func writeSourceFiles(files []RATDSourceFile, workDir string) error {
	for _, f := range files {
		if err := safeWriteFile(workDir, f.Path, f.Content); err != nil {
			return fmt.Errorf("write source %s: %w", f.Path, err)
		}
	}
	return nil
}

// writeTestFiles writes all RedTeam test files under workDir. Each path is
// validated with the same safety checks as writeSourceFiles.
func writeTestFiles(tests []RATDTest, workDir string) error {
	for _, t := range tests {
		if err := safeWriteFile(workDir, t.TargetFile, t.TestCode); err != nil {
			return err
		}
	}
	return nil
}

// removeTestFile removes a single RedTeam test file from workDir. Paths
// outside workDir are refused; missing files are not an error.
func removeTestFile(test RATDTest, workDir string) error {
	return safeRemoveFile(workDir, test.TargetFile)
}

// syncTestFilesToDisk makes on-disk test files match state.RATD.Tests:
// removes previously-written files no longer in the suite, then writes every
// current test, and records the written paths in state.WrittenTestFiles.
// Idempotent: with an unchanged suite it rewrites the same files and leaves
// no stale ones behind.
func syncTestFilesToDisk(state *RATDState, workDir string) error {
	current := make(map[string]bool, len(state.Tests))
	for _, t := range state.Tests {
		current[t.TargetFile] = true
	}
	for _, p := range state.WrittenTestFiles {
		if !current[p] {
			if err := safeRemoveFile(workDir, p); err != nil {
				return err
			}
		}
	}
	if err := writeTestFiles(state.Tests, workDir); err != nil {
		return err
	}
	written := make([]string, 0, len(state.Tests))
	for _, t := range state.Tests {
		written = append(written, t.TargetFile)
	}
	state.WrittenTestFiles = written
	return nil
}

// sandboxRunnerFunc executes a sandbox command and returns the outcome.
// Injectable in tests to control ExitCode without a real toolchain.
type sandboxRunnerFunc func(ctx context.Context, command, workDir string, timeout time.Duration) *RATDSandboxResult

// sandboxCommand maps a language to its base test command. Empty means the
// language has no available runner — the sandbox is marked Unavailable.
func sandboxCommand(language string) string {
	switch language {
	case "go":
		return "go test -timeout 60s ./..."
	case "python":
		return "pytest -x -q"
	case "rust":
		return "cargo test"
	case "java":
		return "mvn test"
	case "typescript":
		return "npm test"
	case "cpp":
		return "ctest --output-on-failure"
	default:
		return ""
	}
}

// execSandboxRunner is the default sandbox runner: run via bash -c under the
// engine's ctx with a hard timeout, capturing ExitCode and truncated output.
func execSandboxRunner(ctx context.Context, command, workDir string, timeout time.Duration) *RATDSandboxResult {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	cmd.Dir = workDir
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	res := &RATDSandboxResult{}
	if runCtx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	out := buf.String()
	runes := []rune(out)
	if len(runes) > 4000 {
		// Truncate on a rune boundary so multibyte UTF-8 output is never
		// split mid-character.
		out = string(runes[:4000]) + "\n... (truncated)"
	}
	res.Output = out
	return res
}

// RATDHall orchestrates the /ratd harness.
type RATDHall struct {
	engine *Engine
	// sandboxRunner is injectable for tests; nil uses execSandboxRunner.
	sandboxRunner sandboxRunnerFunc
}

func NewRATDHall(e *Engine) *RATDHall {
	return &RATDHall{engine: e}
}

// runSandbox executes the language test command in workDir.
func (h *RATDHall) runSandbox(ctx context.Context, workDir, language string) *RATDSandboxResult {
	cmd := sandboxCommand(language)
	if cmd == "" {
		return &RATDSandboxResult{Unavailable: true}
	}
	if h.sandboxRunner != nil {
		return h.sandboxRunner(ctx, cmd, workDir, ratdSandboxTimeout)
	}
	return execSandboxRunner(ctx, cmd, workDir, ratdSandboxTimeout)
}

// runRole executes a harness role via the sub agent with its role prompt.
// Returns the sub-agent's Summary (the JSON payload) or a failure marker.
func (h *RATDHall) runRole(ctx context.Context, role, goal string, zh bool, iterations int) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "ratd_role",
			Name:   role,
			Detail: pickPrompt(zh, role, role),
		})
	}
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: iterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, ratdRolePrompt(role, zh))
		if err != nil || result == nil {
			return ""
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary
	}
	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

func (h *RATDHall) runProposer(ctx context.Context, goal string, state *RATDState, zh bool) string {
	return h.runRole(ctx, "proposer", buildProposerGoal(goal, state, zh), zh, ratdRoleIterations)
}

func (h *RATDHall) runRedTeam(ctx context.Context, goal string, state *RATDState, zh bool) string {
	return h.runRole(ctx, "redteam", buildRedTeamGoal(goal, state.SourceFiles, zh), zh, ratdRoleIterations)
}

func (h *RATDHall) runArbitrator(ctx context.Context, goal string, state *RATDState, zh bool) (string, string) {
	// The failing test is the last one appended this round.
	var test RATDTest
	if len(state.Tests) > 0 {
		test = state.Tests[len(state.Tests)-1]
	}
	payload := h.runRole(ctx, "arbitrator", buildArbitratorGoal(goal, state.SourceFiles, test, state.LastSandbox, zh), zh, ratdArbitratorIterations)
	decision, feedback, err := parseArbitration(payload)
	if err != nil {
		return "ACCEPT_TEST", "" // 仲裁失败默认接受（宁可信其有）
	}
	return decision, feedback
}

// handleRATDArena runs the /ratd harness state machine to completion within
// one Run(). Idempotent: re-entering after a partial failure resumes from the
// stored Phase. On convergence it renders the delivery and clears RATD state.
func (h *RATDHall) handleRATDArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.RATD == nil {
		return nil, nil
	}
	zh := msgIsChinese(state.RATD.Goal)
	goal := state.RATD.Goal
	workDir := h.engine.config.WorkDir

	for {
		switch state.RATD.Phase {
		case RATDPropose:
			payload := h.runProposer(ctx, goal, state.RATD, zh)
			lang, files, notes, err := parseCodePayload(payload)
			if err != nil {
				return nil, fmt.Errorf("proposer payload: %w", err)
			}
			if err := writeSourceFiles(files, workDir); err != nil {
				return nil, fmt.Errorf("write sources: %w", err)
			}
			state.RATD.Language = lang
			state.RATD.SourceFiles = files
			state.RATD.DesignNotes = notes
			state.RATD.Phase = RATDRedTeam

		case RATDRedTeam:
			if state.RATD.CurrentRound >= ratdMaxRounds {
				state.RATD.Phase = RATDDone
				continue
			}
			payload := h.runRedTeam(ctx, goal, state.RATD, zh)
			test, noIssues, err := parseTestPayload(payload)
			if err != nil {
				return nil, fmt.Errorf("redteam payload: %w", err)
			}
			if noIssues {
				state.RATD.FinalVerify = true
			} else {
				state.RATD.Tests = append(state.RATD.Tests, *test)
			}
			state.RATD.Phase = RATDSandbox

		case RATDSandbox:
			if err := syncTestFilesToDisk(state.RATD, workDir); err != nil {
				return nil, fmt.Errorf("sync test files: %w", err)
			}
			res := h.runSandbox(ctx, workDir, state.RATD.Language)
			state.RATD.LastSandbox = res
			state.RATD.CurrentRound++
			passed := res != nil && !res.TimedOut && res.ExitCode == 0
			if passed {
				if state.RATD.FinalVerify {
					state.RATD.Phase = RATDDone
				} else {
					state.RATD.Phase = RATDRedTeam
				}
			} else if state.RATD.FinalVerify {
				// 红队已认输，失败来自回归套件本身 → 回 Propose 重构，
				// 不再仲裁（此时没有新测试可仲裁）。
				state.RATD.FinalVerify = false
				state.RATD.Phase = RATDPropose
			} else {
				state.RATD.Phase = RATDArbitrate
			}

		case RATDArbitrate:
			decision, feedback := h.runArbitrator(ctx, goal, state.RATD, zh)
			if decision == "REJECT_TEST" && len(state.RATD.Tests) > 0 {
				// Drop the invalid test and let RedTeam try again.
				state.RATD.Tests = state.RATD.Tests[:len(state.RATD.Tests)-1]
				state.RATD.Phase = RATDRedTeam
			} else {
				// ACCEPT: force a refactor round.
				state.RATD.Phase = RATDPropose
			}
			if feedback != "" {
				state.RATD.ActionableFeedback = feedback
			}

		case RATDDone:
			resp := h.buildRATDDelivery(goal, zh)
			h.engine.state.RATD = nil
			return resp, nil

		default:
			h.engine.state.RATD = nil
			return nil, nil
		}
	}
}

// buildRATDDelivery renders the final delivery screen: changed files list +
// final sandbox result. Code and tests are already on disk.
func (h *RATDHall) buildRATDDelivery(goal string, zh bool) *EngineResponse {
	state := h.engine.state
	var sb strings.Builder
	sb.WriteString(pickPrompt(zh,
		"## RATD-Harness Complete — Code Delivered\n\n",
		"## RATD-Harness 完成 — 代码已交付\n\n"))
	sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", pickPrompt(zh, "Requirement", "需求"), goal))
	sb.WriteString(pickPrompt(zh, "### Changed Files\n\n", "### 改动文件\n\n"))
	for _, f := range state.RATD.SourceFiles {
		sb.WriteString(fmt.Sprintf("- `%s`\n", f.Path))
	}
	for _, t := range state.RATD.Tests {
		sb.WriteString(fmt.Sprintf("- `%s` (test, %s)\n", t.TargetFile, t.Severity))
	}
	sb.WriteString("\n" + pickPrompt(zh, "### Final Sandbox Result\n\n", "### 最终沙箱结果\n\n"))
	if state.RATD.LastSandbox == nil {
		sb.WriteString(pickPrompt(zh, "No sandbox run recorded.", "无沙箱运行记录。"))
	} else if state.RATD.LastSandbox.Unavailable {
		sb.WriteString(pickPrompt(zh, "No usable test command for this language — not physically verified.", "该语言无可用测试命令——未做物理验证。"))
	} else {
		sb.WriteString(fmt.Sprintf("exit code: %d\n", state.RATD.LastSandbox.ExitCode))
		if state.RATD.LastSandbox.TimedOut {
			sb.WriteString(pickPrompt(zh, "(timed out)\n", "（超时）\n"))
		}
		sb.WriteString("```\n" + state.RATD.LastSandbox.Output + "\n```\n")
	}
	sb.WriteString("\n" + pickPrompt(zh,
		"Code is written to the workspace. Review and commit as you see fit.",
		"代码已写入工作区，请自行查看并提交。"))
	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}
