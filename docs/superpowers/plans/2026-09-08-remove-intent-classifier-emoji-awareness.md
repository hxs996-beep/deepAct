# 移除 IntentClassifier + 用户负面情绪感知 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 移除 `IntentClassifier`（每轮省 1 次 flash 调用），并新增中文负面情绪感知（关键词检测 → prompt 注入反思重规划）。

**架构：** 删除 `classifier.go`/`types.go`/`loop.go`/`stop_hook.go`/`turn.go`/`builder.go`/`cmd/exec.go`/`ui/runner.go` 中的 Intent 相关代码与 `AnalysisMode` 字段（b1 连带删除）；新增 `engine/feedback.go` 纯关键词检测函数 + `engine/loop.go` 原 intent switch 位置接入反思注入。

**技术栈：** Go, 纯正则/关键词匹配（零 LLM 调用）, 标准 `testing`。

**规格：** `docs/superpowers/specs/2026-09-08-user-emotion-awareness-design.md`（已批准）

---

## 文件结构

| 文件 | 职责 | 操作 |
|------|------|------|
| `engine/test_stubs_test.go` | 共享测试 stub：`errBoom` / `stubCompleteModel`（从 `intent_classifier_test.go` 迁出，避免删除该文件后 roundtable/compressor 测试编译失败） | 新建 |
| `engine/intent_classifier_test.go` | 6 个 `TestIntentClassifier_*` + stub 定义 | 整个删除 |
| `engine/feedback.go` | `detectNegativeFeedback`（负面关键词检测）+ `applyNegativeFeedbackRewrite`（改写 history 注入反思指引） | 新建 |
| `engine/feedback_test.go` | 检测命中/防误报/注入行为/语言自适应测试 | 新建 |
| `engine/classifier.go` | 删 Intent 相关（IntentCheck/IntentJudge/IntentClassifier/Classify/parseIntentJSON/intentFromString/pickIntentPrompt/2 个 prompt 常量）；保留 `rememberRe`/`extractRememberMarkers`/`isIntermediateText` | 修改 |
| `engine/types.go` | 删 `UserIntent` 枚举(8-15)、`AnalysisMode` 字段(264-269) | 修改 |
| `engine/loop.go` | 删 `intentJudge` 字段(143-146)、`skillJustActivated`(457,464)、`SetIntentJudge`(1559-1560)、`NewIntentClassifier()`(1562-1566)、`detectUserIntent`(1654-1683)、意图 switch(700-731)、所有 `e.state.AnalysisMode = false` 赋值；原 switch 位置接入 `applyNegativeFeedbackRewrite` | 修改 |
| `engine/stop_hook.go` | 删 `AnalysisMode` 字段(18) | 修改 |
| `engine/turn.go` | 删 `AnalysisMode: e.state.AnalysisMode` 传参(278) | 修改 |
| `context/builder.go` | 删 `[ANALYSIS MODE]` 注入块(168-177) | 修改 |
| `context/builder_test.go` | 删 `TestBuild_AnalysisModeConstraint`(260-292)、`TestBuild_AnalysisModeConstraint_SoftWording`(296-320) | 修改 |
| `cmd/exec.go` | 删 `agent.SetIntentJudge(agent.NewIntentClassifier())`(27) | 修改 |
| `ui/runner.go` | 删 `r.eng.SetIntentJudge(r.eng.NewIntentClassifier())`(116) | 修改 |
| `engine/loop_intent_test.go` | 整个删除（`stubIntentJudge` + 8 个测试） | 删除 |
| `engine/confirm_command_test.go` | 删 `TestDetectUserIntent_ConfirmCommandFastPath`(92-101)；删 3 处 `AnalysisMode` 字段与 2 处断言块 | 修改 |
| `engine/analysis_gate_test.go` | 删 2 处 `AnalysisMode: true` 字段与 1 处断言块 | 修改 |
| `engine/present_options_test.go` | 删 3 处 `AnalysisMode: true` 字段、`e.state.AnalysisMode ||` 断言 | 修改 |
| `engine/present_options_ends_run_test.go` | 删 state 中 `AnalysisMode: true`(48) | 修改 |
| `engine/loop_guard_reset_test.go` | 无需改动（line 49 仅历史注释） | — |

---

### 任务 1：迁移共享测试 stub

**文件：**
- 创建：`engine/test_stubs_test.go`
- 删除：`engine/intent_classifier_test.go`

- [ ] **步骤 1：创建共享 stub 文件**

```go
package engine

import (
	"context"
	"errors"
)

// errBoom is a sentinel error for tests that assert error propagation.
var errBoom = errors.New("boom")

// stubCompleteModel is a controllable ModelClient stub: Complete returns preset
// content or error, and captures the last request for assertions. Stream is
// unused by this test suite.
type stubCompleteModel struct {
	resp      string
	reasoning string
	err       error
	last      ModelRequest
}

func (m *stubCompleteModel) Stream(context.Context, ModelRequest) (<-chan ModelChunk, error) {
	return nil, nil
}

func (m *stubCompleteModel) Complete(_ context.Context, req ModelRequest) (*ModelResponse, error) {
	m.last = req
	if m.err != nil {
		return nil, m.err
	}
	return &ModelResponse{Message: ModelMessage{Content: m.resp, ReasoningContent: m.reasoning}}, nil
}
```

- [ ] **步骤 2：删除 intent_classifier_test.go**

`git rm engine/intent_classifier_test.go`（内容全部是 stub + `TestIntentClassifier_*`，stub 已迁出）

- [ ] **步骤 3：运行测试验证 roundtable/compressor 测试仍通过**

运行：`go test ./engine/ -run 'TestBuildBlueprint|TestSynthesizeDebate|TestCompressModelMessages|TestDetermineWinner'`
预期：PASS（这些测试依赖迁移后的 `stubCompleteModel`/`errBoom`）

- [ ] **步骤 4：Commit**

```bash
git add engine/test_stubs_test.go
git rm engine/intent_classifier_test.go
git commit -m "test: move errBoom/stubCompleteModel to shared stubs file"
```

---

### 任务 2：新增负面情绪检测函数（TDD）

**文件：**
- 创建：`engine/feedback.go`
- 测试：`engine/feedback_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
package engine

import (
	"strings"
	"testing"
)

func TestDetectNegativeFeedback_Hits(t *testing.T) {
	positives := []string{
		// 失望/不满
		"不对，这方向有问题",
		"这个方案没用",
		"白做了一下午",
		"太差了",
		"什么垃圾代码",
		// 反驳/纠正方向
		"不是这样，我让你改这里",
		"你理解错了",
		"方向不对",
		"跑偏了",
		// 质疑/批评
		"你怎么搞的",
		"就这水平？",
		"越做越差",
		// 停止/重做
		"重新来",
		"推倒重来",
		"停下，别做了",
		"撤销刚才的修改",
		// 英文
		"this sucks",
		"not what I asked",
		"that's wrong",
		"try again",
		"why did you change it",
	}
	for _, msg := range positives {
		if !detectNegativeFeedback(msg) {
			t.Errorf("detectNegativeFeedback(%q) = false, want true", msg)
		}
	}
}

func TestDetectNegativeFeedback_Misses(t *testing.T) {
	negatives := []string{
		"停车场怎么走",
		"如果不对就跳过这个",
		"确认",
		"继续",
		"/confirm 1",
		"/clear",
		"帮我加个按钮",
		"这个接口为什么报错",
		"可以",
	}
	for _, msg := range negatives {
		if detectNegativeFeedback(msg) {
			t.Errorf("detectNegativeFeedback(%q) = true, want false", msg)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestDetectNegativeFeedback'`
预期：FAIL，报错 "undefined: detectNegativeFeedback"

- [ ] **步骤 3：编写最少实现代码**

```go
package engine

import (
	"regexp"
	"strings"
)

// buDuiRe 匹配"不对"的负面用法：独立成句或带语气/标点。
// 避免误伤条件句"如果不对就跳过"（"不对就"无标点且不以"不对"开头）。
var buDuiRe = regexp.MustCompile(`(?m)^\s*不对[，。！？!?]|(?m)^\s*不对吧|(?m)^\s*不对$`)

// jiuZheRe 匹配"就这"的负面用法：后接标点/语气词或行尾。
// 避免误伤"就这个接口为什么报错"（"就这"后接"个"不是负面）。
var jiuZheRe = regexp.MustCompile(`就这[？！!?。，]|就这$`)

// negativePatterns 是中文负面情绪关键词（失望/反驳/质疑/停止重做）。
var negativePatterns = []string{
	// 失望/不满
	"错了", "没用", "白做", "白费", "浪费", "不行", "太差", "差劲",
	"垃圾", "什么玩意", "失望", "离谱", "莫名其妙", "一塌糊涂", "糊弄",
	// 反驳/纠正方向
	"不是这样", "不是我要的", "我让你", "你理解错了", "搞错了", "想错了",
	"方向不对", "走偏了", "跑偏了", "没按我说的", "说反了",
	// 质疑/批评
	"你怎么搞的", "就这水平", "这也算", "什么逻辑", "越做越差",
	"还不如之前", "能力不行",
	// 停止/重做
	"重新来", "重做", "再来一遍", "推倒重来", "停下", "停一下", "别做了",
	"别改了", "撤销", "回退", "恢复原样",
}

// negativeEnglishPatterns 是英文负面关键词（参考 claude-code 的 userPromptKeywords）。
var negativeEnglishPatterns = []string{
	"this sucks", "not what i asked", "that's wrong", "try again",
	"undo that", "why did you", "wrong direction",
}

// detectNegativeFeedback 检测用户消息是否为负面情绪反馈。
// 纯关键词匹配，零 LLM 成本，每轮用户消息调用一次。
func detectNegativeFeedback(userMsg string) bool {
	msg := strings.TrimSpace(userMsg)
	if msg == "" {
		return false
	}
	if buDuiRe.MatchString(msg) || jiuZheRe.MatchString(msg) {
		return true
	}
	lower := strings.ToLower(msg)
	for _, p := range negativeEnglishPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	for _, p := range negativePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
```

注意：
- `"不对"` **不**放入 `negativePatterns`（否则"如果不对就跳过"会被 `strings.Contains` 命中）——它单独用 `buDuiRe` 正则处理。
- `"就这"` **不**放入 `negativePatterns`（否则"就这个接口为什么报错"会被误命中）——它单独用 `jiuZheRe` 正则处理（后接标点/语气词或行尾），"就这水平"作为复合词保留在列表。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestDetectNegativeFeedback'`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/feedback.go engine/feedback_test.go
git commit -m "feat: add negative-feedback keyword detection (zero LLM cost)"
```

---

### 任务 3：反思注入函数（TDD）

**文件：**
- 修改：`engine/feedback.go`
- 测试：`engine/feedback_test.go`

- [ ] **步骤 1：编写失败的测试**

```go
func TestApplyNegativeFeedbackRewrite_Chinese(t *testing.T) {
	history := []Message{{Role: "user", Content: "不对，这没用"}}
	ok := applyNegativeFeedbackRewrite(history, "不对，这没用", true)
	if !ok {
		t.Fatal("expected rewrite to trigger")
	}
	got := history[len(history)-1].Content
	if !strings.Contains(got, "负面反馈") || !strings.Contains(got, "反思") || !strings.Contains(got, "重新规划") {
		t.Errorf("Chinese rewrite missing reflection guidance: %q", got)
	}
}

func TestApplyNegativeFeedbackRewrite_English(t *testing.T) {
	history := []Message{{Role: "user", Content: "this sucks"}}
	ok := applyNegativeFeedbackRewrite(history, "this sucks", false)
	if !ok {
		t.Fatal("expected rewrite to trigger")
	}
	got := history[len(history)-1].Content
	if !strings.Contains(got, "negative feedback") || !strings.Contains(got, "re-plan") {
		t.Errorf("English rewrite missing reflection guidance: %q", got)
	}
}

func TestApplyNegativeFeedbackRewrite_NonNegativeNoop(t *testing.T) {
	history := []Message{{Role: "user", Content: "帮我加个按钮"}}
	ok := applyNegativeFeedbackRewrite(history, "帮我加个按钮", true)
	if ok {
		t.Error("expected no rewrite for non-negative message")
	}
	if history[0].Content != "帮我加个按钮" {
		t.Errorf("history must stay unchanged, got %q", history[0].Content)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestApplyNegativeFeedbackRewrite'`
预期：FAIL，报错 "undefined: applyNegativeFeedbackRewrite"

- [ ] **步骤 3：追加实现代码到 feedback.go 末尾**

```go
// applyNegativeFeedbackRewrite 在用户负面反馈时改写最后一条 user 消息，
// 注入反思重规划指引，让主 agent 本轮自行反思。返回是否触发了改写。
// 照 pendingEditPlan 反馈路径模式（loop.go 的 pendingEditPlan 分支），零新状态。
func applyNegativeFeedbackRewrite(history []Message, userMsg string, zh bool) bool {
	if !detectNegativeFeedback(userMsg) {
		return false
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return false
	}
	if zh {
		history[len(history)-1].Content = fmt.Sprintf(
			"用户对当前进展给出了负面反馈：%s\n\n请暂停当前执行，反思此前的路径是否正确、是否符合用户需求。找出偏离点，然后重新规划执行路线，向用户说明你的新计划。",
			userMsg)
	} else {
		history[len(history)-1].Content = fmt.Sprintf(
			"The user expressed negative feedback about the current progress: %s\n\nPause the current work, reflect on whether the previous path was correct and matched the user's needs. Identify where it went off track, then re-plan the execution route and explain your new plan to the user.",
			userMsg)
	}
	return true
}
```

`feedback.go` 的 import 需加 `"fmt"`。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestApplyNegativeFeedbackRewrite|TestDetectNegativeFeedback'`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add engine/feedback.go engine/feedback_test.go
git commit -m "feat: add reflection-rewrite injection for negative feedback"
```

---

### 任务 4：删除 IntentClassifier 生产代码 + 接入注入调用

**文件：**
- 修改：`engine/classifier.go`、`engine/types.go`、`engine/loop.go`、`engine/stop_hook.go`、`engine/turn.go`、`context/builder.go`、`cmd/exec.go`、`ui/runner.go`

- [ ] **步骤 1：classifier.go 删除 Intent 相关**

删除 `IntentCheck`(69-72)、`IntentJudge`(77-79)、`IntentClassifier`(83-87)、`NewIntentClassifier`(89-91)、`Classify`(95-120)、`parseIntentJSON`(124-140)、`intentFromString`(142-153)、`pickIntentPrompt`(155-160)、`intentClassifierSystemPromptZh`(162-168)、`intentClassifierSystemPromptEn`(170-176)。

**保留**：`rememberRe`(12)、`extractRememberMarkers`(17-31)、`isIntermediateText`(45-66)。

- [ ] **步骤 2：types.go 删除 UserIntent + AnalysisMode**

删除 `UserIntent` 枚举(8-15)：

```go
// UserIntent classifies the user's intention for the current message,
// used to control analysis-only constraints.
type UserIntent int

const (
	IntentContinue UserIntent = iota // continuing previous task —
	IntentNewTopic                   // new topic, different from previous goal —
	IntentAnalyze                    // analysis/explanation only, no modifications — reset + inject constraint
)
```

删除 `AnalysisMode` 字段(264-269)：

```go
	// AnalysisMode is set when the user's intent is analysis-only. When true,
	// the context builder injects a [ANALYSIS MODE] constraint every turn,
	// persisting across turns (unlike the former pendingPinnedMessages approach
	// which was cleared after the first turn). Cleared when the user confirms
	// the analysis report or starts a new topic.
	AnalysisMode bool `json:"analysis_mode,omitempty"`
```

- [ ] **步骤 3：loop.go 删除字段与辅助方法**

删除 `intentJudge` 字段(143-146)：

```go
	// intentJudge classifies user messages into analyze/continue/new_topic
	// via a lightweight LLM call. Replaces the old keyword-based detection
	// functions. Nil falls back to IntentContinue.
	intentJudge IntentJudge
```

删除 `skillJustActivated` 变量与赋值(457, 464)：

```go
	skillJustActivated := false
	if e.state.ActiveSkillName == "" && e.skillMatcher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		matched := e.skillMatcher.Match(ctx, userMsg, e.skills.All())
		cancel()
		if matched != nil {
			e.activateSkill(matched, "semantic match")
			skillJustActivated = true
		}
	}
```

改为（仅保留匹配逻辑）：

```go
	if e.state.ActiveSkillName == "" && e.skillMatcher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		matched := e.skillMatcher.Match(ctx, userMsg, e.skills.All())
		cancel()
		if matched != nil {
			e.activateSkill(matched, "semantic match")
		}
	}
```

删除 `SetIntentJudge`(1559-1560) 与 `NewIntentClassifier()`(1562-1566)：

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

删除 `detectUserIntent`(1654-1683)：

```go
func (e *Engine) detectUserIntent(ctx context.Context, userMsg string) UserIntent {
	...
}
```

- [ ] **步骤 4：loop.go 替换意图 switch 为负面反馈注入**

将 switch 块(700-731)：

```go
	// Detect user intent: analysis-only vs new-topic vs continue.
	// Resets PlanConfirmed when the user starts a new topic or asks for
	// analysis only, preventing edit-plan-guard bypass across Run() calls.
	// When a skill was just auto-activated via keyword matching, the skill's
	// methodology takes priority over analysis-only classification.
	intent := e.detectUserIntent(ctx, userMsg)
	switch intent {
	case IntentAnalyze:
		...
	case IntentNewTopic:
		...
	default: // IntentContinue
		...
	}
```

替换为：

```go
	// 用户负面反馈：暂停当前路径，反思并重新规划执行路线。
	// 改写 history 最后一条 user 消息（照 pendingEditPlan 反馈路径模式），
	// 让主 agent 本轮自行反思，不引入新状态。纯关键词检测，零 LLM 调用。
	applyNegativeFeedbackRewrite(e.history, userMsg, zh)
```

- [ ] **步骤 5：loop.go 删除所有 AnalysisMode 赋值**

删除以下行（保留相邻逻辑）：
- switch 内的 `e.state.AnalysisMode = true/false`（已在步骤 4 整体删除）
- `teamVerdictPending` 块内 `e.state.AnalysisMode = false`(814)
- `collabVerdictPending` 块内 `e.state.AnalysisMode = false`(823)
- `handleConfirmCommand` 内 `e.state.AnalysisMode = false`(1727)
- `handleAnalysisNudgeConfirmation` 内 `e.state.AnalysisMode = false`(1764)
- `clearSessionState` 内 `e.state.AnalysisMode = false`(1889)

- [ ] **步骤 6：stop_hook.go / turn.go / builder.go / 接线删除**

`stop_hook.go` 删字段(18)：

```go
	AnalysisMode       bool   // true when user intent is analysis-only; text output IS the report
```

`turn.go` 删传参(278)：

```go
			AnalysisMode:       e.state.AnalysisMode,
```

`context/builder.go` 删注入块(168-177)：

```go
	// Analysis mode constraint: when the user's intent is analysis-only, inject
	// the constraint on every Build call so it persists across turns. The former
	// approach used pendingPinnedMessages which was cleared after the first turn.
	// Wording is a stage contract (await user confirmation), NOT an engine-level
	// hard ban — a hard-ban phrasing made agents refuse to edit even after the
	// user confirmed, claiming "the engine forbids modifications".
	if state != nil && state.AnalysisMode {
		constraint := "[ANALYSIS MODE] 本任务处于分析阶段：请先输出分析报告 / 方案，等待用户通过确认选项确认后，再执行修改。"
		if a.userLang != "中文" {
			constraint = "[ANALYSIS MODE] This task is in the analysis stage: present your analysis report / plan first, then wait for the user to confirm via the option popup before making changes."
		}
		messages = append(messages, engine.ModelMessage{Role: "user", Content: constraint})
	}
```

`cmd/exec.go` 删(27)：

```go
	agent.SetIntentJudge(agent.NewIntentClassifier())
```

`ui/runner.go` 删(116)：

```go
		r.eng.SetIntentJudge(r.eng.NewIntentClassifier())
```

- [ ] **步骤 7：编译验证生产代码**

运行：`go build ./...`
预期：仅测试文件报编译错（`AnalysisMode` 等引用），生产代码无错。若生产代码报错说明有遗漏引用，用 `grep -rn "AnalysisMode\|IntentClassifier\|IntentJudge\|detectUserIntent\|UserIntent\|IntentAnalyze\|IntentNewTopic\|IntentContinue" engine/ context/ cmd/ ui/ --include="*.go"` 排查（过滤 `_test.go`）。

- [ ] **步骤 8：Commit**

```bash
git add -u
git commit -m "refactor: remove IntentClassifier and AnalysisMode (saves 1 flash call per turn)"
```

---

### 任务 5：清理测试文件

**文件：**
- 删除：`engine/loop_intent_test.go`
- 修改：`engine/confirm_command_test.go`、`engine/analysis_gate_test.go`、`engine/present_options_test.go`、`engine/present_options_ends_run_test.go`、`context/builder_test.go`

- [ ] **步骤 1：删除 loop_intent_test.go**

`git rm engine/loop_intent_test.go`（全部是 `stubIntentJudge` + `detectUserIntent` 测试）

- [ ] **步骤 2：confirm_command_test.go**

删 `TestDetectUserIntent_ConfirmCommandFastPath`(92-101)：

```go
// detectUserIntent 对 /confirm 前缀走确定性 fast-path，不调用 intentJudge。
func TestDetectUserIntent_ConfirmCommandFastPath(t *testing.T) {
	judge := &stubIntentJudge{intent: IntentAnalyze} // 即使 judge 判 analyze 也不该被调用
	e := &Engine{state: &TaskState{Goal: "g"}, intentJudge: judge}

	if got := e.detectUserIntent(context.Background(), "/confirm 1"); got != IntentContinue {
		t.Errorf("detectUserIntent(/confirm 1) = %v, want IntentContinue", got)
	}
	if judge.called {
		t.Error("intentJudge must NOT be called for /confirm (deterministic channel)")
	}
}
```

`TestHandleConfirmCommand_ConfirmExecutes`(32-61) 与 `TestHandleConfirmCommand_WithOptions_FirstPlanInjected`(63-88)：
- 删 `AnalysisMode:            true,` 字段行
- 删 `if e.state.AnalysisMode { t.Error(...) }` 断言块
- 注释(31) `// /confirm 1 确定性确认：置 AnalysisReportConfirmed、清 AnalysisMode。` → `// /confirm 1 确定性确认：置 AnalysisReportConfirmed。`

`TestConfirmOptions_ReturnedWhenGateIntercepted`(109+) 中 line 144：

```go
		state:    &TaskState{TaskID: "test", ConfirmedScope: true, AnalysisMode: true},
```

→

```go
		state:    &TaskState{TaskID: "test", ConfirmedScope: true},
```

- [ ] **步骤 3：analysis_gate_test.go**

`TestHandleAnalysisNudgeConfirmation` 两个 case：
- line 12 与 33：`&TaskState{AnalysisMode: true, AnalysisReportConfirmed: false}` → `&TaskState{AnalysisReportConfirmed: false}`
- 删断言块(24-25)：

```go
	if e.state.AnalysisMode {
		t.Error("AnalysisMode should be false after confirmation")
	}
```

- [ ] **步骤 4：present_options_test.go**

3 个 `TestHandleConfirmCommand_*`：
- line 159/183/208：`&TaskState{AnalysisMode: true, AnalysisReportConfirmed: false}` → `&TaskState{AnalysisReportConfirmed: false}`
- line 168/192/217：`if e.state.AnalysisMode || !e.state.AnalysisReportConfirmed {` → `if !e.state.AnalysisReportConfirmed {`
- 对应 `t.Error("AnalysisMode should be false / AnalysisReportConfirmed true after ...")` → `t.Error("AnalysisReportConfirmed should be true after ...")`

- [ ] **步骤 5：present_options_ends_run_test.go**

line 48：

```go
		state:     &TaskState{TaskID: "test", ConfirmedScope: true, AnalysisMode: true},
```

→

```go
		state:     &TaskState{TaskID: "test", ConfirmedScope: true},
```

- [ ] **步骤 6：context/builder_test.go**

删 `TestBuild_AnalysisModeConstraint`(260-292) 与 `TestBuild_AnalysisModeConstraint_SoftWording`(296-320) 两个函数（连同它们引用的 `[ANALYSIS MODE]` 断言）。

- [ ] **步骤 7：全量测试验证**

运行：`go test ./engine/ ./context/ ./ui/ ./cmd/`
预期：PASS

- [ ] **步骤 8：最终确认无残留引用**

运行：`grep -rn "AnalysisMode\|IntentClassifier\|IntentJudge\|detectUserIntent\|UserIntent\|IntentAnalyze\|IntentNewTopic\|IntentContinue\|stubIntentJudge" engine/ context/ cmd/ ui/ --include="*.go"`
预期：无匹配（`loop_guard_reset_test.go` 中 line 49 的历史注释保留——仅注释，不是代码引用）

- [ ] **步骤 9：Commit**

```bash
git add -A
git commit -m "test: remove IntentClassifier/AnalysisMode test references"
```

---

## 验证

全部完成后运行：

```bash
go build ./... && go test ./...
```

预期：全绿。

**行为验证（可选手工）**：
- 向 agent 发送"不对，这方向有问题" → 观察 history 注入反思指引（可通过 `go test ./engine/ -run TestApplyNegativeFeedbackRewrite` 确认逻辑）
- 普通消息"帮我加个按钮" → 不触发注入

## 风险与回滚

- 删除 `AnalysisMode` 后 `[ANALYSIS MODE]` 软提醒消失，analysis gate（`turn.go:430`，引擎级硬保护）仍是唯一"先报告后修改"约束。
- 负面关键词误报/漏报：通过防误报规则（`不对` 正则、复合词 `停下`/`别做了`）收敛；漏报靠后续扩充词表迭代。
- 回滚：`git revert` 各 commit 即可恢复。
