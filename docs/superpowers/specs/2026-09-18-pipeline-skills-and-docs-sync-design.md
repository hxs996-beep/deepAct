# 管线技能化（ratd/collab/debate）+ 文档同步 — 设计规格

> **日期：** 2026-09-18
> **状态：** 已批准
> **取代：** `2026-09-15-ratd-harness-design.md`、`2026-09-16-collab-parallel-research-design.md`（引擎状态机方案）

## 目标

对照 AGENTS.md 两条第一性原理，把 `/ratd`、`/collab` 从引擎硬编码状态机改造为**官方内置技能**，并补齐 README 承诺但从未实现的 `/debate`；同时让架构文档与代码真实状态同步：

1. **泛化性优先**：`RATDHall`/`CollabHall` 是两个写死在引擎核心的具体场景（专用 Phase 枚举、专用 State 类型、`loop.go` 硬编码 if 分派）。正确的泛化形态是：**多角色协作的方法论是数据（SKILL.md），不是引擎逻辑**——新协作模式 = 新技能文件，零引擎改动。
2. **能力优先**：引擎已具备编排所需的全部通用能力（并行 handoff、按交接限定工具集、bash 执行测试、ScopeGuard 拦截危险命令、LoopTracker 防循环、task_complete 显式完成）。状态机是在替模型做"下一步该哪个角色"的判断——应删除，由技能提示词引导模型自主编排。

## 背景：现状违反点

### 违反点 1：引擎核心的场景枚举（engine/ratd.go + engine/collab.go + engine/types.go）

- `TaskState.Collab *CollabState` / `TaskState.RATD *RATDState`：两个专用状态机挂在通用任务状态上（types.go:258-259）。
- `loop.go:306-332`：`parseRATDCommand`/`parseCollabCommand` 硬编码 if 链识别两个特定命令。
- `loop.go:618-645`：`handleRATDArena`/`handleCollabArena` 硬编码分派，主 agent 循环被整体旁路。
- `RATDHall`/`CollabHall` 持有 `*Engine` 反向引用，直接摸 `engine.state`/`engine.agents`/`engine.config`（ratd.go:473-477、collab.go:44-46）。
- 合计约 1160 行引擎代码只服务于两个场景。

### 违反点 2：README 双语版宣传 `/debate`，代码从未实现

`README.md:107-113` / `README.zh.md:107-113` 描述了完整的四角色辩论模式（含 `--members`/`--add` 参数），engine/ 中无任何实现（仅测试文件提及）。

### 违反点 3：架构文档系统性漂移

- README 架构图列出不存在的 `policy/` 包；根目录 `app/`、`retrieval/` 为空壳目录。
- README 宣称 "Dual-model routing — flash handles tool calls..."，实际主循环 `selectModel()` 固定返回主模型（turn.go:780-785，为保 DeepSeek per-model 前缀缓存稳定），router 仅剩测试调用。
- README "Parallel Subagents (searcher / planner / critic / tester)"，实际 `default_agents.go` 只剩单个通用 `sub` agent（critic 已删）。
- CLAUDE.md / check-archs 的分层规则（"tools/ MUST NOT import engine/" 等）与真实依赖图不符（`tools/adapter.go`、`llm/adapter.go`、`context`、`router`、`session` 等均导入 engine——engine 是类型中枢）。

## 方案

### 总体架构

**引擎纯减法，技能通道做加法。** 三个多代理模式变为 `go:embed` 内置技能，随二进制分发、首次启动即用、无需安装和外部引入；用户目录同名技能按既有"后者覆盖前者"规则覆盖内置技能（同一条优先级规则覆盖 N 个来源，零特判）。

被否决的备选：
- **首次启动写文件到 `~/.deepact/skills/`**——违反"无安装无引入"要求，且升级时产生合并冲突。
- **Go 常量伪技能**——脱离 SKILL.md 标准格式，无法被用户覆盖，制造格式分裂。
- **数据驱动管线 DSL / 编译期 Go 接口注册**——保留引擎编排层的方案，与"判断权交给模型"的第一性原理不符（详见 2026-09-18 头脑风暴记录）。

### 组件设计

#### 1. 内置技能资产（新增 `skill/builtin/`）

```
skill/builtin/
├── ratd/SKILL.md
├── collab/SKILL.md
└── debate/SKILL.md
```

标准 Claude Code 布局 + frontmatter（name / description / when_to_use / argument-hint），由现有 `ParseMarkdownSkill` 解析——内置技能是普通技能，不是新机制。

三个技能的内容同构，各包含：
- **角色提示词原文**：当前 `ratdRolePrompt`/`collabResearchRolePrompt` 的中英双语内容迁入 Markdown，指示模型在 handoff goal 中原样使用（ratd：Proposer/RedTeam/Arbitrator；collab：Decomposer/Worker/Synthesizer——collab 的拆解与汇总由主模型自身承担，仅 Worker 走 handoff；debate：四个辩论角色 + 评分规则）。
- **输出契约**：CodePayload / TestPayload / ArbitrationResult 的 JSON schema（作为 handoff goal 的一部分传给子代理）。
- **编排规则**：每角色工具子集（read/grep/glob/lsp）、轮数上限（ratd 3 轮）、失败语义（仲裁失败默认 ACCEPT_TEST）、测试命令映射（go→`go test -timeout 60s ./...` 等）。
- **完成判据**：交付摘要格式 + task_complete 调用要求。

#### 2. 内置通道（新增 `skill/builtin.go`）

```go
//go:embed builtin/*/SKILL.md
var builtinSkillFS embed.FS

// BuiltinSkills 解析嵌入的官方保留技能。
func BuiltinSkills() ([]*Skill, error)
```

注册顺序：内置技能最先（最低优先级）→ 4 个用户目录（既有规则不变）。装配点在 `cmd/run.go` 技能注册表构建处。

#### 3. 引擎删除清单

| 文件 | 改动 |
|---|---|
| `engine/ratd.go`（685 行） | 整文件删除 |
| `engine/collab.go`（375 行） | 整文件删除 |
| `engine/types.go` | 删 `CollabPhase/CollabTask/CollabState/RATDPhase/RATDSourceFile/RATDTest/RATDSandboxResult/RATDState`（约 100 行）；`TaskState` 删 `Collab/RATD` 字段；`ProgressEvent.Type` 注释删 `ratd_role` 枚举 |
| `engine/loop.go` | 删 `parseRATDCommand`/`parseCollabCommand` 分派块（306-332）、RATD/Collab arena 分派块（618-645）、`ratdHall/collabHall` 字段（99-103）与 `NewEngine` 装配（184-185） |
| `engine/sub_agent.go` | `RunWithPrompt` 专用通道若无其他调用方则一并清理（实现时核实） |
| `engine/*_test.go` | ratd/collab 状态机相关测试删除（`ratd_parse_test.go` 等） |

保留不动：`topLevelJSONObjects` 若被 debate/其他路径复用则迁移到使用方；`resolveSafePath`/`safeWriteFile` 系列是通用安全写工具，迁移到 tools 包或保留位置由实现计划核实调用方后决定。

#### 4. UI 清理

- `ui/model.go:517-537`：`member_start/member_done` 分支删除（collab 状态机删除后成为死代码；注释已标注 legacy debate UI）。
- `ui/model.go:93-94`：`/ratd`、`/collab` 帮助行保留（命令语法不变，改为技能语义描述），新增 `/debate` 帮助行。
- `ui/model.go:3171-3172`：欢迎语改为提示三个内置协作技能。

### 数据流（`/ratd 实现一个限流器` 端到端）

1. 用户输入 → `parseSkillCommand` default 分支命中（`ratd` 为合法技能名，`isValidSkillName`）→ 命中内置技能 → 方法论全文注入提示词，`extractTaskTextAfterSkillCmd` 提取 `<goal>` 作为任务文本。
2. 模型按剧本编排：`handoff_to_agent`（agent=sub，goal=Proposer 角色 prompt + 任务 + CodePayload 契约，tools=read/grep/glob/lsp）→ 子代理返回 CodePayload JSON。
3. 模型解析 payload，write 源文件 → `handoff_to_agent`（RedTeam prompt）→ 返回 TestPayload 或 no_issues → write 测试文件。
4. `bash` 执行语言测试命令 → 失败则 handoff（Arbitrator prompt + 失败测试 + 沙箱输出）→ ACCEPT（回到 Proposer 重构）/ REJECT（删除该测试文件，回到 RedTeam）。
5. 循环由模型按技能纪律控制（≤3 轮），LoopTracker 兜底防角色循环；沙箱通过 + 红队认输 → `task_complete` 附交付摘要（改动文件清单 + 最终测试结果）。

`/collab`：模型自身拆解 2-6 个研究方向 → **一轮内并行发起多个 handoff**（`Executor.Execute` 每调用一 goroutine，天然并行，已验证）→ 汇总各 worker 报告输出研究报告。

`/debate`：模型并行 handoff 四个辩论角色 → 评分 → 胜者方案改写为实施蓝图。

### 失去与保留（诚实清单）

| 失去（用户已接受） | 保留 |
|---|---|
| Phase 断点恢复（部分失败后 Run 重入续跑；技能形态下重跑即重入） | LoopTracker 循环守卫（同键重复 → nudge/block） |
| 引擎强制 3 轮上限（变为提示词纪律 + LoopTracker 兜底） | ScopeGuard 危险命令拦截（go test 之外的 rm -rf 等仍被拦） |
| payload 正则容错解析（模型读子代理摘要，比正则更容错） | handoff 并行 / 每交接工具子集 / task_complete 显式完成 |
| RATD 沙箱 120s 进程级超时 | bash 工具自身超时 |

### 文档同步清单

| 文件 | 改动 |
|---|---|
| `README.md` / `README.zh.md` | ① 架构图重画为真实拓扑（engine 类型中枢 + adapter 桥接；删 `policy/`；保留各包职责描述）；② `/debate` `/ratd` `/collab` 段落改写为内置技能机制说明（命令语法不变、无 `--members`/`--add` 参数——从未实现）；③ "Parallel Subagents" 段改为真实情况（单个通用 sub agent + handoff_to_agent 并行委托）；④ "Dual-model routing" 表述修正为主循环固定主模型保前缀缓存、flash 用于子代理与压缩路径（实现时验证 router 现状后落笔）；⑤ 技能优先级表头部加内置技能行（最低优先级，可被用户目录同名覆盖） |
| `CLAUDE.md` | 分层规则改为真实依赖描述：engine 是共享类型/接口中枢（hub-and-spoke），llm/tools 核心文件零项目依赖、由 adapter 文件桥接到 engine 类型 |
| `.claude/skills/check-archs/SKILL.md` | 检查规则同步真实拓扑（tools/llm 核心不导入 engine；adapter 桥接文件为例外并说明理由） |
| `docs/archive/DESIGN.md` | 文件头加历史注记（已过时，仅存档） |
| 根目录 `app/` `retrieval/` | 删除（实现时先确认确实为空且无引用） |

### 测试策略

- 删除：ratd/collab 状态机全部引擎测试。
- 新增：`skill/builtin_test.go`——embed 资产可解析、frontmatter 完整（name/description/when_to_use 非空）、名字合法（`isValidSkillName`）、内容含关键结构标记（角色提示词/输出契约段落存在）。
- 冒烟：`/ratd <goal>` 命令 → 技能加载路径（stub model 验证技能内容进入提示词 + goal 文本注入）。
- 回归：`go build ./...` + `go test ./...` 全绿；`gofmt` 无 diff。

## 验收标准

1. `grep -rn "ratdHall\|collabHall\|RATDState\|CollabState\|parseRATDCommand\|parseCollabCommand" engine/ cmd/ ui/ --include="*.go" | grep -v _test` 零命中。
2. 全新环境（无任何用户技能目录）启动，`/skills` 列表含 ratd/collab/debate 三个内置技能。
3. 用户目录放置同名 `ratd/SKILL.md` 后，`/skills` 与加载内容反映用户版本（覆盖语义）。
4. README/CLAUDE.md/check-archs 描述的架构与 `go list` 实际依赖图一致；无 `policy/` 引用；`app/` `retrieval/` 不存在。
5. `go build ./... && go test ./...` 全绿。
