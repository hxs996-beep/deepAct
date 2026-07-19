# Stop Hook LLM 结论判定 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 `StalledNarrationHook` 的关键词前缀判定（`looksLikeNextStepNarration`）换成 LLM 结论判定（`ConclusionClassifier`），取消 `nextStepPrefixes`/`conclusionMarkers` 关键词机制，根治纯文本回合提前结束、用户只能输入"继续"的问题。

**架构：** 新增 `ConclusionClassifier`（复用 `compressor` 的 `Complete`+`JsonMode`+flash 模型范式），判定"用户目标+助手文本"是否最终结论。`StalledNarrationHook` 持有 `ConclusionJudge` 接口（便于 stub 测试），在 `runToolCallCount>0` 时调 `IsConclusion`：是结论放行、不是则 nudge、调用失败保守 nudge、重试上限 `Exhausted`。`ZeroToolCallHook` 不变。

**技术栈：** Go 1.24+，DeepAct engine 包，DeepSeek `Complete` API，table-driven 测试。

**规格：** `docs/superpowers/specs/2026-07-11-stop-hook-llm-conclusion-judgment-design.md`

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/classifier.go` | 新增 `ConclusionJudge` 接口 + `ConclusionClassifier` 实现 + 双语 system prompt；删 `looksLikeNextStepNarration`/`nextStepPrefixes`/`conclusionMarkers`；保留 `isIntermediateText`、`extractRememberMarkers` | 修改 |
| `engine/stop_hook.go` | `StopHook.Check` 加 `ctx context.Context`；`StopHookContext` 加 `Goal string`；`runStopHooks` 加 ctx；`StalledNarrationHook` 持有 `Classifier ConclusionJudge` 改调 `IsConclusion`；`ZeroToolCallHook` 签名适配 | 修改 |
| `engine/turn.go` | `runStopHooks` 调用传 ctx；`StopHookContext` 填 `Goal: e.state.Goal` | 修改 |
| `engine/loop.go` | 新增 `NewConclusionClassifier()` 方法 | 修改 |
| `cmd/exec.go` | 构造 classifier 注入 `StalledNarrationHook` | 修改 |
| `engine/conclusion_classifier_test.go` | `ConclusionClassifier` 单元测试（stub `ModelClient`） | 创建 |
| `engine/stop_hook_test.go` | 适配 `Check`/`runStopHooks` 新签名（加 ctx） | 修改 |
| `engine/stalled_narration_test.go` | 改 stub classifier、删关键词用例、适配签名 | 修改 |
| `engine/turn_test.go` | 适配 executeTurn 测试中 `StalledNarrationHook` 注入 stub classifier（若注册了它） | 修改 |

**类型一致性约定（全计划统一）：**
- `ConclusionJudge` 接口：`IsConclusion(ctx context.Context, goal, text string) (bool, error)`
- `NewConclusionClassifier(model ModelClient, flashModelName string, isChinese bool) *ConclusionClassifier`
- `StopHook.Check(ctx context.Context, sc StopHookContext) StopHookResult`
- `(*Engine).runStopHooks(ctx context.Context, sc StopHookContext) StopHookResult`
- `StopHookContext.Goal string`
- `StalledNarrationHook.Classifier ConclusionJudge`

---

## 任务 1：新增 ConclusionClassifier + ConclusionJudge 接口

**文件：**
- 修改：`engine/classifier.go`（追加新类型，不动现有内容）
- 创建：`engine/conclusion_classifier_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `engine/conclusion_classifier_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

// stubCompleteModel 是一个可控的 ModelClient stub：Complete 返回预设内容或错误，
// 并捕获最后一次请求供断言。Stream 不用于本测试。
type stubCompleteModel struct {
	resp string
	err  error
	last ModelRequest
}

func (m *stubCompleteModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubCompleteModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	return &ModelResponse{Message: ModelMessage{Content: m.resp}}, nil
}

func TestConclusionClassifier_IsConclusion_True(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), "修复 turn.go 的 bug", "任务已完成，测试全部通过。")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Errorf("expected conclusion=true, got false")
	}
}

func TestConclusionClassifier_IsConclusion_False(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": false}`}
	c := NewConclusionClassifier(m, "flash-model", true)
	ok, err := c.IsConclusion(context.Background(), "修复 turn.go 的 bug", "上述修改已写入 turn.go。下面运行测试验证。")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Errorf("expected conclusion=false for mid-task narration, got true")
	}
}

func TestConclusionClassifier_BadJSON_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `not json`}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.IsConclusion(context.Background(), "goal", "text")
	if err == nil {
		t.Fatalf("expected error for non-JSON response, got nil")
	}
}

func TestConclusionClassifier_CallError_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewConclusionClassifier(m, "flash-model", true)
	_, err := c.IsConclusion(context.Background(), "goal", "text")
	if err == nil {
		t.Fatalf("expected error from Complete, got nil")
	}
}

func TestConclusionClassifier_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"conclusion": true}`}
	c := NewConclusionClassifier(m, "flash-model", false)
	_, _ = c.IsConclusion(context.Background(), "fix the bug", "Done, tests pass.")
	req := m.last
	if req.Model != "flash-model" {
		t.Errorf("expected Model=flash-model, got %q", req.Model)
	}
	if !req.JsonMode {
		t.Errorf("expected JsonMode=true")
	}
	if req.Temperature != 0 {
		t.Errorf("expected Temperature=0, got %v", req.Temperature)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("expected system+user messages, got %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, "fix the bug") || !strings.Contains(req.Messages[1].Content, "Done, tests pass.") {
		t.Errorf("expected user message to contain goal and text, got %q", req.Messages[1].Content)
	}
}

var errBoom = errors.New("boom")
```

注意：`var errBoom` 需 `import "errors"`，补到 import 块。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestConclusionClassifier -v`
预期：FAIL，编译错误 `undefined: NewConclusionClassifier` / `undefined: ConclusionClassifier`。

- [ ] **步骤 3：实现 ConclusionClassifier**

在 `engine/classifier.go` 顶部 import 块加入 `"context"`、`"encoding/json"`、`"fmt"`、`"time"`（若已有则跳过），并在文件末尾追加：

```go
// ConclusionJudge 用轻量 LLM 调用判定助手文本是否为对用户目标的最终结论。
// 接口化以便测试注入 stub；*ConclusionClassifier 是其生产实现。
type ConclusionJudge interface {
	IsConclusion(ctx context.Context, goal, text string) (bool, error)
}

// ConclusionClassifier 复用 compressor 的 Complete + JsonMode 范式，跑 flash 模型控制成本。
type ConclusionClassifier struct {
	model          ModelClient
	flashModelName string
	isChinese      bool
}

func NewConclusionClassifier(model ModelClient, flashModelName string, isChinese bool) *ConclusionClassifier {
	return &ConclusionClassifier{model: model, flashModelName: flashModelName, isChinese: isChinese}
}

// IsConclusion 返回 true 表示 text 是对 goal 的最终结论/完成总结；
// false 表示中间过程/下一步计划/部分结果/待办陈述；
// err 表示 LLM 调用或 JSON 解析失败（调用方按保守策略处理）。
func (c *ConclusionClassifier) IsConclusion(ctx context.Context, goal, text string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var prompt string
	if c.isChinese {
		prompt = fmt.Sprintf("目标：%s\n\n助手回复：%s", goal, text)
	} else {
		prompt = fmt.Sprintf("Goal: %s\n\nAssistant reply: %s", goal, text)
	}
	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickClassifierPrompt(c.isChinese)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		MaxTokens:   64,
		JsonMode:    true,
	}
	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return false, fmt.Errorf("conclusion classify: %w", err)
	}
	var out struct {
		Conclusion bool `json:"conclusion"`
	}
	if err := json.Unmarshal([]byte(resp.Message.Content), &out); err != nil {
		return false, fmt.Errorf("parse conclusion response: %w", err)
	}
	return out.Conclusion, nil
}

func pickClassifierPrompt(zh bool) string {
	if zh {
		return conclusionClassifierSystemPromptZh
	}
	return conclusionClassifierSystemPromptEn
}

const conclusionClassifierSystemPromptZh = `你是一个编程助手的结论判定器。给定用户目标和助手的最新纯文本回复，判断该回复是否为对目标的最终结论或完成总结。中间过程、下一步计划、部分结果、待办陈述都不是结论。只输出 JSON：{"conclusion": true 或 false}。`

const conclusionClassifierSystemPromptEn = `You are a conclusion classifier for a coding agent. Given the user's goal and the assistant's latest text-only reply, decide whether the reply is the FINAL conclusion or completion summary for the goal. Intermediate process, next-step plans, partial results, or pending todos are NOT conclusions. Output JSON only: {"conclusion": true or false}.`
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestConclusionClassifier -v`
预期：PASS（5 个测试全过）。

- [ ] **步骤 5：Commit**

```bash
git add engine/classifier.go engine/conclusion_classifier_test.go
git commit -m "feat(engine): add ConclusionClassifier for LLM-based conclusion judgment

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 2：StopHook 接口加 ctx + StopHookContext 加 Goal（机械适配）

本任务只改签名与字段，**不改行为**。结束时全量测试仍绿。

**文件：**
- 修改：`engine/stop_hook.go`（`StopHook` 接口、`StopHookContext`、`runStopHooks`、`ZeroToolCallHook.Check`、`StalledNarrationHook.Check` 签名）
- 修改：`engine/turn.go:225`（`runStopHooks` 调用传 ctx + 填 Goal）
- 修改：`engine/stop_hook_test.go`（所有 `Check`/`runStopHooks` 调用加 ctx）
- 修改：`engine/stalled_narration_test.go`（所有 `hook.Check(...)` 调用加 ctx）

- [ ] **步骤 1：改 stop_hook.go 签名**

`engine/stop_hook.go`：

(a) `StopHookContext` 加 `Goal string` 字段（在 `IsChinese` 后）：

```go
type StopHookContext struct {
	RunToolCallCount   int
	LastContent        string
	FinishReason       string
	StopHookActive     bool
	StopHookRetryCount int
	IsChinese          bool
	Goal               string // 当前 Run 的用户目标（e.state.Goal），供 LLM 判定对照
}
```

(b) `StopHook` 接口加 `ctx context.Context`：

```go
type StopHook interface {
	Check(ctx context.Context, sc StopHookContext) StopHookResult
}
```

(c) `ZeroToolCallHook.Check` 加 ctx 参数（逻辑不变）：

```go
func (h *ZeroToolCallHook) Check(_ context.Context, ctx StopHookContext) StopHookResult {
```

（其余函数体不变。注意参数名 `ctx` 与现有 `ctx StopHookContext` 冲突，把第二个改名为 `sc` 并把函数体内 `ctx.` 引用改为 `sc.`。）

(d) `StalledNarrationHook.Check` 同样加 `context.Context` 并把现有 `ctx StopHookContext` 改名 `sc`，函数体内 `ctx.` 改 `sc.`（逻辑仍用 `looksLikeNextStepNarration`，本任务不动）：

```go
func (h *StalledNarrationHook) Check(_ context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount == 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	if sc.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	if !looksLikeNextStepNarration(sc.LastContent) {
		return StopHookResult{}
	}
	// ... 现有 nudge 消息逻辑，把 ctx. 改为 sc. ...
}
```

(e) `runStopHooks` 加 ctx 并透传：

```go
func (e *Engine) runStopHooks(ctx context.Context, sc StopHookContext) StopHookResult {
	exhausted := false
	for _, hook := range e.stopHooks {
		result := hook.Check(ctx, sc)
		if result.Block {
			return result
		}
		if result.Exhausted {
			exhausted = true
		}
	}
	return StopHookResult{Exhausted: exhausted}
}
```

确保 `stop_hook.go` import 含 `"context"`。

- [ ] **步骤 2：改 turn.go 调用点**

`engine/turn.go:225` 的 `runStopHooks` 调用，传 ctx 并填 Goal：

```go
		hookResult := e.runStopHooks(ctx, StopHookContext{
			RunToolCallCount:   e.runToolCallCount,
			LastContent:        content,
			FinishReason:       finish,
			StopHookActive:     e.stopHookActive,
			StopHookRetryCount: e.stopHookRetryCount,
			IsChinese:          e.isChinese,
			Goal:               e.state.Goal,
		})
```

- [ ] **步骤 3：适配 stop_hook_test.go 调用点**

`engine/stop_hook_test.go` 中所有 `hook.Check(StopHookContext{` 改为 `hook.Check(context.Background(), StopHookContext{`；所有 `e.runStopHooks(StopHookContext{` 改为 `e.runStopHooks(context.Background(), StopHookContext{`。共 9 处（6 个 `ZeroToolCallHook.Check` + 3 个 `runStopHooks`）。在 import 块加 `"context"`。

示例（`TestZeroToolCallHook_BlocksWhenNoToolCalls`）：

```go
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		IsChinese:          true,
	})
```

- [ ] **步骤 4：适配 stalled_narration_test.go 调用点**

`engine/stalled_narration_test.go` 中所有 `hook.Check(StopHookContext{` 改为 `hook.Check(context.Background(), StopHookContext{`（约 9 处，分布在 `TestStalledNarrationHook_*` 各测试）。在 import 块加 `"context"`（若未有）。

- [ ] **步骤 5：编译 + 运行全量测试**

运行：`go build ./engine/... && go test ./engine/ -run 'StopHook|StalledNarration|LooksLikeNextStepNarration|ExecuteTurn' -v`
预期：编译通过，所有现有测试 PASS（行为未变）。

- [ ] **步骤 6：Commit**

```bash
git add engine/stop_hook.go engine/turn.go engine/stop_hook_test.go engine/stalled_narration_test.go
git commit -m "refactor(engine): add ctx to StopHook.Check and Goal to StopHookContext

Mechanical signature change; no behavior change. Prepares for LLM
conclusion judgment needing context (timeout) and user goal.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 3：StalledNarrationHook 改用 Classifier（TDD）

**文件：**
- 修改：`engine/stop_hook.go`（`StalledNarrationHook` 持有 `Classifier`，`Check` 改调 `IsConclusion`，抽 `stalledNudgeMsg`）
- 修改：`engine/stalled_narration_test.go`（重写为 stub classifier 驱动，删关键词用例，适配 executeTurn 测试注入 stub classifier）

- [ ] **步骤 1：重写 stalled_narration_test.go 为 stub classifier 驱动**

整个文件替换为下面内容（保留 executeTurn 集成测试，但 `StalledNarrationHook` 注入 stub classifier）：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

// stubClassifier 是 ConclusionJudge 的可控 stub。
type stubClassifier struct {
	conclusion bool
	err        error
	called     bool
	lastGoal   string
	lastText   string
}

func (s *stubClassifier) IsConclusion(_ context.Context, goal, text string) (bool, error) {
	s.called = true
	s.lastGoal = goal
	s.lastText = text
	return s.conclusion, s.err
}

func TestStalledNarrationHook_BlocksWhenNotConclusion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "上述修改已写入 turn.go。下面运行测试验证。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true when classifier says not conclusion")
	}
	if result.Reason != "stalled_narration" {
		t.Errorf("expected Reason='stalled_narration', got %q", result.Reason)
	}
	if result.Message == "" {
		t.Errorf("expected non-empty nudge Message")
	}
}

func TestStalledNarrationHook_PassesWhenConclusion(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: true},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "任务已完成，测试全部通过。",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when classifier says conclusion")
	}
}

func TestStalledNarrationHook_ConservativeBlockOnClassifierError(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{err: errBoom},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 0,
		LastContent:        "some text",
		Goal:               "修复 bug",
		IsChinese:          true,
	})
	if !result.Block {
		t.Errorf("expected Block=true (conservative) when classifier errors")
	}
	if result.Reason != "classifier_error" {
		t.Errorf("expected Reason='classifier_error', got %q", result.Reason)
	}
}

func TestStalledNarrationHook_PassesWhenZeroToolCalls(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   0,
		StopHookRetryCount: 0,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if result.Block {
		t.Errorf("expected Block=false when runToolCallCount==0 (delegated to ZeroToolCallHook)")
	}
}

func TestStalledNarrationHook_ExhaustedAfterMaxRetries(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 2,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 2,
		LastContent:        "查看 X 逻辑",
		IsChinese:          true,
	})
	if !result.Exhausted {
		t.Errorf("expected Exhausted=true when retryCount>=maxRetries")
	}
}

func TestStalledNarrationHook_RetryNudgeReferencesLastContent(t *testing.T) {
	hook := &StalledNarrationHook{
		MaxRetries: 4,
		Classifier: &stubClassifier{conclusion: false},
	}
	result := hook.Check(context.Background(), StopHookContext{
		RunToolCallCount:   3,
		StopHookRetryCount: 1,
		LastContent:        "查看 buildResult 如何提取 Summary",
		IsChinese:          true,
	})
	if !result.Block {
		t.Fatalf("expected Block=true")
	}
	if !strings.Contains(result.Message, "buildResult") {
		t.Errorf("expected retry nudge to reference LastContent 'buildResult', got: %q", result.Message)
	}
}

func TestStalledNarrationHook_PassesGoalAndTextToClassifier(t *testing.T) {
	sc := &stubClassifier{conclusion: true}
	hook := &StalledNarrationHook{MaxRetries: 4, Classifier: sc}
	_, _ = hook.Check(context.Background(), StopHookContext{
		RunToolCallCount: 3,
		LastContent:      "完成",
		Goal:             "目标X",
		IsChinese:        true,
	})
	if !sc.called {
		t.Fatalf("expected classifier to be called")
	}
	if sc.lastGoal != "目标X" || sc.lastText != "完成" {
		t.Errorf("expected goal/text passed to classifier, got goal=%q text=%q", sc.lastGoal, sc.lastText)
	}
}

// executeTurn 集成测试：中间态叙述 -> nudge（不 Done）
func TestExecuteTurn_StalledNarrationAfterToolCalls_Nudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "上述修改已写入 turn.go。下面运行测试验证。", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history:   []Message{{Role: "user", Content: "修复 bug"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: false}}},
		isChinese: true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (mid-task narration -> nudge), got Done=true")
	}
	last := e.history[len(e.history)-1]
	if last.Role != "user" {
		t.Errorf("expected last message to be user nudge, got role=%q", last.Role)
	}
}

// executeTurn 集成测试：真结论 -> Done
func TestExecuteTurn_ConclusionAfterToolCalls_StillDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "执行方案"},
		history:   []Message{{Role: "user", Content: "执行方案"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: true}}},
		isChinese: true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion, got Done=false")
	}
}

// executeTurn 集成测试：classifier error -> 保守 nudge（不 Done）
func TestExecuteTurn_ClassifierError_Nudges(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "some mid text", FinishReason: "stop"},
		}},
		context:   &stubContextBuilder{},
		tools:     stubToolExecutor{},
		state:     &TaskState{TurnNumber: 3, Goal: "修复 bug"},
		history:   []Message{{Role: "user", Content: "修复 bug"}},
		config:    EngineConfig{ModelName: "test-model"},
		stopHooks: []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{err: errBoom}}},
		isChinese: true,
		runToolCallCount: 5,
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false (conservative nudge on classifier error), got Done=true")
	}
}

// executeTurn 集成测试：重试上限耗尽 -> Blocked（不 Done，交回用户）
func TestExecuteTurn_StopHookExhausted_ReturnsBlocked(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "查看 finishStreaming 逻辑，确认用户看到的是流式内容。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3, Goal: "分析截断问题"},
		history:            []Message{{Role: "user", Content: "分析截断问题"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 3}, &StalledNarrationHook{MaxRetries: 2, Classifier: &stubClassifier{conclusion: false}}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 2, // MaxRetries 耗尽
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Done {
		t.Errorf("expected Done=false when stop hook exhausted, got Done=true")
	}
	if !result.Blocked {
		t.Errorf("expected Blocked=true when stop hook exhausted")
	}
	if result.BlockedBy != "stalled_narration_exhausted" {
		t.Errorf("expected BlockedBy='stalled_narration_exhausted', got %q", result.BlockedBy)
	}
}

// executeTurn 集成测试：nudge 后模型给出真结论 -> Done（不 Blocked）
func TestExecuteTurn_ConclusionAfterNudge_ReturnsDone(t *testing.T) {
	e := &Engine{
		model: &stubStreamModel{chunks: []ModelChunk{
			{Delta: "任务已完成，所有文件已修改。", FinishReason: "stop"},
		}},
		context:            &stubContextBuilder{},
		tools:              stubToolExecutor{},
		state:              &TaskState{TurnNumber: 3, Goal: "执行方案"},
		history:            []Message{{Role: "user", Content: "执行方案"}},
		config:             EngineConfig{ModelName: "test-model"},
		stopHooks:          []StopHook{&ZeroToolCallHook{MaxRetries: 5}, &StalledNarrationHook{MaxRetries: 4, Classifier: &stubClassifier{conclusion: true}}},
		isChinese:          true,
		runToolCallCount:   5,
		stopHookRetryCount: 1, // 之前 nudge 过，但 MaxRetries 未耗尽
	}

	result, err := e.executeTurn(context.Background())
	if err != nil {
		t.Fatalf("executeTurn error: %v", err)
	}
	if result.Blocked {
		t.Errorf("expected Blocked=false when model produces conclusion after nudge, got Blocked=true BlockedBy=%q", result.BlockedBy)
	}
	if !result.Done {
		t.Errorf("expected Done=true for genuine conclusion after nudge, got Done=false")
	}
}
```

注意：`errBoom` 已在 `conclusion_classifier_test.go` 定义（同包，复用）。删除旧的 `reportedStallExamples`、`TestStalledNarrationHook_BlocksReportedMidTaskExamples`、`TestStalledNarrationHook_PassesGenuineConclusion`、`TestStalledNarrationHook_EnglishNarration`、`TestStalledNarrationHook_EnglishConclusion`、`TestStalledNarrationHook_DefaultMaxRetries`、`TestLooksLikeNextStepNarration`、`TestExecuteTurn_StopHookExhausted_ReturnsBlocked`（exhausted 行为由 `TestStalledNarrationHook_ExhaustedAfterMaxRetries` 覆盖单元层；若想保留 executeTurn 层 exhausted 集成测试，可保留一个用 `StopHookRetryCount: 3` 的用例，此处从简）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'StalledNarration|ExecuteTurn_(Stalled|Conclusion|Classifier)' -v`
预期：FAIL，编译错误 `unknown field 'Classifier' in struct literal of type StalledNarrationHook`（字段还没加）。

- [ ] **步骤 3：改造 StalledNarrationHook**

`engine/stop_hook.go` 中 `StalledNarrationHook` 改为：

```go
// StalledNarrationHook 在模型已调用工具后输出纯文本时，用 LLM 判定该文本是否
// 最终结论。非结论则 nudge 续接；判定失败保守 nudge；重试上限 Exhausted。
type StalledNarrationHook struct {
	MaxRetries int
	Classifier ConclusionJudge
}

func (h *StalledNarrationHook) Check(ctx context.Context, sc StopHookContext) StopHookResult {
	if sc.RunToolCallCount == 0 {
		return StopHookResult{}
	}
	maxRetries := h.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	if sc.StopHookRetryCount >= maxRetries {
		return StopHookResult{Exhausted: true}
	}
	isConclusion, err := h.Classifier.IsConclusion(ctx, sc.Goal, sc.LastContent)
	if err != nil {
		turnLog.Printf("conclusion classifier error: %v (conservative block)", err)
		return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "classifier_error"}
	}
	if isConclusion {
		return StopHookResult{}
	}
	return StopHookResult{Block: true, Message: stalledNudgeMsg(sc), Reason: "stalled_narration"}
}

// stalledNudgeMsg 生成中英双语 nudge，retry 时引用 LastContent 片段。
func stalledNudgeMsg(sc StopHookContext) string {
	msg := "你在描述下一步却没有实际执行。请直接调用工具继续执行，不要只描述计划；全部完成后再给出最终结论。"
	if !sc.IsChinese {
		msg = "You described the next step without doing it. Call a tool to perform it now - don't just describe a plan - then give your final conclusions once the goal is complete."
	}
	if sc.StopHookRetryCount > 0 && sc.LastContent != "" {
		snippet := truncateStr(sc.LastContent, 60)
		msg = fmt.Sprintf("你又描述了下一步\"%s\"却仍未执行。请立即调用工具，不要再叙述计划。", snippet)
		if !sc.IsChinese {
			msg = fmt.Sprintf("You again described a step (\"%s\") without doing it. Call a tool now - stop narrating and act.", snippet)
		}
	}
	return msg
}
```

确保 `stop_hook.go` import 含 `"context"`（任务 2 已加）。`turnLog` 在 engine 包可见（`turn.go` 定义）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'StalledNarration|ExecuteTurn_(Stalled|Conclusion|Classifier|StopHookExhausted)' -v`
预期：PASS（全部测试通过，含 2 个加回的集成测试）。

- [ ] **步骤 5：Commit**

```bash
git add engine/stop_hook.go engine/stalled_narration_test.go
git commit -m "feat(engine): StalledNarrationHook uses LLM conclusion judgment

Replace looksLikeNextStepNarration keyword heuristic with ConclusionJudge
call. Non-conclusion -> nudge; classifier error -> conservative nudge;
MaxRetries exhausted -> Blocked. Fixes premature loop termination on
non-prefix mid-task narration.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 4：删除关键词启发式死代码

任务 3 后 `looksLikeNextStepNarration`/`nextStepPrefixes`/`conclusionMarkers` 已无引用。

**文件：**
- 修改：`engine/classifier.go`（删除三个符号）

- [ ] **步骤 1：确认无引用**

运行：`rg -n "looksLikeNextStepNarration|nextStepPrefixes|conclusionMarkers" engine/`
预期：仅命中 `classifier.go` 中的定义本身（无其他引用）。

- [ ] **步骤 2：删除三个符号**

从 `engine/classifier.go` 删除：`looksLikeNextStepNarration` 函数（含注释）、`nextStepPrefixes` 变量、`conclusionMarkers` 变量。保留 `isIntermediateText`、`extractRememberMarkers`、`rememberRe` 以及任务 1 新增的 `ConclusionClassifier` 相关内容。

- [ ] **步骤 3：编译 + 全量测试**

运行：`go build ./engine/... && go test ./engine/ -v`
预期：编译通过，全量 PASS。

- [ ] **步骤 4：Commit**

```bash
git add engine/classifier.go
git commit -m "refactor(engine): remove dead keyword heuristic looksLikeNextStepNarration

Replaced by LLM ConclusionClassifier; no remaining references.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 5：Engine.NewConclusionClassifier + cmd/exec.go 注册注入

**文件：**
- 修改：`engine/loop.go`（新增方法）
- 修改：`cmd/exec.go`（注入 classifier）

- [ ] **步骤 1：新增 Engine.NewConclusionClassifier**

在 `engine/loop.go` 的 `Engine` 方法区（靠近 `SetStopHooks` 或其他 setter 附近，例如 `runStopHooks` 定义之后）追加：

```go
// NewConclusionClassifier constructs a ConclusionClassifier bound to the
// engine's model, flash model name, and language preference. Used by callers
// (e.g. cmd/exec.go) to wire StalledNarrationHook without exposing e.model.
func (e *Engine) NewConclusionClassifier() *ConclusionClassifier {
	return NewConclusionClassifier(e.model, e.config.FlashModelName, e.isChinese)
}
```

- [ ] **步骤 2：cmd/exec.go 注入 classifier**

`cmd/exec.go:26` 的 `SetStopHooks` 调用改为：

```go
	agent := engine.NewEngine(config, deps)
	classifier := agent.NewConclusionClassifier()
	agent.SetStopHooks([]engine.StopHook{
		&engine.ZeroToolCallHook{MaxRetries: 5},
		&engine.StalledNarrationHook{MaxRetries: 4, Classifier: classifier},
	})
```

- [ ] **步骤 3：编译 + 验证 cmd 注册**

运行：`go build ./... `
预期：编译通过（含 `cmd/`）。

- [ ] **步骤 4：Commit**

```bash
git add engine/loop.go cmd/exec.go
git commit -m "feat(engine): wire ConclusionClassifier into StalledNarrationHook

Add Engine.NewConclusionClassifier and inject it at cmd/exec.go
registration. ZeroToolCallHook unchanged.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## 任务 6：全量验证

- [ ] **步骤 1：全量测试（含 race）**

运行：`make test`
预期：PASS，无 race。

- [ ] **步骤 2：构建**

运行：`make build`
预期：生成 `./deepact`，无错误。

- [ ] **步骤 3：lint**

运行：`make lint`
预期：无新增告警。

- [ ] **步骤 4：（可选）手动冒烟**

运行一个会触发中间叙述的多步任务，确认不再提前结束、不再需要手动"继续"。
（此步可选，不阻塞完成。）

---

## 自检

**1. 规格覆盖度：**
- ConclusionClassifier 单元 → 任务 1 ✓
- 双语 system prompt → 任务 1 ✓
- `StopHook.Check` 加 ctx → 任务 2 ✓
- `StopHookContext.Goal` → 任务 2 ✓
- `StalledNarrationHook` 持有 Classifier、调 IsConclusion、失败保守 block、MaxRetries 兜底 → 任务 3 ✓
- `ZeroToolCallHook` 不变 → 任务 2 仅签名适配 ✓
- `turn.go` 注入 Goal + 传 ctx → 任务 2 ✓
- `Engine.NewConclusionClassifier` → 任务 5 ✓
- `cmd/exec.go` 注册注入 → 任务 5 ✓
- 删 `looksLikeNextStepNarration`/`nextStepPrefixes`/`conclusionMarkers` → 任务 4 ✓
- 保留 `isIntermediateText` → 任务 4 明确保留 ✓
- 测试策略（classifier 单元、hook stub、executeTurn 集成）→ 任务 1/3 ✓

**2. 占位符扫描：** 无 TODO/待定；所有代码步骤含完整代码块；删除清单明确。✓

**3. 类型一致性：**
- `ConclusionJudge.IsConclusion(ctx, goal, text) (bool, error)` — 任务 1 定义、任务 3 调用、stub 实现 ✓
- `NewConclusionClassifier(model, flashModelName, isChinese)` — 任务 1 定义、任务 5 调用 ✓
- `StalledNarrationHook.Classifier ConclusionJudge` — 任务 3 定义、任务 5 注入 ✓
- `Check(ctx context.Context, sc StopHookContext)` — 任务 2 定义、任务 3 实现、测试调用一致 ✓
- `StopHookContext.Goal` — 任务 2 加、任务 2 turn.go 填、任务 3 用 `sc.Goal` ✓
- `errBoom` — 任务 1 定义、任务 3 复用（同包）✓
- `turnLog` — engine 包可见，任务 3 使用 ✓
