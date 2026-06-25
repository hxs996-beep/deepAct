package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RoundtablePhase describes which stage of the roundtable we are in.
type RoundtablePhase int

const (
	RoundtableIdle           RoundtablePhase = iota
	RoundtableExplore                        // brainstorming proposals
	RoundtableReview                         // parallel multi-stance review
	RoundtableTeamExplore                    // team brainstorm: members generate ideas in parallel
	RoundtableTeamSynthesize                 // team synthesis: merge all perspectives into a plan
	RoundtableDone                           // finished, awaiting normal flow
)

func (p RoundtablePhase) String() string {
	switch p {
	case RoundtableExplore:
		return "explore"
	case RoundtableReview:
		return "review"
	case RoundtableTeamExplore:
		return "team_explore"
	case RoundtableTeamSynthesize:
		return "team_synthesize"
	case RoundtableDone:
		return "done"
	default:
		return "idle"
	}
}

// RoundtableState tracks the current roundtable session within TaskState.
type RoundtableState struct {
	Goal      string             `json:"goal"`
	Proposals []string           `json:"proposals"`
	Phase     RoundtablePhase    `json:"phase"`
	Members   []RoundtableMember `json:"members,omitempty"`
	Reviews   []MemberReview     `json:"reviews,omitempty"`
}

// RoundtableMember defines a single reviewer's identity and stance.
type RoundtableMember struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	Stance string `json:"stance"`
	Prompt string `json:"prompt"` // system-level instruction injected as extraPrompt
}

// MemberReview is the result from one roundtable member's review.
type MemberReview struct {
	MemberID      string        `json:"member_id"`
	ProposalIndex int           `json:"proposal_index"` // which proposal this review targets
	Verdict       ReviewVerdict `json:"verdict"`
	Score         int           `json:"score"` // 0-100
	Findings      []Finding     `json:"findings,omitempty"`
	Summary       string        `json:"summary"`
	Elapsed       string        `json:"elapsed,omitempty"` // human-readable duration
	Error         string        `json:"error,omitempty"`   // non-empty if agent failed
}

// ReviewVerdict is the member's overall assessment.
type ReviewVerdict string

const (
	VerdictApprove     ReviewVerdict = "approve"
	VerdictConditional ReviewVerdict = "conditional"
	VerdictReject      ReviewVerdict = "reject"
)

// TeamThought is the output from one team member during team exploration.
type TeamThought struct {
	MemberID string `json:"member_id"`
	Content  string `json:"content"`
	Summary  string `json:"summary"` // extracted from SUMMARY: marker
}

// Finding is a single issue discovered by a reviewer.
type Finding struct {
	Severity   string `json:"severity"`   // critical / high / medium / low
	Category   string `json:"category"`   // security / design / performance / correctness
	Content    string `json:"content"`    // what the problem is
	Suggestion string `json:"suggestion"` // how to fix it
}

// RefuteOutcome is the verdict of the refute stage on a single finding.
type RefuteOutcome string

const (
	RefuteConfirmed RefuteOutcome = "confirmed" // 代码中找到具体证据,finding 真实
	RefuteRefuted   RefuteOutcome = "refuted"   // 未能找到证据,判为误报/幻觉
)

// RefuteResult captures the refute stage's verdict on one finding.
type RefuteResult struct {
	Outcome RefuteOutcome `json:"outcome"`
	Reason  string        `json:"reason,omitempty"` // 证伪/确认的依据
}

// refuteTarget pairs a finding with its origin (proposal + reviewer) so the
// refute stage can report results back by stable key.
type refuteTarget struct {
	ProposalIndex int
	MemberID      string
	Finding       Finding
}

// findingKey builds a stable key for a finding across the review->refute pipeline.
func findingKey(proposalIndex int, memberID, content string) string {
	return fmt.Sprintf("%d:%s:%s", proposalIndex, memberID, content)
}

// RoundtableCommand represents a parsed /round command.
type RoundtableCommand struct {
	Goal string
}

// parseRoundtableCommand checks if userMsg is a /round command.
func parseRoundtableCommand(userMsg string) *RoundtableCommand {
	trimmed := strings.TrimSpace(userMsg)
	if !strings.HasPrefix(trimmed, "/") {
		return nil
	}
	rest := trimmed[1:]
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "round" {
		return nil
	}
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	if args == "" {
		return nil
	}
	return &RoundtableCommand{Goal: args}
}

// RoundtableHall orchestrates the roundtable flow.
type RoundtableHall struct {
	engine *Engine
}

func NewRoundtableHall(e *Engine) *RoundtableHall {
	return &RoundtableHall{engine: e}
}

// handleTeamFlow orchestrates the full agent team workflow:
// 1. Runs all team members in parallel to generate ideas
// 2. Synthesizes all perspectives into a unified plan
// 3. Returns the team output as an EngineResponse
func (h *RoundtableHall) handleTeamFlow(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Roundtable == nil {
		return nil, nil
	}

	zh := msgIsChinese(state.Roundtable.Goal)
	goal := state.Roundtable.Goal
	members := state.Roundtable.Members
	if len(members) == 0 {
		members = DefaultRoundtableMembers
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "team_explore",
			Detail: fmt.Sprintf("团队协作：%d 位角色并行分析...", len(members)),
		})
	}

	// Step 1: Run all members in parallel
	thoughts := h.runTeamExplore(ctx, goal, members)

	// Step 2: Synthesize all perspectives into a unified plan
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "team_synthesize",
			Detail: "合并各角色视角，生成统一方案...",
		})
	}

	plan := h.synthesizeTeamOutput(ctx, goal, thoughts)

	// Step 3: Build the output report
	var sb strings.Builder
	if zh {
		sb.WriteString("## 🤝 团队协作方案\n\n")
	} else {
		sb.WriteString("## 🤝 Team Collaboration Plan\n\n")
	}

	// Show each member's contribution
	if zh {
		sb.WriteString(fmt.Sprintf("**需求**: %s\n\n", goal))
		sb.WriteString("### 各角色分析\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("**Goal**: %s\n\n", goal))
		sb.WriteString("### Member Perspectives\n\n")
	}
	for _, t := range thoughts {
		member := findMember(members, t.MemberID)
		avatar := ""
		name := t.MemberID
		if member != nil {
			avatar = member.Avatar + " "
			name = member.Name
		}
		sb.WriteString(fmt.Sprintf("%s**%s**\n\n", avatar, name))
		sb.WriteString(t.Content)
		sb.WriteString("\n\n---\n\n")
	}

	// Show the synthesized plan
	if plan != "" {
		if zh {
			sb.WriteString("### 📋 统一方案\n\n")
		} else {
			sb.WriteString("### 📋 Unified Plan\n\n")
		}
		sb.WriteString(plan)
		sb.WriteString("\n\n")
	}

	if zh {
		sb.WriteString("\n💡 团队协作完成。以上方案已注入上下文，后续交互将基于团队方案执行。")
	} else {
		sb.WriteString("\n💡 Team collaboration complete. The plan above has been injected into context for subsequent execution.")
	}

	// Step 4: Inject the synthesized plan as a pinned message for the main agent
	if plan != "" {
		pinned := fmt.Sprintf("[TEAM PLAN: %s]\n\n%s", goal, plan)
		h.engine.pendingPinnedMessages = append(h.engine.pendingPinnedMessages, pinned)
	} else {
		// Fallback: inject all member thoughts
		var fallback strings.Builder
		fallback.WriteString(fmt.Sprintf("[TEAM THOUGHTS: %s]\n\n", goal))
		for _, t := range thoughts {
			member := findMember(members, t.MemberID)
			name := t.MemberID
			if member != nil {
				name = member.Name
			}
			fallback.WriteString(fmt.Sprintf("### %s\n%s\n\n", name, t.Content))
		}
		h.engine.pendingPinnedMessages = append(h.engine.pendingPinnedMessages, fallback.String())
	}

	// Step 5: Mark phase as done
	state.Roundtable.Phase = RoundtableDone

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "team_done",
			Detail: fmt.Sprintf("%d 位角色协作完成", len(members)),
		})
	}

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}, nil
}

// runTeamExplore runs all team members in parallel to generate ideas from their
// respective professional perspectives. Returns a slice of TeamThought results.
func (h *RoundtableHall) runTeamExplore(ctx context.Context, goal string, members []RoundtableMember) []TeamThought {
	type task struct {
		Member RoundtableMember
		Index  int
	}
	var tasks []task
	for i, m := range members {
		tasks = append(tasks, task{Member: m, Index: i})
	}

	results := make([]TeamThought, len(tasks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, t := range tasks {
		wg.Add(1)
		go func(t task) {
			defer wg.Done()
			thought := h.runMemberThought(ctx, t.Member, goal)
			mu.Lock()
			results[t.Index] = thought
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	return results
}

// runMemberThought executes a single team member's exploration as a sub-agent.
// Each member receives a role-specific prompt to analyze the goal from their perspective.
func (h *RoundtableHall) runMemberThought(ctx context.Context, member RoundtableMember, goal string) TeamThought {
	start := time.Now()

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_start",
			Name:   member.ID,
			Detail: member.Name,
		})
	}

	taskGoal := buildTeamExploreGoal(goal, member)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          taskGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 50,
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		h.emitThoughtDone(member, "分析出错")
		return TeamThought{
			MemberID: member.ID,
			Content:  fmt.Sprintf("分析失败: %v", err),
			Summary:  "error",
		}
	}

	// Type-assert to get the SubAgentRunner for RunWithPrompt
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	pr, ok := agent.(promptRunner)
	if !ok {
		result, err := agent.Run(ctx, handoff)
		if err != nil {
			h.emitThoughtDone(member, "分析出错")
			return TeamThought{MemberID: member.ID, Content: fmt.Sprintf("分析失败: %v", err), Summary: "error"}
		}
		summary := extractTeamSummary(result.Summary)
		h.emitThoughtDone(member, summary)
		return TeamThought{
			MemberID: member.ID,
			Content:  result.Summary,
			Summary:  summary,
		}
	}

	result, err := pr.RunWithPrompt(ctx, handoff, member.Prompt)
	if err != nil {
		h.emitThoughtDone(member, "分析出错")
		return TeamThought{MemberID: member.ID, Content: fmt.Sprintf("分析失败: %v", err), Summary: "error"}
	}

	summary := extractTeamSummary(result.Summary)
	h.emitThoughtDone(member, summary)

	elapsed := time.Since(start).Round(time.Millisecond).String()
	_ = elapsed // available for logging if needed

	return TeamThought{
		MemberID: member.ID,
		Content:  result.Summary,
		Summary:  summary,
	}
}

// emitThoughtDone emits a member_done progress event for team exploration.
func (h *RoundtableHall) emitThoughtDone(member RoundtableMember, summary string) {
	if h.engine.config.OnProgress == nil {
		return
	}
	status := "✅"
	if summary == "error" || summary == "" {
		status = "❌"
	}
	h.engine.config.OnProgress(ProgressEvent{
		Type:   "member_done",
		Name:   member.ID,
		Detail: fmt.Sprintf("%s %s %s", member.Avatar, member.Name, status),
	})
}

// synthesizeTeamOutput uses the planner agent to merge all member thoughts
// into a unified, actionable implementation plan.
func (h *RoundtableHall) synthesizeTeamOutput(ctx context.Context, goal string, thoughts []TeamThought) string {
	members := h.engine.state.Roundtable.Members
	if len(members) == 0 {
		members = DefaultRoundtableMembers
	}

	// Build the synthesis context from all member thoughts
	var ctxBuilder strings.Builder
	ctxBuilder.WriteString(fmt.Sprintf("## 原始需求\n%s\n\n", goal))
	ctxBuilder.WriteString("## 各角色分析结果\n\n")
	for _, t := range thoughts {
		member := findMember(members, t.MemberID)
		name := t.MemberID
		if member != nil {
			name = member.Name
		}
		ctxBuilder.WriteString(fmt.Sprintf("### %s\n%s\n\n", name, t.Content))
	}

	handoff := Handoff{
		Agent:         AgentPlanner,
		Goal:          "基于以上各角色的分析结果，生成一个统一的、可执行的实现方案。请综合考虑所有视角，输出一个结构化的方案。",
		Context:       ctxBuilder.String(),
		Tools:         []string{"read", "grep", "glob"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 15,
	}

	agent, err := h.engine.agents.Get(AgentPlanner)
	if err != nil {
		// Fallback: concatenate all thoughts
		var sb strings.Builder
		for _, t := range thoughts {
			member := findMember(members, t.MemberID)
			name := t.MemberID
			if member != nil {
				name = member.Name
			}
			sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", name, t.Summary))
		}
		return sb.String()
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return ""
	}

	return result.Summary
}

// Advance steps through the roundtable phases given the current user message.
// Returns an EngineResponse to send back to the user, or nil if the roundtable
// is done and normal flow should resume.
func (h *RoundtableHall) Advance(ctx context.Context, userMsg string) (*EngineResponse, error) {
	state := h.engine.state
	if state.Roundtable == nil {
		return nil, nil
	}

	zh := msgIsChinese(userMsg)
	if !zh && userMsg == "" {
		zh = msgIsChinese(state.Roundtable.Goal)
	}

	switch state.Roundtable.Phase {
	case RoundtableReview:
		return h.handleReview(ctx, userMsg, zh)
	case RoundtableDone:
		// Don't clear state here — Engine.Run() handles cleanup AFTER the
		// normal agent loop runs, so Block B can include roundtable results.
		return nil, nil
	default:
		return nil, nil
	}
}

// handleReview runs all member agents in parallel to review one or all proposals,
// then presents a structured proposal×member matrix.
func (h *RoundtableHall) handleReview(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	proposals := state.Roundtable.Proposals
	if len(proposals) == 0 {
		return nil, fmt.Errorf("no proposals to review")
	}

	members := state.Roundtable.Members
	if len(members) == 0 {
		members = DefaultRoundtableMembers
	}

	// Determine which proposals to review
	indices := parseReviewTarget(userMsg, len(proposals))
	if len(indices) == 0 {
		// User didn't ask for a review — let the normal agent handle this message
		return nil, nil
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "review",
			Detail: fmt.Sprintf("评审 %d 个方案 × %d 位角色...", len(indices), len(members)),
		})
	}

	// Build review tasks: proposal × member
	type reviewTask struct {
		ProposalIdx int
		Member      RoundtableMember
	}
	var tasks []reviewTask
	for _, pi := range indices {
		for _, m := range members {
			tasks = append(tasks, reviewTask{ProposalIdx: pi, Member: m})
		}
	}

	// Run all in parallel
	var mu sync.Mutex
	reviews := make([]MemberReview, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t reviewTask) {
			defer wg.Done()
			review := h.runMemberReview(ctx, t.Member, state.Roundtable.Goal, proposals[t.ProposalIdx], t.ProposalIdx)
			mu.Lock()
			reviews[idx] = review
			mu.Unlock()
		}(i, task)
	}
	wg.Wait()

	// Refute stage: 用反向偏见 sub-agent 逐条证伪,剔除假阳性 finding。
	// 失败/解析不出时默认 confirmed(降级保留,不丢真问题)。
	refutedCount := 0
	if targets := collectAllFindings(reviews); len(targets) > 0 {
		refuteMap := h.refuteFindings(ctx, state.Roundtable.Goal, proposals, targets)
		for i := range reviews {
			kept := reviews[i].Findings[:0]
			for _, f := range reviews[i].Findings {
				key := findingKey(reviews[i].ProposalIndex, reviews[i].MemberID, f.Content)
				if r, ok := refuteMap[key]; ok && r.Outcome == RefuteRefuted {
					refutedCount++
					continue
				}
				kept = append(kept, f)
			}
			reviews[i].Findings = kept
		}
	}

	state.Roundtable.Reviews = reviews
	state.Roundtable.Phase = RoundtableDone

	// Build output: organized by proposal
	var sb strings.Builder
	if zh {
		sb.WriteString("## 📋 多角色评审汇总\n\n")
		sb.WriteString(fmt.Sprintf("**需求**: %s\n\n", state.Roundtable.Goal))
	} else {
		sb.WriteString("## 📋 Multi-Stance Review Summary\n\n")
		sb.WriteString(fmt.Sprintf("**Goal**: %s\n\n", state.Roundtable.Goal))
	}

	for _, pi := range indices {
		proposalText := proposals[pi]
		firstLine := strings.SplitN(proposalText, "\n", 2)[0]
		if zh {
			sb.WriteString(fmt.Sprintf("### 方案%d: %s\n\n", pi+1, firstLine))
		} else {
			sb.WriteString(fmt.Sprintf("### Approach %d: %s\n\n", pi+1, firstLine))
		}
		sb.WriteString(proposalText)
		sb.WriteString("\n\n")

		for _, r := range reviews {
			if r.ProposalIndex != pi {
				continue
			}
			member := findMember(members, r.MemberID)
			avatar := ""
			name := r.MemberID
			if member != nil {
				avatar = member.Avatar + " "
				name = member.Name
			}

			verdictIcon := "✅"
			switch r.Verdict {
			case VerdictConditional:
				verdictIcon = "⚠️"
			case VerdictReject:
				verdictIcon = "❌"
			}

			sb.WriteString(fmt.Sprintf("%s%s  %s (评分: %d/100)\n", avatar, name, verdictIcon, r.Score))
			if r.Elapsed != "" {
				sb.WriteString(fmt.Sprintf("⏱ %s\n", r.Elapsed))
			}
			if r.Error != "" {
				sb.WriteString(fmt.Sprintf("⚠️ 评审出错: %s\n", r.Error))
			} else {
				sb.WriteString(fmt.Sprintf("**结论**: %s\n\n", r.Summary))
				if len(r.Findings) > 0 {
					for _, f := range r.Findings {
						sevIcon := "🔴"
						switch f.Severity {
						case "high":
							sevIcon = "🟠"
						case "medium":
							sevIcon = "🟡"
						case "low":
							sevIcon = "🔵"
						}
						sb.WriteString(fmt.Sprintf("- %s [%s/%s] %s", sevIcon, f.Severity, f.Category, f.Content))
						if f.Suggestion != "" {
							sb.WriteString(fmt.Sprintf("\n  → 建议: %s", f.Suggestion))
						}
						sb.WriteString("\n")
					}
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString("---\n\n")
	}

	// Conflict detection
	conflicts := detectConflicts(reviews, members, zh)
	if len(conflicts) > 0 {
		if zh {
			sb.WriteString("## ⚡ 需要关注的问题\n\n")
		} else {
			sb.WriteString("## ⚡ Items to Note\n\n")
		}
		for _, c := range conflicts {
			sb.WriteString(c)
			sb.WriteString("\n")
		}
	}

	// Refute stage transparency: report how many false-positive findings were filtered.
	if refutedCount > 0 {
		if zh {
			sb.WriteString(fmt.Sprintf("\n🛡 证伪检验:已剔除 %d 条疑似误报 finding(经独立 sub-agent 复核代码后判定无据)\n", refutedCount))
		} else {
			sb.WriteString(fmt.Sprintf("\n🛡 Refute check: filtered %d likely false-positive findings (independently verified against the code)\n", refutedCount))
		}
	}

	// Check if any member timed out
	hasTimedOut := false
	timedOutMembers := ""
	for _, r := range reviews {
		if r.Error != "" && strings.Contains(r.Summary, "超时") {
			hasTimedOut = true
			if m := findMember(members, r.MemberID); m != nil {
				if timedOutMembers != "" {
					timedOutMembers += "、"
				}
				timedOutMembers += m.Name
			}
		}
	}

	if hasTimedOut {
		if zh {
			sb.WriteString(fmt.Sprintf("\n⚠️ %s 分析超时，以上发现仅供参考。\n", timedOutMembers))
		} else {
			sb.WriteString(fmt.Sprintf("\n⚠️ %s timed out. Findings above are partial.\n", timedOutMembers))
		}
	}

	if zh {
		sb.WriteString("\n💡 评审完成。你可以根据以上意见选择方案，或继续讨论。")
	} else {
		sb.WriteString("\n💡 Review complete. You can choose a proposal based on feedback or continue discussion.")
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "review_done",
			Detail: fmt.Sprintf("%d 个方案 × %d 位角色评审完成", len(indices), len(members)),
		})
	}

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}, nil
}

// runMemberReview executes a single roundtable member's review as a sub-agent.
// It emits member_start / member_done progress events so the UI can show
// each member's status without displaying the LLM's raw thinking.
func (h *RoundtableHall) runMemberReview(ctx context.Context, member RoundtableMember, goal, proposalText string, proposalIndex int) MemberReview {
	start := time.Now()

	// Emit member start progress
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_start",
			Name:   member.ID,
			Detail: member.Name,
		})
	}

	// Build the review goal: target a specific proposal
	reviewGoal := fmt.Sprintf(`请以「%s」的立场评审以下方案。

## 你的立场
%s

## 待评审的需求
%s

## 待评审的方案
%s

## 评审要求
- 基于代码库实际情况进行评审（如果需要，请使用 read/grep/glob 工具查看相关代码）
- 指出具体问题并给出改进建议
- 每个问题标注严重程度（critical/high/medium/low）和分类（security/design/performance/correctness/other）
- 最终给出总体评分（0-100）和结论（approve/conditional/reject）
- 评分 >= 80 为 approve，60-79 为 conditional，< 60 为 reject

## 输出格式
在最后输出以下三行用于解析：
VERDICT: <approve|conditional|reject>
SCORE: <0-100>
SUMMARY: <一句话总结>`,
		member.Name, member.Stance, goal, proposalText)

	// Use the generic sub-agent with the member's role prompt injected
	// via RunWithPrompt. The member's Prompt serves as the system-level
	// instruction (extraPrompt), shaping the agent's persona.
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          reviewGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 50,
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return h.memberError(member.ID, proposalIndex, fmt.Sprintf("评审失败: %v", err), err, start)
	}

	// Type-assert to get the SubAgentRunner for RunWithPrompt
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	pr, ok := agent.(promptRunner)
	if !ok {
		// Fallback: use regular Run without prompt injection
		result, err := agent.Run(ctx, handoff)
		review := parseMemberReview(member.ID, proposalIndex, result, err, start)
		h.emitMemberDone(member, review)
		return review
	}

	result, err := pr.RunWithPrompt(ctx, handoff, member.Prompt)
	review := parseMemberReview(member.ID, proposalIndex, result, err, start)
	h.emitMemberDone(member, review)
	return review
}

// collectAllFindings flattens all findings across reviews into refute targets.
func collectAllFindings(reviews []MemberReview) []refuteTarget {
	var targets []refuteTarget
	for _, r := range reviews {
		for _, f := range r.Findings {
			targets = append(targets, refuteTarget{
				ProposalIndex: r.ProposalIndex,
				MemberID:      r.MemberID,
				Finding:       f,
			})
		}
	}
	return targets
}

// refuteFindings runs the refute stage: for each finding, an independent
// sub-agent with a refute-biased prompt tries to disprove it. Returns a map
// keyed by findingKey. On sub-agent error or unparseable output, the finding
// defaults to RefuteConfirmed (degrade-safe: never drop a real finding).
//
// Refute bias (mirrors claude-code AGENTIC_REFUTE_SYSTEM): default assumption
// is that the finding is a false positive; only concrete code evidence flips
// it to confirmed. The agent MUST read/grep the code, not judge from the
// proposal text alone.
func (h *RoundtableHall) refuteFindings(ctx context.Context, goal string, proposals []string, targets []refuteTarget) map[string]RefuteResult {
	results := make(map[string]RefuteResult, len(targets))
	if len(targets) == 0 {
		return results
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, t := range targets {
		wg.Add(1)
		go func(t refuteTarget) {
			defer wg.Done()
			key := findingKey(t.ProposalIndex, t.MemberID, t.Finding.Content)
			verdict := h.refuteOne(ctx, goal, proposals, t)
			mu.Lock()
			results[key] = verdict
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return results
}

// refuteOne drives a single refute sub-agent for one finding.
func (h *RoundtableHall) refuteOne(ctx context.Context, goal string, proposals []string, t refuteTarget) RefuteResult {
	proposalText := ""
	if t.ProposalIndex >= 0 && t.ProposalIndex < len(proposals) {
		proposalText = proposals[t.ProposalIndex]
	}
	f := t.Finding

	refuteGoal := fmt.Sprintf(`请证伪以下评审 finding。你的默认立场是：该 finding 是误报/幻觉，除非你在代码中找到具体证据证明它真实存在。

## 原始需求
%s

## 被评审的方案
%s

## 待证伪的 finding
- 内容: %s
- 严重程度: %s
- 分类: %s

## 证伪要求
- 必须使用 read/grep/glob/lsp 工具查看相关代码，不得仅凭方案文本下结论
- 只有在代码中找到具体证据（确有该问题）才判定 confirmed
- 找不到证据、或代码显示实际已处理/不适用，判定 refuted
- 不信任注释中的安全声明，以代码为准

## 输出格式
在最后输出以下两行用于解析：
VERDICT: <confirmed|refuted>
REASON: <一句话依据>`,
		goal, proposalText, f.Content, f.Severity, f.Category)

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          refuteGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 50,
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		// Degrade-safe: keep the finding.
		return RefuteResult{Outcome: RefuteConfirmed, Reason: fmt.Sprintf("refute agent unavailable: %v", err)}
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	var content string
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, "")
		if err != nil || result == nil {
			return RefuteResult{Outcome: RefuteConfirmed, Reason: "refute sub-agent error, kept by default"}
		}
		content = result.Summary
	} else {
		// Fallback: regular Run.
		result, err := agent.Run(ctx, handoff)
		if err != nil || result == nil {
			return RefuteResult{Outcome: RefuteConfirmed, Reason: "refute sub-agent error, kept by default"}
		}
		content = result.Summary
	}

	return parseRefuteResult(content)
}

// memberError creates a MemberReview for a failed review and emits completion.
func (h *RoundtableHall) memberError(memberID string, proposalIndex int, summary string, err error, start time.Time) MemberReview {
	review := MemberReview{
		MemberID:      memberID,
		ProposalIndex: proposalIndex,
		Verdict:       VerdictConditional,
		Score:         0,
		Summary:       summary,
		Error:         err.Error(),
		Elapsed:       time.Since(start).Round(time.Millisecond).String(),
	}
	// Emit member done so UI knows this member finished (even if failed)
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_done",
			Name:   memberID,
			Detail: fmt.Sprintf("%s ❌ 评审出错", summary),
		})
	}
	return review
}

// emitMemberDone emits a member_done progress event after review completes.
func (h *RoundtableHall) emitMemberDone(member RoundtableMember, review MemberReview) {
	if h.engine.config.OnProgress == nil {
		return
	}
	verdictIcon := "✅"
	switch review.Verdict {
	case VerdictConditional:
		verdictIcon = "⚠️"
	case VerdictReject:
		verdictIcon = "❌"
	}
	h.engine.config.OnProgress(ProgressEvent{
		Type:   "member_done",
		Name:   member.ID,
		Detail: fmt.Sprintf("%s %s %s (评分: %d)", member.Avatar, member.Name, verdictIcon, review.Score),
	})
}

// parseMemberReview extracts structured review data from a sub-agent result.
func parseMemberReview(memberID string, proposalIndex int, result *HandoffResult, err error, start time.Time) MemberReview {
	elapsed := time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		return MemberReview{
			MemberID:      memberID,
			ProposalIndex: proposalIndex,
			Verdict:       VerdictConditional,
			Score:         0,
			Summary:       fmt.Sprintf("评审出错: %v", err),
			Error:         err.Error(),
			Elapsed:       elapsed,
		}
	}
	if result == nil {
		return MemberReview{
			MemberID:      memberID,
			ProposalIndex: proposalIndex,
			Verdict:       VerdictConditional,
			Score:         0,
			Summary:       "评审未返回结果",
			Elapsed:       elapsed,
		}
	}

	content := result.Summary

	// Check if the sub-agent timed out (max iterations reached)
	if result.TimedOut {
		return MemberReview{
			MemberID:      memberID,
			ProposalIndex: proposalIndex,
			Verdict:       VerdictConditional,
			Score:         0,
			Summary:       "⚠️ 该角色分析超时，以下为部分发现（仅供参考）",
			Findings:      nil,
			Elapsed:       elapsed,
			Error:         content,
		}
	}

	// Extract structured fields from the result
	review := MemberReview{
		MemberID:      memberID,
		ProposalIndex: proposalIndex,
		Verdict:       parseVerdict(content),
		Score:         parseScore(content),
		Summary:       parseSummaryLine(content),
		Elapsed:       elapsed,
		Findings:      parseFindings(content),
	}

	// Fallback: use conclusions from the HandoffResult
	if len(review.Findings) == 0 && len(result.Conclusions) > 0 {
		for _, c := range result.Conclusions {
			review.Findings = append(review.Findings, Finding{
				Severity: "medium",
				Category: "other",
				Content:  c,
			})
		}
	}

	if review.Summary == "" {
		review.Summary = truncateString(content, 200)
	}

	return review
}

// --- Parsing helpers ---

func parseVerdict(content string) ReviewVerdict {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "verdict: approve") || strings.Contains(lower, "verdict:approve") {
		return VerdictApprove
	}
	if strings.Contains(lower, "verdict: conditional") || strings.Contains(lower, "verdict:conditional") ||
		strings.Contains(lower, "verdict: conditional approve") {
		return VerdictConditional
	}
	if strings.Contains(lower, "verdict: reject") || strings.Contains(lower, "verdict:reject") {
		return VerdictReject
	}
	// Fuzzy: look for approve/reject in the last 500 chars
	tail := lower
	if len(tail) > 500 {
		tail = tail[len(tail)-500:]
	}
	if strings.Contains(tail, "approve") {
		return VerdictApprove
	}
	if strings.Contains(tail, "reject") {
		return VerdictReject
	}
	if strings.Contains(tail, "conditional") {
		return VerdictConditional
	}
	return VerdictConditional // safe default
}

func parseScore(content string) int {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "score:") {
			var score int
			if _, err := fmt.Sscanf(trimmed, "SCORE: %d", &score); err == nil {
				return clampScore(score)
			}
			if _, err := fmt.Sscanf(trimmed, "score: %d", &score); err == nil {
				return clampScore(score)
			}
		}
	}
	return 50 // default middle score if unparseable
}

func clampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// parseRefuteResult extracts the refute stage's verdict from a sub-agent result.
// Format expected at the end of the content:
//
//	VERDICT: <confirmed|refuted>
//	REASON: <一句话依据>
//
// Unparseable output defaults to RefuteConfirmed — failure to refute must never
// silently drop a real finding (宁可保留假阳性,不可漏掉真问题).
func parseRefuteResult(content string) RefuteResult {
	result := RefuteResult{Outcome: RefuteConfirmed}

	lower := strings.ToLower(content)
	if strings.Contains(lower, "verdict: refuted") || strings.Contains(lower, "verdict:refuted") {
		result.Outcome = RefuteRefuted
	} else if strings.Contains(lower, "verdict: confirmed") || strings.Contains(lower, "verdict:confirmed") {
		result.Outcome = RefuteConfirmed
	} else {
		// Fuzzy: scan the last 500 chars for a bare refuted/confirmed keyword.
		tail := lower
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		if strings.Contains(tail, "refuted") {
			result.Outcome = RefuteRefuted
		} else if !strings.Contains(tail, "confirmed") {
			// No verdict signal at all -> default confirmed (degrade-safe).
			return result
		}
	}

	// Extract REASON line (case-insensitive).
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "reason:") {
			result.Reason = strings.TrimSpace(trimmed[len("reason:"):])
			break
		}
	}
	return result
}

func parseSummaryLine(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "summary:") {
			rest := strings.TrimSpace(trimmed[len("summary:"):])
			// Also handle "SUMMARY:"
			if rest == "" {
				rest = strings.TrimSpace(trimmed[len("SUMMARY:"):])
			}
			return rest
		}
	}
	return ""
}

func parseFindings(content string) []Finding {
	var findings []Finding
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Look for bullet points with findings
		if !strings.HasPrefix(trimmed, "- ") && !strings.HasPrefix(trimmed, "* ") {
			continue
		}
		// Check if it looks like a finding with severity/category markers
		// e.g. "- [high/security] some text" or "- 🔴 some text"
		text := trimmed[2:]
		if strings.Contains(text, "/") || strings.HasPrefix(text, "🔴") ||
			strings.HasPrefix(text, "🟠") || strings.HasPrefix(text, "🟡") || strings.HasPrefix(text, "🔵") {
			finding := Finding{
				Severity: "medium",
				Category: "other",
				Content:  text,
			}
			// Try to extract severity from emoji
			if strings.HasPrefix(text, "🔴") {
				finding.Severity = "critical"
				text = strings.TrimSpace(text[2:])
			} else if strings.HasPrefix(text, "🟠") {
				finding.Severity = "high"
				text = strings.TrimSpace(text[2:])
			} else if strings.HasPrefix(text, "🟡") {
				finding.Severity = "medium"
				text = strings.TrimSpace(text[2:])
			} else if strings.HasPrefix(text, "🔵") {
				finding.Severity = "low"
				text = strings.TrimSpace(text[2:])
			}
			// Try to extract [severity/category] pattern
			if idx := strings.Index(text, "]"); idx > 0 && strings.HasPrefix(text, "[") {
				tag := text[1:idx]
				parts := strings.SplitN(tag, "/", 2)
				if len(parts) == 2 {
					finding.Severity = strings.TrimSpace(parts[0])
					finding.Category = strings.TrimSpace(parts[1])
				}
				text = strings.TrimSpace(text[idx+1:])
			}
			finding.Content = text
			findings = append(findings, finding)
		}
	}
	return findings
}

// extractProposals splits brainstorm output into individual proposals.
func extractProposals(summary string) []string {
	if summary == "" {
		return nil
	}
	// Try to split by "## 方案 N:" or "## Approach N:" headers
	var proposals []string
	lines := strings.Split(summary, "\n")
	var current strings.Builder
	inProposal := false
	proposalIdx := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## 方案 ") || strings.HasPrefix(trimmed, "## Approach ") ||
			strings.HasPrefix(trimmed, "## 方案") || strings.HasPrefix(trimmed, "## Approach") {
			if inProposal && current.Len() > 0 {
				proposals = append(proposals, strings.TrimSpace(current.String()))
			}
			current.Reset()
			inProposal = true
			proposalIdx++
			current.WriteString(trimmed)
			current.WriteString("\n")
		} else if inProposal {
			current.WriteString(trimmed)
			current.WriteString("\n")
		}
	}
	if inProposal && current.Len() > 0 {
		proposals = append(proposals, strings.TrimSpace(current.String()))
	}

	return proposals
}

func findMember(members []RoundtableMember, id string) *RoundtableMember {
	for i := range members {
		if members[i].ID == id {
			return &members[i]
		}
	}
	return nil
}

// detectConflicts finds disagreements between member reviews.
// Returns a list of formatted conflict descriptions, each highlighting
// which members disagree and what the disagreement is about.
func detectConflicts(reviews []MemberReview, members []RoundtableMember, zh bool) []string {
	var conflicts []string

	// 1. Verdict conflicts: one approve, another reject
	for i, a := range reviews {
		for j, b := range reviews {
			if j <= i {
				continue
			}
			if a.Error != "" || b.Error != "" {
				continue
			}
			// Opposite verdicts: approve vs reject
			if (a.Verdict == VerdictApprove && b.Verdict == VerdictReject) ||
				(a.Verdict == VerdictReject && b.Verdict == VerdictApprove) {
				aName := memberName(members, a.MemberID)
				bName := memberName(members, b.MemberID)
				if zh {
					conflicts = append(conflicts, fmt.Sprintf(
						"🔴 **%s** ✅ 通过  vs  **%s** ❌ 拒绝\n  → 两人结论完全相反，需要你判断谁的分析更合理。",
						aName, bName))
				} else {
					conflicts = append(conflicts, fmt.Sprintf(
						"🔴 **%s** ✅ approve  vs  **%s** ❌ reject\n  → Opposite verdicts. You need to decide which analysis is more convincing.",
						aName, bName))
				}
			}
		}
	}

	// 2. Score divergence: >40 point gap
	for i, a := range reviews {
		for j, b := range reviews {
			if j <= i {
				continue
			}
			if a.Error != "" || b.Error != "" {
				continue
			}
			diff := a.Score - b.Score
			if diff < 0 {
				diff = -diff
			}
			if diff > 40 {
				aName := memberName(members, a.MemberID)
				bName := memberName(members, b.MemberID)
				if zh {
					conflicts = append(conflicts, fmt.Sprintf(
						"🟠 **%s** (%d分)  vs  **%s** (%d分)  — 评分差距 %d 分\n  → 双方对方案质量判断差异较大，需要你权衡。",
						aName, a.Score, bName, b.Score, diff))
				} else {
					conflicts = append(conflicts, fmt.Sprintf(
						"🟠 **%s** (%d)  vs  **%s** (%d)  — score gap of %d points\n  → Large quality assessment gap, requires your judgment.",
						aName, a.Score, bName, b.Score, diff))
				}
			}
		}
	}

	// 3. Critical issues: if any member found a critical finding, flag it
	var criticalFindings []string
	for _, r := range reviews {
		for _, f := range r.Findings {
			if f.Severity == "critical" {
				m := memberName(members, r.MemberID)
				criticalFindings = append(criticalFindings, fmt.Sprintf("%s: %s", m, f.Content))
			}
		}
	}
	if len(criticalFindings) > 0 {
		var cb strings.Builder
		if zh {
			cb.WriteString("🔴 **严重问题（必须处理）**\n\n")
		} else {
			cb.WriteString("🔴 **Critical Issues (must address)**\n\n")
		}
		for _, cf := range criticalFindings {
			cb.WriteString(fmt.Sprintf("- %s\n", cf))
		}
		conflicts = append(conflicts, cb.String())
	}

	return conflicts
}

func memberName(members []RoundtableMember, id string) string {
	if m := findMember(members, id); m != nil {
		return m.Name
	}
	return id
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// parseReviewTarget determines which proposal indices to review
// based on the user's message. Returns nil if no review was requested.
func parseReviewTarget(userMsg string, numProposals int) []int {
	trimmed := strings.TrimSpace(userMsg)
	lower := strings.ToLower(trimmed)

	// "都评一下" / "全部" / "review all" → all proposals
	if strings.Contains(lower, "都评") || strings.Contains(lower, "全部") ||
		strings.Contains(lower, "review all") || lower == "all" {
		indices := make([]int, numProposals)
		for i := 0; i < numProposals; i++ {
			indices[i] = i
		}
		return indices
	}

	// "评方案2" / "方案2" / "review approach 2" / "approach 2"
	var num int
	if _, err := fmt.Sscanf(trimmed, "评方案%d", &num); err == nil {
		if num >= 1 && num <= numProposals {
			return []int{num - 1}
		}
	}
	if _, err := fmt.Sscanf(trimmed, "方案%d", &num); err == nil {
		if num >= 1 && num <= numProposals {
			return []int{num - 1}
		}
	}
	if _, err := fmt.Sscanf(lower, "review approach %d", &num); err == nil {
		if num >= 1 && num <= numProposals {
			return []int{num - 1}
		}
	}
	if _, err := fmt.Sscanf(lower, "approach %d", &num); err == nil {
		if num >= 1 && num <= numProposals {
			return []int{num - 1}
		}
	}

	// Not a review command
	return nil
}

// TeamCommand represents a parsed /team command.
type TeamCommand struct {
	Goal string
}

// parseTeamCommand checks if userMsg is a /team command.
func parseTeamCommand(userMsg string) *TeamCommand {
	trimmed := strings.TrimSpace(userMsg)
	if trimmed == "" {
		return nil
	}
	// Handle leading newlines in userMsg: find the first non-empty line
	lines := strings.SplitN(trimmed, "\n", 2)
	firstLine := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(firstLine, "/") {
		return nil
	}
	rest := firstLine[1:]
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) == 0 {
		return nil
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	if cmd != "team" {
		return nil
	}
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	if args == "" {
		return nil
	}
	return &TeamCommand{Goal: args}
}

// buildTeamExploreGoal creates the task prompt for a roundtable member during team exploration.
// Each member receives the shared goal plus their unique role and stance.
func buildTeamExploreGoal(goal string, member RoundtableMember) string {
	return fmt.Sprintf(`## 任务
从你的专业角度分析以下需求，输出你的专业意见和建议。

## 你的角色
%s - %s

## 需求
%s

## 输出要求
- 分析需求的关键点和潜在风险
- 给出你的专业建议和实现思路
- 列出你认为需要特别注意的事项
- 最后用一行 SUMMARY: <总结> 概括你的核心观点`, member.Name, member.Stance, goal)
}

// extractTeamSummary extracts the SUMMARY: line from a member's output.
// Returns the summary text or empty string if not found.
func extractTeamSummary(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "summary:") {
			rest := strings.TrimSpace(trimmed[len("summary:"):])
			// Also handle "SUMMARY:"
			if rest == "" {
				rest = strings.TrimSpace(trimmed[len("SUMMARY:"):])
			}
			return rest
		}
	}
	return ""
}
