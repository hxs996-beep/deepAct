# 防止 Agent 反复读同一文件 — 设计规格

- 日期：2026-06-28
- 分支：feat/agent-teams
- 范围：engine / context / tools(read 死代码清理)

## 1. 背景与根因

分析模式下，Agent 反复用不同 `offset`/`limit` 重读同一文件（如 `ui/model.go`）而不产出结论，最终触发"检测到重复操作循环，Agent 可能卡住了"提示。

项目内存在**两套对 read 判定互相冲突**的循环守卫：

- **LoopGuard**（`engine/guards.go`，per-tool-call，会话级，`NewLoopGuard(6)`）：按 `read:path:scopeHash` 计数，带 offset/limit/symbol 的读因 scope 各异 → 每个 key 仅计数 1 次 → 不触发。
- **per-turn 计数**（`engine/loop.go:624-680`）：`LastOp = "read:" + path`（`engine/turn.go:558-569`，**只含 path，不含 scope**），连续 5 轮首个调用相同即触发，输出"检测到重复操作循环"。

结果："读同文件不同区段"被 per-turn 误判为卡死，而 LoopGuard 因 scope 不同未拦截——两套机制判定矛盾，per-turn 更严格的那套抢先误触发。

此外 `tools/builtin/read.go` 历史上做过 mtime stub 短路（`fileUnchangedStub` 常量 + `mtimeCache` 字段），因"stub 让模型更迷茫、继续读"被回退，现仅写不读，为死代码。

## 2. 设计原则

> **同一指令反复执行 = 有问题；但读文件能让 LLM 自我更正。**

据此：read 循环要给自我更正机会（nudge 引导），非 read 工具（grep/bash/edit 等）的循环仍直接硬拦——它们不像 read 有自我更正价值。

## 3. 组件设计

### 3.1 数据结构（`engine/types.go`）

`TaskState` 新增字段记录本会话已读文件与区段：

```go
type ReadRecord struct {
    Path  string `json:"path"`
    Scope string `json:"scope"`  // "" = 全文件读; "symbol:Run" / "L10-50" = 区段
}

// TaskState 新增：
ReadHistory []ReadRecord `json:"read_history"`
```

- `Scope` 为可读串（非 hash），便于注入提示与 nudge 文案。
- 裸读（仅 path，无 offset/limit/symbol）→ `Scope = ""`。
- 带区段读 → `"symbol:Run"` 或 `"L10-50"`。

### 3.2 提示层已读清单（预防）

每轮 read 执行后，引擎追加一条 `ReadRecord` 到 `state.ReadHistory`。构建系统提示时按 path 聚合注入：

> 已读文件（不要重读，内容已在对话历史中）：
> - `ui/model.go`（symbol:Run, L10-50）
> - `engine/loop.go`（全文）
>
> 需要新信息时：用 `lsp`（hover/goToDefinition/workspaceSymbol）或读取该文件**尚未读过**的区段。

注入点在 `context/` 提示构建链路，按现有系统提示拼装方式追加一段。

### 3.3 统一 scope-aware 守卫（消除不一致）

将两套守卫对 read 的判定统一为可读 scope 串：

- **LastOp**（`engine/turn.go:558-569`）：read 调用 → `"read:" + path + "::" + scope`（scope 为可读串，裸读为空串）。
- **LoopGuard**（`engine/guards.go:55-85`，`extractReadScopeHash`）：read 的 key 同步改为 `"read:" + path + "::" + scope`，废弃 hex hash。

两套口径对齐后，"读同文件不同区段" → 不同 key → 不计数 → 不触发（合法探索）。LoopGuard 保留为会话级硬底线（6 次 block），与 per-turn 计数判定一致，不再互相抢先。

### 3.4 两级脱困（`engine/loop.go`）

将 per-turn 循环计数从"连续 N 轮首个调用相同"改为"**同一 (path, scope) 的累计 read 次数**"——更贴合"重复读同一个方法"的直觉，且每个 (path, scope) 独立计数，读别处不干扰。

| 阶段 | 触发条件 | 行为 |
|---|---|---|
| 1-2 次 | 同一 (path, scope) read | 允许 |
| 第 3 次（超过 2 次） | 同一 (path, scope) 第 3 次 read | **nudge**：向 history 注入一条 user 消息，给 1 次自救机会 |
| 第 4 次 | nudge 后仍读同一 (path, scope) | **硬拦**：block 并请用户澄清 |

**nudge 消息**（中英文按会话 `zh` 切换）：

```
[LOOP NUDGE] 你已 3 次读取 <path> 的 <scope 描述>，内容已在对话历史中。
不要再读取它。请直接基于已有内容产出分析结论；
如需新的具体信息，改用 lsp（hover/goToDefinition/workspaceSymbol）
或读取该文件尚未读过的区段。
```

`<scope 描述>` 人话化：裸读 → "整个文件"；带区段 → "symbol:Run 方法"或"第 10-50 行"。

nudge 注入为 user 消息，正常进入后续上下文压缩流（与 sub_agent.go 现有 `getNudgeMessage` 模式一致）。

**硬拦文案**（`engine/loop.go:670`，替换原"检测到重复操作循环"）：

```
检测到重复读取循环：已反复读取 <path>（<scope>），nudge 后仍未改善。
Agent 可能卡住了。请澄清：是想查看哪段未读内容，还是基于已有内容直接给出结论？
```

明确导向"请用户澄清"，而非泛泛"提供新方向"。

**计数存储与协调**：累计计数复用 `LoopGuard.entries` map（read 条目按 `"read:path::scope"` 为 key），新增两级判定（3 nudge / 4 硬拦）。`LoopGuard` 原有的 6 次 block 阈值对**非 read 工具**仍生效；对 read，因 per-turn 在第 3/4 次提前拦截而实际不触发，仅作兜底。这样两套阈值共用一份计数存储，避免维护两个 map。

**计数清零时机**：仅在用户发新消息时清零（复用 `engine/loop.go:200` 的 `loop.Reset()` 时机）。文件被 edit/write 修改后不清零。其他情况不清零。

**触发范围**：nudge 与硬拦只针对 read 循环。非 read 工具（grep/bash/edit 等）的循环仍走原有硬拦逻辑，不动。

## 4. 非目标（YAGNI）

- **不改非 read 工具的循环判定**：它们无自我更正价值，反复执行本就该硬拦，现有逻辑保留。
- **不复活 read.go 的 mtime stub**：`fileUnchangedStub` 常量与 `mtimeCache` 死代码可附带清理，但不依赖它做抑制。
- **不加单文件读次数硬上限**（如"单文件最多读 3 次"）：与"读不同区段合法"冲突，nudge 已覆盖真正问题。
- **不动 `engine/sub_agent.go` 的独立循环检测**：它有独立 `maxSameOp=5` 逻辑，sub-agent 任务结构化、循环风险不同，本次不扩展。

## 5. 测试

- **扩展 `engine/guards_test.go`**：scope-aware read key 生成（裸读、symbol、offset/limit 三种）；不同 scope 产生不同 key 不互相计数。
- **新增 loop 集成测试**：
  - 同一 (path, scope) 第 3 次 → nudge 注入 history。
  - 第 4 次 → 硬拦，`BlockedBy: "loop_guard"`，文案含"请澄清"。
  - 用户新消息后计数清零。
  - 读同文件不同区段 → 不触发 nudge/硬拦。
- **提示层注入测试**：`ReadHistory` 多条记录按 path 聚合渲染。

## 6. 改动文件清单

- `engine/types.go` — 新增 `ReadRecord`、`TaskState.ReadHistory`。
- `engine/turn.go` — LastOp 改 scope-aware 可读串；read 后写 `ReadHistory`。
- `engine/guards.go` — read key 改可读 scope 串，废弃 hex hash。
- `engine/loop.go` — per-turn 计数改"同一 (path,scope) 累计次数"；3 次 nudge、4 次硬拦；清零时机复用 `loop.Reset()`。
- `context/`（提示构建）— 注入已读清单段落。
- `tools/builtin/read.go` — 附带清理 `fileUnchangedStub`/`mtimeCache` 死代码。
- `engine/guards_test.go` + 新增 loop 测试 — 覆盖上述行为。
