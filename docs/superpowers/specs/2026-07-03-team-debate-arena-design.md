# Team 辩论场模式 — 设计文档

> 日期: 2026-07-03
> 状态: 设计中
> 替换: 现有 `/team` 模式（并行分析 → 合成）

## 1. 动机

现有 `/team` 是「专家组各自出意见 → 组长合并」的流水线模式。四个领域角色（架构师、安全、质量、维护）并行独立分析，最后合成一个方案。用户全程旁观，没有参与感。

用户原始设想是**多 agent 对抗辩论**——agent 们互相挑战、互相说服、最终达成共识，用户作为裁判观看并裁决。

同时，现有 skills 体系（brainstorming、code-review 等）已覆盖了单 agent 的多角度思考和代码审查，team 需要有不可替代的独特价值。

## 2. 核心定位

**用户是裁判，agent 是辩论选手。用户看他们吵，最后自己拍板。**

与 brainstorming skill 的区分：
- brainstorming：一个 agent 探索方案 → 推荐 → 用户确认
- team debate：多个 agent 从不同性格立场辩论 → 用户观看全程 → 用户裁决

## 3. 辩论流程

```
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
│ 提案轮    │───▶│ 质询轮    │───▶│ 反驳轮    │───▶│ 终陈轮    │───▶│ 用户裁决  │
│ 各角色独立 │    │ 交叉攻击   │    │ 回应修正   │    │ 最终立场   │    │ 选择方案  │
│ 提出方案   │    │ 对方方案   │    │ 对方质疑   │    │ 总结发言   │    │ 或举证    │
└──────────┘    └──────────┘    └──────────┘    └──────────┘    └──────────┘
```

### 3.1 提案轮

- 各角色**并行**执行，只看需求描述，看不到其他人
- 每个 agent 可使用 read/grep/glob/lsp 工具探索代码
- 输出：独立的技术方案（结构化的实现思路）

### 3.2 质询轮

- 所有提案公开，每个角色看到**全部提案**
- 每个角色从自己的立场攻击**其他**角色的方案
- 输出：对其他方案的具体质疑（指出问题、给出理由）

### 3.3 反驳轮

- 每个角色只看到**针对自己的质询**（不暴露其他人被质询的内容，避免被带偏）
- 角色回应质疑，必要时修正自己的方案
- 输出：回应 + 可能的方案修正

### 3.4 终陈轮

- 每个角色看到**完整辩论记录**
- 总结最终立场，给所有方案打分（含自己的）
- 输出：最终方案 + 评分矩阵

### 3.5 用户裁决

- 系统展示辩论总结 + 评分矩阵
- 用户可选操作：

| 用户输入 | 行为 |
|---------|------|
| `支持方案1` | 选定方案1，注入为 [TEAM PLAN]，进入实现 |
| `方案2，但要加限流` | 选定方案2并附加条件 |
| `都不行，应该用 session` | 用户自己的论证成为 [TEAM PLAN] |
| `再辩一轮` | 加赛一轮，agent 基于用户反馈继续 |

## 4. 角色体系

### 4.1 核心设计：性格驱动而非领域驱动

现有角色（架构师/安全/质量/维护）是领域分工，换到前端项目安全工程师可能无话可说。辩论需要的不是领域分工，而是**思维方式的碰撞**。

新角色体系：**所有 agent 都是程序员，区别在于性格和思考方式。**

### 4.2 默认四角色

| 角色 | 性格 | 本能反应 | 典型发言 |
|------|------|---------|---------|
| 🔮 创新派 (radical) | 激进、求变 | 看到旧代码就想重构，追求优雅和新范式 | "这个模块设计过时了，应该用 X 模式重写" |
| 🛡️ 防守派 (defender) | 谨慎、求稳 | 关注改动风险、向后兼容、边界条件 | "你这样改会破坏现有调用方，风险太大" |
| 🔧 务实派 (pragmatic) | 结果导向、求快 | 最小改动达成目标，够用就好 | "加个参数就行了，别为了优雅引入新框架" |
| 👤 用户派 (advocate) | 共情、体验优先 | 站在使用者角度，关注直觉和易用性 | "这个 API 对调用方太复杂了，应该简化" |

**自然对抗关系：**
- 创新派 ↔ 防守派（求变 vs 求稳）
- 创新派 ↔ 务实派（理想 vs 现实）
- 防守派 ↔ 用户派（安全 vs 体验）

### 4.3 插件化扩展

和 skills 一样，角色支持内置默认 + 用户自定义：

```toml
# ~/.deepact/members/性能狂.toml
id = "perf-freak"
name = "性能狂"
avatar = "⚡"
stance = "极致性能优先，宁可牺牲可读性也要榨干每毫秒"

[prompt]
system = """
## 性格
你是一个性能偏执狂……
"""
```

**配置方式：**
```toml
# .deepact/config.toml
[team]
members = ["radical", "defender", "pragmatic", "advocate"]
```

**命令行：**
```
/team --members radical,defender 重构认证模块
/team --add ~/.deepact/members/性能狂.toml 优化查询
```

## 5. 数据结构

### 5.1 状态机

```
Idle → ProposalRound → ChallengeRound → RebuttalRound → FinalRound → AwaitingVerdict → Done
```

### 5.2 RoundtableState（重构）

```go
type RoundtableState struct {
    Goal         string             `json:"goal"`
    Phase        RoundtablePhase    `json:"phase"`
    Members      []RoundtableMember `json:"members"`
    DebateRounds []DebateRound      `json:"debate_rounds"` // 替代原 Proposals + Reviews
}
```

### 5.3 新增类型

```go
type DebateRound struct {
    Phase   DebateRoundPhase `json:"phase"`   // proposal / challenge / rebuttal / final
    Outputs []DebateOutput   `json:"outputs"` // 每人一条
}

type DebateOutput struct {
    MemberID string   `json:"member_id"`
    Content  string   `json:"content"`   // 该轮完整输出
    Targets  []string `json:"targets"`   // 针对哪些其他成员（质询/反驳轮使用）
}

type DebateRoundPhase string

const (
    DebateProposal  DebateRoundPhase = "proposal"
    DebateChallenge DebateRoundPhase = "challenge"
    DebateRebuttal  DebateRoundPhase = "rebuttal"
    DebateFinal     DebateRoundPhase = "final"
)
```

### 5.4 RoundtablePhase 扩展

```go
const (
    RoundtableIdle           RoundtablePhase = iota
    RoundtableProposal                        // 提案轮
    RoundtableChallenge                       // 质询轮
    RoundtableRebuttal                        // 反驳轮
    RoundtableFinal                           // 终陈轮
    RoundtableAwaitingVerdict                 // 等待用户裁决
    RoundtableDone                            // 完成
)
```

移除旧阶段：`RoundtableExplore`、`RoundtableReview`、`RoundtableTeamExplore`、`RoundtableTeamSynthesize`。

## 6. 可见性规则

每轮 agent 可见上下文不同，用 `Targets` 字段路由：

| 阶段 | Agent 可见上下文 |
|------|-----------------|
| 提案轮 | 只有需求描述 |
| 质询轮 | 需求 + **所有人的提案**（DebateRounds[0].Outputs） |
| 反驳轮 | 需求 + **Targets 指向自己的质询**（DebateRounds[1] 中 Targets 包含自己 ID 的 Output） |
| 终陈轮 | 需求 + **完整辩论记录**（全部 DebateRounds） |

## 7. 用户界面

### 7.1 辩论过程 UI

每轮结束后 UI 展示该轮所有输出，格式化渲染。用户可以跟进看辩论进展，也可以在任意阶段提前介入：

- `/verdict` — 提前裁决，跳过剩余轮次
- `/continue` — 继续下一轮

### 7.2 裁决界面

```
┌─────────────────────────────────────────────────┐
│  🤝 辩论完成 — 请裁决                              │
│                                                 │
│  需求: 重构用户模块的认证逻辑                        │
│                                                 │
│  ┌─ 方案1: 🔮 创新派 ──────────────────────────┐  │
│  │  JWT + Redis 黑名单方案                       │  │
│  │  评分: 🔮85 | 🛡️70 | 🔧80 | 👤75              │  │
│  │  [查看完整辩论记录]                             │  │
│  └────────────────────────────────────────────┘  │
│                                                 │
│  ┌─ 方案2: 🛡️ 防守派 ──────────────────────────┐  │
│  │  OAuth2 + 短期 Token                         │  │
│  │  评分: 🔮75 | 🛡️90 | 🔧85 | 👤80              │  │
│  │  [查看完整辩论记录]                             │  │
│  └────────────────────────────────────────────┘  │
│                                                 │
│  > _                                             │
└─────────────────────────────────────────────────┘
```

## 8. 实现范围

### 8.1 新增/修改文件

| 文件 | 变更 |
|------|------|
| `engine/roundtable.go` | 重写：新阶段流转、辩论轮次逻辑、可见性路由 |
| `engine/types.go` | 新增 DebateRound / DebateOutput / DebateRoundPhase；重构 RoundtablePhase |
| `engine/default_members.go` | 重写：替换为 4 性格角色，保留插件加载接口 |
| `config/config.go` | 新增 team 配置段 |
| `ui/model.go` | 新增辩论轮次渲染 |
| `cmd/run.go` | `/team` 解析支持 --members / --add 参数 |
| `engine/roundtable_test.go` | 更新测试覆盖新辩论流程 |

### 8.2 移除

- 旧的 `handleTeamFlow`（team explore + synthesize 流水线）
- 旧的 `handleReview`（多角色评审）
- 旧的 `refuteFindings` 证伪阶段（辩论过程本身包含了交叉验证）
- `ReviewContext` / `ReviewContextLevel`（不再需要）
- 旧的 `RoundtablePhase` 枚举值

### 8.3 不变

- `SubAgentRunner` — 辩论每轮仍通过现有 sub-agent 机制执行
- `AgentRegistry` — 注册机制不变
- `RoundtableMember` 结构 — 保持兼容，Prompt 内容替换
- 裁决后的 `pinnedMessage` 注入机制 — 和现有一样

## 9. 风险与约束

- **Token 消耗**: 4 角色 × 4 轮 = 16 次 LLM 调用。通过 sub-agent 独立缓存分区可复用前缀缓存
- **辩论发散**: 通过结构化轮次 + 每轮明确的 prompt 指令防止 agent 跑偏
- **用户中断**: 任意阶段支持 `/verdict` 提前裁决，避免等待

## 10. 开放问题

- [ ] 终陈轮的评分是否需要证伪过滤（类似旧 refuteFindings）？还是信任辩论过程本身的交叉验证？
- [ ] 是否需要「辩论时长限制」防止某轮超时拖慢整体体验？
