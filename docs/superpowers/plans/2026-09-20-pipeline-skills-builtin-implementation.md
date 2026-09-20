# 管线技能化（ratd/collab/debate 内置技能 + 引擎减法）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 按已批准规格 `docs/superpowers/specs/2026-09-18-pipeline-skills-and-docs-sync-design.md`，把 `/ratd`、`/collab` 从引擎硬编码状态机改造为 `go:embed` 内置技能（含新增 `/debate`），并做文档/UI 同步与目录清理。实现后引擎零场景枚举，多角色协作方法论全部是 SKILL.md 数据。

**架构：** 新增 `skill/builtin/` 三个 SKILL.md + `skill/builtin.go`（go:embed + `BuiltinSkills()`），注册到 `cmd/run.go` 技能装配最前（最低优先级，用户目录同名覆盖）。删除 `engine/collab.go`、`engine/ratd.go` 及 types.go/loop.go 中相关状态机类型与分派（约 1160 行），删 `RunWithPrompt` 专用通道。`/ratd`、`/collab` 命令改由 `parseSkillCommand` default 分支自然命中技能，主 agent 按技能提示自主编排 handoff。UI 删 member_start/member_done 死代码，帮助行/欢迎语改技能语义。README/CLAUDE.md/check-archs 文档同步，删 `app/` `retrieval/`。

**技术栈：** Go（标准库 + `embed`）、现有 `skill` 包（`ParseMarkdownSkill`/`LoadExternalSkillsFromPaths`）、`tools/builtin/pathutil.go`。

**关键前提（已核实，实现时无需再查）：**
- `topLevelJSONObjects`（engine/ratd.go:150）仅被 ratd.go/collab.go 使用，随删；无其他调用方。
- `errText`（engine/collab.go:337）仅被 collab.go 使用，随删。
- `RunWithPrompt` 生产调用方仅 collab.go/ratd.go（`promptRunner` 接口本地声明），sub_agent.go:145 与 default_agents.go:31 可一并删除。
- `tools/builtin/pathutil.go:39` 有独立 `resolveSafePath`，engine 内副本（ratd.go:290-359 的 resolveSafePath/safeWriteFile/safeRemoveFile）是 RATD 专用重复代码，随 ratd.go 删除。
- `roundtable_enter`/`roundtable_phase`/`member_start`/`member_done` 引擎侧已无事件源（唯一 member_* 事件源是 collab.go:238/250），UI 内 roundtable/member 分支全是死代码。
- `parseSkillCommand`（engine/loop.go:1245-1267）default 分支认合法技能名并 `Get`；内置技能注册后 `/ratd` `/collab` 自然命中加载路径，无需命令分派。
- 并行 handoff 已天然存在：`turn.go:599` 批量喂 `Execute` → `tools/registry.go:98` 每调用一 goroutine。

---

## 文件结构

| 文件 | 职责 | 操作 |
|---|---|---|
| `skill/builtin/ratd/SKILL.md` | /ratd 方法论（角色提示 + 输出契约 + 编排规则 + 完成判据） | 创建 |
| `skill/builtin/collab/SKILL.md` | /collab 方法论（拆解/Worker 角色 + 汇总 + 完成判据） | 创建 |
| `skill/builtin/debate/SKILL.md` | /debate 方法论（四角色 + 评分 + 实施蓝图） | 创建 |
| `skill/builtin.go` | `//go:embed builtin/*/SKILL.md` + `BuiltinSkills()` | 创建 |
| `skill/builtin_test.go` | embed 资产可解析、frontmatter 完整、名字合法、关键段落存在 | 创建 |
| `engine/collab.go` | 删除（整文件） | 删除 |
| `engine/ratd.go` | 删除（整文件） | 删除 |
| `engine/types.go` | 删 Collab/RATD 类型 + TaskState 字段 + ProgressEvent 注释 | 修改 |
| `engine/loop.go` | 删命令分派块、arena 分派块、hall 字段与装配 | 修改 |
| `engine/sub_agent.go` | 删 `RunWithPrompt` | 修改 |
| `engine/default_agents.go` | 删 `genericSubAgent.RunWithPrompt` | 修改 |
| `engine/collab_test.go`、`engine/ratd_test.go`、`engine/ratd_parse_test.go` | 删除（状态机测试随实现消失） | 删除 |
| `cmd/run.go` | 技能装配前置注册内置技能（最低优先级） | 修改 |
| `ui/model.go` | 删 member_start/member_done 死代码；帮助行/欢迎语改技能语义；加 /debate | 修改 |
| `README.md` / `README.zh.md` | 架构图、/ratd /collab /debate 段、Parallel Subagents、技能优先级表 | 修改 |
| `CLAUDE.md` | 分层规则改真实依赖描述 | 修改 |
| `.claude/skills/check-archs/SKILL.md` | 检查规则同步真实拓扑 | 修改 |
| `docs/archive/DESIGN.md` | 文件头加历史注记 | 修改 |
| `app/`、`retrieval/` | 删除空壳目录 | 删除 |

---

### 任务 1：内置技能资产（skill/builtin/ 三个 SKILL.md）

**文件：**
- 创建：`skill/builtin/ratd/SKILL.md`、`skill/builtin/collab/SKILL.md`、`skill/builtin/debate/SKILL.md`
- 测试：`skill/builtin_test.go`（本任务只建资产，测试在任务 2 一并写）

- [ ] **步骤 1：创建 `skill/builtin/ratd/SKILL.md`**

从 `engine/ratd.go` 的 `ratdRolePrompt`（:54）、`buildProposerGoal`（:76）、`buildRedTeamGoal`（:101）、`buildArbitratorGoal`（:114）、`sandboxCommand`（:420）、`codePayloadJSON`（:139）、`testPayloadJSON`（:217）、`parseArbitration`（:263）迁移内容。frontmatter 用标准 Claude Code 布局：

```markdown
---
name: ratd
description: 反向测试驱动开发（Red-Team-Then-Drive）：由红队写对抗测试，再驱动实现。需要严格验证实现、防止过度自信时使用。
when_to_use: 用户要求"先写测试再实现"、需要对抗性验证、或明确使用 /ratd 时
argument-hint: "<需求>"
---

# 反向测试驱动（/ratd）

## 角色与编排（模型自主按此剧本编排，非硬流程）

你是编排者，用 `handoff_to_agent`（agent=sub）逐角色委派，每角色一次委派、轮数上限 3 轮：

### 1. Proposer（提议者）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「提议者」——资深工程师。基于需求产出实现代码。只输出 CodePayload JSON：
> ```json
> {"language":"go","files":[{"path":"相对路径","content":"完整代码"}],"design_notes":"设计说明"}
> ```
> 约束：单文件优先；不得写测试；代码必须自洽可编译。

### 2. RedTeam（红队）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「红队」——对抗性测试工程师。针对实现产出对抗测试。只输出 TestPayload JSON：
> ```json
> {"tests":[{"path":"相对路径","content":"测试代码"}],"no_issues":false}
> ```
> 若无问题则输出 `{"tests":[],"no_issues":true}`。

### 3. Sandbox（引擎侧执行）
- 由你（编排者）用 `bash` 执行测试命令，语言映射：go→`go test -timeout 60s ./...`。
- 失败时收集输出。

### 4. Arbitrator（仲裁员）
把下面角色提示原文放入 handoff goal，tools 白名单 `read/grep/glob/lsp`：

> 你是「仲裁员」。基于失败测试与沙箱输出判定：`{"decision":"ACCEPT","feedback":"重构方向"}` 或 `{"decision":"REJECT","feedback":"红队测试缺陷"}`。
> ACCEPT=实现有问题，回到 Proposer 重构；REJECT=测试本身有问题，删除该测试文件回到 RedTeam。

## 完成判据
- 沙箱通过且红队无问题（no_issues=true）→ `task_complete`，交付摘要含：改动文件清单 + 最终测试结果。
- 循环由你按纪律控制（≤3 轮），`LoopTracker` 兜底防角色循环。

## 注意
- 会话语言为中文时使用上面中文角色提示；英文时译为英文。
- 两语言变体语义必须一致。
```

- [ ] **步骤 2：创建 `skill/builtin/collab/SKILL.md`**

从 `engine/collab.go` 的 `collabResearchRolePrompt`（:113）、`buildDecomposerGoal`（:177）、`buildSynthesizerGoal`（:294）迁移。按规格：**拆解与汇总由主模型自身承担，仅 Worker 走 handoff**。

```markdown
---
name: collab
description: 并行研究：把研究目标拆成多个互不重叠方向，并行委派子代理调研，再汇总成报告。需要多方向/多模块调研、交叉验证的深度分析时使用。
when_to_use: 用户要求"调研""分析""研究"某主题、涉及多个模块/方向、或明确使用 /collab 时
argument-hint: "<研究目标>"
---

# 并行研究（/collab）

## 编排（模型自主）

1. **拆解**（你自己做，不委派）：把研究目标拆成 2-6 个互不重叠的研究方向。每个方向自包含——独立子代理无共享上下文，仅凭方向描述就能开工。
2. **并行委派**：对每个方向发起一次 `handoff_to_agent`（agent=sub），**一轮内并行发出多个 handoff**（引擎天然并行）。每个 handoff 参数：
   - `goal`：把下面 Worker 角色提示原文 + 研究方向放入
   - `tools`：`["read","grep","glob","lsp"]`
   - `expected_output`：`"研究小结：结论 + 关键证据 file:line 引用"`
   - `constraints`：`["只读调研，不改任何文件"]`
3. **汇总**（你自己做，不委派）：合并各 worker 报告，做交叉分析（找矛盾、验证证据、标注未验证假设），产出结构化研究报告。

## Worker 角色提示（放入 handoff goal）

> 你是「研究员」——独立研究者。你的任务是用只读工具彻底调研一个研究方向，产出简洁研究小结，附具体证据（file:line 引用）。不要改任何文件。
>
> 研究方向：<此处放方向描述>

## 完成判据
- 所有方向调研完毕 → `task_complete`，交付研究报告：总体结论 + 各方向发现 + 跨方向综合分析 + 未验证假设。
- 单方向失败：你自己决定重试/降级/标注，不隐藏失败。

## 注意
- 会话语言为中文时使用上面中文角色提示；英文时译为英文。
- 深度纪律：每个方向必须真实读代码取证，禁止凭印象；worker 结果中的 file:line 你应抽查复核。
```

- [ ] **步骤 3：创建 `skill/builtin/debate/SKILL.md`**

新增模式（README 承诺过、从未实现）。规格 :106 定义四角色并行辩论 + 评分 + 实施蓝图。

```markdown
---
name: debate
description: 多角色辩论：让多个不同立场的角色并行发表方案，评分选出胜者并改写为实施蓝图。需要多角度权衡方案时使用。
when_to_use: 用户要求"辩论""多方案对比""权衡取舍"、或明确使用 /debate 时
argument-hint: "<议题>"
---

# 多角色辩论（/debate）

## 编排（模型自主）

1. **并行委派**：对四个角色各发起一次 `handoff_to_agent`（agent=sub），**一轮内并行发出**。每个 handoff 的 goal 包含：角色提示 + 议题 + `expected_output: "你的方案：立场 + 具体方案 + 理由"`，tools 白名单 `read/grep/glob/lsp`。
2. **评分**（你自己做）：按 方案可行性 / 风险意识 / 创新性 / 可实施性 四维评分（每维 1-5），取平均分最高者为胜者。
3. **改写实施蓝图**（你自己做）：把胜者方案改写为可直接实施的蓝图：目标 + 逐文件改动 + 验证方式 + 风险与回退。

## 角色（放入各自 handoff goal）

- **创新者**：天马行空，提出突破性方案，不惧风险。
- **防守者**：聚焦稳定性与风险，指出漏洞与回退路径。
- **务实者**：关注成本与可实施性，给最简可行方案。
- **用户代言人**：站在用户视角，强调体验与真实需求。

## 完成判据
- 四角色全部发言完毕 → 评分 → `task_complete`，交付：各角色方案摘要 + 评分表 + 胜者实施蓝图。

## 注意
- 会话语言为中文时使用上面中文角色；英文时译为英文。
- 角色无实际 tool 需求时也保留 handoff（保证独立上下文与独立推理）。
```

- [ ] **步骤 4：运行 `gofmt` 校验（无 Go 代码，跳过）；人工检查 frontmatter 完整**

三个 SKILL.md 的 frontmatter 必须含 `name`/`description`/`when_to_use`/`argument-hint`，`name` 与目录名一致（ratd/collab/debate），均为合法技能名（小写字母/数字/`-`/`_`，≤30 字符）。

- [ ] **步骤 5：Commit**

```bash
git add skill/builtin/
git commit -m "feat(skill): add built-in pipeline skills (ratd/collab/debate) as SKILL.md"
```

---

### 任务 2：内置技能通道（skill/builtin.go + skill/builtin_test.go）

**文件：**
- 创建：`skill/builtin.go`
- 创建：`skill/builtin_test.go`

- [ ] **步骤 1：编写失败的测试**

创建 `skill/builtin_test.go`：

```go
package skill

import (
	"strings"
	"testing"
)

func TestBuiltinSkills_Parsable(t *testing.T) {
	skills, err := BuiltinSkills()
	if err != nil {
		t.Fatalf("BuiltinSkills() error: %v", err)
	}
	want := map[string]bool{"ratd": false, "collab": false, "debate": false}
	for _, s := range skills {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected builtin skill %q", s.Name)
		}
		want[s.Name] = true
		if s.Description == "" {
			t.Errorf("skill %q missing description", s.Name)
		}
		if s.WhenToUse == "" {
			t.Errorf("skill %q missing when_to_use", s.Name)
		}
		if !isValidSkillName(s.Name) {
			t.Errorf("skill %q has invalid name", s.Name)
		}
		// 关键结构段落存在：角色提示词 / 完成判据
		if !strings.Contains(s.Content, "handoff_to_agent") {
			t.Errorf("skill %q content should mention handoff_to_agent", s.Name)
		}
		if !strings.Contains(s.Content, "task_complete") {
			t.Errorf("skill %q content should mention task_complete", s.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("builtin skill %q missing", name)
		}
	}
}

// isValidSkillName 复制 engine/loop.go 的校验逻辑（同包不能引 engine，避免循环依赖）。
func isValidSkillName(name string) bool {
	if len(name) == 0 || len(name) > 30 {
		return false
	}
	for _, r := range name {
		if r != '-' && r != '_' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
```

> 注：`skill` 包内测试用 `isValidSkillName` 需自行定义（skill 包不依赖 engine）。若 `BuiltinSkills()` 尚未定义，编译失败——符合 RED。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./skill/ -run TestBuiltinSkills -v`
预期：编译失败——`BuiltinSkills` 未定义。

- [ ] **步骤 3：编写最少实现代码**

创建 `skill/builtin.go`：

```go
package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
)

//go:embed builtin/*/SKILL.md
var builtinSkillFS embed.FS

// BuiltinSkills 解析嵌入的官方保留技能（ratd/collab/debate）。
// 内置技能是最低优先级：cmd/run.go 先注册内置、后注册用户目录，同名覆盖。
func BuiltinSkills() ([]*Skill, error) {
	var skills []*Skill
	entries, err := fs.ReadDir(builtinSkillFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("read builtin skills dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		data, err := builtinSkillFS.ReadFile(filepath.Join("builtin", name, "SKILL.md"))
		if err != nil {
			return nil, fmt.Errorf("read builtin skill %s: %w", name, err)
		}
		sf, err := ParseMarkdownSkill(data)
		if err != nil {
			return nil, fmt.Errorf("parse builtin skill %s: %w", name, err)
		}
		if sf.Name == "" {
			sf.Name = name
		}
		sf.BaseDir = filepath.Join("builtin", name)
		skills = append(skills, SkillFromSkillFile(sf))
	}
	return skills, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./skill/ -run TestBuiltinSkills -v`
预期：PASS（三个技能全部解析、frontmatter 完整、含关键段落）。

- [ ] **步骤 5：Commit**

```bash
git add skill/builtin.go skill/builtin_test.go
git commit -m "feat(skill): add go:embed builtin skills channel (BuiltinSkills)"
```

---

### 任务 3：cmd/run.go 注册内置技能（最低优先级）

**文件：**
- 修改：`cmd/run.go`（技能装配前置注册）
- 测试：无独立单测（由任务 2 的 BuiltinSkills 测试覆盖解析；装配逻辑靠全量回归）

- [ ] **步骤 1：在 `cmd/run.go` 技能装配处前置注册内置技能**

在 `skillReg := skill.NewRegistry()` 之后、`LoadExternalSkillsFromPaths` 之前插入：

```go
	// 内置官方技能（ratd/collab/debate）最先注册，作为最低优先级。
	// 用户目录同名技能按"后者覆盖前者"规则覆盖内置技能。
	builtinSkills, err := skill.BuiltinSkills()
	if err != nil {
		return engine.EngineConfig{}, engine.EngineDeps{}, fmt.Errorf("load builtin skills: %w", err)
	}
	for _, s := range builtinSkills {
		skillReg.Register(s)
	}
```

- [ ] **步骤 2：构建 + 全量测试**

运行：`go build ./... && go test ./cmd/ ./skill/ -count=1`
预期：全部 PASS。

- [ ] **步骤 3：Commit**

```bash
git add cmd/run.go
git commit -m "feat(cmd): register builtin pipeline skills at lowest priority"
```

---

### 任务 4：引擎减法 — 删除状态机

**文件：**
- 删除：`engine/collab.go`、`engine/ratd.go`
- 删除：`engine/collab_test.go`、`engine/ratd_test.go`、`engine/ratd_parse_test.go`
- 修改：`engine/types.go`（删 Collab/RATD 类型 + TaskState 字段 + ProgressEvent 注释）
- 修改：`engine/loop.go`（删命令分派块、arena 分派块、hall 字段与装配、Run 末尾清理）
- 修改：`engine/sub_agent.go`（删 `RunWithPrompt`）
- 修改：`engine/default_agents.go`（删 `genericSubAgent.RunWithPrompt`）

- [ ] **步骤 1：删除状态机源文件与测试文件**

运行：
```bash
rm engine/collab.go engine/ratd.go engine/collab_test.go engine/ratd_test.go engine/ratd_parse_test.go
```

- [ ] **步骤 2：修改 `engine/types.go`**

删除：
- `TaskState` 中 `Collab *CollabState` 与 `RATD *RATDState` 两字段（:258-259）
- `CollabPhase`/`CollabTask`/`CollabState`（:345-372）
- `RATDPhase`/`RATDSourceFile`/`RATDTest`/`RATDSandboxResult`/`RATDState`（:374-432）
- `ProgressEvent.Type` 注释中的 `"ratd_role"` 枚举（:36）

具体删除块示例（types.go:345-432 整段）：

```go
// CollabPhase describes which stage of the /collab parallel research we are in.
type CollabPhase int

const (
	CollabIdle       CollabPhase = iota
	CollabDecompose              // 拆解：LLM 拆解 agent 产出任务列表
	CollabParallel               // 并行：并发执行各任务（只读调研）
	CollabSynthesize             // 汇总：LLM 合并各 worker 报告
	CollabDone                   // 完成：展示最终报告并清理
)
// ...（CollabTask/CollabState/RATDPhase/RATDSourceFile/RATDTest/RATDSandboxResult/RATDState 全部删除）
```

TaskState 中删除：
```go
	Collab              *CollabState     `json:"collab,omitempty"`
	RATD                *RATDState       `json:"ratd,omitempty"`
```

ProgressEvent 注释改为：
```go
	Type       string // "tool_start" | "tool_done" | "thinking" | "content_delta" | "reasoning_delta" | "agent_start" | "agent_done" | "usage" | "todo_update"
```

- [ ] **步骤 3：修改 `engine/loop.go`**

删除：
- `ratdHall` 字段（:99-100）、`collabHall` 字段（:102-103）
- `NewEngine` 中 `e.ratdHall = NewRATDHall(e)`（:184）、`e.collabHall = NewCollabHall(e)`（:185）
- `/ratd` 命令分派块（:306-317）与 `/collab` 命令分派块（:319-332）
- arena 分派块（:629-644）——`if e.state.Collab != nil { ... }` 与 `if e.state.RATD != nil { ... }`
- Run 末尾 `CollabDone`/`RATDDone` 清理（:830-833 附近）

删除命令分派块示例（:306-332 整段）：

```go
	if rc := parseRATDCommand(userMsg); rc != nil {
		e.state.RATD = &RATDState{ ... }
		...
	}
	if cc := parseCollabCommand(userMsg); cc != nil {
		e.state.Collab = &CollabState{ ... }
		...
	}
```

删除 arena 分派块示例（:629-644 整段）：

```go
	// Collab parallel research phase — run decompose → parallel → synthesize.
	if e.state.Collab != nil {
		phase := e.state.Collab.Phase
		switch phase {
		case CollabDecompose, CollabParallel, CollabSynthesize:
			...
		case CollabDone:
			e.state.Collab = nil
		}
	}
	// RATD harness phase — run propose → redteam → sandbox → arbitrate.
	if e.state.RATD != nil { ... }
```

- [ ] **步骤 4：修改 `engine/sub_agent.go`**

删除 `RunWithPrompt`（:141-147）：

```go
// RunWithPrompt runs a sub-agent with an extra system-level instruction prompt
// prepended to the volatile content. This is used by roundtable member agents
// that need a role-specific instruction (e.g. "你是一位安全工程师...") injected
// as a high-priority user message after the stable system prompt.
func (r *SubAgentRunner) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	return r.runLoop(ctx, input, extraPrompt, input.MaxIterations)
}
```

> 注：`runLoop` 的 `extraPrompt` 参数仍被 `Run` 调用（传 ""），保留签名不动；仅删 `RunWithPrompt` 公开方法。

- [ ] **步骤 5：修改 `engine/default_agents.go`**

删除 `genericSubAgent.RunWithPrompt`（:31-34）：

```go
func (a *genericSubAgent) RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error) {
	input.StructuredResult = a.Spec().StructuredResult
	return a.runner.RunWithPrompt(ctx, input, extraPrompt)
}
```

- [ ] **步骤 6：构建 + 测试，修复所有残留引用**

运行：`go build ./... 2>&1 | head -50`
预期：可能报错——`ratdHall`/`collabHall`/`CollabState` 等残留引用。逐一修复：
```bash
grep -rn "ratdHall\|collabHall\|CollabState\|RATDState\|parseRATDCommand\|parseCollabCommand\|CollabPhase\|RATDPhase\|RATDSourceFile\|RATDTest\|RATDSandboxResult\|CollabTask\|RunWithPrompt\|promptRunner" --include="*.go" engine/ cmd/ ui/ | grep -v _test
```
预期：除测试文件外零命中（测试文件引用随删除的文件消失）。

运行：`go test ./engine/ ./cmd/ ./ui/ -count=1`
预期：全部 PASS。

- [ ] **步骤 7：Commit**

```bash
git add -A engine/ cmd/ ui/
git commit -m "refactor(engine): remove ratd/collab state machines, delegate to builtin skills

Delete CollabHall/RATDHall (~1160 lines), Collab/RATD state types, command
dispatch blocks, and the RunWithPrompt channel. Multi-agent pipeline
methodology now lives in skill/builtin SKILL.md data; the main agent
orchestrates handoffs per the skill instructions."
```

---

### 任务 5：UI 清理

**文件：**
- 修改：`ui/model.go`

- [ ] **步骤 1：删除 member_start/member_done 死代码**

删除 `ui/model.go` 中 `case "member_start":`（:524-543）与 `case "member_done":`（:544-556）两个分支。它们是 legacy debate UI，引擎已无事件源。

同时删除仅被这两个分支使用的辅助：`memberAvatar`（:2330-2342）、`renderMemberProgress`（:2836-2870）、`renderOverlayStatus`（:2896-2925）——先 grep 确认无其他调用：

```bash
grep -n "memberAvatar\|renderMemberProgress\|renderOverlayStatus" ui/model.go
```

> 若 `renderOverlayStatus` 还被 `renderOverlayStatus(m.todoItems, m.memberStatuses, width)`（:1958）调用，删分支后 `memberStatuses` 恒空，需同步简化该调用点（保留 todo 渲染，去掉 members 参数）。**实现时仔细核对调用图，只删确认死代码，不碰 todo 渲染。**

- [ ] **步骤 2：更新 slashCommands 帮助行**

`ui/model.go:91-96` 改为：

```go
var slashCommands = []Suggestion{
	{Command: "/help", Args: "", Description: "Show this help screen"},
	{Command: "/clear", Args: "", Description: "Reset session state (clear messages and context)"},
	{Command: "/ratd", Args: "<需求>", Description: "反向测试驱动：内置技能（红队写对抗测试驱动实现）"},
	{Command: "/collab", Args: "<需求>", Description: "并行研究：内置技能（拆解→并行委派调研→汇总）"},
	{Command: "/debate", Args: "<议题>", Description: "多角色辩论：内置技能（并行发言→评分→实施蓝图）"},
	{Command: "/resume", Args: "", Description: "恢复之前的会话"},
}
```

- [ ] **步骤 3：更新欢迎语**

`ui/model.go:3171-3182` 末尾改为：

```go
	b.WriteString("Type a natural language request to start, or use `/ratd <需求>` for reverse-test-driven harness.\n")
	b.WriteString("Use `/collab <需求>` for parallel research, `/debate <议题>` for multi-role debate — all via built-in skills.\n")
```

- [ ] **步骤 4：构建 + 测试**

运行：`go build ./ui/ && go test ./ui/ -count=1`
预期：全部 PASS。若删成员渲染导致测试失败，检查 `ui/model_test.go` 是否有成员卡片断言，同步更新。

- [ ] **步骤 5：Commit**

```bash
git add ui/model.go ui/model_test.go
git commit -m "refactor(ui): drop legacy member cards, update /ratd /collab help to skill semantics, add /debate"
```

---

### 任务 6：文档同步 + 目录清理

**文件：**
- 修改：`README.md`、`README.zh.md`
- 修改：`CLAUDE.md`
- 修改：`.claude/skills/check-archs/SKILL.md`
- 修改：`docs/archive/DESIGN.md`
- 删除：`app/`、`retrieval/`

- [ ] **步骤 1：README.md 架构图重画为真实拓扑**

`README.md:204-208` 的架构块替换：

```text
cmd/      CLI entry (Cobra)         ui/       Terminal UI (Bubble Tea)
engine/   agent loop · subagents · shared type hub   policy/  → removed
context/  prompt build · tree snapshot · compaction   llm/      DeepSeek client (stream·retry·rate)
tools/    built-in tools + MCP      router/    model routing
session/  JSONL sessions·fork·rewind  artifact/ content-addressed store·auto-redact
skill/    built-in + external skill loading    config/    shared config
```

改为（删 policy/，加内置技能说明）：

```text
cmd/      CLI entry (Cobra)         ui/       Terminal UI (Bubble Tea)
engine/   agent loop · subagents · shared type hub
context/  prompt build · tree snapshot · compaction   llm/      DeepSeek client (stream·retry·rate)
tools/    built-in tools + MCP      router/    model routing
session/  JSONL sessions·fork·rewind  artifact/ content-addressed store·auto-redact
skill/    built-in (ratd/collab/debate) + external skill loading    config/    shared config
```

Layering rules 改为真实依赖描述：engine 是共享类型/接口中枢（hub-and-spoke），llm/tools 核心文件零项目依赖、由 adapter 文件桥接到 engine 类型。

- [ ] **步骤 2：README 三模式段改写为内置技能机制**

`README.md:107-121`（/debate + /collab 段）与 `/ratd` 段（如有）改写：命令语法不变，说明由内置技能驱动、主 agent 按技能提示自主编排 handoff；删 `--members`/`--add` 参数（从未实现）。

`README.md:183` "Parallel Subagents" 段改为真实情况：单个通用 sub agent + `handoff_to_agent` 并行委托（`tools/registry.go` 每调用一 goroutine）。

`README.md:48` 首段 "team collaboration, parallel subagents" 措辞保留但可补充 "via built-in pipeline skills"。

- [ ] **步骤 3：README.zh.md 同步**

同上，中文版本。

- [ ] **步骤 4：技能优先级表头部加内置技能行**

README 中技能优先级说明（若有表）头部加：内置技能（ratd/collab/debate）最低优先级，可被用户目录同名覆盖。

- [ ] **步骤 5：CLAUDE.md 分层规则同步**

将 `CLAUDE.md` 分层规则改为真实依赖描述（engine 是共享类型/接口中枢；tools/llm 核心不导入 engine，adapter 桥接为例外）。

- [ ] **步骤 6：`.claude/skills/check-archs/SKILL.md` 同步**

检查规则更新为真实拓扑（tools/llm 核心不导入 engine；adapter 桥接文件为例外并说明理由）。

- [ ] **步骤 7：`docs/archive/DESIGN.md` 文件头加历史注记**

```markdown
> **历史注记（2026-09-20）：** 本文档已过时，仅存档。当前架构以 engine 为类型中枢、多代理模式由 skill/builtin 内置技能驱动，详见 README.md。
```

- [ ] **步骤 8：删除空壳目录 `app/` `retrieval/`**

```bash
rm -rf app retrieval
```

> `app/` 下曾有 `modes` 子目录（已空）、`retrieval/` 为空。确认无 git 跟踪重要文件后删除。

- [ ] **步骤 9：全量构建 + 测试 + gofmt**

运行：
```bash
go build ./... && go test ./... -count=1
gofmt -l .
```
预期：全部 PASS、gofmt 无输出（或仅第三方目录）。

- [ ] **步骤 10：Commit**

```bash
git add -A
git commit -m "docs: sync README/CLAUDE.md/check-archs to builtin-skill topology, remove empty app/ retrieval/ dirs"
```

---

## 验收标准

1. `grep -rn "ratdHall\|collabHall\|RATDState\|CollabState\|parseRATDCommand\|parseCollabCommand" engine/ cmd/ ui/ --include="*.go" | grep -v _test` 零命中。
2. `skill/builtin_test.go` 通过：`BuiltinSkills()` 返回 ratd/collab/debate 三技能，frontmatter 完整，内容含 handoff_to_agent/task_complete。
3. `/skills` 列表含三个内置技能（装配后运行 `deepact` 或单测验证注册）。
4. 用户目录放置同名 `ratd/SKILL.md` 后，加载内容反映用户版本（覆盖语义——由 `LoadExternalSkillsFromPaths` 后者覆盖前者保证）。
5. README/CLAUDE.md/check-archs 描述的架构与真实依赖一致；无 `policy/` 引用；`app/` `retrieval/` 不存在。
6. `go build ./... && go test ./...` 全绿；`gofmt -l .` 无输出。

## 风险与回退

- **删状态机后残留引用**：`grep` 清单覆盖 engine/cmd/ui 非测试代码；测试文件随源文件删除。
- **UI member 渲染删除牵连 todo 渲染**：`renderOverlayStatus` 同时服务 todoItems 与 memberStatuses；删除成员部分时保留 todo 渲染，先 grep 调用图。
- **内置技能内容质量**：SKILL.md 从现有角色提示迁移，提示词质量等价于原状态机；模型编排自由度更高，可能跑偏——由 LoopTracker/ScopeGuard 兜底（与规格"失去与保留"一致）。
- **用户目录同名覆盖**：内置技能最低优先级，用户可覆盖；覆盖后 `/ratd` 仍命中（`parseSkillCommand` 按注册表 `Get`）。
