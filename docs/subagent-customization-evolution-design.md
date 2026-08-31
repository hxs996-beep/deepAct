# 子 Agent 定制与自我进化设计

> 目标：让 DeepAct 的子 agent 能力**超过 pi/其他 harness**，而不只是对齐。
> 覆盖三种定制方式：① 固定提示词的定制子 agent；② skill 用文字描述子 agent 性格；③ agent 自我进化（干完事后形成自己的内容，回写 skill / agent md）。
>
> 决策记录（用户已确认）：
> - 范围：先出设计文档，review 通过后逐块实现（P1 → P2 → P3）。
> - 进化策略：**经验库 + 受限回写**（自动存库，回写仅限项目内 `.deepact/` 的 `## Experience` 段落，带日期戳）。
> - 存放位置：**两级级联**（`~/.deepact/agents` 全局 → `<project>/.deepact/agents` 项目级覆盖）。

## 1. 现状与差距

| 维度 | 现状 | 卡点 |
|---|---|---|
| 固定提示词子 agent | 只有 `sub` + `critic`，critic 的 prompt 硬编码在 `engine/default_agents.go` 的 Go 源码里 | 加一个角色要改代码重编译 |
| handoff 枚举 | `handoffToolSpec`（`engine/agent.go:211`）的 agent enum **硬编码 `["sub","critic"]`** | registry 注册多少 agent，工具描述都看不见 |
| skill 描述性格 | frontmatter 已解析 `agent`、`context: fork` 字段（`skill/skill.go`）但**没接线** | Claude Code 的 `context: fork` 能力是空壳 |
| 文字→人设通道 | `/team --add xxx.toml` 用 TOML 文字描述成员性格，经 `runLoop` 的 `extraPrompt` 注入子代理（机制已验证） | 只对辩论流程有效，未推广到全局 handoff |
| 自我进化 | 只有主代理的 `<!-- REMEMBER: -->` memory markers（`memory/store.go`）；`eval_store.go` 只存 eval 分数 | 干完事学不到东西，不形成回写 |

## 2. 总体架构

三个能力共享一条数据流：

```
定义层（文件）                注册层（内存）              执行层（每次 handoff）
─────────────────          ─────────────────          ─────────────────────────
~/.deepact/agents/*/    ┐
AGENT.md                ├─→ AgentRegistry（新增      ─→ 动态枚举/描述注入
<project>/.deepact/     │   目录化 agent）              handoffToolSpec(registry)
  agents/*/AGENT.md     ┘
~/.deepact/skills/*/    ─→ Skill（已有 Registry，     ─→ 激活时 persona 注入
SKILL.md (persona/agent)    接线 agent/context:fork）     所有子代理 stable prompt
                        │
                        └─→ ExperienceStore（新）     ─→ 相似 handoff 检索注入经验
~/.deepact/experience/     ← run 结束自动存库（只增）
<project>/.deepact/
  agents/ experience/     ← 受限回写仅限此目录
```

## 3. P1 — Agent 目录化（方式 1）

### 3.1 文件格式：`AGENT.md`（frontmatter + 正文）

```markdown
---
id: code-archaeologist
description: 追溯历史代码的来龙去脉，回答"为什么这么写"
tools: [read, grep, glob, lsp]
model: flash          # 可选：flash | pro | <具体模型名>
max_iterations: 20    # 可选
structured_result: true  # 可选，默认 true
---

## 角色
你是一位代码考古学家。你的任务是……（固定提示词正文）
```

- frontmatter 各字段与 `AgentSpec` 一一对应；正文 = 固定角色提示词。
- `tools` 留空 = 全部工具；`model` 复用 `runLoop` 已有的 modelOverride 通道（`flash` 已支持，失败自动升级 Pro 的机制免费获得）。

### 3.2 加载与注册

新建 `skill/agentmd.go`（或 `agent/` 包，倾向放 `skill/` 避免新增顶层包）：

```go
// skill/agentmd.go
type AgentFile struct {
    ID               string   `yaml:"id"`
    Description      string   `yaml:"description"`
    Tools            []string `yaml:"tools"`
    Model            string   `yaml:"model"`
    MaxIterations    int      `yaml:"max_iterations"`
    StructuredResult *bool    `yaml:"structured_result"`
    Prompt           string   // 正文（角色提示词，中英一体，双语不强制）
    Source           string   // 文件路径，用于回写
    BaseDir          string
}

// LoadExternalAgents 扫描目录下每个子目录的 AGENT.md（布局与 skills 一致：
// <dir>/<name>/AGENT.md，name 缺省用目录名）。
func LoadExternalAgents(dir string) ([]*AgentFile, error)
// LoadExternalAgentsFromPaths 两级级联：全局 ~/.deepact/agents 在前，
// 项目 .deepact/agents 在后覆盖（与 skill.LoadExternalSkillsFromPaths 同构）。
func LoadExternalAgentsFromPaths(dirs ...string) ([]*AgentFile, error)
```

`cmd/run.go` 启动顺序：在 `NewDefaultRegistry` 之后加载两级目录，逐个
`registry.Register(&specialistAgent{...})`——**复用现有 `specialistAgent`**，
不新增 agent 类型：

```go
// engine/default_agents.go 新增
func RegisterAgentFile(reg *AgentRegistry, runner *SubAgentRunner, af AgentFile) {
    prompt := af.Prompt
    reg.Register(&specialistAgent{
        id:   AgentID(af.ID),
        spec: AgentSpec{
            ID: AgentID(af.ID), Description: af.Description,
            ToolNames: af.Tools, ModelName: af.Model,
            MaxIterations: af.MaxIterations, StructuredResult: defaultTrue(af.StructuredResult),
        },
        promptEn: prompt, promptZh: prompt,
        runner:   runner,
    })
}
```

### 3.3 动态 handoff 枚举（关键改动）

现在 `handoffToolSpec(zh bool)` 的 enum 是死的。改成从 registry 渲染：

```go
// engine/agent.go
func handoffToolSpec(reg *AgentRegistry, zh bool) ModelTool {
    specs := reg.AgentSpecs()
    ids := make([]string, 0, len(specs))
    descParts := make([]string, 0, len(specs))
    for _, s := range specs {
        ids = append(ids, string(s.ID))
        descParts = append(descParts, fmt.Sprintf("%s (%s)", s.ID, s.Description))
    }
    // agent 参数：enum 用 ids；description 用 descParts 连接
    // 其余 goal/context/tools/constraints/expected_output 不变
}
```

改动波及点（已在代码里确认）：

- `engine/turn.go:749` `toolSpecsWithHandoff()` → `handoffToolSpec(e.agents, e.isChinese)`；
- `engine/sub_agent.go:740` `filterTools()` → 同理传 registry（`SubAgentRunner` 已有 `registry` 字段）；
- 默认 registry 里 `sub` 的描述同时补全为 `Execute a well-defined subtask with specified tools`。

> 注意：enum 变化属于 stable 前缀的一部分。agent 注册顺序必须**确定性排序**（按 ID 排序），否则每次启动顺序抖动会破坏前缀缓存。`AgentRegistry.AgentSpecs()` 目前是 map 随机序，需改为排序输出（小改动，带测试）。

### 3.4 工具侧可见性

- `handoff_to_agent` 找不到 agent 时已有报错（`agent not found`），保持；
- `turn.go:1091` 的 UI 摘要已支持任意 agent 名（`→ agent: goal`），无需改。

### 3.5 P1 验收

- `~/.deepact/agents/code-archaeologist/AGENT.md` 建好后，新会话 `/handoff` 工具描述里出现 `code-archaeologist` 及其描述；
- 主代理可以 handoff 给它，它按正文人设、按 tools 白名单执行，结果回流；
- 不建任何 AGENT.md 时行为与现状**逐字节一致**（零回归）；
- registry 排序确定性测试通过。

## 4. P2 — Skill 人设注入（方式 2）

### 4.1 接线已解析但未使用的字段

`skill/skill.go` 已有 `Agent`、`Context`（"fork" 语义）、`Model`、`AllowedTools` 字段。语义对齐 Claude Code：

| frontmatter | 语义 | DeepAct 接线 |
|---|---|---|
| `agent: <id>` | skill 激活后，主代理应把任务 handoff 给该 agent 执行 | 激活时注入指令："本 skill 推荐由 agent `<id>` 执行，优先 handoff_to_agent 给它" |
| `context: fork` | skill 内容在独立上下文执行，不污染主会话 | 激活时自动把 skill 正文注入到 handoff 到 `<id>` 的子代理 context |
| `model: <m>` | skill 期望的模型 | handoff 时写入 `Handoff.ModelName`（`Handoff` 结构需加该字段，runLoop 已支持 modelOverride） |
| `allowed-tools: [...]` | skill 的工具白名单 | 激活期间 handoff 的默认 `Tools` 合并该白名单 |

### 4.2 新增 `persona` 字段（方式 2 的核心）

skill frontmatter 新增 `persona`：一段**自然语言描述子代理性格/气质的文字**：

```markdown
---
name: architecture-review
description: 架构评审
persona: |
  你是一位从业 20 年的架构师，说话直接、不留情面，但对事不对人。
  你偏好简单可靠的方案，反对过度设计。评审时先找最致命的那个问题。
---
```

接线方式——**激活时注入子代理 stable 层**：

- `engine.activateSkill`（`loop.go:1857`）检测 `s.Persona != ""` 时，
  把 persona 段落写入 `SubAgentRunner` 的一个会话级字段（如
  `SetActivePersona(text)`；清空 = 移除）；
- `runLoop` 在 `stableSystemPrompt` 之后、`extraPrompt` 之前追加 persona 消息
  （放在 stable 前缀内 → 同一 skill 激活期内所有子代理共享前缀，缓存友好）；
- `agent: xxx` 与 `persona` 并用：persona 是"气质"，agent 是"谁执行"。
  无 `agent` 字段时 persona 作用于所有子代理。

> 这就是把 roundtable 已验证的 `RunWithPrompt(extraPrompt)` 通道推广到全局：
> 区别是 roundtable 的 extraPrompt 每成员不同（易变），而 persona 是激活期
> stable 的，放 stable 层拿前缀缓存。

### 4.3 `context: fork` 的自动执行

skill 激活且 `context: fork` 且 `agent: xxx` 时，主代理 handoff 到 `xxx` 的
`Handoff.Context` 自动携带 skill 正文（`s.Content`）+ persona。具体做法：

- `engine/turn.go` 处理 `handoff_to_agent` 调用处（约 `turn.go:831`），
  构造 Handoff 时查 `e.state.ActiveSkillName` → skill registry → 若
  `Context == "fork"` 且 `Agent == params.Agent`，把 skill 正文拼入 Context。
- 不强制主代理必须 handoff（保留 LLM 自主权），但激活提示里明确"优先"。

### 4.4 P2 验收

- 带 `persona` 的 skill 激活后，任意子代理的行为体现该气质（用固定
  probe 任务验证输出风格变化）；
- `agent: critic-x` + `context: fork` 的 skill 激活后，handoff 到
  `critic-x` 时子代理能拿到 skill 全文，且主会话 history 不被污染；
- 无 persona 的 skill 行为与现状一致。

## 5. P3 — 自我进化（方式 3）

### 5.1 经验库（自动、只增）

新建 `experience/` 包（或放 `memory/` 下，倾向独立包 `experience/`）：

```go
// experience/store.go
type Experience struct {
    ID           string    `json:"id"`            // sha256(goal指纹 + agent)
    Timestamp    time.Time `json:"timestamp"`
    AgentID      string    `json:"agent_id"`      // 哪个子 agent 产生的
    GoalSnippet  string    `json:"goal_snippet"`  // 截断的目标（展示用）
    GoalHash     string    `json:"goal_hash"`     // 检索键
    FinishReason string    `json:"finish_reason"` // completed/max_tokens/loop_detected/...
    Lesson       string    `json:"lesson"`        // 一条教训："这类任务要先列文件清单再逐文件读"
    Tokens       int       `json:"tokens"`
    DurationMs   int64     `json:"duration_ms"`
    Useful       bool      `json:"useful"`        // 父代理事后判定（可留空）
}

type Store struct { dir string }
func (s *Store) Append(exp Experience) error           // append-only JSONL
func (s *Store) Search(goal string, agentID string, k int) ([]Experience, error) // 关键词/哈希相似
```

**存库触发点**：`runLoop` 的每个 return 路径之前（completed / max_tokens /
loop_detected / stalled_narration / no_result / error 全覆盖）。存库逻辑：

1. 用 `input.Goal` 生成 GoalHash；
2. Lesson 的生成**不额外调 LLM**（省钱）：先取 finish_reason + summary 头 N 字
   作为初始记录；父代理在收到 handoff 结果后，由**主代理侧**在下一步用一个
   已有工具（新增 `remember_lesson` 工具，主代理可选调用）精炼为一条教训——
   也就是"干某件事，完成或没完成，形成自己的内容"由主代理撰写、结构由代码保证。
3. 检索注入：`runLoop` 开始时 `Search(input.Goal, agentID, k=3)`，命中（相似度
   阈值）则追加一条 user 消息 `## Past Experience (this agent)\n- ...`。
   注入在 volatile 之前，不破坏 stable 前缀。

### 5.2 受限回写（只写 `.deepact/` 内、只写 `## Experience` 段）

策略（用户已确认）：

- **自动**：经验库 append 无需任何审批（只增 JSONL，不影响 prompt 行为以外）；
- **回写文件**：仅允许写 `<project>/.deepact/agents/<id>/AGENT.md` 或
  `<project>/.deepact/skills/<name>/SKILL.md` 的 `## Experience` 段落（文件不存在则
  新建到 `.deepact/` 下），每条带日期戳与 GoalHash 前 8 位，追加到该段末尾；
- **禁止**：改动 frontmatter、改动正文其余部分、写 `.deepact/` 以外的任何
  skill/agent 文件、删除历史条目；
- 触发条件（保守）：同一 GoalHash 家族的失败经验 ≥ 2 条，或一条 completed
  经验被后续会话复用过 ≥ 1 次（即"被验证有效"）才回写；
- 实现为内置工具 `learn_lesson`：主代理调用，参数 `{target: "agent:<id>" |
  "skill:<name>", lesson: "..."}`，工具内部执行上述白名单校验与段落级
  merge（读取→定位 `## Experience`→追加→原子写）。全部写路径收敛到这一个
  工具，审计点唯一。

### 5.3 与现有 memory 的关系

- `memory/store.go` 的 MemoryMarkers 是**主代理**的跨会话记忆，不动；
- `eval_store.go` 的 EvalRecord 是**评分数据**，不动；
- 经验库是**子代理维度**的新数据面，三者互补。经验库也存
  `~/.deepact/experience/`（全局）+ `<project>/.deepact/experience/`（项目级），
  检索时项目级优先、全局兜底（与 agents/skills 级联一致）。

### 5.4 P3 验收

- 子代理失败（loop_detected 等）后，经验 JSONL 自动新增一条记录；
- 下一次相似 goal 的 handoff，子代理收到 `## Past Experience` 注入（用两条
  相似 goal 验证命中）；
- `learn_lesson` 写 `## Experience` 段成功后，SKILL.md/AGENT.md 其余内容
  byte 级不变；尝试写 `.deepact/` 之外路径被拒绝；
- 全流程对默认配置零影响：不加经验库路径配置时，行为与 P2 完全一致。

## 6. 兼容与安全

1. **前缀缓存**：registry 排序确定性 + persona 放 stable 层 + 经验注入放
   volatile 层，三级配合保证缓存不劣化；
2. **零回归**：无 AGENT.md / 无 persona / 无经验库时的输出与现状逐字节一致，
   每阶段附 golden test；
3. **回写安全**：白名单目录 + 段落级 merge + 日期戳 + 触发条件保守 + 唯一
   写工具 `learn_lesson`（可被 skill gate 拦截，兼容现有 `GateConfig`）；
4. **防爆炸**：经验库 JSONL 每项目文件大小上限（如 5MB）后自动截断最旧条目；
   检索 k=3、每条 lesson 截断 300 字。

## 7. 分阶段实施计划

| 阶段 | 内容 | 涉及文件 | 独立可验收 |
|---|---|---|---|
| P1 | AGENT.md 加载 + 动态枚举 + 排序 registry | `skill/agentmd.go`(新)、`engine/default_agents.go`、`engine/agent.go`、`engine/agent_registry.go`、`engine/turn.go`、`engine/sub_agent.go`、`cmd/run.go` | ✅ |
| P2 | persona 注入 + agent/context:fork/model 接线 | `skill/skill.go`、`skill/markdown.go`、`engine/loop.go`、`engine/sub_agent.go`、`engine/turn.go`、`context/builder.go`（激活提示） | ✅ |
| P3 | 经验库 + 检索注入 + learn_lesson 受限回写 | `experience/store.go`(新)、`engine/sub_agent.go`、`engine/agent.go`、`tools/builtin/learn_lesson.go`(新)、`cmd/run.go` | ✅ |

每阶段独立 commit（`feat(engine): ...` / `feat(skill): ...` / `feat(engine): ...`），
按 CLAUDE.md 的 commit 规范执行。

## 8. 未决问题（实现前确认）

1. `Handoff` 增加 `ModelName` 字段：P2 的 `model:` 接线需要。小改动，默认零影响；
2. 经验检索的相似度：先用关键词重叠 + goal 长度归一化（零依赖），效果不好再上
   本地 embedding（需要新依赖，暂缓）；
3. `learn_lesson` 是否默认暴露给模型：建议默认开（写路径已被白名单锁死），
   可通过配置 `disable_self_evolution` 关闭。
