package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type TokenEstimator interface {
	EstimateTokens(messages []ModelMessage) int
}

type CompressionOrchestrator struct {
	model          ModelClient
	estimator      TokenEstimator
	modelName      string
	flashModelName string
	userLang       string // session-locked user language, used to pick prompt language
}

func NewCompressionOrchestrator(model ModelClient, estimator TokenEstimator, modelName string) *CompressionOrchestrator {
	return &CompressionOrchestrator{
		model:          model,
		estimator:      estimator,
		modelName:      modelName,
		flashModelName: "deepseek-v4-flash",
	}
}

func (c *CompressionOrchestrator) SetFlashModelName(name string) {
	c.flashModelName = name
}

// SetUserLang sets the session-locked user language used for prompt selection.
// The same CompressionOrchestrator instance is shared by the engine and the
// sub-agent runner, so setting it once covers both compression paths.
func (c *CompressionOrchestrator) SetUserLang(lang string) {
	c.userLang = lang
}

const (
	// tailBudget is the maximum tokens of verbatim recent history to keep
	// after compression. Reasonix uses 16384 — matches the DeepSeek pricing
	// sweet spot where the cacheable prefix dominates the request.
	tailBudget = 16384

	// compactRatio triggers FullCompact at this fraction of the context window.
	// Single threshold: no incremental layers (no 65% stale eviction or 85%
	// code collapse), just one pass at 80%.
	compactRatio = 0.80
)

func (c *CompressionOrchestrator) ShouldCompress(currentTokens, maxTokens int) (CompressionLayer, bool) {
	if maxTokens <= 0 {
		return LayerToolGovernance, false
	}
	ratio := float64(currentTokens) / float64(maxTokens)
	if ratio >= compactRatio {
		return LayerFullCompact, true
	}
	return LayerToolGovernance, false
}

func (c *CompressionOrchestrator) Compress(layer CompressionLayer, state *TaskState, history []Message) ([]Message, error) {
	switch layer {
	case LayerFullCompact:
		return c.compressArchive(state, history)
	default:
		return history, nil
	}
}

func (c *CompressionOrchestrator) compressArchive(state *TaskState, history []Message) ([]Message, error) {
	if c.model == nil || len(history) <= tailBudget/1000 {
		return history, nil
	}

	freshStart := findSafeSplitPoint(history, len(history)-10)
	if freshStart < 0 {
		return history, nil
	}

	oldHistory := history[:freshStart]
	freshHistory := history[freshStart:]

	summary, err := c.generateArchiveSummary(state, oldHistory)
	if err != nil {
		return history, nil
	}

	// Backfill TaskState from parsed ArchiveSummary
	if parsed, err := ParseArchiveSummary(summary); err == nil {
		for _, d := range parsed.Decisions {
			if !containsDecisionText(state.Decisions, d) {
				state.Decisions = append(state.Decisions, Decision{
					ID:   fmt.Sprintf("d-%d", len(state.Decisions)+1),
					Text: d,
				})
			}
		}
		for _, kf := range parsed.KeyFindings {
			if !containsString(state.Assumptions, kf) {
				state.Assumptions = append(state.Assumptions, kf)
			}
		}
		for _, oi := range parsed.OpenIssues {
			if !containsString(state.OpenQuestions, oi) {
				state.OpenQuestions = append(state.OpenQuestions, oi)
			}
		}
	}

	result := make([]Message, 0, len(freshHistory)+1)
	result = append(result, Message{
		Role:      "system",
		Content:   "[SESSION ARCHIVE]\n" + summary,
		Timestamp: time.Now(),
	})
	result = append(result, freshHistory...)
	return result, nil
}

func containsDecisionText(decisions []Decision, text string) bool {
	for _, d := range decisions {
		if d.Text == text {
			return true
		}
	}
	return false
}

func (c *CompressionOrchestrator) generateArchiveSummary(state *TaskState, history []Message) (string, error) {
	prompt := buildArchivePrompt(state, history, zhFromLang(c.userLang))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickPrompt(zhFromLang(c.userLang), archiveSystemPromptEn, archiveSystemPromptZh)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		JsonMode:    true,
	}

	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("archive summary: %w", err)
	}
	return resp.Message.Content, nil
}

// archiveSystemPromptEn / archiveSystemPromptZh are the two language variants of
// the compressor system prompt. The JSON structure and field names MUST stay
// identical across both (ParseArchiveSummary decodes by key) — only the
// descriptive prose is translated.
const archiveSystemPromptEn = `You are a context compressor for a coding agent session.
Extract ONLY actionable information. Output JSON with this exact structure:
{
  "goal": "what the user wants to achieve",
  "decisions": ["confirmed decision 1", "confirmed decision 2"],
  "files_read": ["path1", "path2"],
  "files_modified": ["path1: what was changed"],
  "key_findings": ["important discovery 1", "important discovery 2"],
  "open_issues": ["unresolved problem 1"]
}
Rules:
- Keep file paths exact
- decisions = things user explicitly confirmed or chose
- key_findings = information that would be expensive to re-discover
- Omit empty arrays
- Be terse: each string should be 1 short sentence max
- Total output must be under 10000 tokens`

const archiveSystemPromptZh = `你是一个编码代理会话的上下文压缩器。
只提取可操作的信息。输出具有以下精确结构的 JSON：
{
  "goal": "用户想要实现的目标",
  "decisions": ["已确认的决策 1", "已确认的决策 2"],
  "files_read": ["path1", "path2"],
  "files_modified": ["path1: 改动了什么"],
  "key_findings": ["重要发现 1", "重要发现 2"],
  "open_issues": ["未解决的问题 1"]
}
规则：
- 文件路径保持精确
- decisions = 用户明确确认或选择的事项
- key_findings = 重新发现成本较高的信息
- 省略空数组
- 简洁：每个字符串最多 1 个短句
- 总输出必须少于 10000 token`

func buildArchivePrompt(state *TaskState, history []Message, zh bool) string {
	var b strings.Builder
	b.WriteString(pickPrompt(zh, "Compress this coding session segment:\n\n", "压缩以下编码会话片段：\n\n"))

	if state != nil && state.Goal != "" {
		b.WriteString(pickPrompt(zh, "Task goal: ", "任务目标：") + state.Goal + "\n\n")
	}

	// Include accumulated blocks as additional context for the archive.
	// These contain per-turn findings that should be distilled into the summary.
	if state != nil && len(state.MemoryMarkers) > 0 {
		b.WriteString(pickPrompt(zh, "Memory markers (include these in the summary):\n", "记忆标记（需纳入摘要）：\n"))
		for _, m := range state.MemoryMarkers {
			b.WriteString("  - " + m + "\n")
		}
		b.WriteString("\n")
	}

	// Progressive summarization: extract previous archive from history and include it
	prevArchive := extractPreviousArchive(history)
	if prevArchive != "" {
		b.WriteString(pickPrompt(zh, "Previous archive summary (extend this, do NOT repeat):\n", "先前的归档摘要（在此基础上扩展，不要重复）：\n"))
		b.WriteString(prevArchive + "\n\n")
	}

	b.WriteString(pickPrompt(zh, "New conversation to compress:\n", "待压缩的新对话：\n"))
	for _, msg := range history {
		if strings.HasPrefix(msg.Content, "[SESSION ARCHIVE]") {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	if state != nil && len(state.Decisions) > 0 {
		b.WriteString(pickPrompt(zh, "\nPreviously recorded decisions:\n", "\n先前记录的决策：\n"))
		for _, d := range state.Decisions {
			b.WriteString("- " + d.Text + "\n")
		}
	}
	if state != nil && len(state.OpenQuestions) > 0 {
		b.WriteString(pickPrompt(zh, "\nPreviously recorded open issues:\n", "\n先前记录的待解决问题：\n"))
		for _, q := range state.OpenQuestions {
			b.WriteString("- " + q + "\n")
		}
	}

	return b.String()
}

// findSafeSplitPoint walks backward from the end of history to find a safe split
// index that doesn't break a turn boundary.
func findSafeSplitPoint(history []Message, minFresh int) int {
	if len(history) <= minFresh {
		return 0
	}
	start := len(history) - minFresh
	if start < 0 {
		start = 0
	}
	for i := start; i >= 0; i-- {
		msg := history[i]
		if msg.Role == "user" || msg.Role == "system" {
			return i + 1
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			if i > 0 && history[i-1].Role == "user" {
				return i - 1
			}
			return i
		}
	}
	return 0
}

// extractPreviousArchive finds the most recent [SESSION ARCHIVE] message in history.
func extractPreviousArchive(history []Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if strings.HasPrefix(history[i].Content, "[SESSION ARCHIVE]") {
			content := strings.TrimPrefix(history[i].Content, "[SESSION ARCHIVE]")
			return strings.TrimSpace(content)
		}
	}
	return ""
}

// ParseArchiveSummary extracts structured data from flash model's JSON output.
func ParseArchiveSummary(jsonStr string) (*ArchiveSummary, error) {
	text := strings.TrimSpace(jsonStr)
	if strings.HasPrefix(text, "```") {
		end := strings.LastIndex(text, "```")
		if end > 3 {
			text = strings.TrimSpace(text[3:end])
			if idx := strings.Index(text, "\n"); idx >= 0 {
				text = strings.TrimSpace(text[idx:])
			}
		}
	}
	var summary ArchiveSummary
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		return nil, err
	}
	return &summary, nil
}

// ArchiveSummary holds structured data from a compression archive pass.
type ArchiveSummary struct {
	Goal          string   `json:"goal"`
	Decisions     []string `json:"decisions"`
	FilesRead     []string `json:"files_read"`
	FilesModified []string `json:"files_modified"`
	KeyFindings   []string `json:"key_findings"`
	OpenIssues    []string `json:"open_issues"`
}

// EstimateTokens estimates the token count for ModelMessage slices.
func (c *CompressionOrchestrator) EstimateTokens(messages []ModelMessage) int {
	if c.estimator != nil {
		return c.estimator.EstimateTokens(messages)
	}
	return 0
}

// CompressModelMessages applies compression to ModelMessage history for sub-agents.
func (c *CompressionOrchestrator) CompressModelMessages(layer CompressionLayer, goal string, history []ModelMessage) ([]ModelMessage, error) {
	switch layer {
	case LayerFullCompact:
		return c.compressModelArchive(goal, history)
	default:
		return history, nil
	}
}

func (c *CompressionOrchestrator) compressModelArchive(goal string, history []ModelMessage) ([]ModelMessage, error) {
	if c.model == nil || len(history) <= tailBudget/1000 {
		return history, nil
	}
	summary, err := c.generateModelArchiveSummary(goal, history)
	if err != nil {
		return history, nil
	}
	result := make([]ModelMessage, 0, len(history))
	result = append(result, ModelMessage{
		Role:    "system",
		Content: "[SESSION ARCHIVE]\n" + summary,
	})
	result = append(result, history...)
	return result, nil
}

func (c *CompressionOrchestrator) generateModelArchiveSummary(goal string, history []ModelMessage) (string, error) {
	prompt := buildModelArchivePrompt(goal, history, zhFromLang(c.userLang))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: pickPrompt(zhFromLang(c.userLang), archiveSystemPromptEn, archiveSystemPromptZh)},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
		JsonMode:    true,
	}
	resp, err := c.model.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("archive summary: %w", err)
	}
	return resp.Message.Content, nil
}

func buildModelArchivePrompt(goal string, history []ModelMessage, zh bool) string {
	var b strings.Builder
	b.WriteString(pickPrompt(zh, "Compress this coding session segment:\n\n", "压缩以下编码会话片段：\n\n"))
	if goal != "" {
		b.WriteString(pickPrompt(zh, "Task goal: ", "任务目标：") + goal + "\n\n")
	}
	prevArchive := ""
	for i := len(history) - 1; i >= 0; i-- {
		if strings.HasPrefix(history[i].Content, "[SESSION ARCHIVE]") {
			prevArchive = strings.TrimPrefix(history[i].Content, "[SESSION ARCHIVE]")
			prevArchive = strings.TrimSpace(prevArchive)
			break
		}
	}
	if prevArchive != "" {
		b.WriteString(pickPrompt(zh, "Previous archive summary (extend this, do NOT repeat):\n", "先前的归档摘要（在此基础上扩展，不要重复）：\n"))
		b.WriteString(prevArchive + "\n\n")
	}
	b.WriteString(pickPrompt(zh, "New conversation to compress:\n", "待压缩的新对话：\n"))
	for _, msg := range history {
		if strings.HasPrefix(msg.Content, "[SESSION ARCHIVE]") {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}
	return b.String()
}
