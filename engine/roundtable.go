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
	RoundtableIdle    RoundtablePhase = iota
	RoundtableExplore                 // brainstorming proposals
	RoundtableReview                  // parallel multi-stance review
	RoundtableDone                    // finished, awaiting normal flow
)

func (p RoundtablePhase) String() string {
	switch p {
	case RoundtableExplore:
		return "explore"
	case RoundtableReview:
		return "review"
	case RoundtableDone:
		return "done"
	default:
		return "idle"
	}
}

// RoundtableState tracks the current roundtable session within TaskState.
type RoundtableState struct {
	Goal       string             `json:"goal"`
	Proposals  []string           `json:"proposals"`
	ChosenPlan string             `json:"chosen_plan,omitempty"`
	Phase      RoundtablePhase    `json:"phase"`
	Members    []RoundtableMember `json:"members,omitempty"`
	Reviews    []MemberReview     `json:"reviews,omitempty"`
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
	MemberID string        `json:"member_id"`
	Verdict  ReviewVerdict `json:"verdict"`
	Score    int           `json:"score"` // 0-100
	Findings []Finding     `json:"findings,omitempty"`
	Summary  string        `json:"summary"`
	Elapsed  string        `json:"elapsed,omitempty"` // human-readable duration
	Error    string        `json:"error,omitempty"`   // non-empty if agent failed
}

// ReviewVerdict is the member's overall assessment.
type ReviewVerdict string

const (
	VerdictApprove     ReviewVerdict = "approve"
	VerdictConditional ReviewVerdict = "conditional"
	VerdictReject      ReviewVerdict = "reject"
)

// Finding is a single issue discovered by a reviewer.
type Finding struct {
	Severity   string `json:"severity"`   // critical / high / medium / low
	Category   string `json:"category"`   // security / design / performance / correctness
	Content    string `json:"content"`    // what the problem is
	Suggestion string `json:"suggestion"` // how to fix it
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
	case RoundtableExplore:
		return h.handleExplore(ctx, userMsg, zh)
	case RoundtableReview:
		return h.handleReview(ctx, userMsg, zh)
	case RoundtableDone:
		// Don't clear state here — Engine.Run() handles cleanup AFTER the
		// normal agent loop runs, so Block B can include roundtable results.
		return nil, nil
	}
	return nil, nil
}

// handleExplore runs the brainstorm phase: generates 2-3 proposals via
// a sub-agent and presents them to the user for selection.
func (h *RoundtableHall) handleExplore(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	goal := state.Roundtable.Goal

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "explore",
			Detail: "探索方案中...",
		})
	}

	// Use the brainstorm sub-agent to generate proposals.
	agent, err := h.engine.agents.Get(AgentBrainstorm)
	if err != nil {
		// Fallback: use generic sub-agent if brainstorm not found
		agent, err = h.engine.agents.Get(AgentSub)
		if err != nil {
			return nil, fmt.Errorf("no agent available for explore: %w", err)
		}
	}

	explorePrompt := fmt.Sprintf(`分析以下需求，提出 2-3 个不同的实现方案。

需求：%s

对每个方案：
1. 简要描述方案思路
2. 列出涉及的主要文件和改动
3. 指出方案的优缺点

格式要求：
- 每个方案用 "## 方案 N: 标题" 开头
- 保持简洁，每个方案 3-5 句话`, goal)

	handoff := Handoff{
		Agent: AgentBrainstorm,
		Goal:  explorePrompt,
		Tools: []string{"read", "grep", "glob", "lsp"},
		Depth: 0,
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil {
		return nil, fmt.Errorf("explore agent: %w", err)
	}

	proposals := extractProposals(result.Summary)
	if len(proposals) == 0 {
		// If no structured proposals found, use the entire summary as one proposal
		proposals = []string{result.Summary}
	}
	state.Roundtable.Proposals = proposals
	state.Roundtable.Phase = RoundtableReview

	// Build user-facing response
	var sb strings.Builder
	if zh {
		sb.WriteString(fmt.Sprintf("## 🎯 需求分析：%s\n\n", goal))
		sb.WriteString("以下是几种可行的方案，请选择你倾向的方向：\n\n")
		for i, p := range proposals {
			sb.WriteString(fmt.Sprintf("---\n**方案 %d**\n\n%s\n\n", i+1, p))
		}
		sb.WriteString("\n💡 请选择你倾向的方案（例如：\"方案2\"），或者提出你的想法。")
	} else {
		sb.WriteString(fmt.Sprintf("## 🎯 Goal: %s\n\n", goal))
		sb.WriteString("Here are a few approaches. Pick the direction you prefer:\n\n")
		for i, p := range proposals {
			sb.WriteString(fmt.Sprintf("---\n**Approach %d**\n\n%s\n\n", i+1, p))
		}
		sb.WriteString("\n💡 Pick an approach (e.g. \"Approach 2\") or share your thoughts.")
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "explore_done",
			Detail: fmt.Sprintf("已提出 %d 个方案", len(proposals)),
		})
	}

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}, nil
}

// handleReview runs all member agents in parallel to review the chosen plan,
// then presents a structured summary.
func (h *RoundtableHall) handleReview(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state

	// Use default members if none configured (e.g., via skill)
	members := state.Roundtable.Members
	if len(members) == 0 {
		members = DefaultRoundtableMembers
	}

	// The user's message is treated as their chosen direction / context
	chosenPlan := userMsg
	if chosenPlan == "" {
		chosenPlan = state.Roundtable.Goal
	}
	state.Roundtable.ChosenPlan = chosenPlan

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "review",
			Detail: fmt.Sprintf("发起 %d 位成员并行评审...", len(members)),
		})
	}

	// Run all member reviews in parallel using goroutines + WaitGroup
	var mu sync.Mutex
	reviews := make([]MemberReview, len(members))
	var wg sync.WaitGroup

	for i, member := range members {
		wg.Add(1)
		go func(idx int, m RoundtableMember) {
			defer wg.Done()
			review := h.runMemberReview(ctx, m, state.Roundtable.Goal, chosenPlan)
			mu.Lock()
			reviews[idx] = review
			mu.Unlock()
		}(i, member)
	}
	wg.Wait()

	state.Roundtable.Reviews = reviews

	// Present summary
	var sb strings.Builder
	if zh {
		sb.WriteString("## 📋 多立场评审汇总\n\n")
		sb.WriteString(fmt.Sprintf("**需求**: %s\n\n", state.Roundtable.Goal))
		if chosenPlan != state.Roundtable.Goal {
			sb.WriteString(fmt.Sprintf("**选定的方向**: %s\n\n", chosenPlan))
		}
	} else {
		sb.WriteString("## 📋 Multi-Stance Review Summary\n\n")
		sb.WriteString(fmt.Sprintf("**Goal**: %s\n\n", state.Roundtable.Goal))
		if chosenPlan != state.Roundtable.Goal {
			sb.WriteString(fmt.Sprintf("**Chosen direction**: %s\n\n", chosenPlan))
		}
	}

	// Conflict detection: find disagreements between members
	conflicts := detectConflicts(reviews, members, zh)

	for _, review := range reviews {
		member := findMember(members, review.MemberID)
		avatar := ""
		name := review.MemberID
		if member != nil {
			avatar = member.Avatar + " "
			name = member.Name
		}

		verdictIcon := "✅"
		switch review.Verdict {
		case VerdictConditional:
			verdictIcon = "⚠️"
		case VerdictReject:
			verdictIcon = "❌"
		}

		sb.WriteString(fmt.Sprintf("### %s%s  %s (评分: %d/100)\n", avatar, name, verdictIcon, review.Score))
		if review.Elapsed != "" {
			sb.WriteString(fmt.Sprintf("⏱ %s\n", review.Elapsed))
		}
		if review.Error != "" {
			sb.WriteString(fmt.Sprintf("⚠️ 评审出错: %s\n", review.Error))
		} else {
			sb.WriteString(fmt.Sprintf("**结论**: %s\n\n", review.Summary))
			if len(review.Findings) > 0 {
				for _, f := range review.Findings {
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

	// Conflicts section — prominently highlighted
	if len(conflicts) > 0 {
		if zh {
			sb.WriteString("---\n## ⚡⚡⚡ 立场冲突\n\n")
			sb.WriteString("以下成员之间存在意见分歧，需要你决策：\n\n")
		} else {
			sb.WriteString("---\n## ⚡⚡⚡ Stance Conflicts\n\n")
			sb.WriteString("The following disagreements require your decision:\n\n")
		}
		for _, c := range conflicts {
			sb.WriteString(c)
			sb.WriteString("\n")
		}
	}

	// Mark roundtable as done
	state.Roundtable.Phase = RoundtableDone

	if zh {
		sb.WriteString("\n💡 评审完成。你可以根据以上意见继续讨论，或者直接提出实现需求。")
	} else {
		sb.WriteString("\n💡 Review complete. You can discuss the feedback or request implementation.")
	}

	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "roundtable_phase",
			Name:   "review_done",
			Detail: fmt.Sprintf("%d 位成员评审完成", len(reviews)),
		})
	}

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}, nil
}

// runMemberReview executes a single roundtable member's review as a sub-agent.
// It emits member_start / member_done progress events so the UI can show
// each member's status without displaying the LLM's raw thinking.
func (h *RoundtableHall) runMemberReview(ctx context.Context, member RoundtableMember, goal, chosenPlan string) MemberReview {
	start := time.Now()

	// Emit member start progress
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "member_start",
			Name:   member.ID,
			Detail: member.Name,
		})
	}

	// Build the review goal: the plan to review + what stance to take
	reviewGoal := fmt.Sprintf(`请以「%s」的立场评审以下方案。

## 你的立场
%s

## 待评审的需求
%s

## 选定的方向
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
		member.Name, member.Stance, goal, chosenPlan)

	// Use the generic sub-agent with the member's role prompt injected
	// via RunWithPrompt. The member's Prompt serves as the system-level
	// instruction (extraPrompt), shaping the agent's persona.
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          reviewGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 5,
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return h.memberError(member.ID, fmt.Sprintf("评审失败: %v", err), err, start)
	}

	// Type-assert to get the SubAgentRunner for RunWithPrompt
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	pr, ok := agent.(promptRunner)
	if !ok {
		// Fallback: use regular Run without prompt injection
		result, err := agent.Run(ctx, handoff)
		review := parseMemberReview(member.ID, result, err, start)
		h.emitMemberDone(member, review)
		return review
	}

	result, err := pr.RunWithPrompt(ctx, handoff, member.Prompt)
	review := parseMemberReview(member.ID, result, err, start)
	h.emitMemberDone(member, review)
	return review
}

// memberError creates a MemberReview for a failed review and emits completion.
func (h *RoundtableHall) memberError(memberID string, summary string, err error, start time.Time) MemberReview {
	review := MemberReview{
		MemberID: memberID,
		Verdict:  VerdictConditional,
		Score:    0,
		Summary:  summary,
		Error:    err.Error(),
		Elapsed:  time.Since(start).Round(time.Millisecond).String(),
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
func parseMemberReview(memberID string, result *HandoffResult, err error, start time.Time) MemberReview {
	elapsed := time.Since(start).Round(time.Millisecond).String()
	if err != nil {
		return MemberReview{
			MemberID: memberID,
			Verdict:  VerdictConditional,
			Score:    0,
			Summary:  fmt.Sprintf("评审出错: %v", err),
			Error:    err.Error(),
			Elapsed:  elapsed,
		}
	}
	if result == nil {
		return MemberReview{
			MemberID: memberID,
			Verdict:  VerdictConditional,
			Score:    0,
			Summary:  "评审未返回结果",
			Elapsed:  elapsed,
		}
	}

	content := result.Summary

	// Extract structured fields from the result
	review := MemberReview{
		MemberID: memberID,
		Verdict:  parseVerdict(content),
		Score:    parseScore(content),
		Summary:  parseSummaryLine(content),
		Elapsed:  elapsed,
		Findings: parseFindings(content),
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
