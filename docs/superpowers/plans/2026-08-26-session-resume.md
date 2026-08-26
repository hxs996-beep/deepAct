# 会话恢复（/resume）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** TUI 内 `/resume` 弹出会话选择器，选择后以 continue 语义（沿用原会话 ID）恢复，重放最近的用户+assistant 文本流。

**架构：** 引擎新增 `message` 事件把对话历史落盘到既有 JSONL 会话文件（Run 收尾 defer 单点写入，tool 消息摘要化）；新增 `RebuildHistory` 从事件重建+按 token 预算裁剪+剥离工具链；`EngineRunner` 接口扩展 `SetSessionID`/`SetHistory`/`ListSessions`/`LoadHistory`；TUI 新增 `stateResume` 状态与选择器。

**技术栈：** Go，标准库（encoding/json/os/path/filepath/strings），既有 bubbletea 状态机（`ui/model.go` handleKey / submitInput / View）、`session.Store`（`session/store.go`）。

## 文件结构

| 文件 | 职责 |
|------|------|
| `engine/types.go`（改） | `Event.WorkDir` 字段、`EventTypeMessage` 常量 |
| `engine/loop.go`（改） | `persistedCount` 字段、`persistHistory()`、`SetSessionID()`、`SetHistory()`、Run 收尾 defer |
| `engine/resume.go`（新） | `RebuildHistory()`（过滤+裁剪+剥离）、`DefaultResumeBudget`、`tokenCount()` |
| `session/store.go`（改） | `SessionInfo.FirstMsg` 字段、`sessionStats()` 提取首条 user 消息摘要 |
| `ui/runner.go`（改） | `EngineRunner` 接口扩展、`SessionSummary`、`ProgressEngineRunner`/`DefaultEngineRunner` 实现 |
| `ui/model.go`（改） | `stateResume` 状态、`/resume` 命令、选择器渲染与按键、`applyResume()` |
| `cmd/run.go`（不改） | store 已在 deps.Session 中；`ProgressEngineRunner` 经类型断言访问 `*session.Store` |
| 测试 | `engine/loop_persist_test.go`、`engine/loop_resume_test.go`、`session/store_test.go`（改）、`ui/resume_test.go` |

---

### 任务 1：engine — 事件持久化（message 事件 + WorkDir + persistHistory）

**文件：**
- 修改：`engine/types.go:205-211`（Event 结构）、`engine/types.go`（常量区）
- 修改：`engine/loop.go:166-190`（Engine 字段/NewEngine）、`engine/loop.go:226-232`（Run 收尾 defer）、`engine/loop.go:1122-1135`（emitEvent）
- 测试：创建 `engine/loop_persist_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/loop_persist_test.go`：

```go
package engine

import (
	"strings"
	"testing"
)

// mockSessionStore 记录 AppendEvent 调用，供持久化测试断言。
type mockSessionStore struct {
	events []Event
}

func (m *mockSessionStore) AppendEvent(e Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *mockSessionStore) LoadEvents(sessionID string) ([]Event, error) {
	return m.events, nil
}

// TestPersistHistoryWritesMessageEvents 验证 persistHistory 把 history 落盘为
// message 事件：user/assistant 全文、tool 摘要、reasoning_content 不落盘。
func TestPersistHistoryWritesMessageEvents(t *testing.T) {
	store := &mockSessionStore{}
	e := &Engine{
		session: store,
		config:  EngineConfig{SessionID: "sess-1", WorkDir: "/proj"},
		state:   &TaskState{},
		history: []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好，需要什么帮助？", ReasoningContent: "思考中"},
			{Role: "tool", ToolCallID: "call-1", Content: "第一行\n第二行\n第三行\n"},
		},
	}
	e.persistHistory()

	var msgs []Message
	for _, ev := range store.events {
		if ev.Type != EventTypeMessage {
			t.Fatalf("event type = %q, want %q", ev.Type, EventTypeMessage)
		}
		if ev.WorkDir != "/proj" {
			t.Errorf("ev.WorkDir = %q, want /proj", ev.WorkDir)
		}
		var m Message
		if err := jsonUnmarshal(ev.Payload, &m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "你好" {
		t.Errorf("msgs[0] = %+v, want user 你好", msgs[0])
	}
	// assistant 全文、reasoning 清空
	if msgs[1].Role != "assistant" || msgs[1].Content != "你好，需要什么帮助？" {
		t.Errorf("msgs[1] = %+v, want full assistant content", msgs[1])
	}
	if msgs[1].ReasoningContent != "" {
		t.Errorf("msgs[1].ReasoningContent = %q, want empty", msgs[1].ReasoningContent)
	}
	// tool 摘要：首行 + 行数
	if msgs[2].Role != "tool" || !strings.Contains(msgs[2].Content, "第一行") || !strings.Contains(msgs[2].Content, "3 lines") {
		t.Errorf("msgs[2] = %+v, want brief digest (first line + line count)", msgs[2])
	}
}

// TestPersistHistoryResumesFromPersistedCount 验证第二次 persistHistory 只写新增消息。
func TestPersistHistoryResumesFromPersistedCount(t *testing.T) {
	store := &mockSessionStore{}
	e := &Engine{
		session: store,
		config:  EngineConfig{SessionID: "sess-1"},
		state:   &TaskState{},
		history: []Message{{Role: "user", Content: "第一条"}},
	}
	e.persistHistory()
	if len(store.events) != 1 {
		t.Fatalf("first persist: %d events, want 1", len(store.events))
	}
	e.history = append(e.history, Message{Role: "assistant", Content: "回复"})
	e.persistHistory()
	if len(store.events) != 2 {
		t.Fatalf("second persist: %d events, want 2 (only new)", len(store.events))
	}
}

// jsonUnmarshalEventPayload 解包 message 事件 payload。
func jsonUnmarshalEventPayload(ev Event, out *Message) error {
	return jsonUnmarshal(ev.Payload, out)
}
```

在 `engine/loop_persist_test.go` 顶部加 helper（文件内已引用的 `jsonUnmarshal` 定义见下；为避免与包内符号冲突，直接用 `encoding/json`）：

```go
import "encoding/json"

func jsonUnmarshal(data []byte, out *Message) error {
	return json.Unmarshal(data, out)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestPersistHistory -v`
预期：FAIL（编译错误——`persistHistory` 未定义、`EventTypeMessage` 未定义、`Event.WorkDir` 不存在、`persistedCount` 不存在）

- [ ] **步骤 3：编写实现代码**

修改 `engine/types.go`，在 Event 结构（第 205 行）加 `WorkDir` 字段，并新增常量：

```go
// EventTypeMessage 记录对话消息（user/assistant 全文、tool 摘要），
// 供 /resume 会话恢复重建历史。
const EventTypeMessage = "message"

type Event struct {
	SessionID string          `json:"session_id"`
	WorkDir   string          `json:"work_dir,omitempty"`
	Type      string          `json:"type"`
	Stage     Stage           `json:"stage"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}
```

修改 `engine/loop.go`：

1. 在 `Engine` 结构体（`runStartHistoryLen` 字段附近，约第 137 行）加字段：

```go
	// persistedCount 记录已落盘为 message 事件的 history 消息数。
	// persistHistory 只写 history[persistedCount:]，压缩后越界时重置为 0。
	persistedCount int
```

2. 修改 `emitEvent`（第 1122-1135 行），在构造 Event 时填 `WorkDir`：

```go
	event := Event{SessionID: e.config.SessionID, WorkDir: e.config.WorkDir, Type: eventType, Stage: stage, Timestamp: time.Now(), Payload: data}
```

3. 在 `Run()` 开头（`if e.state == nil { return ... }` 之后，第 228-229 行）加 defer：

```go
	// Persist this Run's conversation history as message events on every exit
	// path (Run has ~20 returns). tool messages are stored as brief digests.
	defer e.persistHistory()
```

4. 新增 `persistHistory` 方法（放在 `emitEvent` 之后）：

```go
// persistHistory 把本次 Run 新增的对话消息落盘为 message 事件。
// user/assistant 全文；tool 仅保留 briefDigest 摘要；reasoning_content 不落盘。
func (e *Engine) persistHistory() {
	if e.session == nil {
		return
	}
	if e.persistedCount > len(e.history) {
		// 压缩替换了 history → 从 0 重写（恢复窗口语义，避免越界 panic）
		e.persistedCount = 0
	}
	for _, msg := range e.history[e.persistedCount:] {
		persisted := msg
		persisted.ReasoningContent = ""
		if persisted.Role == "tool" {
			persisted.Content = briefDigest(persisted.Content)
		}
		if err := e.emitEvent(EventTypeMessage, StageAct, persisted); err != nil {
			loopLog.Printf("persist message event: %v", err)
		}
	}
	e.persistedCount = len(e.history)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestPersistHistory -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归**

运行：`go test ./engine/`
预期：全部 PASS

- [ ] **步骤 6：Commit**

```bash
git add engine/types.go engine/loop.go engine/loop_persist_test.go
git commit -m "feat(engine): persist conversation history as message events"
```

---

### 任务 2：engine — 会话重建（SetSessionID / SetHistory / RebuildHistory）

**文件：**
- 创建：`engine/resume.go`
- 修改：`engine/loop.go`（新增 `SetSessionID` / `SetHistory` 方法）
- 测试：创建 `engine/loop_resume_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/loop_resume_test.go`：

```go
package engine

import (
	"strings"
	"testing"
)

// TestRebuildHistory 验证 RebuildHistory：过滤 message 事件、跳过 tool、
// 剥离 ToolCalls、按 budget 裁剪（user 边界）。
func TestRebuildHistory(t *testing.T) {
	events := []Event{
		{Type: "user_message", Payload: jsonRaw(`"旧事件被忽略"`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"问题1"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答1"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"tool","tool_call_id":"c1","content":"工具结果"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答2","tool_calls":[{"id":"c1","name":"grep","arguments":"{}"}]}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"问题3"}`)},
	}
	msgs := RebuildHistory(events, 1<<30) // 大预算：全保留
	// 期望：user/assistant 文本流，tool 被跳过，tool_calls 被剥离
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d, want 4 (user,assistant,assistant,user)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "问题1" {
		t.Errorf("msgs[0] = %+v, want user 问题1", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "回答1" {
		t.Errorf("msgs[1] = %+v, want assistant 回答1", msgs[1])
	}
	if msgs[2].Role != "assistant" || msgs[2].Content != "回答2" {
		t.Errorf("msgs[2] = %+v, want assistant 回答2", msgs[2])
	}
	if len(msgs[2].ToolCalls) != 0 {
		t.Errorf("msgs[2].ToolCalls = %+v, want stripped", msgs[2].ToolCalls)
	}
	if msgs[3].Role != "user" || msgs[3].Content != "问题3" {
		t.Errorf("msgs[3] = %+v, want user 问题3", msgs[3])
	}
}

// TestRebuildHistoryTrimsToBudget 验证 budget 裁剪且在 user 边界切割。
func TestRebuildHistoryTrimsToBudget(t *testing.T) {
	events := []Event{
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"用户A"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答A"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"user","content":"用户B"}`)},
		{Type: EventTypeMessage, Payload: jsonRaw(`{"role":"assistant","content":"回答B"}`)},
	}
	// budget 小到只能容纳最后 2 条 → 裁剪应从 user（用户B）开始，保留 2 条
	msgs := RebuildHistory(events, tokenCount("用户B")+tokenCount("回答B"))
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2 (cut at user boundary)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "用户B" {
		t.Errorf("msgs[0] = %+v, want user 用户B (cut at user boundary)", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "回答B" {
		t.Errorf("msgs[1] = %+v, want assistant 回答B", msgs[1])
	}
}

// TestRebuildHistoryEmpty 验证无 message 事件返回空。
func TestRebuildHistoryEmpty(t *testing.T) {
	events := []Event{{Type: "user_message", Payload: jsonRaw(`"x"`)}}
	if msgs := RebuildHistory(events, 16384); len(msgs) != 0 {
		t.Fatalf("len(msgs) = %d, want 0", len(msgs))
	}
}

// TestSetSessionIDAndHistory 验证 SetSessionID / SetHistory 状态迁移。
func TestSetSessionIDAndHistory(t *testing.T) {
	e := &Engine{config: EngineConfig{SessionID: "old"}, state: &TaskState{TaskID: "old"}}
	e.SetSessionID("new-id")
	if e.config.SessionID != "new-id" || e.state.TaskID != "new-id" {
		t.Errorf("SetSessionID not applied: config=%s state=%s", e.config.SessionID, e.state.TaskID)
	}
	hist := []Message{{Role: "user", Content: "预载"}}
	e.SetHistory(hist)
	if len(e.history) != 1 || e.history[0].Content != "预载" {
		t.Errorf("SetHistory not applied: %+v", e.history)
	}
	if e.persistedCount != 1 {
		t.Errorf("persistedCount = %d, want 1 (preloaded history not re-persisted)", e.persistedCount)
	}
}

// jsonRaw 构造测试用 JSON raw payload。
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}
```

在 `engine/loop_resume_test.go` 顶部补 import：

```go
import "encoding/json"
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestRebuildHistory -v && go test ./engine/ -run TestSetSessionIDAndHistory -v`
预期：FAIL（编译错误——`RebuildHistory`/`SetSessionID`/`SetHistory` 未定义）

- [ ] **步骤 3：编写实现代码**

创建 `engine/resume.go`：

```go
package engine

import (
	"encoding/json"
)

// DefaultResumeBudget 是 /resume 恢复历史的最大 token 预算，
// 对齐 compressor.go 的 tailBudget=16384（DeepSeek 缓存甜点区）。
const DefaultResumeBudget = 16384

// tokenCount 粗略估算文本 token 数（约 4 字符/token）。
func tokenCount(s string) int {
	return len([]rune(s))/4 + 1
}

// RebuildHistory 从会话事件重建可重放的对话历史：
//  1. 过滤 message 事件
//  2. 跳过 tool 消息、剥离 ToolCalls（规避 assistant(tool_calls)→tool 的 API 契约）
//  3. 从尾部向前按 budget 累加 token，超预算时回退到最近 user 消息边界切割
func RebuildHistory(events []Event, budget int) []Message {
	var msgs []Message
	for _, ev := range events {
		if ev.Type != EventTypeMessage {
			continue
		}
		var m Message
		if err := json.Unmarshal(ev.Payload, &m); err != nil {
			continue
		}
		if m.Role == "tool" {
			continue
		}
		m.ToolCalls = nil
		msgs = append(msgs, m)
	}
	if budget <= 0 || len(msgs) == 0 {
		return msgs
	}
	total := 0
	cut := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		total += tokenCount(msgs[i].Content)
		if total > budget {
			cut = i
			// 回退到最近 user 消息，保证恢复起点是完整用户提问
			for cut > 0 && msgs[cut].Role != "user" {
				cut--
			}
			break
		}
	}
	if cut > 0 {
		msgs = msgs[cut:]
	}
	return msgs
}
```

在 `engine/loop.go` 新增两个方法（放在 `SetOnProgress` 附近）：

```go
// SetSessionID 切换会话 ID（/resume continue 语义：后续事件写入该文件）。
func (e *Engine) SetSessionID(id string) {
	e.config.SessionID = id
	if e.state != nil {
		e.state.TaskID = id
	}
}

// SetHistory 预载恢复的会话历史（首次 Run() 前调用）。
// 预载的消息不再重复落盘（persistedCount 同步）。
func (e *Engine) SetHistory(h []Message) {
	e.history = append([]Message(nil), h...)
	e.persistedCount = len(e.history)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestRebuildHistory -v && go test ./engine/ -run TestSetSessionIDAndHistory -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 engine 测试防回归**

运行：`go test ./engine/`
预期：全部 PASS

- [ ] **步骤 6：Commit**

```bash
git add engine/resume.go engine/loop.go engine/loop_resume_test.go
git commit -m "feat(engine): rebuild session history (trim + strip tool chain)"
```

---

### 任务 3：session — 会话预览（SessionInfo.FirstMsg）

**文件：**
- 修改：`session/store.go:20-26`（SessionInfo）、`session/store.go:85-118`（List）、`session/store.go:124-157`（sessionStats）
- 测试：修改 `session/store_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**

在 `session/store_test.go` 末尾追加：

```go
// TestListFirstMsg verifies List() populates FirstMsg from the first user message.
func TestListFirstMsg(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}
	// user_message 事件 + message 事件
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: "user_message", Timestamp: time.Now(),
		Payload: json.RawMessage(`"第一条用户消息，这是一段较长的内容需要截断"`)})
	s.AppendEvent(engine.Event{SessionID: "sess1", Type: engine.EventTypeMessage, Timestamp: time.Now(),
		Payload: json.RawMessage(`{"role":"assistant","content":"回答"}`)})

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if !strings.Contains(infos[0].FirstMsg, "第一条用户消息") {
		t.Errorf("FirstMsg = %q, want contains 第一条用户消息", infos[0].FirstMsg)
	}
	if len(infos[0].FirstMsg) > 40 {
		t.Errorf("FirstMsg = %q, want truncated to <=40 runes", infos[0].FirstMsg)
	}
}
```

检查 `session/store_test.go` 顶部 imports 是否含 `strings` 和 `encoding/json`；缺则补：

```go
import (
	"encoding/json"
	"strings"
)
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./session/ -run TestListFirstMsg -v`
预期：FAIL（编译错误——`SessionInfo` 无 `FirstMsg` 字段）

- [ ] **步骤 3：编写实现代码**

修改 `session/store.go`：

```go
type SessionInfo struct {
	ID         string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	EventCount int
	FirstMsg   string // 首条 user 消息摘要（会话预览，≤40 rune）
}
```

修改 `List()` 中 `sessionStats` 调用处（第 105 行）与填充处（第 115 行）：

```go
		created, updated, count, firstMsg, err := sessionStats(path)
		if err != nil {
			return nil, err
		}
		if created.IsZero() {
			created = info.ModTime()
		}
		if updated.IsZero() {
			updated = info.ModTime()
		}
		infos = append(infos, SessionInfo{ID: id, CreatedAt: created, UpdatedAt: updated, EventCount: count, FirstMsg: firstMsg})
```

修改 `sessionStats`（签名 + 提取首条 user 消息）：

```go
func sessionStats(path string) (time.Time, time.Time, int, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, time.Time{}, 0, "", fmt.Errorf("open session file: %w", err)
	}
	defer file.Close()

	var created time.Time
	var updated time.Time
	count := 0
	var firstMsg string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		count++
		var event engine.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return time.Time{}, time.Time{}, 0, "", fmt.Errorf("unmarshal event: %w", err)
		}
		if created.IsZero() || event.Timestamp.Before(created) {
			created = event.Timestamp
		}
		if updated.IsZero() || event.Timestamp.After(updated) {
			updated = event.Timestamp
		}
		// 提取首条 user 消息作为会话预览（user_message 事件优先，message 事件兜底）
		if firstMsg == "" && event.Type == "user_message" {
			firstMsg = extractFirstMsg(event.Payload)
		}
		if firstMsg == "" && event.Type == engine.EventTypeMessage && event.Payload != nil {
			var m engine.Message
			if err := json.Unmarshal(event.Payload, &m); err == nil && m.Role == "user" && strings.TrimSpace(m.Content) != "" {
				firstMsg = m.Content
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, time.Time{}, 0, "", fmt.Errorf("read session file: %w", err)
	}
	return created, updated, count, firstMsg, nil
}

// extractFirstMsg 提取 user_message 事件的 payload 文本并截断到 40 rune。
func extractFirstMsg(payload json.RawMessage) string {
	var s string
	if err := json.Unmarshal(payload, &s); err != nil {
		return ""
	}
	return truncateRunes(strings.TrimSpace(s), 40)
}

// truncateRunes 按 rune 截断字符串。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./session/ -v`
预期：全部 PASS（含既有测试）

- [ ] **步骤 5：Commit**

```bash
git add session/store.go session/store_test.go
git commit -m "feat(session): expose first-message preview for resume picker"
```

---

### 任务 4：ui/runner.go — EngineRunner 接口扩展

**文件：**
- 修改：`ui/runner.go:17-23`（接口）、`ui/runner.go:25-66`（DefaultEngineRunner）、`ui/runner.go:68-152`（ProgressEngineRunner）
- 测试：创建 `ui/resume_test.go`（与任务 5 共用 mock runner）

- [ ] **步骤 1：编写失败的测试**

创建 `ui/resume_test.go`（定义 mock runner 与接口断言）：

```go
package ui

import (
	"testing"

	"github.com/deepact/deepact/engine"
)

// mockResumeRunner 实现 EngineRunner（含新方法），供 /resume 选择器测试。
type mockResumeRunner struct {
	sessionID string
	history   []engine.Message
	sessions  []SessionSummary
}

func (m *mockResumeRunner) Run(prompt string) tea.Cmd                        { return nil }
func (m *mockResumeRunner) Cancel()                                          {}
func (m *mockResumeRunner) SetProgressChan(ch chan ProgressMsg)               {}
func (m *mockResumeRunner) ValidateConnection() error                        { return nil }
func (m *mockResumeRunner) Steer(msg string)                                 {}
func (m *mockResumeRunner) SetSessionID(id string)                           { m.sessionID = id }
func (m *mockResumeRunner) SetHistory(messages []engine.Message)             { m.history = messages }
func (m *mockResumeRunner) ListSessions() []SessionSummary                   { return m.sessions }
func (m *mockResumeRunner) LoadHistory(id string) []engine.Message           {
	return []engine.Message{{Role: "user", Content: "历史问题"}}
}

// TestEngineRunnerInterface ensures the mock satisfies the full interface.
func TestEngineRunnerInterface(t *testing.T) {
	var _ EngineRunner = (*mockResumeRunner)(nil)
}
```

在 `ui/resume_test.go` 补 import：

```go
import tea "github.com/charmbracelet/bubbletea"
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestEngineRunnerInterface -v`
预期：FAIL（编译错误——接口新增方法 `SetSessionID`/`SetHistory`/`ListSessions`/`LoadHistory`，现有实现不满足；mock 也无法编译）

- [ ] **步骤 3：编写实现代码**

修改 `ui/runner.go`：

1. 扩展接口（第 17-23 行）：

```go
type EngineRunner interface {
	Run(prompt string) tea.Cmd
	Cancel()
	SetProgressChan(ch chan ProgressMsg)
	ValidateConnection() error
	Steer(msg string)
	// ---- 会话恢复（/resume）----
	SetSessionID(id string)
	SetHistory(messages []engine.Message)
	ListSessions() []SessionSummary
	LoadHistory(id string) []engine.Message
}
```

2. 新增 `SessionSummary` 类型（接口之前）：

```go
// SessionSummary 是 /resume 选择器展示的会话摘要（ui 自有类型，
// 避免 ui 直接依赖 session 包）。
type SessionSummary struct {
	ID         string
	UpdatedAt  time.Time
	FirstMsg   string
	EventCount int
}
```

3. `DefaultEngineRunner` 追加空实现：

```go
func (r *DefaultEngineRunner) SetSessionID(id string)          {}
func (r *DefaultEngineRunner) SetHistory(messages []engine.Message) {}
func (r *DefaultEngineRunner) ListSessions() []SessionSummary  { return nil }
func (r *DefaultEngineRunner) LoadHistory(id string) []engine.Message { return nil }
```

4. `ProgressEngineRunner` 追加实现：

```go
func (r *ProgressEngineRunner) SetSessionID(id string) {
	r.getEngine().SetSessionID(id)
}

func (r *ProgressEngineRunner) SetHistory(messages []engine.Message) {
	r.getEngine().SetHistory(messages)
}

// ListSessions 返回当前 store 中的会话列表（经 deps.Session 类型断言访问
// *session.Store，store 在 cmd/run.go 注入）。
func (r *ProgressEngineRunner) ListSessions() []SessionSummary {
	store, ok := r.Deps.Session.(*session.Store)
	if !ok || store == nil {
		return nil
	}
	infos, err := store.List()
	if err != nil {
		return nil
	}
	out := make([]SessionSummary, 0, len(infos))
	for _, info := range infos {
		out = append(out, SessionSummary{
			ID:         info.ID,
			UpdatedAt:  info.UpdatedAt,
			FirstMsg:   info.FirstMsg,
			EventCount: info.EventCount,
		})
	}
	return out
}

// LoadHistory 读取会话事件并重建为可重放历史（裁剪+剥离工具链）。
func (r *ProgressEngineRunner) LoadHistory(id string) []engine.Message {
	events, err := r.Deps.Session.LoadEvents(id)
	if err != nil {
		return nil
	}
	return engine.RebuildHistory(events, engine.DefaultResumeBudget)
}
```

5. 在 `ui/runner.go` 顶部补 import：

```go
import "github.com/deepact/deepact/session"
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestEngineRunnerInterface -v`
预期：PASS

- [ ] **步骤 5：编译验证**

运行：`go build ./...`
预期：无错误

- [ ] **步骤 6：Commit**

```bash
git add ui/runner.go ui/resume_test.go
git commit -m "feat(ui): extend EngineRunner for session resume"
```

---

### 任务 5：ui/model.go — /resume 命令与选择器

**文件：**
- 修改：`ui/model.go:20-27`（AppState）、`ui/model.go:89-92`（slashCommands）、`ui/model.go:118-154`（Model 字段）、`ui/model.go:1395-1430`（submitInput）、`ui/model.go:1000-1005`（handleKey 加 stateResume 分支）、`ui/model.go:755-935`（View 加 renderResumePopup）
- 测试：追加到 `ui/resume_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `ui/resume_test.go` 追加：

```go
// TestResumeCommandEnterPicker verifies /resume enters the picker state.
// 直接调用 submitInput()（不经过 handleKey 的 Enter 路径），避免 macOS
// Option/Shift 物理键检测在测试环境下的不确定性。
func TestResumeCommandEnterPicker(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: []SessionSummary{
		{ID: "sess-1", FirstMsg: "之前的问题", EventCount: 3},
	}}
	m.state = stateReady
	m.height = 40
	m.width = 80

	// 输入 /resume 并提交
	m.inputBuf.SetValue("/resume")
	result, _ := m.submitInput()
	m2 := result.(Model)
	if m2.state != stateResume {
		t.Fatalf("state = %v, want stateResume", m2.state)
	}
	if len(m2.resumeSessions) != 1 || m2.resumeSessions[0].ID != "sess-1" {
		t.Fatalf("resumeSessions = %+v, want sess-1", m2.resumeSessions)
	}
}

// TestResumePickerNavigateAndEnter verifies ↑↓ navigation and Enter applies resume.
func TestResumePickerNavigateAndEnter(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	r := &mockResumeRunner{sessions: []SessionSummary{
		{ID: "sess-1", FirstMsg: "问题1", EventCount: 2},
		{ID: "sess-2", FirstMsg: "问题2", EventCount: 4},
	}}
	m.engine = r
	m.state = stateResume
	m.resumeSessions = r.sessions
	m.height = 40
	m.width = 80

	// Down 到第 2 个
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	m2 := result.(Model)
	if m2.selectedResume != 1 {
		t.Fatalf("selectedResume = %d, want 1", m2.selectedResume)
	}

	// Enter 恢复第 2 个
	result, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m3 := result.(Model)
	if r.sessionID != "sess-2" {
		t.Errorf("runner sessionID = %q, want sess-2", r.sessionID)
	}
	if m3.state != stateReady {
		t.Errorf("state = %v, want stateReady", m3.state)
	}
	// 预填了 system 提示 + 历史消息
	found := false
	for _, msg := range m3.messages {
		if msg.Role == "user" && msg.Content == "历史问题" {
			found = true
		}
	}
	if !found {
		t.Errorf("resumed history not shown in messages: %+v", m3.messages)
	}
}

// TestResumePickerEscCancel verifies Esc exits the picker.
func TestResumePickerEscCancel(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: []SessionSummary{{ID: "sess-1"}}}
	m.state = stateResume
	m.resumeSessions = []SessionSummary{{ID: "sess-1"}}
	m.height = 40
	m.width = 80

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := result.(Model)
	if m2.state != stateReady {
		t.Fatalf("state = %v, want stateReady after Esc", m2.state)
	}
	if m2.resumeSessions != nil {
		t.Errorf("resumeSessions = %+v, want nil after Esc", m2.resumeSessions)
	}
}

// TestResumeNoSessions verifies /resume with empty list shows a notice.
func TestResumeNoSessions(t *testing.T) {
	m := NewModel(nil, engine.PricingConfig{})
	m.engine = &mockResumeRunner{sessions: nil}
	m.state = stateReady
	m.height = 40
	m.width = 80
	m.inputBuf.SetValue("/resume")
	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := result.(Model)
	if m2.state == stateResume {
		t.Fatalf("state = %v, want stay ready with notice", m2.state)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./ui/ -run TestResume -v`
预期：FAIL（编译错误——`stateResume` 未定义、`resumeSessions`/`selectedResume` 不存在、`applyResume` 未定义）

- [ ] **步骤 3：编写实现代码**

修改 `ui/model.go`：

1. AppState 枚举（第 20-27 行）加 `stateResume`：

```go
const (
	stateInit AppState = iota
	stateApiKeyPrompt
	stateReady
	stateRunning
	stateResume
)
```

2. `slashCommands`（第 89-92 行附近）加 `/resume`：

```go
	{Command: "/resume", Args: "", Description: "恢复之前的会话"},
```

3. Model 结构体（`skillSuggestions` 字段附近）加字段：

```go
	// Resume session picker
	resumeSessions []SessionSummary
	selectedResume int
```

4. `submitInput`（`/help` 处理之后、`if m.engine == nil` 检查之后、`m.state = stateRunning` 之前）加 `/resume` 处理：

```go
	// Handle /resume: show session picker without invoking the engine
	if strings.TrimSpace(content) == "/resume" {
		if m.engine == nil {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "API key required. Restart and enter key."})
			return m, nil
		}
		sessions := m.engine.ListSessions()
		if len(sessions) == 0 {
			m.messages = append(m.messages, DisplayMessage{Role: "system", Content: "没有可恢复的会话。"})
			return m, nil
		}
		m.resumeSessions = sessions
		m.selectedResume = 0
		m.state = stateResume
		return m, nil
	}
```

5. `handleKey` 中，在 `Ctrl+Q`（第 1003 行）处理之后、`Esc`（第 1009 行）处理之前，插入 stateResume 按键分支：

```go
	// ---- Resume session picker keys ----
	if m.state == stateResume {
		switch msg.Type {
		case tea.KeyUp:
			m.selectedResume--
			if m.selectedResume < 0 {
				m.selectedResume = len(m.resumeSessions) - 1
			}
			return m, nil
		case tea.KeyDown:
			m.selectedResume = (m.selectedResume + 1) % len(m.resumeSessions)
			return m, nil
		case tea.KeyEnter:
			if !msg.Alt {
				return m.applyResume(m.selectedResume), nil
			}
		case tea.KeyEsc:
			m.state = stateReady
			m.resumeSessions = nil
			m.selectedResume = 0
			return m, nil
		default:
			return m, nil // picker 中忽略其他键
		}
	}
```

6. 新增 `applyResume` 方法（放在 `submitInput` 之后）：

```go
// applyResume 恢复选中的会话：continue 语义 + 预载历史 + 预填 UI 消息流。
func (m *Model) applyResume(idx int) {
	if m.engine == nil || idx < 0 || idx >= len(m.resumeSessions) {
		return
	}
	s := m.resumeSessions[idx]
	m.engine.SetSessionID(s.ID)
	history := m.engine.LoadHistory(s.ID)
	if history == nil {
		history = []engine.Message{}
	}
	m.engine.SetHistory(history)

	m.messages = append(m.messages, DisplayMessage{Role: "system", Content: fmt.Sprintf("已恢复会话 %s", s.ID)})
	for _, h := range history {
		if h.Role == "user" || h.Role == "assistant" {
			m.messages = append(m.messages, DisplayMessage{Role: h.Role, Content: h.Content})
		}
	}
	m.state = stateReady
	m.resumeSessions = nil
	m.selectedResume = 0
}
```

7. `View()`（第 771-772 行）加 `renderResumePopup` 调用，并加入 footerParts（第 903-909 行）：

```go
	resumePopup := renderResumePopup(m, contentWidth)
	// ...
	if resumePopup != "" {
		footerHeight += renderedHeight(resumePopup)
	}
	// ...
	if resumePopup != "" {
		footerParts = append([]string{resumePopup}, footerParts...)
	}
```

8. 新增 `renderResumePopup` 函数（放在 `renderOptionsPopup` 之后）：

```go
// renderResumePopup 渲染会话选择器（/resume）。
func renderResumePopup(m Model, width int) string {
	if m.state != stateResume || len(m.resumeSessions) == 0 {
		return ""
	}
	total := len(m.resumeSessions)
	start, end := visiblePopupWindow(total, m.selectedResume, maxPopupItems)
	var lines []string
	for i := start; i < end; i++ {
		s := m.resumeSessions[i]
		preview := s.FirstMsg
		if len([]rune(preview)) > 30 {
			preview = string([]rune(preview)[:30]) + "…"
		}
		line := fmt.Sprintf("%s  %s  %s", s.UpdatedAt.Format("01-02 15:04"), preview, s.ID)
		if i == m.selectedResume {
			line = SuggestionSelected.Render(" " + line + " ")
		} else {
			line = SuggestionItem.Render(line)
		}
		lines = append(lines, line)
	}
	if total > maxPopupItems {
		remain := total - end
		if remain > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" … and %d more (scroll ↑↓)", remain)))
		} else if start > 0 {
			lines = append(lines, DimStyle.Render(fmt.Sprintf(" (↑ scroll for %d more)", start)))
		}
	}
	lines = append(lines, DimStyle.Render("Enter: 恢复  ↑↓: 选择  Esc: 取消"))
	content := strings.Join(lines, "\n")
	return SuggestionBox.Width(width - 2).Render(content)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./ui/ -run TestResume -v`
预期：全部 PASS

- [ ] **步骤 5：运行既有 ui 测试防回归**

运行：`go test ./ui/`
预期：全部 PASS

- [ ] **步骤 6：Commit**

```bash
git add ui/model.go ui/resume_test.go
git commit -m "feat(ui): add /resume session picker"
```

---

### 任务 6：集成验证

**文件：** 无新代码；验证跨包集成。

- [ ] **步骤 1：全量编译**

运行：`go build ./...`
预期：无错误

- [ ] **步骤 2：全量测试**

运行：`go test ./...`
预期：全部 PASS

- [ ] **步骤 3：手动冒烟测试（可选）**

```bash
# 用一个已有会话目录启动，输入 /resume 应列出会话，Enter 恢复后出现
# 「已恢复会话 <id>」与历史消息流。
deepact
```

- [ ] **步骤 4：Commit（如无改动跳过）**

```bash
git status --short
# 若有遗留改动再 commit
```

---

## 自检

- **规格覆盖度：**
  - 持久化 `message` 事件（user 全文/assistant 全文/tool 摘要）→ 任务 1
  - `reasoning_content` 不落盘 → 任务 1 测试断言
  - `message` 事件附带 WorkDir → 任务 1（`Event.WorkDir` + `emitEvent` 填充）
  - 恢复重建 + 裁剪（keepRecentTokens=16384）+ user 边界切割 → 任务 2（`DefaultResumeBudget` + `RebuildHistory`）
  - 剥离工具链（跳过 tool、剥离 ToolCalls）→ 任务 2 测试断言
  - `SetSessionID`（continue 语义）/ `SetHistory` 预载 → 任务 2
  - EngineRunner 接口扩展 + SessionSummary → 任务 4
  - `/resume` 命令 + stateResume 选择器 + ↑↓/Enter/Esc → 任务 5
  - 恢复后预填 m.messages（system 提示 + user/assistant 流）→ 任务 5 `applyResume`
  - 会话列表按 WorkDir 分组 / FirstMsg 预览 → 任务 3
  - 错误处理（无会话提示、损坏跳过、nil 防御）→ 任务 1/2/4/5
  - 测试计划 4 个文件 → 任务 1/2/3/5
- **占位符扫描：** 无 TODO/待定；所有步骤含完整代码与命令。
- **类型一致性：** `SessionSummary{ID,UpdatedAt,FirstMsg,EventCount}`、`RebuildHistory(events, budget)`、`persistHistory()`、`persistedCount`、`EventTypeMessage`、`Event.WorkDir` 在各任务间一致；`mockResumeRunner` 与 `EngineRunner` 接口方法签名一致。
- **已知取舍：** `ListSessions` 用类型断言 `r.Deps.Session.(*session.Store)`（规格允许"注入会话读取器"路径），ui/runner.go import session 包；若中途压缩替换 history，persistHistory 从 0 重写（恢复窗口语义，与规格一致）。
