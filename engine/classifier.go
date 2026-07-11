package engine

import (
	"regexp"
	"strings"
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
