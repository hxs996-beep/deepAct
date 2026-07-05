# Team 辩论场模式 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将现有 `/team` 从「并行分析→合成」流水线替换为多 agent 对抗辩论场模式（提案→质询→反驳→终陈→用户裁决）

**Architecture:** 重写 `engine/roundtable.go` 为 4 轮结构化辩论流程，每轮 agent 并行执行但可见性逐轮递增（提案轮只看到需求，质询轮看到所有提案，反驳轮只看到对自己的质询，终陈轮看到全记录）。`SubAgentRunner` 复用不变，`RoundtableState` 扩展为多轮数据存储。

**Tech Stack:** Go 1.24+, 现有 engine/ui/config 包

## Global Constraints

- 完全替换现有 `/team`，不保留旧模式
- 角色体系改为性格驱动（创新派/防守派/务实派/用户派），可插件化扩展
- 4 轮辩论（提案/质询/反驳/终陈）+ 用户裁决
- SubAgentRunner、AgentRegistry、pinnedMessage 机制不变
- 可见性规则：每轮 agent 看到的内容由 `Targets` 字段路由

---

### Task 1: 数据模型 — 新增类型和阶段常量

**Files:**
- Modify: `engine/types.go`

**Interfaces:**
- Produces: `DebateRound`, `DebateOutput`, `DebateRoundPhase` (新增), `RoundtablePhase` (重构常量)

- [ ] **Step 1: 在 types.go 中新增 DebateRound 相关类型**

在 `engine/types.go` 末尾添加以下类型：

```go
// DebateRoundPhase labels the phase of a single debate round.
type DebateRoundPhase string

const (
	DebateProposal  DebateRoundPhase = "proposal"
	DebateChallenge DebateRoundPhase = "challenge"
	DebateRebuttal  DebateRoundPhase = "rebuttal"
	DebateFinal     DebateRoundPhase = "final"
)

// DebateRound captures one round of the debate arena.
type DebateRound struct {
	Phase   DebateRoundPhase `json:"phase"`
	Outputs []DebateOutput   `json:"outputs"`
}

// DebateOutput is one member's contribution in a debate round.
type DebateOutput struct {
	MemberID string   `json:"member_id"`
	Content  string   `json:"content"`
	Targets  []string `json:"targets"` // member IDs this output targets (challenge/rebuttal)
}
```

- [ ] **Step 2: 重构 RoundtablePhase 常量**

将现有的 `RoundtablePhase` 枚举替换为：

```go
const (
	RoundtableIdle            RoundtablePhase = iota
	RoundtableProposal                         // 提案轮
	RoundtableChallenge                        // 质询轮
	RoundtableRebuttal                         // 反驳轮
	RoundtableFinal                            // 终陈轮
	RoundtableAwaitingVerdict                  // 等待用户裁决
	RoundtableDone                             // 完成
)
```

删除旧的枚举值：`RoundtableExplore`, `RoundtableReview`, `RoundtableTeamExplore`, `RoundtableTeamSynthesize`。

- [ ] **Step 3: 更新 RoundtablePhase.String() 方法**

```go
func (p RoundtablePhase) String() string {
	switch p {
	case RoundtableProposal:
		return "proposal"
	case RoundtableChallenge:
		return "challenge"
	case RoundtableRebuttal:
		return "rebuttal"
	case RoundtableFinal:
		return "final"
	case RoundtableAwaitingVerdict:
		return "awaiting_verdict"
	case RoundtableDone:
		return "done"
	default:
		return "idle"
	}
}
```

- [ ] **Step 4: 重构 RoundtableState**

```go
type RoundtableState struct {
	Goal         string             `json:"goal"`
	Phase        RoundtablePhase    `json:"phase"`
	Members      []RoundtableMember `json:"members"`
	DebateRounds []DebateRound      `json:"debate_rounds"` // 替代 Proposals + Reviews
}
```

移除 `Proposals []string` 和 `Reviews []MemberReview` 字段。

- [ ] **Step 5: 删除不再需要的类型**

删除以下类型（被辩论模式替代，不再需要）：
- `TeamThought`
- `MemberReview`
- `ReviewVerdict`（及 `VerdictApprove`/`VerdictConditional`/`VerdictReject` 常量）
- `Finding`
- `RefuteOutcome`（及 `RefuteConfirmed`/`RefuteRefuted` 常量）
- `RefuteResult`
- `refuteTarget`
- `ReviewContext`
- `ReviewContextLevel`（及 `ReviewLevelFull`/`ReviewLevelPartial`/`ReviewLevelMinimal` 常量）

- [ ] **Step 6: 保留不变的类型**

以下类型保留：
- `RoundtableMember` — 结构不变，内容由 default_members.go 替换
- `TeamCommand` 和 `parseTeamCommand` — 解析逻辑稍后修改

- [ ] **Step 7: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```
Expected: 编译错误（引用旧类型的代码尚未更新），但新增类型本身无语法错误。

- [ ] **Step 8: Commit**

```bash
git add engine/types.go
git commit -m "feat: add DebateRound types and refactor RoundtablePhase for debate arena"
```

---

### Task 2: 默认辩论角色 — 性格驱动的四角色

**Files:**
- Modify: `engine/default_members.go`

**Interfaces:**
- Produces: `DefaultDebateMembers` (替换 `DefaultRoundtableMembers`)
- Consumes: `RoundtableMember` (不变)

- [ ] **Step 1: 重写 default_members.go**

完整替换文件内容：

```go
package engine

// DefaultDebateMembers defines the built-in debate roles.
// Each member represents a distinct personality/thinking style, not a domain role.
// Skills can override or extend this list via the plugin system.
var DefaultDebateMembers = []RoundtableMember{
	{
		ID:       "radical",
		Name:     "创新派",
		NameEn:   "Innovator",
		Avatar:   "🔮",
		Stance:   "激进、求变，看到旧代码就想重构，追求优雅和新范式",
		StanceEn: "Radical, embraces change — sees legacy code and wants to refactor, pursues elegance and new paradigms",
		Prompt: `## 性格
你是一个激进的技术创新者。你的核心本能：
- 看到旧代码就觉得应该重构——技术债越早还越好
- 追求最新的技术范式和设计模式
- 愿意为了更好的架构承担短期风险
- 相信优雅的设计最终会降低长期成本

## 辩论风格
- 大胆提出与众不同的技术路线
- 用代码示例和架构图说服他人
- 面对保守派质疑时，用长期收益论证
- 但也尊重证据——如果对方证明你的方案有致命缺陷，可以修正

## 提案要求
- 分析需求后提出你的技术方案
- 说明为什么你的方案比保守方案更好
- 给出具体的实现路径`,
		PromptEn: `## Personality
You are a radical technical innovator. Your core instincts:
- Sees legacy code and immediately thinks refactor — tech debt should be paid early
- Pursues the latest technical paradigms and design patterns
- Willing to accept short-term risk for better architecture
- Believes elegant design reduces long-term cost

## Debate Style
- Boldly propose unconventional technical approaches
- Persuade others with code examples and architecture diagrams
- When challenged by conservatives, argue with long-term benefits
- But respect evidence — if proven your approach has fatal flaws, adapt

## Proposal Requirements
- Analyze the requirement and propose your technical approach
- Explain why your approach is better than conservative alternatives
- Provide a concrete implementation path`,
	},
	{
		ID:       "defender",
		Name:     "防守派",
		NameEn:   "Defender",
		Avatar:   "🛡️",
		Stance:   "谨慎、求稳，关注改动风险、向后兼容、边界条件",
		StanceEn: "Cautious, stability-first — focuses on change risk, backward compatibility, edge cases",
		Prompt: `## 性格
你是一个谨慎的防守者。你的核心本能：
- 任何改动都有风险——先证明安全再同意
- 向后兼容是底线，不能破坏现有调用方
- 关注边界条件和异常路径
- 相信稳定的系统比漂亮的系统更重要

## 辩论风格
- 对每个提案追问：这个改动会影响什么？有什么风险？
- 要求对方提供兼容方案和回滚方案
- 如果对方的方案确实安全且必要，可以同意
- 不为了反对而反对——只反对未经充分验证的方案

## 提案要求
- 分析需求后提出最安全、风险最小的方案
- 关注向后兼容性和边界条件
- 给出风险清单和缓解措施`,
		PromptEn: `## Personality
You are a cautious defender. Your core instincts:
- Every change carries risk — prove safety before agreement
- Backward compatibility is the baseline — never break existing callers
- Focus on edge cases and exception paths
- Believes a stable system is more important than a beautiful one

## Debate Style
- For every proposal, ask: what does this affect? What are the risks?
- Demand compatibility plans and rollback strategies
- If the proposal is proven safe and necessary, agree
- Don't oppose for opposition's sake — only oppose unverified proposals

## Proposal Requirements
- Analyze the requirement and propose the safest, lowest-risk approach
- Focus on backward compatibility and edge cases
- Provide a risk list and mitigation measures`,
	},
	{
		ID:       "pragmatic",
		Name:     "务实派",
		NameEn:   "Pragmatist",
		Avatar:   "🔧",
		Stance:   "结果导向、求快，最小改动达成目标，够用就好",
		StanceEn: "Results-oriented, speed-first — minimal changes to achieve the goal, good enough is enough",
		Prompt: `## 性格
你是一个务实的执行者。你的核心本能：
- 能跑就行——最小改动达成目标，别过度设计
- 时间是最稀缺的资源，早点交付比完美更重要
- 引入新依赖或新框架需要充分的理由
- 简单直接的方案通常就是最好的方案

## 辩论风格
- 对过于复杂的方案追问：能不能更简单？
- 对引入新技术/新框架的方案追问：现有工具不能解决吗？
- 如果对方的方案确实更快更简单，支持对方
- 反对为了「优雅」或「未来可能」而增加复杂度

## 提案要求
- 分析需求后提出最简单的可行方案
- 优先利用现有代码和工具
- 给出最少改动量的实现路径`,
		PromptEn: `## Personality
You are a pragmatic executor. Your core instincts:
- If it works, ship it — minimal changes to achieve the goal, don't over-engineer
- Time is the scarcest resource — early delivery beats perfection
- Introducing new dependencies or frameworks requires strong justification
- The simplest direct solution is usually the best

## Debate Style
- For overly complex proposals, ask: can this be simpler?
- For proposals introducing new tech/frameworks, ask: can existing tools solve this?
- If another proposal is genuinely faster and simpler, support it
- Oppose complexity added for "elegance" or "future possibility"

## Proposal Requirements
- Analyze the requirement and propose the simplest viable approach
- Prioritize existing code and tools
- Provide the minimal-change implementation path`,
	},
	{
		ID:       "advocate",
		Name:     "用户派",
		NameEn:   "Advocate",
		Avatar:   "👤",
		Stance:   "共情、体验优先，站在使用者角度，关注直觉和易用性",
		StanceEn: "Empathetic, experience-first — stands in the user's shoes, focuses on intuitiveness and usability",
		Prompt: `## 性格
你是一个用户代言人。你的核心本能：
- 站在使用者（调用方、终端用户、其他开发者）的角度思考
- API 设计应该直觉易用，调用方不应该需要读源码才能理解
- 好的开发者体验（DX）和用户体验（UX）同样重要
- 复杂的实现可以接受，但复杂的使用方式不能接受

## 辩论风格
- 对每个方案追问：调用方会怎么用这个？容易用错吗？
- 如果方案功能强大但使用复杂，要求简化接口
- 关注文档、命名、示例代码的质量
- 支持对用户最友好的方案，即使实现更复杂

## 提案要求
- 分析需求后从使用者角度提出方案
- 关注 API 设计、命名直觉、使用流程
- 给出调用示例说明你的方案为什么好用`,
		PromptEn: `## Personality
You are a user advocate. Your core instincts:
- Think from the user's perspective (callers, end users, other developers)
- API design should be intuitive — callers shouldn't need to read source code to understand
- Good developer experience (DX) and user experience (UX) are equally important
- Complex implementation is acceptable; complex usage is not

## Debate Style
- For every proposal, ask: how would callers use this? Is it easy to misuse?
- If a proposal is powerful but complex to use, demand interface simplification
- Focus on documentation, naming, and example code quality
- Support the most user-friendly proposal, even if implementation is harder

## Proposal Requirements
- Analyze the requirement and propose from the user's perspective
- Focus on API design, naming intuitiveness, and usage flow
- Provide usage examples showing why your approach is better`,
	},
}
```

- [ ] **Step 2: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./engine/...
```
Expected: 编译错误（引用 `DefaultRoundtableMembers` 的代码尚未更新），但 default_members.go 本身无语法错误。

- [ ] **Step 3: Commit**

```bash
git add engine/default_members.go
git commit -m "feat: replace domain roles with personality-based debate members"
```

---

### Task 3: 配置 — team 配置段和成员加载

**Files:**
- Modify: `config/config.go`

**Interfaces:**
- Produces: `TeamConfig` struct, `LoadTeamMembers()` 函数
- Consumes: `engine.RoundtableMember`

- [ ] **Step 1: 添加 team 配置段**

在 `config/config.go` 的 `File` 结构体中添加 `Team` 字段：

```go
type File struct {
	Model   modelConfig   `toml:"model"`
	Routing routingConfig `toml:"routing"`
	Context contextConfig `toml:"context"`
	Guards  guardsConfig  `toml:"guards"`
	Team    teamConfig    `toml:"team"`
	UI      uiConfig      `toml:"ui"`
}
```

在文件末尾添加 `teamConfig` 类型：

```go
type teamConfig struct {
	Members []string `toml:"members"` // member IDs to use (default: radical,defender,pragmatic,advocate)
}
```

- [ ] **Step 2: Apply 方法中添加 team 配置应用**

在 `Apply` 函数末尾添加：

```go
if len(f.Team.Members) > 0 {
	cfg.TeamMembers = f.Team.Members
}
```

- [ ] **Step 3: 在 EngineConfig 中添加 TeamMembers 字段**

在 `engine/types.go` 的 `EngineConfig` 结构体中添加：

```go
// TeamMembers is the ordered list of member IDs to use in /team debate mode.
// Empty = use DefaultDebateMembers.
TeamMembers []string
```

- [ ] **Step 4: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```
Expected: 部分编译错误（roundtable.go 未更新），但 config 和 types 变更应无问题。

- [ ] **Step 5: Commit**

```bash
git add config/config.go engine/types.go
git commit -m "feat: add team config section and TeamMembers engine config"
```

---

### Task 4: 核心辩论引擎 — 重写 roundtable.go

**Files:**
- Modify: `engine/roundtable.go`

**Interfaces:**
- Produces: `RoundtableHall.handleDebateArena(ctx) (*EngineResponse, error)` 
- Consumes: `SubAgentRunner.RunWithPrompt()`, `DebateRound`, `DebateOutput`, `DefaultDebateMembers`, `RoundtablePhase` 常量

- [ ] **Step 1: 删除旧代码，保留必要的基础设施**

删除以下函数（被辩论模式替代）：
- `handleTeamFlow`
- `runTeamExplore`
- `runMemberThought`
- `synthesizeTeamOutput`
- `handleReview`
- `runMemberReview`
- `collectAllFindings`
- `refuteFindings`
- `refuteOne`
- `memberError`
- `emitMemberDone`
- `emitThoughtDone`
- `parseMemberReview`
- `extractProposals`
- `detectConflicts`
- `parseReviewTarget`
- `parseVerdict` / `parseScore` / `parseSummaryLine` / `parseFindings` / `parseRefuteResult`
- `buildTeamExploreGoal`
- `extractTeamSummary`
- `RoundtableCommand` / `parseRoundtableCommand`（/round 命令不再支持）
- `memberName`

保留：
- `RoundtableHall` 结构体
- `NewRoundtableHall`
- `RoundtableMember`（displayName/displayStance/displayPrompt 方法）
- `findMember`
- `truncateString`
- `parseTeamCommand` / `TeamCommand`（稍后修改以支持 --members 参数）
- `Advance` 方法（重写）

- [ ] **Step 2: 添加核心辩论方法 `handleDebateArena`**

```go
// handleDebateArena orchestrates the full 4-round debate arena.
func (h *RoundtableHall) handleDebateArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Roundtable == nil {
		return nil, nil
	}

	zh := msgIsChinese(state.Roundtable.Goal)
	goal := state.Roundtable.Goal
	members := state.Roundtable.Members
	if len(members) == 0 {
		members = DefaultDebateMembers
	}

	// Resolve members from config if TeamMembers is set
	if len(h.engine.config.TeamMembers) > 0 {
		resolved := resolveMembers(h.engine.config.TeamMembers, DefaultDebateMembers)
		if len(resolved) > 0 {
			members = resolved
		}
	}
	state.Roundtable.Members = members

	// Round 1: Proposal — each member proposes independently
	if err := h.runDebateRound(ctx, DebateProposal, goal, members, zh); err != nil {
		return nil, fmt.Errorf("proposal round: %w", err)
	}
	state.Roundtable.Phase = RoundtableChallenge

	// Round 2: Challenge — each member challenges others' proposals
	if err := h.runDebateRound(ctx, DebateChallenge, goal, members, zh); err != nil {
		return nil, fmt.Errorf("challenge round: %w", err)
	}
	state.Roundtable.Phase = RoundtableRebuttal

	// Round 3: Rebuttal — each member responds to challenges against them
	if err := h.runDebateRound(ctx, DebateRebuttal, goal, members, zh); err != nil {
		return nil, fmt.Errorf("rebuttal round: %w", err)
	}
	state.Roundtable.Phase = RoundtableFinal

	// Round 4: Final — each member summarizes final position with scores
	if err := h.runDebateRound(ctx, DebateFinal, goal, members, zh); err != nil {
		return nil, fmt.Errorf("final round: %w", err)
	}
	state.Roundtable.Phase = RoundtableAwaitingVerdict

	return h.buildVerdictPrompt(goal, members, zh), nil
}
```

- [ ] **Step 3: 添加 `runDebateRound` — 一轮辩论的并行执行**

```go
// runDebateRound executes one round of the debate: all members run in parallel,
// each with visibility scoped to the current debate phase.
func (h *RoundtableHall) runDebateRound(ctx context.Context, phase DebateRoundPhase, goal string, members []RoundtableMember, zh bool) error {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "debate_phase",
			Name:   string(phase),
			Detail: phaseLabel(phase, zh),
		})
	}

	type task struct {
		Member RoundtableMember
		Index  int
	}
	tasks := make([]task, len(members))
	for i, m := range members {
		tasks[i] = task{Member: m, Index: i}
	}

	outputs := make([]DebateOutput, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, t := range tasks {
		wg.Add(1)
		go func(t task) {
			defer wg.Done()
			output := h.runMemberDebateTurn(ctx, t.Member, goal, phase, members, zh)
			mu.Lock()
			outputs[t.Index] = output
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	state := h.engine.state
	state.Roundtable.DebateRounds = append(state.Roundtable.DebateRounds, DebateRound{
		Phase:   phase,
		Outputs: outputs,
	})

	return nil
}
```

- [ ] **Step 4: 添加 `runMemberDebateTurn` — 单个成员在一轮辩论中的执行**

```go
// runMemberDebateTurn executes a single member's turn in a debate round.
// Visibility is scoped by phase: proposal sees only goal; challenge sees all proposals;
// rebuttal sees only challenges targeting this member; final sees the full record.
func (h *RoundtableHall) runMemberDebateTurn(ctx context.Context, member RoundtableMember, goal string, phase DebateRoundPhase, allMembers []RoundtableMember, zh bool) DebateOutput {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_start",
			Name:   member.ID,
			Detail: member.displayName(zh),
		})
	}

	taskGoal := buildDebateGoal(goal, member, phase, allMembers, h.engine.state.Roundtable.DebateRounds, zh)
	targets := determineTargets(member.ID, phase, allMembers)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 50,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		if h.engine.config.OnProgress != nil {
			h.engine.config.OnProgress(ProgressEvent{
				Type:   "member_done",
				Name:   member.ID,
				Detail: fmt.Sprintf("%s ❌", member.displayName(zh)),
			})
		}
		return DebateOutput{MemberID: member.ID, Content: fmt.Sprintf("analysis failed: %v", err)}
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	var content string
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, member.displayPrompt(zh))
		if err != nil {
			content = fmt.Sprintf("analysis failed: %v", err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	} else {
		result, err := agent.Run(ctx, handoff)
		if err != nil {
			content = fmt.Sprintf("analysis failed: %v", err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_done",
			Name:   member.ID,
			Detail: fmt.Sprintf("%s ✓", member.displayName(zh)),
		})
	}

	return DebateOutput{
		MemberID: member.ID,
		Content:  content,
		Targets:  targets,
	}
}
```

- [ ] **Step 5: 添加 `buildDebateGoal` — 构建每轮的辩论提示**

```go
// buildDebateGoal constructs the task prompt for a member in a specific debate phase.
func buildDebateGoal(goal string, member RoundtableMember, phase DebateRoundPhase, allMembers []RoundtableMember, rounds []DebateRound, zh bool) string {
	var sb strings.Builder

	switch phase {
	case DebateProposal:
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Task\nPropose your technical solution for the following requirement. You are working independently — other members will propose their own solutions.\n\n## Requirement\n%s\n\n## Your Role\n%s — %s\n\n## Output\nProvide a structured proposal: your approach, key design decisions, implementation path, and why it's the right choice.",
			"## 任务\n为以下需求提出你的技术方案。你独立工作——其他成员会提出他们自己的方案。\n\n## 需求\n%s\n\n## 你的角色\n%s — %s\n\n## 输出\n提供结构化方案：你的方法、关键设计决策、实现路径，以及为什么这是正确的选择。",
		), goal, member.displayName(zh), member.displayStance(zh)))

	case DebateChallenge:
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Task\nReview ALL proposals below and challenge the ones you disagree with. From your perspective, point out flaws, risks, or missed considerations.\n\n## Requirement\n%s\n\n## All Proposals\n",
			"## 任务\n审阅以下所有方案，从你的立场挑战你不同意的方案。指出缺陷、风险或遗漏的考量。\n\n## 需求\n%s\n\n## 所有方案\n",
		), goal))
		// Append all proposals from round 0
		if len(rounds) > 0 {
			for _, out := range rounds[0].Outputs {
				if out.MemberID == member.ID {
					continue // don't challenge your own
				}
				m := findMember(allMembers, out.MemberID)
				name := out.MemberID
				if m != nil {
					name = m.displayName(zh)
				}
				sb.WriteString(fmt.Sprintf("### %s's proposal\n%s\n\n", name, out.Content))
			}
		}
		sb.WriteString(pickPrompt(zh,
			"\n## Output\nFor each proposal you challenge, clearly state: which proposal, what the problem is, why it matters. Be specific — reference code or architectural facts when possible.",
			"\n## 输出\n对你挑战的每个方案，清晰说明：哪个方案、什么问题、为什么重要。尽量具体——引用代码或架构事实。",
		))

	case DebateRebuttal:
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Task\nRespond to the challenges raised against YOUR proposal. Defend valid points, concede where appropriate, and revise your proposal if needed.\n\n## Requirement\n%s\n\n## Your Original Proposal\n%s\n\n## Challenges Against Your Proposal\n",
			"## 任务\n回应针对你方案提出的质疑。为合理的观点辩护，适当让步，必要时修正你的方案。\n\n## 需求\n%s\n\n## 你的原始方案\n%s\n\n## 针对你方案的质疑\n",
		), goal, getOwnProposal(member.ID, rounds)))
		// Append challenges targeting this member from round 1
		if len(rounds) > 1 {
			for _, out := range rounds[1].Outputs {
				for _, target := range out.Targets {
					if target == member.ID {
						challenger := findMember(allMembers, out.MemberID)
						name := out.MemberID
						if challenger != nil {
							name = challenger.displayName(zh)
						}
						sb.WriteString(fmt.Sprintf("### From %s\n%s\n\n", name, out.Content))
					}
				}
			}
		}
		sb.WriteString(pickPrompt(zh,
			"\n## Output\nRespond to each challenge. If the challenge is valid, acknowledge it and revise your proposal. If invalid, explain why with evidence.",
			"\n## 输出\n回应每个质疑。如果质疑合理，承认并修正方案。如果不合理，用证据解释为什么。",
		))

	case DebateFinal:
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Task\nReview the complete debate record. State your final position, and score every proposal (including your own) on a 0-100 scale.\n\n## Requirement\n%s\n\n## Complete Debate Record\n%s\n\n## Output Format\n1. Your final position summary\n2. Score each proposal:\n   SCORE: <member_id> = <0-100>\n   REASON: <one-line reason>\nEnd with: VERDICT: <your preferred proposal member_id>",
			"## 任务\n审阅完整辩论记录。陈述你的最终立场，给每个方案（含自己的）打分（0-100）。\n\n## 需求\n%s\n\n## 完整辩论记录\n%s\n\n## 输出格式\n1. 你的最终立场总结\n2. 给每个方案打分:\n   SCORE: <member_id> = <0-100>\n   REASON: <一句话理由>\n以: VERDICT: <你支持的方案 member_id> 结尾",
		), goal, formatDebateRecord(rounds, allMembers, zh)))
	}

	return sb.String()
}
```

- [ ] **Step 6: 添加辅助函数**

```go
// determineTargets returns which member IDs this member should target in a given phase.
func determineTargets(memberID string, phase DebateRoundPhase, allMembers []RoundtableMember) []string {
	switch phase {
	case DebateChallenge:
		// Target everyone except self
		var targets []string
		for _, m := range allMembers {
			if m.ID != memberID {
				targets = append(targets, m.ID)
			}
		}
		return targets
	case DebateRebuttal:
		// Targets are filled from the challenge round outputs later
		return nil
	default:
		return nil
	}
}

// getOwnProposal retrieves a member's own proposal from round 0.
func getOwnProposal(memberID string, rounds []DebateRound) string {
	if len(rounds) == 0 {
		return "(proposal not found)"
	}
	for _, out := range rounds[0].Outputs {
		if out.MemberID == memberID {
			return out.Content
		}
	}
	return "(proposal not found)"
}

// formatDebateRecord renders the full debate record for the final round.
func formatDebateRecord(rounds []DebateRound, members []RoundtableMember, zh bool) string {
	var sb strings.Builder
	for i, round := range rounds {
		sb.WriteString(fmt.Sprintf("## Round %d: %s\n\n", i+1, phaseLabel(round.Phase, zh)))
		for _, out := range round.Outputs {
			m := findMember(members, out.MemberID)
			name := out.MemberID
			if m != nil {
				name = fmt.Sprintf("%s %s", m.Avatar, m.displayName(zh))
			}
			sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", name, out.Content))
		}
	}
	return sb.String()
}

// phaseLabel returns a human-readable label for a debate phase.
func phaseLabel(phase DebateRoundPhase, zh bool) string {
	switch phase {
	case DebateProposal:
		return pickPrompt(zh, "Proposals", "提案轮")
	case DebateChallenge:
		return pickPrompt(zh, "Challenges", "质询轮")
	case DebateRebuttal:
		return pickPrompt(zh, "Rebuttals", "反驳轮")
	case DebateFinal:
		return pickPrompt(zh, "Final Statements", "终陈轮")
	default:
		return string(phase)
	}
}

// resolveMembers resolves member IDs from config against the defaults.
// Returns nil if no valid members found (caller should fall back to defaults).
func resolveMembers(ids []string, defaults []RoundtableMember) []RoundtableMember {
	var result []RoundtableMember
	for _, id := range ids {
		for _, d := range defaults {
			if d.ID == id {
				result = append(result, d)
				break
			}
		}
	}
	return result
}
```

- [ ] **Step 7: 添加 `buildVerdictPrompt` — 生成裁决界面**

```go
// buildVerdictPrompt generates the verdict prompt shown to the user after the debate.
func (h *RoundtableHall) buildVerdictPrompt(goal string, members []RoundtableMember, zh bool) *EngineResponse {
	var sb strings.Builder

	if zh {
		sb.WriteString("## 🤝 辩论完成 — 请裁决\n\n")
		sb.WriteString(fmt.Sprintf("**需求**: %s\n\n", goal))
	} else {
		sb.WriteString("## 🤝 Debate Complete — Your Verdict\n\n")
		sb.WriteString(fmt.Sprintf("**Goal**: %s\n\n", goal))
	}

	state := h.engine.state
	rounds := state.Roundtable.DebateRounds

	// Show each member's proposal with scores from the final round
	if len(rounds) > 0 {
		for _, out := range rounds[0].Outputs {
			m := findMember(members, out.MemberID)
			avatar := ""
			name := out.MemberID
			if m != nil {
				avatar = m.Avatar
				name = m.displayName(zh)
			}

			sb.WriteString(fmt.Sprintf("### 方案: %s %s\n\n", avatar, name))
			sb.WriteString(truncateString(out.Content, 500))
			sb.WriteString("\n\n")

			// Show scores from final round
			if len(rounds) >= 4 {
				sb.WriteString(pickPrompt(zh, "**评分**: ", "**Scores**: "))
				scores := extractScores(rounds[3].Outputs, members, zh)
				sb.WriteString(scores)
				sb.WriteString("\n\n")
			}
		}
	}

	if zh {
		sb.WriteString("---\n\n")
		sb.WriteString("**你的裁决**: 输入 `支持方案<角色名>`、`方案<角色名>但要<条件>`、`都不行，应该<你的方案>`、或 `再辩一轮`\n")
	} else {
		sb.WriteString("---\n\n")
		sb.WriteString("**Your verdict**: Type `support <role>`, `<role> but <condition>`, `none, should <your approach>`, or `debate again`\n")
	}

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}

// extractScores extracts SCORE lines from final round outputs.
func extractScores(outputs []DebateOutput, members []RoundtableMember, zh bool) string {
	var parts []string
	for _, out := range outputs {
		m := findMember(members, out.MemberID)
		name := out.MemberID
		avatar := ""
		if m != nil {
			avatar = m.Avatar
			name = m.displayName(zh)
		}
		// Parse SCORE: lines from content
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(trimmed), "score:") {
				parts = append(parts, fmt.Sprintf("%s%s: %s", avatar, name, strings.TrimSpace(trimmed[len("score:"):])))
			}
		}
	}
	return strings.Join(parts, " | ")
}
```

- [ ] **Step 8: 重写 `Advance` 方法**

```go
// Advance handles user input during the debate phases.
func (h *RoundtableHall) Advance(ctx context.Context, userMsg string) (*EngineResponse, error) {
	state := h.engine.state
	if state.Roundtable == nil {
		return nil, nil
	}

	zh := msgIsChinese(userMsg)
	if !zh && userMsg == "" {
		zh = msgIsChinese(state.Roundtable.Goal)
	}

	lower := strings.ToLower(strings.TrimSpace(userMsg))

	switch state.Roundtable.Phase {
	case RoundtableAwaitingVerdict:
		return h.handleVerdict(userMsg, lower, zh), nil
	case RoundtableDone:
		return nil, nil
	default:
		return nil, nil
	}
}

// handleVerdict processes the user's verdict after the debate.
func (h *RoundtableHall) handleVerdict(userMsg, lower string, zh bool) *EngineResponse {
	state := h.engine.state

	// "再辩一轮" / "debate again" / "继续"
	if strings.Contains(lower, "再辩") || strings.Contains(lower, "继续") ||
		strings.Contains(lower, "debate again") || lower == "again" {
		state.Roundtable.Phase = RoundtableProposal
		return &EngineResponse{
			Summary: pickPrompt(zh, "Starting another debate round...", "开始新一轮辩论..."),
			Stage:   StageAct,
		}
	}

	// User picks a proposal or provides their own
	pinned := fmt.Sprintf("[TEAM PLAN: %s]\n\n%s", state.Roundtable.Goal, userMsg)
	h.engine.pendingPinnedMessages = append(h.engine.pendingPinnedMessages, pinned)
	state.Roundtable.Phase = RoundtableDone

	return &EngineResponse{
		Summary: pickPrompt(zh,
			fmt.Sprintf("✅ Verdict recorded. Proceeding with: %s", userMsg),
			fmt.Sprintf("✅ 裁决已记录。将按以下方向执行: %s", userMsg),
		),
		Stage: StageAct,
	}
}
```

- [ ] **Step 9: 修改 `parseTeamCommand` 支持 `--members` 参数**

在 `parseTeamCommand` 中添加成员解析：

```go
// TeamCommand represents a parsed /team command.
type TeamCommand struct {
	Goal        string
	MemberIDs   []string // from --members flag
	AddMemberPath string // from --add flag
}

// parseTeamCommand checks if userMsg is a /team command.
func parseTeamCommand(userMsg string) *TeamCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	// Split by spaces but respect quoted strings
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "team" {
		return nil
	}

	tc := &TeamCommand{}
	i := 1
	for i < len(parts) {
		switch parts[i] {
		case "--members":
			if i+1 < len(parts) {
				tc.MemberIDs = strings.Split(parts[i+1], ",")
				i += 2
			} else {
				i++
			}
		case "--add":
			if i+1 < len(parts) {
				tc.AddMemberPath = parts[i+1]
				i += 2
			} else {
				i++
			}
		default:
			// Everything else is the goal
			tc.Goal = strings.Join(parts[i:], " ")
			i = len(parts)
		}
	}

	if tc.Goal == "" {
		return nil
	}
	return tc
}
```

- [ ] **Step 10: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./engine/...
```
Expected: 编译成功。如有编译错误，根据错误信息修复。

- [ ] **Step 11: Commit**

```bash
git add engine/roundtable.go
git commit -m "feat: rewrite roundtable as debate arena with 4-round flow"
```

---

### Task 5: Loop 集成 — 更新 /team 处理

**Files:**
- Modify: `engine/loop.go`

- [ ] **Step 1: 更新 /team 命令处理**

在 `loop.go` 中找到 `parseTeamCommand` 调用处（约第 229 行），替换为：

```go
// Team command handling — /team <goal>
// Activates the debate arena: 4-round structured debate → user verdict.
if tc := parseTeamCommand(userMsg); tc != nil {
	e.state.Roundtable = &RoundtableState{
		Goal:  tc.Goal,
		Phase: RoundtableProposal,
	}
	// Resolve members: command-line > config > defaults
	if len(tc.MemberIDs) > 0 {
		resolved := resolveMembers(tc.MemberIDs, DefaultDebateMembers)
		if len(resolved) > 0 {
			e.state.Roundtable.Members = resolved
		}
	}
	// Replace raw "/team <goal>" so the main agent loop sees a proper prompt
	if len(e.history) > 0 {
		e.history[len(e.history)-1].Content = fmt.Sprintf(
			"辩论模式已启动：%s\n\n请等待团队成员完成辩论。",
			tc.Goal)
		userMsg = fmt.Sprintf("辩论模式已启动：%s\n\n请等待团队成员完成辩论。", tc.Goal)
	}
}
```

- [ ] **Step 2: 更新辩论阶段的分发逻辑**

找到旧的分发逻辑（约第 400-419 行），替换为：

```go
// Debate Arena phase — execute the current debate round, then return
// the round result to the user. The engine continues to the next round
// on the next Run() call until AwaitingVerdict.
if e.state.Roundtable != nil {
	phase := e.state.Roundtable.Phase
	switch phase {
	case RoundtableProposal, RoundtableChallenge, RoundtableRebuttal, RoundtableFinal:
		response, err := e.roundtableHall.handleDebateArena(ctx)
		if err != nil {
			return nil, fmt.Errorf("debate arena: %w", err)
		}
		if response != nil {
			return response, nil
		}
	case RoundtableAwaitingVerdict:
		response, err := e.roundtableHall.Advance(ctx, userMsg)
		if err != nil {
			return nil, fmt.Errorf("verdict: %w", err)
		}
		if response != nil {
			return response, nil
		}
	case RoundtableDone:
		// Debate complete — clear roundtable state so normal flow resumes.
		// The verdict was already injected as a pinned message.
		e.state.Roundtable = nil
	}
}
```

注意：`handleDebateArena` 会执行**所有 4 轮**在一个 Run() 调用中。每个回合用户会看到中间结果（通过 progress 事件），然后最终的裁决提示。如果需要在每轮之间等待用户确认，需要改为逐轮执行——但当前设计是 4 轮连续执行后展示裁决界面，符合「用户看完整吵架过程」的需求。

- [ ] **Step 3: 移除旧的 roundtable Advance 调用**

删除 `e.roundtableHall.Advance(ctx, userMsg)` 的旧调用（已被上面的 switch 覆盖）。

- [ ] **Step 4: 编译验证**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```
Expected: 编译成功。

- [ ] **Step 5: Commit**

```bash
git add engine/loop.go
git commit -m "feat: integrate debate arena into engine loop"
```

---

### Task 6: 测试更新

**Files:**
- Modify: `engine/roundtable_test.go`

- [ ] **Step 1: 更新 /team 命令解析测试**

保留 `TestParseTeamCommand_Valid` 和 `TestParseTeamCommand_WithExtraWhitespace`，但更新期望值以匹配新的 `TeamCommand` 结构（MemberIDs 字段）。

添加 `--members` 解析测试：

```go
func TestParseTeamCommand_WithMembers(t *testing.T) {
	cmd := parseTeamCommand("/team --members radical,defender 重构认证")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "重构认证" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "重构认证")
	}
	if len(cmd.MemberIDs) != 2 || cmd.MemberIDs[0] != "radical" || cmd.MemberIDs[1] != "defender" {
		t.Errorf("MemberIDs = %v, want [radical defender]", cmd.MemberIDs)
	}
}

func TestParseTeamCommand_WithAdd(t *testing.T) {
	cmd := parseTeamCommand("/team --add ~/.deepact/members/perf.toml 优化查询")
	if cmd == nil {
		t.Fatal("expected non-nil TeamCommand")
	}
	if cmd.Goal != "优化查询" {
		t.Errorf("Goal = %q, want %q", cmd.Goal, "优化查询")
	}
	if cmd.AddMemberPath != "~/.deepact/members/perf.toml" {
		t.Errorf("AddMemberPath = %q", cmd.AddMemberPath)
	}
}
```

- [ ] **Step 2: 删除旧测试函数**

删除以下测试（测试已删除的函数）：
- `TestHandleTeamFlow_*`
- `TestHandleReview_*`
- `TestRefuteFindings_*`
- `TestDetectConflicts_*`
- `TestParseReviewTarget_*`
- `TestParseVerdict_*`
- `TestParseScore_*`

- [ ] **Step 3: 添加辩论流程测试**

```go
func TestDebateArena_ProposalRound(t *testing.T) {
	e := newTestEngine()
	e.state = &TaskState{
		Roundtable: &RoundtableState{
			Goal:    "实现一个简单的缓存层",
			Phase:   RoundtableProposal,
			Members: DefaultDebateMembers[:2], // use only 2 members for faster test
		},
	}
	e.roundtableHall = NewRoundtableHall(e)

	resp, err := e.roundtableHall.handleDebateArena(context.Background())
	if err != nil {
		t.Fatalf("handleDebateArena() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// Should have completed all 4 rounds and be awaiting verdict
	if e.state.Roundtable.Phase != RoundtableAwaitingVerdict {
		t.Errorf("Phase = %v, want RoundtableAwaitingVerdict", e.state.Roundtable.Phase)
	}
	if len(e.state.Roundtable.DebateRounds) != 4 {
		t.Errorf("got %d debate rounds, want 4", len(e.state.Roundtable.DebateRounds))
	}
}

func TestDebateArena_VerdictPick(t *testing.T) {
	e := newTestEngine()
	e.state = &TaskState{
		Roundtable: &RoundtableState{
			Goal:    "测试裁决",
			Phase:   RoundtableAwaitingVerdict,
			Members: DefaultDebateMembers,
		},
	}
	e.roundtableHall = NewRoundtableHall(e)

	resp, err := e.roundtableHall.Advance(context.Background(), "支持创新派的方案")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if e.state.Roundtable.Phase != RoundtableDone {
		t.Errorf("Phase = %v, want RoundtableDone", e.state.Roundtable.Phase)
	}
}

func TestDebateArena_VerdictDebateAgain(t *testing.T) {
	e := newTestEngine()
	e.state = &TaskState{
		Roundtable: &RoundtableState{
			Goal:    "测试再辩",
			Phase:   RoundtableAwaitingVerdict,
			Members: DefaultDebateMembers,
		},
	}
	e.roundtableHall = NewRoundtableHall(e)

	resp, err := e.roundtableHall.Advance(context.Background(), "再辩一轮")
	if err != nil {
		t.Fatalf("Advance() unexpected error: %v", err)
	}
	if e.state.Roundtable.Phase != RoundtableProposal {
		t.Errorf("Phase = %v, want RoundtableProposal", e.state.Roundtable.Phase)
	}
}
```

- [ ] **Step 4: 运行测试**

```bash
cd /Users/admin/gitspace/deepact && go test ./engine/... -run "TestParseTeamCommand|TestDebateArena" -v -count=1
```
Expected: 测试通过（parse 相关）或编译通过（debate 相关需 mock agent）。

- [ ] **Step 5: Commit**

```bash
git add engine/roundtable_test.go
git commit -m "test: update roundtable tests for debate arena"
```

---

### Task 7: 清理 — 移除不再使用的引用

**Files:**
- Modify: `ui/model.go`
- Modify: `cmd/run.go`（如有旧 /team 引用）

- [ ] **Step 1: 检查全项目编译**

```bash
cd /Users/admin/gitspace/deepact && go build ./...
```

修复所有编译错误。可能涉及：
- `ui/model.go` 中对旧 `RoundtablePhase` 的引用
- 其他文件中引用已删除的类型

- [ ] **Step 2: 运行完整测试套件**

```bash
cd /Users/admin/gitspace/deepact && go test ./... -count=1
```

- [ ] **Step 3: Commit**

```bash
git add -A
git commit -m "chore: cleanup old team code, fix compilation"
```
