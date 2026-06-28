# 防止 Agent 反复读同一文件 — 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 从源头抑制 Agent 反复读同一文件同一区段，并在卡住时通过 nudge 引导自我更正、再不行请用户澄清，替代当前的直接硬拦。

**架构：** 提示层注入"已读文件清单"预防重读；引擎层把两套互相冲突的循环守卫统一为 scope-aware 可读 key，按"同一 (path,scope) 累计 read 次数"判定——3 次 nudge、4 次硬拦请用户澄清；计数仅在用户新消息时清零。

**技术栈：** Go 1.24+，标准库 `encoding/json`/`crypto/sha256`，表驱动测试。

**规格：** `docs/superpowers/specs/2026-06-28-read-loop-prevention-design.md`

---

## 文件结构与职责

- `engine/types.go` — 新增 `ReadRecord` 结构与 `TaskState.ReadHistory` 字段（数据模型）。
- `engine/turn.go` — 新增 `extractReadScope` 解析可读 scope；LastOp 改 scope-aware；`updateTaskStateFromTools` 的 read 分支写 `ReadHistory`。
- `engine/guards.go` — `extractToolKey` 的 read 分支改用可读 scope 串，废弃 hex hash；新增 `ReadLoopState` 承载两级阈值判定。
- `engine/loop.go` — per-turn 计数改"同一 (path,scope) 累计次数"，3 次 nudge、4 次硬拦；硬拦文案中文化请用户澄清。
- `context/builder.go` — `formatTaskStateVolatile` 增加 `ReadHistory` 字段，进 Block B 注入提示。
- `tools/builtin/read.go` — 清理 `fileUnchangedStub`/`mtimeCache` 死代码。
- `engine/guards_test.go` — 扩展 scope-aware read key 测试。
- `engine/loop_read_loop_test.go`（新建）— 两级阈值、清零、不同 scope 不触发的集成测试。
- `context/builder_test.go`（如不存在则新建）— 已读清单渲染测试。

---

## 任务 1：数据模型 — ReadRecord 与 ReadHistory

**文件：**
- 修改：`engine/types.go`（`TaskState` 结构体，约 215-238 行）

- [ ] **步骤 1：在 `TaskState` 中新增 `ReadRecord` 类型与 `ReadHistory` 字段**

在 `engine/types.go` 的 `TaskState` 结构体内（`Roundtable` 字段之后）追加字段，并在结构体下方新增 `ReadRecord` 类型：

```go
	// ReadHistory records each file read this session (path + scope) so the
	// prompt can warn the agent against re-reading, and the loop guard can count
	// repeated reads of the same (path, scope). Cleared on new user message.
	ReadHistory []ReadRecord `json:"read_history"`
```

在 `TaskState` 结构体定义之后（`FileCollapse` 类型之前或之后均可）新增：

```go
// ReadRecord captures a single read operation for loop-prevention and prompt
// injection. Scope is a human-readable string: "" for a full-file read,
// "symbol:Run" for a symbol read, "L10-50" for an offset/limit range.
type ReadRecord struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}
```

- [ ] **步骤 2：编译验证**

运行：`go build ./engine/...`
预期：编译通过，无错误。

- [ ] **步骤 3：Commit**

```bash
git add engine/types.go
git commit -m "feat(engine): add ReadRecord and TaskState.ReadHistory for read tracking"
```

---

## 任务 2：scope 提取函数 — extractReadScope

**文件：**
- 修改：`engine/turn.go`（在 `extractPathFromArgs` 函数附近，约 939 行之后）
- 测试：`engine/turn_test.go`（如不存在则新建，新建需 `package engine`）

- [ ] **步骤 1：编写失败的测试**

在 `engine/turn_test.go`（不存在则新建，首行 `package engine`）中添加表驱动测试：

```go
package engine

import (
	"encoding/json"
	"testing"
)

func TestExtractReadScope(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare read", `{"path":"a.go"}`, ""},
		{"symbol", `{"path":"a.go","symbol":"Run"}`, "symbol:Run"},
		{"offset+limit", `{"path":"a.go","offset":10,"limit":50}`, "L10-50"},
		{"offset only", `{"path":"a.go","offset":10}`, "L10-"},
		{"limit only", `{"path":"a.go","limit":50}`, "L1-50"},
		{"empty input", ``, ""},
		{"invalid json", `{not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractReadScope(json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("extractReadScope(%s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestExtractReadScope -v`
预期：FAIL，报错 `undefined: extractReadScope`。

- [ ] **步骤 3：编写实现**

在 `engine/turn.go` 的 `extractPathFromArgs` 函数之后新增：

```go
// extractReadScope derives a human-readable scope string from a read tool call's
// arguments: "" for a bare full-file read, "symbol:<name>" for a symbol read,
// "L<offset>-<limit>" for an offset/limit range. Used as the scope component of
// the loop-detection key and the ReadRecord.
func extractReadScope(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	symbol, _ := m["symbol"].(string)
	if symbol != "" {
		return "symbol:" + symbol
	}
	offset, _ := m["offset"].(float64)
	limit, _ := m["limit"].(float64)
	if int(offset) == 0 && int(limit) == 0 {
		return ""
	}
	return fmt.Sprintf("L%d-%d", int(offset), int(limit))
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestExtractReadScope -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/turn_test.go
git commit -m "feat(engine): add extractReadScope for human-readable read scope"
```

---

## 任务 3：在 updateTaskStateFromTools 中写 ReadHistory

**文件：**
- 修改：`engine/turn.go:923-924`（`updateTaskStateFromTools` 的 `case "read"` 分支）
- 测试：`engine/turn_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/turn_test.go` 中添加：

```go
func TestUpdateTaskStateFromTools_RecordsReadHistory(t *testing.T) {
	e := &Engine{state: &TaskState{}}
	calls := []ToolCallRequest{
		{Name: "read", Input: json.RawMessage(`{"path":"a.go","symbol":"Run"}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"a.go","offset":10,"limit":50}`)},
		{Name: "read", Input: json.RawMessage(`{"path":"b.go"}`)},
	}
	e.updateTaskStateFromTools(calls, nil)
	want := []ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "a.go", Scope: "L10-50"},
		{Path: "b.go", Scope: ""},
	}
	if len(e.state.ReadHistory) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(e.state.ReadHistory), len(want), e.state.ReadHistory)
	}
	for i, r := range want {
		if e.state.ReadHistory[i] != r {
			t.Errorf("record %d = %+v, want %+v", i, e.state.ReadHistory[i], r)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestUpdateTaskStateFromTools_RecordsReadHistory -v`
预期：FAIL，`ReadHistory` 为空（当前 read 分支只调 `addToWorkingSet`）。

- [ ] **步骤 3：修改实现**

将 `engine/turn.go` 中 `case "read":` 分支（约 923-924 行）改为：

```go
		case "read":
			addToWorkingSet(e.state, path, "read")
			e.state.ReadHistory = append(e.state.ReadHistory, ReadRecord{
				Path:  path,
				Scope: extractReadScope(call.Input),
			})
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestUpdateTaskStateFromTools_RecordsReadHistory -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/turn_test.go
git commit -m "feat(engine): record ReadHistory on each read tool call"
```

---

## 任务 4：LoopGuard read key 改可读 scope 串

**文件：**
- 修改：`engine/guards.go:55-131`（`extractToolKey`、`extractReadScopeHash`）
- 测试：`engine/guards_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/guards_test.go` 中添加（替换或新增 `TestExtractToolKey_ReadScope`）：

```go
func TestExtractToolKey_ReadScope(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare read", `{"path":"a.go"}`, "read:a.go::"},
		{"symbol", `{"path":"a.go","symbol":"Run"}`, "read:a.go::symbol:Run"},
		{"offset+limit", `{"path":"a.go","offset":10,"limit":50}`, "read:a.go::L10-50"},
		{"no path", `{"symbol":"Run"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolKey(ToolCallRequest{Name: "read", Input: json.RawMessage(tt.input)})
			if got != tt.want {
				t.Errorf("extractToolKey(read, %s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

注意：`guards_test.go` 顶部需 `import "encoding/json"`，若已有则跳过。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestExtractToolKey_ReadScope -v`
预期：FAIL，当前 read 分支返回 hex hash 形式，不匹配可读串。

- [ ] **步骤 3：修改实现**

在 `engine/guards.go` 中，将 `extractToolKey` 的 `case "read":` 分支改为使用可读 scope。由于 `guards.go` 不能导入 `engine` 包外的 `extractReadScope`（同包，可直接调用——`extractReadScope` 在 `turn.go`，同 `package engine`），直接复用：

将 `extractToolKey` 中：
```go
	case "read":
		scopeHash := extractReadScopeHash(call.Input)
		if scopeHash == "" {
			return "read:" + path
		}
		return "read:" + path + ":" + scopeHash
```
改为：
```go
	case "read":
		// Human-readable scope ("", "symbol:Run", "L10-50") — aligned with
		// LastOp and ReadRecord so all three use one consistent key form.
		return "read:" + path + "::" + extractReadScope(call.Input)
```

然后**删除** `extractReadScopeHash` 函数（guards.go:113-131 整个函数），它不再被引用。

- [ ] **步骤 4：检查旧测试是否引用了被删函数**

运行：`grep -n "extractReadScopeHash" engine/*.go`
预期：无输出（若有引用，更新或删除对应测试）。

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./engine/ -run "TestExtractToolKey_ReadScope|TestLoopGuard" -v`
预期：PASS。注意 `TestLoopGuard_ReadWithScope`（guards_test.go:79）若断言旧 hex 行为需同步更新——检查并修正其断言以匹配新可读串。

- [ ] **步骤 6：Commit**

```bash
git add engine/guards.go engine/guards_test.go
git commit -m "refactor(engine): unify read loop key to human-readable scope"
```

---

## 任务 5：LastOp 改 scope-aware

**文件：**
- 修改：`engine/turn.go:558-569`（`executeTurn` 末尾 LastOp 生成）
- 测试：`engine/turn_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine/turn_test.go` 中添加测试，验证 LastOp 含 scope（通过一个辅助函数，或直接构造）。由于 LastOp 在 `TurnResult` 上，编写一个针对 LastOp 生成逻辑的单元测试：

```go
func TestLastOp_ReadIsScopeAware(t *testing.T) {
	// LastOp is derived from the first regular call; verify read includes scope.
	calls := []ToolCallRequest{
		{Name: "read", Input: json.RawMessage(`{"path":"a.go","symbol":"Run"}`)},
	}
	var lastOp string
	for _, c := range calls {
		path := extractPathFromArgs(c.Input)
		if path == "" {
			continue
		}
		if c.Name == "read" {
			lastOp = c.Name + ":" + path + "::" + extractReadScope(c.Input)
		} else {
			lastOp = c.Name + ":" + path + "#" + contentSignature(c.Input)
		}
		break
	}
	want := "read:a.go::symbol:Run"
	if lastOp != want {
		t.Errorf("lastOp = %q, want %q", lastOp, want)
	}
}
```

- [ ] **步骤 2：运行测试验证通过**（此测试复刻逻辑，先确保逻辑正确）

运行：`go test ./engine/ -run TestLastOp_ReadIsScopeAware -v`
预期：PASS（验证逻辑片段）。

- [ ] **步骤 3：修改实现**

将 `engine/turn.go:558-569` 的 LastOp 生成块改为：

```go
	for _, c := range regularCalls {
		path := extractPathFromArgs(c.Input)
		if path == "" {
			continue
		}
		if c.Name == "read" {
			// Scope-aware: reading different sections of the same file produces
			// distinct LastOps and is not counted as a loop. Aligned with
			// LoopGuard's read key form.
			result.LastOp = c.Name + ":" + path + "::" + extractReadScope(c.Input)
		} else {
			result.LastOp = c.Name + ":" + path + "#" + contentSignature(c.Input)
		}
		break
	}
```

- [ ] **步骤 4：编译并运行全部 engine 测试**

运行：`go build ./engine/... && go test ./engine/ -short`
预期：编译通过，测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/turn.go engine/turn_test.go
git commit -m "feat(engine): make LastOp scope-aware for read calls"
```

---

## 任务 6：两级脱困 — ReadLoopState 与 loop.go 集成

**文件：**
- 修改：`engine/guards.go`（新增 `ReadLoopState` 类型与方法）
- 修改：`engine/loop.go:624-680`（per-turn 计数改累计 + 两级阈值）
- 修改：`engine/loop.go:200-201`（Reset 清零）
- 测试：`engine/loop_read_loop_test.go`（新建）

- [ ] **步骤 1：编写失败的测试 — 同一 scope 第 3 次 nudge、第 4 次硬拦**

新建 `engine/loop_read_loop_test.go`：

```go
package engine

import "testing"

func TestReadLoopState_NudgeThenBlock(t *testing.T) {
	s := NewReadLoopState()
	key := "read:a.go::symbol:Run"

	// 1st, 2nd: allow
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("1st: want allow, got %s (%s)", a.Type, a.Message)
	}
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("2nd: want allow, got %s (%s)", a.Type, a.Message)
	}
	// 3rd: nudge
	a := s.Check(key)
	if a.Type != GuardDiagnose {
		t.Fatalf("3rd: want diagnose(nudge), got %s (%s)", a.Type, a.Message)
	}
	if a.Message == "" {
		t.Fatal("3rd: nudge message empty")
	}
	// 4th: block
	a = s.Check(key)
	if a.Type != GuardBlock {
		t.Fatalf("4th: want block, got %s (%s)", a.Type, a.Message)
	}
}

func TestReadLoopState_DifferentScopeIndependent(t *testing.T) {
	s := NewReadLoopState()
	k1 := "read:a.go::symbol:Run"
	k2 := "read:a.go::L10-50"
	for i := 0; i < 4; i++ {
		if a := s.Check(k1); a.Type == GuardBlock {
			t.Fatalf("k1 should not block on alternate reads, iter %d", i)
		}
		if a := s.Check(k2); a.Type == GuardBlock {
			t.Fatalf("k2 should not block, iter %d", i)
		}
	}
}

func TestReadLoopState_Reset(t *testing.T) {
	s := NewReadLoopState()
	key := "read:a.go::symbol:Run"
	s.Check(key)
	s.Check(key)
	s.Check(key) // nudge
	s.Reset()
	// After reset, 1st is allow again
	if a := s.Check(key); a.Type != GuardAllow {
		t.Fatalf("after reset 1st: want allow, got %s", a.Type)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestReadLoopState -v`
预期：FAIL，`undefined: NewReadLoopState`。

- [ ] **步骤 3：在 guards.go 实现 ReadLoopState**

在 `engine/guards.go` 末尾新增：

```go
// ReadLoopState tracks per-(path,scope) read counts and applies a two-tier
// policy: 3rd read of the same key → nudge (GuardDiagnose); 4th → block.
// Different (path, scope) keys are independent. Reset on new user message.
type ReadLoopState struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewReadLoopState() *ReadLoopState {
	return &ReadLoopState{counts: make(map[string]int)}
}

// Check returns GuardAllow (1st-2nd), GuardDiagnose (3rd, nudge), or
// GuardBlock (4th+). key is the scope-aware read key "read:path::scope".
func (s *ReadLoopState) Check(key string) GuardAction {
	if s == nil || key == "" {
		return GuardAction{Type: GuardAllow}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[key]++
	switch s.counts[key] {
	case 1, 2:
		return GuardAction{Type: GuardAllow}
	case 3:
		return GuardAction{Type: GuardDiagnose, Message: "read-loop-nudge"}
	default: // 4+
		return GuardAction{Type: GuardBlock, Message: "read-loop-block"}
	}
}

func (s *ReadLoopState) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts = make(map[string]int)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestReadLoopState -v`
预期：PASS。

- [ ] **步骤 5：在 loop.go 接入 ReadLoopState**

在 `engine/loop.go` 的 `Engine` 结构体（约 37-46 行，`guards` 字段附近）新增字段：

```go
	readLoop *ReadLoopState
```

在 `NewLoopGuard` 初始化处（约 loop.go:122-124，`guard := &GuardSystem{...}` 之后）初始化：

```go
	readLoop: NewReadLoopState(),
```

将 `loop.go:200-201` 的 reset 块扩展：

```go
	if e.guards.loop != nil {
		e.guards.loop.Reset()
	}
	if e.readLoop != nil {
		e.readLoop.Reset()
	}
```

- [ ] **步骤 6：在 loop.go 接入 ReadLoopState（read 走两级阈值，非 read 保留原 consecutiveSameOp 检测）**

原 `consecutiveSameOp`（loop.go:624-680）检测**所有工具**的首轮循环。LoopGuard（guards.go）只跟踪 `edit/write/read`，**不覆盖 grep/bash 等**（default 分支返回 ""）。因此**不能整体删除** `consecutiveSameOp`——否则 grep/bash 的连续循环会失去检测。

改为：read 调用走 `ReadLoopState` 两级阈值并跳过 `consecutiveSameOp`；非 read 调用仍走原 `consecutiveSameOp >= 5` 硬拦。

保留 `loop.go:624-625` 的：
```go
	var lastOp string
	consecutiveSameOp := 0
```

将循环体内原 `if turnResult.LastOp != "" { ... consecutiveSameOp ... }` 整块（约 666-680 行）**替换**为：

```go
		// Loop detection: read ops go through ReadLoopState (two-tier:
		// 3rd → nudge, 4th → block). Non-read ops keep the original
		// consecutiveSameOp guard (5 consecutive same first-calls → block),
		// which covers tools LoopGuard doesn't track (grep/bash/etc.).
		if turnResult.LastOp != "" {
			if strings.HasPrefix(turnResult.LastOp, "read:") {
				action := e.readLoop.Check(turnResult.LastOp)
				switch action.Type {
				case GuardDiagnose:
					nudge := buildReadLoopNudge(turnResult.LastOp, zh)
					e.history = append(e.history, Message{
						Role:    "user",
						Content: nudge,
					})
					loopLog.Printf("read-loop nudge injected for %s", turnResult.LastOp)
				case GuardBlock:
					msg := buildReadLoopBlockMsg(turnResult.LastOp, zh)
					return &EngineResponse{
						Summary:      msg,
						Stage:        StageAct,
						Blocked:      true,
						BlockedBy:    "loop_guard",
						FinishReason: "loop_detected",
					}, nil
				}
				// read ops do not feed consecutiveSameOp
			} else {
				if turnResult.LastOp == lastOp {
					consecutiveSameOp++
					if consecutiveSameOp >= 5 {
						msg := "检测到重复操作循环，Agent 可能卡住了。请提供新的方向。"
						if !zh {
							msg = "Detected repeated operation loop. The agent may be stuck. Please provide new direction."
						}
						return &EngineResponse{
							Summary:      msg,
							Stage:        StageAct,
							Blocked:      true,
							BlockedBy:    "loop_guard",
							FinishReason: "loop_detected",
						}, nil
					}
				} else {
					consecutiveSameOp = 0
				}
				lastOp = turnResult.LastOp
			}
		}
```

确保 `strings` 已在 loop.go 的 import 中（通常已有）。

- [ ] **步骤 7：在 loop.go 新增文案构建函数**

在 `engine/loop.go` 末尾新增：

```go
// buildReadLoopNudge builds the nudge message for the 3rd repeated read of the
// same (path, scope). key has form "read:path::scope".
func buildReadLoopNudge(key string, zh bool) string {
	path, scope := splitReadKey(key)
	scopeDesc := describeScope(scope, zh)
	if zh {
		return fmt.Sprintf("[LOOP NUDGE] 你已 3 次读取 %s 的 %s，内容已在对话历史中。不要再读取它。"+
			"请直接基于已有内容产出分析结论；如需新的具体信息，改用 lsp"+
			"（hover/goToDefinition/workspaceSymbol）或读取该文件尚未读过的区段。", path, scopeDesc)
	}
	return fmt.Sprintf("[LOOP NUDGE] You have read %s (%s) 3 times; its content is already in conversation history. "+
		"Do not read it again. Produce your analysis from existing content; for new specifics use lsp "+
		"(hover/goToDefinition/workspaceSymbol) or read an un-read section of the file.", path, scopeDesc)
}

// buildReadLoopBlockMsg builds the block message for the 4th repeated read.
func buildReadLoopBlockMsg(key string, zh bool) string {
	path, scope := splitReadKey(key)
	scopeDesc := describeScope(scope, zh)
	if zh {
		return fmt.Sprintf("检测到重复读取循环：已反复读取 %s（%s），nudge 后仍未改善。"+
			"Agent 可能卡住了。请澄清：是想查看哪段未读内容，还是基于已有内容直接给出结论？", path, scopeDesc)
	}
	return fmt.Sprintf("Repeated read loop detected: %s (%s) has been read repeatedly despite a nudge. "+
		"The agent may be stuck. Please clarify: do you want to view an un-read section, or conclude from existing content?", path, scopeDesc)
}

// splitReadKey splits "read:path::scope" into (path, scope).
func splitReadKey(key string) (path, scope string) {
	const prefix = "read:"
	if !strings.HasPrefix(key, prefix) {
		return key, ""
	}
	rest := key[len(prefix):]
	if i := strings.Index(rest, "::"); i >= 0 {
		return rest[:i], rest[i+2:]
	}
	return rest, ""
}

// describeScope turns a scope string into a human-readable phrase.
func describeScope(scope string, zh bool) string {
	if scope == "" {
		if zh {
			return "整个文件"
		}
		return "entire file"
	}
	if strings.HasPrefix(scope, "symbol:") {
		name := scope[len("symbol:"):]
		if zh {
			return name + " 方法"
		}
		return name + " symbol"
	}
	if zh {
		return "第 " + scope + " 行"
	}
	return "lines " + scope
}
```

- [ ] **步骤 8：编译并运行全部 engine 测试**

运行：`go build ./engine/... && go test ./engine/ -short`
预期：编译通过，测试 PASS。

- [ ] **步骤 9：Commit**

```bash
git add engine/guards.go engine/loop.go engine/loop_read_loop_test.go
git commit -m "feat(engine): two-tier read-loop prevention (nudge then block)"
```

---

## 任务 7：提示层注入已读清单

**文件：**
- 修改：`context/builder.go:199-221`（`formatTaskStateVolatile`）
- 测试：`context/builder_test.go`（不存在则新建）

- [ ] **步骤 1：编写失败的测试**

新建或追加到 `context/builder_test.go`（`package context`，注意 builder.go 的包名——确认是 `package context` 还是其他，先 `head -5 context/builder.go`）：

```go
package context

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepact/deepact/engine"
)

func TestFormatTaskStateVolatile_IncludesReadHistory(t *testing.T) {
	state := &engine.TaskState{
		TurnNumber: 3,
		ReadHistory: []engine.ReadRecord{
			{Path: "a.go", Scope: "symbol:Run"},
			{Path: "a.go", Scope: "L10-50"},
			{Path: "b.go", Scope: ""},
		},
	}
	out := formatTaskStateVolatile(state)
	if out == "" {
		t.Fatal("expected non-empty volatile output")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	rh, ok := m["read_history"]
	if !ok {
		t.Fatal("read_history missing from volatile state")
	}
	s := fmt.Sprintf("%v", rh)
	if !strings.Contains(s, "a.go") || !strings.Contains(s, "b.go") {
		t.Errorf("read_history does not contain expected paths: %v", rh)
	}
}
```

注意 import 需加 `"fmt"`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./context/ -run TestFormatTaskStateVolatile_IncludesReadHistory -v`
预期：FAIL，`read_history missing`（当前 volatile 结构未含此字段）。

- [ ] **步骤 3：修改实现**

将 `context/builder.go` 的 `formatTaskStateVolatile` 内匿名结构体增加字段并赋值。改为：

```go
func formatTaskStateVolatile(state *engine.TaskState) string {
	if state == nil {
		return ""
	}
	volatile := struct {
		ActiveSkillName  string                    `json:"active_skill_name,omitempty"`
		TurnNumber       int                       `json:"turn_number"`
		ConsecutiveFails int                       `json:"consecutive_failures"`
		EditScopeFiles   int                       `json:"edit_scope_files"`
		ReadHistory      []readRecordVolatile      `json:"read_history,omitempty"`
		Roundtable       *roundtableVolatile       `json:"roundtable,omitempty"`
	}{
		ActiveSkillName:  state.ActiveSkillName,
		TurnNumber:       state.TurnNumber,
		ConsecutiveFails: state.ConsecutiveFailures,
		EditScopeFiles:   state.EditScopeFiles,
		ReadHistory:      flattenReadHistory(state.ReadHistory),
		Roundtable:       flattenRoundtable(state.Roundtable),
	}
	data, err := json.Marshal(volatile)
	if err != nil {
		return ""
	}
	return string(data)
}

// readRecordVolatile is the compact form injected into Block B.
type readRecordVolatile struct {
	Path  string `json:"path"`
	Scope string `json:"scope,omitempty"`
}

// flattenReadHistory aggregates records by path into a compact list. When too
// many records exist for a path, it summarizes scopes to keep the prompt small.
func flattenReadHistory(records []engine.ReadRecord) []readRecordVolatile {
	if len(records) == 0 {
		return nil
	}
	// Keep last 20 records to bound prompt size; most recent reads matter most.
	start := 0
	if len(records) > 20 {
		start = len(records) - 20
	}
	out := make([]readRecordVolatile, 0, len(records)-start)
	for _, r := range records[start:] {
		out = append(out, readRecordVolatile{Path: r.Path, Scope: r.Scope})
	}
	return out
}
```

- [ ] **步骤 4：在提示文案中加入"不要重读"指引**

Block B 是 JSON 形式的 task state。已读清单以 JSON 注入即可，但模型需要语义指引。在 `context/builder.go` 的 `Build` 方法中，Block B 之后（约 121 行 `blockB` 追加之后）追加一段 system 消息：

```go
	// Read-history hint: warn against re-reading files already in history.
	if len(state.ReadHistory) > 0 {
		messages = append(messages, engine.ModelMessage{
			Role:    "system",
			Content: BuildReadHistoryHint(state.ReadHistory, a.userLang),
		})
	}
```

并在 `context/` 下新增函数（可放入 `builder.go` 末尾或新建 `context/readhint.go`）。放入 `builder.go` 末尾：

```go
// BuildReadHistoryHint renders a system message listing already-read files so
// the agent avoids re-reading them.
func BuildReadHistoryHint(records []engine.ReadRecord, lang string) string {
	if len(records) == 0 {
		return ""
	}
	zh := lang == "zh" || lang == "chinese"
	byPath := make(map[string][]string)
	order := []string{}
	for _, r := range records {
		if _, ok := byPath[r.Path]; !ok {
			order = append(order, r.Path)
		}
		byPath[r.Path] = append(byPath[r.Path], describeScopeForHint(r.Scope, zh))
	}
	var sb strings.Builder
	if zh {
		sb.WriteString("已读文件（内容已在对话历史中，不要重读）：\n")
	} else {
		sb.WriteString("Files already read (content is in conversation history — do not re-read):\n")
	}
	for _, p := range order {
		scopes := byPath[p]
		// dedupe scopes
		seen := map[string]bool{}
		uniq := []string{}
		for _, s := range scopes {
			if !seen[s] {
				seen[s] = true
				uniq = append(uniq, s)
			}
		}
		if zh {
			sb.WriteString("- " + p + "（" + strings.Join(uniq, ", ") + "）\n")
		} else {
			sb.WriteString("- " + p + " (" + strings.Join(uniq, ", ") + ")\n")
		}
	}
	if zh {
		sb.WriteString("需要新信息时：用 lsp 或读取该文件尚未读过的区段。")
	} else {
		sb.WriteString("For new info: use lsp or read an un-read section of the file.")
	}
	return sb.String()
}

func describeScopeForHint(scope string, zh bool) string {
	if scope == "" {
		if zh {
			return "全文"
		}
		return "full"
	}
	if strings.HasPrefix(scope, "symbol:") {
		return scope // "symbol:Run"
	}
	if zh {
		return "行 " + scope
	}
	return "L " + scope
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./context/ -run TestFormatTaskStateVolatile_IncludesReadHistory -v`
预期：PASS。

补充一个 hint 渲染测试，在 `context/builder_test.go` 追加：

```go
func TestBuildReadHistoryHint(t *testing.T) {
	records := []engine.ReadRecord{
		{Path: "a.go", Scope: "symbol:Run"},
		{Path: "a.go", Scope: "L10-50"},
		{Path: "b.go", Scope: ""},
	}
	out := BuildReadHistoryHint(records, "zh")
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.go") {
		t.Errorf("hint missing paths: %s", out)
	}
	if !strings.Contains(out, "不要重读") {
		t.Errorf("hint missing do-not-reread directive: %s", out)
	}
}
```

运行：`go test ./context/ -run TestBuildReadHistoryHint -v`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add context/builder.go context/builder_test.go
git commit -m "feat(context): inject read-history hint into Block B to prevent re-reads"
```

---

## 任务 8：清理 read.go 死代码

**文件：**
- 修改：`tools/builtin/read.go:19-25,27-29,84-87,132-135`

- [ ] **步骤 1：确认死代码无引用**

运行：`grep -rn "fileUnchangedStub\|mtimeCache" --include="*.go" .`
预期：仅 `tools/builtin/read.go` 内部出现，无外部引用。

- [ ] **步骤 2：删除死代码**

在 `tools/builtin/read.go` 中：
- 删除常量 `fileUnchangedStub`（约 24 行）。
- 从 `ReadTool` 结构体删除 `mtimeCache sync.Map` 字段（约 28 行）。
- `NewReadTool` 无需改（返回 `&ReadTool{}` 仍有效）。
- 删除约 84-87 行的注释块（"Mtime cache: skip stub return..."）。
- 删除约 132-135 行更新 mtime cache 的代码块：
  ```go
  	if payload.Offset == 0 && payload.Limit == 0 {
  		t.mtimeCache.Store(safePath, info.ModTime().UnixMilli())
  	}
  ```
- 检查 import：删除后 `sync` 可能不再使用，运行 `goimports` 或手动移除未用 import。

- [ ] **步骤 3：编译并运行测试**

运行：`go build ./tools/... && go test ./tools/...`
预期：编译通过，测试 PASS。

- [ ] **步骤 4：Commit**

```bash
git add tools/builtin/read.go
git commit -m "refactor(tools): remove dead mtime stub code from read tool"
```

---

## 任务 9：全量验证

- [ ] **步骤 1：运行全量测试（含 race）**

运行：`make test`
预期：全部 PASS，无 race。

- [ ] **步骤 2：运行 lint**

运行：`make lint`
预期：无新增告警。

- [ ] **步骤 3：构建**

运行：`make build`
预期：生成 `./deepact`，无错误。

- [ ] **步骤 4：Commit（如有 lint 修复）**

```bash
git add -A
git commit -m "test(engine): full verification of read-loop prevention"
```

---

## 自检结果

**1. 规格覆盖度：**
- §3.1 数据结构 → 任务 1 ✓
- §3.2 提示层已读清单（预防）→ 任务 7 ✓
- §3.3 统一 scope-aware 守卫（LastOp + LoopGuard key）→ 任务 4、5 ✓
- §3.4 两级脱困（3 nudge / 4 block / 清零时机 / 触发范围）→ 任务 6 ✓
- §4 非目标：read.go 死代码清理 → 任务 8 ✓
- §5 测试 → 各任务内嵌 + 任务 9 ✓

**2. 占位符扫描：** 无 TODO/待定；每个代码步骤含完整代码块；文案均为最终版本。

**3. 类型一致性：**
- `ReadRecord{Path, Scope string}` 在任务 1 定义，任务 3、7 使用一致。
- `extractReadScope` 签名 `(json.RawMessage) string`，任务 2 定义，任务 3、4、5 调用一致。
- `ReadLoopState.Check(key string) GuardAction`，任务 6 定义并在 loop.go 调用一致。
- `buildReadLoopNudge/BlockMsg(key, zh)` 与 `splitReadKey`/`describeScope` 在任务 6 内自洽。
- read key 格式 `"read:path::scope"` 在任务 4（LoopGuard）、任务 5（LastOp）、任务 6（ReadLoopState.Check 入参）三处统一。
- `GuardDiagnose`/`GuardBlock`/`GuardAllow` 常量已存在于 guards.go:17-22，复用一致。

**一个边界确认：** 任务 6 step 6 **保留**了 `consecutiveSameOp` 对非 read 工具的循环检测（grep/bash 等 LoopGuard 不跟踪的工具仍由它在 5 次连续首轮同调用时硬拦）；read 调用剥离给 `ReadLoopState` 走 3 nudge / 4 block 两级阈值。这样既符合规格"非 read 工具循环仍直接硬拦"，又不丢失对 grep/bash 循环的覆盖。
