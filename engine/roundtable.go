package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

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
	Prompt   string `json:"prompt"`               // Chinese system-level instruction injected as extraPrompt
	PromptEn string `json:"prompt_en,omitempty"`   // English variant
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

	return h.buildVerdictPrompt(goal, members, zh), nil
}

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
			"\n## Output\nFor each proposal you challenge, clearly state: which proposal, what the problem is, why it matters. Be specific — reference code or architectural facts when possible.",
			"\n## 输出\n对你挑战的每个方案，清晰说明：哪个方案、什么问题、为什么重要。尽量具体——引用代码或架构事实。",
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
			"## Task\nReview the complete debate record. State your final position, and score every proposal (including your own) on a 0-100 scale.\n\n## Requirement\n%s\n\n## Complete Debate Record\n%s\n\n## Output Format\n1. Your final position summary\n2. Score each proposal:\n   SCORE: <member_id> = <0-100>\n   REASON: <one-line reason>\nEnd with: VERDICT: <your preferred proposal member_id>",
			"## 任务\n审阅完整辩论记录。陈述你的最终立场，给每个方案（含自己的）打分（0-100）。\n\n## 需求\n%s\n\n## 完整辩论记录\n%s\n\n## 输出格式\n1. 你的最终立场总结\n2. 给每个方案打分:\n   SCORE: <member_id> = <0-100>\n   REASON: <一句话理由>\n以: VERDICT: <你支持的方案 member_id> 结尾",
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

			if len(rounds) >= 4 {
				sb.WriteString(pickPrompt(zh, "**Scores**: ", "**评分**: "))
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
		for _, line := range strings.Split(out.Content, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToLower(trimmed), "score:") {
				parts = append(parts, fmt.Sprintf("%s%s: %s", avatar, name, strings.TrimSpace(trimmed[len("score:"):])))
			}
		}
	}
	return strings.Join(parts, " | ")
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

// findMember returns the member with the given ID, or nil.
func findMember(members []RoundtableMember, id string) *RoundtableMember {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}

// truncateString truncates s to maxLen runes, appending "..." if truncated.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
