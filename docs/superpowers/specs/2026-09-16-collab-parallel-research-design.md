# /collab 并行研究模式 — 设计规格

> **日期：** 2026-09-16
> **状态：** 已批准（拆解→并行→汇总 全自动 / 并发上限 4 / worker 只读 / MaxIterations=99）

## 目标

把 `/collab` 从"串行流水线（侦察→设计→开发→把关）"改造为**并行研究模式**：一件事拆解成多个研究方向后，多个 agent 并行各负责一部分（只读调研），最后汇总成研究报告。目的是**加快研究进度**——多个 worker 并发调研，比串行逐一调研更快。

## 背景

### 现状（将被替换）

- `engine/collab.go`：`CollabHall` 编排 4 阶段串行流水线（recon → design → dev → review），每个阶段一次 `RunWithPrompt` 调用注入角色提示，前序阶段产出全量拼接进后续阶段（`renderCollabPrior`）。阶段产出展示给用户后，由用户输入 `支持`/`但要<条件>`/`重新协作` 确认（`Advance`/`handleConfirmation`），确认后主 agent 落地执行。
- 串行流水线的问题：阶段之间有顺序依赖，总耗时 = 各阶段之和；阶段产出全量文本拼接导致 token 累积（后序阶段上下文越来越长）；多角色串行并不比单个深度 agent 更快。

### 设计决策（用户已确认）

| # | 决策 | 选择 |
|---|---|---|
| 1 | 产出形态 | **研究/分析为主**：并行 worker 各自只读调研，汇总成报告/方案 |
| 2 | 拆解职责 | **LLM 拆解 agent** 分析目标，产出任务列表（每个任务带研究方向）——符合"能力优先" |
| 3 | 命令落点 | **改造现有 /collab**，语义保持一致（同一命令做协作） |
| 4 | 确认门 | **全自动**：拆解→并行→汇总一气呵成，最后展示汇总报告 |
| 5 | 并发上限 | **固定上限 4**，超出的排队串行 |
| 6 | worker 上下文 | **独立上下文**：每个 worker 只拿到自己的任务描述，完全独立调研 |
| 7 | 实现结构 | **方案 B：仿 RATD 全新状态机**（CollabState 保留名字，Phase/字段重构） |
| 8 | 任务数 | 2~6 个 |
| 9 | worker 工具 | 只读 `read/grep/glob/lsp`，无 edit/write |
| 10 | worker 轮数 | **MaxIterations=99**（更彻底，不因预算提前收敛） |

## 架构

### 状态类型（改造 `engine/types.go`）

保留 `CollabState` 名字与 `TaskState.Collab` 字段名（最小化 loop.go/context/ui 引用改动），重构 `CollabPhase` 与字段：

```go
// CollabPhase describes which stage of the /collab parallel research we are in.
type CollabPhase int

const (
	CollabIdle      CollabPhase = iota
	CollabDecompose             // 拆解：LLM 拆解 agent 产出任务列表
	CollabParallel              // 并行：并发执行各任务（只读调研）
	CollabSynthesize            // 汇总：LLM 合并各 worker 报告
	CollabDone                  // 完成：展示最终报告并清理
)

// CollabTask is one research direction produced by the decomposer.
type CollabTask struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Direction string `json:"direction"` // 研究方向（自包含，worker 独立开工）
	Status    string `json:"status"`    // pending/running/done/failed
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CollabState tracks the current /collab parallel research within TaskState.
type CollabState struct {
	Goal   string       `json:"goal"`
	Phase  CollabPhase  `json:"phase"`
	Tasks  []CollabTask `json:"tasks"`
	Report string       `json:"report,omitempty"` // 汇总研究报告
}
```

删除旧类型 `CollabStageName` / `CollabStage`（串行阶段已不存在）。

### 命令解析

`parseCollabCommand` 不变（识别 `/collab <目标>`），`CollabCommand` 不变。loop.go 命令启动块不变，`Phase` 初始化为 `CollabDecompose`。

### 状态机（单次 Run 内同步推进，仿 RATD `handleRATDArena`）

模式对齐 `RATDHall.handleRATDArena`（`engine/ratd.go:571`）：`for { switch phase }` 循环，单 Run 内跑完，幂等重入（部分失败后重入从当前 Phase 继续）。

```
INIT(/collab)
  → DECOMPOSE      Decomposer 子代理分析目标 → 输出任务列表 JSON → 引擎解析 → Tasks[]
       └─ 解析失败/无有效任务 → 报错返回，清理 Collab
  → PARALLEL       信号量(4) + goroutine 并发执行各任务（只读调研）
       └─ 单 worker 失败不中断其他，结果标记 failed
  → SYNTHESIZE     Synthesizer 子代理合并所有报告 → Report
  → DONE           渲染最终报告 → 清理 Collab → 返回
```

### 角色契约（子代理输出，引擎解析）

**Decomposer（拆解员）**：资深架构师，把研究目标拆成互不重叠的研究方向。输出结构化 JSON：

```json
{
  "tasks": [
    {
      "id": "t1",
      "title": "调研现有缓存实现",
      "direction": "定位并阅读代码库中与缓存相关的模块（cache.go、store.go），梳理接口、TTL 逻辑、调用方"
    },
    {
      "id": "t2",
      "title": "调研第三方缓存库选型",
      "direction": "评估 deepact 现有依赖中可复用的缓存方案，对比优缺点"
    }
  ]
}
```

约束：任务数 2~6 个；每个 `direction` 自包含（worker 无共享上下文，必须能从描述独立开工）；**不写代码方案**，只定研究方向。

**Worker（研究员）**：独立调研一个研究方向。角色提示注入"研究员"；`Goal` = 任务 `direction`；工具白名单 `read/grep/glob/lsp`；`MaxIterations=99`。产出研究小结（结论 + 关键证据 file:line）。

**Synthesizer（汇总员）**：合并所有成功 worker 报告成结构化研究总报告。角色提示注入"汇总员"；单次 LLM 调用。输出格式：

```
## 研究结论总览（目标：<goal>）
## 各方向发现（按任务列出：标题 + 结论要点 + 关键证据 file:line）
## 综合分析与建议（跨方向的交叉发现、权衡、推荐方向）
```

失败任务明确标注"未完成"，不掩盖。

### JSON 解析

复用 `topLevelJSONObjects`（`engine/ratd.go:150`，同包直接可用）+ 新增 `parseCollabTasks`：

- 容错与 RATD 一致：容忍 prose 包裹、跳过非法对象。
- 校验：`tasks` 非空、每项有 `id`/`title`/`direction`。
- 任务数处理：**超出 6 截断到前 6**（保留拆解 agent 的输出顺序，不整体失败）；**少于 2 报错**（1 个任务没必要拆解，直接主 agent 做即可）。
- `parseCollabTasks` 返回 `([]CollabTask, error)`。

### 并行调度

```go
const collabMaxConcurrency = 4
const collabWorkerMaxIterations = 99
const collabDecomposerMaxIterations = 5
const collabSynthesizerMaxIterations = 5

sem := make(chan struct{}, collabMaxConcurrency)
var wg sync.WaitGroup
for i := range state.Tasks {
	wg.Add(1)
	sem <- struct{}{}
	go func(t *CollabTask) {
		defer wg.Done()
		defer func() { <-sem }()
		t.Status = "running"
		emit member_start 进度事件
		result, err := h.runWorker(ctx, t, zh)
		t.Status = "done" / "failed"
		t.Result / t.Error = result / err
		emit member_done 进度事件
	}(&state.Tasks[i])
}
wg.Wait()
```

- 每个 worker 的 `runWorker` 用 `RunWithPrompt` 注入"研究员"角色 + 任务 `direction`；`accumulateUsage` 累加。
- 并发安全已验证：`SubAgentRunner.runLoop` 每次 `model.Fork()` 独立 client（sub_agent.go:187）；`accumulateUsage` 有 `usageMu`（loop.go:1236）；`OnProgress` 回调可并发调用（UI member cards 复用）。
- 注意：`CollabState.Tasks` 被多个 goroutine 并发写 `Status/Result/Error`。goroutine 各自写**自己的** `*CollabTask`（按索引取地址），无共享写；`wg.Wait()` 后主 goroutine 再读全部——无数据竞争，但**测试建议用 `-race` 验证**。

### 进度事件

- 阶段事件用 `collab_phase`（Type），Detail 为阶段名（`decompose`/`parallel`/`synthesize`/`done`）——避免与既有 `ratd_role`/`debate_phase` 事件计数冲突（参照 ratd.go:499 的做法）。
- worker 卡片复用 UI 已有 `member_start`/`member_done` 事件（ui/model.go:518/539），Name 为 `worker-<taskID>`。

```
collab_phase:decompose → 拆解中...
member_start:worker-t1 → 调研：现有缓存实现
member_start:worker-t2 → 调研：第三方缓存库选型
member_done:worker-t1
member_done:worker-t2
collab_phase:synthesize → 汇总中...
```

### loop.go 接线

- 调度块（`engine/loop.go:633`）改新 Phase 集合：`CollabDecompose, CollabParallel, CollabSynthesize` → `handleCollabArena`；`CollabDone` → 清理。
- 删除 `collabVerdictPending` 门控（loop.go:668）与 `Advance`/`handleConfirmation`（全自动无确认门）。
- Engine 结构体 `collabVerdictPending` 字段删除；`handleCollabArena` 末尾不再置 `CollabAwaitingConfirmation`。
- Run 末尾 `CollabDone` 清理保留（loop.go:861）。

### context/builder.go

`flattenCollab`/`collabPhaseName`（builder.go:289-314）同步更新为新 Phase 名：`decompose`/`parallel`/`synthesize`/`done`。

### UI

- `ui/model.go:95` `/collab` 描述更新为"并行研究：拆解→并行调研→汇总报告"。
- `ui/model.go:3197` 欢迎文案同步。

## 错误处理

- **拆解失败**（LLM 出错/解析失败/任务数越界）→ `handleCollabArena` 返回错误，清理 `Collab`，用户可见错误提示。
- **单 worker 失败**（LLM 出错/超时）→ `t.Status=failed`，`t.Error` 记录；不中断其他 worker；汇总仍合并成功部分。
- **汇总失败** → `Report=""`，最终展示直接列出各任务结果（fallback），功能不中断。

## 测试

仿 `ratd_test.go`/`collab_test.go` 结构（复用 `mockPromptRunner`，roundtable_test.go:135 同包共享）：

| 测试 | 验证 |
|---|---|
| `TestParseCollabCommand_Valid/NotCollab` | 命令解析（保留不变） |
| `TestParseCollabTasks` | JSON 解析：合法/容错 prose/任务数越界/无有效任务 |
| `TestHandleCollabArena_DecomposeThenParallel` | 拆解产出任务 → 并行全部完成（mock 固定文本） |
| `TestHandleCollabArena_Concurrency` | N 个任务并发执行、结果全部落回 Tasks |
| `TestHandleCollabArena_WorkerFailureTolerated` | 单 worker 失败不中断其他 |
| `TestHandleCollabArena_SynthesizeFallback` | 汇总失败时展示任务结果 fallback |
| `TestHandleCollabArena_ToolsAllowlist` | 每个 worker 只用只读工具 `read/grep/glob/lsp` |
| `TestCollabPhaseName` | context 阶段名映射 |

## 改动文件汇总

| 文件 | 改动 |
|---|---|
| `engine/types.go` | CollabPhase/CollabState 重构 + 新增 CollabTask；删 CollabStageName/CollabStage |
| `engine/collab.go` | 重写为状态机（拆解/并行/汇总）+ 角色提示 + parseCollabTasks；删 Advance/handleConfirmation/buildCollabSummary/buildCollabPrompt/renderCollabPrior/collabPriorSection/collabRolePrompt/buildCollabStageGoal/collabStageLabel |
| `engine/loop.go` | 调度块改新 Phase、删 collabVerdictPending 门控与字段 |
| `context/builder.go` | collabPhaseName 同步新阶段名 |
| `ui/model.go` | /collab 描述 + 欢迎文案更新 |
| `engine/collab_test.go` | 重写为新流程测试 |
| `README.md` / `README.zh.md` | /collab 语义更新为并行研究 |

## 风险与回退

- **并行成本**：N 个 worker 并发 = N 倍 token。用户明确选择"更彻底"（MaxIterations=99），接受成本。
- **API 限流**：并发 4 可能触发 DeepSeek 限流。现有 client 有 limiter/retry（llm/limiter.go），失败 worker 标记 failed 不阻塞整体。
- **拆解跑偏**：拆解方向不对则整体方向偏。用户选择全自动（求快），接受此风险；失败任务在汇总报告中可见。
- **数据竞争**：goroutine 并发写各自 `*CollabTask`，测试用 `-race` 验证。
