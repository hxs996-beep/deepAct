package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseCodePayload_Valid(t *testing.T) {
	lang, files, notes, err := parseCodePayload(`{"language":"go","source_files":[{"path":"src/q.go","content":"package q"}],"design_notes":"CAS queue"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lang != "go" || len(files) != 1 || files[0].Path != "src/q.go" || notes != "CAS queue" {
		t.Errorf("got lang=%q files=%v notes=%q", lang, files, notes)
	}
}

func TestParseCodePayload_NoFilesError(t *testing.T) {
	if _, _, _, err := parseCodePayload(`{"language":"go","source_files":[]}`); err == nil {
		t.Error("expected error for empty source_files")
	}
}

func TestParseCodePayload_InvalidJSON(t *testing.T) {
	if _, _, _, err := parseCodePayload(`not json`); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseTestPayload_NoIssues(t *testing.T) {
	_, noIssues, err := parseTestPayload(`{"no_issues_found": true}`)
	if err != nil || !noIssues {
		t.Fatalf("expected no_issues=true, got noIssues=%v err=%v", noIssues, err)
	}
}

func TestParseTestPayload_Valid(t *testing.T) {
	test, noIssues, err := parseTestPayload(`{"test_category":"CONCURRENCY_STRESS","severity":"P0_CRITICAL","target_file":"src/q_test.go","test_code":"func TestX(t *testing.T){}","assertion_rationale":"deadlock"}`)
	if err != nil || noIssues {
		t.Fatalf("unexpected: noIssues=%v err=%v", noIssues, err)
	}
	if test == nil || test.Severity != "P0_CRITICAL" || test.TargetFile != "src/q_test.go" {
		t.Errorf("test = %+v", test)
	}
}

func TestParseTestPayload_MissingFields(t *testing.T) {
	if _, _, err := parseTestPayload(`{"severity":"P1_HIGH"}`); err == nil {
		t.Error("expected error for missing test_code/target_file")
	}
}

func TestParseArbitration_Accept(t *testing.T) {
	decision, _, err := parseArbitration(`{"decision":"ACCEPT_TEST","rejected_reason":"","actionable_feedback":"fix leak"}`)
	if err != nil || decision != "ACCEPT_TEST" {
		t.Fatalf("got decision=%q err=%v", decision, err)
	}
}

func TestParseArbitration_InvalidDecision(t *testing.T) {
	if _, _, err := parseArbitration(`{"decision":"MAYBE"}`); err == nil {
		t.Error("expected error for invalid decision")
	}
}

func TestWriteSourceFiles_WritesUnderWorkdir(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "src/q.go", Content: "package q"}}
	if err := writeSourceFiles(files, dir); err != nil {
		t.Fatalf("writeSourceFiles: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "src", "q.go"))
	if err != nil || string(b) != "package q" {
		t.Errorf("read back = %q, err=%v", b, err)
	}
}

func TestWriteSourceFiles_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "../evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for path escaping workdir")
	}
}

func TestWriteSourceFiles_RejectsAbsolute(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "/etc/evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for absolute path")
	}
}

func TestWriteSourceFiles_RejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	files := []RATDSourceFile{{Path: "link/evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Fatal("expected error for symlink escape")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.go")); !os.IsNotExist(err) {
		t.Errorf("evil.go must not be written outside workdir, stat err=%v", err)
	}
}

func TestParseTestPayload_AmbiguousNoIssuesAndTest(t *testing.T) {
	_, _, err := parseTestPayload(`{"no_issues_found": true, "test_code":"x", "target_file":"y"}`)
	if err == nil {
		t.Error("expected error for no_issues_found=true with test fields")
	}
}

func TestParseTestPayload_NoIssuesFalse(t *testing.T) {
	test, noIssues, err := parseTestPayload(`{"no_issues_found": false, "test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"a_test.go","test_code":"func T(){}","assertion_rationale":"r"}`)
	if err != nil || noIssues {
		t.Fatalf("unexpected: noIssues=%v err=%v", noIssues, err)
	}
	if test == nil || test.TargetFile != "a_test.go" || test.TestCode != "func T(){}" {
		t.Errorf("test = %+v", test)
	}
}

func TestWriteSourceFiles_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	files := []RATDSourceFile{{Path: "", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestWriteSourceFiles_RejectsLeafSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "evil.go"), filepath.Join(dir, "evil.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	files := []RATDSourceFile{{Path: "evil.go", Content: "x"}}
	if err := writeSourceFiles(files, dir); err == nil {
		t.Fatal("expected error writing through leaf symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.go")); !os.IsNotExist(err) {
		t.Error("content leaked outside workdir through leaf symlink")
	}
}

func TestSandboxCommand(t *testing.T) {
	cases := map[string]string{
		"go": "go test -timeout 60s ./...", "python": "pytest -x -q", "rust": "cargo test",
	}
	for lang, want := range cases {
		if got := sandboxCommand(lang); got != want {
			t.Errorf("sandboxCommand(%q) = %q, want %q", lang, got, want)
		}
	}
	if sandboxCommand("unknown") != "" {
		t.Error("unknown language should return empty command")
	}
}

func TestExecSandboxRunner_ExitCode(t *testing.T) {
	res := execSandboxRunner(context.Background(), "exit 3", t.TempDir(), 10*time.Second)
	if res.TimedOut || res.ExitCode != 3 {
		t.Errorf("got exit=%d timedout=%v", res.ExitCode, res.TimedOut)
	}
	res = execSandboxRunner(context.Background(), "echo hi", t.TempDir(), 10*time.Second)
	if res.ExitCode != 0 || !strings.Contains(res.Output, "hi") {
		t.Errorf("got exit=%d output=%q", res.ExitCode, res.Output)
	}
}

func TestExecSandboxRunner_Timeout(t *testing.T) {
	res := execSandboxRunner(context.Background(), "sleep 5", t.TempDir(), 50*time.Millisecond)
	if !res.TimedOut {
		t.Error("expected timed out")
	}
	if res.ExitCode != -1 {
		t.Errorf("expected ExitCode -1 on timeout, got %d", res.ExitCode)
	}
}

func TestRATDHall_RunSandbox_Unavailable(t *testing.T) {
	h := &RATDHall{}
	res := h.runSandbox(context.Background(), t.TempDir(), "brainfuck")
	if res == nil || !res.Unavailable {
		t.Error("unknown language should be Unavailable")
	}
}

// ratdMockAgent returns canned payloads per role for harness tests.
type ratdMockAgent struct {
	proposerPayload    string
	redTeamPayloads    []string // consumed one per RED_TEAM entry
	redTeamFallback    string   // used once payloads are exhausted ("" = no_issues)
	arbitratorDecision string
}

func (m *ratdMockAgent) ID() AgentID { return AgentSub }
func (m *ratdMockAgent) Spec() AgentSpec {
	return AgentSpec{ID: AgentSub, Description: "ratd mock", StructuredResult: true}
}
func (m *ratdMockAgent) SetOnProgress(fn ProgressFunc) {}

func (m *ratdMockAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	return &HandoffResult{Summary: m.pick(input), Conclusions: []string{m.pick(input)}}, nil
}
func (m *ratdMockAgent) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	// extraPrompt carries the role prompt. Dispatch on the JSON contract token
	// that appears in BOTH language variants of each role prompt (tests use
	// Chinese goals → zh=true → role prompt is Chinese, so English role names
	// like "Proposer" must NOT be the discriminator).
	switch {
	case strings.Contains(extraPrompt, "CodePayload"):
		return &HandoffResult{Summary: m.proposerPayload, Conclusions: []string{m.proposerPayload}}, nil
	case strings.Contains(extraPrompt, "TestPayload"):
		p := m.redTeamFallback
		if p == "" {
			p = "{\"no_issues_found\": true}"
		}
		if len(m.redTeamPayloads) > 0 {
			p = m.redTeamPayloads[0]
			m.redTeamPayloads = m.redTeamPayloads[1:]
		}
		return &HandoffResult{Summary: p, Conclusions: []string{p}}, nil
	case strings.Contains(extraPrompt, "ArbitrationResult"):
		return &HandoffResult{Summary: m.arbitratorDecision, Conclusions: []string{m.arbitratorDecision}}, nil
	}
	return &HandoffResult{Summary: "unexpected role", Conclusions: []string{"unexpected role"}}, nil
}

func (m *ratdMockAgent) pick(input Handoff) string {
	switch {
	case strings.Contains(input.Goal, "CodePayload"):
		return m.proposerPayload
	case strings.Contains(input.Goal, "TestPayload"):
		return "{\"no_issues_found\": true}"
	default:
		return "{\"decision\":\"ACCEPT_TEST\"}"
	}
}

func newRATDTestEngine(t *testing.T, agent *ratdMockAgent, runner sandboxRunnerFunc) (*Engine, *RATDHall) {
	t.Helper()
	reg := NewAgentRegistry()
	reg.Register(agent)
	e := &Engine{
		agents:  reg,
		state:   &TaskState{TaskID: "test-ratd"},
		config:  EngineConfig{WorkDir: t.TempDir()},
		context: &stubContextBuilder{},
	}
	hall := NewRATDHall(e)
	if runner != nil {
		hall.sandboxRunner = runner
	}
	return e, hall
}

const validProposer = `{"language":"go","source_files":[{"path":"src/q.go","content":"package q\n"}],"design_notes":"simple"}`

func TestRATDArena_ConvergesOnNoIssues(t *testing.T) {
	agent := &ratdMockAgent{proposerPayload: validProposer}
	e, hall := newRATDTestEngine(t, agent, func(ctx context.Context, cmd, wd string, to time.Duration) *RATDSandboxResult {
		return &RATDSandboxResult{ExitCode: 0, Output: "ok"}
	})
	e.state.RATD = &RATDState{Goal: "实现队列", Phase: RATDPropose}

	resp, err := hall.handleRATDArena(context.Background())
	if err != nil {
		t.Fatalf("handleRATDArena: %v", err)
	}
	if resp == nil {
		t.Fatal("expected delivery response")
	}
	if !strings.Contains(resp.Summary, "代码已交付") || !strings.Contains(resp.Summary, "src/q.go") {
		t.Errorf("delivery missing content: %q", resp.Summary)
	}
	if e.state.RATD != nil {
		t.Error("RATD state should be cleared after delivery")
	}
	if _, err := os.Stat(filepath.Join(e.config.WorkDir, "src", "q.go")); err != nil {
		t.Errorf("source file not written: %v", err)
	}
}

func TestRATDArena_RedTeamTestAppendedAndPassed(t *testing.T) {
	agent := &ratdMockAgent{
		proposerPayload: validProposer,
		redTeamPayloads: []string{`{"test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"edge"}`},
	}
	e, hall := newRATDTestEngine(t, agent, func(ctx context.Context, cmd, wd string, to time.Duration) *RATDSandboxResult {
		return &RATDSandboxResult{ExitCode: 0, Output: "ok"}
	})
	e.state.RATD = &RATDState{Goal: "实现队列", Phase: RATDPropose}

	_, err := hall.handleRATDArena(context.Background())
	if err != nil {
		t.Fatalf("handleRATDArena: %v", err)
	}
	if e.state.RATD != nil {
		t.Errorf("state not cleared: %+v", e.state.RATD)
	}
}

func TestRATDArena_FailThenArbitrateAcceptRefactor(t *testing.T) {
	agent := &ratdMockAgent{
		proposerPayload:    validProposer,
		redTeamPayloads:    []string{`{"test_category":"CORRECTNESS","severity":"P0_CRITICAL","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"bug"}`},
		arbitratorDecision: `{"decision":"ACCEPT_TEST","rejected_reason":"","actionable_feedback":"fix it"}`,
	}
	// First sandbox fails, then after refactor passes.
	calls := 0
	e, hall := newRATDTestEngine(t, agent, func(ctx context.Context, cmd, wd string, to time.Duration) *RATDSandboxResult {
		calls++
		if calls == 1 {
			return &RATDSandboxResult{ExitCode: 1, Output: "FAIL: TestQ"}
		}
		return &RATDSandboxResult{ExitCode: 0, Output: "ok"}
	})
	e.state.RATD = &RATDState{Goal: "实现队列", Phase: RATDPropose}

	resp, err := hall.handleRATDArena(context.Background())
	if err != nil {
		t.Fatalf("handleRATDArena: %v", err)
	}
	if resp == nil {
		t.Fatal("expected delivery after refactor convergence")
	}
	if calls != 2 {
		t.Errorf("expected 2 sandbox runs (fail + refactor pass), got %d", calls)
	}
}

func TestRATDArena_RejectInvalidTest(t *testing.T) {
	agent := &ratdMockAgent{
		proposerPayload: validProposer,
		redTeamPayloads: []string{
			`{"test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"bad"}`,
			`{"no_issues_found": true}`,
		},
		arbitratorDecision: `{"decision":"REJECT_TEST","rejected_reason":"invalid","actionable_feedback":""}`,
	}
	// 第一次沙箱失败（触发仲裁 REJECT 丢弃无效测试），随后 RedTeam 认输
	// → FinalVerify 沙箱成功 → 收敛。验证无效测试被丢弃后仍能正常交付。
	calls := 0
	e, hall := newRATDTestEngine(t, agent, func(ctx context.Context, cmd, wd string, to time.Duration) *RATDSandboxResult {
		calls++
		if calls == 1 {
			return &RATDSandboxResult{ExitCode: 1, Output: "FAIL"}
		}
		return &RATDSandboxResult{ExitCode: 0, Output: "ok"}
	})
	e.state.RATD = &RATDState{Goal: "实现队列", Phase: RATDPropose}

	resp, err := hall.handleRATDArena(context.Background())
	if err != nil {
		t.Fatalf("handleRATDArena: %v", err)
	}
	if resp == nil {
		t.Fatal("expected delivery")
	}
	if e.state.RATD != nil {
		t.Error("state should be cleared")
	}
	if calls != 2 {
		t.Errorf("expected 2 sandbox runs (fail→reject + final verify pass), got %d", calls)
	}
}

func TestRATDArena_MaxRoundsConverges(t *testing.T) {
	// RedTeam keeps producing failing tests (fallback), Arbitrator keeps
	// accepting, sandbox keeps failing → must converge after ratdMaxRounds
	// sandbox runs via the REACHED_MAX_ROUNDS branch.
	agent := &ratdMockAgent{
		proposerPayload:    validProposer,
		arbitratorDecision: `{"decision":"ACCEPT_TEST","rejected_reason":"","actionable_feedback":""}`,
		redTeamFallback:    `{"test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"x"}`,
	}
	sandboxCalls := 0
	e, hall := newRATDTestEngine(t, agent, func(ctx context.Context, cmd, wd string, to time.Duration) *RATDSandboxResult {
		sandboxCalls++
		return &RATDSandboxResult{ExitCode: 1, Output: "FAIL"}
	})
	e.state.RATD = &RATDState{Goal: "实现队列", Phase: RATDPropose}

	resp, err := hall.handleRATDArena(context.Background())
	if err != nil {
		t.Fatalf("handleRATDArena: %v", err)
	}
	if resp == nil {
		t.Fatal("expected delivery after max rounds")
	}
	if e.state.RATD != nil {
		t.Error("state should be cleared")
	}
	if sandboxCalls != ratdMaxRounds {
		t.Errorf("expected %d sandbox runs before max-rounds convergence, got %d", ratdMaxRounds, sandboxCalls)
	}
}
