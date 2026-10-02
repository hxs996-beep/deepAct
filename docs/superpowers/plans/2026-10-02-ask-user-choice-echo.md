# ask_user 选择回显实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 用户在 ask_user / 确认弹窗中确定选择后，在对话流追加一条不可变的“选择记录”，列出全部候选并标出所选项，便于事后回看与反悔。

**架构：** 纯 UI 展示改动，零引擎改动。在 Enter 确定性选择路径上，把弹窗候选（清空前捕获）传入 `submitConfirm`，由它构造一条 `Role:"choice"` 的 `DisplayMessage` 追加到消息流；`renderMessage` 新增 `"choice"` 渲染分支，首行用新增的 `ChoiceMsgStyle`、候选行复用 `DimStyle`。

**技术栈：** Go、Bubble Tea、lipgloss。

**规格：** `docs/superpowers/specs/2026-10-02-ask-user-choice-echo-design.md`

---

## 文件结构

- 修改：`ui/styles.go` — 新增 `ChoiceMsgStyle`（暗/亮各一处），负责首行的确认色样式。
- 修改：`ui/model.go` — 新增 `newChoiceRecord` 构造器、`renderMessage` 的 `"choice"` 分支、Enter 分支传递候选、`submitConfirm` 签名与追加记录。
- 测试：`ui/confirm_test.go` — 记录构造、渲染、选择路径端到端断言。

---

### 任务 1：选择记录构造器与渲染（含样式）

**文件：**
- 修改：`ui/styles.go:33`（`SystemMsgStyle` 之后新增变量声明）、`ui/styles.go:85`（暗色初始化）、`ui/styles.go:123`（亮色初始化）
- 修改：`ui/model.go:1473`（`newChoiceRecord` 新增于 `submitConfirm` 之前）、`ui/model.go:2046`（`renderMessage` 新增分支）
- 测试：`ui/confirm_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `ui/confirm_test.go` 顶部导入区加入 `"strings"`（保留已有的 `"testing"`、tea、engine）：

```go
import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/deepact/deepact/engine"
)
```

在文件末尾追加：

```go
// 选择记录：列出全部候选并标出所选项。
func TestNewChoiceRecord_ListsCandidatesAndChoice(t *testing.T) {
	rec := newChoiceRecord([]string{"用 Redis 缓存", "改用 MySQL", "输入你的意见"}, 2)
	if rec.Role != "choice" {
		t.Fatalf("role = %q, want %q", rec.Role, "choice")
	}
	for _, want := range []string{"✓ 已选择 [2]", "改用 MySQL", "候选：", "[1] 用 Redis 缓存", "[3] 输入你的意见"} {
		if !strings.Contains(rec.Content, want) {
			t.Errorf("record missing %q; content=%q", want, rec.Content)
		}
	}
}

// 越界编号不得索引候选：只输出序号，不 panic（候选行仍按原样列出）。
func TestNewChoiceRecord_OutOfRange(t *testing.T) {
	rec := newChoiceRecord([]string{"a", "b"}, 5)
	if !strings.Contains(rec.Content, "✓ 已选择 [5]") {
		t.Errorf("out-of-range header missing, got %q", rec.Content)
	}
	if strings.Contains(rec.Content, "✓ 已选择 [5] ") {
		t.Errorf("out-of-range must not append an option label, got %q", rec.Content)
	}
}

// 渲染不 panic，且两行内容输出为两行。
func TestRenderMessage_ChoiceRecord(t *testing.T) {
	rec := newChoiceRecord([]string{"用 Redis 缓存", "改用 MySQL"}, 2)
	lines := renderMessage(rec, 80)
	if len(lines) != 2 {
		t.Errorf("expected 2 rendered lines, got %d: %v", len(lines), lines)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run 'TestNewChoiceRecord|TestRenderMessage_ChoiceRecord' -v`
预期：FAIL（编译失败，`undefined: newChoiceRecord`）

- [ ] **步骤 3：编写最少实现代码**

在 `ui/styles.go` 的变量块中，`SystemMsgStyle` 一行之后新增：

```go
	ChoiceMsgStyle      lipgloss.Style // ask_user selection record header
```

在暗色分支（`SystemMsgStyle = ...` 之后）新增：

```go
		ChoiceMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Bold(true)
```

在亮色分支（`SystemMsgStyle = ...` 之后）新增：

```go
		ChoiceMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("28")).Bold(true)
```

在 `ui/model.go` 的 `submitConfirm` 注释之前（约 `:1473`）新增构造器：

```go
// newChoiceRecord builds the conversation record appended after the user picks
// an option from an ask_user / confirmation popup: it lists every candidate and
// marks the chosen one, so the decision stays visible after the popup closes.
func newChoiceRecord(opts []string, n int) DisplayMessage {
	header := fmt.Sprintf("✓ 已选择 [%d]", n)
	if n >= 1 && n <= len(opts) {
		header = fmt.Sprintf("✓ 已选择 [%d] %s", n, opts[n-1])
	}
	numbered := make([]string, len(opts))
	for i, o := range opts {
		numbered[i] = fmt.Sprintf("[%d] %s", i+1, o)
	}
	content := header
	if len(numbered) > 0 {
		content += "\n候选：" + strings.Join(numbered, "  ")
	}
	return DisplayMessage{Role: "choice", Content: content}
}
```

在 `ui/model.go` 的 `renderMessage` 中，`case "user":` 分支之后新增：

```go
	case "choice":
		// ask_user selection record: the header (chosen option) stands out,
		// the candidate list stays dim. See newChoiceRecord.
		lines := strings.Split(content, "\n")
		styled := make([]string, len(lines))
		for i, l := range lines {
			if i == 0 {
				styled[i] = ChoiceMsgStyle.Render(l)
			} else {
				styled[i] = DimStyle.Render(l)
			}
		}
		return wrapLines(styled, width)
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run 'TestNewChoiceRecord|TestRenderMessage_ChoiceRecord' -v`
预期：PASS（3 个测试全部通过）

- [ ] **步骤 5：Commit**

```bash
git add ui/styles.go ui/model.go ui/confirm_test.go
git commit -m "feat(ui): add the ask_user choice-record message and its renderer"
```

---

### 任务 2：在选择路径追加记录

**文件：**
- 修改：`ui/model.go:1198-1211`（Enter 分支）、`ui/model.go:1476`（`submitConfirm` 签名与追加）
- 测试：`ui/confirm_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `ui/confirm_test.go` 末尾追加：

```go
// 选中方案后应在消息流追加一条 "choice" 记录，且候选来自清空前的 activeOptions。
func TestOptionsEnter_AppendsChoiceRecord(t *testing.T) {
	rr := &recordRunner{}
	m := NewModel(rr, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"用 Redis 缓存", "改用 MySQL", "输入你的意见"}
	m.selectedOption = 0
	m.progressChan = make(chan ProgressMsg, 1)
	m.progressChan <- ProgressMsg{Type: "done"}

	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	if cmd == nil {
		t.Fatal("expected a command for option selection")
	}
	cmd()

	found := false
	for _, msg := range gotModel.messages {
		if msg.Role != "choice" {
			continue
		}
		found = true
		for _, want := range []string{"✓ 已选择 [1]", "用 Redis 缓存", "候选：", "[2] 改用 MySQL", "[3] 输入你的意见"} {
			if !strings.Contains(msg.Content, want) {
				t.Errorf("choice record missing %q; content=%q", want, msg.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected a choice record in the message stream, got %+v", gotModel.messages)
	}
}

// 末项"输入你的意见"是自由输入入口，不得追加选择记录（回归防护）。
func TestOptionsEnter_LastItemNoChoiceRecord(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.state = stateReady
	m.activeOptions = []string{"按报告执行", "输入你的意见"}
	m.selectedOption = 1

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel, ok := got.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", got)
	}
	for _, msg := range gotModel.messages {
		if msg.Role == "choice" {
			t.Errorf("the free-input last item must not append a choice record, got %+v", msg)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run 'TestOptionsEnter_AppendsChoiceRecord' -v`
预期：FAIL，`expected a choice record in the message stream, got [...]`（`TestOptionsEnter_LastItemNoChoiceRecord` 此时已通过，它是回归防护）

- [ ] **步骤 3：编写最少实现代码**

把 `ui/model.go` 的 Enter 分支（`case tea.KeyEnter:` 内 `if !msg.Alt {` 块）改为：

```go
			if !msg.Alt {
				// 非末项（方案）→ 发内部 /confirm N 命令确定性确认；
				// 末项"输入你的意见"→ 关闭弹出框回输入框自由输入。
				n := m.selectedOption + 1
				opts := m.activeOptions
				total := len(opts)
				m.activeOptions = nil
				if n == total {
					// Last item closes the popup back to free input → repaint
					// so the popup background doesn't linger.
					return m, m.repaintCmd()
				}
				return m.submitConfirm(n, opts)
			}
```

把 `ui/model.go` 的 `submitConfirm` 改为（新增 `opts` 参数与追加记录）：

```go
// submitConfirm sends an internal "/confirm N" command for the selected option.
// It drives the same Run startup path as submitInput (state→running, spinner,
// engine.Run) but never writes to the input buffer. opts is the candidate list
// as shown in the popup (captured before activeOptions was cleared); a "choice"
// record is appended so the user can still see which option they picked.
func (m Model) submitConfirm(n int, opts []string) (tea.Model, tea.Cmd) {
	m.messages = append(m.messages, newChoiceRecord(opts, n))
	m.state = stateRunning
	m.cancelled = false
	m.runSeq++
	m.runStartMsgIdx = len(m.messages)
	m.toolTree = nil
	m.spinners = []AgentSpinner{{Role: "deepact", Goal: "processing your request...", Active: true}}
	m.streaming = ""
	m.narration = ""
	m.narrationPending = ""
	return m, tea.Batch(
		m.engine.Run(fmt.Sprintf("/confirm %d", n), m.runSeq),
		tea.Tick(spinnerRate, func(time.Time) tea.Msg { return TickMsg{} }),
		waitForProgress(m.progressChan),
	)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run 'TestOptionsEnter|TestNewChoiceRecord|TestRenderMessage_ChoiceRecord' -v`
预期：PASS（含既有 `TestOptionsEnter_SendsConfirmCommand`、`TestOptionsEnter_LastItemReturnsToInput`）

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/confirm_test.go
git commit -m "feat(ui): echo the picked ask_user option into the conversation"
```

---

### 任务 3：全量验证

**文件：** 无（仅验证）

- [ ] **步骤 1：编译整个模块**

运行：`go build ./...`
预期：无输出（成功）

- [ ] **步骤 2：运行 ui 包全部测试**

运行：`go test ./ui/...`
预期：PASS

- [ ] **步骤 3：运行全仓测试（确认无跨包回归）**

运行：`go test ./...`
预期：PASS（若存在与本改动无关的既有失败，逐条确认是否由本次改动引入）

- [ ] **步骤 4：Commit（仅当步骤 1-3 产生格式/残留改动）**

若前序步骤没有文件改动则跳过；如 `gofmt` 产生改动：

```bash
gofmt -l ui/
git add ui/
git commit -m "chore(ui): gofmt the ask_user choice-echo changes"
```

---

## 自检记录

- **规格覆盖度：** 规格「触发与数据来源」「展示格式」「改动点」「记录构造」「样式」「测试」「验收」均有对应任务——触发/数据 → 任务 2；格式/构造/样式/渲染 → 任务 1；测试 → 任务 1、2；验收 → 任务 3。非目标（不回显问题、不跨 /resume、不覆盖 Tab、不剔除哨兵项）在实现中均未引入对应代码。
- **占位符扫描：** 无 TODO/待定；每个代码步骤含完整可粘贴代码。
- **类型一致性：** `newChoiceRecord(opts []string, n int) DisplayMessage` 在任务 1 定义、任务 2 调用；`submitConfirm(n int, opts []string)` 在任务 2 同时更新签名与唯一调用点（`ui/model.go:1210`）；`ChoiceMsgStyle` 在 `styles.go` 声明并在 `renderMessage` 使用，命名一致。
