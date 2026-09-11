# 移除关键词启发式 + 统一循环守卫 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 删除引擎中依赖关键词枚举的模型行为判定（isIntermediateText / isPlanStatement / detectNegativeFeedback），并把四个循环守卫收敛为统一 `LoopTracker` 计数核心 + 配置实例。

**架构：** ① 删除意图/计划句/负面情绪三处关键词判断，改为 system prompt 纪律（输出纪律 + 用户消息最高优先级纪律），`summarizeHistory` 诚实化（计划句作为部分结果直接返回）。② 四个守卫（LoopGuard/ReadLoopState/ErrorLoopState/ProgressLoopState）收敛为一个 `LoopTracker` 统一计数核心 + 四个配置实例，key 构造收敛。③ MadeProgress 扩展：新 scope 的 grep/glob 也算进展。

**技术栈：** Go（engine/context 包）、嵌入式 markdown prompt（`context/promptset/zh/system.md`）。

**规格：** `docs/superpowers/specs/2026-09-11-remove-heuristics-unify-loop-guards-design.md`

---

## 文件结构

| 文件 | 职责 | 变更 |
|---|---|---|
| `engine/classifier.go` | REMEMBER 标记提取 | 删 `isIntermediateText`（保留 `extractRememberMarkers`） |
| `engine/turn.go` | 主 agent 回合执行 | 删清洗调用、MadeProgress 扩展、LoopTracker 接入 |
| `engine/sub_agent.go` | 子 agent 执行 + 摘要 | 删清洗调用、删 `isPlanStatement`、summarizeHistory 诚实化 |
| `engine/feedback.go` | 负面情绪检测 | **整个删除** |
| `engine/loop.go` | Run 主循环 | 删反馈调用、守卫接入块改用 LoopTracker |
| `engine/guards.go` | 守卫 | 重构为 `LoopTracker`（ScopeGuard/危险命令保留） |
| `context/promptset/zh/system.md` | 主/子 agent 共享 system prompt | 加两条纪律 |
| 测试 | 同步 | `classifier_test.go` `feedback_test.go` `sub_agent_summarize_test.go` `guards_test.go` `error_loop_test.go` `loop_read_loop_test.go` `progress_loop_test.go` `loop_guard_reset_test.go` `roundtable_test.go` `confirm_command_test.go` `ask_user_ends_run_test.go` |

任务按依赖排序：任务 1-4 独立可先行（删除类），任务 5（guards.go）是任务 6/7 的前置，任务 8 全量验证。

---

### 任务 1：删除 isIntermediateText

**文件：**
- 修改：`engine/classifier.go:30-62`
- 修改：`engine/turn.go:226`
- 修改：`engine/sub_agent.go:375`
- 修改：`engine/classifier_test.go:73-101`

- [ ] **步骤 1：删除 `isIntermediateText` 函数**

在 `engine/classifier.go` 中删除第 30-62 行的整个 `isIntermediateText` 函数（含注释）。保留 `extractRememberMarkers`。

```go
// 删除后 classifier.go 仅保留：
// var rememberRe, extractRememberMarkers
```

- [ ] **步骤 2：删除主 agent 清洗调用**

在 `engine/turn.go:226` 删除：

```go
	// Layer 3: When tool calls exist, strip intermediate thinking text from content.
	// The model sometimes outputs intent text ("Let me...", "让我...") alongside
	// DSML tool calls. This text is noise — tool results provide execution context.
	if hasValidToolCalls(toolCalls) && isIntermediateText(content) {
		content = ""
	}
```

（同时更新上面 `// Layer 2b` 块的注释编号，如涉及）

- [ ] **步骤 3：删除子 agent 清洗调用**

在 `engine/sub_agent.go:375` 删除：

```go
		// Strip intermediate thinking text from content when tool calls exist.
		// The model sometimes outputs intent text alongside structured tool calls;
		// this text is noise and should not pollute the sub-agent's history.
		if len(msg.ToolCalls) > 0 && isIntermediateText(msg.Content) {
			msg.Content = ""
		}
```

- [ ] **步骤 4：删除对应测试**

在 `engine/classifier_test.go` 删除 `TestIsIntermediateText`（第 73-101 行）。保留 `TestExtractRememberMarkers` / `TestExtractRememberMarkers_NilOnNoMatch`。

- [ ] **步骤 5：验证编译**

运行：`go build ./engine/...`
预期：PASS（若 `sub_agent.go` 或 `turn.go` 仍有 `isIntermediateText` 引用会报 undefined）

- [ ] **步骤 6：Commit**

```bash
git add engine/classifier.go engine/turn.go engine/sub_agent.go engine/classifier_test.go
git commit -m "refactor: remove isIntermediateText keyword heuristic, rely on prompt discipline"
```

---

### 任务 2：删除 isPlanStatement + summarizeHistory 诚实化

**文件：**
- 修改：`engine/sub_agent.go:636-702`
- 修改：`engine/sub_agent_summarize_test.go:88-113`

- [ ] **步骤 1：改写 summarizeHistory 删除语义跳过**

`engine/sub_agent.go:636-661`。删除 `isPlanStatement(trimmed)` 分支，保留结构性过滤。当前循环体：

```go
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			content := history[i].Content
			trimmed := strings.TrimSpace(content)
			if len(trimmed) < 50 {
				continue
			}
			// Single line ending with ":" → model self-instruction, not output
			if !strings.Contains(trimmed, "\n") && strings.HasSuffix(trimmed, ":") {
				continue
			}
			// Plan statement ("让我...", "Let me...", "现在读取...",
			// "I now understand... Let me examine...") → the agent stating
			// what it will do next, not a conclusion. Skip so a timed-out
			// run falls back to a real finding instead of the agent's
			// "next step" line (the "/collab shows plan statement as
			// conclusion" bug).
			if isPlanStatement(trimmed) {
				continue
			}
			return "(analysis timed out, partial result)\n" + content
		}
	}
```

改为删除 `isPlanStatement` 分支及其注释，其余不动：

```go
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			content := history[i].Content
			trimmed := strings.TrimSpace(content)
			if len(trimmed) < 50 {
				continue
			}
			// Single line ending with ":" → model self-instruction, not output
			if !strings.Contains(trimmed, "\n") && strings.HasSuffix(trimmed, ":") {
				continue
			}
			// 诚实化：计划句（"让我…"）也是模型真实输出，作为部分结果
			// 直接返回（带超时前缀），不再假装找到更实质的结论。
			return "(analysis timed out, partial result)\n" + content
		}
	}
```

- [ ] **步骤 2：删除 `isPlanStatement` 函数**

`engine/sub_agent.go:680-702` 删除整个 `isPlanStatement` 函数（含注释）。

- [ ] **步骤 3：改写测试**

`engine/sub_agent_summarize_test.go:88-113` 的 `TestSummarizeHistory_SkipsIntentStatement` 改为验证"计划句作为部分结果直接返回"：

```go
// TestSummarizeHistory_PlanStatementAsPartialResult 诚实化行为：最后一条
// 实质 assistant 消息是计划句时，直接作为部分结果返回（带超时前缀），
// 不再跳过它假装找到更实质的结论。
func TestSummarizeHistory_PlanStatementAsPartialResult(t *testing.T) {
	r := &SubAgentRunner{}
	history := []ModelMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "调研 codex 与 deepact 的区别"},
		{Role: "assistant", Content: "核心区别已定位：codex 使用原生 worktree 隔离与 hooks 事件机制，deepact 使用 stop hooks 与 guard 系统。这是实质性结论。"},
		{Role: "tool", Content: "read codex/hooks/..."},
		{Role: "assistant", Content: "让我深入了解 codex 的 skills 模型（与 deepact 的 skill 差异）、worktree、execpolicy、hooks、memory、realtime 等核心模块。"},
	}
	got := r.summarizeHistory(history, "调研 codex 与 deepact 的区别")

	if !strings.Contains(got, "让我深入了解") {
		t.Errorf("plan statement should be returned as the partial result, got %q", got)
	}
	if !strings.Contains(got, "analysis timed out") {
		t.Errorf("partial result should carry the timeout prefix, got %q", got)
	}
}
```

- [ ] **步骤 4：运行测试**

运行：`go test ./engine/ -run TestSummarizeHistory -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_summarize_test.go
git commit -m "refactor: summarizeHistory returns plan statement as partial result, drop isPlanStatement heuristic"
```

---

### 任务 3：删除 feedback.go

**文件：**
- 删除：`engine/feedback.go`
- 修改：`engine/loop.go:607-610`
- 删除：`engine/feedback_test.go`

- [ ] **步骤 1：删除调用**

在 `engine/loop.go:607-610` 删除：

```go
	// 用户负面反馈：暂停当前路径，反思并重新规划执行路线。
	// 改写 history 最后一条 user 消息（照 pendingEditPlan 反馈路径模式），
	// 让主 agent 本轮自行反思，不引入新状态。纯关键词检测，零 LLM 调用。
	applyNegativeFeedbackRewrite(e.history, userMsg, zh)
```

- [ ] **步骤 2：删除文件**

删除 `engine/feedback.go` 与 `engine/feedback_test.go`。

- [ ] **步骤 3：验证编译**

运行：`go build ./engine/...`
预期：PASS

- [ ] **步骤 4：Commit**

```bash
git add -A engine/feedback.go engine/feedback_test.go engine/loop.go
git commit -m "refactor: remove negative-feedback keyword detector, rely on user-message-priority discipline"
```

---

### 任务 4：system prompt 新增两条纪律

**文件：**
- 修改：`context/promptset/zh/system.md`

- [ ] **步骤 1：新增输出纪律**

在 `context/promptset/zh/system.md` 的「# 回复格式」一节（约第 58 行起）后追加一段。在「## 始终：」条目附近插入：

```markdown
## 回合输出纪律：
- 调用工具的回合：不要输出计划性文字（如"让我…""接下来…"），直接执行工具调用。工具结果自带上下文。
- 结论只在最后无工具调用的回合给出。
```

- [ ] **步骤 2：新增用户消息优先级纪律**

在「# 核心规则（必须遵守）」开头附近（第 4 行后）插入：

```markdown
## 用户消息优先级：
- 用户的最新消息永远是最高优先级指令，可推翻此前任何计划。
- 若最新消息包含纠正、不满或需求变更，先暂停当前执行，反思此前路径是否符合用户需求，重新规划并向用户说明新计划后再继续。
- 用户原话即真相：不要臆测用户情绪，也不要假设你的路径一定有问题。
```

- [ ] **步骤 3：验证嵌入与编译**

运行：`go build ./... && go test ./context/ -run TestBuild -v`
预期：PASS（promptset embed 重新编译，无单测直接断言具体措辞则自然通过）

- [ ] **步骤 4：Commit**

```bash
git add context/promptset/zh/system.md
git commit -m "feat: add output discipline and user-message-priority rules to system prompt"
```

---

### 任务 5：guards.go 重构为 LoopTracker

**文件：**
- 修改：`engine/guards.go`
- 修改：`engine/guards_test.go`

先写 TDD：新增 `LoopTracker` 与测试，删除旧守卫后替换测试。

- [ ] **步骤 1：编写 LoopTracker 实现**

在 `engine/guards.go` 顶部（`GuardAction` 常量之后）新增：

```go
// LoopTracker is the unified counting core for all loop guards. Four
// configured instances replace LoopGuard / ReadLoopState / ErrorLoopState /
// ProgressLoopState: they differ only in key granularity, thresholds, and
// whether a success resets the streak. A new loop variant = a new instance +
// key construction, never a new struct.
type LoopTracker struct {
	mu             sync.Mutex
	counts         map[string]int
	nudgeAt        int // 0 = no nudge tier
	blockAt        int
	resetOnSuccess bool // error/progress semantics: a success clears the key
}

// NewLoopTracker creates a LoopTracker. blockAt<=0 defaults to 4; a nudgeAt
// >= blockAt is treated as no nudge tier.
func NewLoopTracker(nudgeAt, blockAt int, resetOnSuccess bool) *LoopTracker {
	if blockAt <= 0 {
		blockAt = 4
	}
	if nudgeAt >= blockAt {
		nudgeAt = 0
	}
	return &LoopTracker{
		counts:         make(map[string]int),
		nudgeAt:        nudgeAt,
		blockAt:        blockAt,
		resetOnSuccess: resetOnSuccess,
	}
}

// Check records a key occurrence and returns GuardAllow, GuardDiagnose
// (nudge, when count == nudgeAt), or GuardBlock (when count >= blockAt).
// For progress/error instances, success=true clears the streak for key
// (progress uses key="" as a global single counter).
func (t *LoopTracker) Check(key string, success bool) GuardAction {
	if t == nil {
		return GuardAction{Type: GuardAllow}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if success && t.resetOnSuccess {
		delete(t.counts, key)
		return GuardAction{Type: GuardAllow}
	}
	t.counts[key]++
	switch {
	case t.nudgeAt > 0 && t.counts[key] == t.nudgeAt:
		return GuardAction{Type: GuardDiagnose, Message: "loop-nudge"}
	case t.counts[key] >= t.blockAt:
		return GuardAction{Type: GuardBlock, Message: "loop-block"}
	default:
		return GuardAction{Type: GuardAllow}
	}
}

// Reset clears all tracking state (e.g., on new user message / new Run).
func (t *LoopTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts = make(map[string]int)
}
```

- [ ] **步骤 2：编写 LoopTracker 测试**

在 `engine/guards_test.go` 中新增（替换原 LoopGuard 测试段）：

```go
// --- LoopTracker (unified counting core) ---

func TestLoopTracker_DefaultBlockAt(t *testing.T) {
	tr := NewLoopTracker(0, 0, false)
	tr.Check("k", false)
	tr.Check("k", false)
	tr.Check("k", false)
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("4th repeat: want block, got %s", a.Type)
	}
}

func TestLoopTracker_AllowUntilBlockAt(t *testing.T) {
	tr := NewLoopTracker(0, 3, false)
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatal("1st should allow")
	}
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatal("2nd should allow")
	}
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("3rd should block, got %s", a.Type)
	}
}

func TestLoopTracker_NudgeThenBlock(t *testing.T) {
	tr := NewLoopTracker(3, 4, false)
	tr.Check("k", false)
	tr.Check("k", false)
	if a := tr.Check("k", false); a.Type != GuardDiagnose {
		t.Fatalf("3rd: want diagnose(nudge), got %s", a.Type)
	}
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("4th: want block, got %s", a.Type)
	}
}

func TestLoopTracker_DifferentKeysIndependent(t *testing.T) {
	tr := NewLoopTracker(0, 2, false)
	tr.Check("a", false)
	if a := tr.Check("b", false); a.Type != GuardAllow {
		t.Fatalf("different key should be independent, got %s", a.Type)
	}
	if a := tr.Check("a", false); a.Type != GuardBlock {
		t.Fatalf("same key 2nd: want block, got %s", a.Type)
	}
}

func TestLoopTracker_ResetOnSuccess(t *testing.T) {
	tr := NewLoopTracker(0, 3, true)
	tr.Check("k", false) // error
	tr.Check("k", false) // error
	if a := tr.Check("k", true); a.Type != GuardAllow { // success resets
		t.Fatalf("success should clear streak, got %s", a.Type)
	}
	tr.Check("k", false)
	tr.Check("k", false)
	tr.Check("k", false) // block
	if a := tr.Check("k", false); a.Type != GuardBlock {
		t.Fatalf("after reset + 3 errors: want block, got %s", a.Type)
	}
}

func TestLoopTracker_GlobalCounter(t *testing.T) {
	// progress: key="" single global counter, success resets it.
	tr := NewLoopTracker(4, 6, true)
	for i := 0; i < 3; i++ {
		tr.Check("", false)
	}
	if a := tr.Check("", false); a.Type != GuardDiagnose {
		t.Fatalf("4th: want nudge, got %s", a.Type)
	}
	if a := tr.Check("", true); a.Type != GuardAllow {
		t.Fatalf("progress signal should reset, got %s", a.Type)
	}
	tr.Check("", false)
	if a := tr.Check("", false); a.Type != GuardAllow {
		t.Fatalf("post-reset counting should restart, got %s", a.Type)
	}
}

func TestLoopTracker_Reset(t *testing.T) {
	tr := NewLoopTracker(0, 2, false)
	tr.Check("k", false)
	tr.Reset()
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatalf("after reset should allow, got %s", a.Type)
	}
}

func TestLoopTracker_NilSafe(t *testing.T) {
	var tr *LoopTracker
	if a := tr.Check("k", false); a.Type != GuardAllow {
		t.Fatalf("nil Check: want allow, got %s", a.Type)
	}
	tr.Reset() // must not panic
}
```

- [ ] **步骤 3：运行新测试确认通过**

运行：`go test ./engine/ -run TestLoopTracker -v`
预期：PASS

- [ ] **步骤 4：删除旧守卫 struct**

在 `engine/guards.go` 删除以下类型及其全部方法：
- `LoopGuard`（含 `NewLoopGuard`、`loopEntry`）
- `ReadLoopState`（`NewReadLoopState`、`Check`、`Reset`）
- `ErrorLoopState`（`NewErrorLoopState`、`Check`、`Reset`）
- `ProgressLoopState`（`NewProgressLoopState`、`Check`、`Reset`）

**保留不动（迁移到 `engine/key.go`）：** `normalizePath`、`extractPathField`、`extractToolKey`、`extractEditContentHash`、`extractWriteContentHash`、`readMultiTargetScope`、`extractReadScope`、`contentSignature`——这些是 key 构造工具，不是守卫 struct，任务 6/7 继续使用。将它们从 `guards.go` 剪切到新建 `engine/key.go`（原实现原样保留，仅去掉注释中的 "LoopGuard" 字样）。

**保留不动（留在 guards.go）：** `GuardAction` 常量、`GuardSystem`、`ScopeGuard` 及危险命令相关、`GuardAllow/Block/Diagnose/AskUser` 的使用。

> 注意：`engine/key.go` 是任务 5 的产出，任务 7 步骤 2 不再创建（删除任务 7 步骤 2 中"若任务 5 已删除则还原"的兜底）。任务 6/7 直接 import 使用 `engine/key.go` 中的函数。

- [ ] **步骤 5：更新 GuardSystem 字段**

`GuardSystem.loop` 类型从 `*LoopGuard` 改为 `*LoopTracker`：

```go
type GuardSystem struct {
	scope *ScopeGuard
	loop  *LoopTracker
}
```

- [ ] **步骤 6：重写 guards_test.go 相关测试**

删除 `engine/guards_test.go` 中直接测试 LoopGuard struct 的测试（`TestNewLoopGuard`、`TestNewLoopGuard_CustomMax`、`TestLoopGuard_Check_AllowsFirstCall`、`TestLoopGuard_Check_BlocksAfterMaxRepeats`、`TestLoopGuard_Check_DifferentContentHash`、`TestLoopGuard_Check_NonDestructiveTool`、`TestLoopGuard_Check_ReadWithScope`、`TestLoopGuard_Reset`、`TestLoopGuard_Reset_Nil`、`TestLoopGuard_Check_Nil`）。

**保留** `TestExtractPathField`、`TestExtractEditContentHash`、`TestExtractWriteContentHash`、`TestExtractToolKey`（它们测试的是已迁至 `engine/key.go` 的 key 构造辅助，不依赖守卫 struct，随文件移动一起保留）。保留 ScopeGuard/危险命令/GuardAction 测试。

- [ ] **步骤 7：Commit**

```bash
git add engine/guards.go engine/guards_test.go
git commit -m "refactor: unify four loop guards into LoopTracker counting core"
```

---

### 任务 6：loop.go 接入 LoopTracker

**文件：**
- 修改：`engine/loop.go`
- 修改：`engine/loop_guard_reset_test.go`
- 修改：`engine/roundtable_test.go`
- 修改：`engine/confirm_command_test.go`
- 修改：`engine/ask_user_ends_run_test.go`
- 修改：`engine/error_loop_test.go`
- 修改：`engine/loop_read_loop_test.go`
- 修改：`engine/progress_loop_test.go`（MadeProgress 相关在任务 7）

- [ ] **步骤 1：替换 Engine 字段类型**

`engine/loop.go:60-67`，将三个字段类型改为 `*LoopTracker`：

```go
	guards       *GuardSystem
	readLoop     *LoopTracker
	errorLoop    *LoopTracker
	progressLoop *LoopTracker
```

同时更新 `readProgressKeys` 字段注释（重命名留到任务 7，此处仅改类型不动名）。

- [ ] **步骤 2：替换 NewEngine 构造**

`engine/loop.go:162-184`：

```go
func NewEngine(cfg EngineConfig, deps EngineDeps) *Engine {
	guard := &GuardSystem{
		scope: NewScopeGuard(cfg.AutoConfirmScope),
		loop:  NewLoopTracker(0, 6, false), // block after 6 repeats of same (tool, path)
	}
	e := &Engine{
		...
		guards:       guard,
		readLoop:     NewLoopTracker(3, 4, false),   // 3rd nudge / 4th block
		errorLoop:    NewLoopTracker(0, 3, true),    // 3 errors → block, success resets
		progressLoop: NewLoopTracker(4, 6, true),    // 4th nudge / 6th block, progress resets
	}
	...
}
```

- [ ] **步骤 3：适配 Reset 调用**

`engine/loop.go:287-291`，`e.readLoop.Reset()` / `e.progressLoop.Reset()` 保持不变（`LoopTracker.Reset` 签名一致）。若存在 `e.guards.loop.Reset()` 也保持不变。

- [ ] **步骤 4：重构 Run 守卫接入块**

`engine/loop.go:787-868`。三处 Check 调用签名适配：

```go
		if turnResult.LastOp != "" {
			if strings.HasPrefix(turnResult.LastOp, "read:") {
				action := e.readLoop.Check(turnResult.LastOp, false)
				// ... 其余不变
			} else {
				if e.errorLoop != nil {
					action := e.errorLoop.Check(coarseOp(turnResult.LastOp), turnResult.LastOpError)
					// ... 不变
				}
				// consecutiveSameOp 保留（现状，不在本次范围）
				...
			}
		}
```

- [ ] **步骤 5：progress 调用适配**

`engine/loop.go:850-851`：

```go
		if e.progressLoop != nil {
			action := e.progressLoop.Check("", turnResult.MadeProgress)
			// ... 其余不变
		}
```

- [ ] **步骤 6：适配各测试构造与调用**

- `engine/loop_guard_reset_test.go:24-25,30,33,41,57-58,63,65,72`：`NewLoopGuard("",6)` → `NewLoopTracker(0,6,false)`；`e.readLoop.Check(key)` → `e.readLoop.Check(key, false)`
- `engine/roundtable_test.go:293-295`：`NewLoopGuard("",6)` → `NewLoopTracker(0,6,false)`；`NewReadLoopState()` → `NewLoopTracker(3,4,false)`；`NewErrorLoopState(0)` → `NewLoopTracker(0,3,true)`
- `engine/confirm_command_test.go:96-97,153`：同上替换
- `engine/ask_user_ends_run_test.go:42-43,96-97`：同上替换
- `engine/error_loop_test.go`：`NewErrorLoopState(3)` → `NewLoopTracker(0,3,true)`；`s.Check(...)` → `s.Check("op:path", false)`（error 测试用具体 key；success 断言用 `s.Check("op:path", true)`）
- `engine/loop_read_loop_test.go`：`NewReadLoopState()` → `NewLoopTracker(3,4,false)`；`Check(key)` → `Check(key, false)`

> 各测试的具体 key 字符串参照现有断言。逐文件运行确认。

- [ ] **步骤 7：编译并跑守卫相关测试**

运行：`go build ./engine/... && go test ./engine/ -run 'TestLoop|TestError|TestProgress|TestRun' -v`
预期：PASS

- [ ] **步骤 8：Commit**

```bash
git add engine/loop.go engine/loop_guard_reset_test.go engine/roundtable_test.go engine/confirm_command_test.go engine/ask_user_ends_run_test.go engine/error_loop_test.go engine/loop_read_loop_test.go
git commit -m "refactor: wire LoopTracker into Run loop and tests"
```

---

### 任务 7：turn.go MadeProgress 扩展 + key 收敛

**文件：**
- 修改：`engine/turn.go`
- 修改：`engine/progress_loop_test.go`
- 修改：`engine/guards_normalize_test.go`

- [ ] **步骤 1：新增 grep/glob 进展 case**

`engine/turn.go:617-649`。将 `readProgressKeys` 重命名为 `progressKeys`（字段定义在 `loop.go:63-67`，同步改名并更新注释），并在 switch 中新增 grep/glob：

```go
	if e.progressKeys == nil {
		e.progressKeys = make(map[string]bool)
	}
	for _, c := range regularCalls {
		switch c.Name {
		case "edit", "write", "revert", "bash":
			if statusByID[c.ID] == "ok" {
				result.MadeProgress = true
			}
		case "read":
			if path := extractPathFromArgs(c.Input, e.config.WorkDir); path != "" {
				key := "read:" + path + "::" + extractReadScope(c.Input)
				if !e.progressKeys[key] {
					e.progressKeys[key] = true
					result.MadeProgress = true
				}
			}
		case "read_multi":
			for _, tgt := range parseReadMultiTargets(c.Input) {
				if tgt.Path == "" {
					continue
				}
				key := "read:" + normalizePath(tgt.Path, e.config.WorkDir) + "::" + readMultiTargetScope(tgt)
				if !e.progressKeys[key] {
					e.progressKeys[key] = true
					result.MadeProgress = true
				}
			}
		case "grep", "glob":
			// 新信息获取（新 scope 的搜索）＝推进理解＝进展。
			key := searchKey(c, e.config.WorkDir)
			if key != "" && !e.progressKeys[key] {
				e.progressKeys[key] = true
				result.MadeProgress = true
			}
		}
	}
```

- [ ] **步骤 2：新增 searchKey 辅助**

`engine/turn.go` 新增（key 构造基础函数已由任务 5 迁至 `engine/key.go`）：

```go
// searchKey builds a per-search progress key: "grep:<pattern>:<path>",
// "glob:<pattern>:<path>". pattern is the primary dimension; path is
// appended so searching different files is distinct. Empty key when neither
// pattern nor path is present.
func searchKey(c ToolCallRequest, workDir string) string {
	var m map[string]interface{}
	if len(c.Input) == 0 || json.Unmarshal(c.Input, &m) != nil {
		return ""
	}
	pattern, _ := m["pattern"].(string)
	path := extractPathFromArgs(c.Input, workDir)
	if pattern == "" && path == "" {
		return ""
	}
	return c.Name + ":" + pattern + ":" + path
}
```

> 本任务 progress 用 `searchKey`，LoopTracker 精确键用 `extractToolKey`（已由任务 5 迁至 `engine/key.go`），read 用 `extractReadScope`（同上）。`engine/key.go` 是任务 5 的产出，本任务不再创建。

- [ ] **步骤 3：更新 turn.go 精确键接入**

`engine/turn.go:387-431` 的 loop 检查块改用 `extractToolKey` + `LoopTracker.Check`：

```go
		if e.guards.loop != nil {
			var loopAction GuardAction
			if call.Name == "read_multi" {
				loopAction = GuardAction{Type: GuardAllow}
				for _, tgt := range parseReadMultiTargets(call.Input) {
					key := "read:" + normalizePath(tgt.Path, e.config.WorkDir) + "::" + readMultiTargetScope(tgt)
					a := e.guards.loop.Check(key, false)
					if a.Type != GuardAllow {
						loopAction = GuardAction{
							Type:    a.Type,
							Message: fmt.Sprintf("read_multi target %s: %s", tgt.Path, a.Message),
						}
						break
					}
				}
			} else {
				key := extractToolKey(call, e.config.WorkDir)
				if key == "" {
					loopAction = GuardAction{Type: GuardAllow}
				} else {
					loopAction = e.guards.loop.Check(key, false)
				}
			}
			if loopAction.Type != GuardAllow {
				// ... 其余不变
			}
		}
```

- [ ] **步骤 4：新增 MadeProgress 测试**

`engine/progress_loop_test.go` 新增：

```go
func TestExecuteTurn_MadeProgress_NovelGrep(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "搜索",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "grep", Arguments: `{"pattern":"LoopTracker"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "搜"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.MadeProgress {
		t.Error("expected MadeProgress=true for a novel grep pattern")
	}
}

func TestExecuteTurn_MadeProgress_RepeatedGrep(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{{
			Delta: "重复搜索",
			ToolCalls: []ModelToolCall{{ID: "c1", Type: "function", Function: ModelFunctionCall{
				Name: "grep", Arguments: `{"pattern":"LoopTracker"}`,
			}}},
			FinishReason: "tool_calls",
		}}},
		context: &stubContextBuilder{},
		tools:   &recordingToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "搜"}},
		config:  EngineConfig{ModelName: "test-model"},
		guards:  &GuardSystem{loop: NewLoopTracker(0, 6, false), scope: NewScopeGuard(false)},
		// 该 pattern 本轮已搜过：key 形式 "grep:<pattern>:"
		progressKeys: map[string]bool{"grep:LoopTracker:": true},
	}
	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.MadeProgress {
		t.Error("expected MadeProgress=false for a repeated grep of the same pattern")
	}
}
```

并将现有 `TestExecuteTurn_MadeProgress_*` 中 `readProgressKeys` 字段引用改为 `progressKeys`。

- [ ] **步骤 5：更新 guards_normalize_test.go**

`engine/guards_normalize_test.go:42`：`NewLoopGuard(workDir, 4)` → `NewLoopTracker(0, 4, false)`，调用 `g.Check(call)` → `g.Check(extractToolKey(call, workDir), false)`（该文件若测试的是 normalizePath/extractToolKey，保留断言并适配签名）。

- [ ] **步骤 6：编译并跑测试**

运行：`go build ./... && go test ./engine/... ./context/... -v`
预期：PASS

- [ ] **步骤 7：Commit**

```bash
git add engine/turn.go engine/loop.go engine/progress_loop_test.go engine/guards_normalize_test.go engine/key.go
git commit -m "feat: count novel grep/glob as progress; unify loop keys into extractToolKey"
```

---

### 任务 8：全量验证

- [ ] **步骤 1：构建 + 全量测试（race）**

运行：`go build ./...`
预期：PASS

运行：`go test ./engine/... ./context/... -race`
预期：PASS

- [ ] **步骤 2：gofmt 检查**

运行：`gofmt -l engine context`
预期：无输出（若有，`gofmt -w` 修正）

- [ ] **步骤 3：检查孤儿符号**

运行：`grep -rn "isIntermediateText\|isPlanStatement\|detectNegativeFeedback\|applyNegativeFeedbackRewrite\|NewLoopGuard\|NewReadLoopState\|NewErrorLoopState\|NewProgressLoopState\|readProgressKeys" engine context`
预期：无输出（或仅文档/注释残留需清理）

- [ ] **步骤 4：Commit（如步骤 3 有清理）**

```bash
git add -A
git commit -m "chore: cleanup orphaned heuristic references after refactor"
```

---

## 规格覆盖度自检

- ✅ 变更 1（删除 isIntermediateText/isPlanStatement + summarizeHistory 诚实化）→ 任务 1、任务 2
- ✅ 变更 2（删除 feedback.go + 两条 prompt 纪律）→ 任务 3、任务 4
- ✅ 变更 3（LoopTracker 统一核心 + 四实例 + key 收敛）→ 任务 5、任务 6、任务 7（步骤 2/3）
- ✅ 变更 4（grep/glob 算进展）→ 任务 7（步骤 1/4）
- ✅ 危险命令黑名单 / REMEMBER / 语言检测 保留不动 → 任务 5 明确"保留不动"

## 明确不做

- 不收敛 `consecutiveSameOp`（loop.go:829-841，第五个残留机制，不在已批准范围，保留现状）
- 不改动危险命令黑名单、ScopeGuard、语言检测
- 不强行统一四个 LoopTracker 实例的阈值数字
