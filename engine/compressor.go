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
	prompt := buildArchivePrompt(state, history)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: archiveSystemPrompt},
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

const archiveSystemPrompt = `You are a context compressor for a coding agent session.
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

func buildArchivePrompt(state *TaskState, history []Message) string {
	var b strings.Builder
	b.WriteString("Compress this coding session segment:\n\n")

	if state != nil && state.Goal != "" {
		b.WriteString("Task goal: " + state.Goal + "\n\n")
	}

	// Include accumulated blocks as additional context for the archive.
	// These contain per-turn findings that should be distilled into the summary.
	if state != nil && len(state.MemoryMarkers) > 0 {
		b.WriteString("Memory markers (include these in the summary):\n")
		for _, m := range state.MemoryMarkers {
			b.WriteString("  - " + m + "\n")
		}
		b.WriteString("\n")
	}

	// Progressive summarization: extract previous archive from history and include it
	prevArchive := extractPreviousArchive(history)
	if prevArchive != "" {
		b.WriteString("Previous archive summary (extend this, do NOT repeat):\n")
		b.WriteString(prevArchive + "\n\n")
	}

	b.WriteString("New conversation to compress:\n")
	for _, msg := range history {
		if strings.HasPrefix(msg.Content, "[SESSION ARCHIVE]") {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}

	if state != nil && len(state.Decisions) > 0 {
		b.WriteString("\nPreviously recorded decisions:\n")
		for _, d := range state.Decisions {
			b.WriteString("- " + d.Text + "\n")
		}
	}
	if state != nil && len(state.OpenQuestions) > 0 {
		b.WriteString("\nPreviously recorded open issues:\n")
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
	prompt := buildModelArchivePrompt(goal, history)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req := ModelRequest{
		Model: c.flashModelName,
		Messages: []ModelMessage{
			{Role: "system", Content: archiveSystemPrompt},
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

func buildModelArchivePrompt(goal string, history []ModelMessage) string {
	var b strings.Builder
	b.WriteString("Compress this coding session segment:\n\n")
	if goal != "" {
		b.WriteString("Task goal: " + goal + "\n\n")
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
		b.WriteString("Previous archive summary (extend this, do NOT repeat):\n")
		b.WriteString(prevArchive + "\n\n")
	}
	b.WriteString("New conversation to compress:\n")
	for _, msg := range history {
		if strings.HasPrefix(msg.Content, "[SESSION ARCHIVE]") {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, msg.Content))
	}
	return b.String()
}
