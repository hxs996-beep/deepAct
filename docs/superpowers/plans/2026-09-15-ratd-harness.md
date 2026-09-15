# RATD-Harness（反向测试驱动 Agent 协作）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 用 `/ratd`（反向测试驱动协作 Harness：Proposer 写代码 → RedTeam 写对抗测试 → 沙箱物理执行 → Arbitrator 仲裁）替换 `/debate` 4 轮辩论状态机，交付代码与测试直接落盘。

**架构：** 三角色复用现有 `genericSubAgent`（structured `submit_result`），只换 role prompt + 输出契约（JSON）；引擎做"手"——解析 JSON、写盘、`exec.CommandContext` 跑测试拿 ExitCode 驱动状态机；沙箱=当前工作区直跑（`go test`/`pytest` 等），收敛控制 `maxRounds=3`。删除 `/debate` 全链路。

**技术栈：** Go（engine 包）、现有 `RunWithPrompt` / `Handoff` / `submit_result` 机制、`os/exec`。

**规格：** `docs/superpowers/specs/2026-09-15-ratd-harness-design.md`

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/types.go` | 新增 `RATDState`/`RATDPhase`/`RATDSourceFile`/`RATDTest`/`RATDSandboxResult`；`TaskState.RATD` 字段；删除 roundtable 全套类型 + `EngineConfig.TeamMembers` | 修改 |
| `engine/ratd.go` | `RATDHall`：命令解析、三角色调用、JSON 解析、写盘、沙箱执行、状态机、交付渲染 | 创建 |
| `engine/ratd_test.go` | 各阶段/状态机/契约/隔离/裁决测试 + mock agent + 可注入 sandboxRunner | 创建 |
| `engine/loop.go` | `ratdHall` 字段替换 `roundtableHall`；`parseRATDCommand` 分支替换 debate 命令块；RATD 分支替换 debate Arena 分支；删除 `teamVerdictPending` | 修改 |
| `context/builder.go` | 删除 `roundtableVolatile`/`flattenRoundtable` 及调用行 | 修改 |
| `context/builder_test.go` | 删除 `TestFlattenRoundtable` | 修改 |
| `config/config.go` | 删除 `cfg.TeamMembers` 赋值行 | 修改 |
| `engine/collab_test.go` | `/debate` 负例改为 `/ratd` | 修改 |
| `engine/roundtable.go` | 整个辩论实现 | **删除** |
| `engine/default_members.go` | 默认辩论成员 | **删除** |
| `engine/roundtable_test.go` | 辩论测试 | **删除** |
| `engine/agent.go` | 删除 `AgentTeamLead` 常量 | 修改 |

---

### 任务 1：engine/types.go — 新增 RATD 类型 + 删除 roundtable 类型

**文件：**
- 修改：`engine/types.go`（删除 348-414 行的 `DebateRoundPhase`/`DebateRound`/`DebateOutput`/`RoundtablePhase`/`RoundtableState`；删除 `TaskState.Roundtable` 字段；删除 `EngineConfig.TeamMembers`；新增 RATD 类型与 `TaskState.RATD`）

- [ ] **步骤 1：删除 `TaskState.Roundtable` 字段，新增 `TaskState.RATD`**

`engine/types.go:261` 附近，找到：
```go
	Roundtable          *RoundtableState `json:"roundtable,omitempty"`
```
改为：
```go
	RATD *RATDState `json:"ratd,omitempty"`
```

- [ ] **步骤 2：删除 `EngineConfig.TeamMembers`**

`engine/types.go:87-89`，删除：
```go
	// TeamMembers is the ordered list of member IDs to use in /team debate mode.
	// Empty = use DefaultDebateMembers.
	TeamMembers []string
```

- [ ] **步骤 3：删除 roundtable 全套类型**

`engine/types.go:348-414`，删除 `DebateRoundPhase`（含 4 个常量）、`DebateRound`、`DebateOutput`、`RoundtablePhase`（含 6 个常量与 `String()` 方法）、`RoundtableState` 整块。保留 `CollabStageName`/`CollabPhase`/`CollabState`（collab 继续使用）。

- [ ] **步骤 4：在 types.go 末尾（collab 类型之后）新增 RATD 类型**

```go
// RATDPhase describes which stage of the /ratd harness we are in.
type RATDPhase int

const (
	RATDIdle     RATDPhase = iota
	RATDPropose            // Proposer 生成代码
	RATDRedTeam            // RedTeam 生成对抗测试
	RATDSandbox            // 引擎跑沙箱
	RATDArbitrate          // Arbitrator 判定失败测试
	RATDDone               // 收敛交付
)

func (p RATDPhase) String() string {
	switch p {
	case RATDPropose:
		return "propose"
	case RATDRedTeam:
		return "red_team"
	case RATDSandbox:
		return "sandbox"
	case RATDArbitrate:
		return "arbitrate"
	case RATDDone:
		return "done"
	default:
		return "idle"
	}
}

// RATDSourceFile is one file produced by the Proposer, written to the workdir.
type RATDSourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RATDTest is the RedTeam's adversarial test contribution.
type RATDTest struct {
	Category  string `json:"test_category"`
	Severity  string `json:"severity"`
	TargetFile string `json:"target_file"`
	TestCode  string `json:"test_code"`
	Rationale string `json:"assertion_rationale"`
}

// RATDSandboxResult captures one sandbox execution outcome.
type RATDSandboxResult struct {
	ExitCode    int    `json:"exit_code"`
	Output      string `json:"output,omitempty"`
	TimedOut    bool   `json:"timed_out,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"` // 无可用测试命令，无法物理验证
}

// RATDState tracks the current /ratd session within TaskState.
type RATDState struct {
	Goal         string             `json:"goal"`
	Phase        RATDPhase          `json:"phase"`
	Language     string             `json:"language,omitempty"`
	SourceFiles  []RATDSourceFile   `json:"source_files,omitempty"`
	DesignNotes  string             `json:"design_notes,omitempty"`
	Tests        []RATDTest         `json:"tests,omitempty"`
	CurrentRound int                `json:"current_round"`
	LastSandbox  *RATDSandboxResult `json:"last_sandbox,omitempty"`
	FinalVerify  bool               `json:"final_verify,omitempty"` // no_issues 触发的最终验证轮
}
```

- [ ] **步骤 5：验证编译**

运行：`go build ./engine/...`
预期：报错（roundtable 引用未清理，正常）——记录当前报错清单，作为后续任务的删除面清单。若报错与 roundtable 无关（如手误），修正。

- [ ] **步骤 6：Commit**

```bash
git add engine/types.go
git commit -m "feat(engine): add RATD state types, remove roundtable types"
```

---

### 任务 2：engine/ratd.go — 命令解析 + 角色 prompt 与 goal 构造

**文件：**
- 创建：`engine/ratd.go`（本任务只写解析与 prompt 构造；状态机/写盘/沙箱在任务 3-6）

- [ ] **步骤 1：写命令解析与常量**

```go
package engine

import (
	"fmt"
	"strings"
	"time"
)

const (
	ratdMaxRounds     = 3
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
```

- [ ] **步骤 2：写三角色 role prompt**

```go
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
```

- [ ] **步骤 3：写角色 goal 构造**

```go
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
```

- [ ] **步骤 4：Commit**

```bash
git add engine/ratd.go
git commit -m "feat(engine): RATD command parsing and role prompt/goal builders"
```

---

### 任务 3：engine/ratd.go — JSON 契约解析 + 写盘

**文件：**
- 修改：`engine/ratd.go`

- [ ] **步骤 1：更新 import 并写 JSON 解析函数**

先将 `engine/ratd.go` 顶部 import 更新为（任务 2 只保留了 fmt/strings/time）：
```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)
```

然后新增：

```go
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

// parseTestPayload parses a RedTeam TestPayload. noIssues=true when the payload
// is {"no_issues_found": true}; otherwise returns the parsed test. Errors on
// invalid JSON or ambiguous payloads.
func parseTestPayload(content string) (test *RATDTest, noIssues bool, err error) {
	var m map[string]interface{}
	if err = json.Unmarshal([]byte(content), &m); err != nil {
		return nil, false, fmt.Errorf("invalid TestPayload: %w", err)
	}
	if v, ok := m["no_issues_found"].(bool); ok && v {
		return nil, true, nil
	}
	var t RATDTest
	raw, _ := json.Marshal(m)
	if err = json.Unmarshal(raw, &t); err != nil {
		return nil, false, fmt.Errorf("invalid TestPayload: %w", err)
	}
	if strings.TrimSpace(t.TestCode) == "" || strings.TrimSpace(t.TargetFile) == "" {
		return nil, false, fmt.Errorf("TestPayload missing test_code/target_file")
	}
	return &t, false, nil
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
```

- [ ] **步骤 2：写写盘函数（含路径安全校验）**

```go
// writeSourceFiles writes all Proposer files under workDir. Paths are
// validated to stay within workDir (rejects absolute paths and ../ escapes).
func writeSourceFiles(files []RATDSourceFile, workDir string) error {
	for _, f := range files {
		p := filepath.Join(workDir, filepath.FromSlash(f.Path))
		rel, err := filepath.Rel(workDir, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(f.Path) {
			return fmt.Errorf("unsafe source path %q escapes workdir", f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	return nil
}
```

- [ ] **步骤 3：写解析/写盘的失败测试**（先在 `engine/ratd_test.go` 中）

```go
package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
```

- [ ] **步骤 4：运行测试确认失败**

运行：`go test ./engine/ -run 'TestParseCodePayload|TestParseTestPayload|TestParseArbitration|TestWriteSourceFiles' -v`
预期：编译失败（函数未定义）——本任务步骤 1-2 尚未添加实现时先确认。实现后预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/ratd.go engine/ratd_test.go
git commit -m "feat(engine): RATD JSON contract parsing and safe source write"
```

---

### 任务 4：engine/ratd.go — 沙箱执行

**文件：**
- 修改：`engine/ratd.go`
- 修改：`engine/ratd_test.go`

- [ ] **步骤 1：更新 import 并写语言适配器与沙箱执行**

先将 `engine/ratd.go` 顶部 import 更新为（任务 3 已含 encoding/json/fmt/os/path/filepath/strings/time；补 context 与 os/exec）：
```go
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
```

然后新增：

```go
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
	if len(out) > 4000 {
		out = out[:4000] + "\n... (truncated)"
	}
	res.Output = out
	return res
}
```

- [ ] **步骤 2：在 RATDHall 上写 runSandbox**

```go
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
```

- [ ] **步骤 3：写沙箱测试**

```go
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
}

func TestRATDHall_RunSandbox_Unavailable(t *testing.T) {
	h := &RATDHall{}
	res := h.runSandbox(context.Background(), t.TempDir(), "brainfuck")
	if res == nil || !res.Unavailable {
		t.Error("unknown language should be Unavailable")
	}
}
```

- [ ] **步骤 4：运行测试确认通过**

运行：`go test ./engine/ -run 'TestSandbox|TestExecSandbox|TestRATDHall_RunSandbox' -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/ratd.go engine/ratd_test.go
git commit -m "feat(engine): RATD sandbox execution with language adapters"
```

---

### 任务 5：engine/ratd.go — 三角色 agent 调用

**文件：**
- 修改：`engine/ratd.go`
- 修改：`engine/ratd_test.go`

- [ ] **步骤 1：写角色执行方法**

```go
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
	payload := h.runRole(ctx, "arbitrator", buildArbitratorGoal(goal, state.SourceFiles, test, state.LastSandbox, zh), zh, 5)
	decision, feedback, err := parseArbitration(payload)
	if err != nil {
		return "ACCEPT_TEST", "" // 仲裁失败默认接受（宁可信其有）
	}
	return decision, feedback
}
```

- [ ] **步骤 2：Commit**

```bash
git add engine/ratd.go
git commit -m "feat(engine): RATD role execution via sub agents"
```

---

### 任务 6：engine/ratd.go — 状态机编排 + 交付渲染

**文件：**
- 修改：`engine/ratd.go`

- [ ] **步骤 1：写状态机主循环**

```go
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
				state.RATD.DesignNotes += "\n[arbitrator] " + feedback
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
```

- [ ] **步骤 2：写交付渲染**

```go
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
```

- [ ] **步骤 3：写状态机测试**

```go
// ratdMockAgent returns canned payloads per role for harness tests.
type ratdMockAgent struct {
	proposerPayload   string
	redTeamPayloads   []string // consumed one per RED_TEAM entry
	redTeamFallback   string   // used once payloads are exhausted ("" = no_issues)
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
		proposerPayload: validProposer,
		redTeamPayloads: []string{`{"test_category":"CORRECTNESS","severity":"P0_CRITICAL","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"bug"}`},
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
	// The invalid test must have been dropped: state cleared on delivery.
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
		proposerPayload: validProposer,
		arbitratorDecision: `{"decision":"ACCEPT_TEST","rejected_reason":"","actionable_feedback":""}`,
		redTeamFallback: `{"test_category":"CORRECTNESS","severity":"P1_HIGH","target_file":"src/q_test.go","test_code":"func TestQ(t *testing.T){}","assertion_rationale":"x"}`,
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
```

- [ ] **步骤 4：运行测试确认通过**

运行：`go test ./engine/ -run TestRATDArena -v`
预期：PASS（先确认 mock 与状态机路径正确）

- [ ] **步骤 5：Commit**

```bash
git add engine/ratd.go engine/ratd_test.go
git commit -m "feat(engine): RATD state machine orchestration and delivery"
```

---

### 任务 7：loop.go — 接入 RATD，删除 debate 分支

**文件：**
- 修改：`engine/loop.go`

- [ ] **步骤 1：替换 Engine 字段与初始化**

`engine/loop.go:99-103`，删除：
```go
	// roundtableHall orchestrates multi-stance roundtable discussions.
	roundtableHall *RoundtableHall
```
并在 collabHall 附近新增：
```go
	// ratdHall orchestrates the /ratd harness (proposer/redteam/arbitrator).
	ratdHall *RATDHall
```

`engine/loop.go:105-109`，删除：
```go
	// teamVerdictPending is set when the user's roundtable verdict is processed.
	// On the next Run(), it causes PlanConfirmed to be set, skipping the
	// edit-plan guard - the user already approved the plan through the debate
	// process.
	teamVerdictPending bool
```

`engine/loop.go:194-195`，改为：
```go
	e.ratdHall = NewRATDHall(e)
	e.collabHall = NewCollabHall(e)
```

- [ ] **步骤 2：替换 debate 命令解析块**

`engine/loop.go:314-349`（`// Team command handling — /debate <goal>` 整块），替换为：
```go
	// RATD command handling — /ratd <goal>
	// Activates the reverse-test-driven harness: propose → red-team → sandbox.
	if rc := parseRATDCommand(userMsg); rc != nil {
		e.state.RATD = &RATDState{
			Goal:  rc.Goal,
			Phase: RATDPropose,
		}
		// Replace raw "/ratd <goal>" so the main agent loop sees a proper prompt.
		if len(e.history) > 0 {
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"反向测试驱动协作已启动：%s\n\n请等待 Harness 完成。", rc.Goal)
			userMsg = fmt.Sprintf("反向测试驱动协作已启动：%s\n\n请等待 Harness 完成。", rc.Goal)
		}
	}
```

- [ ] **步骤 3：替换 debate Arena 分支**

`engine/loop.go:650-686`（`// Debate Arena phase ...` 整块），替换为：
```go
	// RATD harness phase — execute the harness state machine to completion.
	if e.state.RATD != nil {
		response, err := e.ratdHall.handleRATDArena(ctx)
		if err != nil {
			return nil, fmt.Errorf("ratd harness: %w", err)
		}
		if response != nil {
			return response, nil
		}
	}
```

- [ ] **步骤 4：删除 teamVerdict 处理块**

`engine/loop.go:722-730`，删除：
```go
	// Team verdict: the user already approved a plan through the debate process.
	// Override the edit-plan guard.
	// Must come AFTER the roundtable block because handleVerdict (in the
	// AwaitingVerdict case above) sets the flag during this same Run().
	if e.teamVerdictPending {
		e.state.PlanConfirmed = true
		e.teamVerdictPending = false
		loopLog.Printf("team verdict: PlanConfirmed=true, skipping edit-plan guard")
	}
```

- [ ] **步骤 5：验证编译**

运行：`go build ./engine/...`
预期：PASS（types/loop 已同步；若还有 roundtable 引用报错，逐个清理）

- [ ] **步骤 6：Commit**

```bash
git add engine/loop.go
git commit -m "feat(engine): wire /ratd harness into Run loop, remove debate arena"
```

---

### 任务 8：删除 /debate 全链路残留

**文件：**
- 删除：`engine/roundtable.go`、`engine/default_members.go`、`engine/roundtable_test.go`
- 修改：`engine/agent.go`、`context/builder.go`、`context/builder_test.go`、`config/config.go`、`engine/collab_test.go`

- [ ] **步骤 1：删除整文件**

```bash
git rm engine/roundtable.go engine/default_members.go engine/roundtable_test.go
```

- [ ] **步骤 2：删除 `AgentTeamLead`**

`engine/agent.go:15`，`AgentTeamLead AgentID = "team-lead"` 改为删除（保留 `AgentSub`）。确认 grep 无其它引用：
运行：`grep -rn "AgentTeamLead" engine/`
预期：仅 agent.go 定义（已删后无输出）

- [ ] **步骤 3：清理 context/builder.go**

删除 `engine/types.go` 中 roundtable 类型后，`context/builder.go:226` 的：
```go
		Roundtable:       flattenRoundtable(state.Roundtable),
```
改为删除该行。

删除 `context/builder.go:281-307` 的 `roundtableVolatile` struct 与 `flattenRoundtable` 函数。

- [ ] **步骤 4：清理 context/builder_test.go**

删除 `TestFlattenRoundtable`（约 180-207 行，含 `tt.rt *engine.RoundtableState` 表驱动）。

- [ ] **步骤 5：清理 config/config.go**

`config/config.go:227`：
```go
		cfg.TeamMembers = f.Team.Members
```
改为删除该行（`TeamMembers` 字段已删）。

- [ ] **步骤 6：替换 collab_test.go 负例**

`engine/collab_test.go:23`：
```go
		"/debate 实现一个功能",
```
改为：
```go
		"/ratd 实现一个功能",
```

- [ ] **步骤 7：验证编译与孤儿扫描**

运行：`go build ./...`
预期：PASS

运行：`grep -rn "Roundtable\|DebateRound\|DebateOutput\|DefaultDebateMembers\|parseTeamCommand\|teamVerdictPending\|TeamMembers\|flattenRoundtable\|roundtableHall" engine context config cmd`
预期：无输出（注释残留可顺手清理，不阻塞）

- [ ] **步骤 8：Commit**

```bash
git add -A
git commit -m "refactor: remove /debate roundtable pipeline entirely"
```

---

### 任务 9：全量验证

- [ ] **步骤 1：构建 + 全量测试（race）**

运行：`go build ./...`
预期：PASS

运行：`go test ./engine/... ./context/... ./config/... -race`
预期：PASS（含新增 ratd 测试与既有 collab/turn 测试）

- [ ] **步骤 2：gofmt**

运行：`gofmt -l engine context config`
预期：无输出（若有，`gofmt -w` 修正后 commit）

- [ ] **步骤 3：Commit（如步骤 2 有改动）**

```bash
git add -A
git commit -m "chore: gofmt after RATD harness"
```

---

## 规格覆盖度自检

- ✅ 新增 `/ratd` 命令、替换 `/debate` → 任务 1（类型）、任务 2（解析）、任务 7（loop 接入）
- ✅ 删除 /debate 全链路 → 任务 1（types）、任务 7（loop）、任务 8（残留）
- ✅ 三角色 JSON 契约（CodePayload/TestPayload/ArbitrationResult）→ 任务 2（prompt/goal）、任务 3（解析）
- ✅ 引擎做手（写盘+执行）、子代理做脑 → 任务 3（writeSourceFiles）、任务 4（沙箱）、任务 5（角色调用）
- ✅ 上下文隔离（RedTeam 不见 design_notes）→ 任务 2 `buildRedTeamGoal` 只收 SourceFiles
- ✅ 沙箱=工作区直跑 + 语言适配器表 → 任务 4 `sandboxCommand`
- ✅ 收敛控制 max_rounds=3 + no_issues + 回归套件累积 → 任务 6 状态机
- ✅ 交付=直接交付（改动清单+最终结果）→ 任务 6 `buildRATDDelivery`
- ✅ 测试 → 任务 3/4/6 测试 + 任务 9 全量

## 明确不做

- 不容器化 / cgroups / Docker（规格已定 v1 直跑）
- 不做 flaky 自动隔离、不做覆盖率目标门控（YAGNI，规格已列）
- 不新增语言专用压力 runner（RedTeam 在 test_code 内自行使用并发原语）
- `/collab` 流水线不动
- 不为三角色加 `MaxIterations` 以外的额外轮数护栏（`ratdRoleIterations=10` 是机制兜底，防止子代理跑飞——对应本次"机制改了吧"诉求）
