# 子代理输出契约 + 结构化完成判定 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 强化子代理能力——(1) 用输出契约重写子代理系统提示，让角色描述给足； (2) 让 critic 也走 `submit_result` 唯一完成路径，消灭"需要人为输入：继续"的非结构化 LLM 判断分支。

**架构：** 三个文件协同：重写 `context/promptset/zh/sub_agent.md` 加入"输出契约"章节（学 pi 的 agent 输出格式约定 + harness `report` 工具提示词）；`engine/default_agents.go` 给 critic 的 `AgentSpec` 加 `StructuredResult: true` 并让 `specialistAgent.Run` 透传该标志；`engine/sub_agent.go` 在结构化 nudge 分支为 critic 注入"提交 VERDICT"引导。完成判定完全依赖确定性工具调用（`submit_result`），不再依赖 `ConclusionClassifier` 的 LLM 判断。

**技术栈：** Go 1.x，engine 包（`runLoop` / `AgentSpec` / `SubmitResultToolName`），context/promptset 嵌入式提示文件。

**⚠️ 执行前注意：** 当前工作树已有未提交改动（`git status` 显示 `engine/default_agents.go`、`engine/sub_agent.go`、`engine/turn.go` 等 29 个文件被修改，系此前 dsh-structured-completion 相关工作的进行中状态）。任务 2 对 `engine/default_agents.go` 的修改会叠加在该状态之上——先 `git diff engine/default_agents.go` 确认现有内容，再应用本计划的改动，避免覆盖他人工作。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `context/promptset/zh/sub_agent.md` | 子代理系统提示（12 行 → 输出契约） | 修改（整体替换正文） |
| `engine/default_agents.go` | agent 注册；critic 结构化标志；specialistAgent 透传 | 修改（2 处） |
| `engine/sub_agent.go` | runLoop；结构化 nudge 分支 + 新 nudge 函数 | 修改（2 处） |
| `engine/sub_agent_finish_test.go` | 新增 critic 结构化提交测试 | 修改（追加） |
| `engine/sub_agent_terminate_test.go` | 新增 critic 结构化 nudge 测试 | 修改（追加） |

---

### 任务 1：重写子代理系统提示，加入输出契约

**文件：**
- 修改：`context/promptset/zh/sub_agent.md`（全部替换现有 12 行内容）

- [ ] **步骤 1：编写替换后的提示内容**

用 `write` 工具将以下内容整体写入 `context/promptset/zh/sub_agent.md`（该文件现有内容只有 12 行，直接覆盖即可）：

```markdown
你是一个执行委派任务的子代理。完成目标并报告你的发现。

## 工作方式
- 先理解目标与约束，再动手。
- 不要只描述计划——直接使用工具执行。信息不足先调查，调查完立即行动。
- 完成所有必要的探索和修改后，明确宣告完成。

## 搜索方法论（代码阅读协议）
- **意图优先**：使用 grep/glob 配合任务相关关键词缩小范围。不要 glob 所有文件。
- **LSP 先于 Read**：使用 'lsp workspaceSymbol' 按名称查找函数/类型定义；使用 'lsp hover'/'goToDefinition' 获取类型信息。比 grep+Read 更精确。
- **精确读取**：一旦知道需要哪个文件，只读取相关的符号/区域。
- **总结大输出**：如果工具返回 >50 条匹配或 >10KB，总结关键发现而不是全部倾倒。
- **批量并行读取**：当需要检查多个独立文件时，在一个回合中批量处理。
- **追踪代码**：跟踪函数调用和类型引用来建立理解，而不是列出文件。

## 输出契约（必须遵守）
委托你的父代理**看不到你的完整对话记录、工具输出和推理过程**——它只收到你最终的总结。因此：
1. **输出必须自包含**：不要写"如你所见""见上方""done"这类依赖上下文的短语。
2. **结构化格式**：按以下小节组织最终输出：
   - 完成了什么（What was done）
   - 关键发现/改动，附 文件:行号 证据（Key findings with file:line evidence）
   - 遗留问题或需要父代理注意的事项（Notes）
3. **完成判定**：只有当你真正完成目标（必要的探索和修改都已做完）才宣告完成。若任务因外部限制无法完成，明确说明卡在哪里、需要父代理做什么。

你可以使用 'handoff_to_agent' 工具委派子任务。
```

- [ ] **步骤 2：验证构建**

运行：`go build ./...`
预期：无错误（promptset 无单测；此改动为纯文本，由后续 runLoop 测试回归）

- [ ] **步骤 3：Commit**

```bash
git add context/promptset/zh/sub_agent.md
git commit -m "feat(sub-agent): add output contract to sub-agent system prompt"
```

---

### 任务 2：critic 走结构化完成

**文件：**
- 修改：`engine/default_agents.go:19`（critic spec 加 `StructuredResult: true`）
- 修改：`engine/default_agents.go:62-68`（`specialistAgent.Run` 透传 `StructuredResult`）
- 测试：`engine/sub_agent_finish_test.go`（追加测试函数）

- [ ] **步骤 1：编写失败的测试**

在 `engine/sub_agent_finish_test.go` 末尾追加：

```go
// TestSpecialistAgent_CriticStructuredSubmitVerdict: critic 走结构化后，必须
// 通过 submit_result 提交含 VERDICT 的结论，父代理可从 digest 解析出 FAIL。
func TestSpecialistAgent_CriticStructuredSubmitVerdict(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{
			Message: ModelMessage{Role: "assistant", ToolCalls: []ModelToolCall{{
				ID: "s1", Type: "function",
				Function: ModelFunctionCall{Name: SubmitResultToolName,
					Arguments: `{"summary":"发现两处问题\n\nVERDICT: FAIL","conclusions":["a.go:10"]}`},
			}}},
			FinishReason: "tool_calls",
		},
	}}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	agent := &specialistAgent{
		id:       AgentCritic,
		spec:     AgentSpec{ID: AgentCritic, ToolNames: []string{"read", "grep", "glob", "lsp"}, StructuredResult: true},
		promptEn: criticPromptEn,
		promptZh: criticPromptZh,
		runner:   runner,
	}
	result, err := agent.Run(context.Background(), Handoff{Agent: AgentCritic, Goal: "验证实现", MaxIterations: 5})
	if err != nil {
		t.Fatalf("specialistAgent.Run error: %v", err)
	}
	if model.calls != 1 {
		t.Errorf("expected 1 call (terminate on submit_result), got %d", model.calls)
	}
	if result.FinishReason != HandoffReasonCompleted {
		t.Errorf("expected FinishReason=completed, got %q", result.FinishReason)
	}
	if !strings.Contains(result.Summary, "VERDICT: FAIL") {
		t.Errorf("expected VERDICT: FAIL in summary, got %q", result.Summary)
	}
	if !toolsContain(model.lastReq.Tools, SubmitResultToolName) {
		t.Errorf("expected submit_result in tools for structured critic, got %+v", model.lastReq.Tools)
	}
}
```

注意：`engine/sub_agent_finish_test.go` 已 `import "strings"`（现有测试 `TestSubAgentStructured_TextOnlyNeverCompletes` 使用过 `strings.Contains`），无需新增 import。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestSpecialistAgent_CriticStructuredSubmitVerdict -v`
预期：FAIL——当前 `specialistAgent.Run`（`default_agents.go:62-68`）不设置 `input.StructuredResult`，run 不会在 submit_result 处终止，`model.calls` 会超过 1，且 `lastReq.Tools` 不含 submit_result。

- [ ] **步骤 3：实现最少代码**

先运行 `git diff engine/default_agents.go` 确认现有未提交改动，再应用两处改动。

改动 A——critic spec 加结构化标志（`default_agents.go:19`）：

```go
spec: AgentSpec{ID: AgentCritic, Description: "Adversarial verification — try to break the implementation before claiming completion", ToolNames: []string{"read", "grep", "glob", "lsp"}, ModelName: "flash", MaxIterations: 15, StructuredResult: true},
```

改动 B——`specialistAgent.Run` 透传标志（`default_agents.go:62-68`）：

```go
func (a *specialistAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
	maxIter := a.spec.MaxIterations
	if maxIter <= 0 {
		maxIter = maxSubAgentIterations
	}
	input.StructuredResult = a.spec.StructuredResult
	return a.runner.runLoop(ctx, input, a.promptFor(zhFromLang(input.UserLanguage)), maxIter, a.spec.ModelName)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestSpecialistAgent_CriticStructuredSubmitVerdict -v`
预期：PASS（`model.calls == 1`，`FinishReason=completed`，`Summary` 含 `VERDICT: FAIL`，`lastReq.Tools` 含 submit_result）

- [ ] **步骤 5：Commit**

```bash
git add engine/default_agents.go engine/sub_agent_finish_test.go
git commit -m "fix(sub-agent): critic uses structured submit_result completion"
```

---

### 任务 3：critic 结构化 nudge 引导 VERDICT

**文件：**
- 修改：`engine/sub_agent.go:401-404`（结构化 text-only 分支对 critic 用专用 nudge）
- 修改：`engine/sub_agent.go`（在 `getSubmitResultNudge` 附近新增 `getCriticSubmitNudge`）
- 测试：`engine/sub_agent_terminate_test.go`（追加测试函数）

- [ ] **步骤 1：编写失败的测试**

在 `engine/sub_agent_terminate_test.go` 末尾追加：

```go
// TestSubAgentStructured_CriticNudgeNamesVerdict: 结构化 critic 在 text-only
// 轮次收到引导 VERDICT 的 nudge，3-strike 后以 no_result 结束（不误判为完成）。
func TestSubAgentStructured_CriticNudgeNamesVerdict(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "我继续分析。"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: stubToolExecutor{}, modelName: "test"}
	result, err := runner.Run(context.Background(), Handoff{
		Agent: AgentCritic, Goal: "验证实现", MaxIterations: 8, StructuredResult: true,
	})
	if err != nil {
		t.Fatalf("runLoop error: %v", err)
	}
	if model.calls != 3 {
		t.Errorf("expected 3 calls (3-strike), got %d", model.calls)
	}
	if model.classifierCalls != 0 {
		t.Errorf("expected classifier never probed in structured mode, got %d", model.classifierCalls)
	}
	if result.FinishReason != HandoffReasonNoResult {
		t.Errorf("expected no_result, got %q", result.FinishReason)
	}
	last := model.lastReq.Messages[len(model.lastReq.Messages)-1].Content
	if !strings.Contains(last, "VERDICT") {
		t.Errorf("expected nudge to name VERDICT, last message=%q", last)
	}
}
```

`engine/sub_agent_terminate_test.go` 已 `import "strings"`（现有测试使用过）。`stubSeqModel`、`stubToolExecutor`、`HandoffReasonNoResult` 均已在引擎包内定义。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run TestSubAgentStructured_CriticNudgeNamesVerdict -v`
预期：FAIL——当前结构化分支（`sub_agent.go:401-404`）对 critic 也发通用 `getSubmitResultNudge`，不含 "VERDICT"，断言 `strings.Contains(last, "VERDICT")` 失败。

- [ ] **步骤 3：实现最少代码**

改动 A——结构化 text-only 分支（`sub_agent.go:401-404`），将：

```go
			history = append(history, ModelMessage{
				Role:    "user",
				Content: getSubmitResultNudge(zhFromLang(input.UserLanguage)),
			})
```

改为：

```go
			content := getSubmitResultNudge(zhFromLang(input.UserLanguage))
			if input.Agent == AgentCritic {
				content = getCriticSubmitNudge(zhFromLang(input.UserLanguage))
			}
			history = append(history, ModelMessage{
				Role:    "user",
				Content: content,
			})
```

改动 B——在 `getSubmitResultNudge`（`sub_agent.go:627`）函数后新增：

```go
// getCriticSubmitNudge directs a structured critic's text-only turn to the
// terminal tool with a verdict-shaped summary, so the FAIL gate stays reliable.
func getCriticSubmitNudge(zh bool) string {
	if zh {
		return "评审完成。请立即调用 submit_result 提交最终评审结论，summary 必须包含一行 VERDICT: PASS / VERDICT: FAIL / VERDICT: PARTIAL。不要继续输出纯文本或调用其他工具。"
	}
	return "Review complete. Call submit_result now to submit your final review, with the summary containing one line: VERDICT: PASS, VERDICT: FAIL, or VERDICT: PARTIAL. Do not continue with plain text or further tool calls."
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run TestSubAgentStructured_CriticNudgeNamesVerdict -v`
预期：PASS（`model.calls == 3`，`FinishReason=no_result`，最后一条消息含 `VERDICT`）

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent.go engine/sub_agent_terminate_test.go
git commit -m "fix(sub-agent): structured critic nudge directs verdict-shaped submit"
```

---

### 任务 4：回归验证

- [ ] **步骤 1：跑相关子集**

运行：
```bash
go test ./engine/ -run 'TestSpecialistAgent_CriticStructuredSubmitVerdict|TestSubAgentStructured_CriticNudgeNamesVerdict|TestSubAgentRunLoop_CriticVerdictBypassesClassifier|TestSubAgentRunLoop_TerminatesOnConclusionNotLoop|TestSubAgentRunLoop_NudgesOnNextStepNarration|TestSubAgentStructured_SubmitsResult|TestSubAgentStructured_TextOnlyNeverCompletes|TestSubAgentStructured_InvalidSubmitRetries' -v
```
预期：全部 PASS。既有 critic 测试直接调 `runner.Run()`，`Handoff.StructuredResult` 默认 false → 走非结构化路径，不受任务 2/3 影响，仍 PASS。

- [ ] **步骤 2：跑 engine 全量**

运行：`go test ./engine/ -count=1`
预期：全部 PASS

- [ ] **步骤 3：跑全仓**

运行：`go test ./... 2>&1 | tail -30`
预期：无 FAIL

- [ ] **步骤 4：Commit（若验证中发现需修复的回归）**

```bash
git add -u
git commit -m "test(sub-agent): verify structured completion regression suite"
```

---

## 自检清单

**规格覆盖度：**
- 痛点 1（角色描述/事情给够）→ 任务 1 输出契约（自包含 + 结构化小节 + 完成判定）✅
- 痛点 2（结果判断要人为继续）→ 任务 2/3 让 critic 走 `submit_result` 唯一完成路径，完成判定从 LLM 判断文本变为确定性工具调用 ✅

**占位符扫描：** 无 "TODO"/"待定"/"补充细节"；每个代码步骤都含完整代码。✅

**类型一致性：**
- `AgentSpec.StructuredResult`（`engine/agent.go:85`）——任务 2 使用，已存在 ✅
- `specialistAgent` 字段 `id/spec/promptEn/promptZh/runner`（`engine/default_agents.go:47-52`）——测试构造使用，已存在 ✅
- `criticPromptEn/criticPromptZh`（`engine/default_agents.go:73/112`）——测试使用，已存在 ✅
- `stubSeqModel`（`engine/sub_agent_terminate_test.go:14`）、`stubToolExecutor`（`engine/turn_test.go:75`）、`toolsContain`（`engine/sub_agent_finish_test.go:350`）——测试基建，已存在 ✅
- `getCriticSubmitNudge`（任务 3 新增）——只在任务 3 内部引用，无跨任务引用问题 ✅

**明确不做（YAGNI）：**
- 不做 report/中间汇报通道（新工具 + 渲染层成本高）
- 不做 outputSchema / verdict 字段（`parseCriticVerdict` 已能确定性解析）
- 不动 `ConclusionClassifier`（结构化覆盖后仅剩 team/roundtable 兜底）
- 不动主引擎 IntentContinue（`loop.go:709`，与子代理完成判定无关）
