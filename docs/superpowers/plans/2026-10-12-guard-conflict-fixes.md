# 护栏冲突修复实现计划（完成语义 + 工具元数据）

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。
>
> **执行环境约束（deepact）：** `handoff_to_agent` 子代理是**只读**的（`read/read_multi/grep/glob/lsp/web_search/fetch`），不能写文件、不能跑 `go test`。因此**推荐内联执行**；若走子代理，只能把它们当作"代码草拟"环节，落盘与 `make test` 由主代理执行。
>
> **本文档状态：待审核。** 审核通过后再进入实现。审核重点是 §3「待你决策」的 6 项。

**目标：** 消除两处"引擎替模型/替工具做判定"的护栏硬编码：①非结构化子代理的纯文本回复被 3 击判为叙述；②`MadeProgress`/破坏性分类/工具拦截按工具名清单分发。

**架构：** ①把"完成语义"变成角色可声明的能力（`AgentSpec.TextResult` → `Handoff.TextResult`），复用既有的"文本即结果"分支；②把"工具是什么"变成工具自己的声明式元数据（`tools.ToolSpec` → `engine.ModelTool`），引擎与调度只读元数据，不再按名字 switch；③把"引擎拦截工具"变成一张单一事实来源的表格，同时驱动拦截与排除。

**技术栈：** Go 标准库；`go test -race`；仓库既有分层（`tools` 通过 `adapter.go` 桥接到 `engine`）。

**规格：** 调研报告 `docs/code-review-report/deepact-2026-10-12-硬编码与护栏哲学审计.md` §1、§2.1–§2.4；判据来自 `AGENTS.md`（泛化性优先 / 能力优先）。

**范围外（本次不做，留在审计报告里）：** 只读宇宙元数据化（§2.5）、角色 enum 双源（§2.6）、UI 渲染元数据（§2.7）、提示块剥离边界（§4）、token 口径（§5）、安全层客观缺陷（§7）、单形态能力（§6）。

---

## 1. 现状证据（主 agent 已读码核实）

### 1.1 完成语义被引擎猜（叙述 3 击）

| 位置 | 事实 |
|---|---|
| `engine/sub_agent.go:782` | 入口：`if len(msg.ToolCalls) == 0 {` |
| `engine/sub_agent.go:783-796` | 截断分支（`finish_reason=length`）先处理 |
| `engine/sub_agent.go:797-819` | `structured` 分支：纯文本永不完成，nudge 到 `submit_result` |
| `engine/sub_agent.go:820-824` | `if input.NoNudge { return buildResult(...) }` —— 已是"文本即结果"的现成实现（`buildResult` 置 `HandoffReasonCompleted`，`engine/sub_agent.go:1317-1322`） |
| `engine/sub_agent.go:828-835` | 否则 `consecutiveIntermediate++`，`>= 3` → `HandoffReasonStalledNarration`（`:829` 的 `3` 是内联魔法数） |
| `engine/sub_agent.go:1041-1043` | 仅当本轮真派发过工具才清零计数 |
| `engine/sub_agent.go:568-570` | 注释：resume 后叙述计数重置，"可再吃 3 击" |
| `engine/agent.go:82` | `NoNudge bool` 存在于进程内 `Handoff` |
| `engine/agent.go:224-250` | 模型可见的 `HandoffToAgentParams` **没有** `no_nudge`/文本完成字段 |
| 全仓 grep | `NoNudge: true` **只**出现在 `*_test.go`；`engine/handoff.go` 无任何 `Nudge` 赋值 → 生产路径从不设置它 |
| `engine/sub_agent.go:799-800` | 注释称 "harness roles (collab/ratd) pass NoNudge:true" —— **与生产代码不符（注释漂移）** |
| `engine/default_agents.go:192-225` | `proposerSpec`/`redteamSpec`/`arbitratorSpec` 均未设 `StructuredResult` |
| `engine/default_agents.go:237-249` | 三个 persona 要求 `Output ONLY a CodePayload/TestPayload/decision JSON object … Nothing but the JSON object.`（纯文本交付） |
| `agent.go:666-667` | `stalled_narration` 标题为"子代理持续叙述未执行（**部分结果**）" |
| `engine/turn.go:2030-2037` | `isHandoffFollowUpReason` 对 `stalled_narration` 返回 true（除 `""`/`completed`/`cancelled`/`async_running` 外全 true） |
| `engine/turn.go:2016-2022`、`:2048-2051` | 于是父代理收到 pinned 指令："…未给出完整结论…重新委派…**不要停在这里等待用户输入**" → 同一 Run 内继续 → 再委派 |

**结论：** 内置 `ratd` 三个角色**按设计交付纯文本 JSON**，而引擎规定"纯文本永不算完成"。这不是边界情况，是当前必然发生的行为。

### 1.2 工具语义被引擎按名字枚举

| 位置 | 事实 |
|---|---|
| `engine/turn.go:855-908` | `switch c.Name` 判定 `MadeProgress`：只有 `edit/write/apply_patch/revert/bash`、`read`、`read_multi`、`grep/glob`、`lsp`、`web_search/fetch` 有分支 |
| `engine/loop.go:710-733` | 该信号喂给 `progressLoop`（`loop.go:410` `NewLoopTracker(4, 6, true)`），`GuardBlock` → `BlockedBy:"loop_guard"`、`FinishReason:"loop_detected"`，**终止 Run** |
| `engine/loop.go:1380-1387` / `:1391-1397` | 第 4 轮注入 `[进度提示]`；第 6 轮终止并问用户"请澄清：…基于已有内容直接给出结论，还是调整任务方向？"（文案假设是"只读/规划"，与实际不符） |
| `engine/turn.go:909-911` | 例外：`handoff` 无条件算进展 |
| `engine/turn.go:919-924` + `engine/agent_manage.go:78-83` | 例外：async 子代理状态观察按"新状态"算进展（`agent_*` 工具） |
| `cmd/run.go:494` / `:348` / `:498` | **现在就被漏算**：`artifact`（读回大数据＝获取新信息）、`skill_install`（写磁盘）、`remember_preference`（写 AGENTS.md） |
| `engine/turn.go:770-777` | 调度分类只认 `edit/write/apply_patch` 为"破坏性"（串行 + 渐进展示，`:801` 注释）；其余（含 `bash`，`:782` 注释自认）进并行批 |
| `tools/registry.go:108-112` | 文件锁**只**对 `edit`/`write` 生效，且只认 `path` 键 |
| `engine/turn.go:672-700` | 11 个 `else if call.Name == XxxToolName { continue }` 组成拦截排除链 |
| `engine/turn.go:666-669` | 注释自陈后果：漏加 → 同一 `tool_call_id` 两条 tool 消息 → 违反 API 契约 |
| `tools/registry.go:120-125` | 未注册工具返回 **相同 `ToolCallID`** 的 `tool not found` 信封 → 正是"同一 id 两条响应"的来源 |
| `engine/key.go:35-61,167-182` | `extractToolKey` 按工具名 switch；`extractPathField` 只认 `path`/`file_path`，不认 `filePath`（`engine/turn.go:1386-1404` 的 `extractPathFromArgs` 认三个） |

---

## 2. 不改的影响（已核实，供审核对照）

**A. 叙述 3 击（影响子代理；主代理无内置 stop hook，`engine/stop_hook.go:35-38`，不受影响）**

1. **ratd 三角色功能失配**：正确输出 JSON 后每次被注入 `getNudgeMessage`（`engine/sub_agent.go:1517-1522`）"请直接使用工具执行下一步" → 每角色白烧 2 次 LLM 调用。
2. **交付物被标为"部分结果"**（`agent.go:666-667`）；JSON 仍在 `Summary`（`formatHandoffResult:632-634`），父代理拿得到，但标题引导它认为不完整 → 重做/重委派。
3. **自动续跑放大**：`isHandoffFollowUpReason` + `buildHandoffFollowUp` 让父代理在同一 Run 内继续委派 → 直到宽度 6 / Run 级 token 预算（20× 窗口）打满 → 之后委派被 `fanout_limit`/`tree_budget_exceeded` 拒绝（`agent.go:674-677`）。**用户看到的是变慢 + 委派被拒，不是报错。**
4. **nudge 与 persona 对抗**：模型可能放弃"只输出 JSON"改去 read/grep 行动 → 交付物不再可解析 → ratd 编排者要么空跑，要么退化成 `max_iterations`/`loop_detected`。
5. **resume 后击数翻倍**（`sub_agent.go:568-570`）。
6. **配置能声明角色能力，却声明不了完成语义**（`[agents.x]` 无对应字段；`HandoffToAgentParams` 也没有）→ 自定义非结构化角色默认踩坑且无法自救。
7. **用户错误归因**：UI 标题"部分结果"让人以为是子代理能力问题。

**B. 工具名枚举**

1. `MadeProgress` 漏算 `artifact`/`skill_install`/`remember_preference` → 连续 6 轮这类调用 → Run 被 `loop_guard` 终止 + 一条与事实不符的"只读/规划"解释；用户必须再发一条消息才能继续。
2. 新增写类工具（MCP 工具、`rename_file`/`run_tests` 等）默认不计进展 → 6 轮内误杀；且新增写工具会落入并行批 → 无文件锁（`tools/registry.go:108` 只认 edit/write）→ 同名文件并发写竞态。
3. 拦截链漏加 → **同一 `tool_call_id` 两条响应 → 请求 400 → 该会话持续卡死**（最重的一项，但只在有人新增拦截工具时触发）。
4. `filePath` 别名 → `extractToolKey` 返回空 → `annotateRepeats` 无 key → 模型收不到"你已重复同一操作 N 次"的事实；read 路径能识别、edit 路径识别不了。

---

## 3. 待你决策（审核时请逐条拍板）

| 编号 | 决策点 | 备选 | 我的建议 |
|---|---|---|---|
| **D1** | 完成语义的声明位置与命名 | (a) 仅角色级 `AgentSpec.TextResult`；(b) 仅调用级（`HandoffToAgentParams.text_result`）；(c) 两者 | **(a)**。完成语义是角色的稳定契约，不是每次调用的临时开关；调用级可被模型随意伪造，且 `tools` 覆盖已有"只能收窄"的纪律，不宜再开一个可放宽的旋钮 |
| **D2** | 内部字段命名 | (a) 保留 `Handoff.NoNudge`；(b) 重命名为 `Handoff.TextResult` | **(b)**。现名描述的是被删掉的 workaround，语义已变为"声明式能力"；重命名是机械改动（生产 1 处 + 测试约 12 处） |
| **D3** | 未声明元数据的**新**工具，算不算"进展" | (a) 算（信任默认）；(b) 不算（保守） | **(a)**。`progressLoop` 是**唯一能终止 Run** 的护栏，误杀代价 > 漏检代价；且引擎对不认识的工具没有判定依据（能力优先） |
| **D4** | 未声明元数据的**新**工具，并行还是串行 | (a) 串行（保守，慢但无竞态）；(b) 并行（保持现状） | **(a)**，并让 `bash` **显式声明** `Execution: batch` 以保留今天的性能（把隐式默认变成写下来的偏离）。若你更在意交互速度，选 (b) 并把该风险记在文档 |
| **D5** | key 构造（读/搜/联网的新奇度）是否一并元数据化 | (a) 最小：只修 `filePath` 别名，保留现有 per-tool key 函数；(b) 完整：工具声明 key 字段，删掉 `extractReadScope`/`searchKey`/`lspKey`/`infoQueryKey` | **(a)**。完整版会改变进度语义（护栏灵敏度变化），属另一个风险面；本次先消除"名字 switch 决定能力"这一根因中风险最低的部分 |
| **D6** | 拦截链改法 | (a) 只用常量集合替代 if-else（改动最小）；(b) 数据驱动表 `{name, handler}`，同时驱动拦截与排除（单一事实来源） | **(b)** + 一条**一致性测试**（表里的名字必须在 `Specs()` 中存在、且必须真有 handler）。仅 (a) 无法防"新增工具忘了加进集合"这一失效模式 |

---

## 4. 工具元数据表（除 §3 决策外，请一并确认这份分类）

新增到 `tools.ToolSpec`（并镜像到 `engine.ModelTool`）的三个声明式字段：

- `Mutates bool` — 会修改文件/系统状态
- `NovelInfo bool` — 返回新信息（首次 `(tool, 归一化参数)` 计为进展）
- `Execution string` — `""`（默认，并行批）/ `"sequential"`（逐个执行并渐进展示）

| 工具 | Mutates | NovelInfo | Execution | 说明 |
|---|---|---|---|---|
| `read` | | ✔ | | 现有 `read:<path>::<scope>` key 逻辑保留（D5=a） |
| `read_multi` | | ✔ | | 同上 |
| `grep` | | ✔ | | |
| `glob` | | ✔ | | |
| `lsp` | | ✔ | | |
| `web_search` | | ✔ | | |
| `fetch` | | ✔ | | |
| `artifact` | | ✔ | | **修正漏算**：读回被截断的大输出＝获取新信息 |
| `edit` | ✔ | | sequential | |
| `write` | ✔ | | sequential | |
| `apply_patch` | ✔ | | sequential | |
| `revert` | ✔ | | | 现为并行批（保持现状） |
| `bash` | ✔ | | `batch`（显式） | 现状是并行批（`turn.go:782` 注释）；本次把它写下来 |
| `skill_install` | ✔ | | | **修正漏算**：写磁盘 |
| `remember_preference` | ✔ | | | **修正漏算**：写 AGENTS.md |
| `todo_write` / `plan_task` / `load_skill` / `task_complete` / `ask_user` / `submit_result` | | | | 计划与通道类，既非写也非新信息（`task_complete`/`ask_user` 会结束 turn，不进 `regularCalls`） |
| `handoff_to_agent` | | | | 由 `turn.go:909-911` 无条件计进展（保持） |
| `agent_poll` / `agent_resume` / `list_agents` / `wait_agent` / `send_message` / `interrupt_agent` / `close_agent` | | | | 由 `agent_manage.go:78-83` 的状态观察计进展（保持） |

> 上表是**唯一事实来源**的实现形态；审核通过后逐项落到各工具的 `Spec()` 里。

---

## 5. 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `engine/agent.go` | `Handoff.TextResult`（原 `NoNudge`）、`AgentSpec.TextResult` | 修改（`:82`、`:184-204`） |
| `engine/default_agents.go` | ratd 三角色声明 `TextResult`；`specSubAgent.Run` 合并该声明 | 修改（`:103`、`:192-225`） |
| `engine/sub_agent.go` | 修正注释；分支改用新字段名；击数提为命名常量 | 修改（`:799-800`、`:820`、`:828-835`、常量区） |
| `engine/handoff.go` | （仅当 D1 选 (b)/(c)）把调用级字段接进 `Handoff` | 修改 |
| `config/config.go` | `[agents.x] text_result` 解析 | 修改（`:467-506`） |
| `tools/registry.go` | `ToolSpec` 新增三个元数据字段 | 修改（`:13-17`） |
| `tools/adapter.go` | 把元数据桥接到 `engine.ModelTool` | 修改（`:15-32`） |
| `engine/types.go` | `ModelTool` 新增三个字段 | 修改（`:191-200`） |
| `tools/builtin/*.go` | 各工具 `Spec()` 填元数据 | 修改（约 14 个文件） |
| `engine/turn.go` | 调度分类 + `MadeProgress` 改元数据驱动；拦截链改表驱动 | 修改（`:672-700`、`:770-777`、`:855-908`） |
| `engine/agent_manage.go` | 拦截 handler 归一签名（表驱动） | 修改 |
| `engine/key.go` | `extractPathField` 认 `filePath` | 修改（`:167-182`） |
| `engine/tool_metadata_test.go` | **新**：元数据表 + 分类 + 进展判定 + 拦截链一致性 | 创建 |
| `engine/sub_agent_completion_test.go` | **新**：角色级完成语义（含 ratd 三角色回归） | 创建 |
| `config/config_test.go` | **新**：`text_result` 解析与冲突校验 | 修改（追加） |
| `tools/builtin/tool_metadata_test.go` | **新**：每个内置工具的元数据断言 | 创建 |

---

## 任务 1：把"完成语义"变成角色可声明的能力

**文件：**
- 修改：`engine/agent.go:82`（`Handoff.NoNudge` → `TextResult`）、`:184-204`（`AgentSpec` 新增字段）
- 修改：`engine/default_agents.go:103`、`:192-225`
- 修改：`engine/sub_agent.go:799-800`、`:820`
- 修改：`config/config.go:467-506`
- 测试：`engine/sub_agent_completion_test.go`（创建）、`config/config_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/sub_agent_completion_test.go`：

```go
package engine

import (
	"context"
	"strings"
	"testing"
)

// A role that declares its text IS the result must complete on a text-only
// reply instead of being counted as narration and killed by the 3-strike guard.
func TestSubAgentTextResult_CompletesOnPlainText(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: `{"decision":"ACCEPT","feedback":"x"}`}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	res, err := runner.Run(context.Background(), Handoff{
		Agent: AgentArbitrator, Goal: "judge", TextResult: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("text-result role must complete on a text reply, got %q", res.FinishReason)
	}
	if !strings.Contains(res.Summary, "ACCEPT") {
		t.Errorf("the text reply must be the result, got %q", res.Summary)
	}
	if model.calls != 1 {
		t.Errorf("a declared text-result role must not be nudged: got %d model calls", model.calls)
	}
}

// Regression: without the declaration the guard must still bite, otherwise the
// fix would silently disable runaway protection for every role.
func TestSubAgentTextResult_UndeclaredStillStrikes(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{
		{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"},
		{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"},
		{Message: ModelMessage{Role: "assistant", Content: "结论"}, FinishReason: "stop"},
	}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}

	res, err := runner.Run(context.Background(), Handoff{Agent: AgentSub, Goal: "g"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonStalledNarration {
		t.Fatalf("undeclared role must still be guarded, got %q", res.FinishReason)
	}
}

// The three ratd roles deliver their payload as plain text (persona: "Output
// ONLY a … JSON object"). They must declare text-result completion, or their
// correct output is judged as narration.
func TestDefaultAgents_RatdRolesDeclareTextResult(t *testing.T) {
	reg := NewDefaultRegistry(nil, nil)
	for _, id := range []AgentID{AgentProposer, AgentRedTeam, AgentArbitrator} {
		agent, ok := reg.Get(id)
		if !ok {
			t.Fatalf("role %q is not registered", id)
		}
		if !agent.Spec().TextResult {
			t.Errorf("role %q delivers JSON text and must declare TextResult", id)
		}
	}
}

// The role declaration is an invariant of the role, not something a delegating
// agent can widen or narrow per call.
func TestSpecSubAgent_TextResultComesFromSpec(t *testing.T) {
	capture := &captureSubRunner{}
	a := &specSubAgent{runner: capture, spec: AgentSpec{ID: AgentArbitrator, TextResult: true}}
	if _, err := a.Run(context.Background(), Handoff{Agent: AgentArbitrator, Goal: "judge"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !capture.input.TextResult {
		t.Error("specSubAgent must merge the role's TextResult into the handoff")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestSubAgentTextResult|TestDefaultAgents_RatdRolesDeclareTextResult|TestSpecSubAgent_TextResultComesFromSpec' -count=1`

预期：编译失败，报 `unknown field TextResult in struct literal of type Handoff`、`agent.Spec().TextResult undefined`。

- [ ] **步骤 3：给 `Handoff` 与 `AgentSpec` 加字段（D1=a、D2=b）**

在 `engine/agent.go` 把 `NoNudge bool \`json:"no_nudge,omitempty"\`` 改为：

```go
	// TextResult declares this run's completion semantics: a text-only reply IS
	// the result (the role delivers its payload as text). Roles set it through
	// AgentSpec.TextResult; the runner maps the declaration here. Without it, a
	// non-structured run treats a text-only reply as narration
	// (3-strike → stalled_narration).
	TextResult bool `json:"text_result,omitempty"`
```

在 `AgentSpec`（`engine/agent.go:184-204`）末尾追加：

```go
	// TextResult declares that this role's text-only reply IS its result, so a
	// non-structured run must complete on it instead of counting it as
	// narration. Use it for roles whose contract is a text payload (ratd's
	// CodePayload/TestPayload/decision JSON). Mutually exclusive in spirit with
	// StructuredResult: a structured run completes only through submit_result.
	TextResult bool
```

- [ ] **步骤 4：合并声明（`specSubAgent.Run`）**

在 `engine/default_agents.go:103` 之后插入：

```go
	input.StructuredResult = spec.StructuredResult
	// Completion semantics are a role invariant, so the spec — not the caller —
	// decides whether text completes the run. An explicit caller opt-in is kept.
	if spec.TextResult {
		input.TextResult = true
	}
	return a.runner.Run(ctx, input)
```

（删除原 `return a.runner.Run(ctx, input)` 那一行，避免重复 return。）

- [ ] **步骤 5：切换分支并修正漂移的注释**

`engine/sub_agent.go:799-800` 的注释改为：

```go
			// The structured contract ("only submit_result counts") takes
			// precedence over TextResult: a lenient "any text is the answer" mode
			// must not bypass it — otherwise a plain-text narration becomes the
			// final summary and the harness parse fails.
```

`engine/sub_agent.go:820` 改为：

```go
			if input.TextResult {
```

- [ ] **步骤 6：ratd 三角色声明（`engine/default_agents.go:192-225`）**

`proposerSpec()` / `redteamSpec()` / `arbitratorSpec()` 各加一行（示例：`proposerSpec`）：

```go
	return AgentSpec{
		ID:            AgentProposer,
		Description:   "ratd implementation role: produce complete implementation code for a requirement, output only a CodePayload JSON object.",
		ToolNames:     []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations: 20,
		Persona:       proposerPersona,
		// Its contract IS a text payload ("Nothing but the JSON object"), so text
		// is the result — not narration.
		TextResult: true,
	}
```

（`redteamSpec`、`arbitratorSpec` 同样加 `TextResult: true`。）

- [ ] **步骤 7：配置键（`[agents.x] text_result`）**

在 `config/config.go` 的 agent 结构体（`rc.StructuredResult` 所在处，`config.go:502-504` 附近）之后追加：

```go
		if rc.TextResult != nil {
			spec.TextResult = *rc.TextResult
		}
```

在 `contextConfig`/`agents` 配置结构里，与 `StructuredResult *bool` 并列加：

```go
	// TextResult declares that this role's text-only reply IS its result (a text
	// payload contract, e.g. ratd's JSON). Mutually exclusive with
	// structured_result: a structured role completes only via submit_result.
	TextResult *bool `toml:"text_result"`
```

并在 `Apply` 里拒绝两者同时为 true（fail loud，与其它键同风格）：

```go
		if rc.TextResult != nil && rc.StructuredResult != nil && *rc.TextResult && *rc.StructuredResult {
			return fmt.Errorf("[agents.%s] text_result and structured_result cannot both be true: a structured run completes only through submit_result", name)
		}
```

- [ ] **步骤 8：机械重命名测试中的字段**

`engine/sub_agent_limits_test.go`、`engine/sub_agent_budget_nudge_test.go`、`engine/agent_manage_test.go`、`engine/fork_test.go`、`engine/sub_agent_finish_test.go` 中所有 `NoNudge: true` → `TextResult: true`；`engine/default_agents_test.go:36` 的注释同步改词。

运行：`grep -rn "NoNudge" --include=*.go .` → 预期无输出（除本计划文档）。

- [ ] **步骤 9：运行测试验证通过**

运行：`go test ./engine/ ./config/ -run 'TextResult|RatdRoles|SpecSubAgent|SubAgent' -count=1 -race`

预期：PASS。

- [ ] **步骤 10：Commit**

```bash
git add engine/agent.go engine/default_agents.go engine/sub_agent.go config/config.go \
  engine/sub_agent_completion_test.go config/config_test.go \
  engine/sub_agent_limits_test.go engine/sub_agent_budget_nudge_test.go \
  engine/agent_manage_test.go engine/fork_test.go engine/sub_agent_finish_test.go \
  engine/default_agents_test.go
git commit -m "feat(engine): let roles declare that text is their result"
```

---

## 任务 2：击数常量与文档同步（关闭现实缺陷的尾巴）

**文件：**
- 修改：`engine/sub_agent.go`（常量区 `:13-28`、`:828-835`）
- 修改：`docs/superpowers/plans/2026-10-12-subagent-delegation-tree-invariants.md`（执行记录的行号勘误）

- [ ] **步骤 1：把击数提为命名常量**

在 `engine/sub_agent.go` 常量区追加：

```go
	// narrationStrikeLimit is how many consecutive text-only replies a
	// non-structured, non-TextResult run may produce before the engine ends it
	// as stalled_narration. Named because it is a completion contract, not a
	// tuning knob: the fix for a role that delivers text is AgentSpec.TextResult.
	narrationStrikeLimit = 3
```

- [ ] **步骤 2：使用常量**

`engine/sub_agent.go:829` 改为：

```go
			if consecutiveIntermediate >= narrationStrikeLimit {
```

`engine/sub_agent.go:785`（截断分支）与 `:807`（结构化分支）同样替换各自的 `>= 3`。

- [ ] **步骤 3：文档行号勘误**

在 `docs/superpowers/plans/2026-10-12-subagent-delegation-tree-invariants.md` 的「执行记录（偏差）」中，把 `engine/sub_agent.go:669-679` 更正为 `:825-835`（击数在 `:829`，现为 `narrationStrikeLimit`）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'StalledNarration|SubAgent' -count=1`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/sub_agent.go docs/superpowers/plans/2026-10-12-subagent-delegation-tree-invariants.md
git commit -m "refactor(engine): name the narration strike limit, fix the plan's stale line refs"
```

---

## 任务 3：工具元数据字段（`tools.ToolSpec` → `engine.ModelTool`）

**文件：**
- 修改：`tools/registry.go:13-17`
- 修改：`tools/adapter.go:15-32`
- 修改：`engine/types.go:191-200`
- 测试：`tools/builtin/tool_metadata_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

创建 `tools/builtin/tool_metadata_test.go`：

```go
package builtin

import "testing"

// metadataOf returns a tool's declared metadata, so every built-in can be
// asserted in one table without repeating the struct literal shape.
func metadataOf(t *testing.T, spec interface {
	Spec() struct {
		Name        string
		Description string
		Parameters  []byte
	}
}) {
	t.Helper()
}

// Every built-in that writes must declare Mutates, and every built-in that
// returns information must declare NovelInfo. Missing metadata is what made
// artifact/skill_install/remember_preference invisible to the progress guard.
func TestBuiltinToolMetadata(t *testing.T) {
	cases := []struct {
		name      string
		mutates   bool
		novelInfo bool
	}{
		{"read", false, true},
		{"read_multi", false, true},
		{"grep", false, true},
		{"glob", false, true},
		{"lsp", false, true},
		{"web_search", false, true},
		{"fetch", false, true},
		{"artifact", false, true},
		{"edit", true, false},
		{"write", true, false},
		{"apply_patch", true, false},
		{"revert", true, false},
		{"bash", true, false},
		{"skill_install", true, false},
		{"remember_preference", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := SpecByName(t, tc.name)
			if spec.Mutates != tc.mutates {
				t.Errorf("%s.Mutates = %v, want %v", tc.name, spec.Mutates, tc.mutates)
			}
			if spec.NovelInfo != tc.novelInfo {
				t.Errorf("%s.NovelInfo = %v, want %v", tc.name, spec.NovelInfo, tc.novelInfo)
			}
		})
	}
}

// Only the three diff-rendered tools run sequentially; bash stays in the
// parallel batch (declared, not incidental — see the audit's D4).
func TestBuiltinToolExecutionClass(t *testing.T) {
	sequential := map[string]bool{"edit": true, "write": true, "apply_patch": true}
	for name, want := range sequential {
		if got := SpecByName(t, name).Execution; got != tools.ExecutionSequential {
			t.Errorf("%s.Execution = %q, want %q", name, got, tools.ExecutionSequential)
		}
	}
	if got := SpecByName(t, "bash").Execution; got != tools.ExecutionBatch {
		t.Errorf("bash.Execution = %q, want an explicit %q", got, tools.ExecutionBatch)
	}
	if got := SpecByName(t, "read").Execution; got != tools.ExecutionBatch {
		t.Errorf("read.Execution = %q, want the default %q", got, tools.ExecutionBatch)
	}
}
```

（`SpecByName` 是同文件里的测试辅助：遍历 `NewReadTool()`…`NewRememberPreferenceTool()` 的 `Spec()`，按 `Name` 返回；实现时把它写在测试文件顶部即可。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./tools/builtin/ -run 'TestBuiltinToolMetadata|TestBuiltinToolExecutionClass' -count=1`

预期：编译失败，报 `spec.Mutates undefined`。

- [ ] **步骤 3：`tools.ToolSpec` 加元数据**

`tools/registry.go:13-17` 改为：

```go
// Execution class of a tool: how the engine schedules its calls.
const (
	// ExecutionBatch runs the call in the parallel batch with no per-call
	// display. It is the zero value (and therefore the default).
	ExecutionBatch = ""
	// ExecutionSequential runs the call alone and displays its result
	// progressively (diff-style UX).
	ExecutionSequential = "sequential"
)

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`

	// --- Declarative semantics ---------------------------------------------
	// Mutates: the tool changes files or system state. The engine never has to
	// know a tool's name to tell read-only work from modification work.
	Mutates bool `json:"-"`
	// NovelInfo: the tool returns information. A first-time (tool, normalized
	// arguments) call counts as progress.
	NovelInfo bool `json:"-"`
	// Execution: ExecutionBatch (default) or ExecutionSequential.
	Execution string `json:"-"`
}
```

- [ ] **步骤 4：`engine.ModelTool` 镜像元数据**

`engine/types.go:191-200` 改为：

```go
type ModelTool struct {
	Type     string
	Function ModelToolFunction

	// --- Declarative tool semantics, mirrored from tools.ToolSpec by the
	// adapter. The engine keys its scheduling and progress logic off THESE,
	// never off a tool name.
	Mutates   bool
	NovelInfo bool
	// Execution is "" (batch) or "sequential"; see tools.ExecutionSequential.
	Execution string
}
```

- [ ] **步骤 5：适配器桥接**

`tools/adapter.go:22-29` 的 `engine.ModelTool{...}` 字面量补三个字段：

```go
		result = append(result, engine.ModelTool{
			Type: "function",
			Function: engine.ModelToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
			Mutates:   spec.Mutates,
			NovelInfo: spec.NovelInfo,
			Execution: spec.Execution,
		})
```

- [ ] **步骤 6：各内置工具填元数据**

对 `tools/builtin/` 中每个 `Spec()` 按 §4 的表补字段（示例：`artifact.go`）：

```go
func (t *ArtifactTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name: "artifact",
		// ...
		NovelInfo: true, // reading back a stored output is new information
	}
}
```

`bash.go` 额外显式声明执行类别（把今天的隐式默认写下来）：

```go
		Mutates:   true,
		Execution: tools.ExecutionBatch, // status quo: batched with the read-only calls
```

`edit.go` / `write.go` / `apply_patch.go`：

```go
		Mutates:   true,
		Execution: tools.ExecutionSequential,
```

`read.go` / `read_multi.go` / `grep.go` / `glob.go` / `lsp*.go` / `fetch.go` / `search.go`：

```go
		NovelInfo: true,
```

`skill_install.go` / `remember_preference.go`：

```go
		Mutates: true,
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./tools/... -count=1`

预期：PASS（含新增两个用例）。

- [ ] **步骤 8：Commit**

```bash
git add tools/registry.go tools/adapter.go engine/types.go tools/builtin/
git commit -m "feat(tools): declare mutates/novel-info/execution on every tool spec"
```

---

## 任务 4：调度与进展判定改元数据驱动

**文件：**
- 修改：`engine/turn.go:770-777`（调度分类）、`:855-908`（进展判定）
- 测试：`engine/tool_metadata_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

创建 `engine/tool_metadata_test.go`：

```go
package engine

import (
	"context"
	"encoding/json"
	"testing"
)

// toolMetaExecutor is a ToolExecutor stub whose specs carry declared metadata,
// so the engine's classification can be tested without a real registry.
type toolMetaExecutor struct {
	specs []ModelTool
}

func (e toolMetaExecutor) Specs() []ModelTool { return e.specs }
func (e toolMetaExecutor) Execute(_ ToolExecContext, calls []ToolCallRequest) []ToolResult {
	out := make([]ToolResult, 0, len(calls))
	for _, c := range calls {
		out = append(out, ToolResult{ToolCallID: c.ID, ToolName: c.Name, Status: "ok", Digest: "ok"})
	}
	return out
}

func metaSpec(name string, mutates, novel bool, exec string) ModelTool {
	return ModelTool{Type: "function", Function: ModelToolFunction{Name: name}, Mutates: mutates, NovelInfo: novel, Execution: exec}
}

// A tool the engine has never heard of, declared Mutates with sequential
// execution, must be scheduled sequentially — the engine keys off metadata, not
// off the hardcoded edit/write/apply_patch list.
func TestExecuteTurn_SequentialFromMetadata(t *testing.T) {
	e := &Engine{
		model:  &stubStreamModel{chunks: []ModelChunk{{Delta: "ok", FinishReason: "stop"}}},
		tools:  toolMetaExecutor{specs: []ModelTool{metaSpec("rename_file", true, false, "sequential")}},
		context: &stubContextBuilder{},
		state:  &TaskState{TurnNumber: 0},
		config: EngineConfig{ModelName: "test", WorkDir: "/repo"},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}
	if _, err := e.executeTurn(context.Background()); err != nil {
		t.Fatalf("executeTurn: %v", err)
	}
}

// A declared Mutates tool counts as progress even though the engine does not
// know its name. This is the class that used to be missed entirely.
func TestExecuteTurn_MadeProgress_FromMetadata(t *testing.T) {
	e := &Engine{
		state:  &TaskState{TurnNumber: 0},
		config: EngineConfig{WorkDir: "/repo"},
		guards: &GuardSystem{scope: NewScopeGuard()},
	}
	statusByID := map[string]string{"c1": "ok"}
	got := e.madeProgressFromCalls(
		[]ToolCallRequest{{ID: "c1", Name: "rename_file", Input: json.RawMessage(`{"path":"a.go","new_path":"b.go"}`)}},
		statusByID,
		map[string]ModelTool{"rename_file": metaSpec("rename_file", true, false, "sequential")},
	)
	if !got.MadeProgress {
		t.Error("a declared Mutating tool must count as progress")
	}
}

// A declared NovelInfo tool counts once per new (tool, args) pair and not on a
// repeat — the same rule read/grep already had.
func TestExecuteTurn_MadeProgress_NovelInfoOnce(t *testing.T) {
	e := &Engine{
		progressKeys: map[string]bool{},
		config:       EngineConfig{WorkDir: "/repo"},
	}
	specs := map[string]ModelTool{"artifact": metaSpec("artifact", false, true, "")}
	call := ToolCallRequest{ID: "c1", Name: "artifact", Input: json.RawMessage(`{"ref":"sha256:abc"}`)}

	if !e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"c1": "ok"}, specs).MadeProgress {
		t.Fatal("first artifact read must count as progress")
	}
	call.ID = "c2"
	if e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"c2": "ok"}, specs).MadeProgress {
		t.Error("re-reading the same artifact must not count again")
	}
}

// Undeclared tools (no metadata at all) are trusted: an unknown tool must not
// be able to starve the run into a false no-progress block (decision D3).
func TestExecuteTurn_MadeProgress_UndeclaredIsTrusted(t *testing.T) {
	e := &Engine{
		progressKeys: map[string]bool{},
		config:       EngineConfig{WorkDir: "/repo"},
	}
	call := ToolCallRequest{ID: "c1", Name: "some_mcp_tool", Input: json.RawMessage(`{}`)}
	if !e.madeProgressFromCalls([]ToolCallRequest{call}, map[string]string{"c1": "ok"}, nil).MadeProgress {
		t.Error("an undeclared tool must be trusted as progress (D3)")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestExecuteTurn_(SequentialFromMetadata|MadeProgress_FromMetadata|MadeProgress_NovelInfoOnce|MadeProgress_UndeclaredIsTrusted)' -count=1`

预期：编译失败，报 `e.madeProgressFromCalls undefined`。

- [ ] **步骤 3：抽出按元数据驱动的判定函数**

在 `engine/turn.go` 的 `updateTaskStateFromTools` 附近新增：

```go
// specsByName indexes the tool specs the engine was offered this turn, so
// scheduling and progress decisions can read declared metadata instead of
// matching tool names.
func (e *Engine) specsByName() map[string]ModelTool {
	specs := e.tools.Specs()
	m := make(map[string]ModelTool, len(specs))
	for _, s := range specs {
		m[s.Function.Name] = s
	}
	return m
}

// madeProgressFromCalls is the metadata-driven replacement for the tool-name
// switch: a call counts as progress when it modifies state successfully
// (Mutates), or when it brings information the run has not seen yet
// (NovelInfo + a new key). An undeclared tool is trusted (see decision D3):
// the engine has no basis to call it "no progress".
func (e *Engine) madeProgressFromCalls(calls []ToolCallRequest, statusByID map[string]string, specs map[string]ModelTool) TurnResult {
	var out TurnResult
	if e.progressKeys == nil {
		e.progressKeys = make(map[string]bool)
	}
	for _, c := range calls {
		spec, declared := specs[c.Name]
		switch {
		case declared && spec.Mutates:
			if statusByID[c.ID] == "ok" {
				out.MadeProgress = true
			}
		case declared && spec.NovelInfo:
			key := infoKeyFor(c, e.config.WorkDir)
			if key != "" && !e.progressKeys[key] {
				e.progressKeys[key] = true
				out.MadeProgress = true
			}
		case !declared:
			// Unknown tool: trust it rather than block a run we cannot judge.
			out.MadeProgress = true
		}
	}
	return out
}
```

`infoKeyFor` 保留既有 per-tool key 构造（D5=a），只是改为集中一处：

```go
// infoKeyFor builds the novelty key for an information-returning call. The
// per-tool key shape is unchanged (read scope, search pattern, lsp query, web
// query) — only the dispatch is centralized.
func infoKeyFor(call ToolCallRequest, workDir string) string {
	switch call.Name {
	case "read":
		if path := extractPathFromArgs(call.Input, workDir); path != "" {
			return "read:" + path + "::" + extractReadScope(call.Input)
		}
	case "read_multi":
		// read_multi's per-target keys are recorded by the existing loop below;
		// the umbrella call reports novelty through those targets.
		return ""
	case "grep", "glob":
		return searchKey(call, workDir)
	case "lsp":
		return lspKey(call, workDir)
	case "web_search", "fetch":
		return infoQueryKey(call)
	default:
		// A declared NovelInfo tool the engine has no key shape for: fall back
		// to the canonical argument JSON, so it is novel exactly once per
		// distinct call (this is what makes artifact/新工具 work with zero
		// per-tool code).
		return call.Name + ":" + canonicalArgs(call.Input)
	}
	return ""
}

// canonicalArgs renders a tool call's arguments deterministically (sorted
// object keys) so the same call always yields the same key.
func canonicalArgs(input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return string(input)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%v;", k, m[k])
	}
	return sb.String()
}
```

- [ ] **步骤 4：替换 `MadeProgress` 的工具名 switch**

删除 `engine/turn.go:855-908` 的整个 `for _, c := range regularCalls { switch c.Name { ... } }`（含 `read_multi` 的 per-target 记账循环），改为在 `updateTaskStateFromTools` 内先按 `read_multi` 记 per-target key、再调用新函数：

```go
	// read_multi reports one call but several targets; keep its per-target
	// novelty accounting, then let the metadata-driven pass handle the rest.
	for _, c := range regularCalls {
		if c.Name != "read_multi" {
			continue
		}
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
	}
	if mp := e.madeProgressFromCalls(regularCalls, statusByID, e.specsByName()); mp.MadeProgress {
		result.MadeProgress = true
	}
```

（`handoffCalls` 的无条件计进展 `:909-911` 与 `observedAgentStates` 的 `:919-924` **原样保留**。）

- [ ] **步骤 5：调度分类改元数据**

`engine/turn.go:770-777` 的循环体改为：

```go
		specs := e.specsByName()
		for _, call := range regularCalls {
			callByID[call.ID] = call
			if specs[call.Name].Execution == tools.ExecutionSequential {
				destructiveCalls = append(destructiveCalls, call)
			} else {
				readOnlyCalls = append(readOnlyCalls, call)
			}
		}
```

未声明元数据的工具 `Execution == ""`，即 `ExecutionBatch` —— 若 D4 选 (a)，则改为：

```go
			spec, declared := specs[call.Name]
			if !declared || spec.Execution != tools.ExecutionBatch {
				destructiveCalls = append(destructiveCalls, call) // undeclared → serial (D4=a)
			} else {
				readOnlyCalls = append(readOnlyCalls, call)
			}
```

（注意：`engine` 需要 import `tools`；本仓库 `tools` 已 import `engine`，反向 import 会成环。**若成环**，改为在 `engine.ModelTool` 上定义 `ExecutionSequential` 常量（`engine/types.go`），`adapter.go` 用它赋值——这是本任务唯一需要留意的依赖细节。）

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ -run 'TestExecuteTurn' -count=1 -race`

预期：PASS，且既有的 `progress_loop_test.go`、`repeat_fact_test.go` 全绿。

- [ ] **步骤 7：Commit**

```bash
git add engine/turn.go engine/tool_metadata_test.go
git commit -m "refactor(engine): schedule and score tools from declared metadata, not names"
```

---

## 任务 5：引擎拦截工具改为单一事实来源

**文件：**
- 修改：`engine/turn.go:672-700`（排除链）、`:363-416`（`load_skill` 拦截）附近
- 修改：`engine/agent_manage.go`（handler 签名归一）
- 测试：`engine/tool_metadata_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/tool_metadata_test.go`：

```go
// Every engine-intercepted tool must be handled by exactly one interceptor and
// must NOT reach the tool registry. A name in the table without a handler (or a
// handler without a table entry) is the bug that produces two tool responses
// for one tool_call_id and 400s the session.
func TestInterceptedTools_TableIsConsistent(t *testing.T) {
	tools := interceptedTools()
	seen := map[string]bool{}
	for _, it := range tools {
		if it.name == "" {
			t.Error("intercepted tool with an empty name")
		}
		if seen[it.name] {
			t.Errorf("intercepted tool %q listed twice", it.name)
		}
		seen[it.name] = true
		if it.handle == nil {
			t.Errorf("intercepted tool %q has no handler", it.name)
		}
	}
	for _, want := range []string{
		TodoWriteToolName, AskUserToolName, PlanTaskToolName, LoadSkillToolName,
		AgentPollToolName, AgentResumeToolName, ListAgentsToolName, WaitAgentToolName,
		SendMessageToolName, InterruptAgentToolName, CloseAgentToolName,
	} {
		if !seen[want] {
			t.Errorf("engine-intercepted tool %q is missing from the table", want)
		}
	}
}

// The exclusion used when splitting regularCalls must be derived from the same
// table, so a new interceptor can never fall through to Execute().
func TestInterceptedTools_NameSetMatchesTable(t *testing.T) {
	set := interceptedToolNames()
	if len(set) != len(interceptedTools()) {
		t.Fatalf("name set has %d entries, table has %d", len(set), len(interceptedTools()))
	}
	for _, it := range interceptedTools() {
		if !set[it.name] {
			t.Errorf("%q missing from the derived name set", it.name)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestInterceptedTools_' -count=1`

预期：编译失败，报 `undefined: interceptedTools`。

- [ ] **步骤 3：建立单一事实来源的表**

在 `engine/turn.go`（或新文件 `engine/intercept.go`）新增：

```go
// interceptedTool is an engine-side tool: its calls are answered by the engine
// itself and must never be dispatched to the tool registry. Both the
// interception dispatch and the exclusion from regularCalls read this ONE
// table, so adding a tool cannot leave the two out of sync — the failure mode
// that produced two tool responses for one tool_call_id (API 400).
type interceptedTool struct {
	name   string
	handle func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message
}

// interceptedTools is the single source of truth for engine-side tool
// interception. Order matters only for the messages it emits.
func interceptedTools() []interceptedTool {
	return []interceptedTool{
		{name: TodoWriteToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processTodoWriteCalls(calls)
		}},
		{name: AskUserToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processAskUserCalls(calls)
		}},
		{name: PlanTaskToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processPlanTaskCalls(calls)
		}},
		{name: LoadSkillToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processLoadSkillCalls(calls)
		}},
		{name: AgentPollToolName, handle: func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message {
			return e.processAgentPollCalls(calls)
		}},
		{name: AgentResumeToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processAgentResumeCalls(calls)
		}},
		{name: ListAgentsToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processListAgentsCalls(calls)
		}},
		{name: WaitAgentToolName, handle: func(e *Engine, ctx context.Context, calls []ToolCallRequest) []Message {
			return e.processWaitAgentCalls(ctx, calls)
		}},
		{name: SendMessageToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processSendMessageCalls(calls)
		}},
		{name: InterruptAgentToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processInterruptAgentCalls(calls)
		}},
		{name: CloseAgentToolName, handle: func(e *Engine, _ context.Context, calls []ToolCallRequest) []Message {
			return e.processCloseAgentCalls(calls)
		}},
	}
}

// interceptedToolNames derives the exclusion set from the table above.
func interceptedToolNames() map[string]bool {
	table := interceptedTools()
	set := make(map[string]bool, len(table))
	for _, it := range table {
		set[it.name] = true
	}
	return set
}
```

> 实现时请按仓库现有的函数名逐一对齐（`processAskUserCalls`、`processPlanTaskCalls`、`processLoadSkillCalls`、`processAgentResumeCalls`、`processSendMessageCalls`、`processInterruptAgentCalls`、`processCloseAgentCalls` 的实际名字以 `engine/turn.go`、`engine/agent_manage.go` 为准；签名不一致的用闭包适配，不要改它们的语义）。

- [ ] **步骤 4：用表替换 if-else 链**

`engine/turn.go:672-700` 改为：

```go
	intercepted := interceptedToolNames()
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		switch {
		case call.Name == HandoffToolName:
			handoffCalls = append(handoffCalls, call)
		case intercepted[call.Name]:
			continue // answered by the engine's interceptor (see interceptedTools)
		default:
			regularCalls = append(regularCalls, call)
		}
	}
```

- [ ] **步骤 5：用表替换分散的拦截调用**

把 `engine/turn.go` 中逐个 `pendingXxxMsgs := e.processXxxCalls(calls)` 的调用改为一次遍历（保持原有的消息拼接顺序）：

```go
	for _, it := range interceptedTools() {
		for _, msg := range it.handle(e, ctx, calls) {
			// The interceptors answer only the ids they own; the loop over the
			// table guarantees every intercepted call gets exactly one response.
			e.history = append(e.history, msg)
		}
	}
```

> 若某些拦截必须在 `handoffCalls` 之前/之后有特定顺序（例如 `todo_write` 的早返回语义），把该顺序约束写成表上的 `order int` 字段并在遍历前排序，**不要**退回 if-else。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./engine/ ./tools/ -run 'Intercepted|TodoWrite|AskUser|AgentPoll|LoadSkill|PlanTask' -count=1 -race`

预期：PASS（含 `turn_todo_write_test.go`、`ask_user_test.go`、`turn_agent_poll_test.go`、`turn_load_skill_test.go`、`plan_task_test.go`）。

- [ ] **步骤 7：Commit**

```bash
git add engine/turn.go engine/intercept.go engine/agent_manage.go engine/tool_metadata_test.go
git commit -m "refactor(engine): drive tool interception from one table"
```

---

## 任务 6：key 路径别名（`filePath`）

**文件：**
- 修改：`engine/key.go:167-182`
- 测试：`engine/tool_metadata_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `engine/tool_metadata_test.go`：

```go
// extractToolKey must see the same path aliases the read path sees
// (extractPathFromArgs accepts path/file_path/filePath). Otherwise an edit sent
// with the filePath alias loses its repeat key and the model never learns it is
// repeating itself.
func TestExtractToolKey_PathAliases(t *testing.T) {
	forms := []string{
		`{"path":"a.go","old_string":"x","new_string":"y"}`,
		`{"file_path":"a.go","old_string":"x","new_string":"y"}`,
		`{"filePath":"a.go","old_string":"x","new_string":"y"}`,
	}
	var first string
	for i, in := range forms {
		got := extractToolKey(ToolCallRequest{Name: "edit", Input: json.RawMessage(in)}, "/repo")
		if got == "" {
			t.Fatalf("form %d produced no key: the alias is invisible to repeat tracking", i)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Errorf("form %d key %q != canonical key %q", i, got, first)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./engine/ -run 'TestExtractToolKey_PathAliases' -count=1`

预期：FAIL，报 `form 2 produced no key`（`filePath` 形式）。

- [ ] **步骤 3：统一路径提取**

`engine/key.go:167-182` 的 `extractPathField` 改为复用统一入口：

```go
// extractPathField returns the call's target path, accepting every alias the
// tool layer accepts (path / file_path / filePath). It delegates to
// extractPathFromArgs so the repeat key and the read-progress key can never
// disagree about which path a call touches.
func extractPathField(input json.RawMessage, workDir string) string {
	return extractPathFromArgs(input, workDir)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./engine/ -run 'TestExtractToolKey|TestExecuteTurn' -count=1 -race`

预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add engine/key.go engine/tool_metadata_test.go
git commit -m "fix(engine): accept the filePath alias in the repeat key"
```

---

## 任务 7：回归与全量验证

**文件：**
- 测试：`engine/sub_agent_completion_test.go`（追加 ratd 端到端回归）
- 文档：`docs/code-review-report/deepact-2026-10-12-硬编码与护栏哲学审计.md`（把已修项标注为已修）

- [ ] **步骤 1：ratd 端到端回归**

追加到 `engine/sub_agent_completion_test.go`：

```go
// End-to-end: an arbitrator that answers with one JSON text reply must complete
// on the FIRST reply, with no nudge and no strike. This is the regression that
// keeps the fix honest for the built-in ratd pipeline.
func TestRatdArbitrator_CompletesWithoutNudge(t *testing.T) {
	model := &stubSeqModel{responses: []ModelResponse{{
		Message:      ModelMessage{Role: "assistant", Content: `{"decision":"REJECT","feedback":"tests faulty"}`},
		FinishReason: "stop",
	}}}
	runner := &SubAgentRunner{model: model, tools: readSpecExec(), modelName: "test"}
	reg := NewDefaultRegistry(runner, nil)
	agent, ok := reg.Get(AgentArbitrator)
	if !ok {
		t.Fatal("arbitrator not registered")
	}
	res, err := agent.Run(context.Background(), Handoff{Agent: AgentArbitrator, Goal: "judge"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinishReason != HandoffReasonCompleted {
		t.Fatalf("arbitrator must complete on its JSON reply, got %q", res.FinishReason)
	}
	if model.calls != 1 {
		t.Errorf("expected exactly one model call, got %d", model.calls)
	}
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./engine/ -run 'TestRatdArbitrator_CompletesWithoutNudge' -v -count=1`

预期：PASS。

- [ ] **步骤 3：全量测试（含 race）**

运行：`make test`

预期：`ok` 全部通过，无 DATA RACE。若失败，先判断是否为本次改动引入（本次改动只新增声明与元数据读取，不应改变任何已声明工具的既有行为）。

- [ ] **步骤 4：人工冒烟（建议，需交互环境）**

1. `/ratd <一个真实小需求>`：确认 proposer/redteam/arbitrator 都在**一次**文本回复后 `completed`，无 `stalled_narration`，无重委派。
2. 连续多轮只调 `artifact` 读回被截断输出：确认第 4 轮**不**出现 `[进度提示]`、第 6 轮**不**被终止。
3. 一次 `bash` + 一次 `read` 混合调用：确认 `bash` 仍在并行批（性能未退化）。

- [ ] **步骤 5：文档同步**

在 `docs/code-review-report/deepact-2026-10-12-硬编码与护栏哲学审计.md` 中，把 §1、§2.1–§2.4 标注为"已修复（日期 + commit）"，与历史审计文档的写法一致。

- [ ] **步骤 6：Commit**

```bash
git add engine/sub_agent_completion_test.go docs/code-review-report/
git commit -m "test(engine): pin ratd text-result completion; sync the audit doc"
```

---

## 6. 自检（计划作者已执行）

**1. 规格覆盖度**

| 审计条目 | 对应任务 |
|---|---|
| §1 叙述 3 击 / `NoNudge` 不可达 / ratd 三角色 | 任务 1（角色声明 + 接线 + 测试）、任务 2（常量 + 文档）、任务 7（回归） |
| §2.1 `MadeProgress` 工具名 switch（含 artifact/skill_install/remember_preference 漏算） | 任务 3（元数据）、任务 4（判定函数）、任务 7 步骤 4.2 |
| §2.2 破坏性/只读分类 | 任务 3（`Execution`）、任务 4 步骤 5 |
| §2.3 拦截链 | 任务 5（表 + 一致性测试） |
| §2.4 `filePath` 别名 | 任务 6 |
| 决策点 D1–D6 | §3（审核后落到任务 1/3/4/5 的具体分支） |

**2. 占位符扫描**：无 TODO／"待定"／"类似任务 N"；每个改代码的步骤都带完整代码块与精确命令。任务 5 步骤 3 提示"按仓库现有函数名对齐"是**必要**的（拦截函数名需在实现时逐个核对），已给出核对方式与替代方案，不是留空。

**3. 类型一致性**：`TextResult` 在 `Handoff`（任务 1 步骤 3）、`AgentSpec`（同）、`specSubAgent.Run`（步骤 4）、配置键（步骤 7）、测试（步骤 1）中逐字一致；`Mutates`/`NovelInfo`/`Execution` 在 `tools.ToolSpec`（任务 3 步骤 3）、`engine.ModelTool`（步骤 4）、`adapter.go`（步骤 5）、`madeProgressFromCalls`/`specsByName`（任务 4）、测试 stub（任务 4 步骤 1）中逐字一致。`ExecutionSequential` 的归属（`tools` vs `engine`）已在任务 4 步骤 5 标注依赖风险与规避方式（避免 `engine ↔ tools` 成环）。

**4. 已知边界（写下来而不是留空）**

- `read_multi` 的 per-target key 记账**保留**手工循环（D5=a 的自觉代价）：它的单次调用携带多个目标，元数据无法表达"一次调用多个 novelty key"。
- 任务 4 的 `canonicalArgs` 回退 key 会改变**未声明 NovelInfo 形状的已声明工具**的 key 形状；因 §4 表里所有 NovelInfo 工具都有专门分支，实际只作用于未来新增工具。
- 任务 5 的遍历顺序：若某拦截器依赖"在 handoff 之前"这类顺序，需在表上加 `order` 字段，不要退回 if-else。
- 本计划**不**触碰 §2.5 只读宇宙、§2.7 UI 渲染元数据——它们共用同一套元数据的后续扩展，但属独立批次。

---

## 7. 执行交接

计划保存到 `docs/superpowers/plans/2026-10-12-guard-conflict-fixes.md`。审核通过后两种执行方式：

1. **子代理驱动（推荐用于任务 3–5 的机械改动）** — 需要 superpowers:subagent-driven-development；注意本仓库子代理只读，只能产出代码文本，落盘与 `make test` 由主代理执行。
2. **内联执行（推荐）** — 需要 superpowers:executing-plans；按任务 1→7 顺序，每个任务一组 `go test` + 一次 commit。

---

## 执行记录（2026-10-12）

任务 1-7 内联执行完毕（在 `main` 上，经用户明确同意；未建 worktree）。`make test`（`go test ./... -count=1 -race -timeout=120s`）全绿。

| 任务 | commit |
|---|---|
| 1 完成语义角色声明 | `6f48c52` feat(engine): let roles declare that text is their result |
| 2 击数常量 + 计划勘误 | `b212c1b` refactor(engine): name the three strike limits; fix the stale line refs |
| 3 工具元数据 | `2a65c9d` feat(tools): declare mutates/novel-info/sequential on every tool spec |
| 4 调度与进展元数据化 | `8b1b9ac` refactor(engine): schedule and score tools from declared metadata |
| 5 拦截单一事实来源 | `765d89d` refactor(engine): drive tool interception from one table |
| 6 `filePath` 别名 | `b53adfe` fix(engine): accept the filePath alias in the repeat key |
| 7 回归 + 文档同步 | 见本次文档提交 |

### 与计划原文不一致之处（以仓库代码为准）

1. **`Execution string` + `tools.ExecutionSequential` → `Sequential bool`。** 计划任务 4 步骤 5 已标注该风险：`tools` 已 import `engine`，反向引用常量会成环。改用 bool 后无需跨包常量，也免去十几个 builtin 文件新增 import；语义不变（`false` = 并行批）。
2. **D3 的适用面扩大到"已注册但一条语义都没声明"的工具。** 计划只写了"未声明元数据的新工具"。实现中发现 MCP 工具是**已注册**（出现在 `Specs()` 里）但 `ToolSpec` 无任何元数据的形态；若只按"不在 spec 列表里"判定信任，MCP 工具会被静默判为"无进展"，6 轮后误杀。因此规则定为 `!declared || (!Mutates && !NovelInfo)` → 信任。`tools/builtin/tool_metadata_test.go` 断言每个内置工具至少声明一条语义，避免内置工具掉进这个兜底桶。
3. **测试桩必须携带元数据。** `recordingToolExecutor.Specs()` 原本只返回一个无元数据的 `read`；在新规则下"重复读不算进展"等断言会变成空洞。改为返回 `testToolSpecs()`，并在注释里指向真实来源 `tools/builtin/tool_metadata_test.go`。
4. **`updateTaskStateFromTools` 未改动。** 落实时核实：它只写 TaskState（ReadHistory / ModifiedFiles / working set），不碰 `progressKeys`，与新判定函数无重叠、无需改造。
5. **实现中自查出并当场修复的顺序错误。** 首版把拦截得到的 tool 消息追加在 `assistant(tool_calls)` **之前**（违反"tool 响应紧跟其请求消息"的 API 契约），在同一任务内改为先追加 assistant。

### 验证证据（实际运行的命令与结果）

- 基线（改动前）：`make test` → 17 包全部 ok。
- 任务 1：`go build ./...` OK；`go test ./engine/ ./config/ -count=1 -race` → ok。
- 任务 3：`go test ./tools/... -count=1 -race` → ok（含 `TestBuiltinToolMetadata`）。
- 任务 4：`make test` → ok（含 `TestSplitCallsByExecution_FromMetadata`、`TestMadeProgressFromCalls_*` 与既有 `TestExecuteTurn_MadeProgress_*`）。
- 任务 5：`make test` → ok（含 `TestInterceptedTools_*` 与 `todo_write` / `ask_user` / `agent_poll` / `load_skill` / `plan_task` 既有用例）。
- 任务 6：`go test ./engine/ -run 'TestExtractToolKey|TestExecuteTurn' -count=1 -race` → ok。
- 最终全量：`make test` → `go test ./... -count=1 -race -timeout=120s`，17 包 ok、0 FAIL。
- `gofmt -l` 于本次改动文件：干净。（`engine/agent_registry_test.go`、`classifier_test.go`、`loop_summary_test.go`、`sub_agent_stream_test.go`、`sub_agent_terminate_test.go`、`summarizeargs_test.go`、`tools/mcp/*` 在 HEAD 即为未格式化，非本次引入。）

### 未做（如实记录）

- **未做真实会话人工冒烟**：`/ratd` 全流程、多轮只调 `artifact` 的只读会话、`bash`+`read` 混合批的性能表现。当前为非交互执行环境。已用测试覆盖对应性质：`TestRatdArbitrator_CompletesWithoutNudge`（一次文本回复即 `completed`）、`TestMadeProgressFromCalls_NovelInfoOnlyOnce`（artifact 读回计进展、重复不计）、`TestSplitCallsByExecution_FromMetadata`（bash 仍在并行批）。
- **计划范围外的审计项保持原状**：只读宇宙元数据化（§2.5）、角色 enum 双源（§2.6）、UI 渲染元数据（§2.7）、提示块剥离边界（§4）、token 口径（§5）、安全层客观缺陷（§7）、单形态能力（§6）。见 `docs/code-review-report/deepact-2026-10-12-硬编码与护栏哲学审计.md`。

### 复核修正（2026-10-12，审查意见 #1）

**审查意见：** D4 的"未声明→串行"对 MCP 工具未生效——`splitCallsByExecution` 只把"不在 spec 列表里"视为未声明，而 MCP 包装器（`tools/mcp/tool.go` 的 `Spec()`）不设任何语义字段却仍在 `e.tools.Specs()` 里 → 被判为已声明且 `Sequential=false` → 进并行批，与 D4=a 及函数注释矛盾（后果正是审计 §2.2 要消除的"写类工具并发执行、无文件锁"）。

**核实结论：成立。** `tools/mcp/tool.go:22-26` 确认无任何语义字段；MCP 包装器经 `cmd/run.go:449 registry.Register(wrapper)` 注册，故确实出现在 spec 列表里。`madeProgressFromCalls` 已按"无法分类"信任它 → 同一工具在调度与进展两处得到不一致的分类，D4 只落地了一半。

**修法（对建议做了技术修正）：** 不复制判定式，而是抽出**共享谓词**，两处调用同一份定义，避免"同一事实两处维护"：

```go
// toolSemanticsKnown reports whether a tool told the engine what it does.
func toolSemanticsKnown(spec ModelTool) bool { return spec.Mutates || spec.NovelInfo }
```

- `splitCallsByExecution`：`unclassifiable := !declared || !toolSemanticsKnown(spec)`；`if unclassifiable || spec.Sequential` → 串行
- `madeProgressFromCalls`：同一 `unclassifiable` 走"信任为进展"分支

**新增测试（已证明非空洞）**：`TestSplitCallsByExecution_UnclassifiableRunsAlone`（已注册但无语义 → 必须串行）、`TestToolClassification_AgreesBetweenDecisions`（同一 `unclassifiable` 输入下，调度与进展两个判断必须一致）。把缺陷临时还原后复跑，两条用例精确失败（`got batch=[fs_write]` / `batch=[wrapper] sequential=[]`），恢复后通过 —— 证明测试真的守着这条规则。

**顺带写下契约**：`tools/mcp/tool.go` 的 `Spec()` 增加注释，说明 MCP 工具**有意不声明语义**，引擎按"无法分类"处理（串行 + 计进展），未来若要声明必须是逐 server 的自觉行为。

**验证证据：** `make test` → 17 行、0 FAIL；改动文件 `gofmt -l` 干净。
