# IntentJudge LLM 判定 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `detectUserIntent` 的三个关键字意图判定函数替换为一次 LLM 判定（`IntentJudge`），复用 `ConclusionClassifier` 范式。

**Architecture:** 新增 `IntentClassifier`（flash 模型 + JSON `{intent}`），注入 Engine via `SetIntentJudge`。`detectUserIntent` 保留 `isDangerousConfirmation` 确定性安全门作为快速路径，其余交 LLM 判定；nil/err 保守兜底 `IntentContinue`。删除 `isAnalysisOnly`/`hasContextReference`/`isSameTopic`/`extractKeyTerms`/`isCJK`。

**Tech Stack:** Go, flash LLM via `ModelClient.Complete`, `JsonMode`, table-driven tests with stub `ModelClient`/`IntentJudge`.

## Global Constraints

- 复用 `ConclusionClassifier`（`classifier.go:261-364`）的完整范式：flash 模型、`Temperature:0`、`MaxTokens:64`、`JsonMode:true`、10s 超时、`parseConclusionJSON` 同款兜底解析。
- `isDangerousConfirmation`（`loop.go:907`）保留不动（确定性安全门）。
- nil `intentJudge` / classify err -> 保守 `IntentContinue`（不重置 PlanConfirmed）。
- `UserIntent` 枚举已定义于 `types.go:10-15`：`IntentContinue=0`、`IntentNewTopic=1`、`IntentAnalyze=2`。

## File Structure

| 文件 | 责任 | 操作 |
|------|------|------|
| `engine/classifier.go` | 新增 `IntentCheck`/`IntentJudge`/`IntentClassifier` + 双语 prompt + `parseIntentJSON` | 追加（在 `ConclusionClassifier` 段之后，`buildToolCallSummary` 之前） |
| `engine/intent_classifier_test.go` | `IntentClassifier` 单元测试（stub `ModelClient`） | 新建 |
| `engine/loop.go` | `detectUserIntent` 改造 + Engine 字段/setter/构造方法 + 删旧关键字函数 | 修改 |
| `engine/loop_intent_test.go` | 删旧函数测试 + 新增 stub-judge 测试 | 修改 |
| `cmd/exec.go` | 注入 `SetIntentJudge` | 修改 |
| `ui/runner.go` | 注入 `SetIntentJudge` | 修改 |

---

### Task 1: IntentClassifier

**Files:**
- Create: `engine/intent_classifier_test.go`
- Modify: `engine/classifier.go`（追加到 `pickClassifierPrompt` 函数之后、`buildToolCallSummary` 注释之前，即 `conclusionClassifierSystemPromptEn` 常量之后）

**Interfaces:**
- Consumes: `ModelClient`（`interfaces.go:5`）、`ModelRequest`/`ModelResponse`/`ModelMessage`（`types.go:102-119`）、`UserIntent`（`types.go:10-15`）
- Produces: `IntentCheck` struct、`IntentJudge` interface、`IntentClassifier` struct、`NewIntentClassifier(model, flashModelName, isChinese) *IntentClassifier`、`(*IntentClassifier).Classify(ctx, IntentCheck) (UserIntent, error)`

- [ ] **Step 1: Write the failing test**

Create `engine/intent_classifier_test.go`:

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

func TestIntentClassifier_Analyze(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "analyze"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "分析一下这个接口为什么报错"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentAnalyze {
		t.Errorf("expected IntentAnalyze, got %v", got)
	}
}

func TestIntentClassifier_Continue(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "continue"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "刚才那个再调整一下"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentContinue {
		t.Errorf("expected IntentContinue, got %v", got)
	}
}

func TestIntentClassifier_NewTopic(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "new_topic"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	got, err := c.Classify(context.Background(), IntentCheck{Goal: "添加登录页面", Message: "重构数据库查询"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != IntentNewTopic {
		t.Errorf("expected IntentNewTopic, got %v", got)
	}
}

func TestIntentClassifier_BadJSON_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `not json`}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error for non-JSON response, got nil")
	}
}

func TestIntentClassifier_CallError_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{err: errBoom}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error from Complete, got nil")
	}
}

func TestIntentClassifier_UnrecognizedIntent_ReturnsError(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "unknown"}`}
	c := NewIntentClassifier(m, "flash-model", true)
	_, err := c.Classify(context.Background(), IntentCheck{Goal: "goal", Message: "msg"})
	if err == nil {
		t.Fatalf("expected error for unrecognized intent, got nil")
	}
}

func TestIntentClassifier_RequestShape(t *testing.T) {
	m := &stubCompleteModel{resp: `{"intent": "analyze"}`}
	c := NewIntentClassifier(m, "flash-model", false)
	_, _ = c.Classify(context.Background(), IntentCheck{Goal: "add login page", Message: "explain the logic"})
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
	if req.MaxTokens != 64 {
		t.Errorf("expected MaxTokens=64, got %d", req.MaxTokens)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Errorf("expected system+user messages, got %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, "add login page") || !strings.Contains(req.Messages[1].Content, "explain the logic") {
		t.Errorf("expected user message to contain goal and message, got %q", req.Messages[1].Content)
	}
}

func TestIntentClassifier_ParsesNonPureJSON(t *testing.T) {
	tests := []struct {
		name   string
		resp   string
		want   UserIntent
	}{
		{"markdown wrapped analyze", "```json\n{\"intent\": \"analyze\"}\n```", IntentAnalyze},
		{"markdown wrapped continue", "```json\n{\"intent\": \"continue\"}\n```", IntentContinue},
		{"prefix text then json", "根据分析，意图如下：\n{\"intent\": \"new_topic\"}", IntentNewTopic},
		{"suffix text after json", "{\"intent\": \"analyze\"}\n以上是判定。", IntentAnalyze},
		{"json with leading spaces", "   {\"intent\": \"continue\"}   ", IntentContinue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &stubCompleteModel{resp: tt.resp}
			c := NewIntentClassifier(m, "flash-model", true)
			got, err := c.Classify(context.Background(), IntentCheck{Goal: "g", Message: "m"})
			if err != nil {
				t.Fatalf("unexpected err for %s: %v (resp=%q)", tt.name, err, tt.resp)
			}
			if got != tt.want {
				t.Errorf("%s: got %v, want %v (resp=%q)", tt.name, got, tt.want, tt.resp)
			}
		})
	}
}
```

> Note: `stubCompleteModel` and `errBoom` are defined in `conclusion_classifier_test.go` (same package `engine`), reused here.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run TestIntentClassifier -v`
Expected: FAIL / build error — `undefined: NewIntentClassifier`, `undefined: IntentClassifier`, `undefined: IntentCheck`

- [ ] **Step 3: Write minimal implementation**

Append to `engine/classifier.go`, after the `conclusionClassifierSystemPromptEn` constant (line 364) and before the `buildToolCallSummary` comment (line 366):

```go

// IntentCheck bundles the information the judge needs to classify user intent.
type IntentCheck struct {
	Goal    string // current Run's user goal (e.state.Goal)
	Message string // user's latest message
}

// IntentJudge classifies a user message relative to the current goal into
// IntentAnalyze, IntentContinue, or IntentNewTopic. Interface for testability;
// *IntentClassifier is the production impl.
type IntentJudge interface {
	Classify(ctx context.Context, check IntentCheck) (UserIntent, error)
}

// IntentClassifier reuses the ConclusionClassifier's Complete + JsonMode
// pattern with a flash model to control cost.
type IntentClassifier struct {
	model          ModelClient
	flashModelName string
	isChinese      bool
}

func NewIntentClassifier(model ModelClient, flashModelName string, isChinese bool) *IntentClassifier {
	return &IntentClassifier{model: model, flashModelName: flashModelName, isChinese: isChinese}
}

// Classify returns IntentAnalyze / IntentContinue / IntentNewTopic;
// err on LLM call or JSON parse failure (caller falls back conservatively).
func (c *IntentClassifier) Classify(ctx context.Context, check IntentCheck) (UserIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var prompt string
	if c.isChinese {
		prompt = fmt.Sprintf("目标：%s\n\n用户消息：%s", check.Goal, check.Message)
	} else {
		prompt = fmt.Sprintf("Goal: %s\n\nUser message: %s", check.Goal, check.Message)
	}
	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickIntentPrompt(c.isChinese)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		MaxTokens:   64,
		JsonMode:    true,
	}
	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("intent classify: %w", err)
	}
	return parseIntentJSON(resp.Message.Content)
}

// parseIntentJSON extracts the intent verdict from the model's response.
// Mirrors parseConclusionJSON: tries direct parse, then extracts first {...}.
func parseIntentJSON(content string) (UserIntent, error) {
	content = strings.TrimSpace(content)
	var out struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(content), &out); err == nil {
		return intentFromString(out.Intent)
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &out); err == nil {
			return intentFromString(out.Intent)
		}
	}
	return 0, fmt.Errorf("parse intent response: no valid JSON in %q", content)
}

func intentFromString(s string) (UserIntent, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "analyze":
		return IntentAnalyze, nil
	case "continue":
		return IntentContinue, nil
	case "new_topic":
		return IntentNewTopic, nil
	default:
		return 0, fmt.Errorf("unrecognized intent %q", s)
	}
}

func pickIntentPrompt(zh bool) string {
	if zh {
		return intentClassifierSystemPromptZh
	}
	return intentClassifierSystemPromptEn
}

const intentClassifierSystemPromptZh = `你是一个编程助手的用户意图分类器。给定用户当前目标和用户最新消息，判断消息意图属于哪一类。

analyze：用户仅要求分析、解释、排查、检查，不要求修改代码。
continue：用户继续当前目标的已有工作（追加、修改、验证、优化之前的内容，或引用之前的工作）。
new_topic：用户开启与当前目标无关的新任务。

只输出 JSON：{"intent": "analyze" 或 "continue" 或 "new_topic"}。`

const intentClassifierSystemPromptEn = `You are a user-intent classifier for a coding agent. Given the user's current goal and the user's latest message, classify the message intent.

analyze: the user only asks for analysis, explanation, investigation, or inspection - no code changes requested.
continue: the user continues existing work on the current goal (adding to, modifying, verifying, or optimizing prior work, or referencing previous work).
new_topic: the user starts a new task unrelated to the current goal.

Output JSON only: {"intent": "analyze" or "continue" or "new_topic"}.`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./engine/ -run TestIntentClassifier -v`
Expected: PASS (all 8 test functions)

- [ ] **Step 5: Commit**

```bash
git add engine/classifier.go engine/intent_classifier_test.go
git commit -m "feat: add IntentClassifier LLM judge for user-intent classification

Mirrors ConclusionClassifier: flash model + JsonMode {intent} output.
Replaces keyword-based isAnalysisOnly/hasContextReference/isSameTopic."
```

---

### Task 2: Integrate IntentJudge into detectUserIntent + wire callers

**Files:**
- Modify: `engine/loop.go` — Engine struct field, setter, constructor method, `detectUserIntent` refactor, call-site update, delete 5 keyword functions
- Modify: `engine/loop_intent_test.go` — delete old tests, add stub-judge tests
- Modify: `cmd/exec.go:26-30` — add `SetIntentJudge` call
- Modify: `ui/runner.go:89-93` — add `SetIntentJudge` call

**Interfaces:**
- Consumes: `IntentJudge`/`IntentCheck`/`NewIntentClassifier` (from Task 1)
- Produces: `(*Engine).SetIntentJudge(IntentJudge)`, `(*Engine).NewIntentClassifier() *IntentClassifier`, refactored `(*Engine).detectUserIntent(ctx, userMsg) UserIntent`

- [ ] **Step 1: Write the failing tests**

Rewrite `engine/loop_intent_test.go` entirely:

```go
package engine

import (
	"context"
	"testing"
)

// stubIntentJudge is a controllable IntentJudge stub for detectUserIntent tests.
type stubIntentJudge struct {
	intent UserIntent
	err    error
	called bool
	last   IntentCheck
}

func (s *stubIntentJudge) Classify(_ context.Context, check IntentCheck) (UserIntent, error) {
	s.called = true
	s.last = check
	if s.err != nil {
		return 0, s.err
	}
	return s.intent, nil
}

func TestDetectUserIntent_NoGoal_Continue(t *testing.T) {
	e := &Engine{state: &TaskState{Goal: ""}}
	got := e.detectUserIntent(context.Background(), "分析一下")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue for empty goal, got %v", got)
	}
}

func TestDetectUserIntent_ConfirmationFastPath(t *testing.T) {
	// isDangerousConfirmation is a deterministic fast-path; judge must NOT be called.
	judge := &stubIntentJudge{intent: IntentNewTopic} // would be wrong if called
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	for _, msg := range []string{"确认", "确认执行", "确认执行修改", "继续执行", "yes"} {
		judge.called = false
		got := e.detectUserIntent(context.Background(), msg)
		if got != IntentContinue {
			t.Errorf("detectUserIntent(%q) = %v, want IntentContinue", msg, got)
		}
		if judge.called {
			t.Errorf("detectUserIntent(%q) should not call judge (confirmation fast-path)", msg)
		}
	}
}

func TestDetectUserIntent_NilJudge_Continue(t *testing.T) {
	// nil intentJudge (wiring bug) falls back conservatively to IntentContinue.
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue for nil judge, got %v", got)
	}
}

func TestDetectUserIntent_JudgeAnalyze(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "为什么点击没反应")
	if got != IntentAnalyze {
		t.Errorf("expected IntentAnalyze, got %v", got)
	}
	if !judge.called {
		t.Error("expected judge to be called")
	}
	if judge.last.Goal != "添加登录页面功能" {
		t.Errorf("expected goal passed to judge, got %q", judge.last.Goal)
	}
}

func TestDetectUserIntent_JudgeContinue(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentContinue}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "刚才那个再调整一下")
	if got != IntentContinue {
		t.Errorf("expected IntentContinue, got %v", got)
	}
}

func TestDetectUserIntent_JudgeNewTopic(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentNewTopic}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentNewTopic {
		t.Errorf("expected IntentNewTopic, got %v", got)
	}
}

func TestDetectUserIntent_JudgeError_Continue(t *testing.T) {
	judge := &stubIntentJudge{err: errBoom}
	e := &Engine{state: &TaskState{Goal: "添加登录页面功能"}, intentJudge: judge}
	got := e.detectUserIntent(context.Background(), "重构数据库查询")
	if got != IntentContinue {
		t.Errorf("expected conservative IntentContinue on judge error, got %v", got)
	}
}

func TestIsDangerousConfirmation(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		// Exact matches
		{"确认", true}, {"yes", true}, {"好的", true}, {"y", true},
		// Separator-compound
		{"对，改吧", true}, {"好的，执行", true}, {"ok, go", true},
		// Concatenated confirm words (no separator)
		{"确认执行", true}, {"继续执行", true}, {"继续", true}, {"执行吧", true},
		// Exact "修改" compounds
		{"确认执行修改", true}, {"确认修改", true}, {"执行修改", true},
		// Real instructions / feedback must NOT be treated as confirmation
		{"确认但先改下方案", false}, {"改一下方案再执行", false}, {"修改一下配置", false},
		{"修改", false}, {"你确认下对不对", false}, {"did", false}, {"你好", false},
	}
	for _, tt := range tests {
		if got := isDangerousConfirmation(tt.msg); got != tt.exp {
			t.Errorf("isDangerousConfirmation(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}

func TestIsClearCommand(t *testing.T) {
	tests := []struct {
		msg string
		exp bool
	}{
		{"/clear", true},
		{"/clear ", true},
		{"/clear all the things", true},
		{"/Clear", false},
		{"clear", false},
		{"/clear  extra", true},
	}
	for _, tt := range tests {
		if got := isClearCommand(tt.msg); got != tt.exp {
			t.Errorf("isClearCommand(%q) = %v, want %v", tt.msg, got, tt.exp)
		}
	}
}
```

> This deletes `TestDetectUserIntent_AnalyzeOnly`, `TestHasContextReference`, `TestIsAnalysisOnly`, `TestExtractKeyTerms`, `TestIsSameTopic` and `TestDetectUserIntent_ConfirmationContinues` (replaced by `TestDetectUserIntent_ConfirmationFastPath`). Keeps `TestIsDangerousConfirmation` and `TestIsClearCommand` unchanged.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./engine/ -run TestDetectUserIntent -v`
Expected: FAIL / build error — `e.intentJudge undefined`, `detectUserIntent takes 1 arg but 2 given`, `undefined: stubIntentJudge`

- [ ] **Step 3: Write the implementation**

**(a) Add `intentJudge` field to Engine struct** — in `engine/loop.go`, after the `stopHooks []StopHook` field (line 109):

```go
	// stopHooks are checked when the model outputs text without tool calls.
	stopHooks []StopHook
	// intentJudge classifies user messages into analyze/continue/new_topic
	// via a lightweight LLM call. Replaces keyword-based isAnalysisOnly/
	// hasContextReference/isSameTopic. Nil falls back to IntentContinue.
	intentJudge IntentJudge
```

**(b) Add setter and constructor method** — in `engine/loop.go`, after the `SetStopHooks` method (defined in `stop_hook.go:170`). Add these to `loop.go` near `NewConclusionClassifier` is NOT defined in loop.go (it's in stop_hook.go:177), so add both methods in `loop.go` after `NewEngine` or anywhere in the file. Place after the `deactivateSkill` function (around line 1210):

```go
// SetIntentJudge registers the intent classifier used by detectUserIntent.
func (e *Engine) SetIntentJudge(j IntentJudge) { e.intentJudge = j }

// NewIntentClassifier constructs an IntentClassifier bound to the engine's
// model, flash model name, and language preference. Used by callers (e.g.
// cmd/exec.go) to wire detectUserIntent without exposing e.model.
func (e *Engine) NewIntentClassifier() *IntentClassifier {
	return NewIntentClassifier(e.model, e.config.FlashModelName, e.isChinese)
}
```

**(c) Refactor `detectUserIntent`** — replace the entire function and its doc comment (loop.go:1296-1340) with:

```go
// detectUserIntent classifies the user's message relative to the current goal.
// isDangerousConfirmation is a deterministic fast-path (safety gate, not fuzzy
// intent detection). All other messages go through the LLM IntentJudge; nil
// judge or classify error falls back conservatively to IntentContinue (does not
// reset PlanConfirmed, avoiding spurious edit-plan re-confirmation).
func (e *Engine) detectUserIntent(ctx context.Context, userMsg string) UserIntent {
	if e.state == nil || e.state.Goal == "" {
		return IntentContinue
	}

	msg := strings.ToLower(strings.TrimSpace(userMsg))

	// Deterministic safety gate: pure confirmation continues the current task.
	if isDangerousConfirmation(msg) {
		return IntentContinue
	}

	// Wiring bug guard: nil judge falls back to IntentContinue.
	if e.intentJudge == nil {
		loopLog.Printf("intentJudge not set (wiring bug), falling back to continue")
		return IntentContinue
	}

	intent, err := e.intentJudge.Classify(ctx, IntentCheck{Goal: e.state.Goal, Message: userMsg})
	if err != nil {
		loopLog.Printf("intent classify error: %v (conservative fallback to continue)", err)
		return IntentContinue
	}
	return intent
}
```

**(d) Update the call site** — in `engine/loop.go:608`, change:

```go
	intent := e.detectUserIntent(userMsg)
```

to:

```go
	intent := e.detectUserIntent(ctx, userMsg)
```

> `ctx` is in scope: `Run()` receives it as its first parameter and uses it in the turn loop (loop.go:651 `case <-ctx.Done()`).

**(e) Delete the 5 keyword functions** — remove these functions and their doc comments from `engine/loop.go`:

- `hasContextReference` (loop.go:1342-1362)
- `isAnalysisOnly` (loop.go:1364-1444)
- `isSameTopic` (loop.go:1446-1471)
- `extractKeyTerms` (loop.go:1473 to its closing brace, includes the inline `stopWords` map)
- `isCJK` (loop.go:1522 to its closing brace)

> Verification: these functions are only called by `detectUserIntent` (now deleted) and `loop_intent_test.go` (rewritten in Step 1). `grep -n 'isAnalysisOnly\|hasContextReference\|isSameTopic\|extractKeyTerms\|isCJK' engine/*.go` should return zero matches after deletion (excluding the rewrite test file which no longer references them).

**(f) Wire `cmd/exec.go`** — after the `SetStopHooks` call (cmd/exec.go:30), add:

```go
	agent.SetStopHooks([]engine.StopHook{
		&engine.ZeroToolCallHook{MaxRetries: 5},
		&engine.StalledNarrationHook{MaxRetries: 4, Classifier: classifier},
	})
	agent.SetIntentJudge(agent.NewIntentClassifier())
```

**(g) Wire `ui/runner.go`** — after the `SetStopHooks` call (ui/runner.go:93), add:

```go
		r.eng.SetStopHooks([]engine.StopHook{
			&engine.ZeroToolCallHook{MaxRetries: 5},
			&engine.StalledNarrationHook{MaxRetries: 4, Classifier: r.eng.NewConclusionClassifier()},
		})
		r.eng.SetIntentJudge(r.eng.NewIntentClassifier())
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./engine/ -run 'TestDetectUserIntent|TestIsDangerousConfirmation|TestIsClearCommand' -v`
Expected: PASS (all test functions)

Then run the full engine test suite to catch any regressions from the deleted functions:

Run: `go test ./engine/ -v`
Expected: PASS (no compilation errors from dangling references)

Then verify the whole project builds:

Run: `go build ./...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add engine/loop.go engine/loop_intent_test.go cmd/exec.go ui/runner.go
git commit -m "refactor: replace keyword intent detection with LLM IntentJudge

detectUserIntent now uses IntentJudge (LLM) instead of isAnalysisOnly/
hasContextReference/isSameTopic keyword matching. Keeps isDangerousConfirmation
as a deterministic safety fast-path. nil/err fallback to IntentContinue.
Wired in cmd/exec.go and ui/runner.go via SetIntentJudge."
```

---

## Self-Review

**1. Spec coverage:**
- Spec §1 (IntentJudge in classifier.go) → Task 1 ✓
- Spec §2 (判定 prompt, JsonMode, parseIntentJSON 兜底) → Task 1 implementation ✓
- Spec §3 (detectUserIntent 改造, ctx 参数, nil 守卫, isDangerousConfirmation 快速路径) → Task 2(c) ✓
- Spec §4 (Engine 字段 + SetIntentJudge + NewIntentClassifier) → Task 2(a)(b) ✓
- Spec §4 (cmd/exec.go + ui/runner.go 接线) → Task 2(f)(g) ✓
- Spec §5 (数据流) → Task 2(c) covers all branches ✓
- Spec 测试策略 (IntentClassifier 单元测试, detectUserIntent stub-judge 测试, 删除旧函数测试) → Task 1 Step 1 + Task 2 Step 1 ✓
- Spec 范围边界 (isDangerousConfirmation 保留, detectIntentShift 不动, 组1/3/4 不动) → 计划未触碰这些 ✓

**2. Placeholder scan:** No "TBD"/"TODO"/"add appropriate" found. All code steps contain complete Go code. Deletion steps specify exact function names + line ranges. ✓

**3. Type consistency:**
- `IntentCheck{Goal, Message}` — defined Task 1, consumed Task 2(c) ✓
- `IntentJudge.Classify(ctx, IntentCheck) (UserIntent, error)` — defined Task 1, stubbed Task 2 Step 1, called Task 2(c) ✓
- `NewIntentClassifier(model, flashModelName, isChinese)` — defined Task 1, wrapped as `(*Engine).NewIntentClassifier()` Task 2(b), called Task 2(f)(g) ✓
- `SetIntentJudge(IntentJudge)` — defined Task 2(b), called Task 2(f)(g) ✓
- `detectUserIntent(ctx, userMsg)` — defined Task 2(c), called Task 2(d), tested Task 2 Step 1 ✓
- `UserIntent` values `IntentAnalyze`/`IntentContinue`/`IntentNewTopic` — from `types.go:13-15`, used consistently ✓
- `intentFromString` returns `UserIntent` — matches `Classify` return type ✓
- `parseIntentJSON` returns `(UserIntent, error)` — matches `Classify` usage ✓
