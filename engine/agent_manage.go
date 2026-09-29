package engine

import (
	"context"
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

// agentJobLine renders one job as a single line. Callers must hold bgMu.
func agentJobLine(t *bgTask, now time.Time) string {
	if t == nil {
		return "unknown"
	}
	state := agentJobState(t)
	elapsed := now.Sub(t.startAt).Round(time.Second)
	// goal is model-supplied: collapse newlines so the one-job-per-line
	// contract holds (a raw \n would split one job into two lines).
	goal := strings.ReplaceAll(strings.TrimSpace(t.goal), "\n", " ")
	line := fmt.Sprintf("%s | %s | %s | %s | %s", t.id, t.agent, state, elapsed, goal)
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
		lines = append(lines, agentJobLine(e.bgTasks[id], now))
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

// --- wait_agent --------------------------------------------------------------

// wait_agent timeout bounds. The tool takes a timeout_ms argument; the model
// can neither wait forever nor spin in a zero-length wait.
const (
	defaultAgentWaitMS    = 30_000
	minAgentWaitMS        = 100
	maxAgentWaitMS        = 600_000
	agentWaitPollInterval = 250 * time.Millisecond
)

// WaitAgentParams is the argument schema of the wait_agent tool call.
// JobIDs empty means "every running job".
type WaitAgentParams struct {
	JobIDs    []string `json:"job_ids,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// clampAgentWaitMS bounds a caller-supplied timeout, defaulting when unset.
func clampAgentWaitMS(ms int) int {
	if ms <= 0 {
		return defaultAgentWaitMS
	}
	if ms < minAgentWaitMS {
		return minAgentWaitMS
	}
	if ms > maxAgentWaitMS {
		return maxAgentWaitMS
	}
	return ms
}

// waitAgentToolSpec returns the tool definition for waiting on background jobs.
func waitAgentToolSpec(zh bool) ModelTool {
	desc := "Block until one of the given background sub-agent jobs finishes, the timeout elapses, or the turn is cancelled — then report each job's status. OBSERVES only: it never consumes a result, so collect it afterwards with agent_poll(job_id). Omit job_ids to wait on every running job."
	if zh {
		desc = "阻塞等待，直到指定的后台子代理任务之一完成、超时或本轮被取消，然后报告各任务状态。只观测、不消费结果——之后仍要用 agent_poll(job_id) 取结果。省略 job_ids 表示等待所有运行中的任务。"
	}
	return ModelTool{
		Type: "function",
		Function: ModelToolFunction{
			Name:        WaitAgentToolName,
			Description: desc,
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_ids": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Job handles to wait for (e.g. bg-1). Omit to wait on every running job."
					},
					"timeout_ms": {
						"type": "integer",
						"description": "Maximum wait in milliseconds (default 30000, max 600000)."
					}
				},
				"required": []
			}`),
		},
	}
}

// processWaitAgentCalls answers every wait_agent call. It blocks the turn for up
// to the requested timeout — that is the point of the tool (the alternative is
// the model burning turns re-polling).
func (e *Engine) processWaitAgentCalls(ctx context.Context, calls []ToolCallRequest) []Message {
	var msgs []Message
	for _, call := range calls {
		if call.Name != WaitAgentToolName {
			continue
		}
		var params WaitAgentParams
		if err := json.Unmarshal(call.Input, &params); err != nil {
			msgs = append(msgs, agentToolMessage(call.ID,
				`Error: wait_agent expects {"job_ids": ["bg-1", ...], "timeout_ms": <int>}`))
			continue
		}
		msgs = append(msgs, agentToolMessage(call.ID, e.waitForAgents(ctx, params)))
	}
	return msgs
}

// waitForAgents blocks until any target job settles, the timeout elapses, or
// ctx is cancelled. It polls the done signal at agentWaitPollInterval instead of
// a select over the dynamic target set: a goroutine-per-target select would leak
// one goroutine per target after a timed-out wait.
//
// A job whose sub-agent ignores cancellation, or that is dropped from the table,
// cannot hang the wait forever: ctx and the deadline are both always in play.
func (e *Engine) waitForAgents(ctx context.Context, params WaitAgentParams) string {
	timeout := time.Duration(clampAgentWaitMS(params.TimeoutMS)) * time.Millisecond

	e.initBackgroundTasks()
	e.bgMu.Lock()
	targets := e.waitTargetsLocked(params.JobIDs)
	e.bgMu.Unlock()
	if len(targets) == 0 {
		return "No matching background jobs to wait for."
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(agentWaitPollInterval)
	defer ticker.Stop()

	reason := ""
	for reason == "" {
		if e.anyTargetSettled(targets) {
			reason = "activity"
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			reason = "timeout"
		case <-ctx.Done():
			reason = "cancelled"
		}
	}

	e.bgMu.Lock()
	var b strings.Builder
	fmt.Fprintf(&b, "wait_agent (%s):\n", reason)
	now := time.Now()
	for _, t := range targets {
		b.WriteString(agentJobLine(t, now))
		b.WriteByte('\n')
	}
	e.bgMu.Unlock()
	b.WriteString("Collect a done job's result with agent_poll(job_id).")
	return b.String()
}

// anyTargetSettled reports whether any target job reached a settled state. It
// takes bgMu itself (unlike agentJobSettled, which requires the caller to).
func (e *Engine) anyTargetSettled(targets []*bgTask) bool {
	e.bgMu.Lock()
	defer e.bgMu.Unlock()
	for _, t := range targets {
		if agentJobSettled(t) {
			return true
		}
	}
	return false
}

// waitTargetsLocked selects the jobs wait_agent observes: the named ones, or
// every running job when none were named. Callers must hold bgMu.
func (e *Engine) waitTargetsLocked(ids []string) []*bgTask {
	if len(ids) == 0 {
		out := make([]*bgTask, 0, len(e.bgTasks))
		for _, t := range e.bgTasks {
			if t.state == bgStateRunning {
				out = append(out, t)
			}
		}
		return out
	}
	out := make([]*bgTask, 0, len(ids))
	for _, id := range ids {
		if t, ok := e.bgTasks[id]; ok {
			out = append(out, t)
		}
	}
	return out
}
