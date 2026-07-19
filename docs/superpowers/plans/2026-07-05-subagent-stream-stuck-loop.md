# 子 agent 流式空行与卡死循环修复 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 修掉子 agent `stream_delta` 整段重发导致的流式空行累积，以及 `buildRunSummary` 无轮次边界导致卡死时反复回吐旧旁白两个 bug。

**架构：** (1) `Engine` 记录本轮 turn 循环起点 `runStartHistoryLen`，`buildRunSummary` 加 `startIdx` 入参只遍历本轮区间；(2) 子 agent 用 `subAgentStreamer` 守卫 `stream_delta` 仅首次发射。两处均为最小确定性修复，不动模型行为、不加停滞恢复。

**技术栈：** Go 1.24+，table-driven 测试，`make test`/`make build`/`make lint`。

规格：`docs/superpowers/specs/2026-07-05-subagent-stream-stuck-loop-design.md`

---

## 文件结构

- 修改 `engine/loop.go` —— `Engine` 结构体加 `runStartHistoryLen` 字段；`Run()` 在 turn 循环前赋值；`buildRunSummary` 加 `startIdx` 入参、两个遍历循环加上界；两个调用点（`loop.go:660`、`loop.go:753`）传入 `e.runStartHistoryLen`。
- 修改 `engine/loop_summary_test.go` —— 测试结构体加 `startIdx` 字段，既有用例传 `0` 保持原语义，新增 3 个轮次边界 case。
- 修改 `engine/sub_agent.go` —— 新增 `subAgentStreamer` 类型；`runLoop` 实例化并用 `maybeEmit` 替换原 `stream_delta` 直发。
- 创建 `engine/sub_agent_stream_test.go` —— `subAgentStreamer` 的单测。

---

## 任务 1：`buildRunSummary` 轮次边界 + Engine 接入

**文件：**
- 修改：`engine/loop.go`（Engine 结构体 ~L101、`Run()` ~L616、`buildRunSummary` L770-798、调用点 L660 与 L753）
- 测试：`engine/loop_summary_test.go`

- [ ] **步骤 1：编写失败的测试**

修改 `engine/loop_summary_test.go`。在测试结构体加 `startIdx int` 字段，既有 5 个用例显式补 `startIdx: 0`，并把第 69 行调用改为带 `startIdx`。再追加 3 个新 case。完整新结构体与调用如下：

```go
tests := []struct {
    name          string
    history       []Message
    startIdx      int
    toolCallCount int
    zh            bool
    want          string
    wantContains  string // if set, check strings.Contains instead of exact match
    notWant       string // summary must NOT equal this
}{
    {
        name: "last non-empty assistant content wins",
        history: []Message{
            {Role: "assistant", Content: ""},
            {Role: "assistant", Content: "已修改 2 个文件"},
            {Role: "assistant", Content: ""},
        },
        startIdx:      0,
        toolCallCount: 3,
        zh:            true,
        want:          "已修改 2 个文件",
    },
    {
        name: "fall back to reasoning when all content empty",
        history: []Message{
            {Role: "assistant", Content: "", ReasoningContent: "分析: bug 在第 42 行"},
            {Role: "assistant", Content: ""},
        },
        startIdx:      0,
        toolCallCount: 2,
        zh:            true,
        want:          "分析: bug 在第 42 行",
    },
    {
        name: "all empty -> diagnostic, not 完成",
        history: []Message{
            {Role: "assistant", Content: ""},
            {Role: "assistant", Content: "", ReasoningContent: ""},
        },
        startIdx:      0,
        toolCallCount: 5,
        zh:            true,
        notWant:       "完成",
        wantContains:  "5",
    },
    {
        name:          "empty history -> diagnostic, not Done",
        history:       nil,
        startIdx:      0,
        toolCallCount: 0,
        zh:            false,
        notWant:       "Done",
    },
    {
        name: "english diagnostic mentions tool calls",
        history: []Message{
            {Role: "assistant", Content: ""},
        },
        startIdx:      0,
        toolCallCount: 4,
        zh:            false,
        notWant:       "Done",
        wantContains:  "4",
    },
    // --- 新增：轮次边界 ---
    {
        // 本轮只有空 Content 的 assistant（裸工具调用被剥空）+ tool 结果，
        // 旧轮旁白在 startIdx 之前 → 必须返回诊断串，不能回吐旧旁白。
        name: "run boundary: stale narration from prior run not returned",
        history: []Message{
            {Role: "assistant", Content: "关键路径在 sub_agent.go 产生 stream_delta"}, // idx 0：上一轮
            {Role: "user", Content: "继续"},                                              // idx 1
            {Role: "assistant", Content: ""},                                             // idx 2：本轮，裸工具调用剥空
            {Role: "tool", Content: "file contents"},                                    // idx 3
            {Role: "assistant", Content: ""},                                             // idx 4：本轮，剥空
        },
        startIdx:      2,
        toolCallCount: 1,
        zh:            true,
        notWant:       "关键路径在 sub_agent.go 产生 stream_delta",
        wantContains:  "1",
    },
    {
        // 本轮有正文 → 本轮正文胜出，不取上一轮旁白。
        name: "run boundary: this run's content wins over prior run",
        history: []Message{
            {Role: "assistant", Content: "旧旁白"},        // idx 0：上一轮
            {Role: "user", Content: "继续"},               // idx 1
            {Role: "assistant", Content: "本轮新分析结果"}, // idx 2：本轮
        },
        startIdx:      2,
        toolCallCount: 0,
        zh:            true,
        want:          "本轮新分析结果",
    },
    {
        // 本轮无 assistant 消息（仅 tool）→ 诊断串，不回吐旧旁白。
        name: "run boundary: no assistant in this run -> diagnostic",
        history: []Message{
            {Role: "assistant", Content: "旧旁白"},  // idx 0：上一轮
            {Role: "user", Content: "继续"},         // idx 1
            {Role: "tool", Content: "only tool"},    // idx 2：本轮
        },
        startIdx:      2,
        toolCallCount: 1,
        zh:            true,
        notWant:       "旧旁白",
        wantContains:  "1",
    },
}
for _, tt := range tests {
    got := buildRunSummary(tt.history, tt.startIdx, tt.toolCallCount, tt.zh)
    switch {
    case tt.want != "" && got != tt.want:
        t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
    case tt.notWant != "" && got == tt.notWant:
        t.Errorf("%s: got %q, must not equal %q", tt.name, got, tt.notWant)
    case tt.wantContains != "" && !strings.Contains(got, tt.wantContains):
        t.Errorf("%s: got %q, want to contain %q", tt.name, got, tt.wantContains)
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestBuildRunSummary -v`
预期：编译失败，报错 `too many arguments in call to buildRunSummary`（测试用 4 参，函数仍 3 参）。

- [ ] **步骤 3：编写最少实现代码**

在 `engine/loop.go` 做四处改动。

(3a) `Engine` 结构体加字段（在 `runToolCallCount int` 之后，约 L101）：

```go
	runToolCallCount int
	// runStartHistoryLen is the index in e.history where the current Run()'s
	// turn loop began. buildRunSummary only considers assistant messages at
	// or after this index, so a stale narration from a prior run cannot leak
	// into this run's summary when the model emits bare tool calls (empty
	// Content) and never produces a final text body.
	runStartHistoryLen int
```

(3b) `Run()` 在 turn 循环前赋值。找到 `turns := e.state.TurnNumber`（约 L616），在其**上方**插入：

```go
	// Record the history boundary for this Run() so buildRunSummary only
	// surfaces assistant text produced THIS run. Without this, a prior run's
	// narration gets returned every turn when the model only emits tool calls.
	e.runStartHistoryLen = len(e.history)

	turns := e.state.TurnNumber
```

（保留 `turns := e.state.TurnNumber` 原行，仅在它前面加两行注释 + 赋值。）

(3c) `buildRunSummary` 改签名 + 两个循环加上界（L770）：

```go
func buildRunSummary(history []Message, startIdx int, toolCallCount int, zh bool) string {
	summary := ""
	for i := len(history) - 1; i >= startIdx; i-- {
		if history[i].Role == "assistant" && history[i].Content != "" {
			summary = history[i].Content
			break
		}
	}
	if summary == "" {
		for i := len(history) - 1; i >= startIdx; i-- {
			if history[i].Role == "assistant" && history[i].ReasoningContent != "" {
				summary = history[i].ReasoningContent
				break
			}
		}
	}
```

（函数体其余部分 `stripDSMLTokens` / `isSubstantiveSummary` / 诊断串兜底全部不动。两个循环的 `i >= 0` 改为 `i >= startIdx`，签名加 `startIdx int`。）

(3d) 两个调用点传入 `e.runStartHistoryLen`：

- L660（Blocked 分支）：
```go
			Summary:      buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh),
```
- L753（正常结束）：
```go
	summary := buildRunSummary(e.history, e.runStartHistoryLen, e.runToolCallCount, zh)
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestBuildRunSummary -v`
预期：PASS（全部 8 个 case 通过）。

- [ ] **步骤 5：Commit**

```bash
git add engine/loop.go engine/loop_summary_test.go
git commit -m "fix(engine): scope buildRunSummary to current run to stop stale narration

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 2：`subAgentStreamer` —— `stream_delta` 仅首次发射

**文件：**
- 修改：`engine/sub_agent.go`（新增 `subAgentStreamer` 类型，置于 `runLoop` 之前约 L141；`runLoop` 内 ~L201 实例化；~L286-291 用 `maybeEmit` 替换直发）
- 测试：创建 `engine/sub_agent_stream_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/sub_agent_stream_test.go`：

```go
package engine

import (
	"testing"
)

func TestSubAgentStreamer_EmitsOnlyOnce(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "critic", "first content")
	s.maybeEmit(fn, "critic", "second content")
	s.maybeEmit(fn, "critic", "third content")

	if len(calls) != 1 {
		t.Fatalf("expected 1 stream_delta, got %d: %+v", len(calls), calls)
	}
	if calls[0].Type != "stream_delta" {
		t.Errorf("expected type stream_delta, got %q", calls[0].Type)
	}
	if calls[0].Name != "critic" {
		t.Errorf("expected name critic, got %q", calls[0].Name)
	}
	if calls[0].Detail != "first content" {
		t.Errorf("expected first content, got %q", calls[0].Detail)
	}
}

func TestSubAgentStreamer_SkipsEmptyAndNil(t *testing.T) {
	var calls []ProgressEvent
	fn := func(e ProgressEvent) { calls = append(calls, e) }

	s := subAgentStreamer{}
	s.maybeEmit(fn, "critic", "")   // 空内容 → 不发，streamed 仍 false
	s.maybeEmit(nil, "critic", "x") // nil onProgress → 不发、不 panic，streamed 仍 false
	s.maybeEmit(fn, "critic", "real content") // 首次有效 → 发
	s.maybeEmit(fn, "critic", "more")         // 已发过 → 不发

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Detail != "real content" {
		t.Errorf("expected real content, got %q", calls[0].Detail)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestSubAgentStreamer -v`
预期：编译失败，`undefined: subAgentStreamer`。

- [ ] **步骤 3：编写最少实现代码**

(3a) 在 `engine/sub_agent.go` 的 `runLoop` 函数**之前**（约 L141，`func (r *SubAgentRunner) runLoop` 上方）新增类型：

```go
// subAgentStreamer guards sub-agent stream_delta emission. Sub-agents use
// non-streaming Complete, so resp.Message.Content is the full response text.
// Emitting it as a stream_delta on every runLoop iteration causes the UI to
// accumulate duplicate blocks (m.streaming += ...), producing repeated text
// and blank-line gaps. maybeEmit emits only the first non-empty content; the
// final answer is still surfaced by the main engine's Summary at run end.
type subAgentStreamer struct {
	streamed bool
}

// maybeEmit emits content as a stream_delta the first time it is called with
// non-empty content and a non-nil onProgress; subsequent calls are no-ops.
func (s *subAgentStreamer) maybeEmit(onProgress ProgressFunc, agentName, content string) {
	if s.streamed || content == "" || onProgress == nil {
		return
	}
	onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: content})
	s.streamed = true
}
```

(3b) 在 `runLoop` 的局部变量块（约 L201-204，`consecutiveIntermediate := 0` 旁边）加一行：

```go
	consecutiveIntermediate := 0
	lastOpKey := ""
	sameOpCount := 0
	maxSameOp := 5
	streamer := subAgentStreamer{}
```

(3c) 替换 `stream_delta` 直发。把现有的（约 L286-291）：

```go
			} else if resp.Message.Content != "" {
				preview := firstLine(resp.Message.Content, 60)
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, preview)})
				// Stream full content for progressive display
				r.onProgress(ProgressEvent{Type: "stream_delta", Name: agentName, Detail: resp.Message.Content})
			}
```

改为：

```go
			} else if resp.Message.Content != "" {
				preview := firstLine(resp.Message.Content, 60)
				r.onProgress(ProgressEvent{Type: "thinking", Name: agentName, Detail: fmt.Sprintf("%s: %s", agentName, preview)})
				// Stream full content for progressive display — but only once
				// per runLoop. Subsequent text-only rounds re-emit the same
				// full body; without this guard the UI accumulates duplicates.
				streamer.maybeEmit(r.onProgress, agentName, resp.Message.Content)
			}
```

（`thinking` 事件保留不变，只把 `stream_delta` 那一行换成 `streamer.maybeEmit(...)`。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestSubAgentStreamer -v`
预期：PASS（2 个 case）。

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_stream_test.go
git commit -m "fix(engine): emit sub-agent stream_delta only once to stop accumulation

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 3：最终验证

- [ ] **步骤 1：全量测试（含 race）**

运行：`make test`
预期：全绿，含新增的 `TestBuildRunSummary`（8 case）与 `TestSubAgentStreamer`（2 case）。

- [ ] **步骤 2：构建**

运行：`make build`
预期：成功产出 `./deepact`。

- [ ] **步骤 3：lint**

运行：`make lint`
预期：无新增告警。

- [ ] **步骤 4：手工回归（可选但推荐）**

复现原场景：在 DeepAct 中输入 `critic 这个子agent的stream 输出过程中 为什么两行文字之前有大量空行`，确认：
- 子 agent 流式区不再出现重复文本/大量空行；
- agent 即便不收敛，回复显示诊断串（如"本轮未生成回复文本"）而非旧旁白 `关键路径在 sub_agent.go 产生 stream_delta...`。
