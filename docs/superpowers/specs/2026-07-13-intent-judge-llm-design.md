# 用户消息意图分类 LLM 判定 设计文档

## 问题

`Engine.detectUserIntent`（`engine/loop.go:1303`）根据用户消息关键字决定本次 Run 的流程分支：`IntentAnalyze`（注入只读约束，禁止改代码）/ `IntentContinue`（保持 PlanConfirmed，继续当前任务）/ `IntentNewTopic`（重置 PlanConfirmed）。该判定由三个纯关键字列表函数支撑：

- `isAnalysisOnly`（`loop.go:1368`）：分析词 "为什么/怎么/分析/解释/看看..." vs 修改词 "改一下/修复/写个..."
- `hasContextReference`（`loop.go:1345`）：继续词 "刚才/上面/继续/再改/also fix/continue..."
- `isSameTopic`（`loop.go:1450`）：消息与 goal 的关键词重叠（中文 bigram）

根因与 `2026-07-11-stop-hook-llm-conclusion-judgment` 相同：用静态关键字去判定一个语义问题（这条用户消息属于分析/续接/新话题）。中英文同义词永远列不全，边缘 case 误判：

- "帮我看下这个接口为啥报错" 同时命中分析词"看下"和修改词无 -> IntentAnalyze，但用户可能想修。
- "把刚才那个 bug 也修一下" 命中继续词"刚才/也修" -> IntentContinue，正确；但"也优化下性能"不命中任何继续词 -> 误判 IntentNewTopic，PlanConfirmed 被重置，触发多余的方案确认。
- `isSameTopic` 的 bigram 重叠在短消息或抽象目标上失效。

## 方案

把三个语义意图判定函数（`isAnalysisOnly`/`hasContextReference`/`isSameTopic`）替换为一次轻量 LLM 判定，输出 `{"intent":"analyze|continue|new_topic"}`。复用项目已验证的 `ConclusionClassifier` 范式（`classifier.go:261-364`：`ConclusionJudge` 接口 + flash 模型 `Complete` + `JsonMode:true` + `parseConclusionJSON` 兜底解析 + 双语 system prompt）。

`isDangerousConfirmation`（`loop.go:907`）**保留不动**：其注释（`loop.go:905-906`）明确标注"narrow safety gate, exact matches only, safety feature, not fuzzy intent detection"。这是有意做成的确定性安全放行门，用 LLM 会引入"幻觉放行危险命令"的风险。

`detectIntentShift`（`loop.go:1089`，skill 自动 deactivate）**暂不动**：它影响范围独立、语义（开发->使用转换）与意图三分类不同，留作下一轮。

## 设计

### 1. 新增 IntentJudge

独立单元，放 `engine/classifier.go`，与 `ConclusionClassifier` 并列，职责单一：给定"用户目标 + 用户消息"，返回意图分类。

```go
// engine/classifier.go

// IntentCheck bundles the information the judge needs to classify user intent.
type IntentCheck struct {
    Goal    string // current Run's user goal (e.state.Goal)
    Message string // user's latest message
}

// IntentJudge classifies a user message relative to the current goal.
// Interface for testability; *IntentClassifier is the production impl.
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
func (c *IntentClassifier) Classify(ctx context.Context, check IntentCheck) (UserIntent, error)
```

判定器是纯逻辑单元（输入 model/goal/message，输出 intent/err），可独立 stub 测试，不耦合 Engine 内部状态。

### 2. 判定 prompt

复用 `ConclusionClassifier` 范式：`ModelRequest{Model: flashModelName, Messages, Temperature:0, MaxTokens:64, JsonMode:true}`，套 `context.WithTimeout(ctx, 10*time.Second)`。

- **system**（双语，按 `isChinese` 选取）：判定规则——"给定用户当前目标与用户最新消息，判断消息意图属于哪类。analyze=仅要求分析/解释/排查，不修改代码；continue=继续当前目标的已有工作（追加、修改、验证之前的内容）；new_topic=开启与当前目标无关的新任务。只输出 JSON：`{\"intent\":\"analyze|continue|new_topic\"}`。"
- **user**：`目标：{goal}\n\n用户消息：{message}`（英文对应）。
- **输出**：`{"intent":"analyze|continue|new_topic"}`，`JsonMode:true` 强制合法 JSON。
- **解析**：复用 `parseConclusionJSON` 同款兜底（先直接 `json.Unmarshal`，失败则提取首个 `{...}` 子串再解析）。返回 `UserIntent` 枚举值；无法识别的 intent 字符串 -> err。

`goal` 取自 `e.state.Goal`。`detectUserIntent` 已有 `if e.state.Goal == "" { return IntentContinue }` 的前置分支（`loop.go:1309`），Goal 为空时不调判定器，沿用现有行为。

### 3. detectUserIntent 改造

```go
func (e *Engine) detectUserIntent(ctx context.Context, userMsg string) UserIntent {
    if e.state == nil || e.state.Goal == "" {
        return IntentContinue
    }
    msg := strings.ToLower(strings.TrimSpace(userMsg))

    // 确定性安全门：纯确认继续当前任务，不交 LLM。
    if isDangerousConfirmation(msg) {
        return IntentContinue
    }

    // 未注入判定器（测试或接线遗漏）：保守 IntentContinue + 记日志，
    // 与 StalledNarrationHook nil Classifier 处理一致（stop_hook.go:114-116）。
    if e.intentJudge == nil {
        loopLog.Printf("intentJudge not set (wiring bug), falling back to continue")
        return IntentContinue
    }

    // LLM 判定
    intent, err := e.intentJudge.Classify(ctx, IntentCheck{Goal: e.state.Goal, Message: userMsg})
    if err != nil {
        loopLog.Printf("intent classify error: %v (conservative fallback to continue)", err)
        return IntentContinue // 保守兜底：不误判为新话题导致 PlanConfirmed 被重置
    }
    return intent
}
```

调用点 `loop.go:608` 改为 `e.detectUserIntent(ctx, userMsg)`，传入 `ctx`。`detectUserIntent` 签名加 `ctx context.Context`（首个参数）。

### 4. Engine 持有 intentJudge + 构造

`Engine` 新增字段 `intentJudge IntentJudge`。构造方式与 `ConclusionClassifier` 一致——由调用方注入，便于测试 stub：

```go
// engine/loop.go
func (e *Engine) NewIntentClassifier() *IntentClassifier {
    return NewIntentClassifier(e.model, e.config.FlashModelName, e.isChinese)
}
```

`cmd/exec.go` 注册时注入（与 `NewConclusionClassifier()`/`SetStopHooks` 同处）：

```go
agent.SetIntentJudge(agent.NewIntentClassifier())
```

`ui/runner.go:90`（`SetStopHooks` 同处）也注入。新增 setter（`engine/loop.go`）：

```go
func (e *Engine) SetIntentJudge(j IntentJudge) { e.intentJudge = j }
```

`detectUserIntent` 对 nil `intentJudge` 做守卫（见上节），接线遗漏不崩溃。测试时用 stub `IntentJudge`，不依赖真实模型。

### 5. 数据流

```
用户消息进入 Run()  loop.go:603
  └─ detectUserIntent(ctx, userMsg):
       Goal == ""                                  -> IntentContinue（不变）
       isDangerousConfirmation(msg)                -> IntentContinue（保留确定性安全门）
       intentJudge == nil（接线遗漏）               -> IntentContinue + 记日志
       intentJudge.Classify(ctx, {Goal, msg}):
         IntentAnalyze                             -> 注入 [ANALYSIS MODE] 只读约束
         IntentContinue                            -> 保持 PlanConfirmed
         IntentNewTopic                            -> 重置 PlanConfirmed
         err/超时                                   -> 保守 IntentContinue（不重置 PlanConfirmed）
```

## 错误处理与兜底

- **LLM 调用失败**（网络/超时/JSON 解析失败/intent 字符串无法识别）-> 保守返回 `IntentContinue` + 记日志。对冲"误判为新话题导致 PlanConfirmed 被重置、触发多余方案确认"——宁可保持现状也不误重置。
- **成本/延迟**：判定在每条用户消息的 agent loop 启动前同步执行，套 10s 超时。flash 模型 + 短 prompt + `max_tokens=64`，单次成本与 `ConclusionClassifier`/compressor 同档；agent 单个 turn 本身有多次 LLM 调用，多一次 flash 调用可忽略。`ConclusionClassifier` 设计文档用了同样的成本论证。
- **Goal 为空**：`detectUserIntent` 前置分支已处理（`loop.go:1309`），不调判定器，沿用 `IntentContinue`。

## 测试策略

- **`IntentClassifier` 单元测试**（stub `ModelClient`）：
  - 返回 `{"intent":"analyze"}` -> `IntentAnalyze`；`continue`/`new_topic` 对应
  - 非法 JSON / 调用出错 / 无法识别的 intent 字符串 -> 返回 error
  - 断言请求 prompt 含 `goal` 与 `message`、`Model` 为 flashModelName、`JsonMode=true`、`MaxTokens=64`
  - markdown 包裹 / 前后缀文本兜底解析（仿 `TestConclusionClassifier_ParsesNonPureJSON`）
- **`detectUserIntent` 改造测试**（stub `IntentJudge`）：
  - `Goal==""` -> `IntentContinue`（不调 judge）
  - `isDangerousConfirmation` 命中 -> `IntentContinue`（不调 judge）
  - judge 返回各 intent -> 对应分支
  - judge error -> 保守 `IntentContinue`
- **删除** `isAnalysisOnly`/`hasContextReference`/`isSameTopic`/`extractKeyTerms`/`isCJK`（仅意图分类用）及相关测试用例（`loop_intent_test.go` 中 `TestIsAnalysisOnly`/`TestHasContextReference`/`TestIsSameTopic`）。
- **保留** `isDangerousConfirmation`/`isSingleConfirmWord`/`isConcatOfConfirmWords` 及其测试（安全门，不动）。

## 文件改动清单

| 文件 | 改动 |
|---|---|
| `engine/classifier.go` | 新增 `IntentCheck`/`IntentJudge`/`IntentClassifier` + 双语 system prompt + `parseIntentJSON`；保留 `ConclusionClassifier` 及 stop-hook 相关启发式 |
| `engine/loop.go` | `detectUserIntent` 加 `ctx` 参数、改调 `intentJudge.Classify`；删 `isAnalysisOnly`/`hasContextReference`/`isSameTopic`/`extractKeyTerms`/`isCJK`；保留 `isDangerousConfirmation` 系列；`Engine` 加 `intentJudge` 字段 + `NewIntentClassifier()`；调用点 `loop.go:608` 传 `ctx` |
| `cmd/exec.go` | 构造并注入 `IntentClassifier`（与 `NewConclusionClassifier()` 同处） |
| `engine/loop_intent_test.go` | 删 `isAnalysisOnly`/`hasContextReference`/`isSameTopic` 用例；新增 `detectUserIntent` stub-judge 用例 |
| 新增 `engine/intent_classifier_test.go` | `IntentClassifier` 单元测试 |

## 范围边界

本次只替换组 2 中三个语义意图判定函数（`isAnalysisOnly`/`hasContextReference`/`isSameTopic`）为 LLM 判定。以下不在本次范围：

- `isDangerousConfirmation`（确定性安全门，保留）。
- `detectIntentShift`（skill 自动 deactivate，语义独立，下一轮）。
- 组 1 stop-hook 关键字前置层（`hasFutureIntent`/`hasTrailingNextStepIntent` 等，已有 LLM 兜底，不动）。
- 组 3 `roundtable.handleVerdict`（"再辩/继续"）、组 4 `parseScoreFromText`（另开一轮）。
