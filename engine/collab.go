package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// CollabCommand represents a parsed /collab command.
type CollabCommand struct {
	Goal string
}

// parseCollabCommand checks if userMsg is a /collab command.
func parseCollabCommand(userMsg string) *CollabCommand {
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
	if cmd != "collab" {
		return nil
	}
	goal := strings.Join(parts[1:], " ")
	if goal == "" {
		return nil
	}
	return &CollabCommand{Goal: goal}
}

// CollabHall orchestrates the /collab parallel research.
type CollabHall struct {
	engine *Engine
}

func NewCollabHall(e *Engine) *CollabHall {
	return &CollabHall{engine: e}
}

const (
	collabMaxConcurrency           = 4
	collabMaxTasks                 = 6
	collabMinTasks                 = 2
	collabDecomposerMaxIterations  = 5
	collabWorkerMaxIterations      = 99
	collabSynthesizerMaxIterations = 5
)

// collabTaskPayload mirrors the Decomposer's output contract.
type collabTaskPayload struct {
	Tasks []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Direction string `json:"direction"`
	} `json:"tasks"`
}

// parseCollabTasks parses the Decomposer's JSON task list. Tolerates prose
// wrapping (via topLevelJSONObjects), skips invalid entries, truncates to
// collabMaxTasks, and errors when fewer than collabMinTasks valid tasks remain.
func parseCollabTasks(content string) ([]CollabTask, error) {
	objs := topLevelJSONObjects(content)
	if len(objs) == 0 {
		return nil, fmt.Errorf("invalid Decomposer output: no JSON object found")
	}
	for _, obj := range objs {
		var p collabTaskPayload
		if err := json.Unmarshal([]byte(obj), &p); err != nil {
			continue
		}
		if len(p.Tasks) == 0 {
			continue
		}
		var tasks []CollabTask
		for _, t := range p.Tasks {
			id := strings.TrimSpace(t.ID)
			dir := strings.TrimSpace(t.Direction)
			if id == "" || dir == "" {
				continue
			}
			tasks = append(tasks, CollabTask{
				ID:        id,
				Title:     t.Title,
				Direction: dir,
				Status:    "pending",
			})
			if len(tasks) >= collabMaxTasks {
				break
			}
		}
		if len(tasks) < collabMinTasks {
			continue
		}
		return tasks, nil
	}
	return nil, fmt.Errorf("invalid Decomposer output: no valid task list (objects: %d)", len(objs))
}

// collabResearchRolePrompt returns the compact role system prompt for a
// parallel-research harness role.
func collabResearchRolePrompt(role string, zh bool) string {
	switch role {
	case "decomposer":
		return pickPrompt(zh,
			"You are a Decomposer — a senior architect. Split the research goal into 2-6 non-overlapping research directions. Each direction must be self-contained so an independent researcher can start without shared context. Do NOT write code solutions — define research directions only. Output ONLY the tasks JSON: {\"tasks\":[{\"id\":\"t1\",\"title\":\"...\",\"direction\":\"...\"}]}.",
			"你是「拆解员」——资深架构师。把研究目标拆成 2~6 个互不重叠的研究方向。每个方向必须自包含，让独立研究员无需共享上下文即可开工。不要写代码方案——只定研究方向。只输出 tasks JSON：{\"tasks\":[{\"id\":\"t1\",\"title\":\"...\",\"direction\":\"...\"}]}。")
	case "worker":
		return pickPrompt(zh,
			"You are an independent researcher (Worker). Your job is to investigate ONE research direction thoroughly using read-only tools, and produce a concise research summary with concrete evidence (file:line references). Do NOT edit files.",
			"你是「研究员」——独立研究者。你的任务是只用只读工具彻底调研一个研究方向，产出简洁的研究小结，附具体证据（file:line 引用）。不要改任何文件。")
	case "synthesizer":
		return pickPrompt(zh,
			"You are a Synthesizer — a research team lead. Merge the worker reports below into one structured research report: an overview, per-direction findings, and a cross-cutting analysis with recommendations. Mark failed tasks as incomplete explicitly.",
			"你是「汇总员」——研究团队负责人。把下面的各 worker 报告合并成一份结构化研究报告：总体结论、各方向发现、跨方向综合分析建议。失败任务明确标注未完成。")
	}
	return ""
}

// handleCollabArena runs the /collab parallel research state machine to
// completion within one Run(). Idempotent: re-entering after a partial failure
// resumes from the stored Phase. On completion it renders the research report
// and clears Collab state.
func (h *CollabHall) handleCollabArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Collab == nil {
		return nil, nil
	}
	zh := msgIsChinese(state.Collab.Goal)
	goal := state.Collab.Goal

	for {
		switch state.Collab.Phase {
		case CollabDecompose:
			payload := h.runRole(ctx, "decomposer", buildDecomposerGoal(goal, zh), zh, collabDecomposerMaxIterations)
			tasks, err := parseCollabTasks(payload)
			if err != nil {
				state.Collab = nil
				return nil, fmt.Errorf("collab decompose: %w", err)
			}
			state.Collab.Tasks = tasks
			state.Collab.Phase = CollabParallel

		case CollabParallel:
			h.runWorkers(ctx, state.Collab, zh)
			state.Collab.Phase = CollabSynthesize

		case CollabSynthesize:
			state.Collab.Report = h.runRole(ctx, "synthesizer", buildSynthesizerGoal(goal, state.Collab, zh), zh, collabSynthesizerMaxIterations)
			state.Collab.Phase = CollabDone

		case CollabDone:
			resp := h.buildCollabReport(goal, state.Collab, zh)
			h.engine.state.Collab = nil
			return resp, nil

		default:
			h.engine.state.Collab = nil
			return nil, nil
		}
	}
}

// buildDecomposerGoal instructs the Decomposer to split the goal into
// research directions, output as JSON.
func buildDecomposerGoal(goal string, zh bool) string {
	return fmt.Sprintf(pickPrompt(zh,
		"## Task\nSplit the following research goal into 2-6 non-overlapping research directions. Each direction must be self-contained: an independent researcher with read-only tools and NO shared context must be able to start from the direction text alone. Output ONLY the tasks JSON.\n\n## Research Goal\n%s",
		"## 任务\n把下面的研究目标拆成 2~6 个互不重叠的研究方向。每个方向必须自包含：一个只有只读工具、没有共享上下文的独立研究员，仅凭方向描述就能开工。只输出 tasks JSON。\n\n## 研究目标\n%s"), goal)
}

// runRole executes a harness role via the sub agent with its role prompt.
// Returns the sub-agent's Summary (the JSON payload / report) or "" on failure.
func (h *CollabHall) runRole(ctx context.Context, role, goal string, zh bool, iterations int) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "collab_phase",
			Name:   role,
			Detail: collabPhaseLabel(role, zh),
		})
	}
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: iterations,
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
		result, err := pr.RunWithPrompt(ctx, handoff, collabResearchRolePrompt(role, zh))
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

// runWorkers executes all collab tasks concurrently with a concurrency cap.
// A single worker failure is tolerated — the task is marked failed and other
// workers continue.
func (h *CollabHall) runWorkers(ctx context.Context, c *CollabState, zh bool) {
	sem := make(chan struct{}, collabMaxConcurrency)
	var wg sync.WaitGroup
	for i := range c.Tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(t *CollabTask) {
			defer wg.Done()
			defer func() { <-sem }()
			t.Status = "running"
			h.emitWorkerEvent("member_start", t.ID, t.Title, zh)
			result, err := h.runWorker(ctx, t, zh)
			if err != nil || result == "" {
				t.Status = "failed"
				t.Error = errText(err)
				if result != "" {
					t.Result = result
				}
			} else {
				t.Status = "done"
				t.Result = result
			}
			h.emitWorkerEvent("member_done", t.ID, t.Title, zh)
		}(&c.Tasks[i])
	}
	wg.Wait()
}

// runWorker executes a single research task via AgentSub with the worker role.
func (h *CollabHall) runWorker(ctx context.Context, t *CollabTask, zh bool) (string, error) {
	goal := fmt.Sprintf(pickPrompt(zh,
		"## Task\nInvestigate ONE research direction and produce a concise research summary with concrete evidence (file:line references).\n\n## Research Direction\n%s",
		"## 任务\n调研一个研究方向，产出简洁的研究小结，附具体证据（file:line 引用）。\n\n## 研究方向\n%s"), t.Direction)
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: collabWorkerMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}
	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return "", err
	}
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, collabResearchRolePrompt("worker", zh))
		if err != nil || result == nil {
			return "", err
		}
		h.engine.accumulateUsage(result.Usage)
		return result.Summary, nil
	}
	result, err := agent.Run(ctx, handoff)
	if err != nil || result == nil {
		return "", err
	}
	h.engine.accumulateUsage(result.Usage)
	return result.Summary, nil
}

// buildSynthesizerGoal assembles the Synthesizer's input: goal + all reports.
func buildSynthesizerGoal(goal string, c *CollabState, zh bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(pickPrompt(zh,
		"## Task\nMerge the worker reports below into one structured research report: an overview, per-direction findings, and a cross-cutting analysis with recommendations. Mark failed tasks as incomplete explicitly.\n\n## Research Goal\n%s\n\n## Worker Reports\n",
		"## 任务\n把下面的各 worker 报告合并成一份结构化研究报告：总体结论、各方向发现、跨方向综合分析建议。失败任务明确标注未完成。\n\n## 研究目标\n%s\n\n## 各 worker 报告\n"), goal))
	for _, t := range c.Tasks {
		sb.WriteString(fmt.Sprintf("### %s (%s) — %s\n", t.Title, t.ID, t.Status))
		if t.Result != "" {
			sb.WriteString(t.Result + "\n\n")
		}
		if t.Error != "" {
			sb.WriteString(fmt.Sprintf("(error: %s)\n\n", t.Error))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collabPhaseLabel returns a human-readable label for a harness role.
func collabPhaseLabel(role string, zh bool) string {
	switch role {
	case "decomposer":
		return pickPrompt(zh, "decomposing...", "拆解中...")
	case "synthesizer":
		return pickPrompt(zh, "synthesizing...", "汇总中...")
	case "worker":
		return pickPrompt(zh, "researching...", "调研中...")
	}
	return role
}

// emitWorkerEvent emits a member_start/member_done progress event for a worker.
func (h *CollabHall) emitWorkerEvent(eventType, taskID, title string, zh bool) {
	if h.engine.config.OnProgress == nil {
		return
	}
	h.engine.config.OnProgress(ProgressEvent{
		Type:   eventType,
		Name:   "worker-" + taskID,
		Detail: title,
	})
}

// errText returns a compact error string, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// buildCollabReport renders the final research report for the user.
// Falls back to listing task results when the synthesized report is empty.
func (h *CollabHall) buildCollabReport(goal string, c *CollabState, zh bool) *EngineResponse {
	var sb strings.Builder
	sb.WriteString(pickPrompt(zh,
		"## Parallel Research Complete\n\n",
		"## 并行研究完成\n\n"))
	sb.WriteString(fmt.Sprintf("**%s**: %s\n\n", pickPrompt(zh, "Goal", "需求"), goal))

	if c.Report != "" {
		sb.WriteString(c.Report)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString(pickPrompt(zh, "### Task Results\n\n", "### 各任务结果\n\n"))
		for _, t := range c.Tasks {
			sb.WriteString(fmt.Sprintf("#### %s (%s) — %s\n", t.Title, t.ID, t.Status))
			if t.Result != "" {
				sb.WriteString(t.Result + "\n\n")
			}
			if t.Error != "" {
				sb.WriteString(fmt.Sprintf("**(error: %s)**\n\n", t.Error))
			}
		}
	}

	sb.WriteString("---\n\n")
	sb.WriteString(pickPrompt(zh,
		"Research complete. You can ask follow-up questions or run more research.",
		"研究完成。你可以追问细节，或发起新的研究。"))

	return &EngineResponse{Summary: sb.String(), Stage: StageAct}
}
