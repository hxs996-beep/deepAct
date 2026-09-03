package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// CollabHall orchestrates the /collab pipeline.
type CollabHall struct {
	engine *Engine
}

func NewCollabHall(e *Engine) *CollabHall {
	return &CollabHall{engine: e}
}

// collabStageMaxIterations bounds each pipeline stage's sub-agent loop.
// A stage produces focused output (research/design/dev-content/review), so a
// modest cap keeps the pipeline fast while allowing tool-based grounding.
const collabStageMaxIterations = 20

// handleCollabArena runs the /collab pipeline through all stages until the
// final review output is complete, then leaves the state AwaitingConfirmation
// for the user to confirm the synthesized summary. Idempotent: completed
// stages are skipped on re-entry (partial failure / resume safety).
func (h *CollabHall) handleCollabArena(ctx context.Context) (*EngineResponse, error) {
	state := h.engine.state
	if state.Collab == nil {
		return nil, nil
	}

	zh := msgIsChinese(state.Collab.Goal)
	goal := state.Collab.Goal

	order := []struct {
		phase CollabPhase
		name  CollabStageName
	}{
		{CollabReconPhase, CollabRecon},
		{CollabDesignPhase, CollabDesign},
		{CollabDevPhase, CollabDev},
		{CollabReviewPhase, CollabReview},
	}

	for _, o := range order {
		if state.Collab.Phase <= o.phase {
			prior := renderCollabPrior(state.Collab.Stages, zh)
			content := h.runCollabStage(ctx, o.name, goal, prior, zh)
			state.Collab.Stages = append(state.Collab.Stages, CollabStage{Name: o.name, Content: content})
			state.Collab.Phase = o.phase + 1
		}
	}

	state.Collab.Phase = CollabAwaitingConfirmation

	// 占位响应：任务 3 会升级为 buildCollabSummary + buildCollabPrompt。
	// 此处仅返回阶段完成的确认，避免引用任务 3 才定义的函数（类型一致性）。
	return &EngineResponse{
		Summary: pickPrompt(zh,
			"Collaboration pipeline complete. Awaiting confirmation...",
			"协作流水线完成，等待确认..."),
		Stage: StageAct,
	}, nil
}

// runCollabStage executes a single pipeline stage via AgentSub with a
// role-specific system prompt injected through RunWithPrompt. prior carries
// the rendered outputs of already-completed stages ("" for recon).
func (h *CollabHall) runCollabStage(ctx context.Context, stage CollabStageName, goal, prior string, zh bool) string {
	if h.engine.config.OnProgress != nil {
		h.engine.config.OnProgress(ProgressEvent{
			Type:   "collab_stage",
			Name:   string(stage),
			Detail: collabStageLabel(stage, zh),
		})
	}

	stageGoal := buildCollabStageGoal(stage, goal, prior, zh)
	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          stageGoal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: collabStageMaxIterations,
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	agent, err := h.engine.agents.Get(AgentSub)
	if err != nil {
		return fmt.Sprintf("collab stage %s failed: %v", stage, err)
	}

	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}

	var content string
	if pr, ok := agent.(promptRunner); ok {
		result, err := pr.RunWithPrompt(ctx, handoff, collabRolePrompt(stage, zh))
		if err != nil {
			content = fmt.Sprintf("collab stage %s failed: %v", stage, err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	} else {
		result, err := agent.Run(ctx, handoff)
		if err != nil {
			content = fmt.Sprintf("collab stage %s failed: %v", stage, err)
		} else if result != nil {
			h.engine.accumulateUsage(result.Usage)
			content = result.Summary
		}
	}

	fmt.Fprintf(os.Stderr, "[collab]   stage %s done, contentLen=%d\n", stage, len(content))
	return content
}

// collabRolePrompt returns the compact role system prompt for a pipeline stage.
func collabRolePrompt(stage CollabStageName, zh bool) string {
	switch stage {
	case CollabRecon:
		return pickPrompt(zh,
			"You are a codebase scout (Recon). Your job is to locate relevant files and understand the current code.",
			"你是「侦察」——代码库侦察员。你的任务是定位相关文件、摸清当前代码现状。")
	case CollabDesign:
		return pickPrompt(zh,
			"You are a system designer (Designer). Your job is to produce a concrete technical design for the requirement.",
			"你是「设计」——系统架构师。你的任务是产出针对需求的具体技术方案。")
	case CollabDev:
		return pickPrompt(zh,
			"You are an implementation engineer (Builder). Your job is to produce concrete implementation content (code-level changes, file-by-file) for the design.",
			"你是「开发」——实现工程师。你的任务是产出具体的实现内容（逐文件的代码级改动）。")
	case CollabReview:
		return pickPrompt(zh,
			"You are an independent reviewer (Reviewer). Your job is to critically review the implementation and flag risks, bugs, or missing pieces.",
			"你是「把关」——独立评审员。你的任务是批判性审查实现，指出风险、缺陷或遗漏。")
	}
	return ""
}

// buildCollabStageGoal constructs the task prompt for a pipeline stage.
// prior is the rendered output of already-completed stages; recon ignores it
// (its job is only the initial scan), the downstream stages receive it as the
// previous stages' output to build upon.
func buildCollabStageGoal(stage CollabStageName, goal, prior string, zh bool) string {
	switch stage {
	case CollabRecon:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nScan the codebase for context relevant to the requirement. Produce a structured report: relevant files (exact paths), key code references, constraints, risks.\n\n## Requirement\n%s\n\nDo NOT propose solutions — research only.",
			"## 任务\n扫描代码库中与需求相关的上下文。产出结构化报告：相关文件（精确路径）、关键代码引用、约束、风险。\n\n## 需求\n%s\n\n不要提方案——只做调研。"), goal)
	case CollabDesign:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nBased on the codebase context, produce a concrete technical design for the requirement: approach, key design decisions, file-level changes.\n\n%s## Requirement\n%s",
			"## 任务\n基于代码库上下文，为需求产出具体技术方案：方法、关键设计决策、逐文件改动。\n\n%s## 需求\n%s"), collabPriorSection(prior, zh), goal)
	case CollabDev:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nBased on the design, produce concrete implementation content: file-by-file changes with code snippets, function signatures, and integration points.\n\n%s## Requirement\n%s",
			"## 任务\n基于设计方案，产出具体实现内容：逐文件改动 + 代码片段、函数签名、集成点。\n\n%s## 需求\n%s"), collabPriorSection(prior, zh), goal)
	case CollabReview:
		return fmt.Sprintf(pickPrompt(zh,
			"## Task\nCritically review the implementation plan. Flag bugs, risks, missing edge cases, and concrete fixes. Be specific.\n\n%s## Requirement\n%s",
			"## 任务\n批判性审查实现方案。指出 bug、风险、遗漏的边界情况，并给出具体修复建议。\n\n%s## 需求\n%s"), collabPriorSection(prior, zh), goal)
	}
	return ""
}

// collabPriorSection renders the previous-stage outputs block, or "" when no
// stages have completed yet (so the downstream goal stays clean).
func collabPriorSection(prior string, zh bool) string {
	if prior == "" {
		return ""
	}
	return fmt.Sprintf("%s\n%s\n\n", pickPrompt(zh, "## Previous Stage Outputs", "## 前序阶段产出"), prior)
}

// renderCollabPrior renders the outputs of already-completed stages as the
// "previous stage outputs" block passed to downstream stages. Each stage is
// rendered as a labeled block of label + content. Returns "" when no stage has
// completed yet (e.g. recon has no prior).
func renderCollabPrior(stages []CollabStage, zh bool) string {
	if len(stages) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, s := range stages {
		sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", collabStageLabel(s.Name, zh), s.Content))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collabStageLabel returns a human-readable label for a pipeline stage.
func collabStageLabel(stage CollabStageName, zh bool) string {
	switch stage {
	case CollabRecon:
		return pickPrompt(zh, "Recon", "侦察")
	case CollabDesign:
		return pickPrompt(zh, "Design", "设计")
	case CollabDev:
		return pickPrompt(zh, "Dev", "开发")
	case CollabReview:
		return pickPrompt(zh, "Review", "把关")
	}
	return string(stage)
}
