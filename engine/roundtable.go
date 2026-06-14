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
	case RoundtableReview:
		return h.handleReview(ctx, userMsg, zh)
	case RoundtableDone:
		// Don't clear state here — Engine.Run() handles cleanup AFTER the
		// normal agent loop runs, so Block B can include roundtable results.
		return nil, nil
	default:
		return nil, nil
	}
	return nil, nil
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
		MaxIterations: 3,
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

	// Extract structured fields from the result
	review := MemberReview{
		MemberID:      memberID,
		ProposalIndex: proposalIndex,
		Verdict:       parseVerdict(content),
		Score:         parseScore(content),
		Summary:       parseSummaryLine(content),
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
