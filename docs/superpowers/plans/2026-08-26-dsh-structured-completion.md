# 结束判定全面吸收 dsh 结构化逻辑（退役 verdict classifier）— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Commit 策略：** 用户明确要求**改完不要 commit**，由用户手动 commit。每个 Task 以"验证通过"收尾，不执行 `git commit`。

**Goal:** 退役主引擎的 verdict classifier（LLM 三分类判定），全面吸收 dsh 结构化判定逻辑：纯文本（无工具调用）即完成，完成由 `task_complete` 显式信号声明，StopHook 框架保留但默认不注册内建 hook。子代理保留 `ConclusionClassifier`（无用户交互需兜底）。

**Architecture:** `engine/classifier.go` 删除 `VerdictJudge`/`TextVerdict`/`Classify` 等三分类路径（保留 `ConclusionClassifier`/`IsConclusion` 供子代理）；`engine/stop_hook.go` 删除 `ZeroToolCallHook`/`StalledNarrationHook` 内建 hook（保留框架 `StopHook`/`runStopHooks`/`SetStopHooks`）；`engine/turn.go` 删除自问自答守卫；`cmd/exec.go`/`ui/runner.go` 不再注册 hook；`engine/sub_agent.go` 删除 `hasTrailingNextStepIntent` 二次关键词检查，只信 LLM 判定。

**Tech Stack:** Go 1.24, DeepSeek API, 现有 StopHook 接口框架

## Global Constraints

- **不 commit**（用户手动 commit）。
- 保留 `ConclusionClassifier`/`IsConclusion`/`parseConclusionJSON`/`conclusionClassifierSystemPromptZh/En`/`ConclusionCheck`（**子代理仍用**）。
- 保留 `StopHook` 框架：`StopHook` 接口、`StopHookContext`（含 `Goal`/`ToolCallSummary`/`AnalysisMode` 字段）、`StopHookResult`（含 `Block`/`Exhausted`/`AwaitUser`）、`runStopHooks`、`SetStopHooks`。
- 保留 `IntentClassifier`（用户意图 → AnalysisMode，非结束判定，不动）。
- 保留 `isIntermediateText`（`turn.go:221` Layer 3 噪声清理 + `sub_agent.go:335`，非结束判定）。
- 保留 `task_complete` 拦截（turn.go:395）与 loop guard / error loop / read loop。
- 删除必须同步清理测试文件引用，每个 Task 结束时 `go build ./...` 通过、目标测试通过。

---

### Task 1: 退役 VerdictJudge 三分类判定（含自问自答守卫）

**Files:**
- Modify: `engine/classifier.go`
- Modify: `engine/loop.go`
- Modify: `engine/turn.go`
- Delete: `engine/verdict_classifier_test.go`、`engine/stop_hook_await_test.go`
- Modify: `engine/stalled_narration_test.go`（移除 verdict 相关测试，Task 2 彻底重写）

**Interfaces:**
- Removes: `VerdictJudge`、`TextVerdict`、`VerdictConclusion`/`VerdictQuestion`/`VerdictIntermediate`、`TextVerdict.String()`、`(*ConclusionClassifier).Classify`、`parseVerdictJSON`、`verdictFromString`、`pickVerdictPrompt`、`verdictClassifierSystemPromptZh/En`、`ConclusionJudge` 接口、`buildToolCallSummary`
- Removes: `Engine.verdictJudge` 字段（loop.go:134）
- Removes: 自问自答守卫（turn.go:349-393）
- Keeps: `ConclusionClassifier`/`IsConclusion`/`parseConclusionJSON`/`conclusionClassifierSystemPromptZh/En`/`pickClassifierPrompt`/`ConclusionCheck`

- [ ] **Step 1: 删除 classifier.go 的 verdict 路径**

在 `engine/classifier.go` 删除：
- `ConclusionJudge` 接口（204-206）
- `TextVerdict` 类型 + 常量 + `String()`（301-321）
- `VerdictJudge` 接口（323-330）
- `(*ConclusionClassifier).Classify`（336-373）
- `parseVerdictJSON`（377-393）、`verdictFromString`（395-406）、`pickVerdictPrompt`（408-413）
- `verdictClassifierSystemPromptZh`（415）、`verdictClassifierSystemPromptEn`（423）
- `buildToolCallSummary`（560-588）

保留：`rememberRe`/`extractRememberMarkers`、`isIntermediateText`、`ConclusionCheck`、`ConclusionClassifier`+`NewConclusionClassifier`+`IsConclusion`+`parseConclusionJSON`+`pickClassifierPrompt`+`conclusionClassifierSystemPromptZh/En`、`IntentClassifier` 全部、`hasTrailingNextStepIntent`/`lastSentence`/`isSentDelim`（Task 3 才删，sub_agent.go:370 仍用）。

- [ ] **Step 2: 删除 loop.go 的 verdictJudge 字段**

在 `engine/loop.go` 删除字段 `verdictJudge VerdictJudge`（128-134 注释 + 字段）。

- [ ] **Step 3: 删除 turn.go 自问自答守卫 + ToolCallSummary 填充**

在 `engine/turn.go`：
- 删除自问自答守卫整块（349-393，`if e.verdictJudge != nil { ... }`）。
- 删除 `buildToolCallSummary` 调用（362，已随守卫删除）。
- 删除 `runStopHooks` 参数中的 `ToolCallSummary: buildToolCallSummary(...)`（266）——函数已删，字段留零值。

- [ ] **Step 4: 删除 verdict 相关测试文件**

删除 `engine/verdict_classifier_test.go`（Classify 测试）、`engine/stop_hook_await_test.go`（AwaitUser 判定测试 + 集成测试引用 verdictJudge 字段）。

在 `engine/stalled_narration_test.go` 删除引用 `Verdict*`/`stubVerdictJudge` 的测试与定义（Task 2 会彻底重写该文件）。

- [ ] **Step 5: 验证编译 + 保留测试**

```bash
go build ./...
go test ./engine/ -run 'TestConclusionClassifier|TestIntentClassifier|TestSubAgent|TestRunStopHooks|TestSetStopHooks' -count=1
```

Expected: 编译 PASS；conclusion/intent/sub-agent/stop-hook 框架测试 PASS。`TestStalledNarrationHook_*` 可能仍存在（Task 2 重写）。

---

### Task 2: 删除主引擎内建 hook + 注册，重写框架测试

**Files:**
- Modify: `engine/stop_hook.go`
- Modify: `engine/turn.go`（删 orphan `truncateStr`）
- Modify: `cmd/exec.go`、`ui/runner.go`（移除注册段）
- Rewrite: `engine/stalled_narration_test.go`、`engine/stop_hook_test.go`

**Interfaces:**
- Removes: `ZeroToolCallHook`（struct+Check）、`StalledNarrationHook`（struct+Check）、`stalledNudgeMsg`、`(*Engine).NewConclusionClassifier`、`SetStopHooks` 的 verdict 提取 type-switch（247-256）
- Removes: `truncateStr`（turn.go:1519，仅被两个删除的 nudge 函数用）
- Keeps: `StopHook` 接口、`StopHookContext`、`StopHookResult`、`runStopHooks`、`SetStopHooks`（注册入口）

- [ ] **Step 1: 删除 stop_hook.go 内建 hook**

在 `engine/stop_hook.go` 删除：
- `ZeroToolCallHook` struct + `Check`（54-113）
- `StalledNarrationHook` struct + `Check`（123-220）
- `stalledNudgeMsg`（224-237）
- `(*Engine).NewConclusionClassifier`（263-265）
- `SetStopHooks` 中的 type-switch（247-256），简化为直接赋值 `e.stopHooks = hooks`

保留：`StopHookContext`/`StopHookResult`/`StopHook`/`runStopHooks`/`SetStopHooks`/`turnLog` 引用。

- [ ] **Step 2: 删除 turn.go orphan truncateStr**

删除 `engine/turn.go` 的 `truncateStr` 函数（1517-1525）。确认无其他引用（仅 stop_hook.go 用过）。

- [ ] **Step 3: 移除 cmd/exec.go 与 ui/runner.go 注册段**

`cmd/exec.go:26-33` 删除：
```go
verdict := agent.NewConclusionClassifier()
agent.SetStopHooks([]engine.StopHook{
    &engine.ZeroToolCallHook{MaxRetries: 5, Verdict: verdict},
    &engine.StalledNarrationHook{MaxRetries: 4, Classifier: verdict, Verdict: verdict},
})
```
替换为注释：`// 无内建 stop hook：纯文本即结束（dsh 化）。框架保留供未来扩展。`

`ui/runner.go:96-104` 同理删除注册段。

- [ ] **Step 4: 重写 stalled_narration_test.go 为框架级测试**

将 `engine/stalled_narration_test.go` 重写为：
- 移除所有 `StalledNarrationHook`/`ZeroToolCallHook`/`Verdict`/`stubClassifier`/`stubVerdictJudge` 测试与定义。
- 新增框架级集成测试：`TestExecuteTurn_NoStopHooks_TextOnlyDone` —— 无 hook 注册时，模型输出任意纯文本（含"让我先看看"）→ `executeTurn` 返回 `Done=true`。复用现有 `stubStreamModel`/`stubContextBuilder`/`stubToolExecutor` 模式（turn_test.go:57-78）。
- `errBoom` 若被其他保留测试引用，保留其定义（grep 确认）。

- [ ] **Step 5: 重写 stop_hook_test.go 为框架测试**

`engine/stop_hook_test.go` 移除 `TestZeroToolCallHook_*`（6 个）。保留并改写：
- `TestRunStopHooks_FirstBlockingResult`：改用自定义 stub hook（实现 `StopHook.Check` 返回 Block）。
- `TestRunStopHooks_NoHooksRegistered`：不变（无 hook → Block=false）。
- `TestRunStopHooks_HookPassesThrough`：改用自定义 stub hook 返回空结果。
- `TestSetStopHooks`：改用自定义 stub hook。

新增小型 stub：
```go
type stubStopHook struct{ result StopHookResult }
func (s stubStopHook) Check(context.Context, StopHookContext) StopHookResult { return s.result }
```

- [ ] **Step 6: 验证编译 + 框架测试**

```bash
go build ./...
go test ./engine/ -run 'TestExecuteTurn_NoStopHooks|TestRunStopHooks|TestSetStopHooks|TestConclusionClassifier|TestSubAgent' -count=1
```

Expected: 全部 PASS。

---

### Task 3: 子代理去关键词守卫 + 删除 hasTrailingNextStepIntent

**Files:**
- Modify: `engine/sub_agent.go`
- Modify: `engine/classifier.go`
- Modify: `engine/sub_agent_terminate_test.go`
- Modify: `engine/classifier_test.go`

**Interfaces:**
- Removes: `sub_agent.go:370` 的 `!hasTrailingNextStepIntent(msg.Content)` 二次检查
- Removes: `hasTrailingNextStepIntent`（classifier.go:55-117）、`lastSentence`（123-139）、`isSentDelim`（144-157）——删除后无使用者
- Keeps: 子代理 `conclusionClassifier.IsConclusion` + critic VERDICT fast-path + classifier error 保守 nudge + `consecutiveIntermediate >= 3` 兜底

- [ ] **Step 1: 删除 sub_agent.go 关键词守卫**

在 `engine/sub_agent.go:370`，将：
```go
} else if isConc && !hasTrailingNextStepIntent(msg.Content) {
```
改为：
```go
} else if isConc {
```
同步更新注释（不再提"trailing next-step intent"守卫）。

- [ ] **Step 2: 删除 classifier.go 的 hasTrailingNextStepIntent 及依赖**

删除 `hasTrailingNextStepIntent`、`lastSentence`、`isSentDelim`（grep 确认无剩余引用）。

- [ ] **Step 3: 更新子代理终止测试**

`engine/sub_agent_terminate_test.go`：
- 删除 `TestSubAgentRunLoop_NudgesOnNextStepNarrationDespiteClassifierFalsePositive`（191-222）——该测试专门验证"classifier 误判 true 时 `hasTrailingNextStepIntent` 守卫仍 nudge"，守卫删除后场景不存在。
- 保留其余测试（`TestSubAgentRunLoop_TerminatesOnConclusionNotLoop`、`TestSubAgentRunLoop_CriticVerdictBypassesClassifier`、`TestSubAgentRunLoop_NudgesOnNextStepNarration`）。

- [ ] **Step 4: 更新 classifier_test.go**

`engine/classifier_test.go` 删除 `hasTrailingNextStepIntent` 相关测试（144 附近）。保留 `isIntermediateText` 测试（97 附近，函数保留）与其余。

- [ ] **Step 5: 验证编译 + 子代理测试**

```bash
go build ./...
go test ./engine/ -run 'TestSubAgent|TestConclusionClassifier|TestIsIntermediateText' -count=1
```

Expected: 全部 PASS。

---

### Task 4: 全量回归 + Self-Review

- [ ] **Step 1: 全量测试**

```bash
go build ./...
go test ./... -count=1
```

Expected: 全部 PASS。若 `ui/` 测试因未提交的既有改动失败（用户已确认存在 ui/* 改动），报告但区分"既有失败"与"本次引入失败"。

- [ ] **Step 2: 孤儿扫描**

```bash
grep -rn "verdictJudge\|ZeroToolCallHook\|StalledNarrationHook\|VerdictJudge\|TextVerdict\|stalledNudgeMsg\|hasTrailingNextStepIntent\|buildToolCallSummary\|truncateStr" engine/ cmd/ ui/ | grep -v _test.go
```

Expected: 无输出（全删干净）。`conclusionClassifier`/`IsConclusion`/`ConclusionCheck` 允许出现（子代理用）。

- [ ] **Step 3: 对照设计文档 Self-Review**

逐项核对 `docs/superpowers/specs/2026-08-26-dsh-structured-completion-design.md` 的退役清单与保留清单。

---

## Self-Review

### 1. Spec coverage

| Spec 要求 | 实现任务 |
|-----------|----------|
| 删除 VerdictJudge/TextVerdict/Classify/verdict prompts | Task 1 |
| 删除自问自答守卫 + verdictJudge 字段 | Task 1 |
| 删除 ZeroToolCallHook/StalledNarrationHook/stalledNudgeMsg/Engine.NewConclusionClassifier | Task 2 |
| StopHook 框架保留（接口/Context/Result/runStopHooks/SetStopHooks） | Task 2（保留） |
| cmd/exec.go + ui/runner.go 不注册 hook | Task 2 |
| 子代理保留 conclusionClassifier + 去 hasTrailingNextStepIntent 关键词 | Task 3 |
| 删除 hasTrailingNextStepIntent/lastSentence/isSentDelim | Task 3 |
| 保留 ConclusionClassifier/IsConclusion（子代理）/IntentClassifier/isIntermediateText/task_complete/AnalysisMode | Task 1-4（保留） |
| 测试：删 verdict_classifier_test/stop_hook_await_test；重写 stalled_narration_test/stop_hook_test；保留 conclusion_classifier_test/sub_agent_terminate_test | Task 1-3 |

✅ 全部覆盖。

### 2. Placeholder scan

✅ 无 TBD/TODO。所有删除/保留清单引用精确符号名与文件。Task 2 Step 4-5 的框架测试新增 stub 代码完整。

### 3. Type consistency

- `VerdictJudge`/`TextVerdict`/`Verdict*` 常量 → Task 1 删，所有引用（loop.go 字段、turn.go 守卫、stop_hook.go hook、verdict_classifier_test、stop_hook_await_test、stalled_narration_test 相关测试）同步清理 ✅
- `ConclusionClassifier`/`IsConclusion`/`ConclusionCheck` → 保留，sub_agent.go:364 继续用 ✅
- `buildToolCallSummary` → Task 1 删，turn.go:266/362 同步清理 ✅
- `truncateStr` → Task 2 删（仅被两个删除的 nudge 函数用）✅
- `hasTrailingNextStepIntent`/`lastSentence`/`isSentDelim` → Task 3 删（sub_agent.go:370 先去守卫）✅
- `StopHook` 框架 → 保留，stop_hook_test.go 用自定义 stub hook 测试 ✅

### 4. 行为验证点（改后人工验证）

1. 模型输出纯文本"让我先看看" → 回合直接结束，文本展示给用户（不再 nudge）。
2. 模型调用 `task_complete` → 立即完成（不变）。
3. 模型提问"选 A 还是 B？" → 普通结束，无 AwaitUser 拦截。
4. 子代理输出纯文本 → 仍走 conclusionClassifier 判定（LLM），非结论则 nudge。
