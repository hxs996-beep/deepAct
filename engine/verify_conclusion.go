package engine

import (
	"context"
	"fmt"
	"strings"
)

// VerificationResult holds the contrarian verifier's assessment of whether
// the main agent's conclusions are actually supported by code evidence.
type VerificationResult struct {
	Confidence int      `json:"confidence"` // 0-100, how sure the conclusion is supported
	Supported  bool     `json:"supported"`  // whether conclusions have concrete code backing
	Issues     []string `json:"issues,omitempty"`
	Questions  []string `json:"questions,omitempty"` // clarifying questions for the user
}

// conclusionVerifyPromptEn is the English system prompt for the verifier sub-agent.
// Its default stance is contrarian: assume conclusions are NOT supported,
// requiring concrete code evidence to confirm.
// The CONFIDENCE/SUPPORTED/ISSUES/QUESTIONS prefixes are parsed by
// parseVerificationResult and MUST be preserved verbatim in both languages.
const conclusionVerifyPromptEn = `## Role
You are a conclusion verifier with a CONTRARIAN bias. Your job is to rigorously check whether the main agent's analysis conclusions are ACTUALLY SUPPORTED by the code.

## Core Principle
Default assumption: The agent's conclusions are NOT supported by code evidence. You must actively try to disprove them by reading the actual code. Only flip to "supported" when you find concrete, unambiguous evidence.

## Verification Rules (MANDATORY)
- You MUST use read/grep/glob/lsp to check the actual code — never take the agent's word for it
- If a claim says "function X does Y", verify the actual function body
- If a claim says "the code handles Z", check both the happy path AND error paths
- If you can't find evidence for a claim → mark it as unsupported
- Be specific: say exactly which claim is unsupported and why
- For issues that are genuinely unclear even after reading code → output clarifying questions for the user

## Output Format
At the end, output exactly these lines for parsing:
CONFIDENCE: <0-100>
SUPPORTED: <true|false>
ISSUES: <comma-separated list of unsupported claims, or "none">
QUESTIONS: <comma-separated list of clarifying questions for the user, or "none">

Scoring guide:
- 90-100: All claims have clear code evidence
- 70-89: Most claims supported, minor gaps
- 50-69: Some claims unsupported, needs clarification
- 0-49: Critical claims lack evidence, should NOT proceed with edits`

const conclusionVerifyPromptZh = `## 角色
你是一位带有"证伪倾向"的结论验证者。你的职责是严格核查主代理的分析结论是否**真正有代码依据**。

## 核心原则
默认假设：代理的结论没有代码依据。你必须主动尝试通过阅读真实代码来推翻它们。只有找到具体、明确的证据时，才翻转为"supported"。

## 验证规则（必须遵守）
- 必须使用 read/grep/glob/lsp 查看真实代码——绝不轻信代理的一面之词
- 若陈述称"函数 X 做了 Y"，验证真实的函数体
- 若陈述称"代码处理了 Z"，检查正常路径和错误路径
- 若找不到某条陈述的证据 → 标记为 unsupported
- 要具体：准确说明哪条陈述无依据以及原因
- 对于即使阅读代码后仍确实不清楚的问题 → 输出给用户的澄清问题

## 输出格式
在最后，精确输出以下几行用于解析：
CONFIDENCE: <0-100>
SUPPORTED: <true|false>
ISSUES: <无依据的陈述列表，逗号分隔，或 "none">
QUESTIONS: <给用户的澄清问题列表，逗号分隔，或 "none">

评分参考：
- 90-100：所有陈述都有明确代码依据
- 70-89：大部分陈述有依据，有小缺口
- 50-69：部分陈述无依据，需要澄清
- 0-49：关键陈述缺乏依据，不应继续执行编辑`

// runConclusionVerification dispatches an independent sub-agent to verify
// whether the main agent's reasoning/conclusions are backed by code evidence.
// Returns nil on error (degrade-safe: don't block on verification failure).
func (e *Engine) runConclusionVerification(ctx context.Context, reasoning string) *VerificationResult {
	agent, err := e.agents.Get(AgentSub)
	if err != nil {
		return nil
	}

	// Type-assert to get RunWithPrompt for prompt injection.
	type promptRunner interface {
		RunWithPrompt(ctx context.Context, input Handoff, extraPrompt string) (*HandoffResult, error)
	}
	pr, ok := agent.(promptRunner)
	if !ok {
		return nil
	}

	zh := e.isChinese

	// Build context: what the agent read + what it claims.
	var ctxBuilder strings.Builder

	if len(e.state.WorkingSet.Files) > 0 {
		ctxBuilder.WriteString(pickPrompt(zh, "## Files the Main Agent Read\n", "## 主代理阅读过的文件\n"))
		for _, f := range e.state.WorkingSet.Files {
			ctxBuilder.WriteString(fmt.Sprintf("- %s (%s)\n", f.Path, f.Notes))
		}
		ctxBuilder.WriteString("\n")
	}

	ctxBuilder.WriteString(pickPrompt(zh, "## Main Agent's Reasoning\n", "## 主代理的推理过程\n"))
	ctxBuilder.WriteString(reasoning)
	ctxBuilder.WriteString("\n")

	if len(e.state.MemoryMarkers) > 0 {
		ctxBuilder.WriteString(pickPrompt(zh, "\n## Key Facts the Agent Claims to Have Found\n", "\n## 代理声称已找到的关键事实\n"))
		for _, m := range e.state.MemoryMarkers {
			ctxBuilder.WriteString(fmt.Sprintf("- %s\n", m))
		}
	}

	goal := pickPrompt(zh,
		"Verify whether the main agent's conclusions are actually supported by code evidence.\n"+
			"Read the files it referenced and check each claim independently.\n\n",
		"验证主代理的结论是否真正有代码依据。\n"+
			"阅读它引用的文件，独立核查每条陈述。\n\n",
	) + ctxBuilder.String()

	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{
			Type:   "thinking",
			Name:   "verifier",
			Detail: pickPrompt(zh, "Verifying code evidence for conclusions...", "验证结论的代码依据..."),
		})
	}

	handoff := Handoff{
		Agent:         AgentSub,
		Goal:          goal,
		Tools:         []string{"read", "grep", "glob", "lsp"},
		Depth:         0,
		NoNudge:       true,
		MaxIterations: 8, // fast: verifier should not explore deep
		UserLanguage:  pickPrompt(zh, "", "中文"),
	}

	result, err := pr.RunWithPrompt(ctx, handoff, pickPrompt(zh, conclusionVerifyPromptEn, conclusionVerifyPromptZh))
	if err != nil || result == nil {
		// Degrade-safe: on error, don't block progress
		return nil
	}

	e.accumulateUsage(result.Usage)
	return parseVerificationResult(result.Summary)
}

// parseVerificationResult extracts structured data from the verifier's text output.
// Default: safe pass-through (50 confidence, supported=true) so parsing failures
// never block the user's progress.
// confidenceThreshold returns the minimum confidence required for conclusion
// verification to pass. Default: 60 if not explicitly set.
func (e *Engine) confidenceThreshold() int {
	if e.config.ConfidenceThreshold > 0 {
		return e.config.ConfidenceThreshold
	}
	return 60
}

func parseVerificationResult(content string) *VerificationResult {
	res := &VerificationResult{Confidence: 50, Supported: true}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		switch {
		case strings.HasPrefix(lower, "confidence:"):
			rest := strings.TrimSpace(trimmed[len("confidence:"):])
			if strings.HasPrefix(strings.ToLower(rest), "confidence:") {
				rest = strings.TrimSpace(rest[len("confidence:"):])
			}
			var score int
			if _, err := fmt.Sscanf(rest, "%d", &score); err == nil {
				res.Confidence = clampScore(score)
			}

		case strings.HasPrefix(lower, "supported:"):
			rest := strings.TrimSpace(trimmed[len("supported:"):])
			lowerRest := strings.ToLower(rest)
			res.Supported = lowerRest == "true" || lowerRest == "yes" || lowerRest == "1"

		case strings.HasPrefix(lower, "issues:"):
			rest := strings.TrimSpace(trimmed[len("issues:"):])
			if !strings.EqualFold(rest, "none") && rest != "" {
				for _, item := range strings.Split(rest, ",") {
					item = strings.TrimSpace(item)
					if item != "" {
						res.Issues = append(res.Issues, item)
					}
				}
			}

		case strings.HasPrefix(lower, "questions:"):
			rest := strings.TrimSpace(trimmed[len("questions:"):])
			if !strings.EqualFold(rest, "none") && rest != "" {
				for _, q := range strings.Split(rest, ",") {
					q = strings.TrimSpace(q)
					if q != "" {
						res.Questions = append(res.Questions, q)
					}
				}
			}
		}
	}

	return res
}
