package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var rememberRe = regexp.MustCompile(`<!--\s*REMEMBER:\s*(.+?)\s*-->`)

// extractRememberMarkers scans content for <!-- REMEMBER: ... --> markers.
// These are explicit memory annotations the model can use to persist important
// information across context compression.
func extractRememberMarkers(content string) []string {
	matches := rememberRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var markers []string
	for _, m := range matches {
		text := strings.TrimSpace(m[1])
		if text != "" && !seen[text] {
			seen[text] = true
			markers = append(markers, text)
		}
	}
	return markers
}

// isIntermediateText is a lightweight heuristic check for common intermediate
// thinking patterns. Uses keyword matching — no LLM call.
// Used as a guard when tool calls exist alongside content text:
// the model sometimes outputs intent ("Let me...", "让我...") even when
// it also emits tool calls. This text is noise — tool results provide context.
//
// Only a PURE intent utterance is discarded: a single clause that LEADS with
// an intent marker and contains no sentence break (comma/period/newline).
// Anything with a break usually carries a real conclusion, which must not be
// dropped — clearing it produced empty assistant content surfaced as a fake
// "完成" summary.
func isIntermediateText(text string) bool {
	if text == "" || text == "..." {
		return false
	}
	if strings.ContainsAny(text, ",，。;；\n") {
		return false
	}
	patterns := []string{
		"Let me", // "Let me verify..."
		"让我",     // "let me" (Chinese)
		"我来",     // "I'll do"
		"我要先",    // "I need to first..."
		"接下来",    // "next, I'll..."
		"我先",     // "first I'll..."
	}
	for _, p := range patterns {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// looksLikeNextStepNarration reports whether text reads as a forward-looking
// "next step" plan (the model narrating what it is about to do) rather than a
// substantive conclusion. Unlike isIntermediateText, it tolerates multi-clause
// text with punctuation — the reported stalls ("查看 X，确认 Y。") carried
// punctuation and slipped through isIntermediateText's no-break rule.
func looksLikeNextStepNarration(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || t == "..." {
		return false
	}
	t = strings.TrimLeft(t, " \t\n\r\"'`*_-#>.)]}0123456789")
	lower := strings.ToLower(t)
	for _, m := range conclusionMarkers {
		if strings.Contains(lower, m) {
			return false
		}
	}
	for _, p := range nextStepPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

var nextStepPrefixes = []string{
	"查看", "检查", "看看", "看一下", "读取", "阅读", "搜索", "查找", "查询",
	"继续", "接下来", "接着", "然后", "下一步", "现在", "首先",
	"让我", "我来", "我先", "我要", "我需要", "我会", "我将", "先看", "先读", "先检查",
	"let me", "let's", "let us", "i'll", "i will", "i'm going to", "i am going to",
	"i need to", "i should", "next", "first", "now i", "now let", "going to",
}

var conclusionMarkers = []string{
	"综上", "总结", "总的来说", "结论", "因此", "所以", "根因", "根本原因",
	"问题在于", "问题出在", "已完成", "修复完成", "任务完成", "全部通过", "建议",
	"in summary", "in conclusion", "to summarize", "therefore", "root cause",
	"the issue is", "the problem is", "i've fixed", "i have fixed", "in short",
}

// ConclusionJudge uses a lightweight LLM call to determine whether the
// assistant's text is a final conclusion for the user's goal.
// Interface for testability; *ConclusionClassifier is the production impl.
type ConclusionJudge interface {
	IsConclusion(ctx context.Context, goal, text string) (bool, error)
}

// ConclusionClassifier reuses the compressor's Complete + JsonMode pattern
// with a flash model to control cost.
type ConclusionClassifier struct {
	model          ModelClient
	flashModelName string
	isChinese      bool
}

func NewConclusionClassifier(model ModelClient, flashModelName string, isChinese bool) *ConclusionClassifier {
	return &ConclusionClassifier{model: model, flashModelName: flashModelName, isChinese: isChinese}
}

// IsConclusion returns true when text is the final conclusion/summary for goal;
// false for intermediate process, next-step plans, partial results, or todo
// statements; err on LLM call or JSON parse failure (caller should fall back
// conservatively).
func (c *ConclusionClassifier) IsConclusion(ctx context.Context, goal, text string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var prompt string
	if c.isChinese {
		prompt = fmt.Sprintf("目标：%s\n\n助手回复：%s", goal, text)
	} else {
		prompt = fmt.Sprintf("Goal: %s\n\nAssistant reply: %s", goal, text)
	}
	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickClassifierPrompt(c.isChinese)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		MaxTokens:   64,
		JsonMode:    true,
	}
	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return false, fmt.Errorf("conclusion classify: %w", err)
	}
	var out struct {
		Conclusion bool `json:"conclusion"`
	}
	if err := json.Unmarshal([]byte(resp.Message.Content), &out); err != nil {
		return false, fmt.Errorf("parse conclusion response: %w", err)
	}
	return out.Conclusion, nil
}

func pickClassifierPrompt(zh bool) string {
	if zh {
		return conclusionClassifierSystemPromptZh
	}
	return conclusionClassifierSystemPromptEn
}

const conclusionClassifierSystemPromptZh = `你是一个编程助手的结论判定器。给定用户目标和助手的最新纯文本回复，判断该回复是否为对目标的最终结论或完成总结。中间过程、下一步计划、部分结果、待办陈述都不是结论。只输出 JSON：{"conclusion": true 或 false}。`

const conclusionClassifierSystemPromptEn = `You are a conclusion classifier for a coding agent. Given the user's goal and the assistant's latest text-only reply, decide whether the reply is the FINAL conclusion or completion summary for the goal. Intermediate process, next-step plans, partial results, or pending todos are NOT conclusions. Output JSON only: {"conclusion": true or false}.`
