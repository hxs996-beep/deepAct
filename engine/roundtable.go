package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// roundtableMemberMaxIterations bounds each debate member's sub-agent loop.
// A debate lens reasons about the requirement rather than exhaustively editing,
// so a high cap wastes time/tokens (4 members × 4 rounds × up-to-N iterations
// dominates the /team latency). 15 is enough for a member to grep/read a couple
// of files for grounding without looping; it cuts the debate wall-clock
// substantially while keeping analysis quality.
const roundtableMemberMaxIterations = 15

// roundtableSearchMaxIterations bounds the pre-debate shared search agent.
// A single codebase scan needs a bit more budget than a debate member's
// reasoning turn (15), but is still capped to bound wall-clock.
const roundtableSearchMaxIterations = 25

// RoundtableMember defines a single reviewer's identity and stance.
// Name/Stance/Prompt hold the Chinese values (the historical defaults);
// NameEn/StanceEn/PromptEn hold the English variants. The live value is picked
// per-call via the display* helpers based on the session language.
type RoundtableMember struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NameEn   string `json:"name_en,omitempty"`
	Avatar   string `json:"avatar"`
	Stance   string `json:"stance"`
	StanceEn string `json:"stance_en,omitempty"`
	Prompt   string `json:"prompt"`              // Chinese system-level instruction injected as extraPrompt
	PromptEn string `json:"prompt_en,omitempty"` // English variant
}

// displayName returns the member's name in the language matching zh.
func (m RoundtableMember) displayName(zh bool) string {
	if zh || m.NameEn == "" {
		return m.Name
	}
	return m.NameEn
}

// displayStance returns the member's stance in the language matching zh.
func (m RoundtableMember) displayStance(zh bool) string {
	if zh || m.StanceEn == "" {
		return m.Stance
	}
	return m.StanceEn
}

// displayPrompt returns the member's role prompt in the language matching zh.
func (m RoundtableMember) displayPrompt(zh bool) string {
	if zh || m.PromptEn == "" {
		return m.Prompt
	}
	return m.PromptEn
}

// TeamCommand represents a parsed /team command.
type TeamCommand struct {
	Goal          string
	MemberIDs     []string // from --members flag
	AddMemberPath string   // from --add flag
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
			tc.Goal = strings.Join(parts[i:], " ")
			i = len(parts)
		}
	}

	if tc.Goal == "" {
		return nil
	}
	return tc
}

// RoundtableHall orchestrates the roundtable flow.
type RoundtableHall struct {
	engine *Engine
}

func NewRoundtableHall(e *Engine) *RoundtableHall {
	return &RoundtableHall{engine: e}
}

// runSharedSearch runs a single code-search sub-agent that scans the repository
// for context relevant to the debate goal, returning a structured report used
// as the shared baseline for all debate members. Returns "" on failure so the
// debate can proceed without it (members still have their own tools).
func (h *RoundtableHall) runSharedSearch(ctx context.Context, goal string, zh bool) string {
	if h.engine.config.OnProgress != nil {
		// 事件类型用 "team_search" 而非 "debate_phase"：
		// TestDebateArena_ProgressEvents (roundtable_test.go:355-362) 断言恰好
		// 4 个 debate_phase 事件（4 轮辩论），预搜索不可计入该轮次计数。
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "team_search",
			Name:   "search",
			Detail: pickPrompt(zh, "Searching codebase...", "正在搜索代码库..."),
		})
	}
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          buildSearchGoal(goal, zh),
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: roundtableSearchMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}
	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

// buildSearchGoal instructs the pre-debate search agent to produce a structured
// codebase report for the debate goal. Research-only: it must not propose solutions.
func buildSearchGoal(goal string, zh bool) string {
	return fmt.Sprintf(pickPrompt(zh,
		`## Task
Search the codebase for context relevant to the following requirement. Produce a concise, structured report that will be shared with multiple debate members as their baseline.

## Requirement
%s

## Report Format
- **Relevant files**: exact paths + one-line purpose each
- **Key code**: short snippets or precise references (function/type names + file:line) that the requirement touches
- **Constraints**: existing conventions, interfaces, callers that constrain changes
- **Risks**: hotspots, edge cases, likely failure points

Be factual and cite file paths. Do NOT propose solutions — this is research only.`,
		`## 任务
搜索代码库中与以下需求相关的上下文。产出一份简洁、结构化的调研报告，将作为多名辩论成员的共享基线。

## 需求
%s

## 报告格式
- **相关文件**：精确路径 + 每行一句话用途
- **关键代码**：简短片段或精确引用（函数/类型名 + file:行号），说明需求涉及哪些代码
- **约束**：现有约定、接口、调用方对改动的限制
- **风险**：热点、边界情况、可能的失败点

务必基于事实并引用文件路径。不要提方案——这只是调研。`), goal)
}

// handleDebateArena orchestrates the full 4-round debate arena.
// It only executes rounds that haven't been completed yet (safe to re-enter
// after a partial failure).
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
	state.Roundtable.Members = members

	// Pre-search: run the codebase scan once as the shared baseline for all
	// members. Idempotent — skipped if already populated (re-entry after a
	// partial failure or a "debate again" round).
	if state.Roundtable.SharedContext == "" {
		state.Roundtable.SharedContext = h.runSharedSearch(ctx, goal, zh)
	}

	phase := state.Roundtable.Phase

	// Round 1: Proposal — each member proposes independently
	if phase <= RoundtableProposal {
		if err := h.runDebateRound(ctx, DebateProposal, goal, members, zh); err != nil {
			return nil, fmt.Errorf("proposal round: %w", err)
		}
		state.Roundtable.Phase = RoundtableChallenge
	}

	// Round 2: Challenge — each member challenges others' proposals
	if state.Roundtable.Phase <= RoundtableChallenge {
		if err := h.runDebateRound(ctx, DebateChallenge, goal, members, zh); err != nil {
			return nil, fmt.Errorf("challenge round: %w", err)
		}
		state.Roundtable.Phase = RoundtableRebuttal
	}

	// Round 3: Rebuttal — each member responds to challenges against them
	if state.Roundtable.Phase <= RoundtableRebuttal {
		if err := h.runDebateRound(ctx, DebateRebuttal, goal, members, zh); err != nil {
			return nil, fmt.Errorf("rebuttal round: %w", err)
		}
		state.Roundtable.Phase = RoundtableFinal
	}

	// Round 4: Final — each member summarizes final position with scores
	if state.Roundtable.Phase <= RoundtableFinal {
		if err := h.runDebateRound(ctx, DebateFinal, goal, members, zh); err != nil {
			return nil, fmt.Errorf("final round: %w", err)
		}
		state.Roundtable.Phase = RoundtableAwaitingVerdict
	}

	// Determine the winner by average score (ties broken by challenge data).
	var synthesis string
	if w := determineWinner(members, state.Roundtable.DebateRounds); w != nil {
		state.Roundtable.WinnerID = w.ID
		// Generate the detailed blueprint; only fall back to the concise
		// synthesis LLM call if the blueprint generation fails (saves tokens).
		state.Roundtable.Blueprint = h.buildBlueprint(ctx, goal, members, zh, *w, state.Roundtable.DebateRounds)
	}
	if state.Roundtable.Blueprint == "" {
		synthesis = h.synthesizeDebate(ctx, goal, members, zh)
	}
	return h.buildVerdictPrompt(goal, members, zh, synthesis), nil
}

// synthesizeDebate runs a final LLM call to produce a concise structured summary
// of the entire debate. Returns empty string on failure (caller falls back to
// verbose member viewpoints).
func (h *RoundtableHall) synthesizeDebate(ctx context.Context, goal string, members []RoundtableMember, zh bool) string {
	rounds := h.engine.state.Roundtable.DebateRounds
	if len(rounds) < 4 {
		return ""
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "synthesis",
			Name:   "synthesis",
			Detail: pickPrompt(zh, "Synthesizing debate...", "正在合成辩论摘要..."),
		})
	}

	record := formatDebateRecord(rounds, members, zh)
	taskGoal := fmt.Sprintf(pickPrompt(zh,
		"## Task\nYou are the debate arena judge's assistant. Below is the complete debate record. Produce a concise summary to help the user decide quickly.\n\n## Requirement\n%s\n\n## Complete Debate Record\n%s\n\n## Output Format\nFollow this format strictly:\n\n**Top Recommendation**: <winning member name> (avg score X, Y votes)\n<1-2 sentences explaining why>\n\n**One-liner per proposal**:\n- <avatar> <member name>: <core approach>. <main concern raised>\n(one line per proposal, ordered by score descending)\n\n**Strongest Challenge**: <challenger> -> <challenged> (confidence X)\n<challenge summary, 1-2 sentences>\n\n**Best Rebuttal**: <rebuttal author>\n<rebuttal summary, 1-2 sentences>\n\n**Key Disagreements**: <2-3 sentences summarizing core disagreements>",
		"## 任务\n你是辩论场裁判助理。以下是完整的辩论记录。请产出一份精简摘要，帮助用户快速决策。\n\n## 需求\n%s\n\n## 完整辩论记录\n%s\n\n## 输出格式\n请严格按以下格式输出：\n\n**综合推荐**: <获胜方案角色名>（平均分 X，获 Y 票）\n<1-2句话说明推荐理由>\n\n**各方案一句话**:\n- <avatar> <角色名>: <方案核心思路>。<主要被质疑的问题>\n（每个方案一行，按评分从高到低排列）\n\n**最强挑战**: <挑战者> -> <被挑战者>（置信度 X）\n<挑战内容摘要，1-2句>\n\n**最佳反驳**: <反驳者>\n<反驳要点，1-2句>\n\n**关键分歧**: <2-3句话总结辩论中的核心分歧点>",
	), goal, record)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 3,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, "")
		if err != nil || result == nil {
			return ""
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

// buildBlueprint runs a single LLM call that rewrites the winning proposal into
// a detailed, executable implementation blueprint, absorbing reasonable
// corrections raised during the challenge/rebuttal rounds (the LLM judges which
// corrections to absorb from the full debate record). Returns "" on failure so
// the verdict screen falls back to the concise synthesis.
func (h *RoundtableHall) buildBlueprint(ctx context.Context, goal string, members []RoundtableMember, zh bool, winner RoundtableMember, rounds []DebateRound) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "synthesis",
			Name:   "blueprint",
			Detail: pickPrompt(zh, "Writing implementation blueprint...", "正在撰写实施蓝图..."),
		})
	}
	record := formatDebateRecord(rounds, members, zh)
	winnerProposal := getMemberOutput(winner.ID, rounds[0].Outputs)
	if winnerProposal == "" {
		return ""
	}

	taskGoal := fmt.Sprintf(pickPrompt(zh,
		`## Task
You are a senior engineer. Rewrite the winning proposal below into a detailed, executable implementation blueprint that a coding agent can follow directly. Absorb any reasonable corrections raised in the challenges (mark absorbed corrections explicitly).

## Requirement
%s

## Winning Proposal
%s

## Complete Debate Record
%s

## Output Format
## 方案概述
<2-3 sentences>

## 关键设计决策
<numbered list: each decision + rationale; mark absorbed corrections as (来自质询修正)>

## 实现步骤
<numbered list: each step = what to change + where (file/function)>

## 风险与回滚
<bullet list: risk -> mitigation; rollback plan>`,
		`## 任务
你是一位资深工程师。把下面的获胜方案重写为一份详细的、可直接执行的实施蓝图，供编码 agent 直接照做。吸收质询中提出的合理修正（被吸收的修正请明确标注）。

## 需求
%s

## 获胜方案
%s

## 完整辩论记录
%s

## 输出格式
## 方案概述
<2-3句>

## 关键设计决策
<编号列表：每个决策+理由；被吸收的修正标注 (来自质询修正)>

## 实现步骤
<编号列表：每步=改什么+改哪里（文件/函数）>

## 风险与回滚
<列表：风险 -> 缓解；回滚方案>`), goal, winnerProposal, record)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 3,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return ""
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, "")
		if err != nil || result == nil {
			return ""
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary
}

// runDebateRound executes one round of the debate: all members run in parallel,
// each with visibility scoped to the current debate phase.
func (h *RoundtableHall) runDebateRound(ctx context.Context, phase DebateRoundPhase, goal string, members []RoundtableMember, zh bool) error {
	roundStart := time.Now()
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

	fmt.Fprintf(os.Stderr, "[debate] round %s done in %.1fs\n", phaseLabel(phase, zh), time.Since(roundStart).Seconds())

	state := h.engine.state
	state.Roundtable.DebateRounds = append(state.Roundtable.DebateRounds, DebateRound{
		Phase:   phase,
		Outputs: outputs,
	})

	return nil
}

// runMemberDebateTurn executes a single member's turn in a debate round.
// Visibility is scoped by phase: proposal sees only goal; challenge sees all proposals;
// rebuttal sees only challenges targeting this member; final sees the full record.
func (h *RoundtableHall) runMemberDebateTurn(ctx context.Context, member RoundtableMember, goal string, phase DebateRoundPhase, allMembers []RoundtableMember, zh bool) DebateOutput {
	memberStart := time.Now()
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_start",
			Name:   member.ID,
			Detail: member.displayName(zh),
		})
	}

	taskGoal := buildDebateGoal(goal, member, phase, allMembers, h.engine.state.Roundtable.DebateRounds, zh, h.engine.state.Roundtable.SharedContext)
	targets := determineTargets(member.ID, phase, allMembers)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: roundtableMemberMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		if h.engine.config.OnProgress != nil {
			h.engine.config.OnProgress(ProgressEvent{
				Type:   "member_done",
				Name:   member.ID,
				Detail: fmt.Sprintf("%s ✗", member.displayName(zh)),
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
	fmt.Fprintf(os.Stderr, "[debate]   member %s (%s) done in %.1fs, contentLen=%d\n",
		member.ID, phaseLabel(phase, zh), time.Since(memberStart).Seconds(), len(content))

	return DebateOutput{
		MemberID: member.ID,
		Content:  content,
		Targets:  targets,
	}
}

// buildDebateGoal constructs the task prompt for a member in a specific debate phase.
func buildDebateGoal(goal string, member RoundtableMember, phase DebateRoundPhase, allMembers []RoundtableMember, rounds []DebateRound, zh bool, sharedContext string) string {
	var sb strings.Builder

	if sharedContext != "" {
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Shared Code Research\nShared findings from a code-search agent. Use as a baseline, but verify with your own tools before relying on them.\n\n%s\n\n",
			"## 共享代码调研\n代码搜索 agent 的共享调研结果。作为基线使用，但请用你自己的工具核实后再依赖。\n\n%s\n\n"), sharedContext))
	}

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
		if len(rounds) > 0 {
			for _, out := range rounds[0].Outputs {
				if out.MemberID == member.ID {
					continue
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
			"\n## Output\nFor each proposal you challenge, use this format:\n\n### Challenge: <target role name>\n<what the problem is, why it matters - be specific, reference code or architectural facts>\nCONFIDENCE: <0.0-1.0>\n\nOnly include challenges you're confident about. Use 0.9+ for certain issues, 0.7-0.8 for likely issues, below 0.7 for minor concerns.",
			"\n## 输出\n对你挑战的每个方案，使用以下格式：\n\n### 挑战: <被挑战角色名>\n<什么问题、为什么重要--尽量具体，引用代码或架构事实>\nCONFIDENCE: <0.0-1.0>\n\n只包含你有把握的挑战。非常有把握设 0.9+，较有把握设 0.7-0.8，不太确定设 0.7 以下。",
		))

	case DebateRebuttal:
		sb.WriteString(fmt.Sprintf(pickPrompt(zh,
			"## Task\nRespond to the challenges raised against YOUR proposal. Defend valid points, concede where appropriate, and revise your proposal if needed.\n\n## Requirement\n%s\n\n## Your Original Proposal\n%s\n\n## Challenges Against Your Proposal\n",
			"## 任务\n回应针对你方案提出的质疑。为合理的观点辩护，适当让步，必要时修正你的方案。\n\n## 需求\n%s\n\n## 你的原始方案\n%s\n\n## 针对你方案的质疑\n",
		), goal, getOwnProposal(member.ID, rounds)))
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
			"## Task\nReview the complete debate record. State your final position, and score every proposal (including your own) on a 0-100 scale.\n\n## Requirement\n%s\n\n## Complete Debate Record\n%s\n\n## Output Format\n1. Your final position summary\n2. Score each proposal using the member_id shown in the debate record headers (e.g. SCORE: radical = 85):\n   SCORE: <member_id> = <0-100>\n   REASON: <one-line reason>\nEnd with: VERDICT: <your preferred proposal member_id>",
			"## 任务\n审阅完整辩论记录。陈述你的最终立场，给每个方案（含自己的）打分（0-100）。\n\n## 需求\n%s\n\n## 完整辩论记录\n%s\n\n## 输出格式\n1. 你的最终立场总结\n2. 使用辩论记录标题中括号里的 member_id 给每个方案打分（如 SCORE: radical = 85）:\n   SCORE: <member_id> = <0-100>\n   REASON: <一句话理由>\n以: VERDICT: <你支持的方案 member_id> 结尾",
		), goal, formatDebateRecord(rounds, allMembers, zh)))
	}

	return sb.String()
}

// determineTargets returns which member IDs this member should target in a given phase.
func determineTargets(memberID string, phase DebateRoundPhase, allMembers []RoundtableMember) []string {
	switch phase {
	case DebateChallenge:
		var targets []string
		for _, m := range allMembers {
			if m.ID != memberID {
				targets = append(targets, m.ID)
			}
		}
		return targets
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
				name = fmt.Sprintf("%s %s (id: %s)", m.Avatar, m.displayName(zh), m.ID)
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

// memberFileTOML mirrors the structure of a member definition TOML file.
// Supports the format documented in the debate arena design:
//
//	id = "perf-freak"
//	name = "性能狂"
//	avatar = "⚡"
//	stance = "..."
//	prompt = """..."""
type memberFileTOML struct {
	ID       string `toml:"id"`
	Name     string `toml:"name"`
	NameEn   string `toml:"name_en"`
	Avatar   string `toml:"avatar"`
	Stance   string `toml:"stance"`
	StanceEn string `toml:"stance_en"`
	Prompt   string `toml:"prompt"`
	PromptEn string `toml:"prompt_en"`
}

// loadMemberFromFile reads a TOML member definition file and returns a
// RoundtableMember. Returns an error if the file cannot be read or parsed.
func loadMemberFromFile(path string) (*RoundtableMember, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read member file %s: %w", path, err)
	}
	var mf memberFileTOML
	if err := toml.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse member file %s: %w", path, err)
	}
	if mf.ID == "" {
		return nil, fmt.Errorf("member file %s: missing required field 'id'", path)
	}
	return &RoundtableMember{
		ID:       mf.ID,
		Name:     mf.Name,
		NameEn:   mf.NameEn,
		Avatar:   mf.Avatar,
		Stance:   mf.Stance,
		StanceEn: mf.StanceEn,
		Prompt:   mf.Prompt,
		PromptEn: mf.PromptEn,
	}, nil
}

// verdictTally represents one member's vote count from the final round.
type verdictTally struct {
	memberID string
	avatar   string
	name     string
	votes    int
}

// parseVerdicts extracts VERDICT: lines from final round outputs and returns
// a tally sorted by vote count descending.
func parseVerdicts(outputs []DebateOutput, members []RoundtableMember, zh bool) []verdictTally {
	votes := make(map[string]int)
	for _, out := range outputs {
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			lower := strings.ToLower(trimmed)
			if strings.HasPrefix(lower, "verdict:") {
				val := strings.TrimSpace(trimmed[len("verdict:"):])
				if val != "" {
					votes[val]++
				}
				break // only first VERDICT per member
			}
		}
	}

	if len(votes) == 0 {
		return nil
	}

	var result []verdictTally
	for id, count := range votes {
		vt := verdictTally{memberID: id, votes: count}
		if m := findMember(members, id); m != nil {
			vt.avatar = m.Avatar
			vt.name = m.displayName(zh)
		} else {
			vt.name = id
		}
		result = append(result, vt)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].votes > result[j].votes
	})
	return result
}

// determineWinner returns the member with the highest average score from the
// final round's SCORE lines. On a tie for first place, the member facing fewer
// high-confidence (>=0.7) challenges in the challenge round wins; if still
// tied, the earliest in member order wins. Returns nil if no SCORE lines parse.
func determineWinner(members []RoundtableMember, rounds []DebateRound) *RoundtableMember {
	if len(rounds) < 4 {
		return nil
	}
	type avg struct {
		member RoundtableMember
		sum    float64
		count  int
	}
	var avgs []avg
	for _, m := range members {
		a := avg{member: m}
		for _, out := range rounds[3].Outputs {
			for _, line := range strings.Split(out.Content, "\n") {
				trimmed := strings.TrimSpace(line)
				lower := strings.ToLower(trimmed)
				if !strings.HasPrefix(lower, "score:") {
					continue
				}
				rest := strings.TrimSpace(trimmed[len("score:"):])
				parts := strings.SplitN(rest, "=", 2)
				if len(parts) != 2 {
					continue
				}
				if strings.TrimSpace(parts[0]) != m.ID {
					continue
				}
				s, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
				if err != nil {
					continue
				}
				a.sum += s
				a.count++
			}
		}
		if a.count > 0 {
			avgs = append(avgs, a)
		}
	}
	if len(avgs) == 0 {
		return nil
	}
	sort.SliceStable(avgs, func(i, j int) bool {
		ai := avgs[i].sum / float64(avgs[i].count)
		aj := avgs[j].sum / float64(avgs[j].count)
		if ai != aj {
			return ai > aj
		}
		// 平均分并列：被高置信挑战更少者胜
		return countHighConfidenceTargeting(avgs[i].member.ID, rounds) <
			countHighConfidenceTargeting(avgs[j].member.ID, rounds)
	})
	return &avgs[0].member
}

// countHighConfidenceTargeting returns how many high-confidence (>=0.7)
// challenges in the challenge round (index 1) target the given member.
func countHighConfidenceTargeting(memberID string, rounds []DebateRound) int {
	if len(rounds) < 2 {
		return 0
	}
	n := 0
	for _, out := range rounds[1].Outputs {
		for _, target := range out.Targets {
			if target != memberID {
				continue
			}
			for _, block := range splitChallengeBlocks(out.Content) {
				if extractConfidence(block) >= 0.7 {
					n++
				}
			}
		}
	}
	return n
}

// buildVerdictPrompt generates the verdict prompt shown to the user after the
// debate: declares the most-accepted proposal (highest average score), shows
// the score overview table, and renders the detailed implementation blueprint.
// When the blueprint is empty (LLM call failed) it falls back to the concise
// synthesis; when both are empty it falls back to verbose member viewpoints +
// high-confidence challenges.
func (h *RoundtableHall) buildVerdictPrompt(goal string, members []RoundtableMember, zh bool, synthesis string) *EngineResponse {
	var sb strings.Builder

	state := h.engine.state
	rounds := state.Roundtable.DebateRounds

	// Header
	sb.WriteString(pickPrompt(zh,
		"## Debate Complete - Most Accepted Proposal\n\n",
		"## 辩论完成 - 最被大家接受的方案\n\n",
	))
	sb.WriteString(pickPrompt(zh,
		fmt.Sprintf("**Goal**: %s\n\n", goal),
		fmt.Sprintf("**需求**: %s\n\n", goal),
	))

	// ── 胜者区块 ──
	winner := findMember(members, state.Roundtable.WinnerID)
	if winner != nil {
		sb.WriteString(fmt.Sprintf("### 🏆 %s %s", winner.Avatar, winner.displayName(zh)))
		if avg := winnerAvgScore(winner.ID, rounds, members); avg >= 0 {
			sb.WriteString(fmt.Sprintf("（平均分 %.1f）", avg))
		}
		sb.WriteString("\n\n")
	}

	// ── 评分总览 ──
	if len(rounds) >= 4 {
		table := buildScoreTable(rounds[3].Outputs, members, zh)
		if table != "" {
			sb.WriteString(pickPrompt(zh, "### Score Overview\n\n", "### 评分总览\n\n"))
			sb.WriteString(table)
			sb.WriteString("\n")
		}
	}

	// ── 蓝图（优先）→ synthesis → 观点 fallback ──
	switch {
	case state.Roundtable.Blueprint != "":
		sb.WriteString(pickPrompt(zh, "### Implementation Blueprint\n\n", "### 实施蓝图\n\n"))
		sb.WriteString(state.Roundtable.Blueprint)
		sb.WriteString("\n\n")
	case synthesis != "":
		sb.WriteString(pickPrompt(zh, "### Debate Summary\n\n", "### 辩论摘要\n\n"))
		sb.WriteString(synthesis)
		sb.WriteString("\n\n")
	default:
		if len(rounds) > 0 {
			sb.WriteString(pickPrompt(zh, "### Member Viewpoints\n\n", "### 各角色观点\n\n"))
			for _, out := range rounds[0].Outputs {
				m := findMember(members, out.MemberID)
				avatar := ""
				name := out.MemberID
				stance := ""
				if m != nil {
					avatar = m.Avatar
					name = m.displayName(zh)
					stance = m.displayStance(zh)
				}
				sb.WriteString(fmt.Sprintf("#### %s %s\n", avatar, name))
				if stance != "" {
					sb.WriteString(fmt.Sprintf("*%s*\n\n", stance))
				}
				viewpoint := ""
				if len(rounds) >= 4 {
					finalOut := getMemberOutput(out.MemberID, rounds[3].Outputs)
					if finalOut != "" {
						viewpoint = extractFinalPosition(finalOut)
					}
				}
				if viewpoint == "" {
					viewpoint = out.Content
				}
				sb.WriteString(viewpoint)
				sb.WriteString("\n\n")
			}
		}
		challenges := extractHighConfidenceChallenges(rounds, members, zh)
		if len(challenges) > 0 {
			sb.WriteString(pickPrompt(zh, "### High-Confidence Challenges\n\n", "### 高置信度挑战\n\n"))
			for _, c := range challenges {
				sb.WriteString(fmt.Sprintf("> **%s %s** %s\n\n%s\n\n",
					c.challengerAvatar, c.challengerName,
					pickPrompt(zh,
						fmt.Sprintf("(confidence %.0f%%)", c.confidence*100),
						fmt.Sprintf("(置信度 %.0f%%)", c.confidence*100),
					),
					c.content))
			}
		}
	}

	// ── Footer: Verdict instructions ──
	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"**Your verdict**: Type `support` to execute this blueprint, `but <condition>` to adjust, or `debate again`\n",
		"**你的裁决**: 输入 `支持` 执行此蓝图、`但要<条件>` 调整、或 `再辩一轮`\n",
	))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}

// buildScoreTable parses SCORE lines from final round outputs and renders a
// markdown table: rows = proposals (sorted by average descending), columns =
// scorers, with an average column. The top-scoring row is marked with ★.
func buildScoreTable(outputs []DebateOutput, members []RoundtableMember, zh bool) string {
	type scoreKey struct{ scorer, scored string }
	scores := make(map[scoreKey]float64)
	hasAny := false

	for _, out := range outputs {
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			lower := strings.ToLower(trimmed)
			if !strings.HasPrefix(lower, "score:") {
				continue
			}
			rest := strings.TrimSpace(trimmed[len("score:"):])
			parts := strings.SplitN(rest, "=", 2)
			if len(parts) != 2 {
				continue
			}
			scoredID := strings.TrimSpace(parts[0])
			score, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if err != nil {
				continue
			}
			scores[scoreKey{out.MemberID, scoredID}] = score
			hasAny = true
		}
	}
	if !hasAny {
		return ""
	}

	// Compute averages and sort by average descending.
	type memberScore struct {
		member RoundtableMember
		sum    float64
		count  int
	}
	var rankings []memberScore
	for _, scored := range members {
		var sum float64
		var count int
		for _, scorer := range members {
			if s, ok := scores[scoreKey{scorer.ID, scored.ID}]; ok {
				sum += s
				count++
			}
		}
		rankings = append(rankings, memberScore{member: scored, sum: sum, count: count})
	}
	sort.SliceStable(rankings, func(i, j int) bool {
		ai, aj := 0.0, 0.0
		if rankings[i].count > 0 {
			ai = rankings[i].sum / float64(rankings[i].count)
		}
		if rankings[j].count > 0 {
			aj = rankings[j].sum / float64(rankings[j].count)
		}
		return ai > aj
	})

	var sb strings.Builder

	// Header row
	sb.WriteString("| ")
	sb.WriteString(pickPrompt(zh, "Proposal", "方案"))
	sb.WriteString(" |")
	for _, m := range members {
		sb.WriteString(fmt.Sprintf(" %s |", m.displayName(zh)))
	}
	sb.WriteString(pickPrompt(zh, " Avg |\n", " 平均 |\n"))

	// Separator
	sb.WriteString("|---|")
	for range members {
		sb.WriteString("---|")
	}
	sb.WriteString("---|\n")

	// Data rows: sorted by average descending; top row gets ★.
	for idx, rs := range rankings {
		label := fmt.Sprintf("%s%s", rs.member.Avatar, rs.member.displayName(zh))
		if idx == 0 && rs.count > 0 {
			label = "★ " + label
		}
		sb.WriteString(fmt.Sprintf("| %s |", label))
		for _, scorer := range members {
			if s, ok := scores[scoreKey{scorer.ID, rs.member.ID}]; ok {
				sb.WriteString(fmt.Sprintf(" %.0f |", s))
			} else {
				sb.WriteString(" - |")
			}
		}
		if rs.count > 0 {
			sb.WriteString(fmt.Sprintf(" %.1f |", rs.sum/float64(rs.count)))
		} else {
			sb.WriteString(" - |")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// winnerAvgScore returns the winner's average score across all members' final
// round SCORE lines, or -1 if none parse.
func winnerAvgScore(memberID string, rounds []DebateRound, members []RoundtableMember) float64 {
	if len(rounds) < 4 {
		return -1
	}
	var sum, count float64
	for _, out := range rounds[3].Outputs {
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			lower := strings.ToLower(trimmed)
			if !strings.HasPrefix(lower, "score:") {
				continue
			}
			rest := strings.TrimSpace(trimmed[len("score:"):])
			parts := strings.SplitN(rest, "=", 2)
			if len(parts) != 2 {
				continue
			}
			if strings.TrimSpace(parts[0]) != memberID {
				continue
			}
			s, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if err != nil {
				continue
			}
			sum += s
			count++
		}
	}
	if count == 0 {
		return -1
	}
	return sum / count
}

// challengeBlock represents a single high-confidence challenge from the challenge round.
type challengeBlock struct {
	challengerAvatar string
	challengerName   string
	confidence       float64
	content          string
}

// extractHighConfidenceChallenges parses challenge round (round index 1) outputs
// for blocks containing CONFIDENCE: markers, returning only those at or above 0.7.
func extractHighConfidenceChallenges(rounds []DebateRound, members []RoundtableMember, zh bool) []challengeBlock {
	if len(rounds) < 2 {
		return nil
	}

	const threshold = 0.7
	var result []challengeBlock

	for _, out := range rounds[1].Outputs {
		challenger := findMember(members, out.MemberID)
		avatar := ""
		name := out.MemberID
		if challenger != nil {
			avatar = challenger.Avatar
			name = challenger.displayName(zh)
		}

		for _, block := range splitChallengeBlocks(out.Content) {
			conf := extractConfidence(block)
			if conf < threshold {
				continue
			}
			content := strings.TrimSpace(removeConfidenceLine(block))
			if content == "" {
				continue
			}
			result = append(result, challengeBlock{
				challengerAvatar: avatar,
				challengerName:   name,
				confidence:       conf,
				content:          content,
			})
		}
	}
	return result
}

// splitChallengeBlocks splits challenge content into blocks delimited by
// markdown headers (## or ###). Text before the first header forms the first block.
func splitChallengeBlocks(content string) []string {
	lines := strings.Split(content, "\n")
	var blocks []string
	var current strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isHeader := strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ")
		if isHeader && current.Len() > 0 {
			blocks = append(blocks, current.String())
			current.Reset()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}
	return blocks
}

// extractConfidence finds and parses a CONFIDENCE: marker in the text.
// Returns 0 if no marker is found.
func extractConfidence(text string) float64 {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "confidence:") {
			valStr := strings.TrimSpace(trimmed[len("confidence:"):])
			val, err := strconv.ParseFloat(valStr, 64)
			if err == nil {
				return val
			}
		}
	}
	return 0
}

// removeConfidenceLine removes all lines containing CONFIDENCE: markers.
func removeConfidenceLine(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "confidence:") {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// getMemberOutput returns the content of a specific member's output from a
// slice of DebateOutputs. Returns empty string if not found.
func getMemberOutput(memberID string, outputs []DebateOutput) string {
	for _, out := range outputs {
		if out.MemberID == memberID {
			return out.Content
		}
	}
	return ""
}

// extractFinalPosition extracts the final position text from a final round
// output, which is everything before the first SCORE: line.
func extractFinalPosition(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "score:") {
			break
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

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

	// User picks a proposal or provides their own.
	// Include the full debate record so the execution agent sees the actual
	// proposals, challenges, and final positions - not just the verdict label.
	var record string
	if len(state.Roundtable.DebateRounds) > 0 {
		record = formatDebateRecord(state.Roundtable.DebateRounds, state.Roundtable.Members, zh)
	}
	pinned := fmt.Sprintf("[TEAM PLAN: %s]\n\n%s\n\n%s\n%s",
		state.Roundtable.Goal,
		userMsg,
		pickPrompt(zh,
			"## Debate Record (execute the chosen proposal - do NOT re-explore from scratch)",
			"## 辩论记录（请按选定方案执行，无需从零探索）"),
		record)
	h.engine.pendingPinnedMessages = append(h.engine.pendingPinnedMessages, pinned)
	state.Roundtable.Phase = RoundtableDone

	// Mark that the next Run() should skip confirmation gates - the user
	// already approved the plan through the debate process.
	h.engine.teamVerdictPending = true

	// Persist the verdict as a Decision so it survives in Block B across
	// all execution turns, not just the first turn's pinned message.
	state.Decisions = append(state.Decisions, Decision{
		ID:   "team-verdict",
		Text: userMsg,
	})

	return &EngineResponse{
		Summary: pickPrompt(zh,
			fmt.Sprintf("✓ Verdict recorded. Proceeding with: %s", userMsg),
			fmt.Sprintf("✓ 裁决已记录。将按以下方向执行: %s", userMsg),
		),
		Stage: StageAct,
	}
}

// findMember returns the member with the given ID, or nil.
func findMember(members []RoundtableMember, id string) *RoundtableMember {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}
