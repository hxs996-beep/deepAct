package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
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
// Structure: score overview (总) -> member viewpoints (分) -> high-confidence challenges (conditional) -> verdict instructions.
func (h *RoundtableHall) buildVerdictPrompt(goal string, members []RoundtableMember, zh bool) *EngineResponse {
	var sb strings.Builder

	// Header
	sb.WriteString(pickPrompt(zh,
		"## 🤝 Debate Complete - Your Verdict\n\n",
		"## 🤝 辩论完成 - 请裁决\n\n",
	))
	sb.WriteString(pickPrompt(zh,
		fmt.Sprintf("**Goal**: %s\n\n", goal),
		fmt.Sprintf("**需求**: %s\n\n", goal),
	))

	state := h.engine.state
	rounds := state.Roundtable.DebateRounds

	// ── 总: Score overview table ──
	if len(rounds) >= 4 {
		table := buildScoreTable(rounds[3].Outputs, members, zh)
		if table != "" {
			sb.WriteString(pickPrompt(zh, "### 📊 Score Overview\n\n", "### 📊 评分总览\n\n"))
			sb.WriteString(table)
			sb.WriteString("\n")
		}
	}

	// ── 分: Each member's viewpoint ──
	if len(rounds) > 0 {
		sb.WriteString(pickPrompt(zh, "### 📋 Member Viewpoints\n\n", "### 📋 各角色观点\n\n"))

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

			// Prefer final position (round 3); fall back to original proposal (round 0)
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

	// ── Conditional: High-confidence challenges ──
	challenges := extractHighConfidenceChallenges(rounds, members, zh)
	if len(challenges) > 0 {
		sb.WriteString(pickPrompt(zh, "### ⚡ High-Confidence Challenges\n\n", "### ⚡ 高置信度挑战\n\n"))
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

	// ── Footer: Verdict instructions ──
	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"**Your verdict**: Type `support <role>`, `<role> but <condition>`, `none, should <your approach>`, or `debate again`\n",
		"**你的裁决**: 输入 `支持方案<角色名>`、`方案<角色名>但要<条件>`、`都不行，应该<你的方案>`、或 `再辩一轮`\n",
	))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}

// buildScoreTable parses SCORE lines from final round outputs and renders a
// markdown table: rows = proposals, columns = scorers, with an average column.
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

	// Data rows: one per proposal (scored member)
	for _, scored := range members {
		sb.WriteString(fmt.Sprintf("| %s%s |", scored.Avatar, scored.displayName(zh)))
		var sum float64
		var count int
		for _, scorer := range members {
			if s, ok := scores[scoreKey{scorer.ID, scored.ID}]; ok {
				sb.WriteString(fmt.Sprintf(" %.0f |", s))
				sum += s
				count++
			} else {
				sb.WriteString(" - |")
			}
		}
		if count > 0 {
			sb.WriteString(fmt.Sprintf(" %.1f |", sum/float64(count)))
		} else {
			sb.WriteString(" - |")
		}
		sb.WriteString("\n")
	}

	return sb.String()
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


