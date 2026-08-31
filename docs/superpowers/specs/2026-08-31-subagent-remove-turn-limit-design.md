# 移除 Subagent 默认轮次上限（99）

日期：2026-08-31
状态：已批准（方案 A：默认放开，保留显式上限）

## 背景与问题

调研型 subagent（走 `handoff_to_agent` 委派的 `sub`）在长调研任务中会明确报
"子代理超出轮次上限（部分结果）"，即撞到默认轮次上限 `maxSubAgentIterations = 99`
（`engine/sub_agent.go:14`）。

原因：主代理的 `handoff_to_agent` 工具不暴露 `max_iterations` 参数
（`HandoffToAgentParams` 无该字段，`engine/agent.go:106`），且 generic sub 的
`AgentSpec.MaxIterations` 为 0（`engine/default_agents.go:35`）——两条通道都走
99 这个默认值。对调研型任务，99 轮没有正向价值，反而在调研中途截断。

用户决策：**去掉默认限制**（方案 A）——默认不再有轮次上限；显式指定上限的能力保留。

## 目标

1. 默认放开 subagent 轮次上限：`MaxIterations=0`（未指定）不再回退到 99。
2. 保留显式上限：需要收敛的 agent（critic 15 轮、roundtable 3/15 轮）可自行收紧。
3. 保留全部既有安全兜底，不新增机制。

## 方案决策

### A 默认放开，保留显式上限（用户已选）

- 删除 `maxSubAgentIterations = 99` 常量。
- `MaxIterations <= 0` 语义 = **无轮次上限**。
- 显式 `MaxIterations > 0` 的调用（critic / roundtable / 测试）行为不变。

### 未采纳的方案

- **彻底移除上限机制**（方案 B）：连显式设置能力也删，波及 critic/roundtable
  与更多测试，超出本次需求范围。
- **仅调大默认值**（如 99 → 200）：仍会截断极端调研，且数值是新的魔法常量，
  不如直接放开。

## 改动点

### 1. `engine/sub_agent.go` — 删除默认上限常量与回退逻辑

```go
// 删除（:13-16 的 const 块仅保留 defaultSubAgentContext）
const maxSubAgentIterations = 99

// Run / RunWithPrompt（:119-137）：去掉默认回退
func (r *SubAgentRunner) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
    return r.runLoop(ctx, input, "", input.MaxIterations)
}
```

### 2. `engine/sub_agent.go:246` — 循环条件放开

```go
// before
for iter := 0; iter < maxIterations; iter++ {
// after：0 = 无上限；>0 = 显式上限
for iter := 0; maxIterations <= 0 || iter < maxIterations; iter++ {
```

### 3. `engine/default_agents.go:63-66` — specialistAgent 去掉默认回退

```go
func (a *specialistAgent) Run(ctx context.Context, input Handoff) (*HandoffResult, error) {
    input.StructuredResult = a.spec.StructuredResult
    return a.runner.runLoop(ctx, input, a.promptFor(zhFromLang(input.UserLanguage)),
        a.spec.MaxIterations, a.spec.ModelName)
}
```

> `AgentSpec.MaxIterations` 注释更新：`0 = 无上限`（原为 `0 = use default (99)`，`engine/agent.go:85`）。
> `Handoff.MaxIterations` 注释同步更新（`engine/agent.go:53`）。

### 4. 尾部兜底分支（`engine/sub_agent.go:579-591`）

**保留，语义不变**：仅对显式 `MaxIterations > 0` 的 agent 生效——撞上限仍返回
`HandoffReasonMaxIterations` + `TimedOut=true`。对无上限的 agent，该分支不可达
（循环不退出），代码结构不动。

## 不受影响的部分

- **显式上限全部保留**：critic `MaxIterations: 15`（default_agents.go:19）、
  roundtable `3` / `roundtableMemberMaxIterations=15`（roundtable.go:216/316）。
- **安全兜底保留**（不新增、不删除）：
  - 连续 3 轮纯文本 → `HandoffReasonStalledNarration`（sub_agent.go:419-426）
  - 同一文件 edit/write 重复 5 次 → `HandoffReasonLoopDetected`（sub_agent.go:520-537）
  - 单次 LLM 调用 120s 超时（sub_agent.go:305）
  - 上下文超限自动压缩（sub_agent.go:258-272）
  - 连续 3 次输出截断 → `HandoffReasonMaxTokens`（sub_agent.go:367-379）
  - 结构化 run 无提交 3 击 → `HandoffReasonNoResult`（sub_agent.go:388-395）
- **所有现有测试**均显式传 `MaxIterations`（3-8 轮），无任何测试依赖 99 默认值，
  行为不变。

## 已知取舍

无上限意味着：若模型持续产出**有意义的**工具调用且永不收敛，循环不会因轮次终止。
但上述守卫已覆盖病态路径（纯文本、同文件循环、调用超时、输出截断），实际不会
无限运行。这是方案 A 的直接含义，用户已确认接受。

## 测试计划

### 新增 `engine/sub_agent_remove_turn_limit_test.go`

- **核心回归**：`MaxIterations=0` 时用桩工具（如 stubToolExecutor 计数）跑
  **超过 99 轮**的工具调用，验证：
  - 不会因轮次上限终止（不返回 `HandoffReasonMaxIterations`）；
  - 最终经 `submit_result`（结构化）正常完成并返回摘要。
  - 桩模型按轮次序列返回：先 N 次 `read` 工具调用（N > 99），再 `submit_result`。
- **显式上限保留**：`MaxIterations=3` 且模型持续调工具不收敛 → 仍返回
  `HandoffReasonMaxIterations`（防回归，确认没破坏既有语义）。

### 验证

- `go test ./engine/` 全绿（现有 + 新增）。
- `go build ./...` 通过。

## 数据流

```
Run(ctx, input)
  └─ input.MaxIterations      // 0 = 无上限（默认）；>0 = 显式上限
       └─ runLoop(ctx, input, extra, MaxIterations)
            └─ for iter := 0; maxIterations <= 0 || iter < maxIterations; iter++
                 └─ 正常终止（submit_result / VERDICT / NoNudge）
                 └─ 守卫终止（stalled_narration / loop_detected / max_tokens / no_result）
```
