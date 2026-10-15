# 用户中断标记写入对话历史 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 用户按 Esc 中断一轮运行后，在对话历史里留下一条 `<turn_aborted>` 记录，让后续请求的模型知道上一轮是用户主动中断的（对齐 codex 语义）。

**架构：** 在 `Engine.Run` 的 defer 组里注册 `noteUserInterrupt(ctx)`；退出时若 `ctx.Err() != nil` 就往 `e.history` 追加一条 `role: "user"` 的标记消息。注册顺序必须排在 `persistHistory` 之后（defer 是 LIFO，因此它会先执行），标记才会随同一 Run 的落盘路径写入 session jsonl。

**技术栈：** Go（标准库 `testing`）＋ 工程自带的 engine 测试桩：`stubStreamModel`（`engine/turn_test.go:57`）、`stubContextBuilder`（`engine/turn_load_skill_test.go:15`）、`stubToolExecutor`（`engine/turn_test.go:75`）、`mockSessionStore`（`engine/loop_persist_test.go:10`）。

**规格：** `docs/superpowers/specs/2026-10-15-user-interrupt-history-marker-design.md`（已 commit `20fe413`）

---

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `engine/loop.go` | 修改 | `Run` 增 1 行 defer；新增 `noteUserInterrupt`（判定 + 追加历史）与 `buildUserInterruptMarker`（模型可见文本） |
| `engine/interrupt_marker_test.go` | 创建 | 5 个测试 + 1 个 ctx 感知的阻塞 model 桩；只覆盖"中断标记"这一件事 |

不新建其他文件、不改任何对外接口、不改 UI/配置。模型可见文本用包级函数承载，沿用 `buildProgressNudge` / `buildProgressBlockMsg`（`engine/loop.go:1305-1315`）的既有约定。

---

### 任务 1：取消的 Run 必须留下中断标记（红灯 → 绿灯）

**文件：**
- 创建：`engine/interrupt_marker_test.go`
- 修改：`engine/loop.go`（`Run` 约 469-474 行处；文件末尾 `buildProgressBlockMsg` 之后）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/interrupt_marker_test.go`：

```go
package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// interruptBlockingModel blocks inside Stream until its ctx is cancelled and
// then reports the cancellation as a stream error — the shape of a real Esc
// press: the llm layer turns the context error into Chunk{Err}.
type interruptBlockingModel struct {
	started chan struct{}
}

func (m *interruptBlockingModel) Stream(ctx context.Context, _ ModelRequest) (<-chan ModelChunk, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	ch := make(chan ModelChunk, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- ModelChunk{Err: ctx.Err()}
	}()
	return ch, nil
}

func (m *interruptBlockingModel) Complete(_ context.Context, _ ModelRequest) (*ModelResponse, error) {
	return &ModelResponse{}, nil
}

// newInterruptTestEngine 构造一个 Run 所需的最小 Engine（字段集合与
// progress_loop_test.go 的用例一致）。
func newInterruptTestEngine(model ModelClient) *Engine {
	return &Engine{
		model:   model,
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{scope: NewScopeGuard()},
	}
}

// countInterruptMarkers 统计 history 中带中断标记的消息条数。
func countInterruptMarkers(e *Engine) int {
	n := 0
	for _, m := range e.history {
		if strings.Contains(m.Content, "<turn_aborted>") {
			n++
		}
	}
	return n
}

// TestRun_CancelledRunAppendsInterruptMarker：ctx 已取消的 Run 必须在 history
// 末尾留下一条 user 角色的中断标记，且只留一条。
func TestRun_CancelledRunAppendsInterruptMarker(t *testing.T) {
	e := newInterruptTestEngine(&stubStreamModel{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := e.Run(ctx, "跑一个长任务")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if resp == nil || resp.BlockedBy != "cancelled" {
		t.Fatalf("resp = %+v, want BlockedBy=cancelled", resp)
	}
	if got := countInterruptMarkers(e); got != 1 {
		t.Fatalf("interrupt markers in history = %d, want 1 (history=%+v)", got, e.history)
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("last message role = %q, want user", last.Role)
	}
	if !strings.Contains(last.Content, "interrupted the previous turn") {
		t.Errorf("last message = %q, want the interrupt guidance text", last.Content)
	}
}

// TestRun_InterruptMidStreamAppendsMarker：运行中（流式生成期间）被取消同样
// 留下标记——这是真实 Esc 的形态。
func TestRun_InterruptMidStreamAppendsMarker(t *testing.T) {
	model := &interruptBlockingModel{started: make(chan struct{}, 1)}
	e := newInterruptTestEngine(model)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Run(ctx, "跑一个长任务")
	}()

	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("model stream was never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if got := countInterruptMarkers(e); got != 1 {
		t.Fatalf("interrupt markers in history = %d, want 1 (history=%+v)", got, e.history)
	}
}

// TestRun_InterruptMarkerPersistedToSession：标记必须随同一 Run 的落盘路径写入
// session。这条锁住 defer 的注册顺序（LIFO → noteUserInterrupt 先于
// persistHistory 执行）；顺序写反时标记既不进请求也不进 session。
func TestRun_InterruptMarkerPersistedToSession(t *testing.T) {
	store := &mockSessionStore{}
	e := newInterruptTestEngine(&stubStreamModel{})
	e.session = store
	e.config.SessionID = "sess-1"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx, "跑一个长任务"); err != nil {
		t.Fatalf("Run error: %v", err)
	}

	for _, ev := range store.events {
		if ev.Type != EventTypeMessage {
			continue
		}
		var m Message
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if strings.Contains(m.Content, "<turn_aborted>") {
			return
		}
	}
	t.Fatalf("no <turn_aborted> message event persisted; events=%+v", store.events)
}
```

- [ ] **步骤 2：运行测试验证失败**

```bash
go test ./engine/ -run 'Interrupt' -count=1 -v
```

预期：编译通过，3 个用例全部 FAIL，失败信息是标记数量与期望不符，例如：

```
--- FAIL: TestRun_CancelledRunAppendsInterruptMarker (0.00s)
    interrupt_marker_test.go:70: interrupt markers in history = 0, want 1
```

- [ ] **步骤 3：编写最少实现代码**

修改 `engine/loop.go`，在 `Run` 的 `defer e.cancelBackgroundTasks()` 之后插入一行 defer：

```go
	// Background async sub-agent tasks never outlive this Run.
	defer e.cancelBackgroundTasks()
	// Registered AFTER persistHistory on purpose: defers run LIFO, so this one
	// runs FIRST — the interrupt record is appended to history and is then
	// persisted by the persistHistory defer, keeping the request and the
	// session transcript in sync. Swapping the order silently drops the marker
	// from both.
	defer e.noteUserInterrupt(ctx)
	// Ensure the background task table exists for this Run (paired with the
```

在 `buildProgressBlockMsg` 函数之后（`engine/loop.go:1312-1315`）追加两个函数：

```go
// noteUserInterrupt records that the user aborted this Run (Esc → ctx cancel),
// so the next request's model knows the previous turn was interrupted on
// purpose. Without it the model only sees its own partial output and may treat
// a half-finished plan as accepted, or overlook that an aborted command may have
// executed partially. The judgement is ctx alone, which covers every cancel
// shape (mid-stream, mid-tool, between turns) without per-case branches.
//
// The record is a plain "user" message: the system prompt is injected only at
// session start and a tool message requires a tool_call_id, so "user" is the
// only appendable channel. It goes into history (not pendingPinnedMessages) so
// persistHistory writes it to the session transcript and /resume rebuilds it.
func (e *Engine) noteUserInterrupt(ctx context.Context) {
	if ctx.Err() == nil {
		return
	}
	e.history = append(e.history, Message{
		Role:      "user",
		Content:   buildUserInterruptMarker(),
		Timestamp: time.Now(),
	})
}

// buildUserInterruptMarker is the model-facing text of the interrupt record.
// English (prompts are English-only) and <turn_aborted>-tagged, matching the
// marker codex writes when a turn is aborted.
func buildUserInterruptMarker() string {
	return "<turn_aborted>\nThe user interrupted the previous turn on purpose. " +
		"Any running tools may still be running in the background. " +
		"If any tools/commands were aborted, they may have partially executed.\n</turn_aborted>"
}
```

- [ ] **步骤 4：运行测试验证通过**

```bash
go test ./engine/ -run 'Interrupt' -count=1 -v
go test ./engine/ -run 'Interrupt' -count=1 -race
```

预期：`ok  github.com/deepact/deepact/engine`，5 个用例（本任务 3 个）全部 PASS，`-race` 无数据竞争报告。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/interrupt_marker_test.go
git commit -m "feat(engine): record a user interrupt in conversation history"
```

---

### 任务 2：守卫与持久性回归锁

**文件：**
- 修改：`engine/interrupt_marker_test.go`（追加两个用例）

- [ ] **步骤 1：追加守卫测试**

追加到 `engine/interrupt_marker_test.go` 末尾：

```go
// TestRun_CompletedRunHasNoInterruptMarker：正常跑完的 Run 不得留下中断标记
// ——标记只在用户取消时注入，不能污染每一次 Run。
func TestRun_CompletedRunHasNoInterruptMarker(t *testing.T) {
	e := newInterruptTestEngine(&stubStreamModel{chunks: []ModelChunk{
		{Delta: "完成。", FinishReason: "stop"},
	}})
	if _, err := e.Run(context.Background(), "普通任务"); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := countInterruptMarkers(e); got != 0 {
		t.Fatalf("interrupt markers in history = %d, want 0 (history=%+v)", got, e.history)
	}
}

// TestRebuildHistoryKeepsInterruptMarker：/resume 重建后标记仍在（方案 B 的
// 持久性目标）。
func TestRebuildHistoryKeepsInterruptMarker(t *testing.T) {
	payload, err := json.Marshal(Message{Role: "user", Content: buildUserInterruptMarker()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	msgs := RebuildHistory([]Event{{Type: EventTypeMessage, Payload: payload}}, 0)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "<turn_aborted>") {
		t.Fatalf("RebuildHistory dropped the interrupt marker: %+v", msgs)
	}
}
```

- [ ] **步骤 2：运行测试确认回归锁成立**

```bash
go test ./engine/ -run 'Interrupt' -count=1 -v
```

预期：全部 PASS。这两条是**回归锁**而非红灯用例——第一条锁"不做多余注入"（若失败说明触发判据写成了无条件注入，回到任务 1 步骤 3 修正 `noteUserInterrupt` 的 `ctx.Err()` 守卫），第二条锁 `/resume` 的持久性。

- [ ] **步骤 3：Commit**

```bash
git add engine/interrupt_marker_test.go
git commit -m "test(engine): lock no-marker on a completed run and marker survival on resume"
```

---

### 任务 3：全量验证与文档收尾

**文件：**
- 修改：`docs/superpowers/specs/2026-10-15-user-interrupt-history-marker-design.md`（状态行）

- [ ] **步骤 1：编译与全量测试**

```bash
go build ./...
go test ./engine/ -count=1
go test ./... -count=1
```

预期：`go build ./...` 无输出；`engine` 包 `ok`；全仓全部 `ok`（无 FAIL）。

- [ ] **步骤 2：规格状态置为已实施**

把该规格头部的

```
状态：已批准，待实施
```

改为

```
状态：已实施
```

- [ ] **步骤 3：Commit**

```bash
git add docs/superpowers/specs/2026-10-15-user-interrupt-history-marker-design.md
git commit -m "docs(engine): mark the interrupt-marker spec as implemented"
```
