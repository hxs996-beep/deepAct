package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ConferencePhase describes which stage of the meeting we are in.
type ConferencePhase int

const (
	PhaseIdle     ConferencePhase = iota
	PhasePlanning                 // explore code + brainstorm proposals, user can rebut
	PhaseExecute                  // agent implements step-by-step
	PhaseReview                   // challenger reviews implementation against plan
	PhaseDone
	// Review sub-phases used by ScoreCard for labeling.
	PhaseAnalysisReview
	PhaseBrainstormReview
	PhaseVerificationReview
)

func (p ConferencePhase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhasePlanning:
		return "planning"
	case PhaseExecute:
		return "execute"
	case PhaseReview:
		return "review"
	case PhaseDone:
		return "done"
	case PhaseAnalysisReview:
		return "analysis_review"
	case PhaseBrainstormReview:
		return "brainstorm_review"
	case PhaseVerificationReview:
		return "verification_review"
	default:
		return "unknown"
	}
}

// ConferenceBoard accumulates outputs from each phase.
type ConferenceBoard struct {
	Goal          string          `json:"goal"`           // the original user request
	ExploreResult string          `json:"explore_result"` // explore phase: analysis + questions
	DecideOptions string          `json:"decide_options"` // options presented to user in decide phase
	Plan          string          `json:"plan"`           // chosen implementation plan
	RelatedFiles  []string        `json:"related_files"`  // files referenced in plan
	Phase         ConferencePhase `json:"phase"`
	PendingReview bool            `json:"pending_review"` // true when waiting for user review after execute
}

// ConferenceState tracks the current meeting state within TaskState.
type ConferenceState struct {
	Enabled  bool            `json:"enabled"`
	Phase    ConferencePhase `json:"phase"`
	Board    ConferenceBoard `json:"board"`
	Rebuttal int             `json:"rebuttal"` // feedback round counter per review phase
}

// ConferenceCommand represents a parsed slash command.
type ConferenceCommand struct {
	Phase   ConferencePhase
	Goal    string
	Context string
}

// parseConferenceCommand checks if userMsg is a slash command.
func parseConferenceCommand(userMsg string) *ConferenceCommand {
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
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	switch cmd {
	case "plan":
		return &ConferenceCommand{Phase: PhasePlanning, Goal: args}
	case "implement":
		return &ConferenceCommand{Phase: PhaseExecute, Goal: args}
	case "review":
		return &ConferenceCommand{Phase: PhaseReview, Goal: args, Context: args}
	}
	return nil
}

// ConferenceHall orchestrates the meeting flow.
type ConferenceHall struct {
	engine *Engine
	agents *AgentRegistry
}

func NewConferenceHall(e *Engine) *ConferenceHall {
	return &ConferenceHall{
		engine: e,
		agents: e.agents,
	}
}

// Advance steps through the conference phases.
func (h *ConferenceHall) Advance(ctx context.Context, userMsg string) (*EngineResponse, error) {
	state := h.engine.state
	if state.Conference == nil || !state.Conference.Enabled {
		return nil, nil
	}

	zh := msgIsChinese(userMsg)
	if !zh && userMsg == "" {
		// Post-execute auto-review: userMsg is empty. Check history for language.
		zh = msgIsChinese(state.Conference.Board.Goal) ||
			msgIsChinese(h.engine.getLastUserContent())
	}

	switch state.Conference.Phase {
	case PhasePlanning:
		return h.handlePlanning(ctx, userMsg, zh)
	case PhaseExecute:
		board := &state.Conference.Board
		if board.PendingReview {
			return h.handleExecuteReview(ctx, userMsg, zh)
		}
		// First entry — fall through to main agent loop
		return nil, nil
	case PhaseReview:
		return h.handleReview(ctx, userMsg, zh)
	case PhaseDone:
		msg := "🎯 Task completed"
		if zh {
			msg = "🎯 任务完成"
		}
		return &EngineResponse{Summary: msg, Stage: StageVerifyCompact}, nil
	default:
		return nil, nil
	}
}

// handlePlanning manages the Planning phase state machine:
//   - First entry (board.Plan empty): run exploration + brainstorm, present proposals
//   - User responds: number → Execute, /implement → Execute, /plan → re-plan
//   - Everything else → falls through to main agent loop
func (h *ConferenceHall) handlePlanning(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board

	if board.Plan != "" {
		// Numeric option selection: "1", "2", "3" → pick that approach and advance to Execute
		if opt := parseOptionSelection(userMsg); opt > 0 {
			state.Conference.Phase = PhaseExecute
			board.PendingReview = false
			state.Conference.Rebuttal = 0
			h.engine.state.ConfirmedScope = true
			return nil, nil
		}
		// Any other input — fall through to main agent loop.
		// User can use /implement to proceed, /plan to re-plan.
		return nil, nil
	}

	return h.runPlanningPhase(ctx, zh)
}

// repoMapProvider is a local interface to access RepoMap from ContextAssembler
// without importing the context package (avoids circular dependency).
type repoMapProvider interface {
	RefreshRepoMap()
	RepoMapContent() string
}

// buildCodebaseContext builds a unified three-layer codebase context:
//  1. Symbol layer (repoMapSymbols) — types, functions, interfaces (optional)
//  2. Text matching layer (preSearch) — keyword grep results
//  3. Structure layer (buildRepoMapContext) — project directory tree
//
// Total cap: ~1000000 bytes.
func buildCodebaseContext(workDir, goal, repoMapSymbols string) string {
	if workDir == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## Codebase Context\n")

	// Layer 1: Symbol layer — Go AST symbol map (optional, from RepoMap)
	if repoMapSymbols != "" {
		if len(repoMapSymbols) > 1000000 {
			repoMapSymbols = repoMapSymbols[:1000000] + "\n... (symbol context truncated)"
		}
		sb.WriteString("\n### RepoMap (Go AST Symbols)\n")
		sb.WriteString(repoMapSymbols)
		sb.WriteString("\n")
	}

	// Layer 2: Text matching layer — keyword grep results from goal
	ps := preSearch(workDir, goal)
	if ps != "" {
		if len(ps) > 1000000 {
			ps = ps[:1000000] + "\n... (search results truncated)"
		}
		sb.WriteString(ps)
		sb.WriteString("\n")
	}

	// Layer 3: Structure layer — project directory tree
	structure := buildRepoMapContext(workDir)
	if structure != "" {
		if len(structure) > 1000000 {
			structure = structure[:1000000] + "\n... (structure truncated)"
		}
		sb.WriteString(structure)
		sb.WriteString("\n")
	}

	result := sb.String()
	if len(result) > 1000000 {
		result = result[:1000000] + "\n... (codebase context truncated)"
	}
	return result
}

// buildRepoMapContext walks the project directory and returns a file tree overview
// with key Go files, capped at ~2000 bytes. Non-LLM, sub-millisecond.
//
// Deprecated: Use buildCodebaseContext instead, which includes this layer plus
// RepoMap symbols and preSearch text matching.
func buildRepoMapContext(workDir string) string {
	if workDir == "" {
		return ""
	}
	dirs, goFiles := walkProject(workDir)
	if len(dirs) == 0 && len(goFiles) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## Project Structure\n")
	for _, d := range dirs {
		sb.WriteString("- ")
		sb.WriteString(d)
		sb.WriteString("/\n")
	}
	for _, f := range goFiles {
		if len(f) > 0 {
			sb.WriteString("  - ")
			sb.WriteString(f)
			sb.WriteString("\n")
		}
	}
	result := sb.String()
	if len(result) > 2000 {
		result = result[:2000] + "\n... (truncated)"
	}
	return result
}

// walkProject returns top-level directories and Go files (up to 30 entries total).
func walkProject(root string) (dirs []string, goFiles []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	count := 0
	for _, e := range entries {
		if count >= 30 {
			break
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name)
			count++
			// List Go files inside top-level dirs (one level deep)
			subEntries, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				continue
			}
			for _, se := range subEntries {
				if count >= 30 {
					break
				}
				if strings.HasSuffix(se.Name(), ".go") {
					goFiles = append(goFiles, name+"/"+se.Name())
					count++
				}
			}
		} else if strings.HasSuffix(name, ".go") {
			goFiles = append(goFiles, name)
			count++
		}
	}
	return
}

// preSearch runs a fast non-LLM scan of the codebase using ripgrep and file globbing,
// extracting keywords from the goal. Returns formatted Markdown ready for injection
// into the searcher's prompt. Results are capped at ~3000 bytes.
func preSearch(workDir, goal string) string {
	if workDir == "" {
		return ""
	}

	keywords := extractKeywords(goal)
	if len(keywords) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Pre-search Results\n")

	// Run ripgrep for each keyword: 3 matches per keyword, 2 lines of context.
	// The model sees actual code around matches, not just file:line references.
	rgPath := "rg"
	if _, err := lookPath(rgPath); err != nil {
		rgPath = "grep"
	}
	totalBytes := 0
	maxBytes := 1000000
	seen := make(map[string]bool)
	for _, kw := range keywords {
		if totalBytes >= maxBytes {
			break
		}
		args := []string{"--no-heading", "-n", "-C", "5", "-m", "8", "--", kw, workDir}
		if rgPath == "grep" {
			args = []string{"-rn", "-C", "5", "-m", "8", "--", kw, workDir}
		}
		cmd := execCommand(rgPath, args...)
		if cmd == nil {
			continue
		}
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			continue
		}
		// Split output by file blocks, deduplicate, write to result
		for _, block := range splitRgOutput(string(out), workDir) {
			if totalBytes >= maxBytes {
				break
			}
			if seen[block.file] {
				continue
			}
			seen[block.file] = true
			sb.WriteString("**")
			sb.WriteString(block.file)
			sb.WriteString("**\n")
			sb.WriteString(block.content)
			sb.WriteString("\n")
			totalBytes += len(block.file) + len(block.content) + 10
		}
	}

	// Also list key Go files at the top level
	topFiles := listTopGoFiles(workDir)
	if len(topFiles) > 0 {
		sb.WriteString("**Top-level files:**\n")
		for _, f := range topFiles {
			sb.WriteString("- ")
			sb.WriteString(f)
			sb.WriteString("\n")
		}
	}

	result := sb.String()
	if len(result) > 1000000 {
		result = result[:1000000] + "\n... (truncated)"
	}
	if result == "" || result == "\n## Pre-search Results\n" {
		return ""
	}
	return result
}

// extractKeywords pulls meaningful search terms from a user goal.
func extractKeywords(goal string) []string {
	// Remove common punctuation and split
	goal = strings.TrimSpace(goal)
	words := strings.FieldsFunc(goal, func(r rune) bool {
		return r == '，' || r == ',' || r == '。' || r == '.' || r == ' ' || r == '、' || r == '？' || r == '?' || r == '！' || r == '!'
	})

	// Filter out very short words and common stop words
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true, "to": true,
		"in": true, "of": true, "for": true, "and": true, "or": true, "with": true,
		"我": true, "要": true, "想": true, "帮": true, "请": true, "的": true,
		"了": true, "在": true, "是": true, "有": true, "和": true, "与": true,
		"一个": true, "这个": true, "那个": true, "什么": true, "如何": true,
		"怎么": true, "可以": true, "应该": true, "需要": true, "实现": true,
		"修改": true, "添加": true, "删除": true, "优化": true, "重构": true,
		"写": true, "做": true, "搞": true, "弄": true, "来": true, "去": true,
	}
	var kw []string
	for _, w := range words {
		w = strings.TrimSpace(w)
		if len(w) < 2 || stopWords[strings.ToLower(w)] {
			continue
		}
		kw = append(kw, w)
	}

	// Deduplicate, keep first 5
	seen := make(map[string]bool)
	var result []string
	for _, w := range kw {
		lower := strings.ToLower(w)
		if !seen[lower] {
			seen[lower] = true
			result = append(result, w)
			if len(result) >= 15 {
				break
			}
		}
	}
	return result
}

// listTopGoFiles returns up to 10 Go files in the top-level and key subdirs.
func listTopGoFiles(workDir string) []string {
	patterns := []string{
		workDir + "/*.go",
		workDir + "/cmd/**/*.go",
		workDir + "/engine/*.go",
		workDir + "/ui/*.go",
	}
	var result []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			rel, err := filepath.Rel(workDir, m)
			if err != nil {
				rel = m
			}
			result = append(result, rel)
			if len(result) >= 10 {
				return result
			}
		}
	}
	return result
}

type rgBlock struct {
	file    string
	content string
}

// splitRgOutput parses ripgrep --no-heading -C output into per-file blocks.
func splitRgOutput(output, workDir string) []rgBlock {
	lines := strings.Split(output, "\n")
	var blocks []rgBlock
	var current *rgBlock
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "--" {
			continue
		}
		file := extractFileFromRgLine(trimmed)
		if file != "" {
			// New match line — check if same file continues or new file starts
			short := trimFilePath(file, workDir)
			if current != nil && current.file == short {
				current.content += trimmed + "\n"
				continue
			}
			if current != nil && current.content != "" {
				blocks = append(blocks, *current)
			}
			current = &rgBlock{file: short, content: trimmed + "\n"}
		} else if current != nil {
			current.content += trimmed + "\n"
		}
	}
	if current != nil && current.content != "" {
		blocks = append(blocks, *current)
	}
	return blocks
}

// extractFileFromRgLine pulls the file path from a ripgrep output line.
func extractFileFromRgLine(line string) string {
	for i, c := range line {
		if c == ':' || c == '-' {
			candidate := line[:i]
			if strings.Contains(candidate, ".") || strings.Contains(candidate, "/") {
				return candidate
			}
		}
	}
	return ""
}

// trimFilePath strips the workDir prefix and long paths from grep output lines.
func trimFilePath(line, workDir string) string {
	line = strings.TrimPrefix(line, workDir+"/")
	if len(line) > 120 {
		line = line[:120] + "..."
	}
	return line
}

// lookPath is a wrapper around exec.LookPath for testing.
var lookPath = func(file string) (string, error) {
	return exec.LookPath(file)
}

// execCommand is a wrapper around exec.Command for testing.
var execCommand = func(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// langHint returns a language instruction based on whether the user writes in Chinese.
func langHint(zh bool) string {
	if zh {
		return "\n\nIMPORTANT: Respond in Chinese (简体中文)."
	}
	return ""
}

// runPlanningPhase: searcher explores codebase + brainstorm generates proposals.
// On rebuttal, includes user's feedback so the agents can adjust.
func (h *ConferenceHall) runPlanningPhase(ctx context.Context, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board
	goal := board.Goal

	if h.engine.config.OnProgress != nil {
		detail := "Exploring codebase and analyzing request..."
		if zh {
			detail = "正在探索代码并分析需求..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "planning", Detail: detail})
	}

	// Collect prior feedback for rebuttal rounds
	lastFeedback := ""
	if board.Plan != "" {
		lastFeedback = h.engine.getLastUserContent()
	}

	// Language instruction for agent prompts
	langHint := ""
	if zh {
		langHint = "\n\nIMPORTANT: The user communicates in Chinese. You MUST output ALL your findings and analysis in Chinese (简体中文). Use Chinese for all descriptions, file summaries, and recommendations."
	}

	// Refresh RepoMap and build unified codebase context (symbols + search + structure)
	var repoMapSymbols string
	if r, ok := h.engine.context.(repoMapProvider); ok {
		r.RefreshRepoMap()
		repoMapSymbols = r.RepoMapContent()
	}
	codebaseCtx := buildCodebaseContext(h.engine.config.WorkDir, goal, repoMapSymbols)

	if h.engine.config.OnProgress != nil {
		detail := "Analyzing codebase and brainstorming approaches..."
		if zh {
			detail = "正在分析代码并头脑风暴方案..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "planning", Detail: detail})
	}

	// Single agent: brainstorm with full codebase context pre-loaded.
	// May still read 1-2 files for details, but skips exploration rounds.
	brainstormGoal := fmt.Sprintf(
		`Based on the codebase context and user's goal, analyze and respond.

## Original Goal
%s

## Codebase Context (pre-computed, no search needed)
%s
%s

## Instructions
1. First, review the codebase context above. It already contains grep matches WITH code context — you can see the actual code around each match. Do NOT re-search or re-grep.
2. ONLY read a file if the context above doesn't show enough detail. Limit to 1 read max.
3. JUDGE the task type:
   - **Simple question / exploration**: Just answer directly. Summarize what the project does, how it's structured, or answer the user's question. No approaches needed.
   - **Implementation task**: Propose 2-3 different approaches. For each, list pros/cons and recommend the best one with an implementation outline.
4. Be flexible — the output format should match what the user actually needs.%s

## Output Format (adapt to task type)
### For simple questions: just answer directly with a clear summary.
### For implementation tasks:
### Approach 1: <name>
- What: <description>
- Pros: <list>
- Cons: <list>

### Approach 2: <name>
...

## Recommended: <approach name> (reasoning)

## Implementation Outline
1. <step> — <file path> — <what to do>`,
		goal, codebaseCtx, lastFeedback, langHint)
	brainstormResult, err := h.callAgent(ctx, AgentBrainstorm, brainstormGoal)
	// Retry once if the agent returns empty or nonsensical output
	if err == nil && (brainstormResult == nil || len(strings.TrimSpace(brainstormResult.Summary)) < 50) {
		if h.engine.config.OnProgress != nil {
			h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "planning", Detail: "Retrying brainstorm — previous result was empty..."})
		}
		brainstormResult, err = h.callAgent(ctx, AgentBrainstorm, brainstormGoal)
	}
	if err != nil {
		return nil, fmt.Errorf("planning brainstorm agent: %w", err)
	}
	if brainstormResult == nil || strings.TrimSpace(brainstormResult.Summary) == "" {
		msg := "Planning failed — agent returned empty result. Please try /plan again with more detail."
		if zh {
			msg = "规划失败 — Agent 返回空结果。请提供更多细节后重试 /plan。"
		}
		return &EngineResponse{Summary: msg, Stage: StagePlan, Blocked: true, BlockedBy: "empty_brainstorm"}, nil
	}

	files := extractFilePaths(brainstormResult.Summary)

	// Update board
	board.ExploreResult = codebaseCtx
	board.DecideOptions = brainstormResult.Summary
	board.Plan = brainstormResult.Summary
	board.RelatedFiles = files
	board.PendingReview = false

	options := extractApproaches(brainstormResult.Summary)
	hint := ""
	if len(options) > 0 {
		hint = "\n\n---\n\n输入方案编号选择 (如 '1')，或直接输入反馈让 AI 调整方案"
	}
	msg := fmt.Sprintf("## 代码探索与方案分析\n\n### 代码现状\n%s\n\n---\n\n### 方案建议\n%s%s",
		truncateStr(codebaseCtx, 300),
		brainstormResult.Summary, hint)
	if !zh {
		hint = ""
		if len(options) > 0 {
			hint = fmt.Sprintf("\n\n---\n\nSelect an approach (type 1-%d) or type custom feedback to revise.", len(options))
		}
		msg = fmt.Sprintf("## Code Exploration & Proposals\n\n### Current State\n%s\n\n---\n\n### Proposals\n%s%s",
			truncateStr(codebaseCtx, 300),
			brainstormResult.Summary, hint)
	}
	return &EngineResponse{
		Summary:   msg,
		Options:   options,
		Blocked:   true,
		BlockedBy: "conference",
		Stage:     StagePlan,
	}, nil
}

// parseOptionSelection checks if the user typed a single digit (1-9) to select
// a proposed approach. Returns the selected number, or 0 if not a selection.
func parseOptionSelection(msg string) int {
	trimmed := strings.TrimSpace(msg)
	if len(trimmed) == 1 && trimmed[0] >= '1' && trimmed[0] <= '9' {
		return int(trimmed[0] - '0')
	}
	return 0
}

// extractApproaches parses brainstorm output for approach names.
// Matches patterns like "### Approach 1: Redis-based session" or
// "### 方案一：基于Redis的会话管理".
func extractApproaches(summary string) []string {
	var approaches []string
	lines := strings.Split(summary, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match: "### Approach N: <name>" or "### 方案 N: <name>" or "## 方案N: <name>"
		if (strings.HasPrefix(trimmed, "### Approach ") || strings.HasPrefix(trimmed, "### 方案") || strings.HasPrefix(trimmed, "## 方案")) && strings.Contains(trimmed, ":") {
			if idx := strings.Index(trimmed, ":"); idx > 0 {
				name := strings.TrimSpace(trimmed[idx+1:])
				if name != "" {
					approaches = append(approaches, name)
				}
			}
		} else if strings.HasPrefix(trimmed, "### Approach ") || strings.HasPrefix(trimmed, "### 方案") {
			// No colon fallback: "### Approach 1" or "### 方案一"
			approaches = append(approaches, trimmed)
		}
	}
	return approaches
}

// handleExecuteReview processes user feedback after implementation completes.
// User confirms → PhaseReview (challenger verification).
// User provides feedback → re-execute with feedback (max 2 rounds).
func (h *ConferenceHall) handleExecuteReview(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board

	// Any user input after execution is treated as feedback → re-execute.
	// User can use /review to skip to review, or /implement to re-execute cleanly.
	if state.Conference.Rebuttal >= 2 {
		msg := "修改次数过多。请确认当前结果或给出具体方向。"
		if !zh {
			msg = "Too many revision rounds. Please confirm or provide specific direction."
		}
		return &EngineResponse{
			Summary:   msg,
			Questions: []string{msg},
			Blocked:   true,
			BlockedBy: "conference",
			Stage:     StageVerifyCompact,
		}, nil
	}

	state.Conference.Rebuttal++
	board.PendingReview = false
	state.Conference.Phase = PhaseExecute

	feedback := fmt.Sprintf("User feedback on implementation: %s\n\nPlease fix the issues and re-implement.", userMsg)
	if zh {
		feedback = fmt.Sprintf("用户对实现的反馈: %s\n\n请修复问题后重新实现。", userMsg)
	}

	// Return unblocked so main loop can run with feedback
	return &EngineResponse{
		Summary: feedback,
		Stage:   StageAct,
	}, nil
}

// handleReview manages the Review phase state machine:
//   - First entry: run review
//   - User responds to review results: mark Done, fall through to main agent
func (h *ConferenceHall) handleReview(ctx context.Context, userMsg string, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board

	if board.PendingReview {
		// User has seen review results — mark Done. Use /implement to re-execute.
		state.Conference.Phase = PhaseDone
		return nil, nil
	}

	// First entry — assess context and run unified review
	reviewCtx := h.assessContextLevel(state, userMsg)
	return h.runReview(ctx, reviewCtx, zh)
}

// runChallengerReview compares the implementation against the plan using a direct
// Flash model call — no agent, no code search. The review is a text comparison
// of (goal + plan) vs (implementation summary + modified files).
func (h *ConferenceHall) runChallengerReview(ctx context.Context, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board

	if h.engine.config.OnProgress != nil {
		detail := "Reviewing implementation against plan..."
		if zh {
			detail = "正在对照方案审查实现..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "review", Detail: detail})
	}

	// Collect what was built: assistant summary + modified file list + key diffs
	implSummary := h.engine.getLastAssistantContent()
	modifiedFiles := strings.Join(state.ModifiedFiles, ", ")
	keyDiffs := h.collectKeyDiffs(3) // up to 3 key diffs from history

	var reviewPrompt string
	if zh {
		reviewPrompt = fmt.Sprintf(`对照原始目标和方案，审查实现结果。为每个维度打分。

## 原始目标
%s

## 计划方案
%s

## 实现总结
%s

## 修改的文件
%s

## 关键改动 (diff)
%s

## 评分维度
- 目标完成度 (35%%): 实现是否真正解决了原始问题？
- 代码正确性 (30%%): 逻辑是否正确？
- 边界情况覆盖 (20%%): 关键边界情况是否处理？
- 方案一致性 (15%%): 实现是否遵循计划方案？

## 指令
- 对比计划和实际构建的内容。关注意图与结果的匹配度。
- 不要建议读取更多文件。基于以上文档进行审查。
- 每个维度打分 0-100，附简要证据。
- 总分 >= 80 为 PASS，< 80 为 FAIL。

## 输出格式
### 目标 vs 实现
<2-3 句话对比计划和实际完成内容>

### 维度评分
| 维度 | 评分 | 证据 |
|------|------|------|

### 总分: X/100 — PASS/FAIL

### 结论
<总结哪些符合方案、哪些有偏差、以及任何问题>`,
			board.Goal,
			board.Plan,
			implSummary,
			modifiedFiles,
			keyDiffs)
	} else {
		reviewPrompt = fmt.Sprintf(`Compare the implementation against the original goal and plan. Score each dimension.

## Original Goal
%s

## Planned Approach
%s

## Implementation Summary
%s

## Modified Files
%s

## Key Changes (diffs)
%s

## Scoring Dimensions
- Goal Fulfillment (35%%): Does the implementation actually solve the original goal?
- Code Correctness (30%%): Is the approach logically correct?
- Edge Case Coverage (20%%): Are key edge cases handled?
- Plan Consistency (15%%): Does the implementation follow the planned approach?

## Instructions
- Compare the plan vs what was actually built. Focus on intent-vs-outcome.
- Do NOT suggest reading more files. Review based on the documents above.
- Score each dimension 0-100 with brief evidence from the diffs/summary.
- PASS if >= 80, FAIL if < 80.

## Output Format
### Goal vs Implementation
<2-3 sentences comparing what was planned vs what was done>

### Dimension Scores
| Dimension | Score | Evidence |
|-----------|-------|----------|

### Total Score: X/100 — PASS/FAIL

### Verdict
<summary of what matches the plan, what diverges, and any concerns>`,
			board.Goal,
			board.Plan,
			implSummary,
			modifiedFiles,
			keyDiffs)
	}

	req := ModelRequest{
		Model: h.engine.config.ModelName,
		Messages: []ModelMessage{
			{Role: "user", Content: reviewPrompt},
		},
		MaxTokens: 1024,
	}

	resp, err := h.engine.model.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}

	h.persistReviewResult(PhaseVerificationReview, resp.Message.Content)

	board.PendingReview = true
	state.Conference.Rebuttal = 0

	msg := fmt.Sprintf("## 实现审查\n\n%s\n\n---\n\n输入 'yes' 确认完成，或提供反馈重新执行。", resp.Message.Content)
	if !zh {
		msg = fmt.Sprintf("## Implementation Review\n\n%s\n\n---\n\nType 'yes' to confirm completion, or provide feedback to re-execute.", resp.Message.Content)
	}
	return &EngineResponse{
		Summary:   msg,
		Blocked:   true,
		BlockedBy: "conference",
		Stage:     StageVerifyCompact,
	}, nil
}

// collectKeyDiffs extracts up to n recent edit/write diffs from engine history.
func (h *ConferenceHall) collectKeyDiffs(n int) string {
	history := h.engine.history
	var diffs []string
	count := 0
	// Walk backwards to find recent tool results with diff content
	for i := len(history) - 1; i >= 0 && count < n; i-- {
		msg := history[i]
		if msg.Role != "tool" {
			continue
		}
		// Check if this is an edit/write result containing a diff
		if strings.Contains(msg.Content, "--- a/") || strings.Contains(msg.Content, "+++ b/") {
			diffs = append(diffs, msg.Content)
			count++
		}
	}
	if len(diffs) == 0 {
		return "(no diffs found)"
	}
	return strings.Join(diffs, "\n\n---\n\n")
}

// persistReviewResult parses a review text for scoring info and persists it
// as an EvalRecord to the engine's eval store (if configured).
func (h *ConferenceHall) persistReviewResult(phase ConferencePhase, reviewText string) {
	store := h.engine.evalStore
	if store == nil {
		return
	}
	state := h.engine.state
	board := &state.Conference.Board

	score, passed, verdict := parseScoreFromText(reviewText)

	goal := board.Goal
	goalSnippet := goal
	if len(goalSnippet) > 120 {
		goalSnippet = goalSnippet[:120]
	}
	if len(reviewText) > 500 {
		reviewText = reviewText[:500]
	}

	rec := EvalRecord{
		Timestamp:      time.Now(),
		SessionID:      h.engine.config.SessionID,
		PromptVersion:  h.engine.config.PromptVersion,
		Phase:          phase.String(),
		TotalScore:     score,
		Passed:         passed,
		Verdict:        verdict,
		Summary:        reviewText,
		IterationCount: state.TurnNumber,
		GoalSnippet:    goalSnippet,
	}
	_ = store.Insert(rec)
}

// --- Unified Review Pipeline ---

// assessContextLevel determines the review context richness based on conference state and user message.
func (h *ConferenceHall) assessContextLevel(state *TaskState, userMsg string) ReviewContext {
	board := &state.Conference.Board

	// L1: Full context — conference has Goal + recent diffs (Plan is ideal but optional)
	if board.Goal != "" {
		diffs := h.collectKeyDiffs(1)
		if diffs != "" && !strings.Contains(diffs, "no diffs found") {
			return ReviewContext{
				Level:     ReviewLevelFull,
				Goal:      board.Goal,
				PlanSteps: board.Plan,
				UserDesc:  userMsg,
			}
		}
	}

	// L2: Partial context — user described a function/feature, workspace has code
	if userMsg != "" {
		files := h.collectRelevantFiles(userMsg)
		if len(files) > 0 {
			return ReviewContext{
				Level:     ReviewLevelPartial,
				Goal:      board.Goal,
				CodeFiles: files,
				UserDesc:  userMsg,
			}
		}
	}

	// L3: Minimal context — no plan, no diffs, user description is vague
	goal := userMsg
	if goal == "" {
		goal = board.Goal
	}
	return ReviewContext{
		Level:    ReviewLevelMinimal,
		Goal:     goal,
		UserDesc: userMsg,
	}
}

// collectRelevantFiles extracts file path mentions from a user message.
func (h *ConferenceHall) collectRelevantFiles(msg string) []string {
	var files []string
	seen := make(map[string]bool)
	words := strings.Fields(msg)
	for _, w := range words {
		w = strings.Trim(w, "\"',;.()[]{}\n\t")
		if strings.HasSuffix(w, ".go") || strings.HasSuffix(w, ".ts") || strings.HasSuffix(w, ".js") {
			if !seen[w] && len(w) > 2 && len(w) < 200 {
				seen[w] = true
				files = append(files, w)
			}
		}
	}
	// If user didn't specify a file, check modified files in state as context
	if len(files) == 0 {
		if h.engine.state != nil && len(h.engine.state.ModifiedFiles) > 0 {
			files = append(files, h.engine.state.ModifiedFiles...)
		}
	}
	return files
}

// runReview is the unified review entry point. It dispatches to the appropriate
// review strategy based on context richness.
func (h *ConferenceHall) runReview(ctx context.Context, reviewCtx ReviewContext, zh bool) (*EngineResponse, error) {
	switch reviewCtx.Level {
	case ReviewLevelFull:
		return h.runFullReview(ctx, reviewCtx, zh)
	case ReviewLevelPartial:
		return h.runPartialReview(ctx, reviewCtx, zh)
	default:
		return h.runMinimalReview(ctx, reviewCtx, zh)
	}
}

// runFullReview compares implementation against plan using Goal + Plan + Diffs.
// Refactored from runChallengerReview — behavior is identical.
func (h *ConferenceHall) runFullReview(ctx context.Context, reviewCtx ReviewContext, zh bool) (*EngineResponse, error) {
	state := h.engine.state
	board := &state.Conference.Board

	if h.engine.config.OnProgress != nil {
		detail := "Reviewing implementation against plan..."
		if zh {
			detail = "正在对照方案审查实现..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "review", Detail: detail})
	}

	implSummary := h.engine.getLastAssistantContent()
	modifiedFiles := strings.Join(state.ModifiedFiles, ", ")
	keyDiffs := h.collectKeyDiffs(3)

	var reviewPrompt string
	if zh {
		reviewPrompt = fmt.Sprintf(`对照原始目标和方案，审查实现结果。为每个维度打分。

## 原始目标
%s

## 计划方案
%s

## 实现总结
%s

## 修改的文件
%s

## 关键改动 (diff)
%s

## 评分维度
- 目标完成度 (35%%): 实现是否真正解决了原始问题？
- 代码正确性 (30%%): 逻辑是否正确？
- 边界情况覆盖 (20%%): 关键边界情况是否处理？
- 方案一致性 (15%%): 实现是否遵循计划方案？

## 指令
- 对比计划和实际构建的内容。关注意图与结果的匹配度。
- 不要建议读取更多文件。基于以上文档进行审查。
- 每个维度打分 0-100，附简要证据。
- 总分 >= 80 为 PASS，< 80 为 FAIL。

## 输出格式
### 目标 vs 实现
<2-3 句话对比计划和实际完成内容>

### 维度评分
| 维度 | 评分 | 证据 |
|------|------|------|

### 总分: X/100 — PASS/FAIL

### 结论
<总结哪些符合方案、哪些有偏差、以及任何问题>`,
			board.Goal,
			board.Plan,
			implSummary,
			modifiedFiles,
			keyDiffs)
	} else {
		reviewPrompt = fmt.Sprintf(`Compare the implementation against the original goal and plan. Score each dimension.

## Original Goal
%s

## Planned Approach
%s

## Implementation Summary
%s

## Modified Files
%s

## Key Changes (diffs)
%s

## Scoring Dimensions
- Goal Fulfillment (35%%): Does the implementation actually solve the original goal?
- Code Correctness (30%%): Is the approach logically correct?
- Edge Case Coverage (20%%): Are key edge cases handled?
- Plan Consistency (15%%): Does the implementation follow the planned approach?

## Instructions
- Compare the plan vs what was actually built. Focus on intent-vs-outcome.
- Do NOT suggest reading more files. Review based on the documents above.
- Score each dimension 0-100 with brief evidence from the diffs/summary.
- PASS if >= 80, FAIL if < 80.

## Output Format
### Goal vs Implementation
<2-3 sentences comparing what was planned vs what was done>

### Dimension Scores
| Dimension | Score | Evidence |
|-----------|-------|----------|

### Total Score: X/100 — PASS/FAIL

### Verdict
<summary of what matches the plan, what diverges, and any concerns>`,
			board.Goal,
			board.Plan,
			implSummary,
			modifiedFiles,
			keyDiffs)
	}

	req := ModelRequest{
		Model: h.engine.config.ModelName,
		Messages: []ModelMessage{
			{Role: "user", Content: reviewPrompt},
		},
		MaxTokens: 1024,
	}

	resp, err := h.engine.model.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("review: %w", err)
	}

	h.persistReviewResult(PhaseVerificationReview, resp.Message.Content)

	board.PendingReview = true
	state.Conference.Rebuttal = 0

	msg := fmt.Sprintf("## 实现审查\n\n%s\n\n---\n\n输入 'yes' 确认完成，或提供反馈重新执行。", resp.Message.Content)
	if !zh {
		msg = fmt.Sprintf("## Implementation Review\n\n%s\n\n---\n\nType 'yes' to confirm completion, or provide feedback to re-execute.", resp.Message.Content)
	}
	return &EngineResponse{
		Summary:   msg,
		Blocked:   true,
		BlockedBy: "conference",
		Stage:     StageVerifyCompact,
	}, nil
}

// runPartialReview reviews code against a user's functional description (no formal plan).
// Uses AgentTester to search workspace code and compare against the described functionality.
func (h *ConferenceHall) runPartialReview(ctx context.Context, reviewCtx ReviewContext, zh bool) (*EngineResponse, error) {
	if h.engine.config.OnProgress != nil {
		detail := "Running code review based on your description..."
		if zh {
			detail = "正在根据您的描述进行代码审查..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_phase", Name: "review", Detail: detail})
	}

	// Build the review goal for AgentTester
	filesContext := ""
	if len(reviewCtx.CodeFiles) > 0 {
		filesContext = "## Relevant Files\n" + strings.Join(reviewCtx.CodeFiles, "\n")
	}

	goal := fmt.Sprintf(`Review the code against the user's functional description using the ScoreCard.

## User's Functionality Description
%s

%s

## Instructions
- FIRST, understand the functionality the user describes. This is the "implicit requirement".
- SECOND, use grep/glob/read tools to find and read relevant code files in the workspace.
- THIRD, compare the actual code against the user's described functionality.
- Focus on: does the code do what the user expects? Are there bugs, missing edge cases, or potential issues?
- You may need to search the workspace to find relevant files if none are specified.
- Limit searches to 5 tool calls maximum.
- Use the ScoreCard dimensions below and score based on evidence.

## Scoring Dimensions (partial review — no formal plan)
- Functionality Match (40%%): Does the code match the user's described functionality?
- Code Correctness (25%%): Is the code logically correct?
- Edge Case Coverage (20%%): Are key edge cases handled?
- Code Maintainability (15%%): Is the code clean and maintainable?

## Output Format
### Summary
<2-3 sentences summarizing what was reviewed and the key findings>

### Dimension Scores
| Dimension | Score | Evidence |
|-----------|-------|----------|

### Total Score: X/100 — PASS/FAIL

### Issues Found
<list of specific issues with file paths and line numbers if applicable>

### Verdict
<overall assessment and recommendations>%s`,
		reviewCtx.UserDesc,
		filesContext,
		langHint(zh))

	result, err := h.callAgent(ctx, AgentTester, goal)
	if err != nil {
		return nil, fmt.Errorf("partial review: %w", err)
	}

	h.persistReviewResult(PhaseVerificationReview, result.Summary)

	summary := fmt.Sprintf("## 代码审查\n\n%s\n\n---\n\n以上审查基于您描述的功能期望。如需更精确的审查，请提供具体文件或更详细的功能描述。",
		result.Summary)
	if !zh {
		summary = fmt.Sprintf("## Code Review\n\n%s\n\n---\n\nReview based on your described functionality. For a more precise review, provide specific files or a more detailed description.",
			result.Summary)
	}
	return &EngineResponse{
		Summary: summary,
		Stage:   StageVerifyCompact,
	}, nil
}

// runMinimalReview handles the case where context is too vague for an automated review.
// Guides the user to provide more specific information.
func (h *ConferenceHall) runMinimalReview(ctx context.Context, reviewCtx ReviewContext, zh bool) (*EngineResponse, error) {
	msg := "I'd like to help with the review, but I need more context. Please specify:\n\n1. **Which files or functions** would you like me to review? (e.g., `engine/types.go`, the `Run` function)\n2. **What functionality** should I check against? (e.g., \"user authentication flow\", \"error handling\")\n3. **Any specific concerns?** (e.g., performance, security, edge cases)"
	if zh {
		msg = "我需要更多信息来进行审查。请提供：\n\n1. **要审查的文件或函数**（例如 `engine/types.go`、`Run` 函数）\n2. **要对照的功能描述**（例如\"用户认证流程\"、\"错误处理逻辑\"）\n3. **是否有特别关注的点**（例如性能、安全性、边界条件）"
	}
	return &EngineResponse{
		Summary:   msg,
		Blocked:   true,
		BlockedBy: "conference",
		Stage:     StageVerifyCompact,
	}, nil
}

// --- Agent calling helper ---

// agentLabel returns a short human-readable label for an agent ID.
func agentLabel(id AgentID) string {
	switch id {
	case AgentSearcher:
		return "搜索代码库..."
	case AgentBrainstorm:
		return "头脑风暴方案..."
	case AgentProposer:
		return "分析需求..."
	case AgentPlanner:
		return "制定实现计划..."
	case AgentCritic:
		return "严格审查..."
	case AgentChallenger:
		return "挑战者评审..."
	case AgentTester:
		return "验证实现..."
	case AgentCodeSearcher:
		return "搜索代码..."
	default:
		return "执行子任务..."
	}
}

func (h *ConferenceHall) callAgent(ctx context.Context, agentID AgentID, goal string) (*HandoffResult, error) {
	agent, err := h.agents.Get(agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent %s: %w", agentID, err)
	}
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{Type: "agent_start", Name: string(agentID), Detail: agentLabel(agentID)})
	}
	input := Handoff{
		Agent: agentID,
		Goal:  goal,
		Depth: 0,
	}
	result, err := agent.Run(ctx, input)
	if err != nil {
		// Convert agent crash to graceful degradation — don't crash the conference.
		return &HandoffResult{
			Summary:   fmt.Sprintf("Agent %s failed: %v", agentID, err),
			Blocked:   true,
			BlockedBy: "agent_error",
		}, nil
	}
	if h.engine.config.OnProgress != nil {
		// Show only first line of agent result as brief summary
		summary := briefDigest(result.Summary)
		h.engine.config.OnProgress(ProgressEvent{Type: "agent_done", Name: string(agentID), Detail: summary})
	}
	return result, nil
}

// --- Standalone phase execution ---

// runStandalonePhase runs a single conference phase standalone (one-shot, no pipeline).
func (h *ConferenceHall) runStandalonePhase(ctx context.Context, cmd *ConferenceCommand) (*EngineResponse, error) {
	oldConf := h.engine.state.Conference
	defer func() {
		h.engine.state.Conference = oldConf
	}()

	zh := msgIsChinese(cmd.Goal)

	switch cmd.Phase {
	case PhasePlanning:
		return h.runStandalonePlanning(ctx, cmd.Goal, zh)
	case PhaseReview:
		return h.runStandaloneReview(ctx, cmd.Goal, zh)
	}
	return nil, nil
}

// runStandalonePlanning runs the planning phase standalone.
func (h *ConferenceHall) runStandalonePlanning(ctx context.Context, goal string, zh bool) (*EngineResponse, error) {
	h.engine.state.Conference = &ConferenceState{
		Enabled: true,
		Phase:   PhasePlanning,
		Board: ConferenceBoard{
			Goal:  goal,
			Phase: PhasePlanning,
		},
	}

	if h.engine.config.OnProgress != nil {
		detail := "Running standalone planning..."
		if zh {
			detail = "正在独立运行规划..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_enter", Name: "planning", Detail: detail})
	}

	result, err := h.callAgent(ctx, AgentProposer, fmt.Sprintf(
		`Analyze the following request and create a detailed implementation plan.

## Goal
%s

## Instructions
1. Search the codebase for relevant files.
2. Propose 2-3 approaches with pros and cons.
3. Recommend the best approach with a detailed plan.
4. List exact file paths to create/modify.`,
		goal))
	if err != nil {
		return nil, fmt.Errorf("standalone planning: %w", err)
	}

	summary := fmt.Sprintf("## Standalone Plan\n\n%s", result.Summary)
	return &EngineResponse{
		Summary: summary,
		Stage:   StageVerifyCompact,
	}, nil
}

// runStandaloneReview runs the review phase standalone using the unified pipeline.
func (h *ConferenceHall) runStandaloneReview(ctx context.Context, contextStr string, zh bool) (*EngineResponse, error) {
	h.engine.state.Conference = &ConferenceState{
		Enabled: true,
		Phase:   PhaseReview,
		Board: ConferenceBoard{
			Goal:  contextStr,
			Phase: PhaseReview,
		},
	}

	if h.engine.config.OnProgress != nil {
		detail := "Running standalone review..."
		if zh {
			detail = "正在独立运行审查..."
		}
		h.engine.config.OnProgress(ProgressEvent{Type: "conference_enter", Name: "review", Detail: detail})
	}

	state := h.engine.state
	reviewCtx := h.assessContextLevel(state, contextStr)
	return h.runReview(ctx, reviewCtx, zh)
}

// --- Status display ---

func (h *ConferenceHall) showConferenceStatus() *EngineResponse {
	state := h.engine.state
	if state.Conference == nil || !state.Conference.Enabled {
		msg := "没有活跃的会议会话。\n\n使用 `/规划 <需求描述>` 开始分析需求并制定方案，或直接发送代码相关请求自动进入会议模式。\n\nNo active conference session. Use `/规划 <goal>` to start planning, or send a code-related request to auto-enter conference mode."
		return &EngineResponse{Summary: msg, Stage: StageVerifyCompact}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Conference Status\n\n"))
	sb.WriteString(fmt.Sprintf("**Phase:** %s\n", state.Conference.Phase.String()))
	sb.WriteString(fmt.Sprintf("**Goal:** %s\n", truncateStr(state.Conference.Board.Goal, 100)))
	if state.Conference.Board.Plan != "" {
		sb.WriteString(fmt.Sprintf("**Plan:** %s\n", truncateStr(state.Conference.Board.Plan, 100)))
	}
	sb.WriteString(fmt.Sprintf("**Files:** %s\n", formatFileList(state.Conference.Board.RelatedFiles)))
	sb.WriteString(fmt.Sprintf("**Feedback rounds:** %d\n", state.Conference.Rebuttal))

	return &EngineResponse{Summary: sb.String(), Stage: StageVerifyCompact}
}

// --- Helpers ---

// extractQuestions parses numbered questions from an agent's output (e.g. "1. What...?").
func extractQuestions(summary string) []string {
	var questions []string
	lines := strings.Split(summary, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match numbered questions: "1. text?", "2. text?" etc.
		if len(trimmed) > 3 && (trimmed[0] >= '1' && trimmed[0] <= '9') && strings.Contains(trimmed, ". ") {
			trimmed = strings.TrimLeft(trimmed, "0123456789. ")
			if strings.Contains(trimmed, "？") || strings.Contains(trimmed, "?") {
				questions = append(questions, trimmed)
			}
		}
	}
	// Fallback: if no numbered questions found, look for lines with question marks
	if len(questions) == 0 {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, "?") || strings.HasSuffix(trimmed, "？") {
				questions = append(questions, trimmed)
			}
		}
	}
	if questions == nil {
		questions = []string{}
	}
	return questions
}

func extractFilePaths(summary string) []string {
	var files []string
	seen := make(map[string]bool)
	lines := strings.Split(summary, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, ".go") || strings.HasSuffix(trimmed, ".ts") || strings.HasSuffix(trimmed, ".js") || strings.HasSuffix(trimmed, ".css") {
			trimmed = strings.TrimPrefix(trimmed, "- ")
			trimmed = strings.TrimPrefix(trimmed, "* ")
			trimmed = strings.TrimPrefix(trimmed, "`")
			trimmed = strings.TrimSuffix(trimmed, "`")
			if !seen[trimmed] && len(trimmed) > 3 && len(trimmed) < 200 {
				seen[trimmed] = true
				files = append(files, trimmed)
			}
		}
	}
	return files
}

func formatFileList(files []string) string {
	if len(files) == 0 {
		return "(none)"
	}
	return "- " + strings.Join(files, "\n- ")
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
