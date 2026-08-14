# Steer Queue 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让用户在 agent 运行期间输入补充信息，信息在 turn 间隙注入 LLM 上下文。

**架构：** Engine 持有 `steerQueue []string`（mutex 保护），UI 通过 `Steer()` 方法入队。`drainSteerQueue()` 在三个位置调用：Run() 开头（注入上次 Blocked 保留的消息）、turn 间隙（turns++ 前）、Done 分支（队列非空则自动继续）。

**技术栈：** Go 1.24, sync.Mutex, Bubble Tea TUI

---

## 文件结构

| 文件 | 职责 |
|------|------|
| `engine/steer_test.go` | Steer queue 单元测试（新建） |
| `engine/loop.go` | Engine struct 加字段 + Steer()/drainSteerQueue() + Run() drain 调用 + clearSessionState 清空 |
| `ui/runner.go` | EngineRunner 接口加 Steer() + 两个 runner 实现 |
| `ui/model.go` | DisplayMessage 加 Queued 字段 + stateRunning 输入处理 + progress 事件 |

---

### 任务 1：Steer() 和 drainSteerQueue() 基础方法

**文件：**
- 创建：`engine/steer_test.go`
- 修改：`engine/loop.go:44-58`（Engine struct）、`engine/loop.go` 末尾（新方法）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/steer_test.go`：

```go
package engine

import (
	"context"
	"testing"
	"time"
)

func TestSteer_AndDrain(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("补充信息1")
	e.Steer("补充信息2")

	injected := e.drainSteerQueue()
	if !injected {
		t.Fatal("drainSteerQueue should return true when queue is non-empty")
	}

	if len(e.history) != 2 {
		t.Fatalf("expected 2 messages in history, got %d", len(e.history))
	}
	if e.history[0].Content != "补充信息1" {
		t.Errorf("first message = %q, want %q", e.history[0].Content, "补充信息1")
	}
	if e.history[1].Content != "补充信息2" {
		t.Errorf("second message = %q, want %q", e.history[1].Content, "补充信息2")
	}
	if e.history[0].Role != "user" {
		t.Errorf("first message role = %q, want %q", e.history[0].Role, "user")
	}
}

func TestDrainSteerQueue_Empty(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("drainSteerQueue should return false when queue is empty")
	}
	if len(e.history) != 0 {
		t.Fatalf("history should be empty, got %d messages", len(e.history))
	}
}

func TestSteer_EmptyString(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("")
	e.Steer("   ")

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("drainSteerQueue should return false when only empty strings were queued")
	}
}

func TestDrainSteerQueue_ClearsQueue(t *testing.T) {
	e := &Engine{
		state:   &TaskState{},
		history: make([]Message, 0),
	}

	e.Steer("msg1")
	e.drainSteerQueue()

	// Second drain should be no-op
	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("second drain should return false - queue was already cleared")
	}
}

func TestSteer_EmitsProgressEvent(t *testing.T) {
	var eventTypes []string
	e := &Engine{
		state: &TaskState{},
		config: EngineConfig{
			OnProgress: func(event ProgressEvent) {
				eventTypes = append(eventTypes, event.Type)
			},
		},
	}

	e.Steer("test message")

	if len(eventTypes) != 1 || eventTypes[0] != "steer_queued" {
		t.Fatalf("expected [steer_queued], got %v", eventTypes)
	}

	e.drainSteerQueue()

	// drainSteerQueue should emit steer_injected
	if len(eventTypes) != 2 || eventTypes[1] != "steer_injected" {
		t.Fatalf("expected [steer_queued, steer_injected], got %v", eventTypes)
	}
}

func TestClearSessionState_ClearsSteerQueue(t *testing.T) {
	e := &Engine{
		state:           &TaskState{},
		history:         make([]Message, 0),
		activatedSkills: make(map[string]bool),
	}

	e.Steer("queued message")
	e.clearSessionState()

	injected := e.drainSteerQueue()
	if injected {
		t.Fatal("steer queue should be empty after clearSessionState")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestSteer_ -v 2>&1 | head -20`
预期：FAIL，编译错误 `e.Steer undefined` / `e.drainSteerQueue undefined`

- [ ] **步骤 3：实现 Steer() 和 drainSteerQueue()**

在 `engine/loop.go` 的 Engine struct 中（第 58 行 `history []Message` 之后）添加两个字段：

```go
	history      []Message
	steerMu      sync.Mutex
	steerQueue   []string
```

在 `engine/loop.go` 文件末尾（`clearSessionState` 函数之后）添加两个方法：

```go
// Steer queues a user message to be injected into the conversation at the
// next turn boundary (after current tool execution completes, before the
// next LLM call). Called by the UI when the user submits input during an
// active run.
func (e *Engine) Steer(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	e.steerQueue = append(e.steerQueue, msg)
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "steer_queued", Detail: msg})
	}
}

// drainSteerQueue appends all queued steer messages to history as user
// messages. Returns true if any messages were injected.
func (e *Engine) drainSteerQueue() bool {
	e.steerMu.Lock()
	pending := e.steerQueue
	e.steerQueue = nil
	e.steerMu.Unlock()

	if len(pending) == 0 {
		return false
	}
	for _, msg := range pending {
		e.history = append(e.history, Message{
			Role:      "user",
			Content:   msg,
			Timestamp: time.Now(),
		})
	}
	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{
			Type:   "steer_injected",
			Detail: strings.Join(pending, "\n"),
		})
	}
	loopLog.Printf("steer: injected %d message(s)", len(pending))
	return true
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -run "TestSteer_|TestDrainSteerQueue_|TestClearSessionState_ClearsSteerQueue" -v`
预期：PASS

- [ ] **步骤 5：在 clearSessionState 中清空队列**

在 `engine/loop.go` 的 `clearSessionState()` 函数中（第 1354 行 `e.deactivateSkill()` 之前）添加：

```go
	e.steerMu.Lock()
	e.steerQueue = nil
	e.steerMu.Unlock()

	e.deactivateSkill()
```

- [ ] **步骤 6：运行 clearSessionState 测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestClearSessionState_ClearsSteerQueue -v`
预期：PASS

- [ ] **步骤 7：Commit**

```bash
cd /Users/admin/gitspace/deepact && git add engine/steer_test.go engine/loop.go && git commit -m "feat(engine): add Steer() and drainSteerQueue() for runtime message injection"
```

---

### 任务 2：在 Run() 循环中注入 drain 调用

**文件：**
- 修改：`engine/loop.go:223`（Run 开头）、`engine/loop.go:578-581`（Done 分支）、`engine/loop.go:640`（turns++ 前）
- 测试：`engine/steer_test.go`

- [ ] **步骤 1：编写 Done 自动继续的失败测试**

在 `engine/steer_test.go` 末尾追加以下代码。注意 `context` 和 `time` 已在文件顶部 import。

```go
// multiTurnModel returns pre-configured chunk sets for each Stream call.
type multiTurnModel struct {
	turns   [][]ModelChunk
	callIdx int
}

func (m *multiTurnModel) Stream(_ context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	idx := m.callIdx
	m.callIdx++
	var chunks []ModelChunk
	if idx < len(m.turns) {
		chunks = m.turns[idx]
	} else {
		chunks = []ModelChunk{{Delta: "done", FinishReason: "stop", Usage: &ModelUsage{}}}
	}
	ch := make(chan ModelChunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (m *multiTurnModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{FinishReason: "stop"}, nil
}

func TestRun_DoneWithSteerQueue_AutoContinue(t *testing.T) {
	// Turn 1: model returns text-only (Done=true) -> steer queue has msg -> drain -> continue
	// Turn 2: model returns text-only (Done=true) -> steer queue empty -> break
	turn1Chunks := []ModelChunk{
		{Delta: "任务完成", FinishReason: "stop", Usage: &ModelUsage{}},
	}
	turn2Chunks := []ModelChunk{
		{Delta: "处理了补充信息", FinishReason: "stop", Usage: &ModelUsage{}},
	}
	model := &multiTurnModel{turns: [][]ModelChunk{turn1Chunks, turn2Chunks}}
	e := &Engine{
		model:          model,
		tools:          stubToolExecutor{},
		state:          &TaskState{TaskID: "test", ConfirmedScope: true},
		history:        []Message{{Role: "user", Content: "do something", Timestamp: time.Now()}},
		config:         EngineConfig{MaxTurns: 10, MaxContextTokens: 1000000},
		guards:         &GuardSystem{scope: NewScopeGuard(true), loop: NewLoopGuard("", 6)},
		readLoop:       NewReadLoopState(),
		errorLoop:      NewErrorLoopState(0),
		activatedSkills: make(map[string]bool),
	}

	// Steer before Run - simulates UI calling Steer during a prior Blocked run.
	// The drain at Run() start will inject it.
	e.Steer("补充：也检查测试文件")

	resp, err := e.Run(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Run returned nil response")
	}

	// The steer message should be in history
	found := false
	for _, msg := range e.history {
		if msg.Content == "补充：也检查测试文件" {
			found = true
			break
		}
	}
	if !found {
		t.Error("steer message was not injected into history")
	}

	// The final summary should be from turn 2, not turn 1
	if resp.Summary == "任务完成" {
		t.Error("summary should be from the continued turn, not the initial Done turn")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -run TestRun_DoneWithSteerQueue_AutoContinue -v 2>&1 | head -20`
预期：FAIL（steer 消息在 Run 开头被 drain，但 Done 后没有自动继续，所以 summary 是 "任务完成"）

注意：此测试验证的是 Done 自动继续行为。Run 开头的 drain 会注入消息，但如果第一次 executeTurn 就返回 Done，steer 消息已经在 history 中但 agent 不会继续处理它。需要步骤 5 的 Done 分支修改才能让 agent 自动继续。

实际上这个测试在步骤 3 之前就会因为 steer 消息已在 history 中而部分通过（found=true）。要验证自动继续，需要确认 summary 不是 "任务完成"。在步骤 5 完成前，summary 会是 "任务完成"，测试会 FAIL。

- [ ] **步骤 3：在 Run() 开头添加 drain 调用**

在 `engine/loop.go` 的 `Run()` 方法中，找到第 223 行：

```go
	e.history = append(e.history, Message{Role: "user", Content: userMsg, Timestamp: time.Now()})
```

在这行之后添加：

```go
	// Drain steer queue: inject messages retained from a previous Blocked run.
	e.drainSteerQueue()
```

- [ ] **步骤 4：在 turn 间隙添加 drain 调用**

在 `engine/loop.go` 的 `Run()` 循环中，找到第 640 行 `turns++`。在 `turns++` 之前添加：

```go
		// Drain steer queue before next turn: injects user messages queued
		// during the previous turn's tool execution.
		e.drainSteerQueue()

		turns++
```

- [ ] **步骤 5：在 Done 分支添加自动继续**

在 `engine/loop.go` 第 578-581 行，将：

```go
		if turnResult.Done {
			completionSummary = turnResult.CompletionSummary
			break
		}
```

改为：

```go
		if turnResult.Done {
			completionSummary = turnResult.CompletionSummary
			// If steer messages were queued while the agent was running,
			// inject them and continue the loop instead of returning.
			if e.drainSteerQueue() {
				completionSummary = ""
				continue
			}
			break
		}
```

- [ ] **步骤 6：运行测试验证通过**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -run "TestRun_DoneWithSteerQueue|TestSteer_|TestDrainSteerQueue_|TestClearSessionState_" -v`
预期：PASS

- [ ] **步骤 7：运行全部 engine 测试确保无回归**

运行：`cd /Users/admin/gitspace/deepact && go test ./engine/ -count=1 -short 2>&1 | tail -5`
预期：PASS

- [ ] **步骤 8：Commit**

```bash
cd /Users/admin/gitspace/deepact && git add engine/loop.go engine/steer_test.go && git commit -m "feat(engine): inject steer queue at turn boundaries and auto-continue on Done"
```

---

### 任务 3：EngineRunner 接口 + 两个 Runner 实现

**文件：**
- 修改：`ui/runner.go:17-22`（接口）、`ui/runner.go:24-61`（DefaultEngineRunner）、`ui/runner.go:63-146`（ProgressEngineRunner）

- [ ] **步骤 1：在 EngineRunner 接口添加 Steer 方法**

在 `ui/runner.go` 第 17-22 行，将：

```go
type EngineRunner interface {
	Run(prompt string) tea.Cmd
	Cancel()
	SetProgressChan(ch chan ProgressMsg)
	ValidateConnection() error
}
```

改为：

```go
type EngineRunner interface {
	Run(prompt string) tea.Cmd
	Cancel()
	SetProgressChan(ch chan ProgressMsg)
	ValidateConnection() error
	Steer(msg string)
}
```

- [ ] **步骤 2：DefaultEngineRunner 添加 Steer 实现**

在 `ui/runner.go` 的 `DefaultEngineRunner` 中，`ValidateConnection` 方法之后（约第 48 行）添加：

```go
func (r *DefaultEngineRunner) Steer(msg string) {
	r.Eng.Steer(msg)
}
```

- [ ] **步骤 3：ProgressEngineRunner 添加 Steer 实现**

在 `ui/runner.go` 的 `ProgressEngineRunner` 中，`ValidateConnection` 方法之后（约第 114 行）添加：

```go
func (r *ProgressEngineRunner) Steer(msg string) {
	r.getEngine().Steer(msg)
}
```

- [ ] **步骤 4：编译验证**

运行：`cd /Users/admin/gitspace/deepact && go build ./... 2>&1`
预期：编译通过

- [ ] **步骤 5：Commit**

```bash
cd /Users/admin/gitspace/deepact && git add ui/runner.go && git commit -m "feat(ui): add Steer() to EngineRunner interface and both implementations"
```

---

### 任务 4：UI 层 - stateRunning 输入处理 + Queued 显示

**文件：**
- 修改：`ui/model.go:27-31`（DisplayMessage）、`ui/model.go` KeyEnter 处理、`ui/model.go:448` ProgressMsg 处理

- [ ] **步骤 1：DisplayMessage 添加 Queued 字段**

在 `ui/model.go` 第 27-31 行，将：

```go
type DisplayMessage struct {
	Role     string
	Content  string
	ToolTree []ToolNode
}
```

改为：

```go
type DisplayMessage struct {
	Role     string
	Content  string
	ToolTree []ToolNode
	Queued   bool // true when this is a steer message waiting to be injected
}
```

- [ ] **步骤 2：stateRunning 时 Enter 调用 Steer**

在 `ui/model.go` 的 `Update()` 方法中，找到处理 `tea.KeyEnter`（非 Alt）的代码路径。在现有的 `stateReady` 提交逻辑之前，添加 `stateRunning` 分支：

```go
		// stateRunning: steer the running agent instead of starting a new run
		if m.state == stateRunning && m.inputBuf.Len() > 0 {
			text := m.inputBuf.String()
			m.inputBuf.Reset()
			if strings.TrimSpace(text) != "" {
				m.engine.Steer(text)
				m.messages = append(m.messages, DisplayMessage{
					Role:    "user",
					Content: text,
					Queued:  true,
				})
			}
			return m, nil
		}
```

此代码放在 KeyEnter 的非 Alt 分支开头，在 `stateReady` 提交判断之前。这样 stateRunning 时的 Enter 不会触发新的 Run，而是走 Steer 路径。

- [ ] **步骤 3：ProgressMsg 处理 steer_queued 和 steer_injected**

在 `ui/model.go` 的 `ProgressMsg` 处理中（约第 448 行 `switch msg.Type` 内），添加两个 case：

```go
		case "steer_queued":
			// Message already added to display when Steer() was called from UI.
			// This event confirms it was received by the engine.
		case "steer_injected":
			// Update queued messages to show they've been injected
			for i := range m.messages {
				if m.messages[i].Queued {
					m.messages[i].Queued = false
				}
			}
```

- [ ] **步骤 4：编译验证**

运行：`cd /Users/admin/gitspace/deepact && go build ./... 2>&1`
预期：编译通过

- [ ] **步骤 5：运行全部测试确保无回归**

运行：`cd /Users/admin/gitspace/deepact && go test ./... -short -count=1 2>&1 | tail -10`
预期：PASS

- [ ] **步骤 6：Commit**

```bash
cd /Users/admin/gitspace/deepact && git add ui/model.go && git commit -m "feat(ui): handle user input during stateRunning via Steer queue"
```
