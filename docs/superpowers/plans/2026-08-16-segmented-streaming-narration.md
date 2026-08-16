# 分段落流式输出（content_delta 按段落切分）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 将 `content_delta` 从逐字节发射改为按段落（空行 `\n\n` 为界）发射，段落完整才输出，消除流式渲染的 markdown 泄漏与 CJK 截断 bug。

**架构：** 切段逻辑放在引擎侧 `engine/turn.go`。流循环中累积 chunk.Delta 到段缓冲，遇空行发射完整段落（含 `\n\n`）；单段超过 `maxSegmentRunes`（60 字）无空行时强制切段；流结束时发掉残余。UI（`ui/model.go`/`ui/runner.go`）零改动——收到完整段落直接渲染。

**技术栈：** Go，标准库 `strings` + `unicode/utf8`。

**约束：** 按用户要求只修改、不 commit。`engine/turn.go` 与 `engine/stop_hook_await_test.go` 存在未提交的既有改动，不动它们。

---

### 任务 1：引擎切段逻辑

**文件：**
- 修改：`engine/turn.go:1-17`（import 区 + `var turnLog` 后新增常量）
- 修改：`engine/turn.go:100-146`（流循环区域，含段缓冲声明、发射逻辑改写、残余 flush）

- [ ] **步骤 1：新增 import 与常量**

将 `engine/turn.go` 的 import 区：

```go
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	dlog "github.com/deepact/deepact/internal/log"
)

var turnLog = dlog.New("[turn] ")
```

替换为（新增 `unicode/utf8`，`var turnLog` 后加常量）：

```go
import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	dlog "github.com/deepact/deepact/internal/log"
)

var turnLog = dlog.New("[turn] ")

// maxSegmentRunes caps a single buffered paragraph before it is force-flushed
// as a content_delta event. Without a blank-line boundary the UI would sit
// idle while the model streams a long unbroken paragraph; splitting into
// ~60-rune chunks keeps progress visible and each chunk stays a complete
// readable CJK/ASCII span (never cutting a multi-byte rune).
const maxSegmentRunes = 60
```

- [ ] **步骤 2：改写流循环段缓冲逻辑（一次连续编辑）**

将 `engine/turn.go` 的流循环区，从：

```go
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []ModelToolCall
	var finish string
	var lastUsage *ModelUsage
	for chunk := range stream {
```

到循环结束 `}`（即 `engine/turn.go:100-143`），连同其后的 `// Reset consecutive failure counter` 注释行之前，整体改写为：

```go
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []ModelToolCall
	var finish string
	var lastUsage *ModelUsage
	var seg string // buffered content_delta text awaiting a paragraph boundary
	flushUpTo := func(n int) {
		if n <= 0 || e.config.OnProgress == nil {
			return
		}
		e.config.OnProgress(ProgressEvent{Type: "content_delta", Detail: seg[:n]})
		seg = seg[n:]
	}
	for chunk := range stream {
		if chunk.Err != nil {
			turnLog.Printf("stream chunk err: %v", chunk.Err)
			e.state.ConsecutiveFailures++
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "stream_error",
				Questions:    []string{fmt.Sprintf("网络连接中断，请检查网络后重试。\n\nConnection interrupted. Please check your network and try again.")},
				FinishReason: "stream_error",
			}, nil
		}
		if chunk.RetryProgress != "" {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "retry", Detail: chunk.RetryProgress})
			}
			continue
		}
		if chunk.Delta != "" {
			contentBuilder.WriteString(chunk.Delta)
			seg += chunk.Delta
			// 段落切分：发射最后一个空行分隔符之前的完整段落（含 \n\n）。
			// 段落完整才输出，UI 每次渲染的都是语义完整的文本，半截
			// markdown（** / `）和 CJK 字符截断问题从根上消失。
			if idx := strings.LastIndex(seg, "\n\n"); idx >= 0 {
				flushUpTo(idx + 2)
			} else if utf8.RuneCountInString(seg) >= maxSegmentRunes {
				// 单段超长无空行：强制切段，避免屏幕长时间无反馈。
				flushUpTo(len(seg))
			}
		}
		if chunk.ReasoningDelta != "" {
			reasoningBuilder.WriteString(chunk.ReasoningDelta)
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "reasoning_delta", Detail: chunk.ReasoningDelta})
			}
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
	}
	flushUpTo(len(seg)) // 流结束：发掉残余段

	// Reset consecutive failure counter — this LLM call succeeded.
	e.state.ConsecutiveFailures = 0
```

注意：步骤 2 是一次连续编辑，`seg`/`flushUpTo` 声明、发射逻辑、残余 flush 都在同一编辑内完成，编辑后即可编译。

- [ ] **步骤 3：编译验证**

运行：`go build ./engine/`
预期：编译通过，无错误。

- [ ] **步骤 4：运行现有 engine 测试**

运行：`go test ./engine/ -run ContentDelta -v`
预期：现有 `content_delta_test.go` 因断言"每 chunk 一事件"而 **FAIL**（预期行为，下一任务改测试）。

---

### 任务 2：改写/新增 content_delta 测试

**文件：**
- 修改：`engine/content_delta_test.go`

- [ ] **步骤 1：改写 TestExecuteTurn_EmitsContentDelta**

将 `TestExecuteTurn_EmitsContentDelta` 的断言改为"无空行短文本合并为一个事件"：

```go
func TestExecuteTurn_EmitsContentDelta(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "让我搜索这个函数"},
			{Delta: "的定义"},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "查找函数"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var contentDeltas []ProgressEvent
	for _, ev := range events {
		if ev.Type == "content_delta" {
			contentDeltas = append(contentDeltas, ev)
		}
	}
	// 无空行短文本：合并为一个事件，Detail 等于拼接全文（不再逐 chunk 发射）。
	if len(contentDeltas) != 1 {
		t.Fatalf("expected 1 content_delta event, got %d: %+v", len(contentDeltas), contentDeltas)
	}
	if contentDeltas[0].Detail != "让我搜索这个函数的定义" {
		t.Errorf("delta Detail = %q, want %q", contentDeltas[0].Detail, "让我搜索这个函数的定义")
	}
}
```

- [ ] **步骤 2：新增段落切分测试**

```go
// TestExecuteTurn_SegmentsOnBlankLine verifies a blank line (\n\n) is a
// paragraph boundary: content before it is emitted as one complete event
// (including the \n\n), and the next paragraph stays buffered until its own
// boundary or stream end.
func TestExecuteTurn_SegmentsOnBlankLine(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "第一段。"},
			{Delta: "\n\n第二段。"},
			{Delta: "\n\n"},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "test"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	want := []string{"第一段。\n\n", "第二段。\n\n"}
	if len(deltas) != len(want) {
		t.Fatalf("expected %d content_delta events, got %d: %+v", len(want), len(deltas), deltas)
	}
	for i := range want {
		if deltas[i] != want[i] {
			t.Errorf("delta[%d] = %q, want %q", i, deltas[i], want[i])
		}
	}
}
```

- [ ] **步骤 3：新增跨 chunk 段落测试**

```go
// TestExecuteTurn_ParagraphSpansChunks verifies a paragraph split across two
// chunks is emitted ONCE, only after its \n\n boundary arrives.
func TestExecuteTurn_ParagraphSpansChunks(t *testing.T) {
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "第一"},
			{Delta: "段\n\n"},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "test"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	if len(deltas) != 1 {
		t.Fatalf("expected 1 content_delta event, got %d: %+v", len(deltas), deltas)
	}
	if deltas[0] != "第一段\n\n" {
		t.Errorf("delta[0] = %q, want %q", deltas[0], "第一段\n\n")
	}
}
```

- [ ] **步骤 4：新增阈值兜底测试**

用 6 个 30 字增量 chunk（共 180 字，无空行）验证：每次缓冲累积 ≥60 字即强制切段，事件拼接后等于原文。

```go
// TestExecuteTurn_ForceFlushLongParagraph verifies a long paragraph with no
// blank line is force-flushed as soon as the buffer reaches maxSegmentRunes,
// so the UI never waits on an unbroken paragraph. Rejoining all events must
// equal the original text (no rune lost).
func TestExecuteTurn_ForceFlushLongParagraph(t *testing.T) {
	chunk := strings.Repeat("测", 30) // 30 runes per chunk
	var events []ProgressEvent
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{Delta: chunk},
			{FinishReason: "stop"},
		}},
		context: &stubContextBuilder{},
		tools:   stubToolExecutor{},
		state:   &TaskState{TurnNumber: 0},
		history: []Message{{Role: "user", Content: "test"}},
		config: EngineConfig{
			ModelName: "test-model",
			OnProgress: func(ev ProgressEvent) {
				events = append(events, ev)
			},
		},
		isChinese: true,
	}

	_, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}

	var deltas []string
	for _, ev := range events {
		if ev.Type == "content_delta" {
			deltas = append(deltas, ev.Detail)
		}
	}
	// 30*2=60 触发第一次切段，之后每 60 字触发一次：180 字 → 3 个事件。
	if len(deltas) != 3 {
		t.Fatalf("expected 3 content_delta events for 180 runes, got %d: %+v", len(deltas), deltas)
	}
	joined := strings.Join(deltas, "")
	if joined != strings.Repeat("测", 180) {
		t.Errorf("rejoined text = %q (len %d), want %d runes of 测", joined, len(joined), 180)
	}
}
```

- [ ] **步骤 5：运行新测试**

运行：`go test ./engine/ -run ContentDelta -v`
预期：全部 PASS（含改写后的 Test1、新增的段落切分/跨 chunk/阈值兜底，以及保持不变的空 chunk 测试）。

---

### 任务 3：回归验证（UI 零改动）

**文件：** 无（验证用）

- [ ] **步骤 1：运行 engine 全量测试**

运行：`go test ./engine/`
预期：PASS（确认未破坏其他 turn 测试，如 stop_hook、loop 相关；`turn.go` 的既有未提交改动保持不动）。

- [ ] **步骤 2：运行 UI 全量测试**

运行：`go test ./ui/`
预期：PASS（UI 零改动，渲染路径不变）。

- [ ] **步骤 3：不 commit，确认工作区仅含预期改动**

运行：`git status --short`
预期：`engine/turn.go`、`engine/content_delta_test.go` 为 modified；`engine/stop_hook_await_test.go`、`ui/zexp_ab_test.go` 为既有未提交改动（不动）。
