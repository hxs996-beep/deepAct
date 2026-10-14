# 移除 critic 内置角色、改为 /review 内置技能 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 删除 `engine/default_agents.go` 里那个只有 3 句 persona 的 `critic` 内置角色，把"复核"变成用户按需调用的内置技能 `/review`（模型不可见、不可自动调用），复核方法论写在技能文件里，由技能委派给通用只读角色完成独立复核。

**架构：** 三个层面各自归位——① 角色层删掉 `critic`（角色只保留"能力"，不再承载方法论）；② 技能层新增 `skill/builtin/review/SKILL.md`，用 `disable-model-invocation: true` 让技能只对用户可见（`/review`），方法论（评审要求 + 输出契约）写在技能正文里，由技能脚本委派给通用 `sub` 角色执行——**独立上下文**才是复核的价值所在，所以不复用作者的上下文自查；③ UI 层把 `/review` 加进命令注册表并更新欢迎语。引擎零改动：`parseSkillCommand`（`engine/loop.go:1013`）对任意 `/名字` 已经走技能加载，`skill/builtin.go:10` 的 `go:embed builtin/*/SKILL.md` 会自动收录新目录，`cmd/run.go:175` 会把 `DisableModelInvocation` 的技能从模型目录里剔除。

**技术栈：** Go 1.2x，`go:embed`，`handoff_to_agent` 子代理委派，Bubble Tea TUI。

---

## 设计决策（本计划的取舍，实现前请确认）

| 决策 | 选择 | 理由 / 被否决的方案 |
|---|---|---|
| 复核在哪跑 | 委派给 **`sub`**（通用只读分析员），技能正文把「评审要求」原文塞进 `context` | 复核的价值是**独立上下文**（不共享作者的计划与推理）。否决"主代理在自身上下文内复核"：那正是被模型能力增强淘汰掉的"同上下文自查"形态。 |
| 为什么不给复核者新建角色 | 不建。rubric 在技能文件里，角色保持通用 | 上一轮的结论：`critic` 薄的是 persona、厚的是 rubric（codex 的 `prompts/templates/review/rubric.md`）。把 rubric 放进角色 = 又一个只能靠改代码重编译的硬编码；放进技能 = 可评审、可迭代、可被用户技能覆盖。 |
| 复核者能跑 git 吗 | 不能（保持子代理只读宇宙） | 子代理只读是核心不变量（`engine/sub_agent.go` 的 `subAgentUniverse`）。范围事实由主代理在第 1 步用 `git` 取好，作为 `context` 交给复核者。 |
| 复核完改代码吗 | 不改，只报告 | 与 codex `/review` 一致：复核结论先给用户，修不修由用户决定。 |
| 自动触发 | 不自动。`disable-model-invocation: true` | 用户要求"不放在上下文让 LLM 自己调用"。一次复核 = 一次子代理运行，成本由用户按需支付。 |

---

## 实施修正记录（执行期间偏离原规格的地方）

| # | 原规格 | 实际 | 原因 |
|---|---|---|---|
| M1 | 技能文件用中文写 | 改为**英文** | 仓库已决策内置技能文档英文化（`docs/code-review-report/deepact-2026-10-13-提示词英文单语-语言影响系统分析.md:74-75,138`），且技能正文会注入模型上下文，与 `context/promptset/en/*` 同属模型可见提示词。语言规则写在正文里（`Write goal/context and the final report in the user's language`），报告仍随用户语言 |
| M2 | 默认范围 = 未提交改动，`/review` 可裸调 | 改为**必须带范围**（`argument-hint: "<range>"`），技能正文写明裸调用不启动复核 | 质量审查发现的 P0：`engine/loop.go:547-549` 在无 task text 时直接 `return`，只回一句 "Skill loaded"，与提示文案「injected for this turn」及 2026-09-11 规格的「当轮注入全文」都不符。用户拍板不改引擎 |
| M3 | 未提及 `sub` 的完成语义 | 技能正文写明 `sub` 是结构化角色、只能用 `submit_result` 交结果，空 digest 不等于"无发现" | 质量审查 P1：同级技能 `collab/SKILL.md:28` 已明写这一点，缺失会导致空结果被误判 |
| M4 | 范围步骤只有 `git status` + `git diff HEAD` | 补上未跟踪文件处理 | 质量审查 P2：`git diff HEAD` 不含 `??` 新文件（本次新增的 SKILL.md 自己就是未跟踪文件） |
| M5 | "不报作者故意为之的改动" | 改为"**diff 中可见地**故意为之的改动"，并写明不得从推测的意图判断 | 质量审查 P1：原措辞要求 reviewer 判断它拿不到的信息（技能又刻意不传作者意图）——限定为可从 diff 判定后自相矛盾消失 |
| M6 | "宁缺毋滥"无过程证据要求 | 补上：报"无发现"时必须列出检查过的文件与行区间 | 质量审查 P2：否则"截断的运行"与"真正干净"不可区分 |
| M7 | 声称 reviewer 是纯独立上下文 | 改为不夸大：引擎会注入 "Main Agent Context (Review Starting Point)"（working set / memory markers / modified files，`engine/handoff.go:436-458`），技能无法关闭；技能只控制"不再叠加自己的结论" | 质量审查 P1：`engine/handoff.go:167-171` 对每次 depth-0 handoff 无条件注入 |
| M8 | 测试里的 `strings.Contains(spec.Function.Parameters, ...)` | 改为 `string(spec.Function.Parameters)` | `json.RawMessage` 不能直接传给 `strings.Contains`，原规格的测试代码块编译不过 |
| M9 | 未提及陈旧注释 | 顺手更新 `skill/builtin.go` 与 `skill/builtin_test.go` 里只提 "ratd/collab" 的注释 | 质量审查列为“偏好”项 |
| M10 | 欢迎语 `Use \`/review <范围>\` for an independent review **of your changes**` | 去掉 "of your changes" | 质量审查 D2：技能接受任意范围（commit / 分支 / 文档），欢迎语把范围说窄了会给出错误预期 |
| M11 | `/review` 插在 `/collab` 之后（即 `/resume` 之前） | 保留不改 | 质量审查 P1：输入 `/re` 时补全首选由 `/resume` 变为 `/review`。选择保留——三个内置技能命令放在一起是更清晰的注册表不变量，且 `/re` 首选指向哪一条都不算错。这是明确接受的取舍，不是遗漏 |

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `engine/default_agents.go` | 修改（4 处） | 删除 `AgentCritic` 常量、注册、`criticSpec()`、`criticPersona`；修正 `subSpec` 注释 |
| `engine/agent.go` | 修改（3 处） | 静态兜底 handoff schema 的角色 enum 与描述、`MaxIterations` 注释去掉 critic |
| `engine/default_agents_test.go` | 修改（3 处） | 新增"critic 必须不存在"的守卫用例；删除旧 critic 断言；换掉 `Get(AgentCritic)` |
| `engine/agent_manage_test.go` | 修改（1 处） | 测试夹具里的 `agent: "critic"` 改成仍在的角色 |
| `skill/builtin/review/SKILL.md` | **新建** | `/review` 的全部行为：定范围 → 委派独立复核 → 核验呈现。评审要求（rubric）正文即在此 |
| `skill/builtin_test.go` | 修改（1 处） | 内置技能清单加入 `review` |
| `ui/commands.go` | 修改（1 处） | 命令注册表加入 `/review`（engine-owned，`Handle` 为 nil） |
| `ui/commands_test.go` | 修改（1 处） | 命令注册表用例加入 `/review` |
| `ui/model.go` | 修改（2 处） | `/help` 欢迎语加一行；`SubAgentStatus.Agent` 注释去掉 `critic` |
| `ui/todo_render_test.go` | 修改（1 处） | 测试夹具里的 `Agent: "critic"` 改掉 |
| `engine/turn.go` | 修改（1 处） | 注释里举例的隐藏技能列表补上 `review` |
| `README.md` / `README.zh.md` | 修改（各 3 处） | 角色清单、内置技能清单、目录树说明；新增 `/review` 用法小节 |

**非目标（本计划明确不做）：**
- `docs/superpowers/{plans,specs}/*.md`、`docs/subagent-customization-evolution-design.md`、`docs/code-review-report/*` 里对 critic 的历史记载一律保留——它们是有日期的快照，不是活文档。
- `ui/allCommands()` 目前会把内置技能渲染两遍（`builtinCommands()` 一次 + `skillSuggestions` 一次，`/ratd`、`/collab` 今天就是这样）。本计划**不改**这个既有现象，`/review` 与两个同类保持一致。
- 不给 `/review` 加轮次上限、不加新工具、不改子代理只读宇宙。
- 不改 `engine/sub_agent.go`。

---

## 任务 1：删除 critic 角色

**文件：**
- 修改：`engine/default_agents.go:13,27,136-141,174-185,238-239`
- 修改：`engine/agent.go:193,357,371`
- 测试：`engine/default_agents_test.go:19,52-58,107-109`

- [ ] **步骤 1：先写红灯守卫用例**

在 `engine/default_agents_test.go` 末尾追加（`encoding/json` 需加入 import）：

```go
// TestNoCriticRole locks the removal decision: review is a user-invoked skill
// (/review) whose rubric lives in skill/builtin/review/SKILL.md, not a built-in
// role with a thin persona the delegating model can pick on its own. A role the
// model may choose costs a full sub-agent run every time it guesses; the skill
// costs one only when the user asks.
func TestNoCriticRole(t *testing.T) {
	reg := NewDefaultRegistry(&SubAgentRunner{})
	if _, err := reg.Get("critic"); err == nil {
		t.Error(`role "critic" must not be registered: review is /review, a user-invoked skill`)
	}
	// The static fallback schema is the one the model sees when the registered
	// SubAgentTool is absent (engine/sub_agent.go:1250), so its enum must drop
	// critic too, or the model can still ask for a role that no longer exists.
	spec := handoffToolSpec()
	var params struct {
		Properties struct {
			Agent struct {
				Enum []string `json:"enum"`
			} `json:"agent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &params); err != nil {
		t.Fatalf("unmarshal handoff parameters: %v", err)
	}
	for _, id := range params.Properties.Agent.Enum {
		if id == "critic" {
			t.Errorf("handoff schema enum must not offer %q, got %v", "critic", params.Properties.Agent.Enum)
		}
	}
	if !strings.Contains(string(spec.Function.Parameters), `"researcher"`) {
		t.Errorf("sanity: enum should still carry the roles that remain, got %v", params.Properties.Agent.Enum)
	}
}
```

> `spec.Function.Parameters` 是 `json.RawMessage`，传给 `strings.Contains` 前必须转 `string(...)`。

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./engine/ -run TestNoCriticRole -v`
预期：FAIL，两条都报——`role "critic" must not be registered` 与 `handoff schema enum must not offer "critic"`。

- [ ] **步骤 3：删除 critic 的常量与注册**

`engine/default_agents.go` 第 11-17 行：

```go
// Built-in agent IDs (AgentSub is declared in agent.go).
const (
	AgentResearcher AgentID = "researcher" // read-only codebase investigator
	AgentCritic     AgentID = "critic"     // adversarial reviewer
	AgentProposer   AgentID = "proposer"   // ratd: produce implementation (CodePayload JSON)
	AgentRedTeam    AgentID = "redteam"    // ratd: produce adversarial tests (TestPayload JSON)
	AgentArbitrator AgentID = "arbitrator" // ratd: judge ACCEPT/REJECT (decision JSON)
)
```

改为：

```go
// Built-in agent IDs (AgentSub is declared in agent.go).
const (
	AgentResearcher AgentID = "researcher" // read-only codebase investigator
	AgentProposer   AgentID = "proposer"   // ratd: produce implementation (CodePayload JSON)
	AgentRedTeam    AgentID = "redteam"    // ratd: produce adversarial tests (TestPayload JSON)
	AgentArbitrator AgentID = "arbitrator" // ratd: judge ACCEPT/REJECT (decision JSON)
)
```

第 22-38 行的注册函数，删掉这一行：

```go
	reg.Register(&specSubAgent{runner: runner, spec: criticSpec()})
```

- [ ] **步骤 4：删除 criticSpec 与 criticPersona，修正注释**

删除第 174-185 行整块：

```go
// criticSpec is an adversarial reviewer that checks work for blind spots.
// Tool-restricted to read-only; turn-capped so it converges quickly.
func criticSpec() AgentSpec {
	return AgentSpec{
		ID:               AgentCritic,
		Description:      "Adversarial reviewer: critically examine code or a plan for defects, blind spots, and edge cases. Returns findings, does not edit.",
		ToolNames:        []string{"read", "read_multi", "grep", "glob", "lsp"},
		MaxIterations:    15,
		StructuredResult: true,
		Persona:          criticPersona,
	}
}
```

删除第 238-239 行整块：

```go
// criticPersona is the stable system instruction for the critic role.
const criticPersona = `You are a critic — an adversarial reviewer. Your job is to examine the provided code or plan for defects, blind spots, and edge cases, and report findings. You do not edit files; you only report. Be rigorous and specific, citing file:line evidence.`
```

`subSpec` 的注释（136-141 行）现在只剩一个对照物，改为：

```go
// subSpec is the general-purpose read-only analyst: no persona, the full
// read-only universe. It is distinct from researcher (evidence-driven
// investigation with file:line citations): sub is the one-shot analyst to feed
// a large context into and get a structured conclusion back from — including
// the independent reviewer the /review skill delegates to. Sub-agents cannot
// modify files or run commands; the main agent applies changes itself.
```

- [ ] **步骤 5：同步静态兜底 schema**

`engine/agent.go:193` 注释：

```go
	MaxIterations int      // 0 = no turn cap (default). Set > 0 for agents that must finish quickly (e.g. critic: 15).
```

改为：

```go
	MaxIterations int      // 0 = no turn cap (default). Set > 0 for agents that must finish quickly (e.g. researcher: 15).
```

`engine/agent.go:357`：

```go
	agentDesc := "Target agent (role): sub (general read-only analysis), researcher (read-only investigation), critic (adversarial review), proposer/redteam/arbitrator (ratd pipeline roles)"
```

改为：

```go
	agentDesc := "Target agent (role): sub (general read-only analysis), researcher (read-only investigation), proposer/redteam/arbitrator (ratd pipeline roles)"
```

`engine/agent.go:371`：

```go
					"enum": ["sub", "researcher", "critic", "proposer", "redteam", "arbitrator"],
```

改为：

```go
					"enum": ["sub", "researcher", "proposer", "redteam", "arbitrator"],
```

- [ ] **步骤 6：改掉 default_agents_test.go 里的旧断言**

第 19 行，角色清单去掉 `AgentCritic`：

```go
	for _, id := range []AgentID{AgentSub, AgentResearcher, AgentProposer, AgentRedTeam, AgentArbitrator} {
```

第 52-58 行整块删除：

```go
	c := got[AgentCritic]
	if c.MaxIterations == 0 {
		t.Error("critic should have a turn cap")
	}
	if !c.StructuredResult {
		t.Error("critic should use structured result")
	}
```

第 107-109 行改成一个仍在的角色（`researcher` 在同用例里被用户覆盖，用 `proposer` 避免歧义）：

```go
	// Registry still has the other built-ins.
	if _, err := reg.Get(AgentCritic); err != nil {
		t.Errorf("critic should remain registered: %v", err)
	}
```

改为：

```go
	// Registry still has the other built-ins.
	if _, err := reg.Get(AgentProposer); err != nil {
		t.Errorf("proposer should remain registered: %v", err)
	}
```

- [ ] **步骤 7：运行测试确认通过**

运行：`go test ./engine/ -run 'TestNoCriticRole|TestNewDefaultRegistry|TestDefaultAgents' -v`
预期：PASS（`TestNoCriticRole` 两条断言均绿，`TestNewDefaultRegistry_BuiltinRoles` 绿）。

- [ ] **步骤 8：编译整个仓库，确认没有残留引用**

运行：`go build ./... && grep -rn "AgentCritic\|criticSpec\|criticPersona" --include=*.go .`
预期：`go build` 无输出；`grep` 无匹配（若有匹配，说明还有引用点未清理）。

- [ ] **步骤 9：Commit**

```bash
git add engine/default_agents.go engine/agent.go engine/default_agents_test.go
git commit -m "refactor(sub-agent): remove the built-in critic role"
```

---

## 任务 2：新增 /review 内置技能

**文件：**
- 创建：`skill/builtin/review/SKILL.md`
- 测试：`skill/builtin_test.go:13`

- [ ] **步骤 1：先改测试为红灯**

`skill/builtin_test.go:13`：

```go
	want := map[string]bool{"ratd": false, "collab": false}
```

改为：

```go
	want := map[string]bool{"ratd": false, "collab": false, "review": false}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./skill/ -run TestBuiltinSkills_Parsable -v`
预期：FAIL，报 `builtin skill "review" missing`。

- [ ] **步骤 3：创建技能文件**

创建 `skill/builtin/review/SKILL.md`：

````markdown
---
name: review
description: 独立代码复核 — 对指定范围的改动做一次对抗式审查，产出带优先级、置信度与 file:line 证据的发现清单，并按"作者知道后会不会修"过滤。用户按需触发。
when_to_use: 用户显式使用 /review，或明确要求复核/审查这次改动
disable-model-invocation: true
argument-hint: "[范围]"
---

# 独立代码复核（/review）

这是**用户按需发起**的复核。用户没有要求时不要自行发起；本技能只能由 `/review` 进入。

## 第 1 步：定范围（不委派）

默认范围 = 当前工作区未提交改动。用 `bash` 取事实：

```bash
git status --porcelain
git diff HEAD
```

用户指定了范围就按用户的：某个 commit 用 `git show <sha>`；对比分支用 `git diff <base>...HEAD`；方案或文档则直接读取文件。

diff 很大时不要把全文塞进上下文：给出 `git diff --stat HEAD` 与改动文件清单，让复核者自己用 read 去看这些文件。

## 第 2 步：委派给独立复核者（唯一一次）

用 `handoff_to_agent` 委派，**agent 必须是 "sub"**（通用只读分析员）。不要用其它角色，也不要自己先给结论。

- `goal`：`独立复核以下改动，只报告作者知道后一定会修的问题。`
- `context`：必须包含三部分——
  1. **「评审要求」原文**（见下节，逐字带上，不要改写）
  2. 第 1 步的范围事实（diff 全文，或 stat + 改动文件清单）
  3. 用户对范围的任何附加要求
- `expected_output`：`发现清单：每条给出 标题 / 优先级 P0-P3 / 置信度 0-1 / file:line / 为什么是问题 / 触发条件；没有任何问题时明确写"无发现"`

复核者必须是**独立上下文**：它没有作者的计划和推理过程，这正是复核的意义。不要把作者的设计意图或"我已经验证过"之类的结论写进 context，那会把复核拉回作者的锚点。

### 评审要求（原文放进 context）

- 只报三类问题：**正确性、安全、可维护性**。每条必须离散、可执行，不是对代码库的泛泛评价。
- 每条发现必须指出**确证受影响**的代码（file:line）。禁止"可能会影响别处"的猜测——要么指出受影响的确切位置，要么不报。
- 不报：纯风格/格式/typo、与本次改动无关的既有问题、作者有意为之的改动、修复它所需的严谨度超出仓库平均水平的问题。
- **宁缺毋滥**：没有任何作者一定会修的问题就报"无发现"。返回 0 条是合格结果，不要为了凑数而列。
- 优先级：P0 阻断发布或使用（不依赖任何输入假设）；P1 下个周期必须修；P2 正常，最终要修；P3 可选。
- 置信度要有区分度：确证的事实给高置信度，推断给低置信度。

## 第 3 步：核验与呈现（不委派）

拿到复核者的发现之后：

1. **逐条核验**：自己 read 被引用的行，确认发现成立。证据不成立的降级或丢弃，不要原样转述。
2. 去重、合并指向同一处的发现。
3. 给出总体结论：改动是否正确——P0/P1 为 0 才算正确。
4. **只报告，不改代码。** 用户要修再修。
5. 用 `task_complete` 结束，交付：总体结论 + 按优先级的发现清单 + 仍未验证的假设。

## 注意

- 用用户语言写 `goal` / `context` 与最终报告。
- 复核者只读、不能跑 git——范围事实由你在第 1 步提供。
- 一次复核 = 一次子代理运行。用户没要求就不要重复跑。
````

> **实施修正**：上面的技能文件实际用**英文**写成，且本节其余细节在执行中被质量审查改写（必须带范围、`sub` 的结构化完成语义、未跟踪文件、无发现需说明检查范围、"故意改动"限定为 diff 中可见、独立性表述不夸大）。逐条见本文件顶部的**实施修正记录**表。

- [ ] **步骤 4：运行测试确认通过**

运行：`go test ./skill/ -v -run TestBuiltinSkills`
预期：PASS。两个用例都绿——`TestBuiltinSkills_Parsable` 会检查新技能有 `description`/`when_to_use`、正文含 `handoff_to_agent` 与 `task_complete`（`skill/builtin_test.go:28-33`），`TestBuiltinSkills_NotModelInvocable` 会检查 `disable-model-invocation` 为真。

- [ ] **步骤 5：端到端验证 `/review` 已被引擎识别**

运行：
```bash
go build -o /tmp/deepact-review-check . && echo build-ok
```
预期：`build-ok`（技能是 `go:embed` 进去的，编译即收录；`/review` 的解析由 `parseSkillCommand` 的默认分支完成，无需改引擎）。

- [ ] **步骤 6：Commit**

```bash
git add skill/builtin/review/SKILL.md skill/builtin_test.go
git commit -m "feat(skill): add the user-invoked /review skill"
```

---

## 任务 3：UI 命令入口与欢迎语

**文件：**
- 修改：`ui/commands.go:52-53`
- 测试：`ui/commands_test.go:20-26`
- 修改：`ui/model.go:3274-3275`

- [ ] **步骤 1：先改测试为红灯**

`ui/commands_test.go:20-26`：

```go
	want := map[string]string{ // name -> args placeholder
		"/help":   "",
		"/clear":  "",
		"/ratd":   "<需求>",
		"/collab": "<需求>",
		"/resume": "",
	}
```

改为：

```go
	want := map[string]string{ // name -> args placeholder
		"/help":   "",
		"/clear":  "",
		"/ratd":   "<需求>",
		"/collab": "<需求>",
		"/review": "<范围>",
		"/resume": "",
	}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./ui/ -run TestBuiltinCommandsCoverDocumentedSlashCommands -v`
预期：FAIL，报 `registry is missing /review` 与 `registry has 5 commands, want 6`。

- [ ] **步骤 3：注册命令**

`ui/commands.go:51-54`：

```go
		{Name: "/clear", Description: "Reset session state (clear messages and context)"},
		{Name: "/ratd", Args: "<需求>", Description: "反向测试驱动：内置技能（红队写对抗测试驱动实现）"},
		{Name: "/collab", Args: "<需求>", Description: "并行研究：内置技能（拆解→并行委派调研→汇总）"},
		{Name: "/resume", Description: "恢复之前的会话", Handle: handleResumeCommand},
```

在 `/collab` 之后插入一行（`Handle` 为 nil = 由引擎接管，与 `/ratd`、`/collab` 同型）：

```go
		{Name: "/review", Args: "<范围>", Description: "独立复核：内置技能（对抗式审查指定范围的改动，输出带优先级与证据的发现）"},
```

- [ ] **步骤 4：更新 `updateSuggestions` 之外的欢迎语**

`ui/model.go:3274-3275`：

```go
	b.WriteString("Type a natural language request to start, or use `/ratd <需求>` for reverse-test-driven harness.\n")
	b.WriteString("Use `/collab <需求>` for parallel research — via built-in skills.\n")
```

改为：

```go
	b.WriteString("Type a natural language request to start, or use `/ratd <需求>` for reverse-test-driven harness.\n")
	b.WriteString("Use `/collab <需求>` for parallel research — via built-in skills.\n")
	b.WriteString("Use `/review <范围>` for an independent review of your changes — via built-in skills.\n")
```

- [ ] **步骤 5：运行测试确认通过**

运行：`go test ./ui/ -run 'TestBuiltinCommandsCoverDocumentedSlashCommands|TestLookupCommand' -v`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add ui/commands.go ui/commands_test.go ui/model.go
git commit -m "feat(ui): expose /review in the command registry"
```

---

## 任务 4：清理剩余的 critic 引用

**文件：**
- 修改：`engine/agent_manage_test.go:117,136-137`
- 修改：`ui/todo_render_test.go:63`
- 修改：`ui/model.go:80`
- 修改：`engine/turn.go:1613-1614`

- [ ] **步骤 1：改掉测试夹具里的 critic**

`engine/agent_manage_test.go:117`：

```go
		id: "bg-2", agent: "critic", goal: "审查方案",
```

改为（`researcher` 是仍在的角色，且同一轮多 researcher 本就常见）：

```go
		id: "bg-2", agent: "researcher", goal: "审查方案",
```

`engine/agent_manage_test.go:136-137`：

```go
	if !strings.HasPrefix(lines[1], "bg-2 | critic | awaiting_user") {
		t.Errorf("line 1 = %q, want the awaiting_user critic job second", lines[1])
	}
```

改为：

```go
	if !strings.HasPrefix(lines[1], "bg-2 | researcher | awaiting_user") {
		t.Errorf("line 1 = %q, want the awaiting_user researcher job second", lines[1])
	}
```

`ui/todo_render_test.go:63`：

```go
		{RunID: "bg-2", Agent: "critic", Goal: "审查", Status: "running"},
```

改为：

```go
		{RunID: "bg-2", Agent: "researcher", Goal: "审查", Status: "running"},
```

- [ ] **步骤 2：跑这两个用例确认没有被夹具改动带偏**

运行：`go test ./engine/ -run TestProcessListAgentsCalls_ShowsJobs -v && go test ./ui/ -run TestRenderStepsPanel_NestsByStepID -v`
预期：两者均 PASS（断言的是 id 顺序与状态映射，`Agent` 只参与渲染）。

- [ ] **步骤 3：改掉两处过时注释**

`ui/model.go:80`：

```go
	Agent   string // agent type e.g. "sub", "critic", "searcher"
```

改为：

```go
	Agent   string // agent type e.g. "sub", "researcher"
```

`engine/turn.go:1613-1614`：

```go
		// A model-hidden skill (disable-model-invocation, e.g. the builtin
		// multi-agent skills collab/ratd) is user-invocable only, via /name. The
```

改为：

```go
		// A model-hidden skill (disable-model-invocation, e.g. the builtin
		// collab/ratd/review) is user-invocable only, via /name. The
```

- [ ] **步骤 4：全仓确认再无可执行路径上的 critic 引用**

运行：`grep -rn "critic" --include=*.go . | grep -v "_test.go:.*critical"`
预期：无输出（`critical` 是无关英文词，不在排查范围内）。

- [ ] **步骤 5：Commit**

```bash
git add engine/agent_manage_test.go ui/todo_render_test.go ui/model.go engine/turn.go
git commit -m "chore: drop the remaining critic references"
```

---

## 任务 5：文档同步

**文件：**
- 修改：`README.md:84,90,163,192`
- 修改：`README.zh.md:84,90,163,192`

- [ ] **步骤 1：README.md 角色清单（第 163 行）**

```markdown
`handoff_to_agent` delegates work to sub-agents with distinct built-in roles — `sub` (general read-only analysis), `researcher` (read-only investigation), `critic` (adversarial review), plus the `ratd` pipeline roles `proposer` / `redteam` / `arbitrator`. Multiple handoffs issued in one turn run **in parallel** (`tools/registry.go` spawns one goroutine per tool call), with results merged back into the main loop — fast without getting messy.
```

改为：

```markdown
`handoff_to_agent` delegates work to sub-agents with distinct built-in roles — `sub` (general read-only analysis), `researcher` (read-only investigation), plus the `ratd` pipeline roles `proposer` / `redteam` / `arbitrator`. Multiple handoffs issued in one turn run **in parallel** (`tools/registry.go` spawns one goroutine per tool call), with results merged back into the main loop — fast without getting messy. Code review is not a role: it is the user-invoked `/review` skill (see below), so a review costs a sub-agent run only when you ask for one.
```

- [ ] **步骤 2：README.md 内置技能清单（第 90 行）与目录树（第 192 行）**

第 90 行 `go:embed: `ratd` / `collab`` → `go:embed: `ratd` / `collab` / `review``。
第 192 行 `skill/    built-in (ratd/collab) + external skill loading` → `skill/    built-in (ratd/collab/review) + external skill loading`。

- [ ] **步骤 3：README.md 新增 /review 小节（插在第 84 行 `/collab` 段落之后）**

```markdown
### Independent Review (/review)

```bash
deepact exec "/review 当前改动"
deepact exec "/review 只复核 prompt 改动"
deepact exec "/review 对比 main 分支"
```

`/review` is a built-in skill you invoke, not something the agent starts on its own (it is excluded from the model-facing skill catalog). The text after the command is the range — the command needs one, because a bare `/review` only loads the skill and starts no run. It maps the range to facts (`git status` + `git diff HEAD`, a commit, a branch, a document), hands the diff plus a review rubric to an **independent** `sub` sub-agent — a fresh context with no memory of the author's plan — then verifies each finding against the code before reporting. The reviewer is read-only: findings carry a priority (P0–P3), a confidence score and a `file:line` citation, and nothing is fixed until you ask.
```

- [ ] **步骤 4：README.zh.md 同步三处**

第 163 行改为：

```markdown
`handoff_to_agent` 委派给不同内置角色的子代理——`sub`（通用只读分析）、`researcher`（只读调研），以及 `ratd` 流水线角色 `proposer` / `redteam` / `arbitrator`。同一轮发出的多个 handoff **并行**执行（`tools/registry.go` 每个工具调用一个 goroutine），结果汇聚回主循环——快而不乱。代码复核不是角色：它是用户主动调用的 `/review` 技能（见下），所以你要求时才付一次子代理运行的成本。
```

第 90 行 `go:embed：`ratd` / `collab`` → `go:embed：`ratd` / `collab` / `review``。
第 192 行 `skill/    内置（ratd/collab）+ 外部技能加载` → `skill/    内置（ratd/collab/review）+ 外部技能加载`。
第 84 行之后新增小节：

```markdown
### 独立复核（/review）

```bash
deepact exec "/review 当前改动"
deepact exec "/review 只复核 prompt 改动"
deepact exec "/review 对比 main 分支"
```

`/review` 是**你主动调用**的内置技能，agent 不会自己发起（它被排除在模型可见的技能目录之外）。命令后面的文字就是复核范围——必须给，因为裸 `/review` 只会加载技能、不启动任何运行。它把范围映射成事实（`git status` + `git diff HEAD`、某个 commit、某个分支、或某个文档），将 diff 加一份评审要求交给**独立**的 `sub` 子代理——一份没有作者计划记忆的新上下文——然后逐条核验发现再报告。复核者只读：每条发现带优先级（P0–P3）、置信度与 `file:line` 证据，未经你同意不改任何代码。
```

- [ ] **步骤 5：Commit**

```bash
git add README.md README.zh.md
git commit -m "docs: document /review and drop the critic role"
```

---

## 任务 6：全量验证

**文件：** 无（只验证）

- [ ] **步骤 1：全仓构建**

运行：`go build ./...`
预期：无输出。

- [ ] **步骤 2：全仓测试（含竞态）**

运行：`go test -race ./... 2>&1 | tail -30`
预期：全部 `ok`，无 FAIL。

- [ ] **步骤 3：人工端到端冒烟**

运行：`go run . exec "/review"`
预期：
1. 界面/输出先出现 `✓ Skill review loaded...`（`engine/loop.go:548` 的加载反馈）；
2. 主代理执行 `git status --porcelain` 与 `git diff HEAD`；
3. 一次 `handoff_to_agent(agent="sub")`，子代理行出现在步骤面板；
4. 主代理逐条核验后给出带优先级的发现清单，并 `task_complete`。

若第 2 步之后主代理**没有**委派而是自己下了结论：说明技能正文第 2 步的约束不够硬，回到任务 2 步骤 3 把 `agent 必须是 "sub"` 那句改成更明确的祈使句（这是本计划唯一预期需要按真实模型表现微调的地方）。

- [ ] **步骤 4：确认模型侧不可见**

运行：`go run . exec "/skills" | grep -n "review"`
预期：能列出（用户可见）；同时在一次普通会话的稳定区里搜不到 `review` 条目——`cmd/run.go:175` 会按 `DisableModelInvocation` 跳过它。

- [ ] **步骤 5：最终 Commit（若有微调）**

```bash
git add -A && git commit -m "chore(review): finalize /review after end-to-end smoke"
```

---

## 自检

**1. 规格覆盖度**
- 删除 critic 角色 → 任务 1（常量/注册/spec/persona/静态 schema/测试）
- 换成 `/review` 内置技能 → 任务 2（SKILL.md + 内置技能清单测试）
- 不放在上下文让 LLM 自己调用 → `disable-model-invocation: true`（任务 2 步骤 4 的 `TestBuiltinSkills_NotModelInvocable` 守住）+ `cmd/run.go:175` 的目录过滤（任务 6 步骤 4 验证）
- 用户按需自己搞 → 任务 3（命令注册表 + 欢迎语），无自动触发路径
- 残留引用清零 → 任务 4 + 任务 1 步骤 8 的 grep
- 文档一致 → 任务 5
- 验证 → 任务 6

**2. 占位符扫描**：无"待定/TODO/类似任务 N"；每个代码步骤都给了可直接粘贴的代码块与精确命令。

**3. 类型/命名一致性**：`AgentCritic` 全仓删除，测试改用 `AgentProposer`（`engine/default_agents_test.go`）与 `researcher`（夹具）；技能名 `review` 在 `skill/builtin/review/SKILL.md`、`skill/builtin_test.go`、`ui/commands.go`、`ui/commands_test.go`、README 五处写法一致；`Args` 占位符 `[范围]` 在 `ui/commands.go` 与 `ui/commands_test.go` 保持一致。

**4. 已知后果（不是缺陷，是既定取舍）**
- `ui/allCommands()` 会让 `/review` 在 `/help` 与 `/` 弹窗里出现两次（`builtinCommands()` 一次、`skillSuggestions` 一次）——`/ratd`、`/collab` 今天就是这样；已在非目标里声明不改。
- 一次 `/review` = 一次 `sub` 子代理运行。这是"独立上下文"的价格，也是把它做成用户触发的理由。
- 子代理只读、不能跑 git，所以 diff 由主代理搬运；超大 diff 走 `--stat` + 文件清单的路径（技能第 1 步已写）。

---

## 执行交接

计划已保存到 `docs/superpowers/plans/2026-10-14-remove-critic-role-add-review-skill.md`。两种执行方式：

**1. 子代理驱动（推荐）** — 每个任务派一个新的子代理，任务间做审查，迭代快

**2. 内联执行** — 在当前会话里按 executing-plans 批量执行，设检查点
