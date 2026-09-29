package engine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// This file implements the Run-scoped sub-agent management surface: the main
// agent's ability to observe, wait for, message, interrupt and close the
// background jobs it dispatched. All of them are ENGINE built-ins intercepted
// in executeTurn (like agent_poll / agent_resume): they never enter the tool
// executor, and sub-agents never see them (their tool set is filtered in
// sub_agent.go). Every intercepted call MUST get exactly one tool response, or
// the next request breaks the assistant(tool_calls) → tool contract.

// --- Job view primitives -----------------------------------------------------
// A bgTask's completion is a SIGNAL (the done channel), not the state field:
// a finished-but-uncollected job keeps state == bgStateRunning so the async
// outstanding cap still holds its slot until agent_poll consumes the result.

// channelClosed reports whether ch has been closed, without consuming a value.
// A nil channel is never closed.
func channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// agentJobSettled reports whether waiting on a job is pointless: it published a
// result, or it is suspended waiting for the user.
//
// Callers MUST hold bgMu: t.state is rewritten by background goroutines under
// that lock (suspendExistingJob / takeLeafForResume / takeParentForCascade /
// finishResumedJob).
func agentJobSettled(t *bgTask) bool {
	if t == nil {
		return true
	}
	return t.state == bgStateAwaitingUser || channelClosed(t.done)
}

// agentJobState returns the job's state as a bare token — "running", "done" or
// "awaiting_user" — for renderers to decorate. Keeping the token free of
// instructions means callers can compare it instead of parsing prose.
//
// Callers MUST hold bgMu (it reads t.state).
func agentJobState(t *bgTask) string {
	if t == nil {
		return "unknown"
	}
	switch {
	case t.state == bgStateAwaitingUser:
		return "awaiting_user"
	case channelClosed(t.done):
		return "done"
	default:
		return "running"
	}
}

// agentJobLineLocked renders one job as a single line. Callers must hold bgMu.
func agentJobLineLocked(t *bgTask, now time.Time) string {
	if t == nil {
		return "unknown"
	}
	state := agentJobState(t)
	elapsed := now.Sub(t.startAt).Round(time.Second)
	line := fmt.Sprintf("%s | %s | %s | %s | %s", t.id, t.agent, state, elapsed, t.goal)
	if t.resultBrief != "" {
		line += " | result: " + t.resultBrief
	}
	if state == "done" {
		line += " | collect with agent_poll"
	}
	return line
}

// agentToolMessage builds the tool-role Message answering one intercepted call.
func agentToolMessage(callID, content string) Message {
	return Message{Role: "tool", ToolCallID: callID, Content: content, Timestamp: time.Now()}
}

// renderAgentList renders every background job, in id order for stable output.
func (e *Engine) renderAgentList() string {
	e.initBackgroundTasks()
	e.bgMu.Lock()
	ids := make([]string, 0, len(e.bgTasks))
	for id := range e.bgTasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	now := time.Now()
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		lines = append(lines, agentJobLineLocked(e.bgTasks[id], now))
	}
	e.bgMu.Unlock()
	if len(lines) == 0 {
		return "No background sub-agent jobs."
	}
	return strings.Join(lines, "\n")
}

// --- list_agents -------------------------------------------------------------

// listAgentsToolSpec returns the tool definition for enumerating background jobs.
func listAgentsToolSpec(zh bool) ModelTool {
	desc := "List the background sub-agent jobs of this session with their status (running / done / awaiting_user), agent role, elapsed time and goal. Call it before dispatching more work to see what is already running."
	if zh {
		desc = "列出本会话的后台子代理任务及其状态（running / done / awaiting_user）、角色、已运行时长与目标。派发新任务前先用它查看已在运行的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        ListAgentsToolName,
			Description: desc,
			Parameters:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		},
	}
}

// processListAgentsCalls answers every list_agents call with the current table.
func (e *Engine) processListAgentsCalls(calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != ListAgentsToolName {
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.renderAgentList()))
	}
	return msgs
}
