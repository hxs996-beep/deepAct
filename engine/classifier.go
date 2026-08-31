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
// thinking patterns. Uses keyword matching - no LLM call.
// Used as a guard when tool calls exist alongside content text:
// the model sometimes outputs intent ("Let me...", "让我...") even when
// it also emits tool calls. This text is noise - tool results provide context.
//
// Only a PURE intent utterance is discarded: a single clause that LEADS with
// an intent marker and contains no sentence break (comma/period/newline).
// Anything with a break usually carries a real conclusion, which must not be
// dropped - clearing it produced empty assistant content surfaced as a fake
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

// IntentCheck bundles the information the judge needs to classify user intent.
type IntentCheck struct {
	Goal    string // current Run's user goal (e.state.Goal)
	Message string // user's latest message
}

// IntentJudge classifies a user message relative to the current goal into
// IntentAnalyze, IntentContinue, or IntentNewTopic. Interface for testability;
// *IntentClassifier is the production impl.
type IntentJudge interface {
	Classify(ctx context.Context, check IntentCheck) (UserIntent, error)
}

// IntentClassifier reuses the ConclusionClassifier's Complete + JsonMode
// pattern with a flash model to control cost.
type IntentClassifier struct {
	model          ModelClient
	flashModelName string
	isChinese      bool
}

func NewIntentClassifier(model ModelClient, flashModelName string, isChinese bool) *IntentClassifier {
	return &IntentClassifier{model: model, flashModelName: flashModelName, isChinese: isChinese}
}

// Classify returns IntentAnalyze / IntentContinue / IntentNewTopic;
// err on LLM call or JSON parse failure (caller falls back conservatively).
func (c *IntentClassifier) Classify(ctx context.Context, check IntentCheck) (UserIntent, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var prompt string
	if c.isChinese {
		prompt = fmt.Sprintf("目标：%s\n\n用户消息：%s", check.Goal, check.Message)
	} else {
		prompt = fmt.Sprintf("Goal: %s\n\nUser message: %s", check.Goal, check.Message)
	}
	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickIntentPrompt(c.isChinese)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		MaxTokens:   64,
		JsonMode:    true,
	}
	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("intent classify: %w", err)
	}
	return parseIntentJSON(resp.Message.Content)
}

// parseIntentJSON extracts the intent verdict from the model's response.
// Mirrors parseConclusionJSON: tries direct parse, then extracts first {...}.
func parseIntentJSON(content string) (UserIntent, error) {
	content = strings.TrimSpace(content)
	var out struct {
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(content), &out); err == nil {
		return intentFromString(out.Intent)
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &out); err == nil {
			return intentFromString(out.Intent)
		}
	}
	return 0, fmt.Errorf("parse intent response: no valid JSON in %q", content)
}

func intentFromString(s string) (UserIntent, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "analyze":
		return IntentAnalyze, nil
	case "continue":
		return IntentContinue, nil
	case "new_topic":
		return IntentNewTopic, nil
	default:
		return 0, fmt.Errorf("unrecognized intent %q", s)
	}
}

func pickIntentPrompt(zh bool) string {
	if zh {
		return intentClassifierSystemPromptZh
	}
	return intentClassifierSystemPromptEn
}

const intentClassifierSystemPromptZh = `你是一个编程助手的用户意图分类器。给定用户当前目标和用户最新消息，判断消息意图属于哪一类。

analyze：用户仅要求分析、解释、排查、检查，不要求修改代码。即使用户引用了之前分析过的内容（如"看下第2点"、"检查之前的方案是否有兜底"），只要消息的核心动作是查看/检查/确认而非修改，就归为 analyze。
continue：用户继续当前目标的已有工作，且明确要求追加、修改、验证、优化之前的代码或内容。注意：仅引用之前的工作但只要求查看/检查（不含修改动词）的，归为 analyze 而非 continue。
new_topic：用户开启与当前目标无关的新任务。

只输出 JSON：{"intent": "analyze" 或 "continue" 或 "new_topic"}。`

const intentClassifierSystemPromptEn = `You are a user-intent classifier for a coding agent. Given the user's current goal and the user's latest message, classify the message intent.

analyze: the user only asks for analysis, explanation, investigation, or inspection - no code changes requested. Even if the user references prior work items (e.g., "check point 2", "see if the previous approach has fallback"), as long as the core action is to view/check/verify rather than modify, classify as analyze.
continue: the user continues existing work on the current goal AND explicitly requests adding to, modifying, verifying, or optimizing prior code or content. Note: referencing prior work but only asking to view/check (without modification verbs) is analyze, not continue.
new_topic: the user starts a new task unrelated to the current goal.

Output JSON only: {"intent": "analyze" or "continue" or "new_topic"}.`
