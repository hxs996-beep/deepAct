package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dlog "github.com/deepact/deepact/internal/log"
)

var turnLog = dlog.New("[turn] ")

type TurnResult struct {
	Done         bool
	Blocked      bool
	BlockedBy    string
	Questions    []string
	FinishReason string
	LastOp       string // "toolName:path" for loop detection, empty if irrelevant
}

func (e *Engine) executeTurn(ctx context.Context) (TurnResult, error) {
	if e.state == nil {
		return TurnResult{}, fmt.Errorf("state is nil")
	}

	if e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "thinking", Name: "deepact", Detail: "analyzing..."})
	}

	if e.compressor != nil && e.context != nil {
		msgs := e.context.Build(e.state, e.history, nil)
		tokens := e.context.EstimateTokens(msgs)
		layer, should := e.compressor.ShouldCompress(tokens, e.config.MaxContextTokens)
		if should {
			compacted, err := e.compressor.Compress(layer, e.state, e.history)
			if err == nil {
				e.history = compacted
			}
		}
	}

	messages := e.context.Build(e.state, e.history, nil)

	// Append pinned messages (skill activations, etc.) at the very end
	// for highest recency attention. Clear after first use so subsequent
	// turns within the same Run() call don't repeat them.
	for _, pm := range e.pendingPinnedMessages {
		messages = append(messages, ModelMessage{Role: "user", Content: pm})
	}
	e.pendingPinnedMessages = nil

	modelName := e.config.ModelName
	if e.router != nil {
		rctx := RouteContext{
			ConsecutiveFails: e.state.ConsecutiveFailures,
			EditScopeFiles:   e.state.EditScopeFiles,
			IsReadOnly:       false,
		}
		decision := e.router.SelectModel(rctx)
		modelName = decision.Model
	}

	req := ModelRequest{
		Model:     modelName,
		Messages:  messages,
		Tools:     e.toolSpecsWithHandoff(),
		MaxTokens: 8192,
	}
	stream, err := e.model.Stream(ctx, req)
	if err != nil {
		turnLog.Printf("stream model err: %v", err)
		e.state.ConsecutiveFailures++
		// Graceful degradation: don't crash the session on transient API errors.
		// The caller (Run) will see Blocked=true and return to the user.
		return TurnResult{
			Blocked:      true,
			BlockedBy:    "model_error",
			Questions:    []string{fmt.Sprintf("LLM API error: %v. Please check your connection and API key, then try again.", err)},
			FinishReason: "model_error",
		}, nil
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	var toolCalls []ModelToolCall
	var finish string
	var lastUsage *ModelUsage
	for chunk := range stream {
		if chunk.Err != nil {
			turnLog.Printf("stream chunk err: %v", chunk.Err)
			e.state.ConsecutiveFailures++
			return TurnResult{
				Blocked:      true,
				BlockedBy:    "stream_error",
				Questions:    []string{fmt.Sprintf("Stream error: %v. The connection was interrupted. Please try again.", chunk.Err)},
				FinishReason: "stream_error",
			}, nil
		}
		if chunk.Delta != "" {
			contentBuilder.WriteString(chunk.Delta)
		}
		if chunk.ReasoningDelta != "" {
			reasoningBuilder.WriteString(chunk.ReasoningDelta)
		}
		if len(chunk.ToolCalls) > 0 {
			toolCalls = chunk.ToolCalls
		}
		if chunk.FinishReason != "" {
			finish = chunk.FinishReason
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
	}

	if lastUsage != nil && e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "usage", Usage: lastUsage, ModelName: modelName})
	}

	content := contentBuilder.String()
	reasoning := reasoningBuilder.String()

	// Layer 1: If no valid structured tool_calls, try parsing DSML into tool calls
	if !hasValidToolCalls(toolCalls) && hasDSMLToolCalls(content) {
		cleaned, dsmlCalls, ok := parseDSMLToolCalls(content)
		if ok {
			content = cleaned
			toolCalls = dsmlCalls
		}
	}
	if !hasValidToolCalls(toolCalls) && hasDSMLToolCalls(reasoning) {
		_, dsmlCalls, ok := parseDSMLToolCalls(reasoning)
		if ok {
			toolCalls = dsmlCalls
		}
	}

	// Layer 2: Unconditionally strip any remaining DSML tokens from content.
	// Even if structured tool_calls exist, DSML must never reach the user.
	content = stripDSMLTokens(content)

	// Layer 3: When tool calls exist, strip intermediate thinking text from content.
	// The model sometimes outputs intent text ("Let me...", "让我...") alongside
	// DSML tool calls. This text is noise — tool results provide execution context.
	if hasValidToolCalls(toolCalls) && isIntermediateText(content) {
		content = ""
	}

	assistant := Message{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		Timestamp:        time.Now(),
	}
	// Extract explicit memory markers from model output (both content and reasoning)
	if markers := extractRememberMarkers(content); len(markers) > 0 {
		e.state.MemoryMarkers = append(e.state.MemoryMarkers, markers...)
	}
	if markers := extractRememberMarkers(reasoning); len(markers) > 0 {
		e.state.MemoryMarkers = append(e.state.MemoryMarkers, markers...)
	}

	if !hasValidToolCalls(toolCalls) {
		e.history = append(e.history, assistant)
		if finish == "length" {
			e.history = append(e.history, Message{Role: "user", Content: "继续", Timestamp: time.Now()})
			return TurnResult{Done: false, FinishReason: finish}, nil
		}
		return TurnResult{Done: true, FinishReason: finish}, nil
	}

	assistant.ToolCalls = make([]MessageToolCall, 0, len(toolCalls))
	calls := make([]ToolCallRequest, 0, len(toolCalls))
	for _, call := range toolCalls {
		if call.Function.Name == "" {
			continue
		}
		assistant.ToolCalls = append(assistant.ToolCalls, MessageToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
		args := json.RawMessage(call.Function.Arguments)
		calls = append(calls, ToolCallRequest{ID: call.ID, Name: call.Function.Name, Input: args})
	}
	if len(calls) == 0 {
		e.history = append(e.history, assistant)
		return TurnResult{Done: true, FinishReason: finish}, nil
	}

	// Edit plan guard: before executing any edit/write calls for the first time
	// in this Run(), block and present the agent's understanding + proposed changes
	// to the user for approval.
	if e.pendingEditPlan == nil && !e.planConfirmed {
		var editCalls []ToolCallRequest
		for _, call := range calls {
			if call.Name == "edit" || call.Name == "write" {
				editCalls = append(editCalls, call)
			}
		}
		if len(editCalls) > 0 {
			plan := &PendingEditPlan{
				Reasoning: assistant.Content,
				Calls:     calls, // store ALL calls (read, edit, write, bash, handoff, etc.)
				State:     cloneTaskState(e.state),
			}
			for _, c := range editCalls {
				action := buildEditAction(c)
				plan.Edits = append(plan.Edits, action)
			}
			e.pendingEditPlan = plan

			// Build a rich plan summary for the user
			zh := msgIsChinese(e.history[0].Content) // check first user message language
			planSummary := formatEditPlanSummary(plan, zh)

			// Add assistant (with tool_calls) first, then tool messages to close IDs.
			e.history = append(e.history, assistant)
			for _, c := range calls {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Blocked: " + planSummary,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{
				Blocked:    true,
				BlockedBy:  GuardAskUser,
				Questions:  []string{planSummary},
				FinishReason: finish,
			}, nil
		}
	}

	for _, call := range calls {
		// Check loop guard: same (tool, path) repeated → block to prevent cycles
		if e.guards.loop != nil {
			loopAction := e.guards.loop.Check(call)
			if loopAction.Type != GuardAllow {
				e.history = append(e.history, assistant)
				for _, c := range calls {
					e.history = append(e.history, Message{
						Role:       "tool",
						ToolCallID: c.ID,
						Content:    "Blocked: " + loopAction.Message,
						Timestamp:  time.Now(),
					})
				}
				return TurnResult{Blocked: true, BlockedBy: loopAction.Type, Questions: []string{loopAction.Message}}, nil
			}
		}

		scopeAction := e.guards.scope.CheckTool(call, e.state)
		if scopeAction.Type != GuardAllow {
			// If blocked due to dangerous bash command, store it as pending user confirmation
			if call.Name == "bash" {
				e.state.PendingDangerousCmd = e.guards.scope.DangerousPending()
			}
			// Add assistant (with tool_calls) first, then tool messages to close IDs.
			// DeepSeek requires: assistant(tool_calls) must be followed by tool messages.
			e.history = append(e.history, assistant)
			for _, c := range calls {
				e.history = append(e.history, Message{
					Role:       "tool",
					ToolCallID: c.ID,
					Content:    "Blocked: " + scopeAction.Message,
					Timestamp:  time.Now(),
				})
			}
			return TurnResult{Blocked: true, BlockedBy: scopeAction.Type, Questions: []string{scopeAction.Message}}, nil
		}
	}
	e.history = append(e.history, assistant)

	// Separate handoff calls from regular tool calls
	var handoffCalls []ToolCallRequest
	var regularCalls []ToolCallRequest
	for _, call := range calls {
		if call.Name == HandoffToolName {
			handoffCalls = append(handoffCalls, call)
		} else {
			regularCalls = append(regularCalls, call)
		}
	}

	// Execute handoff calls (sub-agents)
	for _, call := range handoffCalls {
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_start", Name: "handoff", Detail: summarizeArgs(call.Input)})
		}
		result := e.executeHandoff(ctx, call)
		if e.config.OnProgress != nil {
			e.config.OnProgress(ProgressEvent{Type: "agent_done", Name: "handoff", Detail: briefDigest(result.Digest)})
		}
		toolMessage := Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()}
		e.history = append(e.history, toolMessage)
	}

	// Execute regular tool calls
	if len(regularCalls) > 0 {
		for _, call := range regularCalls {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "tool_start", Name: call.Name, Detail: summarizeArgs(call.Input)})
			}
		}
		toolResults := e.tools.Execute(ToolExecContext{WorkDir: e.config.WorkDir, SessionID: e.config.SessionID, TurnNumber: e.state.TurnNumber}, regularCalls)
		for _, result := range toolResults {
			if e.config.OnProgress != nil {
				e.config.OnProgress(ProgressEvent{Type: "tool_done", Name: result.ToolName, Detail: briefDigest(result.Digest), FullDetail: result.Digest})
			}
			toolMessage := Message{Role: "tool", ToolCallID: result.ToolCallID, Content: result.Digest, Timestamp: time.Now()}
			e.history = append(e.history, toolMessage)
		}
		e.updateTaskStateFromTools(regularCalls, toolResults)
	}

	result := TurnResult{Done: false, FinishReason: finish}
	// Record the first operation for loop detection.
	// lastOp includes a content-based hash so different edits on the same file,
	// or different reads of the same file, are recognized as distinct operations.
	for _, c := range regularCalls {
		path := extractPathFromArgs(c.Input)
		if path == "" {
			continue
		}
		result.LastOp = c.Name + ":" + path + "#" + contentSignature(c.Input)
		break
	}
	return result, nil
}

// toolSpecsWithHandoff returns the tool specs list with the handoff_to_agent tool appended.
func (e *Engine) toolSpecsWithHandoff() []ModelTool {
	specs := e.tools.Specs()
	specs = append(specs, handoffToolSpec())
	return specs
}

// executeHandoff processes a handoff_to_agent tool call from the main agent loop.
func (e *Engine) executeHandoff(ctx context.Context, call ToolCallRequest) ToolResult {
	if e.agents == nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     "no agent registry configured",
		}
	}

	var params HandoffToAgentParams
	if err := json.Unmarshal(call.Input, &params); err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("invalid handoff params: %v", err),
		}
	}

	agent, err := e.agents.Get(AgentID(params.Agent))
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent not found: %s - %v", params.Agent, err),
		}
	}

	// Auto-inject unified codebase context for search-type agents,
	// matching the three-layer context used in conference planning.
	searchAgents := map[string]bool{"code_searcher": true, "searcher": true, "brainstorm": true}
	if searchAgents[params.Agent] {
		var repoMapSymbols string
		if r, ok := e.context.(repoMapProvider); ok {
			repoMapSymbols = r.RepoMapContent()
		}
		codebaseCtx := buildCodebaseContext(e.config.WorkDir, params.Goal, repoMapSymbols)
		if codebaseCtx != "" {
			if params.Context != "" {
				params.Context = codebaseCtx + "\n\n" + params.Context
			} else {
				params.Context = codebaseCtx
			}
		}
	}

	handoff := Handoff{
		Agent:       AgentID(params.Agent),
		Goal:        params.Goal,
		Context:     params.Context,
		Tools:       params.Tools,
		Constraints: params.Constraints,
		Depth:       0, // main engine starts at depth 0
	}

	// Inject matched skill content into sub-agent context
	if e.matchedSkillsContent != "" {
		if handoff.Context != "" {
			handoff.Context = e.matchedSkillsContent + "\n\n" + handoff.Context
		} else {
			handoff.Context = e.matchedSkillsContent
		}
	}

	result, err := agent.Run(ctx, handoff)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			ToolName:   HandoffToolName,
			Status:     "error",
			Digest:     fmt.Sprintf("agent error: %v", err),
		}
	}

	if result.Usage != nil && e.config.OnProgress != nil {
		e.config.OnProgress(ProgressEvent{Type: "usage", Usage: result.Usage})
	}

	digest := formatHandoffResult(result)
	return ToolResult{
		ToolCallID: call.ID,
		ToolName:   HandoffToolName,
		Status:     "ok",
		Digest:     digest,
	}
}

func summarizeArgs(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if cmd, ok := m["command"].(string); ok {
		return cmd
	}
	if path, ok := m["path"].(string); ok {
		return path
	}
	if pattern, ok := m["pattern"].(string); ok {
		return pattern
	}
	return ""
}

func briefDigest(digest string) string {
	if len(digest) == 0 {
		return ""
	}
	lines := strings.SplitN(digest, "\n", 2)
	first := lines[0]
	if len(first) > 80 {
		first = first[:80] + "..."
	}
	lineCount := strings.Count(digest, "\n")
	if lineCount > 1 {
		return fmt.Sprintf("%s (%d lines)", first, lineCount)
	}
	return first
}

func (e *Engine) updateTaskStateFromTools(calls []ToolCallRequest, results []ToolResult) {
	if e.state == nil {
		return
	}
	for i, call := range calls {
		path := extractPathFromArgs(call.Input)
		if path == "" {
			continue
		}
		switch call.Name {
		case "edit", "write":
			if !containsString(e.state.ModifiedFiles, path) {
				e.state.ModifiedFiles = append(e.state.ModifiedFiles, path)
			}
			e.state.EditScopeFiles = len(e.state.ModifiedFiles)
			addToWorkingSet(e.state, path, "modified")
		case "read":
			addToWorkingSet(e.state, path, "read")
		case "grep", "glob":
			if i < len(results) && results[i].Status == "ok" {
				addToWorkingSet(e.state, path, "searched")
			}
		}
	}
}

func (e *Engine) updateGoalFromFirstMessage(userMsg string) {
	if e.state != nil && e.state.Goal == "" {
		e.state.Goal = userMsg
	}
}

func extractPathFromArgs(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	if p, ok := m["path"].(string); ok {
		return p
	}
	if p, ok := m["file_path"].(string); ok {
		return p
	}
	if p, ok := m["filePath"].(string); ok {
		return p
	}
	return ""
}

// contentSignature returns a short hash of the tool call's input arguments,
// used to distinguish operations with different content on the same file.
// The hash is derived from content-bearing fields (pattern, old_string, content)
// rather than the full JSON, so trivial changes like path or filename don't
// collapse the signature.
func contentSignature(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	// Collect content-bearing fields only
	var parts []string
	for _, key := range []string{"pattern", "old_string", "new_string", "content", "command", "symbol"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				parts = append(parts, key+"="+s)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	combined := strings.Join(parts, "&")
	h := sha256.Sum256([]byte(combined))
	return fmt.Sprintf("%x", h[:4])
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func addToWorkingSet(state *TaskState, path string, notes string) {
	for i, f := range state.WorkingSet.Files {
		if f.Path == path {
			state.WorkingSet.Files[i].Notes = notes
			return
		}
	}
	state.WorkingSet.Files = append(state.WorkingSet.Files, FileRef{Path: path, Notes: notes})
}

// buildEditAction extracts a human-readable description of a proposed edit from a tool call.
func buildEditAction(call ToolCallRequest) PendingEditAction {
	var m map[string]interface{}
	if err := json.Unmarshal(call.Input, &m); err != nil {
		return PendingEditAction{Tool: call.Name, Path: "?", Summary: "? (parse error)"}
	}

	path, _ := m["path"].(string)
	if path == "" {
		if p, ok := m["file_path"].(string); ok {
			path = p
		}
	}

	action := PendingEditAction{Tool: call.Name, Path: path}

	if call.Name == "edit" {
		oldStr, _ := m["old_string"].(string)
		newStr, _ := m["new_string"].(string)
		action.OldText = oldStr
		action.NewText = newStr
		if oldStr != "" && newStr != "" {
			oldLen := len(oldStr)
			action.Summary = fmt.Sprintf("替换 %d 个字符", oldLen)
			if newStr != oldStr {
				action.Summary = fmt.Sprintf("替换 %d 个字符 → %d 个字符", oldLen, len(newStr))
			}
		} else if pattern, ok := m["pattern"].(string); ok {
			replacement, _ := m["replacement"].(string)
			action.Summary = fmt.Sprintf("替换匹配 %q → %q", truncateStr(pattern, 50), truncateStr(replacement, 50))
		} else {
			action.Summary = "编辑文件"
		}
	} else {
		// write tool
		if content, ok := m["content"].(string); ok {
			action.NewText = content
			action.Summary = fmt.Sprintf("写入 %d 个字符", len(content))
		} else {
			action.Summary = "写入文件"
		}
	}
	return action
}

// formatEditPlanSummary builds a user-facing summary of the agent's proposed changes.
func formatEditPlanSummary(plan *PendingEditPlan, zh bool) string {
	var sb strings.Builder
	if zh {
		sb.WriteString("## AI 理解了以下内容，并提出了修改方案\n\n")
	} else {
		sb.WriteString("## AI has analyzed the task and proposes the following changes\n\n")
	}

	// Agent's reasoning
	reasoning := plan.Reasoning
	if reasoning != "" {
		if len(reasoning) > 500 {
			reasoning = reasoning[:500] + "..."
		}
		if zh {
			sb.WriteString("### AI 的理解\n")
		} else {
			sb.WriteString("### AI's Understanding\n")
		}
		sb.WriteString(reasoning)
		sb.WriteString("\n\n")
	}

	// Proposed changes
	if zh {
		sb.WriteString("### 计划修改\n\n")
	} else {
		sb.WriteString("### Proposed Changes\n\n")
	}
	for i, edit := range plan.Edits {
		sb.WriteString(fmt.Sprintf("%d. **%s** `%s`", i+1, edit.Tool, edit.Path))
		if edit.Summary != "" {
			sb.WriteString(fmt.Sprintf(" — %s", edit.Summary))
		}
		sb.WriteString("\n")
	}

	if zh {
		sb.WriteString("\n是否同意这个方案？输入 **确认** 来执行，或输入其他内容让 AI 调整。\n")
	} else {
		sb.WriteString("\nDo you approve this plan? Type **yes** to proceed, or describe what needs to change.\n")
	}
	return sb.String()
}

// cloneTaskState creates a shallow-but-safe copy of TaskState for snapshotting.
func cloneTaskState(s *TaskState) *TaskState {
	if s == nil {
		return nil
	}
	data, _ := json.Marshal(s)
	var clone TaskState
	json.Unmarshal(data, &clone)
	return &clone
}
