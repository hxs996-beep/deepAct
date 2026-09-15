# RATD-Harness：反向测试驱动 Agent 协作 — 设计规格

> **日期：** 2026-09-15
> **状态：** 已批准（沙箱=工作区直跑 / 引擎写盘+执行 / 交付=直接交付）

## 目标

用"反向测试驱动协作 Harness（RATD-Harness）"替换 `/debate` 的 4 轮自然语言辩论状态机，消除两个问题：

1. **辩论耗时失控**：`/debate` 一次触发 4 轮（提案/质询/反驳/终陈）× N 成员并行子代理，每轮子代理 `MaxIterations=0` 无轮数上限、探索型工具循环不受任何护栏约束（同文件循环检测只跟踪 edit/write，辩论成员只用只读工具），用户不手动叫停可跑 3 小时。
2. **同源 LLM 口头扯皮**：自然语言 Code Review 沦为格式/命名口水仗，对错由"审查 Agent 说了算"而非物理证据。

RATD 的核心变化：**审查形态从"自然语言点评"改为"可执行测试用例"，裁决依据从"我觉得"改为"沙箱 ExitCode/断言结果"**。由 Proposer 写代码 → RedTeam 生成对抗测试 → 沙箱物理执行 → Arbitrator 仲裁失败测试合法性，形成状态驱动的收敛闭环。

## 背景

### 现状（将被替换）

- `engine/roundtable.go`：`RoundtableHall` 编排 4 轮辩论，成员经 `RunWithPrompt` 注入角色 prompt，跑 `genericSubAgent`（structured `submit_result`，`MaxIterations=0`）。
- 失败成因已调查（见本次会话）：成员子代理无迭代上限；`firstOpKey` 同文件循环检测只覆盖 edit/write；纯文本 3 击 stalled_narration / structured 3 击都被"持续调工具"重置；单次调用 120s 总时长已移除（只剩 SSE idle 60s 防真挂死）。辩论一轮即可数小时，4 轮相乘不可接受。
- `/collab` 流水线（`engine/collab.go`）是现有最接近的骨架：阶段状态机 + 幂等重入 + `RunWithPrompt` 角色注入 + 确认门。RATD 复用该编排模式。

### 复用底座（已存在，零新执行引擎）

- `genericSubAgent` + `submit_result`：子代理结构化完成，返回 `HandoffResult.Summary`。三角色都复用此机制，只换 role prompt + 输出契约。
- `Handoff`（`engine/agent.go:42`）：支持 `Goal/Context/Tools/Constraints/ExpectedOutput/Depth/NoNudge/MaxIterations`。
- `bash` 工具已返回 `ExitCode *int`（`tools/registry.go:34`、`tools/builtin/bash.go:139`）——但沙箱执行**不走工具循环**，由引擎 `exec.CommandContext` 直接执行，超时与 ExitCode 完全可控。

## 架构

### 新文件

- `engine/ratd.go`：`RATDHall` 状态机编排 + 三角色 agent 调用 + 写盘 + 沙箱执行 + 收敛判定 + 交付渲染。
- `engine/ratd_test.go`：状态机各路径 / 契约解析 / 上下文隔离 / 沙箱裁决 / 命令解析测试。

### 命令解析

新增 `parseRATDCommand(userMsg) *RATDCommand`，识别 `/ratd <目标>`（对齐 `parseCollabCommand` 模式，`engine/collab.go:16`）。在 `loop.go` 的 team-command 处理块中，用 RATD 分支**替换** debate 分支。

### 状态机（单次 Run 内同步推进，幂等重入）

模式对齐 `CollabHall.handleCollabArena`（`engine/collab.go:58`）：已完成的阶段跳过，部分失败后重入安全。状态存 `TaskState.RATD`。

```
INIT(/ratd)
  → PROPOSE_CODE        Proposer 子代理生成代码 → 引擎写盘
  → RED_TEAM_TEST       RedTeam 子代理生成对抗测试（隔离上下文）
       └─ no_issues_found → TERMINATE
  → RUN_SANDBOX         引擎执行 代码 + 回归套件 + 新测试
       ├─ 全过 → 新测试计入回归套件 → CHECK_CONVERGENCE
       │        ├─ 达到 max_rounds 或 无 P0/P1 → TERMINATE
       │        └─ 否则 → RED_TEAM_TEST（继续找更隐蔽的 bug）
       └─ 有失败 → ARBITRATE
                    ├─ ACCEPT → PROPOSE_CODE（重构，注入 FailingTests + 沙箱输出）
                    └─ REJECT → 丢弃该测试 → RED_TEAM_TEST
  → TERMINATE          交付渲染（改动清单 + 最终测试结果）
```

### 角色契约（子代理输出 JSON，引擎解析；引擎做手、子代理做脑）

三个角色都通过 `RunWithPrompt` 注入 role prompt + 结构化输出格式说明，经 `submit_result` 返回 JSON 文本，引擎 `json.Unmarshal` 解析。

**Proposer** → `CodePayload`
```json
{
  "language": "go|python|rust|java|typescript|cpp",
  "source_files": [{"path": "src/queue.go", "content": "..."}],
  "design_notes": "采用 CAS 无锁队列..."
}
```
引擎按 `source_files` 写盘。`design_notes` **只留存在引擎状态中，不注入 RedTeam 上下文**（隔离）。

**RedTeam** → `TestPayload`
```json
{
  "test_category": "CORRECTNESS|BOUNDARY|CONCURRENCY_STRESS|MEMORY_LEAK",
  "severity": "P0_CRITICAL|P1_HIGH|P2_SUGGESTION",
  "target_file": "src/queue_test.go",
  "test_code": "func TestQueue_HighConcurrency(t *testing.T) {...}",
  "assertion_rationale": "并发 Pop 空队列时未处理通道关闭，会死锁。"
}
```
无问题则返回 `{"no_issues_found": true}`。RedTeam **工具集只读**（read/grep/glob/lsp），无 write/edit/bash——沙箱执行完全由引擎控制，安全边界清晰。

**Arbitrator** → `ArbitrationResult`
```json
{
  "decision": "ACCEPT_TEST|REJECT_TEST",
  "rejected_reason": "测试使用了 Spec 之外的无效输入范式。",
  "actionable_feedback": "请修复高并发下内存泄漏，位于数组扩容逻辑。"
}
```

### 上下文隔离（核心要求）

- RedTeam 输入 = 需求 + 当前代码文件内容（source_files）。**不注入** Proposer 的 design_notes / 思考过程。
- Arbitrator 输入 = 需求 + 代码 + 失败测试 + 沙箱输出（ExitCode/Stdout）。
- Proposer 重构输入 = 需求 + 原代码 + 失败测试 + 沙箱输出 + Arbitrator 的 actionable_feedback。

## 沙箱执行（引擎层）

- 引擎用 `exec.CommandContext(ctx, "bash", "-c", cmd)` 执行测试命令，ctx 带超时（v1 固定 120s，常量 `ratdSandboxTimeout`）。
- 结果记录 `ExitCode` + `Stdout`（截断保存，供 Arbitrator 判读）。
- 语言适配器表驱动（新增语言 = 加一行）：

| language | 基础测试命令 |
|---|---|
| go | `go test -timeout 60s ./...` |
| python | `pytest -x -q` |
| rust | `cargo test` |
| java | `mvn test` |
| typescript | `npm test` |
| cpp | `ctest --output-on-failure` |

- 未知/不可用命令 → 该轮视为"无法物理验证"，记入状态并跳过 RUN_SANDBOX 进入收敛判定（避免硬失败卡死）。

## 收敛控制

- `maxRounds = 3`（常量，`engine/ratd.go`，后续可配置）。
- TERMINATE 条件：达到 `maxRounds`；或某轮全过且回归套件中无 P0/P1；或 RedTeam 返回 `no_issues_found`。
- 回归套件累积：每次全过的测试计入，后续轮次持续运行，防止 Proposer 退化。
- flaky 检测 v1 不做（YAGNI，由 max_rounds + Arbitrator 兜底）。

## 交付形态（已定：直接交付）

收敛后（TERMINATE）：
1. 引擎展示：改动文件清单（新增/修改的 source_files 路径）+ 最终沙箱测试结果（ExitCode/通过数摘要）。
2. 代码与测试已直接落盘在工作区，用户自行查看/提交。
3. 状态 `RATD` 清空，恢复正常流程。无确认门。

## 删除范围（/debate 全链路）

- **删除整文件**：`engine/roundtable.go`、`engine/default_members.go`。
- **`engine/types.go`**：删除 `RoundtableState`、`RoundtableMember`、`DebateRound`、`DebateOutput`、`RoundtablePhase`、`DebateRoundPhase` 及关联常量/方法。
- **`engine/loop.go`**：删除 debate 分支（`parseTeamCommand` 调用块、`state.Roundtable` switch、`teamVerdictPending` 处理），替换为 RATD 分支。
- **`context/builder.go`**：删除 `DebateRounds` 字段（`builder.go:286`）与 `flattenRoundtable` 及调用。
- **测试**：删除/替换全部 roundtable 相关测试（`engine/roundtable_test.go`、`context/builder_test.go` 的 `TestFlattenRoundtable`、以及引用 `NewLoopTracker` 的 roundtable 测试中受影响处——保留的 LoopTracker 测试不动）。
- **`engine/loop.go` 内 `state.Roundtable` 其它引用**：实现时全量 `grep "Roundtable|Debate|DefaultDebateMembers|parseTeamCommand|teamVerdictPending"` 确认删除面，不留孤儿。

> 注：`engine/loop.go:107` 注释提及 "edit-plan guard - the user already approved the plan through the debate" 属注释，随分支删除同步清理。

## 测试计划（engine/ratd_test.go）

1. `/ratd` 命令解析（有效 / 空目标 / 非 ratd 前缀）。
2. 状态机推进各路径：全过收敛 / 失败→ARBITRATE→ACCEPT→重构 / REJECT 丢弃 / no_issues_found 直接收敛 / max_rounds 收敛。
3. 写盘正确性：source_files 按 path/content 落盘。
4. JSON 契约解析：CodePayload/TestPayload/ArbitrationResult 合法与非法输入。
5. 上下文隔离：RedTeam 的 task goal 不含 design_notes。
6. 沙箱裁决：ExitCode=0 → pass；非 0 → fail；超时 → fail 处理。
7. 删除后编译：`go build ./...`、`go test ./engine/... ./context/...` 全绿。

## 不做（YAGNI）

- 不容器化 / cgroups / Docker（v1 工作区直跑，已定）。
- 不做 flaky 测试自动隔离（靠 max_rounds + Arbitrator）。
- 不做覆盖率目标门控（RFC 提到 target_coverage，v1 砍掉，保留 max_rounds + 无新 P0/P1）。
- 不做并发压力测试的专用 runner（test_code 由 RedTeam 在测试内自行使用 goroutine/thread 即可）。
- `/collab` 流水线不动。
