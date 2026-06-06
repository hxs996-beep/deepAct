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
	FreshTurns = 50
	AgingTurns = 24
)

func (c *CompressionOrchestrator) ShouldCompress(currentTokens, maxTokens int) (CompressionLayer, bool) {
	if maxTokens <= 0 {
		return LayerToolGovernance, false
	}
	ratio := float64(currentTokens) / float64(maxTokens)
	switch {
	case ratio >= 0.95:
		return LayerFullCompact, true
	case ratio >= 0.85:
		return LayerCodeCollapse, true
	case ratio >= 0.65:
		return LayerStaleEviction, true
	default:
		return LayerToolGovernance, false
	}
}

func (c *CompressionOrchestrator) Compress(layer CompressionLayer, state *TaskState, history []Message) ([]Message, error) {
	switch layer {
	case LayerToolGovernance:
		return history, nil
	case LayerStaleEviction:
		return c.compressAging(state, history), nil
	case LayerCodeCollapse:
		return c.compressCodeCollapse(state, history), nil
	case LayerFullCompact:
		return c.compressArchive(state, history)
	default:
		return history, nil
	}
}

func (c *CompressionOrchestrator) compressAging(state *TaskState, history []Message) []Message {
	if len(history) <= FreshTurns*3 {
		return history
	}

	freshStart := len(history) - FreshTurns*3
	if freshStart < 0 {
		freshStart = 0
	}

	// Protect the last assistant message without tool_calls from compression.
	// This is the assistant's most recent "thinking" response — it provides
	// critical context for the next turn. Compressing it loses key decisions/analysis.
	lastAssistantIdx := findLastAssistantWithoutToolCalls(history)

	compressed := make([]Message, 0, len(history))
	for i, msg := range history {
		if i >= freshStart || i == lastAssistantIdx {
			compressed = append(compressed, msg)
			continue
		}
		compressed = append(compressed, compressMessageByType(msg))
	}
	return compressed
}

// findLastAssistantWithoutToolCalls returns the index of the last assistant message
// that has no tool_calls (i.e., a pure text response). Returns -1 if none found.
func findLastAssistantWithoutToolCalls(history []Message) int {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			return i
		}
	}
	return -1
}

func compressMessageByType(msg Message) Message {
	switch msg.Role {
	case "tool":
		return compressToolMessage(msg)
	case "assistant":
		return msg // 不截断，交给 Archive 层做 Flash 模型摘要
	case "user":
		return msg
	default:
		return msg
	}
}

func compressToolMessage(msg Message) Message {
	content := msg.Content
	if len(content) <= 200 {
		return msg
	}

	toolName := inferToolName(msg)
	switch toolName {
	case "read":
		msg.Content = compressFileRead(content)
	case "grep":
		msg.Content = compressGrepResult(content)
	case "bash":
		msg.Content = compressBashOutput(content)
	case "glob":
		msg.Content = compressGlobResult(content)
	case "edit", "write":
		msg.Content = compressEditResult(content)
	default:
		// 保留完整内容 — 三层压缩（aging → code collapse → archive）
		// 已经控制 token budget，不需要硬编码截断
	}
	return msg
}

// compressCodeCollapse applies code-structure-aware compression for LayerCodeCollapse.
// For read results, it keeps only code structure lines (func/type/const/var declarations)
// instead of the first N lines. Other messages use the same aging strategy.
func (c *CompressionOrchestrator) compressCodeCollapse(state *TaskState, history []Message) []Message {
	if len(history) <= FreshTurns*3 {
		return history
	}

	freshStart := len(history) - FreshTurns*3
	if freshStart < 0 {
		freshStart = 0
	}

	// Protect the last assistant message without tool_calls from compression.
	lastAssistantIdx := findLastAssistantWithoutToolCalls(history)

	compressed := make([]Message, 0, len(history))
	for i, msg := range history {
		if i >= freshStart || i == lastAssistantIdx {
			compressed = append(compressed, msg)
			continue
		}
		// Use code-aware compression instead of generic aging
		compressed = append(compressed, compressMessageWithCodeCollapse(msg))
	}
	return compressed
}

func compressMessageWithCodeCollapse(msg Message) Message {
	switch msg.Role {
	case "tool":
		return compressToolWithCodeCollapse(msg)
	case "assistant":
		return msg // 不截断，交给 Archive 层做 Flash 模型摘要
	case "user":
		return msg
	default:
		return msg
	}
}

func compressToolWithCodeCollapse(msg Message) Message {
	content := msg.Content
	if len(content) <= 200 {
		return msg
	}

	toolName := inferToolName(msg)
	switch toolName {
	case "read":
		msg.Content = compressCodeCollapseRead(content)
	case "grep":
		msg.Content = compressGrepResult(content)
	case "bash":
		msg.Content = compressBashOutput(content)
	case "glob":
		msg.Content = compressGlobResult(content)
	case "edit", "write":
		msg.Content = compressEditResult(content)
	default:
		// 保留完整内容 — 三层压缩（aging → code collapse → archive）
		// 已经控制 token budget，不需要硬编码截断
	}
	return msg
}

// compressCodeCollapseRead extracts only code structure lines from a file read result,
// preserving type/function/const/var declarations while dropping implementation bodies.
func compressCodeCollapseRead(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= 10 {
		return content
	}

	var kept []string
	filePath := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Capture file path from first line (ReadTool format: "file: path/to/file.go")
		if strings.HasPrefix(line, "file:") || strings.HasPrefix(line, "File:") {
			filePath = line
			kept = append(kept, line)
			continue
		}
		// Keep code structure lines: Go declarations and key markers
		if isCodeStructureLine(trimmed) {
			kept = append(kept, trimmed)
			continue
		}
		// Keep closing braces of top-level blocks (helps model understand nesting)
		if trimmed == "}" {
			// Only if preceded by a non-empty line (likely a top-level close)
			if len(kept) > 0 && kept[len(kept)-1] != "" {
				kept = append(kept, "}")
			}
		}
	}

	_ = filePath // filePath captured for potential future use
	if len(kept) == 0 {
		return strings.Join(lines[:3], "\n") + fmt.Sprintf("\n... (%d lines total)", len(lines))
	}

	return strings.Join(kept, "\n") + fmt.Sprintf("\n... (%d lines total, %d structure lines)", len(lines), len(kept))
}

// isCodeStructureLine detects Go code structure declarations that should be preserved
// during code collapse compression. This lets the model see the API surface without
// implementation bodies.
func isCodeStructureLine(line string) bool {
	if line == "" {
		return false
	}
	// Top-level declarations
	declPrefixes := []string{
		"func ", "type ", "const ", "var ", "import ", "package ",
	}
	for _, p := range declPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	// Struct/interface definitions: "type X struct {" or "type X interface {"
	// The "type" prefix already catches these, but partial lines like "X struct {"
	// on continuation lines should not be caught.
	if strings.HasPrefix(line, "// ") {
		return true // keep comments that document exported symbols
	}
	return false
}

func compressFileRead(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= 10 {
		return content
	}
	header := strings.Join(lines[:10], "\n")
	return fmt.Sprintf("%s\n... (%d lines total)", header, len(lines))
}

func compressGrepResult(content string) string {
	lines := strings.Split(content, "\n")
	matchCount := 0
	files := make(map[string]bool)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		matchCount++
		if idx := strings.Index(line, ":"); idx > 0 {
			files[line[:idx]] = true
		}
	}
	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	if len(fileList) > 50 {
		fileList = fileList[:50]
		fileList = append(fileList, "...")
	}
	return fmt.Sprintf("found %d matches in [%s]", matchCount, strings.Join(fileList, ", "))
}

func compressBashOutput(content string) string {
	lines := strings.Split(content, "\n")
	first := ""
	if len(lines) > 0 {
		first = lines[0]
		if len(first) > 1000 {
			first = first[:1000] + "..."
		}
	}
	return fmt.Sprintf("%s (%d lines)", first, len(lines))
}

func compressGlobResult(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	count := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			count++
		}
	}
	return fmt.Sprintf("found %d files", count)
}

func compressEditResult(content string) string {
	return content
}

func inferToolName(msg Message) string {
	content := strings.ToLower(msg.Content)
	if strings.HasPrefix(content, "file:") || strings.Contains(content, "lines)") {
		return "read"
	}
	if strings.Contains(content, "match") && strings.Contains(content, ":") {
		return "grep"
	}
	if strings.HasPrefix(content, "found") && strings.Contains(content, "file") {
		return "glob"
	}
	if strings.Contains(content, "exit code") || strings.Contains(content, "$ ") {
		return "bash"
	}
	if strings.Contains(content, "edited") || strings.Contains(content, "wrote") {
		return "edit"
	}
	return ""
}

// findSafeSplitPoint walks backward from the end of history to find a safe split
// index that doesn't break a turn boundary. A turn ends at an assistant message
// WITHOUT tool_calls, or a user message. Splitting between an assistant-with-tool_calls
// and its tool messages would leave orphan tool messages in the fresh window.
//
// IMPORTANT: When the boundary is an assistant message without tool_calls, we walk
// further back to include its paired user message in the fresh window. This preserves
// the full "user → assistant" turn context rather than splitting the pair.
//
// Returns the index to split at (old=[:idx], fresh=[idx:]), or -1 if not found.
func findSafeSplitPoint(history []Message, minFresh int) int {
	if len(history) <= minFresh {
		return 0
	}
	// Start from the point where we'd have at least minFresh messages fresh
	start := len(history) - minFresh
	if start < 0 {
		start = 0
	}
	// Walk backward from start to find a safe boundary
	for i := start; i >= 0; i-- {
		msg := history[i]
		if msg.Role == "user" || msg.Role == "system" {
			// User/system messages are always safe boundaries
			return i + 1 // split AFTER this message
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			// Assistant without tool calls — a clean response boundary.
			// Walk further back to include its paired user message, so the
			// complete "user → assistant" turn stays in the fresh window.
			if i > 0 && history[i-1].Role == "user" {
				return i - 1 // split BEFORE the paired user, keep the pair intact
			}
			return i // split before this assistant, keep it in fresh window
		}
		// Assistant with tool_calls is NOT a safe boundary (tool messages follow)
	}
	return 0 // fallback: split at beginning
}

// Archive compression: Flash model generates structured summary,
// including accumulated blocks in the archive process.
func (c *CompressionOrchestrator) compressArchive(state *TaskState, history []Message) ([]Message, error) {
	if c.model == nil || len(history) <= FreshTurns*3 {
		return c.compressAging(state, history), nil
	}

	freshStart := findSafeSplitPoint(history, FreshTurns*3)
	if freshStart < 0 {
		return c.compressAging(state, history), nil
	}

	if lastIdx := findLastAssistantWithoutToolCalls(history); lastIdx >= 0 && lastIdx < freshStart {
		pairedUser := lastIdx
		for i := lastIdx; i >= 0; i-- {
			if history[i].Role == "user" {
				pairedUser = i
				break
			}
		}
		freshStart = pairedUser
	}

	oldHistory := history[:freshStart]
	freshHistory := history[freshStart:]

	summary, err := c.generateArchiveSummary(state, oldHistory)
	if err != nil {
		return c.compressAging(state, history), nil
	}

	// Backfill TaskState from parsed ArchiveSummary (memory回填)
	// Also create a compressed block from the summary to replace accumulated blocks.
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

		// Replace accumulated blocks with a single compressed block.
		// This keeps the prefix stable after compression — the compressed
		// block becomes the new base for further accumulation.
		compressedBlock := buildCompressedBlock(parsed)
		if compressedBlock != "" {
			state.AccumulatedBlocks = []string{compressedBlock}
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
	if state != nil && len(state.AccumulatedBlocks) > 0 {
		b.WriteString("Accumulated turn blocks (include these in the summary):\n")
		for _, block := range state.AccumulatedBlocks {
			b.WriteString(block + "\n")
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

// buildCompressedBlock creates a single compact block string from an ArchiveSummary.
// This replaces accumulated blocks after compression, becoming the new stable prefix.
func buildCompressedBlock(summary *ArchiveSummary) string {
	if summary == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Compressed Context\n")

	hasContent := false

	if summary.Goal != "" {
		b.WriteString("Goal: " + summary.Goal + "\n")
		hasContent = true
	}
	if len(summary.Decisions) > 0 {
		b.WriteString("Decisions:\n")
		for _, d := range summary.Decisions {
			b.WriteString("  - " + d + "\n")
		}
		hasContent = true
	}
	if len(summary.FilesRead) > 0 {
		b.WriteString("Files read: " + strings.Join(summary.FilesRead, ", ") + "\n")
		hasContent = true
	}
	if len(summary.FilesModified) > 0 {
		b.WriteString("Files modified: " + strings.Join(summary.FilesModified, ", ") + "\n")
		hasContent = true
	}
	if len(summary.KeyFindings) > 0 {
		b.WriteString("Key findings:\n")
		for _, kf := range summary.KeyFindings {
			b.WriteString("  - " + kf + "\n")
		}
		hasContent = true
	}
	if len(summary.OpenIssues) > 0 {
		b.WriteString("Open issues:\n")
		for _, oi := range summary.OpenIssues {
			b.WriteString("  - " + oi + "\n")
		}
		hasContent = true
	}

	if !hasContent {
		return ""
	}
	return b.String()
}

// extractPreviousArchive finds the most recent [SESSION ARCHIVE] message in history.
func extractPreviousArchive(history []Message) string {
	// Search backwards for the most recent archive
	for i := len(history) - 1; i >= 0; i-- {
		if strings.HasPrefix(history[i].Content, "[SESSION ARCHIVE]") {
			content := history[i].Content
			// Remove the prefix marker
			content = strings.TrimPrefix(content, "[SESSION ARCHIVE]")
			content = strings.TrimSpace(content)
			return content
		}
	}
	return ""
}

func shouldCollapse(content string, modified map[string]struct{}) bool {
	for path := range modified {
		if strings.Contains(content, path) {
			return false
		}
	}
	return true
}

func buildCompressionPrompt(state *TaskState, history []Message) string {
	var builder strings.Builder
	builder.WriteString("Task summary:\n")
	if state != nil {
		builder.WriteString(state.Goal)
		builder.WriteString("\nConstraints: ")
		builder.WriteString(strings.Join(state.Constraints, ", "))
	}
	builder.WriteString("\nRecent messages:\n")
	for _, msg := range history {
		builder.WriteString(msg.Role + ": " + msg.Content + "\n")
	}
	return builder.String()
}

func (c *CompressionOrchestrator) EstimateTokens(messages []ModelMessage) int {
	if c.estimator != nil {
		return c.estimator.EstimateTokens(messages)
	}
	return 0
}

// === ModelMessage variants — shared with SubAgentRunner ===

// CompressModelMessages applies the same layered compression to ModelMessage history.
// goal is used for archive prompt when no TaskState is available (e.g. sub-agents).
func (c *CompressionOrchestrator) CompressModelMessages(layer CompressionLayer, goal string, history []ModelMessage) ([]ModelMessage, error) {
	switch layer {
	case LayerToolGovernance:
		return history, nil
	case LayerStaleEviction:
		return c.compressModelAging(goal, history), nil
	case LayerCodeCollapse:
		return c.compressModelCodeCollapse(goal, history), nil
	case LayerFullCompact:
		return c.compressModelArchive(goal, history)
	default:
		return history, nil
	}
}

func (c *CompressionOrchestrator) compressModelAging(_ string, history []ModelMessage) []ModelMessage {
	if len(history) <= FreshTurns*3 {
		return history
	}

	freshStart := len(history) - FreshTurns*3
	if freshStart < 0 {
		freshStart = 0
	}

	lastAssistantIdx := findLastAssistantWithoutToolCallsModel(history)

	compressed := make([]ModelMessage, 0, len(history))
	for i, msg := range history {
		if i >= freshStart || i == lastAssistantIdx {
			compressed = append(compressed, msg)
			continue
		}
		compressed = append(compressed, compressModelMessageByType(msg))
	}
	return compressed
}

func (c *CompressionOrchestrator) compressModelCodeCollapse(_ string, history []ModelMessage) []ModelMessage {
	if len(history) <= FreshTurns*3 {
		return history
	}

	freshStart := len(history) - FreshTurns*3
	if freshStart < 0 {
		freshStart = 0
	}

	lastAssistantIdx := findLastAssistantWithoutToolCallsModel(history)

	compressed := make([]ModelMessage, 0, len(history))
	for i, msg := range history {
		if i >= freshStart || i == lastAssistantIdx {
			compressed = append(compressed, msg)
			continue
		}
		compressed = append(compressed, compressModelMessageWithCodeCollapse(msg))
	}
	return compressed
}

func (c *CompressionOrchestrator) compressModelArchive(goal string, history []ModelMessage) ([]ModelMessage, error) {
	if c.model == nil || len(history) <= FreshTurns*3 {
		return c.compressModelAging(goal, history), nil
	}

	freshStart := findSafeSplitPointModel(history, FreshTurns*3)
	if freshStart < 0 {
		return c.compressModelAging(goal, history), nil
	}

	if lastIdx := findLastAssistantWithoutToolCallsModel(history); lastIdx >= 0 && lastIdx < freshStart {
		pairedUser := lastIdx
		for i := lastIdx; i >= 0; i-- {
			if history[i].Role == "user" {
				pairedUser = i
				break
			}
		}
		freshStart = pairedUser
	}

	oldHistory := history[:freshStart]
	freshHistory := history[freshStart:]

	summary, err := c.generateModelArchiveSummary(goal, oldHistory)
	if err != nil {
		return c.compressModelAging(goal, history), nil
	}

	result := make([]ModelMessage, 0, len(freshHistory)+1)
	result = append(result, ModelMessage{
		Role:    "system",
		Content: "[SESSION ARCHIVE]\n" + summary,
	})
	result = append(result, freshHistory...)
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

	prevArchive := extractPreviousArchiveModel(history)
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

func extractPreviousArchiveModel(history []ModelMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if strings.HasPrefix(history[i].Content, "[SESSION ARCHIVE]") {
			content := history[i].Content
			content = strings.TrimPrefix(content, "[SESSION ARCHIVE]")
			content = strings.TrimSpace(content)
			return content
		}
	}
	return ""
}

func findLastAssistantWithoutToolCallsModel(history []ModelMessage) int {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			return i
		}
	}
	return -1
}

func findSafeSplitPointModel(history []ModelMessage, minFresh int) int {
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

func compressModelMessageByType(msg ModelMessage) ModelMessage {
	switch msg.Role {
	case "tool":
		return compressModelToolMessage(msg)
	case "assistant":
		return msg // 不截断，交给 Archive 层做 Flash 模型摘要
	case "user":
		return msg
	default:
		return msg
	}
}

func compressModelToolMessage(msg ModelMessage) ModelMessage {
	content := msg.Content
	if len(content) <= 200 {
		return msg
	}

	toolName := inferModelToolName(msg)
	switch toolName {
	case "read":
		msg.Content = compressFileRead(content)
	case "grep":
		msg.Content = compressGrepResult(content)
	case "bash":
		msg.Content = compressBashOutput(content)
	case "glob":
		msg.Content = compressGlobResult(content)
	case "edit", "write":
		msg.Content = compressEditResult(content)
	default:
		// 保留完整内容 — 三层压缩（aging → code collapse → archive）
		// 已经控制 token budget，不需要硬编码截断
	}
	return msg
}

func compressModelMessageWithCodeCollapse(msg ModelMessage) ModelMessage {
	switch msg.Role {
	case "tool":
		return compressModelToolWithCodeCollapse(msg)
	case "assistant":
		return msg
	case "user":
		return msg
	default:
		return msg
	}
}

func compressModelToolWithCodeCollapse(msg ModelMessage) ModelMessage {
	content := msg.Content
	if len(content) <= 200 {
		return msg
	}

	toolName := inferModelToolName(msg)
	switch toolName {
	case "read":
		msg.Content = compressCodeCollapseRead(content)
	case "grep":
		msg.Content = compressGrepResult(content)
	case "bash":
		msg.Content = compressBashOutput(content)
	case "glob":
		msg.Content = compressGlobResult(content)
	case "edit", "write":
		msg.Content = compressEditResult(content)
	default:
		// 保留完整内容 — 三层压缩（aging → code collapse → archive）
		// 已经控制 token budget，不需要硬编码截断
	}
	return msg
}

func inferModelToolName(msg ModelMessage) string {
	content := strings.ToLower(msg.Content)
	if strings.HasPrefix(content, "file:") || strings.Contains(content, "lines)") {
		return "read"
	}
	if strings.Contains(content, "match") && strings.Contains(content, ":") {
		return "grep"
	}
	if strings.HasPrefix(content, "found") && strings.Contains(content, "file") {
		return "glob"
	}
	if strings.Contains(content, "exit code") || strings.Contains(content, "$ ") {
		return "bash"
	}
	if strings.Contains(content, "edited") || strings.Contains(content, "wrote") {
		return "edit"
	}
	return ""
}

// ParseArchiveSummary extracts structured data from flash model's JSON output
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

type ArchiveSummary struct {
	Goal          string   `json:"goal"`
	Decisions     []string `json:"decisions"`
	FilesRead     []string `json:"files_read"`
	FilesModified []string `json:"files_modified"`
	KeyFindings   []string `json:"key_findings"`
	OpenIssues    []string `json:"open_issues"`
}
