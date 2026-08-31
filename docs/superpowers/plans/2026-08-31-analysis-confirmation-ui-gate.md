# 方案确认的确定性 UI 通道 — 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把用户确认编辑方案的动作从"LLM 解读文本关键词"改为"UI 弹出框确定性选择 → 引擎 `/confirm N` 命令确定性处理"，消除 `a`/`确认执行A`/`赶紧改吧` 全被漏判的死循环。

**架构：** 复用已存在但从未被引擎赋值的 `EngineResponse.Options []string` 承载确认选项；UI 复用现有 `activeOptions`/`renderOptionsPopup` 弹出框；引擎新增 `/confirm N` 保留前缀：`/confirm 1` 确定性确认（置 `AnalysisReportConfirmed=true`/`AnalysisMode=false` 后 agent 在同一 Run 重提交 edit 即放行），`/confirm N`(N≥2) 作为反馈语义让 agent 调整。确认判定全程绕过 `isDangerousConfirmation` 名单与 intentJudge LLM 分类。

**技术栈：** Go（engine/、context/、ui/ 包）、charmbracelet/bubbletea（UI）。

**关键前提（已核实，实现时无需再查）：**
- `pendingEditPlan` 是死代码（全仓库只有 nil 赋值，从不非 nil）——`/confirm` 不依赖它，复用 `handleAnalysisNudgeConfirmation`（`engine/loop.go:1597`）现有确认机制：置 `AnalysisReportConfirmed=true` 后 agent 在同一 Run 重提交 edit，门控（`engine/turn.go:439` 条件 `!e.state.AnalysisReportConfirmed`）跳过。
- `EngineResponse.Options`（`engine/types.go:105`）已存在、引擎从未赋值——直接复用为确认选项载体，由 loop.go 单点构造填充（见任务 2），无需改动 `TurnResult`。
- UI `activeOptions`/`selectedOption`/`renderOptionsPopup`（`ui/model.go:159-160, 2762-2790`）已存在；Enter 分支现为写数字进输入框（`ui/model.go:1225-1231`），改为内部命令直连引擎。
- 测试构造：引擎侧 `stubIntentJudge`（`engine/loop_intent_test.go`）、`stubStreamModel`/`stubContextBuilder`/`stubToolExecutor`（各测试文件已定义）；UI 侧 `NewModel(runner, engine.PricingConfig{})` + `recordRunner`（本计划新建，实现 `EngineRunner` 全接口）。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/loop.go` | 新增 `parseConfirmCommand`/`handleConfirmCommand`；Run 入口接入；`detectUserIntent` fast-path 加前缀；Run 结束处挂确认选项（任务 2） | 修改 |
| `engine/confirm_command_test.go` | 任务 1、2 的测试（新建） | 创建 |
| `ui/model.go` | activeOptions 的 Enter 分支改直连 `/confirm`；新增 `submitConfirm` | 修改 |
| `ui/confirm_test.go` | 任务 3 的测试（新建） | 创建 |
| `context/builder.go` | 约束措辞软化（`:166-172`） | 修改 |
| `context/builder_test.go` | 追加措辞断言测试 | 修改 |

---

### 任务 1：引擎 — `/confirm N` 解析与确定性处理

**文件：**
- 修改：`engine/loop.go`（import 区 + Run 入口 + `detectUserIntent` fast-path）
- 测试：`engine/confirm_command_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

新建 `engine/confirm_command_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

func TestParseConfirmCommand(t *testing.T) {
	cases := []struct {
		msg string
		n   int
		ok  bool
	}{
		{"/confirm 1", 1, true},
		{"/confirm 2", 2, true},
		{"/confirm 3", 3, true},
		{"/confirm", 0, false},     // 无编号不命中
		{"/confirm 0", 0, false},   // 编号从 1 起
		{"确认", 0, false},           // 非 /confirm 前缀
		{"请按方案A执行", 0, false},
	}
	for _, c := range cases {
		n, ok := parseConfirmCommand(c.msg)
		if n != c.n || ok != c.ok {
			t.Errorf("parseConfirmCommand(%q) = (%d,%v), want (%d,%v)", c.msg, n, ok, c.n, c.ok)
		}
	}
}

// /confirm 1 确定性确认：置 AnalysisReportConfirmed、清 AnalysisMode。
func TestHandleConfirmCommand_ConfirmExecutes(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:   []Message{{Role: "user", Content: "/confirm 1"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 1") {
		t.Fatal("handleConfirmCommand should handle /confirm 1")
	}
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after /confirm 1")
	}
	if !e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should be true after /confirm 1")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after /confirm 1")
	}
	last := e.history[len(e.history)-1].Content
	if !strings.Contains(last, "已确认") {
		t.Errorf("history user message should be rewritten as confirmation, got %q", last)
	}
}

// /confirm N (N>=2) 是反馈语义：不确认执行，AnalysisMode 保持，清除 nudge。
func TestHandleConfirmCommand_FeedbackVariant(t *testing.T) {
	e := &Engine{
		state: &TaskState{
			Goal:                    "修改 .gitignore 并提交 memory/",
			AnalysisMode:            true,
			AnalysisReportConfirmed: false,
		},
		history:   []Message{{Role: "user", Content: "/confirm 2"}},
		isChinese: true,
	}
	e.pendingAnalysisNudge = true

	if !e.handleConfirmCommand("/confirm 2") {
		t.Fatal("handleConfirmCommand should handle /confirm 2")
	}
	if e.state.AnalysisReportConfirmed {
		t.Error("AnalysisReportConfirmed should stay false for /confirm N (N>=2)")
	}
	if !e.state.AnalysisMode {
		t.Error("AnalysisMode should stay true for /confirm N (N>=2)")
	}
	if e.pendingAnalysisNudge {
		t.Error("pendingAnalysisNudge should be false after /confirm N")
	}
}

// detectUserIntent 对 /confirm 前缀走确定性 fast-path，不调用 intentJudge。
func TestDetectUserIntent_ConfirmCommandFastPath(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze} // 即使 judge 判 analyze 也不该被调用
	e := &Engine{state: &TaskState{Goal: "g"}, intentJudge: judge}

	if got := e.detectUserIntent(context.Background(), "/confirm 1"); got != IntentContinue {
		t.Errorf("detectUserIntent(/confirm 1) = %v, want IntentContinue", got)
	}
	if judge.called {
		t.Error("intentJudge must NOT be called for /confirm (deterministic channel)")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine -run 'TestParseConfirmCommand|TestHandleConfirmCommand_|TestDetectUserIntent_ConfirmCommandFastPath' -count=1`
预期：FAIL（`parseConfirmCommand`/`handleConfirmCommand` 未定义）

- [ ] **步骤 3：实现 — import + 两个新函数 + Run 入口接入 + fast-path**

`engine/loop.go` import 区（`"path/filepath"` 之后）新增 `"strconv"`：

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	dlog "github.com/deepact/deepact/internal/log"
	"github.com/deepact/deepact/skill"
)
```

新增函数（放在 `isClearCommand` 定义 `:1585` 附近，`handleAnalysisNudgeConfirmation` 之前）：

```go
// parseConfirmCommand extracts the option number from a "/confirm N" command.
// N is 1-based. Returns (0, false) for anything that is not a valid "/confirm N".
func parseConfirmCommand(userMsg string) (int, bool) {
	trimmed := strings.TrimSpace(userMsg)
	if !strings.HasPrefix(trimmed, "/confirm ") {
		return 0, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "/confirm "))
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// handleConfirmCommand processes a /confirm N message deterministically,
// bypassing isDangerousConfirmation and the intent LLM classifier.
//
// N=1 (the "execute per report" option) is the only confirm-execute signal:
// it flips AnalysisReportConfirmed + clears AnalysisMode so the agent's next
// edit/write in this same Run passes the analysis gate.
// N>=2 (adjust / cancel options) is feedback: cleared for the agent to revise,
// without confirming execution.
// Returns true if userMsg was a valid /confirm command.
func (e *Engine) handleConfirmCommand(userMsg string) bool {
	n, ok := parseConfirmCommand(userMsg)
	if !ok {
		return false
	}
	// Replace the user's bare command with contextual guidance for the agent.
	if len(e.history) > 0 && e.history[len(e.history)-1].Role == "user" {
		if n == 1 {
			e.state.AnalysisReportConfirmed = true
			e.state.AnalysisMode = false
			e.pendingAnalysisNudge = false
			e.history[len(e.history)-1].Content = "✓ 分析报告已确认（方案A：按报告执行修改），可以开始修改代码。"
		} else {
			e.state.AnalysisReportConfirmed = false
			e.state.AnalysisMode = true
			e.pendingAnalysisNudge = false
			e.history[len(e.history)-1].Content = fmt.Sprintf(
				"用户选择了方案（编号 %d）。请根据该方案调整分析，然后重新输出。", n)
		}
	}
	loopLog.Printf("handleConfirmCommand: /confirm %d processed", n)
	return true
}
```

Run 入口接入——在 `e.updateGoalFromFirstMessage(userMsg)`（`loop.go:436`）之后、`e.handleAnalysisNudgeConfirmation(userMsg)`（`loop.go:441`）**之前**插入：

```go
	// /confirm N — deterministic confirmation channel. Must run before
	// handleAnalysisNudgeConfirmation so the state set here is what the agent
	// sees in this same Run, and before intent detection so /confirm is never
	// routed through the LLM classifier.
	e.handleConfirmCommand(userMsg)
```

`detectUserIntent` fast-path（`loop.go:1566-1569`）加前缀：

```go
	// Deterministic safety gate: pure confirmation continues the current task.
	// /confirm N is the UI confirmation channel — treat as continue, never LLM.
	if isDangerousConfirmation(msg) || strings.HasPrefix(msg, "/confirm ") {
		return IntentContinue
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine -run 'TestParseConfirmCommand|TestHandleConfirmCommand_|TestDetectUserIntent_ConfirmCommandFastPath' -count=1`
预期：PASS

同时跑既有意图测试确认无回归：
运行：`go test ./engine -run 'TestDetectUserIntent_|TestIsDangerousConfirmation' -count=1`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/confirm_command_test.go
git commit -m "feat(engine): deterministic /confirm N confirmation channel"
```

---

### 任务 2：引擎 — Run 正常结束时（门控已拦截）挂载确认选项

**文件：**
- 修改：`engine/loop.go`（新增 `confirmOptions()` + Run 结束 return 处挂 Options）
- 测试：`engine/confirm_command_test.go`（追加）

> 设计要点：确认框**不能**在 `turn.go:473` 门控 return 处弹出——那里是 agent 输出
> 报告**之前**的瞬间，用户看不到报告。正确时机是 Run 正常结束（`loop.go:936`）：
> agent 已输出报告（Summary 展示给用户），且本 Run 内门控拦截过。此时才挂 4 项
> 确认选项供 UI 弹出。因此无需给 `TurnResult` 加 `Options` 字段、无需改动 Blocked
> 分支透传——直接精简为 loop.go 单点构造。
>
> **挂载条件用 `e.analysisNudgeCount > 0`，不用 `pendingAnalysisNudge`**：
> `Run()` 入口的 `handleAnalysisNudgeConfirmation`（`loop.go:441`）会无条件消费
> `pendingAnalysisNudge`（确认词→置 `AnalysisReportConfirmed` 并清掉；非确认词→
> feedback 分支清掉），预置后再调 `Run()` 结束时 nudge 必为 false，测试无法构造。
> 而 `analysisNudgeCount` 只在 `Run()` 开头重置（`loop.go:298`）、由本 Run 内门控
> 拦截递增（`turn.go:448`）——真实流程：turn1 拦截 count=1 → turn2 输出报告 Done
> → Run 结束 count=1 且未确认 → 挂载。用户确认后的下一次 `Run()` 开头重置为 0，
> 不再挂载。测试预置 `analysisNudgeCount: 1` 即可精确复现"门控拦截过"的状态。

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/confirm_command_test.go`：

```go
// 本 Run 内门控拦截过（analysisNudgeCount>0）且 agent 已输出报告（Run 正常结束）
// 时，EngineResponse 携带 4 项确认选项，供 UI 弹出确认框。
func TestConfirmOptions_ReturnedWhenGateIntercepted(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "分析完成，计划修改以下文件……", FinishReason: "stop"},
		}},
		context:          &stubContextBuilder{},
		tools:            stubToolExecutor{},
		state:            &TaskState{TurnNumber: 0, AnalysisMode: true},
		history:          []Message{{Role: "user", Content: "修改代码"}},
		config:           EngineConfig{ModelName: "test-model"},
		analysisNudgeCount: 1, // 本 Run 内门控拦截过，agent 已输出报告
	}

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 4 {
		t.Fatalf("expected 4 options, got %d: %v", len(resp.Options), resp.Options)
	}
	if !strings.Contains(resp.Options[len(resp.Options)-1], "其他") {
		t.Errorf("last option should be the free-input item, got %q", resp.Options[len(resp.Options)-1])
	}
}

// 门控未拦截（analysisNudgeCount=0）时，正常结束不携带确认选项。
func TestConfirmOptions_NotReturnedWithoutGate(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成。", FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "修改代码"}},
		config:  EngineConfig{ModelName: "test-model"},
	}

	resp, err := e.Run(context.Background(), "修改代码")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(resp.Options) != 0 {
		t.Errorf("expected no options without gate interception, got %v", resp.Options)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine -run 'TestConfirmOptions_' -count=1`
预期：FAIL（`confirmOptions` 未定义，Run 结束处未挂 Options）

- [ ] **步骤 3：实现 — 新增 confirmOptions + Run 结束处挂载**

新增辅助函数（放在 `buildRunSummary` 定义 `:948` 附近）：

```go
// confirmOptions returns the confirmation options for the analysis-gate popup.
// The last item is the free-input entry — selecting it returns to the input box.
func confirmOptions() []string {
	return []string{
		"方案A: 按报告执行修改",
		"方案B: 调整方案后执行",
		"方案C: 取消本次修改",
		"其他（输入你的意见）",
	}
}
```

`engine/loop.go:936` 的 Run 结束 return 改为：

```go
	// Analysis gate confirmation: if the gate intercepted edits in this Run
	// (agent produced a report but the user hasn't confirmed), present the
	// confirmation options so the UI can show the popup. The report is already
	// visible as Summary. analysisNudgeCount is > 0 only when the gate blocked
	// this Run; it resets to 0 at the next Run's start (loop.go:298), so a
	// confirmed /confirm N (which clears AnalysisMode) won't re-prompt.
	if e.analysisNudgeCount > 0 {
		return &EngineResponse{
			Summary: summary,
			Options: confirmOptions(),
			Stage:   StageVerifyCompact,
		}, nil
	}
	return &EngineResponse{Summary: summary, Stage: StageVerifyCompact}, nil
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine -run 'TestConfirmOptions_' -count=1`
预期：PASS

同时跑既有分析门控测试确认未破坏：
运行：`go test ./engine -run 'TestExecuteTurn_AnalysisGateDegradation_BatchesEdits|TestHandleAnalysisNudgeConfirmation|TestConfirmOptions_' -count=1`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/confirm_command_test.go
git commit -m "feat(engine): present /confirm options when analysis gate report awaits confirmation"
```

---

### 任务 3：UI — Enter 选方案直连引擎

**文件：**
- 修改：`ui/model.go`（activeOptions 的 Enter 分支 + 新增 submitConfirm）
- 测试：`ui/confirm_test.go`（新建）

- [ ] **步骤 1：编写失败的测试**

新建 `ui/confirm_test.go`：

```go
package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)

// recordRunner 记录 Run 的 prompt，供断言 /confirm N 是否被发送。
type recordRunner struct {
	prompts []string
}

func (r *recordRunner) Run(prompt string) tea.Cmd {
	r.prompts = append(r.prompts, prompt)
	return func() tea.Msg { return nil }
}
func (r *recordRunner) Cancel()                                {}
func (r *recordRunner) SetProgressChan(ch chan ProgressMsg)    {}
func (r *recordRunner) ValidateConnection() error              { return nil }
func (r *recordRunner) Steer(msg string)                       {}
func (r *recordRunner) SetSessionID(id string)                 {}
func (r *recordRunner) SetHistory(messages []engine.Message)   {}
func (r *recordRunner) ListSessions() []SessionSummary         { return nil }
func (r *recordRunner) LoadHistory(id string) []engine.Message { return nil }

// 选方案（非末项）Enter → 发送内部 /confirm N 命令，不写入输入框。
// 注意：Update 与 handleKey 均为值接收者（ui/model.go:249, 949），
// 修改发生在返回的 tea.Model 副本上，断言必须从 got 提取 Model。
func TestOptionsEnter_SendsConfirmCommand(t *testing.T) {
	rr := &recordRunner{}
	m := NewModel(rr, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{
		"方案A: 按报告执行修改",
		"方案B: 调整方案后执行",
		"方案C: 取消本次修改",
		"其他（输入你的意见）",
	}
	m.selectedOption = 0 // 方案A
	// 让 submitConfirm 启动路径中的 waitForProgress 不阻塞：预填一条消息。
	m.progressChan = make(chan ProgressMsg, 1)
	m.progressChan <- ProgressMsg{Type: "done"}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command for option selection")
	}
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	cmd() // 执行命令，触发 recordRunner.Run 与 waitForProgress
	if len(rr.prompts) != 1 || rr.prompts[0] != "/confirm 1" {
		t.Errorf("expected Run(\"/confirm 1\"), got prompts=%v", rr.prompts)
	}
	if gotModel.inputBuf.Value() != "" {
		t.Errorf("input buffer should NOT be filled with the option number, got %q", gotModel.inputBuf.Value())
	}
	if len(gotModel.activeOptions) != 0 {
		t.Errorf("activeOptions should be cleared, got %v", gotModel.activeOptions)
	}
}

// 选末项"其他" Enter → 不发命令，回到输入框（activeOptions 清空）。
func TestOptionsEnter_LastItemReturnsToInput(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"方案A", "方案B", "其他（输入你的意见）"}
	m.selectedOption = 2 // 末项

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected NO command when selecting the free-input last item")
	}
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	if len(gotModel.activeOptions) != 0 {
		t.Errorf("activeOptions should be cleared, got %v", gotModel.activeOptions)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui -run 'TestOptionsEnter_' -count=1`
预期：FAIL（当前 Enter 分支写数字进输入框，不产生 `/confirm` 命令，`rr.prompts` 为空）

- [ ] **步骤 3：实现 — Enter 分支改造 + submitConfirm**

`ui/model.go:1225-1231` 的 activeOptions Enter 分支改为（注意先取 `total` 再清空，避免 `len` 归零）：

```go
		case tea.KeyEnter:
			if !msg.Alt {
				// 非末项（方案）→ 发内部 /confirm N 命令确定性确认；
				// 末项"其他（输入你的意见）"→ 关闭弹出框回输入框自由输入。
				n := m.selectedOption + 1
				total := len(m.activeOptions)
				m.activeOptions = nil
				if n == total {
					return m, nil
				}
				return m.submitConfirm(n)
			}
			// Alt+Enter: fall through to InputBuffer for newline
```

新增 `submitConfirm`（放在 `submitInput` `:1435` 定义附近，沿用同款 Run 启动路径）：

```go
// submitConfirm sends an internal "/confirm N" command for the selected option.
// It drives the same Run startup path as submitInput (state→running, spinner,
// engine.Run) but never writes to the input buffer.
func (m Model) submitConfirm(n int) (tea.Model, tea.Cmd) {
	m.state = stateRunning
	m.cancelled = false
	m.runStartMsgIdx = len(m.messages)
	m.toolTree = nil
	m.spinners = []AgentSpinner{{Role: "deepact", Goal: "processing your request...", Active: true}}
	m.streaming = ""
	m.narration = ""
	m.narrationPending = ""
	return m, tea.Batch(
		m.engine.Run(fmt.Sprintf("/confirm %d", n)),
		tea.Tick(spinnerRate, func(time.Time) tea.Msg { return TickMsg{} }),
		waitForProgress(m.progressChan),
	)
}
```

`spinnerRate`/`TickMsg`/`waitForProgress`/`AgentSpinner` 均在 `ui/model.go` 已定义（`submitInput` 同款启动路径），直接沿用。`fmt` 已在 `ui/model.go` import。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui -run 'TestOptionsEnter_' -count=1`
预期：PASS

同时跑既有 UI 测试确认无回归：
运行：`go test ./ui -count=1`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/confirm_test.go
git commit -m "feat(ui): option popup Enter sends /confirm N, last item returns to input"
```

---

### 任务 4：context — 约束措辞软化

**文件：**
- 修改：`context/builder.go:166-172`
- 测试：`context/builder_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `context/builder_test.go`（沿用 `:224-255` 既有 `TestBuild_AnalysisMode*` 的构造方式：`NewContextAssembler(".", nil)` + 设 `userLang`）：

```go
// ANALYSIS MODE 约束应从"引擎级硬禁令"措辞改为"分析阶段契约"措辞，
// 避免 agent 把约束误读为"系统禁止修改、要用户去界面解除模式"。
func TestBuild_AnalysisModeConstraint_SoftWording(t *testing.T) {
	assembler := NewContextAssembler(".", nil)
	assembler.userLang = "中文"
	assembler.userLangSet = true
	assembler.stableSessionBlock = "stable"

	state := &engine.TaskState{Goal: "test goal", AnalysisMode: true}
	msgs := assembler.Build(state, nil, nil)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "[ANALYSIS MODE]") {
			if strings.Contains(msg.Content, "禁止：edit") {
				t.Error("constraint must not use hard-ban wording (禁止：edit)")
			}
			if !strings.Contains(msg.Content, "等待用户") {
				t.Error("constraint should frame analysis as a stage awaiting user confirmation")
			}
			return
		}
	}
	t.Fatal("expected an [ANALYSIS MODE] constraint message")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./context -run 'TestBuild_AnalysisModeConstraint_SoftWording' -count=1`
预期：FAIL（当前措辞含"禁止：edit"）

- [ ] **步骤 3：实现 — 措辞软化**

`context/builder.go:167` 改为：

```go
	if state != nil && state.AnalysisMode {
		constraint := "[ANALYSIS MODE] 本任务处于分析阶段：请先输出分析报告 / 方案，等待用户通过确认选项确认后，再执行修改。"
		if a.userLang != "中文" {
			constraint = "[ANALYSIS MODE] This task is in the analysis stage: present your analysis report / plan first, then wait for the user to confirm via the option popup before making changes."
		}
		messages = append(messages, engine.ModelMessage{Role: "user", Content: constraint})
	}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./context -run 'TestBuild_AnalysisModeConstraint_SoftWording' -count=1`
预期：PASS

同时确认既有约束注入测试未破坏（原断言只查 `[ANALYSIS MODE]` 字样，措辞变化不影响）：
运行：`go test ./context -count=1`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add context/builder.go context/builder_test.go
git commit -m "feat(context): soften ANALYSIS MODE constraint to stage contract wording"
```

---

### 任务 5：端到端回归验证

- [ ] **步骤 1：全量构建 + 测试**

运行：`go build ./... && go test ./engine ./ui ./context -count=1`
预期：全部 PASS

- [ ] **步骤 2：人工验证交互路径**

`go run .` 启动 TUI，触发一次带 edit/write 的修改请求 → 分析门控弹出 4 项确认框 → 方向键选"方案A"→ Enter → agent 直接执行修改（同一 Run 重提交 edit，门控因 `AnalysisReportConfirmed=true` 跳过）。选末项"其他"→ 回到输入框可自由输入意见。

- [ ] **步骤 3：Commit（如本任务产生测试/修复）**

```bash
git add -A
git commit -m "test: e2e regression for /confirm UI confirmation channel"
```

---

## 自检

**1. 规格覆盖度：**
- 规格 §1（`/confirm` 解析 + 确定性确认）→ 任务 1
- 规格 §2（门控拦截后 Run 结束挂确认选项 + 透传）→ 任务 2
- 规格 §3（数据流：`/confirm 1` 确认执行 / N≥2 反馈）→ 任务 1（`handleConfirmCommand` 分支）
- 规格 §4（UI Enter 直连 `/confirm`）→ 任务 3
- 规格 §5（约束措辞软化）→ 任务 4
- 规格测试（引擎 + UI）→ 各任务步骤 1-2 + 任务 5

**2. 占位符扫描：** 所有步骤含完整代码与精确命令，无"待定/TODO/适当错误处理/类似任务 N"类占位。UI 测试用了可精确构造的 `recordRunner`（实现 `EngineRunner` 全接口）与预填 `progressChan` 避免 `waitForProgress` 阻塞，无留白。

**3. 类型一致性：** `parseConfirmCommand`/`handleConfirmCommand` 在任务 1 定义并被其测试引用；`confirmOptions` 在任务 2 定义并被其测试引用；`submitConfirm` 任务 3 定义并被其测试引用；`stubIntentJudge`/`stubStreamModel`/`stubContextBuilder`/`stubToolExecutor` 沿用既有测试文件定义；`recordRunner` 在本计划新建并完整实现接口。命名全程一致（`/confirm N`、`submitConfirm`、`confirmOptions`）。

**4. 与规格的关键修正：** 规格 §3 原引用 `loop.go:467` 的 `pendingEditPlan` 确认执行路径——实现调研发现该字段是死代码（从不非 nil）。本计划改为复用 `handleAnalysisNudgeConfirmation` 现有机制（`/confirm 1` 置 `AnalysisReportConfirmed=true` 后 agent 同一 Run 重提交 edit 即放行），行为等价且更少改动。此修正不改变规格的目标与外部行为。
