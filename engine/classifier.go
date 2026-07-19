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

// hasTrailingNextStepIntent checks whether the last sentence of text indicates
// a next-step intention (e.g., "让我精读这些关键区域。"). This is a deterministic
// heuristic used as a pre-check in StalledNarrationHook before calling the LLM
// classifier: if the text ends with a clear next-step plan, it is NOT a
// conclusion, and the hook should block without the cost and uncertainty of an
// LLM call.
//
// Two tiers of detection:
//
// Tier 1 — first-person intent markers and transition words ("让我", "接下来",
// "Let me", etc.). High confidence: always block unless a summary phrase
// ("让我总结") is present.
//
// Tier 2 — bare action verbs at the start of the last sentence ("深入读取...",
// "继续查看...", "运行测试..."). Medium confidence: block only when no
// completion marker ("已", "完成", "通过", "了") is present, because those
// markers indicate the action was already carried out (past tense) and the
// sentence is likely a conclusion or partial result rather than a plan.
//
// Summary phrases like "让我总结一下" or "Let me summarize" are excluded in
// Tier 1 — they indicate conclusion, not continuation.
func hasTrailingNextStepIntent(text string) bool {
	last := lastSentence(text)
	if last == "" {
		return false
	}

	// summaryPhrases is shared by both tiers.
	summaryPhrases := []string{
		"总结", "回顾", "归纳", "概括",
		"summarize", "recap", "conclude", "conclusion", "summary",
	}
	hasSummary := false
	for _, s := range summaryPhrases {
		if strings.Contains(last, s) {
			hasSummary = true
			break
		}
	}

	// --- Tier 1: first-person intent markers + transitions ---
	markers := []string{
		"让我", "我来", "我要先", "接下来", "我先", "下面我",
		"我将", "我准备", "下面",
		"Let me", "I'll", "I will", "I need to",
		"I'm going to",
	}
	for _, m := range markers {
		if strings.HasPrefix(last, m) {
			if hasSummary {
				return false
			}
			return true
		}
	}

	// --- Tier 2: bare action verbs without completion markers ---
	// Catches patterns like "深入读取关键位置的代码" where the model states a
	// future action directly, without a first-person pronoun.
	actionStarts := []string{
		"深入", "继续", "开始",
		"查看", "读取", "检查", "分析", "运行", "验证",
		"搜索", "确认", "对比", "梳理", "精读", "细读",
		"测试", "编写", "修改", "排查", "定位", "追踪",
	}
	for _, a := range actionStarts {
		if strings.HasPrefix(last, a) {
			// Completion markers indicate the action was already done,
			// so this is likely a conclusion, not a next step.
			completionMarkers := []string{
				"已", "完成", "通过", "了",
				"done", "passed", "completed", "finished",
			}
			for _, c := range completionMarkers {
				if strings.Contains(last, c) {
					return false
				}
			}
			return true
		}
	}

	return false
}

// lastSentence extracts the last non-empty sentence from text.
// Chinese delimiters (。！？；) and newlines always split sentences.
// English delimiters (.!?;) split only when followed by whitespace or end,
// to avoid splitting on periods inside file paths like "loop.go".
func lastSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		if isSentDelim(runes, i) {
			s := strings.TrimSpace(string(runes[i+1:]))
			s = strings.Trim(s, "。.！!？?；;\n ")
			if s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(text)
}

// isSentDelim reports whether runes[i] is a sentence delimiter.
// Chinese delimiters always qualify. English delimiters qualify only
// when followed by whitespace or end of text (to avoid splitting "loop.go").
func isSentDelim(runes []rune, i int) bool {
	r := runes[i]
	switch r {
	case '。', '！', '？', '；', '\n':
		return true
	case '.', '!', '?', ';':
		if i+1 >= len(runes) {
			return true
		}
		next := runes[i+1]
		return next == ' ' || next == '\t' || next == '\n'
	}
	return false
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

// completionMarkers lists phrases that genuinely signal a finished task or a
// summary. It is a strict subset of the old conclusionMarkers list: only
// completion signals, NOT analytical phrases like "问题出在" or "建议" which
// also appear in intermediate findings.
var completionMarkers = []string{
	// 中文强完成态。弱标记（综上/总结/总的来说/结论是）已移除 - 它们常出现在
	// 中间发现"综上，需要..."里，会误判为完成。只保留明确的完成态动词。
	"已完成", "修复完成", "任务完成", "全部完成",
	"全部通过", "测试通过", "已通过",
	"已修复", "已解决",
	"最终结论",
	// English completion
	"completed", "complete", "all tests pass", "tests pass",
	"passed", "done",
	"in conclusion",
}

// futureIntentMarkers lists phrases that signal a forward-looking or
// incomplete state. Text containing any of these is mid-task regardless of
// what the classifier says.
var futureIntentMarkers = []string{
	// 中文未来态/未完成态
	"需要", "接下来", "下一步", "将要", "准备",
	"尚未", "还没", "待办",
	// English future intent
	"need to", "going to", "next step", "i'll", "i will",
}

// hasFutureIntent reports whether text contains a forward-looking or
// incomplete-state marker. This is the hard guard against declarative
// intermediate findings like "综上，需要在 turn.go 加入校验" that slip past
// hasTrailingNextStepIntent (no leading action verb) and confuse the flash
// classifier. Independent of the classifier, so it still works when the
// classifier call fails.
func hasFutureIntent(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range futureIntentMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// hasCompletionMarker reports whether text contains an explicit completion or
// summary marker. Used by StalledNarrationHook to decide whether to trust a
// classifier "conclusion" verdict: a verdict without any completion marker is
// treated as unconfirmed and blocked one more round conservatively, avoiding
// the flash-model classifier's false positives on declarative partial answers
// ("问题出在 X，建议 Y") that read as conclusions.
func hasCompletionMarker(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range completionMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// ConclusionCheck bundles the information the classifier needs to decide
// whether the model's text-only response is a final conclusion.
type ConclusionCheck struct {
	Goal            string // user's goal for this Run()
	Text            string // model's last text-only output
	ToolCallSummary string // brief summary of tools called this Run() (e.g. "grep×3, read×2")
}

// ConclusionJudge uses a lightweight LLM call to determine whether the
// assistant's text is a final conclusion for the user's goal.
// Interface for testability; *ConclusionClassifier is the production impl.
type ConclusionJudge interface {
	IsConclusion(ctx context.Context, check ConclusionCheck) (bool, error)
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
func (c *ConclusionClassifier) IsConclusion(ctx context.Context, check ConclusionCheck) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var prompt string
	if c.isChinese {
		prompt = fmt.Sprintf("目标：%s\n", check.Goal)
		if check.ToolCallSummary != "" {
			prompt += fmt.Sprintf("\n本次已执行工具：%s\n", check.ToolCallSummary)
		}
		prompt += fmt.Sprintf("\n助手回复：%s", check.Text)
	} else {
		prompt = fmt.Sprintf("Goal: %s\n", check.Goal)
		if check.ToolCallSummary != "" {
			prompt += fmt.Sprintf("\nTools called this run: %s\n", check.ToolCallSummary)
		}
		prompt += fmt.Sprintf("\nAssistant reply: %s", check.Text)
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
	// glm-5.2 occasionally returns the JSON in reasoning_content with empty
	// Content (llm/deepseek.go:460 "model returned only reasoning_content
	// with no visible output"). Fall back to reasoning_content so the
	// classifier still works instead of failing with "no valid JSON in \"\""
	// and degrading to a classifier_error fallback.
	content := resp.Message.Content
	if strings.TrimSpace(content) == "" {
		content = resp.Message.ReasoningContent
	}
	return parseConclusionJSON(content)
}

// parseConclusionJSON extracts the conclusion verdict from the model's
// response. Some flash models (e.g. glm-5.2) ignore JsonMode and wrap the
// JSON in markdown fences (```json ... ```) or surround it with explanation
// text, causing a direct json.Unmarshal to fail with "unexpected end of JSON
// input" or "invalid character" - which in production degraded the stop hook
// to a classifier_error block on every text-only turn. This first tries a
// direct parse; on failure it extracts the first {...} substring and parses
// that. Returns an error only when no JSON object can be found.
func parseConclusionJSON(content string) (bool, error) {
	content = strings.TrimSpace(content)
	var out struct {
		Conclusion bool `json:"conclusion"`
	}
	if err := json.Unmarshal([]byte(content), &out); err == nil {
		return out.Conclusion, nil
	}
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(content[start:end+1]), &out); err == nil {
			return out.Conclusion, nil
		}
	}
	return false, fmt.Errorf("parse conclusion response: no valid JSON in %q", content)
}

func pickClassifierPrompt(zh bool) string {
	if zh {
		return conclusionClassifierSystemPromptZh
	}
	return conclusionClassifierSystemPromptEn
}

const conclusionClassifierSystemPromptZh = `你是一个编程助手的结论判定器。给定用户目标、本次已执行的工具、和助手的最新纯文本回复，判断该回复是否为对目标的最终结论或完成总结。

是结论的标准：回复完整回答了用户目标中的所有问题，包含充分的发现或结果，没有表示要继续执行其他操作。
不是结论的标准：回复只回答了部分问题、描述了将要执行的下一步操作、报告了中间发现但未给出完整结论。

只输出 JSON：{"conclusion": true 或 false}。`

const conclusionClassifierSystemPromptEn = `You are a conclusion classifier for a coding agent. Given the user's goal, the tools called this run, and the assistant's latest text-only reply, decide whether the reply is the FINAL conclusion or completion summary for the goal.

Is conclusion: the reply fully answers all questions in the user's goal, contains complete findings or results, and does not indicate any further action to be taken.
Is NOT conclusion: the reply only partially answers the goal, describes a next step to be taken, or reports intermediate findings without a complete conclusion.

Output JSON only: {"conclusion": true or false}.`

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

// buildToolCallSummary extracts a brief summary of tool calls from the
// engine's history (e.g. "grep×3, read×2"). Used to give the conclusion
// classifier context about what the model has already done this Run(),
// so it can judge whether the task is actually complete rather than
// guessing from text content alone.
func buildToolCallSummary(history []Message, startIdx int) string {
	counts := make(map[string]int)
	var order []string
	for i := startIdx; i < len(history); i++ {
		if history[i].Role == "assistant" {
			for _, tc := range history[i].ToolCalls {
				if tc.Name != "" {
					if counts[tc.Name] == 0 {
						order = append(order, tc.Name)
					}
					counts[tc.Name]++
				}
			}
		}
	}
	if len(order) == 0 {
		return ""
	}
	var parts []string
	for _, name := range order {
		n := counts[name]
		if n > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, n))
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}
