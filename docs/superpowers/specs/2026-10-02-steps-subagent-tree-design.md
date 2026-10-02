# Steps 树：把子代理挂到所属步骤下 — 设计规格

> **日期：** 2026-10-02
> **状态：** 设计已批准（方案 A：单面板步骤树）
> **范围：** 引擎事件链路 + UI 渲染；不改子代理生命周期语义（异步/挂起/关闭规则不变）

## 背景与问题

输入框上方目前堆叠两个**彼此独立**的纵向面板：

- `▍ [→] Sub-Agents` — [ui/model.go:2721](ui/model.go#L2721) `renderSubAgentPanel`
- `▍ [::] Steps` — [ui/model.go:2791](ui/model.go#L2791) `renderTodoList`

两者由 [ui/model.go:1909-1919](ui/model.go#L1909) 顺序追加，**数据上没有任何关联**。用户看到 5 个"方向N 调研"步骤和 5 个 researcher 子代理，却无法判断谁在执行哪一步——这正是"上下结构无法准确表达 subagent 和 steps 的关系"。

根因分两层：

**1）展示层**：步骤与子代理是两个平铺列表，没有归属表达。

**2）数据层**：引擎根本没有提供可用的关联标识。

- `agent_start` / `agent_done` 事件只带角色名与一段文本（[engine/turn.go:659](engine/turn.go#L659)、[engine/loop.go:321](engine/loop.go#L321)），没有稳定 id。
- UI 只能用"角色名相同且仍在 running 的第一条"来配对（[ui/model.go:537-543](ui/model.go#L537)）——多个同名 researcher 并发完成时**必然张冠李戴**。
- 异步派发时每个 handoff 会发**两次** `agent_start`（[engine/turn.go:659](engine/turn.go#L659) 与 [engine/handoff.go:331](engine/handoff.go#L331)），随后派发回执又被当成 `agent_done`（[engine/turn.go:679](engine/turn.go#L679)）。**5 个方向产生 10 行**，且其中 5 行显示的是派发回执而非调研结果。
- `SubAgentStatus.Status == "error"` 分支是死代码：没有任何事件会设置它，失败的子代理渲染为 `✓`。

## 目标

1. 步骤成为唯一主干，子代理缩进挂在它所执行的步骤下；无归属的子代理单独成组。
2. 子代理的 start/done 按稳定 `RunID` 配对，同名并发不再错位。
3. 每条子代理记录带稳定句柄（`bg-N` / tool call id），可与 `agent_poll`、`close_agent` 等通道对齐。

## 非目标

- 不做左右分栏。
- 不做子代理行的展开/折叠。
- 不做 suspended / awaiting_user 的状态可视化：挂起中的子代理沿用现状显示为 running（[engine/handoff.go:352-360](engine/handoff.go#L352) 明确 withhold `agent_done`）。
- 不做"goal 文本 ↔ 步骤文本"匹配：步骤措辞一改就失效，属于脆弱代理。
- 不改动 spinner 动画帧（沿用现有静态首帧）。
- 不改子代理生命周期语义、并发上限、TTL、resume 链路。

## 方案选择记录

| 方案 | 形态 | 结论 |
|---|---|---|
| **A（选定）** | 单面板步骤树：步骤是主干，子代理缩进挂载，游离者单独成组 | 泛化性最好：一个步骤可派生 0（规划/汇总）、1（方向N）或将来 N 个执行单元；一个执行单元也可不挂任何步骤。"主干 + 缩进 + 无归属组"覆盖全部组合，不需按场景打补丁；终端只多 6 列缩进 |
| B | 单面板 + 行内徽章，仅在 ask_user/error 时升格为独立行 | 最紧凑，但子代理 goal/耗时被压成徽章，多条并行时看不到各自在做什么 |
| C | 保留双面板，仅用 `bg-N` 角标互指 | 改动最小，但痛点原样保留——仍要在两个列表间跳转视线 |

## 一、数据链路（引擎侧）

### 核心不变量

> **每个 depth-0 委派产生恰好一对 `agent_start` / `agent_done`，两者携带同一个稳定 `RunID`。UI 只按 `RunID` 配对，不再按角色名配对。**

### 事件表

| 场景 | `agent_start` | `agent_done` |
|---|---|---|
| 同步委派（`async` 省略/false） | `turn.go:659`，`RunID = call.ID` | `turn.go:679`，同一 `RunID` |
| 异步派发成功 | `handoff.go:331`，`RunID = bg-N` | 此处**不发**；完成时由 `loop.go:321` 发，`RunID = bg-N` |
| 异步派发失败（名额满 / agent 不存在） | 不发 | `turn.go:679`，`RunID = call.ID`，`Status = "error"` |
| `close_agent` 终止 | — | `agent_manage.go:631`，`RunID` = 被关闭任务的 id，`Status = "cancelled"` |
| 再挂起 | — | `suspend.go:203`，`RunID = task.id` |

`RunID` 的两种取值为语义标识符，不是位置或文本代理：

- 同步委派用该 handoff tool call 的 `call.ID`（引擎分配、唯一、稳定）；
- 异步委派用 `bg-N`（[engine/handoff.go:305](engine/handoff.go#L305) 分配，与 `agent_poll(job_id)` 是同一个句柄）。

### 字段改动

```go
// engine/types.go
type TodoItem struct {
    ID      string `json:"id,omitempty"`    // 新增：模型给的稳定步骤键
    Content string `json:"content"`
    Status  string `json:"status"`
}

type ProgressEvent struct {
    // ... 现有字段
    RunID  string // 新增：稳定句柄（同步 call.ID / 异步 bg-N）
    Step   string // 新增：本子代理执行的步骤 id，空 = 无归属
    Status string // 新增：agent_done 的结局，ok | error | cancelled
}

// engine/agent.go
type HandoffToAgentParams struct {
    // ... 现有字段
    Step string `json:"step,omitempty"` // 新增
}
```

- `bgTask`（[engine/loop.go:185](engine/loop.go#L185)）增加 `step string`，在 `dispatchAsync` 登记任务时写入，供所有终止路径复用。
- `Status` 映射沿用 `runHandoff` 既有规则（[engine/handoff.go:115-118](engine/handoff.go#L115)：`BlockedBy == "cancelled"` → cancelled）；后台完成路径按 `result.Blocked` / `FinishReason == HandoffReasonError` 判定为 `error`。

### 关键改动

1. **`turn.go` 执行前循环（652-661）**：仅当 `!params.Async` 时发 `agent_start`（`RunID = call.ID`、`Step = params.Step`）。异步的 start 由 `dispatchAsync` 负责，消除重复发射。
2. **`turn.go` 执行后循环（670-681）**：`r.FinishReason == HandoffReasonAsyncRunning` 时**跳过**——异步派发不是结束，真正的 done 来自 `finishBackgroundJob`。其余情况按 `RunID = call.ID` 发出，并带上 `r.Status`。
3. **`dispatchAsync`（handoff.go:325-332）**：位置已在名额校验与 `jobID` 分配之后，只需补 `RunID: jobID`、`Step: params.Step`。
4. **`finishBackgroundJob`（loop.go:320-322）**：补 `RunID: t.id`、`Step: t.step`、`Status`。
5. **`closeAgentJob`（agent_manage.go:629-633）**：`terminal` 目前只收集 agent 名，改为同时收集 `job id`，事件补 `RunID` / `Step` / `Status: "cancelled"`。
6. **`finishResumedJob`（suspend.go:202-204）**：补 `RunID: task.id`、`Step: task.step`；语义不变。

## 二、步骤关联通道（给模型的能力，非启发式）

按 AGENTS.md「能力优先」：提供通道让模型显式声明，而不是让引擎猜。

1. `todo_write` 的每个条目可带一个短稳定 `id`（如 `"dir-1"`）。
2. `handoff_to_agent` 可带 `step`，指向某个步骤 id。
3. 引擎只做一项校验：**`id` 非空则必须唯一**，重复时返回错误消息给模型（fail loud）。
4. `step` 指向不存在的 id **不报错**：UI 把它归入游离组。失败软着陆，避免模型改写 todo 后整轮中断。
5. 提示词同步（模型需要知道这层关系才能用）：
   - `todoWriteToolSpec` 描述（[engine/agent.go:403](engine/agent.go#L403)）：说明 `id` 用于把子代理挂回步骤。
   - `handoffToolSpec` 描述（[engine/agent.go:388](engine/agent.go#L388)）：说明 `step` 的作用与省略后果。
   - `skill/builtin/collab/SKILL.md`：把"给每个方向起 id、派发时带上 step"写进编排步骤。

两条通道都可省略。全部省略时行为退化为"单个 Sub-Agents 平铺列表"，不比现状差。

## 三、渲染规则（UI 侧）

新增合成渲染器，取代现在两次独立调用（[ui/model.go:1909-1919](ui/model.go#L1909)），仍在同一个 `ExecBlockStyle` 块内、仍在输入框上方：

```go
func renderStepsPanel(items []engine.TodoItem, agents []SubAgentStatus, width int) []string
```

```
▍ [::] Steps
  [✓] 规划阶段：读规则文档/源码，建立全局框架
  [~] 方向1 调研：杭麻规则全貌与结算数学
      ✓ bg-1 researcher  研究小结：杭麻"能胡却弃胡做大牌…
  [~] 方向2 调研：现有纯计算策略/搜索/规则数学能力盘点
      ⠋ bg-3 researcher  调查"当前能胡，但主动弃胡去做大牌"…
  [ ] 汇总交叉分析，产出研究报告

▍ [→] Sub-Agents
  ✓ bg-5 researcher  研究小结：仓库如何验证策略收益…
```

规则：

1. 挂载键：`SubAgentStatus.Step == TodoItem.ID`，且两者都非空。一个步骤可挂多个子代理（按派发顺序）。
2. `Step` 为空、或匹配不到任何当前步骤 → 游离组，保持原顺序。
3. 游离组标题沿用现有 `▍ [→] Sub-Agents`，仅在非空时渲染；游离行沿用现有 2 空格前缀。
4. 挂载行用 6 空格前缀（缩进到步骤正文的下一级），行内格式不变：状态符 + `RunID` + 角色 + goal（running）/ summary（done）。
5. `items` 为空时退化为只渲染游离段（即现状的 Sub-Agents 面板）。
6. 每次渲染按当前 todo 快照重新分组，不做状态迁移——todo 被改写导致 id 消失时，相关子代理自动落到游离组。
7. 沿用现有 60 字符截断、配色与 `splitLipglossBlock` 宽度兜底。

数据结构：

```go
// ui/model.go
type SubAgentStatus struct {
    RunID   string // 稳定句柄：同步 call.ID，异步 bg-N
    Agent   string
    Goal    string
    Step    string // 对应 TodoItem.ID，空 = 无归属
    Status  string // running | done | error | cancelled
    Summary string
}
```

原 `ID` 字段删除——它当前只写不读（[ui/model.go:515](ui/model.go#L515)），职责由 `RunID` 取代。

事件处理：

- `agent_start`：以 `RunID` 追加一条 running 行；`RunID` 为空时才回退到角色名（对旧事件保持健壮）。
- `agent_done`：**按 `RunID` 查找**并置为终态；找不到匹配（例如派发失败的异步）则**直接生成一条已结束行**，用 `Detail` 作为 summary。不再使用角色名 FIFO 配对。

## 边界情况

| 情形 | 行为 |
|---|---|
| 多个同名子代理并发 | 靠 `RunID` 区分，done 落在正确行 |
| `agent_done` 先于 `agent_start` / 未知 `RunID` | 直接生成一条已结束行 |
| 一个步骤挂多个子代理 | 全部按派发顺序缩进在该步骤下 |
| `step` 指向不存在的 id | 归入游离组 |
| todo 快照改写导致 id 消失 | 相关子代理下一帧归入游离组 |
| Run 结束清空（[ui/model.go:466-467](ui/model.go#L466)） | 两个列表同时清空，无需额外处理 |
| 首次挂起（`dispatchAsync`） | 不发 `agent_done`（现状如此），行保持 running——不新增 suspended 状态（非目标） |
| 再次挂起（`finishResumedJob`） | 现状就发 `agent_done`；本次只补 `RunID`，使这一 done 落在正确的行 |
| 派发失败的异步（无名额/agent 不存在） | 无 start，done 带 `Status="error"`，渲染为 `✗` 行 |

## 改动点清单

| 文件 | 改动 |
|---|---|
| `engine/types.go` | `TodoItem.ID`；`ProgressEvent.RunID/Step/Status` |
| `engine/agent.go` | `HandoffToAgentParams.Step`；`handoffToolSpec` 与 `todoWriteToolSpec` 的 schema + 描述 |
| `engine/loop.go` | `bgTask.step`；`finishBackgroundJob` 补 `RunID/Step/Status` |
| `engine/turn.go` | 执行前只对同步发 start；执行后跳过异步派发的 done；`processTodoWriteCalls` 校验 id 唯一性 |
| `engine/handoff.go` | `dispatchAsync` 补发事件的 `RunID/Step`；`bgTask` 记录 step |
| `engine/agent_manage.go` | 关闭任务时按 id 发终态事件 |
| `engine/suspend.go` | 再挂起事件补 `RunID/Step` |
| `ui/model.go` | `SubAgentStatus` 重构；事件按 `RunID` 配对；新增 `renderStepsPanel`；视图组装收敛为一次调用 |
| `skill/builtin/collab/SKILL.md` | 编排步骤里说明 id / step 用法 |

## 测试

**引擎**

- 异步派发只产生一次 `agent_start`，且 `RunID == bg-N`（扩 [engine/handoff_test.go](engine/handoff_test.go)）。
- 成功的异步派发不再立即产生 `agent_done`（回归"10 行对 5 个方向"）。
- 5 个同名并发任务完成时，done 事件各带自己的 `RunID`。
- 派发失败路径的 `agent_done` 带 `Status="error"`、`RunID=call.ID`。
- `todo_write` id 唯一性校验：重复 id 返回错误消息（扩 [engine/turn_todo_write_test.go](engine/turn_todo_write_test.go)）。

**UI**

- `renderStepsPanel`：有挂载 / 无挂载 / `step` 失配 / `items` 为空，四种情形的分组与缩进（扩 [ui/todo_render_test.go](ui/todo_render_test.go)）。
- `agent_done` 按 `RunID` 落在正确行；未知 `RunID` 生成已结束行（扩 [ui/model_test.go:494](ui/model_test.go#L494)）。
- `Status == "error"` 渲染为 `✗`。
- 宽度收窄时不越界、不产生幻影空行（沿用 [ui/tab_overflow_test.go](ui/tab_overflow_test.go) 的门禁）。

## 验收

- `go build ./...` 通过。
- `go test ./engine/... ./ui/...` 通过。
